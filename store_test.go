package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSessionLifecycleAndPrune(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)

	// Three prints settle in order; keep 2.
	var ids []string
	for i := 0; i < 3; i++ {
		sess, err := s.addSession("printer-1", "job-uuid-1234567890", "benchy.gcode", "/usb/benchy.gcode", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for f := 0; f < 3; f++ {
			if _, err := s.saveFrame(sess, []byte{0xFF, 0xD8, byte(f)}); err != nil {
				t.Fatal(err)
			}
			sess, _ = s.getSession(sess.ID)
		}
		sess.State = "done"
		sess.VideoBytes = 1234
		if err := s.updateSession(sess); err != nil {
			t.Fatal(err)
		}
		if _, err := s.saveFrame(sess, []byte{0xFF, 0xD8}); err == nil {
			t.Error("saveFrame accepted a settled session")
		}
		ids = append(ids, sess.ID)
	}
	// A failed write must not advance the numbering: the next frame reuses
	// the slot, so ffmpeg's %09d pattern never sees a gap.
	s2 := newStore(dir)
	live, err := s2.addSession("printer-2", "job-gapless", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s2.saveFrame(live, []byte{0xFF, 0xD8, byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, sessionsDir, live.ID, framesDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "000000001.jpg" || entries[1].Name() != "000000002.jpg" {
		t.Fatalf("frame names: %v", entries)
	}
	if got := len(s.listSessions("")); got != 3 {
		t.Fatalf("listed %d sessions, want 3", got)
	}
	s.prune(2)
	got := s.listSessions("")
	if len(got) != 2 {
		t.Fatalf("after prune(2): %d sessions, want 2", len(got))
	}
	// listSessions is newest first: got[0] is the newest.
	if got[0].ID != ids[2] || got[1].ID != ids[1] {
		t.Fatalf("kept %v, want the two newest", []string{got[0].ID, got[1].ID})
	}
	if _, err := os.Stat(filepath.Join(dir, sessionsDir, ids[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest session dir still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, sessionsDir, ids[2], framesDir, "000000003.jpg")); err != nil {
		t.Fatalf("newest session frames missing: %v", err)
	}

	// A recording session is never pruned.
	rec, err := s.addSession("printer-1", "job-live", "live.gcode", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.prune(1)
	if _, ok := s.getSession(rec.ID); !ok {
		t.Fatal("recording session was pruned")
	}
	if _, ok := s.activeSession("printer-1"); !ok {
		t.Fatal("recording session not active")
	}
}

func TestStoreFramePathSafety(t *testing.T) {
	s := newStore(t.TempDir())
	for _, bad := range []struct{ id, name string }{
		{"../sessions", "000000001.jpg"},
		{"000001_abc", "../../module.yaml"},
		{"000001_abc", "000000001.jpg/../../x"},
		{"000001_abc", "foo.jpg"},
		{"", "000000001.jpg"},
	} {
		if _, err := s.framePath(bad.id, bad.name); err == nil {
			t.Errorf("framePath(%q, %q) accepted", bad.id, bad.name)
		}
	}
	if _, err := s.framePath("000001_abc", "000000001.jpg"); err != nil {
		t.Errorf("framePath rejected a good name: %v", err)
	}
	if _, err := s.sessionDir("../evil"); err == nil {
		t.Error("sessionDir accepted a traversal")
	}
}

func TestConfigValidate(t *testing.T) {
	if err := defaultConfig().validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	bad := defaultConfig()
	bad.KeepPrints = 0
	if err := bad.validate(); err == nil {
		t.Error("keep_prints=0 accepted")
	}
	bad = defaultConfig()
	bad.VideoFPS = 0
	if err := bad.validate(); err == nil {
		t.Error("video_fps=0 accepted")
	}
}

// TestSourcePublicFillsDefaults: per-printer capture settings fall back to
// the built-in defaults in the API answer, and a saved value wins.
func TestSourcePublicFillsDefaults(t *testing.T) {
	def := defaultConfig()
	got := source{Camera: "cam"}.public(def)
	if got.FrameIntervalSec != 10 || got.VideoFPS != 24 || got.Mode != "snapshots" {
		t.Errorf("defaults not filled: %+v", got)
	}
	got = source{Camera: "cam", FrameIntervalSec: 5, VideoFPS: 30}.public(def)
	if got.FrameIntervalSec != 5 || got.VideoFPS != 30 {
		t.Errorf("saved values not used: %+v", got)
	}
}
