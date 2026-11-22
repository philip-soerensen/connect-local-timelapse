# GUIDE — build connect-timelapse and install it on a Linux server running Prusa Connect Local as a systemd service

Assumed layout (the one `docs/src/content/docs/install-from-a-release.md` in
`prusa-connect/` sets up):

```
/var/lib/prusa-connect/bin/prusa-connect   # the server binary (user prusa-connect)
/etc/prusa-connect/config.yaml             # main config
/etc/prusa-connect/config.d/               # machine-owned overlay (module declarations land here)
/var/lib/prusa-connect                     # state, incl. core.modules and module-data
systemctl unit: prusa-connect.service
```

API base: `http://<server>:<port>/api/v1` — the port your server listens on
(default 8080; some installs use another). Bearer token: mint one in
the web UI under Settings, or `POST /api/v1/auth/credentials`.

---

## 1. Build the release (any machine with Go >= 1.24 and Node 20+ for the UI)

```sh
cd connect-local-extensions/connect-timelapse
# go.mod replaces the SDK with the local prusa-connect checkout:
#   replace github.com/prusa3d/prusa-connect/sdk => ../prusa-connect/sdk

export PATH=$PATH:$HOME/.local/go/bin

# tests: manifest + openapi parse with the host's own parsers, store unit tests
go test -buildvcs=false ./...

# browser bundle -> ui/dist (module.yaml + ui/index.js)
cd ui && npm ci && npm run build && cd ..

# binaries, one per platform in module.yaml
mkdir -p release/ui
for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64; do
  GOOS=${p%_*} GOARCH=${p#*_} CGO_ENABLED=0 \
    go build -buildvcs=false -trimpath -o "release/connect-timelapse_${p}" .
done
mv release/connect-timelapse_windows_amd64 release/connect-timelapse_windows_amd64.exe
cp module.yaml LICENSE README.md CHANGELOG.md release/
cp ui/dist/ui/index.js release/ui/     # the bundle: entry ui/index.js under ui/
# checksums.txt in the exact format the host verifies: "<sha256>  <path>" with
# paths relative to the release root and NO "./" prefix (fs.ValidPath refuses
# "./CHANGELOG.md"). The marketplace's own tool writes that format:
python3 -I ../prusa-connect-marketplace-publish/lib/release.py checksum release
```

A release is one directory: the manifest at its root, one binary per platform
named `<name>_<GOOS>_<GOARCH>` (`.exe` on Windows), `checksums.txt` listing
every file (`prusa-connect module add` verifies it and refuses unlisted files),
and `ui/` with the bundle. `openapi.json` is NOT in the release: the fragment
travels inside the binary (`go:embed`, delivered in `plugin.info`); the
`openapi:` key in module.yaml names the file in the repository. The
marketplace's own checker is
`prusa-connect-marketplace-publish/lib/release.py` — run it locally:

```sh
python3 -I ../prusa-connect-marketplace-publish/lib/release.py inspect release
```

## 2. Copy the release to the server and install it

```sh
# on the server:
sudo install -d -m 0755 /opt/connect-timelapse-release
sudo cp -r release/. /opt/connect-timelapse-release/
sudo chown -R root:prusa-connect /opt/connect-timelapse-release
sudo chmod -R a+rX /opt/connect-timelapse-release
```

### Option A — from the Extensions page

The Extensions page lists marketplace extensions plus **one card per entry of
`core.modules_local`**. The server reads only `config.yaml` itself plus
`config.d/setup.yaml` and `config.d/resources.json` — an arbitrary `*.yaml`
dropped into `config.d/` is NOT read. So edit `/etc/prusa-connect/config.yaml`
directly, under its existing `core:` block:

```yaml
core:
  modules_local:
    github.com/philip-soerensen/connect-local-timelapse: /opt/connect-timelapse-release
```

Restart the service (`sudo systemctl restart prusa-connect`), reload the
Extensions page: a "Timelapse" card appears showing version and the permissions
it asks for (`observe`, `read_storage`; no egress — the module process
connects nowhere outside Connect). Click install and approve — that writes the declaration and the
grants (viewer role for `observe`/`read_storage`, plus the data directory) and
switches the instance on. The release directory is re-read on each listing, so
dropping a rebuilt release (higher `version`) into the same directory turns
the card into an Update.

### Option B — from the command line

`prusa-connect module add` does the same without the page (works while the
server is stopped or running):

```sh
sudo /var/lib/prusa-connect/bin/prusa-connect \
  module add github.com/philip-soerensen/connect-local-timelapse@0.3.1 \
  -config /etc/prusa-connect/config.yaml \
  -from /opt/connect-timelapse-release \
  -approve
```

`-approve` writes the requested `permissions`/`events`/`data` and the org-level
grant, and switches the instance on. The namespace is derived from the module
path: `com.github.aux.philip-soerensen.connect-local-timelapse`.

Optional settings file (only the keys you want to change; all have defaults):

```sh
sudo tee /etc/prusa-connect/connect-timelapse-settings.json <<'EOF'
{
  "keep_prints": 10
}
EOF
# then add: -settings /etc/prusa-connect/connect-timelapse-settings.json
```

```sh
sudo systemctl restart prusa-connect
sudo journalctl -u prusa-connect -f        # watch the module start
```

The module's routes are mounted at
`/api/v1/modules/com.github.aux.philip-soerensen.connect-local-timelapse/`.

## 3. Requirements and first use

- A **Timelapse guide** page appears in the left-hand Extensions menu with a
  short how-to; everything is configured on the printer's Timelapse tab.
- Upgrading from 0.1.x: remove a leftover `rtsp_hosts` key from the instance
  config block (Settings → Extensions → Timelapse, or the settings file) —
  the host refuses a config key the 0.3.1 manifest no longer declares.
- **ffmpeg on the server** to assemble the videos: `sudo apt install ffmpeg`.
  (Frames are recorded without it; only the MP4 assembly needs it.)
- On a printer's page, open the **Timelapse** tab and configure everything
  there: pick the camera, set its **stream URL** (e.g.
  `rtsp://192.0.2.10/live`, optional), its **snapshot every** cadence
  (e.g. `5s`), plus this printer's **frame every (s)** and **video fps**,
  then **Save camera**. Saving writes the stream URL and cadence
  through the server's own camera API, so the Connect server pulls the stream
  and records snapshots locally; the module only copies frames out of those
  recorded snapshots. No global settings are needed for the camera, and the
  module declares no egress (its process connects nowhere outside Connect).
- Print something. Frames land in
  `/var/lib/prusa-connect/config/config.d/module-data/connect-timelapse/sessions/<id>/frames/`,
  the video in `.../video.mp4`; the tab lists and plays the retained videos.

## 4. Watch it work

```sh
TOKEN=...   # bearer token
PORT=8080    # the port your server listens on
M=/api/v1/modules/com.github.aux.philip-soerensen.connect-local-timelapse
curl -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT$M/status"
curl -H "Authorization: Bearer $TOKEN" "http://127.0.0.1:$PORT$M/timelapses"
# ready events on the stream:
curl -N -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:$PORT/api/v1/events?types=com.github.aux.philip-soerensen.connect-local-timelapse/timelapse.ready"
# a video plays over ranged requests (one module answer carries at most 1 MiB):
curl -H "Authorization: Bearer $TOKEN" -H "Range: bytes=0-524287" \
  "http://127.0.0.1:$PORT$M/timelapses/<id>/video" -o chunk.bin -D - | head -8
sudo journalctl -u prusa-connect -f | grep -i timelapse
```

## 5. Update / remove

```sh
# rebuild the release, bump `version` in module.yaml, re-copy to
# /opt/connect-timelapse-release, then re-run module add (Option B) or reload
# the Extensions page (Option A) and click Update.
sudo /var/lib/prusa-connect/bin/prusa-connect \
  module remove connect-timelapse -config /etc/prusa-connect/config.yaml
```

## Troubleshooting notes (learned on the failure-detector sibling)

- The Extensions page reads local releases only from `core.modules_local` in
  `config.yaml` itself — a random `*.yaml` in `config.d/` is not read.
- If an instance hangs in `starting`, the `plugin.configure` decode refused a
  key: the config block may only name keys declared in `module.yaml`'s
  `options:` list.
- Page/CLI approval grants org-level `viewer` (`observe` + `read_storage`)
  and the data directory; that is all this module needs. The per-printer
  `operate` it only *asks about* from its tab routes, for the caller — no grant
  for the module itself is needed.
- The Timelapse tab writes `stream_url` + `snapshot_every` + `snapshot_storage:
  local` through `PUT /api/v1/cameras/{name}`, so the server records snapshots
  of the stream itself and the module captures `camera.snapshot.captured`
  from them. Pushed-photo cameras (and HTTP `snapshot_url`) need no stream
  URL.

---

# Reference (from the README)

## How it works

```
job.started ──► open a recording session ──► save one frame every N seconds,
                                                      │  copied out of the camera.snapshot.captured
                                                      │  events' storage (the Connect server records
                                                      │  those snapshots itself)
job.settled ──► ffmpeg assembles frames/ → video.mp4 ──► publish
              <namespace>/timelapse.ready ──► prune to the last keep_prints prints
```

- The camera is configured entirely on the printer's **Timelapse** tab: pick
  the camera, optionally set its **stream URL** (e.g.
  `rtsp://192.0.2.10/live`) and its **snapshot cadence** (`snapshot_every`,
  e.g. `5s`). Saving writes those through the server's own camera API
  (`PUT /api/v1/cameras/{name}`), so the Connect server pulls the stream and
  records snapshots locally (`snapshot_storage: local`) — the module process
  itself makes **no outbound connections** and declares no egress.
  Cameras that push photos (`PUT /api/v1/cameras/{id}/snapshot`) or serve an
  HTTP `snapshot_url` need no stream URL at all.
- **ffmpeg is required on the server** to assemble the videos
  (`apt install ffmpeg`). Without it, frames are still recorded, but no video
  is produced.
- Frames and videos live in the module's own data directory (granted at
  install). Only the last `keep_prints` prints are kept; older ones — frames
  and video — are deleted automatically when a new one is assembled.
- Videos are served from the module's routes with HTTP Range support in
  chunks of at most 512 KiB, because one module answer carries at most 1 MiB;
  the tab's `<video>` player works as usual.
- Assembly runs ffmpeg with `-threads 2` and the module's memory budget is
  1024 MiB: x264 at camera resolutions with default threading exceeds 256 MiB,
  which made the host kill the instance mid-assembly (over-memory).

## Settings (instance config block / Settings → Extensions)

| key | default | what |
|---|---|---|
| `enabled` | `true` | master switch |
| `video_crf` | `23` | x264 quality (0 lossless … 51 worst) |
| `keep_prints` | `5` | how many finished prints are retained |
| `max_frame_bytes` | `8388608` | largest frame read (bytes) |

## Per-printer settings (Timelapse tab only)

`frame_interval_seconds` (default 10) and `video_fps` (default 24) are **not**
global options: each printer's values are set on its Timelapse tab and stored
in the module's data directory (`POST /sources`), falling back to the
built-in defaults for printers without a saved row.

## Permissions and egress

The module asks for `observe` (read devices, subscribe to the event stream)
and `read_storage` (read stored camera snapshots), and only *asks about*
`operate` from its routes to authorize a caller's write (e.g. picking the
camera or deleting a timelapse of a printer the caller may operate). It
declares **no egress**: the module process connects nowhere outside Prusa
Connect; the server's own camera component does the stream pulling.

## Events

`<namespace>/timelapse.ready` (endpoint `ready`), routing key = the printer
id, payload: `{printer, job_id, session, timelapse, video, frames,
video_bytes, outcome, display_name, started, ended}` — published once per
print when the video has been assembled.

## Routes

Mounted at `/api/v1/modules/<namespace>/`; see `openapi.json`:

- `GET /healthz`, `GET /status`, `GET /settings`, `GET /config`
- `GET/POST /sources` — per-printer camera input (the camera name)
- `GET /timelapses`, `GET /timelapses/{id}`, `DELETE /timelapses/{id}`
- `GET /timelapses/{id}/video`, `GET /timelapses/{id}/frames/{name}` (ranged)

## UI

A **Timelapse** tab on every printer's page (`ui/`): live recording state,
camera picker with stream URL and snapshot cadence (written through the
server's camera API), retained recordings with in-page video playback and
delete.

## Install

See [GUIDE.md](GUIDE.md) for building a release and installing it on a
server running Prusa Connect Local as a systemd service. Publishing to the
Prusa Plugin Marketplace goes through
[prusa3d/prusa-connect-marketplace-publish](https://github.com/prusa3d/prusa-connect-marketplace-publish):
the release directory is `module.yaml`, one binary per platform named
`<name>_<goos>_<goarch>`, the `ui/` bundle, `checksums.txt`, and at most
`LICENSE`/`README.md`/`CHANGELOG.md` at the root — no hidden files, no
symlinks, no other root files (the OpenAPI fragment travels inside the
binary, embedded from `openapi.json`).

## License

MIT — see [LICENSE](LICENSE). This extension is mainly AI-written and provided
as-is, in good faith.
