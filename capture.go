package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ffmpegPath is resolved once at start; without ffmpeg the module records
// frames but cannot assemble the videos.
var ffmpegPath = "ffmpeg"

// ffmpegAssemble assembles the session's frames into one H.264 MP4. The
// frames are named %09d.jpg, so the image2 demuxer reads them in order.
// yuv420p needs even dimensions, hence the scale filter. The whole run is
// bounded by a timeout: a stuck encoder must not hold a session forever.
func ffmpegAssemble(ctx context.Context, framesDir, outPath string, fps, crf int) error {
	// The timeout grows with the frame count: a stuck encoder must not hold
	// a session forever, but a long print at a 1 s interval is legitimate.
	n, _ := countFrames(framesDir)
	asmCtx, cancel := context.WithTimeout(ctx, 10*time.Minute+time.Duration(n)*time.Second/10)
	defer cancel()
	cmd := exec.CommandContext(asmCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-framerate", fmt.Sprintf("%d", fps),
		"-i", filepath.Join(framesDir, "%09d.jpg"),
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-crf", fmt.Sprintf("%d", crf),
		// Output-side thread caps: placed after -i they limit the x264
		// encoder, whose frame-thread buffers (one set per core at auto)
		// are what blew the memory budget at 1080p+. rc-lookahead (default
		// 40) buffers that many full-resolution frames; 20 bounds it.
		"-threads", "2", "-filter_threads", "2",
		"-x264-params", "rc-lookahead=20",
		"-movflags", "+faststart",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
