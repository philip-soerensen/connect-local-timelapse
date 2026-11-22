package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

// maxServeChunk caps one response body: the host relays at most
// routesapi.MaxModuleBody (1 MiB) per answer, so files are served in ranged
// chunks and a request without a Range header only gets a whole file that
// fits under the cap.
const maxServeChunk = 512 << 10

// maxListItems caps the /timelapses listing so one answer stays under the
// 1 MiB the host relays.
const maxListItems = 500

// mayOperate answers whether the caller of r may perform action on the named
// object, and writes 403 when not. Deny on any error.
func (a *app) mayOperate(w http.ResponseWriter, r *http.Request, action, objectType, objectID string) bool {
	if a.access == nil {
		http.Error(w, `{"error":"no access check available"}`, http.StatusForbidden)
		return false
	}
	decision, err := a.access.Check(r.Context(), accessapi.CheckParams{
		Subject:    routesapi.PrincipalOf(r),
		Action:     action,
		ObjectType: objectType,
		ObjectID:   objectID,
	})
	if err != nil || !decision.Allowed {
		http.Error(w, `{"error":"the caller may not operate this object"}`, http.StatusForbidden)
		return false
	}
	return true
}

// storeOr503 returns the store, or writes 503 and returns nil when
// plugin.configure has not run yet (the SDK contract says it always runs
// first; this is armor).
func (a *app) storeOr503(w http.ResponseWriter) *store {
	s := a.getStore()
	if s == nil {
		http.Error(w, `{"error":"the module is not configured yet"}`, http.StatusServiceUnavailable)
		return nil
	}
	return s
}

// routes is the module's HTTP surface, mounted by the host at
// /api/v1/modules/<namespace>/. Note the host strips that prefix before the
// request reaches the module, and it authorizes non-GET methods against the
// module instance itself (operate_resource); the checks here are the
// module's own, on top of that.
func (a *app) routes(conn *wirerpc.Conn) http.Handler {
	if conn != nil {
		a.access = accessapi.NewClient(conn)
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// GET /status is the module's overall state: config summary, ffmpeg
	// presence and the active recordings.
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		_, ffmpegErr := exec.LookPath(ffmpegPath)
		a.mu.Lock()
		acts := make([]*activeSession, 0, len(a.active))
		for _, act := range a.active {
			acts = append(acts, act)
		}
		a.mu.Unlock()
		// The frame count lives in the store (saveFrame advances it there), not
		// in the activeSession copy, so read it back per session.
		active := map[string]any{}
		for _, act := range acts {
			frames := act.sess.Frames
			if s := a.getStore(); s != nil {
				if cur, ok := s.getSession(act.sess.ID); ok {
					frames = cur.Frames
				}
			}
			active[act.sess.Printer] = map[string]any{
				"session": act.sess.ID,
				"job_id":  act.sess.JobID,
				"frames":  frames,
				"started": act.sess.Started.UTC().Format(time.RFC3339),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled":   cfg.Enabled,
			"ffmpeg":    ffmpegErr == nil,
			"data_dir":  a.getDataDir() != "",
			"recording": active,
			"sessions_kept": func() int {
				s := a.getStore()
				if s == nil {
					return 0
				}
				return len(s.listSessions(""))
			}(),
			"asked_by": routesapi.PrincipalOf(r),
		})
	})

	// GET /settings is what the Timelapse tab reads.
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled":         cfg.Enabled,
			"video_crf":       cfg.VideoCRF,
			"keep_prints":     cfg.KeepPrints,
			"max_frame_bytes": cfg.MaxFrameBytes,
			"asked_by":        routesapi.PrincipalOf(r),
		})
	})

	// GET /config answers the effective config for API users.
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		cfg := a.settings.load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config":   cfg,
			"asked_by": routesapi.PrincipalOf(r),
		})
	})

	// GET /sources?printer=<id> is the per-printer camera input.
	mux.HandleFunc("GET /sources", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		def := a.settings.load()
		out := map[string]any{}
		if id := r.URL.Query().Get("printer"); id != "" {
			if src, ok := store.sourceOf(id); ok {
				out[id] = src.public(def)
			}
		} else {
			for id, src := range store.allSources() {
				out[id] = src.public(def)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sources": out,
			// The built-in defaults for the per-printer capture settings, so
			// the tab can prefill its inputs for printers with no saved row.
			"defaults": map[string]any{"frame_interval_seconds": def.FrameIntervalSec, "video_fps": def.VideoFPS},
			"asked_by": routesapi.PrincipalOf(r),
		})
	})

	// POST /sources sets the camera input for one printer. Only a caller that
	// may operate the printer may change it. An empty camera clears the
	// source.
	mux.HandleFunc("POST /sources", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Printer string `json:"printer"`
			Source  source `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		if body.Printer == "" {
			http.Error(w, `{"error":"need printer"}`, http.StatusBadRequest)
			return
		}
		body.Source.Camera = strings.TrimSpace(body.Source.Camera)
		// Per-printer capture settings: 0 means "use the default"; anything
		// else must be inside the same bounds the global options once had.
		if body.Source.FrameIntervalSec < 0 || body.Source.FrameIntervalSec > 86400 {
			http.Error(w, `{"error":"frame_interval_seconds outside [0, 86400] (0 = default)"}`, http.StatusBadRequest)
			return
		}
		if body.Source.VideoFPS < 0 || body.Source.VideoFPS > 120 {
			http.Error(w, `{"error":"video_fps outside [0, 120] (0 = default)"}`, http.StatusBadRequest)
			return
		}
		if !a.mayOperate(w, r, "operate", "device", body.Printer) {
			return
		}
		if err := a.setSource(r.Context(), body.Printer, body.Source); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		src, _ := a.getStore().sourceOf(body.Printer)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "printer": body.Printer, "source": src.public(a.settings.load())})
	})

	// GET /timelapses?printer=<id> lists the retained records, newest first.
	// The list is capped so one answer stays under the 1 MiB the host relays.
	mux.HandleFunc("GET /timelapses", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		printer := r.URL.Query().Get("printer")
		items := store.listSessions(printer)
		if len(items) > maxListItems {
			items = items[:maxListItems]
		}
		if items == nil {
			items = []session{}
		}
		out := make([]map[string]any, 0, len(items))
		for _, s := range items {
			out = append(out, sessionJSON(s))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": out, "asked_by": routesapi.PrincipalOf(r)})
	})

	// GET /timelapses/{id} is one record.
	mux.HandleFunc("GET /timelapses/{id}", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		sess, ok := store.getSession(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"no such timelapse"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionJSON(sess))
	})

	// DELETE /timelapses/{id} removes one record and its files. The host
	// already requires operate_resource on this module's instance for a
	// DELETE; on top of that, the caller must be allowed to operate the
	// record's printer.
	mux.HandleFunc("DELETE /timelapses/{id}", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		sess, ok := store.getSession(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"no such timelapse"}`, http.StatusNotFound)
			return
		}
		if !a.mayOperate(w, r, "operate", "device", sess.Printer) {
			return
		}
		if sess.State == "recording" {
			http.Error(w, `{"error":"the print is still recording"}`, http.StatusConflict)
			return
		}
		if err := store.removeSession(sess.ID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": sess.ID})
	})

	// GET /timelapses/{id}/video serves the assembled MP4 in ranged chunks.
	mux.HandleFunc("GET /timelapses/{id}/video", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		sess, ok := store.getSession(r.PathValue("id"))
		if !ok || sess.VideoBytes <= 0 {
			http.Error(w, `{"error":"no video for this timelapse"}`, http.StatusNotFound)
			return
		}
		dir, err := store.sessionDir(sess.ID)
		if err != nil {
			http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
			return
		}
		serveFileRanged(w, r, filepath.Join(dir, videoFile), "video/mp4")
	})

	// GET /timelapses/{id}/frames/{name} serves one saved frame JPEG.
	mux.HandleFunc("GET /timelapses/{id}/frames/{name}", func(w http.ResponseWriter, r *http.Request) {
		store := a.storeOr503(w)
		if store == nil {
			return
		}
		p, err := store.framePath(r.PathValue("id"), r.PathValue("name"))
		if err != nil {
			http.Error(w, `{"error":"bad name"}`, http.StatusBadRequest)
			return
		}
		serveFileRanged(w, r, p, "image/jpeg")
	})

	return mux
}

// sessionJSON is the API shape of one session record.
func sessionJSON(s session) map[string]any {
	m := map[string]any{
		"id":          s.ID,
		"printer":     s.Printer,
		"job_id":      s.JobID,
		"state":       s.State,
		"frames":      s.Frames,
		"started":     s.Started.UTC().Format(time.RFC3339),
		"video_url":   s.videoURL(),
		"video_bytes": s.VideoBytes,
		"frames_url":  "/timelapses/" + s.ID + "/frames/",
	}
	if s.DisplayName != "" {
		m["display_name"] = s.DisplayName
	}
	if s.Path != "" {
		m["path"] = s.Path
	}
	if s.Outcome != "" {
		m["outcome"] = s.Outcome
	}
	if !s.Ended.IsZero() {
		m["ended"] = s.Ended.UTC().Format(time.RFC3339)
	}
	if s.Error != "" {
		m["error"] = truncate(s.Error, 500)
	}
	return m
}

// serveFileRanged serves one file, honoring one "bytes=start-end" Range
// request in chunks of at most maxServeChunk. A request without a Range
// header is answered whole only while the file fits under the cap, and with
// 416 otherwise: the host relays at most 1 MiB per module answer, so a large
// file is only reachable through ranges (a browser's <video> always sends
// one). A malformed or inverted range is ignored per RFC 9110 and answered
// like no range at all; an unsatisfiable but well-formed range (start beyond
// the end) is 416.
func serveFileRanged(w http.ResponseWriter, r *http.Request, path, contentType string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.Error(w, `{"error":"no such file"}`, http.StatusNotFound)
		return
	}
	size := info.Size()
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, `{"error":"no such file"}`, http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))

	start, end := int64(0), size-1
	ranged := false
	unsatisfiable := false
	if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") {
		spec := strings.TrimPrefix(rh, "bytes=")
		// Only a single range is served; multi-range requests are ignored.
		if !strings.Contains(spec, ",") {
			s, e, _ := strings.Cut(spec, "-")
			var ns, ne int64
			valid := false
			switch {
			case s != "":
				// bytes=start[-end]
				if v, err := strconv.ParseInt(s, 10, 64); err == nil && v >= 0 {
					ns, ne, valid = v, size-1, true
					if e != "" {
						if ev, err := strconv.ParseInt(e, 10, 64); err == nil && ev >= ns {
							ne = ev
						} else {
							valid = false // malformed or inverted end
						}
					}
				}
			case e != "":
				// suffix range: bytes=-N, the last N bytes
				if v, err := strconv.ParseInt(e, 10, 64); err == nil && v > 0 {
					if v > size {
						v = size
					}
					ns, ne, valid = size-v, size-1, true
				}
			}
			if valid {
				if ns >= size {
					unsatisfiable = true
				} else {
					if ne > size-1 {
						ne = size - 1
					}
					start, end, ranged = ns, ne, true
				}
			}
			// An invalid range is ignored per RFC 9110: serve as no range.
		}
	}
	if unsatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if ranged && end-start+1 > maxServeChunk {
		end = start + maxServeChunk - 1
	}
	if !ranged && size > maxServeChunk {
		// The whole file does not fit one module answer; ask for a range.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	if length < 0 {
		length = 0
	}
	if ranged {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if length == 0 {
		return
	}
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return // the recorder drops a short write and the host refuses it
	}
	_, _ = w.Write(buf)
}
