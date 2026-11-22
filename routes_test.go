package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serve writes one request through serveFileRanged and returns the recorder.
func serve(t *testing.T, path, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	rec := httptest.NewRecorder()
	serveFileRanged(rec, req, path, "video/mp4")
	return rec
}

func TestServeFileRanged(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.mp4")
	if err := os.WriteFile(small, []byte(strings.Repeat("a", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.mp4")
	if err := os.WriteFile(big, make([]byte, maxServeChunk+100_000), 0o600); err != nil {
		t.Fatal(err)
	}

	// No range: small file served whole.
	rec := serve(t, small, "")
	if rec.Code != http.StatusOK || rec.Body.Len() != 1000 {
		t.Errorf("no range on small: code %d len %d", rec.Code, rec.Body.Len())
	}
	// No range on a file over the cap: 416 asking for a range.
	rec = serve(t, big, "")
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("no range on big: code %d, want 416", rec.Code)
	}
	// Well-formed range on the big file: one 206 chunk of at most maxServeChunk.
	rec = serve(t, big, "bytes=0-")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != maxServeChunk {
		t.Errorf("range 0- on big: code %d len %d", rec.Code, rec.Body.Len())
	}
	if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-") {
		t.Errorf("Content-Range %q", cr)
	}
	// Bounded range.
	rec = serve(t, small, "bytes=100-199")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 100 || rec.Body.String() != strings.Repeat("a", 100) {
		t.Errorf("range 100-199: code %d len %d", rec.Code, rec.Body.Len())
	}
	// End beyond size is clamped.
	rec = serve(t, small, "bytes=900-99999")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 100 {
		t.Errorf("range 900-99999: code %d len %d", rec.Code, rec.Body.Len())
	}
	// Suffix range.
	rec = serve(t, small, "bytes=-50")
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 50 {
		t.Errorf("suffix -50: code %d len %d", rec.Code, rec.Body.Len())
	}
	// Inverted range (end < start): ignored, served whole per RFC 9110.
	rec = serve(t, small, "bytes=100-50")
	if rec.Code != http.StatusOK || rec.Body.Len() != 1000 {
		t.Errorf("inverted 100-50: code %d len %d, want 200 whole", rec.Code, rec.Body.Len())
	}
	// Malformed range: ignored, served whole (no panic).
	rec = serve(t, small, "bytes=abc-de")
	if rec.Code != http.StatusOK || rec.Body.Len() != 1000 {
		t.Errorf("malformed: code %d len %d, want 200 whole", rec.Code, rec.Body.Len())
	}
	// Multi-range: ignored, served whole.
	rec = serve(t, small, "bytes=0-1,3-4")
	if rec.Code != http.StatusOK || rec.Body.Len() != 1000 {
		t.Errorf("multi-range: code %d len %d, want 200 whole", rec.Code, rec.Body.Len())
	}
	// start >= size: unsatisfiable.
	rec = serve(t, small, "bytes=1000-")
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("start==size: code %d, want 416", rec.Code)
	}
	rec = serve(t, small, "bytes=5000-6000")
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("start>size: code %d, want 416", rec.Code)
	}
	// Missing file: 404.
	rec = serve(t, filepath.Join(dir, "nope.mp4"), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing file: code %d, want 404", rec.Code)
	}
}

func TestServeChunkBound(t *testing.T) {
	// A range wider than maxServeChunk is clamped to one chunk.
	dir := t.TempDir()
	big := filepath.Join(dir, "big.mp4")
	size := int64(maxServeChunk*2 + 10)
	if err := os.WriteFile(big, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := serve(t, big, "bytes=0-")
	if rec.Body.Len() != maxServeChunk {
		t.Fatalf("chunk len %d, want %d", rec.Body.Len(), maxServeChunk)
	}
	want := "bytes 0-" + strconv.Itoa(maxServeChunk-1) + "/" + strconv.FormatInt(size, 10)
	if rec.Header().Get("Content-Range") != want {
		t.Fatalf("Content-Range %q, want %q", rec.Header().Get("Content-Range"), want)
	}
}

// TestStatusCountsFramesFromStore is a regression test: the live recording
// line used to read the frame count from the activeSession copy, which never
// advances (saveFrame updates the store row), so the tab showed "0 frames
// saved" while recording. /status must report the store's count.
func TestStatusCountsFramesFromStore(t *testing.T) {
	dir := t.TempDir()
	a := newApp()
	a.settings.held.Store(defaultConfig())
	st := newStore(dir)
	a.cfgMu.Lock()
	a.dataDir = dir
	a.store = st
	a.cfgMu.Unlock()

	sess, err := st.addSession("printer-1", "job-1", "print", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.saveFrame(sess, []byte{0xFF, 0xD8, byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	a.mu.Lock()
	a.active["printer-1"] = &activeSession{sess: sess} // the copy still says 0 frames
	a.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	a.routes(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"frames":3`) {
		t.Errorf("status does not report the 3 saved frames: %s", body)
	}
}
