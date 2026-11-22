# Changelog

## 0.3.1

- The AI-use warning ("100% vibecoded by Mistral Large 4 …") now shows on the
  Timelapse guide page and in the repository README.

- The module no longer pulls RTSP streams itself. Frames are always copied
  from the snapshots the Connect server records for the printer's camera
  (`camera.snapshot.captured`), so the module declares **no egress** and the
  global `rtsp_hosts` option is gone from the settings page.
- The Timelapse tab now configures the camera completely: pick the camera,
  set its stream URL (e.g. `rtsp://192.0.2.10/live`) and snapshot cadence
  (`snapshot_every`); saving writes them through the server's camera API
  (`PUT /api/v1/cameras/{name}`), and the server pulls the stream and records
  the snapshots locally. ffmpeg is now only needed to assemble the videos.
- Fix: the recording line on the Timelapse tab showed "0 frames saved" while
  recording; the live count is now read back from the store.
- New "Timelapse guide" page in the left-hand Extensions menu: where the tab
  is, how to configure the camera, how to watch and download the timelapses.
- Each recording card has a **Download** button (the video route answers
  ranged chunks, so the tab fetches the chunks and joins them into one file).
- A print that ended with zero frames now says why when a camera was picked
  ("no snapshot from camera X arrived during the print") instead of a bare
  "empty"; the camera name is trimmed before it is stored.
- Tab errors: a non-2xx answer from a module route (e.g. 503 while the module
  starts) is now shown as an error instead of silently rendering defaults.
- Fix (crash on assembly): the instance was killed as over-memory while ffmpeg
  assembled the video (x264 at 1080p+ with default per-core threading exceeds
  256 MiB, and the host counts the child process toward the module's budget).
  `memory_mib` is now 1024 and the ffmpeg invocation caps the *encoder* (the
  flags sit after `-i`): `-threads 2 -filter_threads 2 -x264-params
  rc-lookahead=20`. The assembly timeout now grows with the frame count, and
  in-flight snapshot reads are capped at 4. A panic in the background assembly
  now marks the session failed instead of taking the module process down.
- `frame_interval_seconds` and `video_fps` moved off the global settings page:
  they are now per-printer, set on the Timelapse tab next to the camera
  (defaults 10 s / 24 fps apply to printers without a saved row). The global
  options left are `enabled`, `video_crf`, `keep_prints`, `max_frame_bytes`.

Upgrade note: if your instance config block still names a key the 0.3.1
manifest no longer declares — `rtsp_hosts`, `frame_interval_seconds`, or
`video_fps` from 0.1.x — remove it: the host refuses an unknown config key and
names it in the refusal. The capture interval and video fps now live on each
printer's Timelapse tab.

## 0.1.2

- `rtsp_hosts` entries may now be whole stream URLs (`rtsp://192.0.2.10/live`),
  not only bare hosts (`192.0.2.10`): URL entries are matched by hostname,
  case-insensitively. Pasting the camera's stream URL into the setting now works.

## 0.1.1

- Timelapse tab: loading one failed call (e.g. the module still starting) no
  longer blanks the camera list; each request is fetched independently and
  errors are shown. The camera dropdown now says explicitly when no camera is
  declared for the printer, and Save is disabled until a camera is picked.

## 0.1.0

Initial release.

- While a printer prints, one camera frame is saved every
  `frame_interval_seconds`: pulled from the camera's RTSP stream with the
  server's ffmpeg (host allowlisted in `rtsp_hosts`), or copied from the
  `camera.snapshot.captured` events of cameras the server records itself
  (pushed photos, HTTP `snapshot_url`).
- When the print settles, the frames are assembled into one H.264 MP4
  (`video_fps`, `video_crf`) with ffmpeg, and a `<namespace>/timelapse.ready`
  event is published on the stream.
- The last `keep_prints` prints are retained (frames and video) in the
  module's data directory; older ones are deleted automatically.
- A "Timelapse" tab on every printer's page shows the live recording, plays
  the retained videos and picks the camera input per printer.
- Module routes serve the video and frames with HTTP Range support in chunks
  of at most 512 KiB (one module answer carries at most 1 MiB).
