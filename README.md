# connect-timelapse

> **Warning: This project was 100% vibecoded by Mistral Large 4.** I have done my best to check
> that everything works and behaves as intended, but consider this potential AI slop.
> There may be bugs.

A [Prusa Connect Local](https://www.prusa3d.com/p/connect/connect-local/) extension that records
a **timelapse video of every print** from the printer's camera.

- While a print runs, one frame per configured interval is saved from the snapshots the Connect
  server records for the printer's camera (pushed photos, HTTP `snapshot_url`, or the camera's
  RTSP `stream_url` — the server pulls those itself; the module connects nowhere outside Connect).
- When the print ends, the frames are assembled into one H.264 MP4 with `ffmpeg` on the server.
- The last few prints are kept (`keep_prints`); older ones are deleted automatically.
- A **Timelapse** tab on each printer's page configures the camera (stream URL, snapshot cadence,
  frame interval, video fps), shows the live recording state, and plays/downloads/deletes the
  retained videos. A **Timelapse guide** page sits in the Extensions menu.

## Install

From the Prusa Plugin Marketplace (once published), or from a local release — see
[GUIDE.md](GUIDE.md) for building a release and installing it on a server running Prusa Connect
Local as a systemd service.

## License

MIT — see [LICENSE](LICENSE).
