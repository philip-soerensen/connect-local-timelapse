package main

import (
	"fmt"
	"sync/atomic"
)

// config is the effective settings: the manifest defaults with the operator's
// config block written over them, delivered on plugin.configure.
type config struct {
	Enabled          bool  `json:"enabled"`
	FrameIntervalSec int   `json:"frame_interval_seconds"`
	VideoFPS         int   `json:"video_fps"`
	VideoCRF         int   `json:"video_crf"`
	KeepPrints       int   `json:"keep_prints"`
	MaxFrameBytes    int64 `json:"max_frame_bytes"`
}

func defaultConfig() config {
	return config{
		Enabled:          true,
		FrameIntervalSec: 10,
		VideoFPS:         24,
		VideoCRF:         23,
		KeepPrints:       5,
		MaxFrameBytes:    8 << 20,
	}
}

// validate refuses a config the module cannot work with, so a bad operator
// write stops the instance at configure instead of failing per print.
func (c config) validate() error {
	if c.FrameIntervalSec < 1 || c.FrameIntervalSec > 86400 {
		return fmt.Errorf("connect-timelapse: frame_interval_seconds %d is outside [1, 86400]", c.FrameIntervalSec)
	}
	if c.VideoFPS < 1 || c.VideoFPS > 120 {
		return fmt.Errorf("connect-timelapse: video_fps %d is outside [1, 120]", c.VideoFPS)
	}
	if c.VideoCRF < 0 || c.VideoCRF > 51 {
		return fmt.Errorf("connect-timelapse: video_crf %d is outside [0, 51]", c.VideoCRF)
	}
	if c.KeepPrints < 1 || c.KeepPrints > 1000 {
		return fmt.Errorf("connect-timelapse: keep_prints %d is outside [1, 1000]", c.KeepPrints)
	}
	if c.MaxFrameBytes < 1024 || c.MaxFrameBytes > 64<<20 {
		return fmt.Errorf("connect-timelapse: max_frame_bytes %d is outside [1024, 67108864]", c.MaxFrameBytes)
	}
	return nil
}

// settingsStore holds the config atomically so routes and the event loop read
// the same value.
type settingsStore struct {
	held atomic.Value // config
}

func (s *settingsStore) load() config {
	if v := s.held.Load(); v != nil {
		return v.(config)
	}
	return defaultConfig()
}
