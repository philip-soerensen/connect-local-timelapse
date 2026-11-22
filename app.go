package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/busapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/event"
	"github.com/prusa3d/prusa-connect/sdk/storageapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// app is the module's state: settings, the on-disk store, the active
// recording sessions and the clients the host's wire gives.
type app struct {
	settings settingsStore

	// cfgMu guards store and dataDir, which configure writes on the wire
	// handler goroutine while watch and routes read them.
	cfgMu   sync.RWMutex
	store   *store
	dataDir string

	conn   *wirerpc.Conn
	access *accessapi.Client

	mu       sync.Mutex
	active   map[string]*activeSession // printer id -> recording session
	settleMu sync.Mutex                // serializes video assemblies
}

// activeSession is one in-flight recording: the store row plus the runtime
// bits (last frame time).
type activeSession struct {
	sess      session
	lastFrame time.Time
}

func newApp() *app {
	return &app{active: map[string]*activeSession{}}
}

func (a *app) getStore() *store {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.store
}

func (a *app) getDataDir() string {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.dataDir
}

// heldSettings is plugin.configure's params: every field is a pointer, so a
// key the host did not send stays nil and an explicit zero (video_crf: 0 is
// valid x264) is told apart from an absent one.
type heldSettings struct {
	Enabled       *bool  `json:"enabled"`
	VideoCRF      *int   `json:"video_crf"`
	KeepPrints    *int   `json:"keep_prints"`
	MaxFrameBytes *int64 `json:"max_frame_bytes"`
}

// configure answers plugin.configure. The host sends the manifest's defaults
// with the operator's config block merged over them.
func (a *app) configure(ctx context.Context, held heldSettings, dataDir string) error {
	cfg := defaultConfig()
	if held.Enabled != nil {
		cfg.Enabled = *held.Enabled
	}
	if held.VideoCRF != nil {
		cfg.VideoCRF = *held.VideoCRF
	}
	if held.KeepPrints != nil {
		cfg.KeepPrints = *held.KeepPrints
	}
	if held.MaxFrameBytes != nil {
		cfg.MaxFrameBytes = *held.MaxFrameBytes
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	a.settings.held.Store(cfg)
	store := newStore(dataDir)
	a.cfgMu.Lock()
	a.dataDir = dataDir
	a.store = store
	// Resolve ffmpeg once, inside the same critical section: /status reads
	// ffmpegPath on other goroutines.
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpegPath = p
	}
	a.cfgMu.Unlock()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		slog.WarnContext(ctx, "ffmpeg not found; frames will still be recorded but videos cannot be assembled (install ffmpeg on the server)")
	}
	slog.InfoContext(ctx, "configured", "data_dir", dataDir,
		"video_crf", cfg.VideoCRF, "keep_prints", cfg.KeepPrints,
		"default frame_interval_seconds", cfg.FrameIntervalSec, "default video_fps", cfg.VideoFPS)
	return nil
}

// watch waits for configure (the store), adopts prints that are already
// running, then subscribes to the event stream and dispatches events,
// retrying a refused or broken subscription after a second.
func (a *app) watch(ctx context.Context, conn *wirerpc.Conn) {
	a.conn = conn
	bus := busapi.NewClient(conn)
	files := storageapi.NewClient(conn)
	devices := deviceapi.NewClient(conn)

	// watch() starts before the host sends plugin.configure, so the store
	// may not exist yet; wait for it instead of never seeding.
	for a.getStore() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	a.seedRunningPrints(ctx, devices)

	params := busapi.SubscribeParams{Types: []string{
		event.TypeCameraSnapshotCaptured,
		event.TypeDeviceJobStarted,
		event.TypeDeviceJobSettled,
	}}

	for ctx.Err() == nil {
		err := busapi.Subscribe(ctx, conn, params, func(window busapi.Window) error {
			for _, raw := range window.Events {
				a.handleEvent(ctx, bus, files, devices, raw)
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "subscribe ended", "error", err)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
}

// seedRunningPrints opens a session for every printer whose job is running,
// for the case where the module (or the server) restarted mid-print and no
// job.started event will arrive. Sessions left "recording" on disk whose
// printer is no longer printing are settled with outcome "unknown" — always,
// even when the module is switched off, so no recording row is stranded.
func (a *app) seedRunningPrints(ctx context.Context, devices *deviceapi.Client) {
	store := a.getStore()
	if store == nil {
		return
	}
	cfg := a.settings.load()
	running := map[string]*deviceapi.Job{}
	offset := 0
	for {
		page, err := devices.List(ctx, deviceapi.ListParams{Limit: deviceapi.MaxPageLimit, Offset: offset})
		if err != nil {
			slog.WarnContext(ctx, "the device list could not be read; in-flight prints are not adopted", "error", err)
			return
		}
		for _, dev := range page.Items {
			jobs, err := devices.Jobs(ctx, deviceapi.JobsParams{ID: dev.ID})
			if err != nil {
				slog.WarnContext(ctx, "the printer's jobs could not be read", "printer", dev.ID, "error", err)
				continue
			}
			if jobs.Current != nil && jobs.Current.Ended == nil {
				running[dev.ID] = jobs.Current
			}
		}
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Total {
			break
		}
	}
	if cfg.Enabled {
		for printer, job := range running {
			if _, ok := store.activeSession(printer); !ok {
				a.startSession(ctx, printer, job.ID, job.DisplayName, job.Path, job.Started)
			}
		}
	}
	// Stale recordings are settled synchronously here on purpose: no
	// subscription is open yet, so this delays only the first subscribe.
	for _, sess := range store.recordingSessions() {
		if _, isRunning := running[sess.Printer]; !isRunning {
			slog.InfoContext(ctx, "settling a recording left over from a restart", "printer", sess.Printer, "session", sess.ID)
			a.mu.Lock()
			if cur, ok := a.active[sess.Printer]; ok && cur.sess.ID == sess.ID {
				delete(a.active, sess.Printer)
			}
			a.mu.Unlock()
			a.assembleAndFinish(ctx, sess, event.JobUnknown, time.Now())
		} else {
			a.attachActive(sess)
		}
	}
}

// handleEvent decodes one envelope and dispatches on its type.
func (a *app) handleEvent(ctx context.Context, bus *busapi.Client, files *storageapi.Client, devices *deviceapi.Client, raw json.RawMessage) {
	var env event.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.WarnContext(ctx, "an event could not be parsed", "error", err)
		return
	}
	switch env.Type {
	case event.TypeDeviceJobStarted:
		var job event.JobStarted
		if err := json.Unmarshal(env.Data, &job); err != nil {
			slog.WarnContext(ctx, "a job.started event could not be parsed", "error", err)
			return
		}
		device := strings.TrimPrefix(env.Subject, "device:")
		a.startSession(ctx, device, job.JobID, job.DisplayName, job.Path, job.Started)
	case event.TypeDeviceJobSettled:
		var job event.JobSettled
		if err := json.Unmarshal(env.Data, &job); err != nil {
			slog.WarnContext(ctx, "a job.settled event could not be parsed", "error", err)
			return
		}
		device := strings.TrimPrefix(env.Subject, "device:")
		// Settling assembles a video, which can take minutes; run it off the
		// event loop so other printers' events are not delayed behind it.
		go a.settleEvent(ctx, device, job.JobID, job.Outcome, job.Ended)
	case event.TypeCameraSnapshotCaptured:
		var snap snapshotEvent
		if err := json.Unmarshal(env.Data, &snap); err != nil {
			slog.WarnContext(ctx, "a snapshot event could not be parsed", "error", err)
			return
		}
		go a.onSnapshot(ctx, files, snap)
	}
}

// snapshotEvent is the data member of com.prusa.connect.camera.snapshot.captured.
type snapshotEvent struct {
	Camera  string    `json:"camera"`
	Printer string    `json:"printer,omitempty"`
	Storage string    `json:"storage"`
	Path    string    `json:"path"`
	Bytes   int64     `json:"bytes"`
	Taken   time.Time `json:"taken"`
}

// startSession opens a recording session for one print; frames arrive from
// the camera snapshot events while it records.
func (a *app) startSession(ctx context.Context, printer, jobID, displayName, path string, started time.Time) {
	store := a.getStore()
	if store == nil {
		return
	}
	cfg := a.settings.load()
	if !cfg.Enabled {
		return
	}
	a.mu.Lock()
	if old, ok := a.active[printer]; ok {
		if old.sess.JobID == jobID {
			a.mu.Unlock()
			return // duplicate started for the print already recording
		}
		// A new print preempts the recording one: stop its capture now and
		// assemble the old session in the background, so this handler never
		// waits on ffmpeg.
		delete(a.active, printer)
		a.mu.Unlock()
		slog.WarnContext(ctx, "a print started while another was recording; settling the old one", "printer", printer, "session", old.sess.ID)
		go a.assembleAndFinish(ctx, old.sess, event.JobUnknown, started)
		a.mu.Lock()
	}
	sess, err := store.addSession(printer, jobID, displayName, path, started)
	if err != nil {
		a.mu.Unlock()
		slog.WarnContext(ctx, "a session could not be opened", "printer", printer, "error", err)
		return
	}
	a.active[printer] = &activeSession{sess: sess}
	a.mu.Unlock()
	slog.InfoContext(ctx, "recording started", "printer", printer, "job", jobID, "session", sess.ID, "name", displayName)
}

// attachActive registers an already-on-disk recording session as active (used
// when resuming after a restart).
func (a *app) attachActive(sess session) {
	a.mu.Lock()
	a.active[sess.Printer] = &activeSession{sess: sess}
	a.mu.Unlock()
}

// settleEvent handles a job.settled: it stops capture for the matching
// active session and assembles its video. A settled event for a job that is
// not the one recording is ignored without touching the active recording.
func (a *app) settleEvent(ctx context.Context, printer, jobID, outcome string, ended time.Time) {
	a.mu.Lock()
	act, ok := a.active[printer]
	if !ok || act.sess.JobID != jobID {
		a.mu.Unlock()
		return // a stale settle for a job we never recorded
	}
	delete(a.active, printer)
	a.mu.Unlock()
	a.assembleAndFinish(ctx, act.sess, outcome, ended)
}

// assembleAndFinish counts the session's frames, assembles the video, updates
// the record, emits the ready event and prunes old records. It never touches
// the active map, so it is safe to run in the background while the next print
// on the same printer records. Assemblies are serialized.
func (a *app) assembleAndFinish(ctx context.Context, sess session, outcome string, ended time.Time) {
	store := a.getStore()
	if store == nil {
		return
	}
	// This runs on background goroutines: a panic here must mark the session
	// failed, not take the whole module process down.
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "assembly panicked", "session", sess.ID, "panic", r)
			sess.State = "failed"
			sess.Error = truncate(fmt.Sprintf("internal panic: %v", r), 500)
			if st := a.getStore(); st != nil {
				_ = st.updateSession(sess)
			}
		}
	}()
	a.settleMu.Lock()
	defer a.settleMu.Unlock()
	sess.Outcome = outcome
	sess.Ended = ended
	// Count the frames actually on disk, so the record matches what ffmpeg
	// reads; fall back to the stored counter if the directory cannot be read.
	framesDir := filepath.Join(a.getDataDir(), sessionsDir, sess.ID, framesDir)
	frames, err := countFrames(framesDir)
	if err != nil {
		slog.WarnContext(ctx, "the frames directory could not be read; using the stored count", "session", sess.ID, "error", err)
		frames = sess.Frames
	}
	sess.Frames = frames
	switch sess.Frames {
	case 0:
		sess.State = "empty"
		// If the operator picked a camera and nothing ever matched it, say
		// so instead of a bare "empty": the usual cause is a typo'd name or
		// a camera the server does not record snapshots of.
		if src, ok := store.sourceOf(sess.Printer); ok && src.Camera != "" {
			sess.Error = fmt.Sprintf("no snapshot from camera %q arrived during the print (check the camera name, or that the server records its snapshots: snapshot_every/snapshot_storage)", src.Camera)
		}
		slog.InfoContext(ctx, "recording ended with no frames", "printer", sess.Printer, "session", sess.ID)
	default:
		cfg := a.settings.load()
		out := filepath.Join(a.getDataDir(), sessionsDir, sess.ID, videoFile)
		if err := ffmpegAssemble(ctx, framesDir, out, a.fpsFor(sess.Printer), cfg.VideoCRF); err != nil {
			sess.State = "failed"
			sess.Error = truncate(err.Error(), 500)
			slog.WarnContext(ctx, "the timelapse could not be assembled", "printer", sess.Printer, "session", sess.ID, "error", err)
		} else if info, err := os.Stat(out); err == nil {
			sess.State = "done"
			sess.VideoBytes = info.Size()
			slog.InfoContext(ctx, "timelapse assembled", "printer", sess.Printer, "session", sess.ID,
				"frames", sess.Frames, "bytes", sess.VideoBytes, "outcome", outcome)
		} else {
			sess.State = "failed"
			sess.Error = truncate(err.Error(), 500)
		}
	}
	if err := store.updateSession(sess); err != nil {
		slog.WarnContext(ctx, "the session record could not be saved", "session", sess.ID, "error", err)
	}
	if sess.State == "done" {
		payload := map[string]any{
			"printer":      sess.Printer,
			"job_id":       sess.JobID,
			"session":      sess.ID,
			"timelapse":    "/timelapses/" + sess.ID,
			"video":        sess.videoURL(),
			"frames":       sess.Frames,
			"video_bytes":  sess.VideoBytes,
			"outcome":      outcome,
			"display_name": sess.DisplayName,
			"started":      sess.Started.UTC(),
			"ended":        sess.Ended.UTC(),
		}
		if a.conn != nil {
			if err := busapi.NewClient(a.conn).Emit(ctx, "ready", sess.Printer, payload); err != nil {
				slog.WarnContext(ctx, "the ready event could not be published", "error", err)
			}
		}
	}
	cfg := a.settings.load()
	store.prune(cfg.KeepPrints)
}

// intervalFor is the capture interval of one printer: the per-printer value
// saved from the Timelapse tab, or the built-in default when it has none.
func (a *app) intervalFor(printer string) time.Duration {
	n := a.settings.load().FrameIntervalSec
	if s := a.getStore(); s != nil {
		if src, ok := s.sourceOf(printer); ok && src.FrameIntervalSec > 0 {
			n = src.FrameIntervalSec
		}
	}
	return time.Duration(n) * time.Second
}

// fpsFor is the assembled video's frame rate for one printer, same fallback.
func (a *app) fpsFor(printer string) int {
	n := a.settings.load().VideoFPS
	if s := a.getStore(); s != nil {
		if src, ok := s.sourceOf(printer); ok && src.VideoFPS > 0 {
			n = src.VideoFPS
		}
	}
	return n
}

// snapReads bounds the in-flight snapshot reads: each allocates up to
// max_frame_bytes, and one goroutine per snapshot event could otherwise spike
// the memory budget on many-printer installs.
var snapReads = make(chan struct{}, 4)

// takeSlot reports whether a frame may be taken now for this printer's
// session, and if so records the time. One check-and-set under mu, so two
// concurrent captures cannot both pass the interval.
func (a *app) takeSlot(printer string, interval time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	act, ok := a.active[printer]
	if !ok {
		return false
	}
	if time.Since(act.lastFrame) < interval {
		return false
	}
	act.lastFrame = time.Now()
	return true
}

// onSnapshot saves one frame out of a stored camera snapshot while the
// printer records; when a camera is picked for the printer, only that
// camera's snapshots are saved.
func (a *app) onSnapshot(ctx context.Context, files *storageapi.Client, snap snapshotEvent) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "snapshot handling panicked", "panic", r)
		}
	}()
	store := a.getStore()
	if store == nil {
		return
	}
	cfg := a.settings.load()
	if !cfg.Enabled {
		return
	}
	printer := snap.Printer
	if printer == "" {
		// The event carries no printer: attribute the camera through the
		// configured sources (camera -> printer).
		for p, src := range store.allSources() {
			if src.Camera == snap.Camera {
				printer = p
				break
			}
		}
	}
	if printer == "" {
		return // cannot attribute the camera to a printer
	}
	if src, ok := store.sourceOf(printer); ok {
		if src.Camera != "" && src.Camera != snap.Camera {
			return // another camera of this printer; the operator picked one
		}
	}
	if !a.takeSlot(printer, a.intervalFor(printer)) {
		return
	}
	if snap.Bytes <= 0 || snap.Bytes > cfg.MaxFrameBytes {
		return
	}
	select {
	case snapReads <- struct{}{}:
		defer func() { <-snapReads }()
	case <-ctx.Done():
		return
	}
	f, err := files.Open(ctx, storageapi.Target{Storage: snap.Storage, Path: snap.Path})
	if err != nil {
		slog.WarnContext(ctx, "a snapshot could not be opened", "storage", snap.Storage, "path", snap.Path, "error", err)
		return
	}
	if f.Size() > cfg.MaxFrameBytes {
		return
	}
	buf := make([]byte, f.Size())
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		slog.WarnContext(ctx, "a snapshot could not be read", "path", snap.Path, "error", err)
		return
	}
	a.mu.Lock()
	act, ok := a.active[printer]
	a.mu.Unlock()
	if !ok {
		return
	}
	if _, err := store.saveFrame(act.sess, buf); err != nil {
		slog.WarnContext(ctx, "a frame could not be saved", "printer", printer, "error", err)
		return
	}
	slog.DebugContext(ctx, "frame saved from snapshot", "printer", printer, "camera", snap.Camera, "session", act.sess.ID)
}

// setSource validates and persists the camera input for one printer. An
// empty camera clears the source.
func (a *app) setSource(ctx context.Context, printer string, src source) error {
	store := a.getStore()
	if store == nil {
		return fmt.Errorf("connect-timelapse: no data directory granted")
	}
	return store.setSource(printer, src)
}

// countFrames counts the frame files in one frames directory.
func countFrames(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && frameNameOK.MatchString(e.Name()) {
			n++
		}
	}
	return n, nil
}

// truncate shortens s to at most n runes, rune-safe.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
