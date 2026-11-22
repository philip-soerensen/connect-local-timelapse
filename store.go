package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionsFile = "sessions.json"
	sourcesFile  = "sources.json"
	sessionsDir  = "sessions"
	framesDir    = "frames"
	videoFile    = "video.mp4"
)

// source is the per-printer camera input the module captures from. The camera
// is one the host itself snapshots (HTTP snapshot_url, RTSP stream_url, or
// frames pushed to PUT /cameras/{id}/snapshot): the module saves frames out of the
// camera.snapshot.captured events. The camera's own stream URL and snapshot
// cadence are configured on the Timelapse tab through the host's camera API,
// so the module process itself makes no outbound connections.
type source struct {
	Camera string `json:"camera"`
	// Per-printer capture settings, set from the Timelapse tab. Zero means
	// "use the built-in default" (10 s, 24 fps), never an invalid value.
	FrameIntervalSec int `json:"frame_interval_seconds,omitempty"`
	VideoFPS         int `json:"video_fps,omitempty"`
}

// publicSource is what GET /sources may answer with, with the defaults
// filled in so the tab can prefill its inputs with the effective values.
type publicSource struct {
	Camera           string `json:"camera"`
	Mode             string `json:"mode"` // always "snapshots"
	FrameIntervalSec int    `json:"frame_interval_seconds"`
	VideoFPS         int    `json:"video_fps"`
}

func (s source) public(def config) publicSource {
	interval, fps := def.FrameIntervalSec, def.VideoFPS
	if s.FrameIntervalSec > 0 {
		interval = s.FrameIntervalSec
	}
	if s.VideoFPS > 0 {
		fps = s.VideoFPS
	}
	return publicSource{Camera: s.Camera, Mode: "snapshots", FrameIntervalSec: interval, VideoFPS: fps}
}

// session is one print's timelapse record. It lives in sessions.json and its
// frames and video in sessions/<id>/.
type session struct {
	ID          string    `json:"id"`
	Seq         int64     `json:"seq"`
	Printer     string    `json:"printer"`
	JobID       string    `json:"job_id"`
	DisplayName string    `json:"display_name,omitempty"`
	Path        string    `json:"path,omitempty"`
	State       string    `json:"state"` // "recording", "done", "empty", "failed"
	Outcome     string    `json:"outcome,omitempty"`
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended,omitempty"`
	Frames      int       `json:"frames"`
	VideoBytes  int64     `json:"video_bytes,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// videoURL is the module-relative path a client plays the video from.
func (s session) videoURL() string {
	if s.VideoBytes <= 0 {
		return ""
	}
	return "/timelapses/" + s.ID + "/video"
}

var sessionIDOK = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var frameNameOK = regexp.MustCompile(`^[0-9]{9}\.jpg$`)

// store keeps the session index and the per-printer sources in the module's
// data directory, and owns the session directories on disk.
type store struct {
	dir string

	mu       sync.Mutex
	sessions []session
	sources  map[string]source // printer id -> camera input
	seq      int64
}

func newStore(dataDir string) *store {
	s := &store{dir: dataDir, sources: map[string]source{}}
	if dataDir != "" {
		s.load()
	}
	return s
}

func (s *store) load() {
	if s.dir == "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, sessionsFile)); err == nil {
		_ = json.Unmarshal(raw, &s.sessions)
	}
	for i := range s.sessions {
		if s.sessions[i].Seq > s.seq {
			s.seq = s.sessions[i].Seq
		}
	}
	if raw, err := os.ReadFile(filepath.Join(s.dir, sourcesFile)); err == nil {
		_ = json.Unmarshal(raw, &s.sources)
	}
	if s.sources == nil {
		s.sources = map[string]source{}
	}
	// Drop index rows whose directory is gone (pruned by hand, or a lost
	// disk), so the tab does not render dead links.
	kept := s.sessions[:0]
	for _, sess := range s.sessions {
		if _, err := os.Stat(filepath.Join(s.dir, sessionsDir, sess.ID)); err != nil {
			continue
		}
		kept = append(kept, sess)
	}
	s.sessions = kept
}

func (s *store) saveLocked() error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(s.sessions)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, sessionsFile), raw, 0o600)
}

func (s *store) saveSourcesLocked() error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.Marshal(s.sources)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, sourcesFile), raw, 0o600)
}

// addSession creates the session record and its directory, and returns it.
func (s *store) addSession(printer, jobID, displayName, path string, started time.Time) (session, error) {
	if s.dir == "" {
		return session{}, fmt.Errorf("connect-timelapse: no data directory granted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	sess := session{
		ID:          fmt.Sprintf("%06d_%s", s.seq, shortID(jobID)),
		Seq:         s.seq,
		Printer:     printer,
		JobID:       jobID,
		DisplayName: displayName,
		Path:        path,
		State:       "recording",
		Started:     started,
	}
	if err := os.MkdirAll(filepath.Join(s.dir, sessionsDir, sess.ID, framesDir), 0o700); err != nil {
		return session{}, err
	}
	s.sessions = append(s.sessions, sess)
	if err := s.saveLocked(); err != nil {
		return session{}, err
	}
	return sess, nil
}

// updateSession writes changed fields back to the index and disk.
func (s *store) updateSession(sess session) error {
	if s.dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions {
		if s.sessions[i].ID == sess.ID {
			s.sessions[i] = sess
			break
		}
	}
	return s.saveLocked()
}

// getSession returns a copy of one session record.
func (s *store) getSession(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.ID == id {
			return sess, true
		}
	}
	return session{}, false
}

// listSessions returns the records newest first, optionally narrowed to one
// printer.
func (s *store) listSessions(printer string) []session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []session
	for i := len(s.sessions) - 1; i >= 0; i-- {
		sess := s.sessions[i]
		if printer == "" || sess.Printer == printer {
			out = append(out, sess)
		}
	}
	return out
}

// activeSession returns the recording session of one printer, if any.
func (s *store) activeSession(printer string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sessions) - 1; i >= 0; i-- {
		if s.sessions[i].Printer == printer && s.sessions[i].State == "recording" {
			return s.sessions[i], true
		}
	}
	return session{}, false
}

// recordingSessions returns every session still recording.
func (s *store) recordingSessions() []session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []session
	for _, sess := range s.sessions {
		if sess.State == "recording" {
			out = append(out, sess)
		}
	}
	return out
}

// saveFrame writes one JPEG into the session's frames directory as the next
// sequence number and returns the file name. The next number is derived from
// the files on disk, not a counter, so a failed write or a restart can never
// leave a gap — ffmpeg's image2 demuxer stops at the first missing number in
// a %09d pattern. The session must still be recording; a frame arriving after
// settle (an in-flight grab finishing late) is refused, not written.
func (s *store) saveFrame(sess session, jpeg []byte) (string, error) {
	if s.dir == "" {
		return "", fmt.Errorf("connect-timelapse: no data directory granted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row := -1
	for i := range s.sessions {
		if s.sessions[i].ID == sess.ID {
			row = i
			break
		}
	}
	if row < 0 || s.sessions[row].State != "recording" {
		return "", fmt.Errorf("connect-timelapse: session %s is not recording", sess.ID)
	}
	dir := filepath.Join(s.dir, sessionsDir, sess.ID, framesDir)
	next := 1
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if n := frameSeqOf(e.Name()); n >= next {
				next = n + 1
			}
		}
	}
	name := fmt.Sprintf("%09d.jpg", next)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, jpeg, 0o600); err != nil {
		return "", err // no counter advanced, so the next frame reuses this number
	}
	s.sessions[row].Frames = next
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return name, nil
}

// frameSeqOf extracts the sequence number from a frame file name
// ("000000012.jpg" -> 12), or 0.
func frameSeqOf(name string) int {
	if !frameNameOK.MatchString(name) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(name, ".jpg"))
	if err != nil {
		return 0
	}
	return n
}

// framePath resolves a frame file name inside one session's frames
// directory, refusing anything that escapes it.
func (s *store) framePath(sessionID, name string) (string, error) {
	if s.dir == "" || !sessionIDOK.MatchString(sessionID) || !frameNameOK.MatchString(name) {
		return "", fmt.Errorf("bad name")
	}
	dir := filepath.Join(s.dir, sessionsDir, sessionID, framesDir)
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != dir {
		return "", fmt.Errorf("bad name")
	}
	return p, nil
}

// sessionDir returns the directory of one session.
func (s *store) sessionDir(sessionID string) (string, error) {
	if s.dir == "" || !sessionIDOK.MatchString(sessionID) {
		return "", fmt.Errorf("bad session id")
	}
	dir := filepath.Join(s.dir, sessionsDir, sessionID)
	if filepath.Base(dir) != sessionID {
		return "", fmt.Errorf("bad session id")
	}
	return dir, nil
}

// removeSession deletes one record and its directory.
func (s *store) removeSession(id string) error {
	if s.dir == "" {
		return fmt.Errorf("connect-timelapse: no data directory granted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.sessions[:0]
	for _, sess := range s.sessions {
		if sess.ID != id {
			kept = append(kept, sess)
		}
	}
	s.sessions = kept
	if err := s.saveLocked(); err != nil {
		return err
	}
	dir, err := s.sessionDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// prune keeps only the newest keep settled records (active recordings are
// never pruned) and deletes their directories.
func (s *store) prune(keep int) {
	if s.dir == "" || keep < 1 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var settled []session
	for _, sess := range s.sessions {
		if sess.State != "recording" {
			settled = append(settled, sess)
		}
	}
	sort.Slice(settled, func(i, j int) bool { return settled[i].Seq < settled[j].Seq })
	if len(settled) <= keep {
		return
	}
	drop := map[string]bool{}
	for _, sess := range settled[:len(settled)-keep] {
		drop[sess.ID] = true
	}
	kept := s.sessions[:0]
	for _, sess := range s.sessions {
		if !drop[sess.ID] {
			kept = append(kept, sess)
		}
	}
	s.sessions = kept
	_ = s.saveLocked()
	for id := range drop {
		_ = os.RemoveAll(filepath.Join(s.dir, sessionsDir, id))
	}
}

// setSource stores the camera input for one printer. An empty camera clears
// it.
func (s *store) setSource(printer string, src source) error {
	if s.dir == "" {
		return fmt.Errorf("connect-timelapse: no data directory granted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if src.Camera == "" {
		delete(s.sources, printer)
	} else {
		s.sources[printer] = src
	}
	return s.saveSourcesLocked()
}

// sourceOf returns the camera input configured for one printer.
func (s *store) sourceOf(printer string) (source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[printer]
	return src, ok
}

// allSources returns a copy of every configured source.
func (s *store) allSources() map[string]source {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]source, len(s.sources))
	for k, v := range s.sources {
		out[k] = v
	}
	return out
}

// shortID shortens a job UUID for a directory name.
func shortID(id string) string {
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, id)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
