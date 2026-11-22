<script lang="ts">
  import { heldHost } from './host';

  // The Timelapse tab on a printer's page. A device-tab is handed the device
  // id and nothing else.
  interface Props {
    deviceId: string;
  }
  let { deviceId }: Props = $props();

  // The bundle is served under /api/v1/modules/<namespace>/ui/, so the
  // namespace is read from the bundle's own address.
  const ns = new URL(import.meta.url).pathname.split('/')[4];
  const mount = `/api/v1/modules/${ns}`;
  // Host API calls (cameras, camera settings) go through the host's own
  // client, with the signed-in person's session and abort signal.
  const hostClient = () => heldHost().client;
  const hostSignal = () => heldHost().signal;

  interface Settings {
    enabled: boolean;
    video_crf: number;
    keep_prints: number;
    max_frame_bytes: number;
  }
  interface Defaults {
    frame_interval_seconds: number;
    video_fps: number;
  }
  interface Status {
    enabled: boolean;
    ffmpeg: boolean;
    data_dir: boolean;
    recording: Record<string, { session: string; job_id: string; frames: number; started: string }>;
    sessions_kept: number;
  }
  interface Timelapse {
    id: string;
    printer: string;
    job_id: string;
    state: string;
    outcome?: string;
    display_name?: string;
    frames: number;
    started: string;
    ended?: string;
    video_url: string;
    video_bytes: number;
    frames_url: string;
    error?: string;
  }
  interface Source {
    camera: string;
    mode: string;
    frame_interval_seconds: number;
    video_fps: number;
  }
  interface Camera {
    name: string;
    type: string;
    version?: string;
    streamUrl: string;
    snapEvery: string;
  }

  let settings = $state<Settings | null>(null);
  let status = $state<Status | null>(null);
  let timelapses = $state<Timelapse[]>([]);
  let source = $state<Source | null>(null);
  let defaults = $state<Defaults>({ frame_interval_seconds: 10, video_fps: 24 });
  let initialized = false;
  let frameInterval = $state(10);
  let videoFps = $state(24);
  let cameras = $state<Camera[]>([]);
  let camName = $state('');
  let streamUrl = $state('');
  let snapEvery = $state('5s');
  let error = $state('');
  let actionError = $state('');
  const shownError = $derived(actionError || error);

  // Each call is fetched on its own: one failing route (e.g. the module is
  // still starting) must not blank the camera list.
  const getJSON = async (url: string) => {
    const r = await fetch(url);
    if (!r.ok) throw new Error(`${r.status}: ${(await r.text()).slice(0, 200)}`);
    return r.json();
  };

  async function load() {
    const firstError: string[] = [];
    const [s, st, tl, src] = await Promise.all(
      [
        getJSON(`${mount}/settings`),
        getJSON(`${mount}/status`),
        getJSON(`${mount}/timelapses?printer=${encodeURIComponent(deviceId)}`),
        getJSON(`${mount}/sources?printer=${encodeURIComponent(deviceId)}`),
      ].map((p) => p.catch((e) => (firstError.push(String(e)), null))),
    );
    // The cameras registered on this instance for the printer, and their
    // settings (stream URL, snapshot cadence, version for the write), via
    // the host client (modules cannot list cameras over the wire).
    let cams: any = { items: [] };
    let camSettings: any = { items: [] };
    try {
      cams = await hostClient().listCameras({ query: { device: deviceId } }, hostSignal());
    } catch (e: any) {
      firstError.push(`camera list: ${e}`);
    }
    try {
      camSettings = await hostClient().listCameraSettings({}, hostSignal());
    } catch (e: any) {
      firstError.push(`camera settings: ${e}`);
    }

    settings = s;
    status = st;
    timelapses = tl?.items ?? [];
    source = src?.sources?.[deviceId] ?? null;
    if (src?.defaults) defaults = src.defaults;
    const byName = new Map<string, any>();
    for (const cs of (camSettings.items ?? []) as any[]) byName.set(cs.name, cs);
    cameras = (cams.items ?? []).map((c: any) => {
      const cs = byName.get(c.name);
      return {
        name: c.name,
        type: c.type ?? 'generic',
        version: cs?.version,
        streamUrl: typeof cs?.config?.stream_url === 'string' ? cs.config.stream_url : '',
        snapEvery: typeof cs?.config?.snapshot_every === 'string' ? cs.config.snapshot_every : '5s',
      };
    });
    if (!initialized) {
      // Prefill the editable fields only on the first successful load, so
      // the 5 s poll does not revert unsaved edits.
      if (source && cameras.some((c) => c.name === source.camera)) {
        camName = source.camera;
      } else if (cameras.length > 0) {
        camName = cameras[0].name;
      }
      const cur = cameras.find((c) => c.name === camName);
      if (cur) {
        streamUrl = cur.streamUrl;
        snapEvery = cur.snapEvery;
      }
      frameInterval = source?.frame_interval_seconds || defaults.frame_interval_seconds;
      videoFps = source?.video_fps || defaults.video_fps;
      initialized = true;
    } else if (cameras.length > 0 && !cameras.some((c) => c.name === camName)) {
      // A saved name that no longer matches a camera (e.g. an untrimmed
      // legacy value) is not held in the select: fall back so one Save
      // click repairs it instead of persisting the stale name.
      camName = cameras[0].name;
    }
    error = firstError.join('; ');
  }

  function pickCamera(name: string) {
    camName = name;
    const c = cameras.find((x) => x.name === name);
    if (c) {
      streamUrl = c.streamUrl;
      snapEvery = c.snapEvery;
    }
  }

  // The camera's stream URL and snapshot cadence are written through the
  // host's own camera API (the signed-in operator may configure cameras), so
  // the Connect server itself pulls the stream and records the snapshots the
  // module then captures. The module process never dials out.
  async function saveCameraSettings() {
    const cam = cameras.find((c) => c.name === camName);
    if (!cam) return;
    const cs = (await hostClient().listCameraSettings({}, hostSignal())).items?.find((x: any) => x.name === camName);
    const config: Record<string, string> = { ...(cs?.config ?? {}) };
    config.printer = deviceId;
    config.snapshot_storage = 'local';
    if (streamUrl.trim()) config.stream_url = streamUrl.trim();
    else delete config.stream_url;
    if (snapEvery.trim()) config.snapshot_every = snapEvery.trim();
    const header: Record<string, string> = cs?.version ? { 'If-Match': `"${cs.version}"` } : { 'If-None-Match': '*' };
    await hostClient().putCamera(
      { path: { camera: camName }, header, body: { type: cam.type, config } },
      hostSignal(),
    );
  }

  async function saveSource() {
    try {
      await saveCameraSettings();
    } catch (e: any) {
      actionError = `camera settings refused: ${e?.message ?? e}`;
      return;
    }
    const r = await fetch(`${mount}/sources`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        printer: deviceId,
        source: { camera: camName, frame_interval_seconds: frameInterval, video_fps: videoFps },
      }),
    });
    if (!r.ok) actionError = `source refused: ${await r.text()}`;
    await load();
  }

  // The video route answers ranged chunks of at most 512 KiB, so a plain
  // <a download> would get 416 on a big file: fetch the chunks and join them.
  let downloading = $state<string | null>(null);
  async function download(t: Timelapse) {
    if (!t.video_url || downloading) return;
    downloading = t.id;
    try {
      const url = `${mount}${t.video_url}`;
      const first = await fetch(url, { headers: { Range: 'bytes=0-0' } });
      if (first.status !== 206) throw new Error(`unexpected status ${first.status}`);
      const total = Number((first.headers.get('content-range') ?? '').split('/')[1]);
      if (!total) throw new Error('the server sent no size');
      const parts: BlobPart[] = [await first.blob()];
      let off = 1;
      while (off < total) {
        const r = await fetch(url, { headers: { Range: `bytes=${off}-${off + 524287}` } });
        if (r.status !== 206) throw new Error(`chunk at ${off}: status ${r.status}`);
        parts.push(await r.blob());
        const end = Number((r.headers.get('content-range') ?? '').split(' ')[1]?.split('-')[1]?.split('/')[0]);
        off = (Number.isFinite(end) ? end : off + 524288) + 1;
      }
      const blob = new Blob(parts, { type: 'video/mp4' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `${t.display_name || t.job_id.slice(0, 8)}.mp4`;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e: any) {
      actionError = `download failed: ${e?.message ?? e}`;
    } finally {
      downloading = null;
    }
  }

  async function remove(id: string) {
    const r = await fetch(`${mount}/timelapses/${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (!r.ok) actionError = `delete refused: ${await r.text()}`;
    await load();
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  const fmtBytes = (n: number) =>
    n > 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MiB` : `${Math.round(n / 1024)} KiB`;
  const fmtTime = (iso: string) => new Date(iso).toLocaleString();
  const videoSec = (t: Timelapse) =>
    t.frames > 0 ? `${(t.frames / videoFps).toFixed(1)} s at ${videoFps} fps` : '';
  const recording = $derived(status?.recording?.[deviceId] ?? null);
</script>

<div class="timelapse">
  <h2>Timelapse</h2>
  {#if shownError}<p class="err">{shownError}</p>{/if}

  {#if !status?.data_dir}
    <p class="err">The module has no writable data directory; the install was not approved with <code>data</code>.</p>
  {/if}
  {#if status && !status.ffmpeg}
    <p class="muted">ffmpeg was not found on the server: frames are still recorded, but videos cannot be assembled. Install ffmpeg on the server.</p>
  {/if}

  <div class="row">
    <label>
      Camera
      <select bind:value={camName} onchange={() => pickCamera(camName)}>
        {#each cameras as c}
          <option value={c.name}>{c.name}</option>
        {/each}
      </select>
    </label>
    {#if cameras.length === 0}
      <span class="muted">
        No cameras are declared for this printer. Add one under the printer's Cameras tab
        (PUT /api/v1/cameras/&lt;name&gt; with config.printer = this printer id), then reload this tab.
      </span>
    {/if}
    {#if camName}
      <label title="The camera's stream, e.g. rtsp://192.0.2.10/live. Saved through the server's camera API: the Connect server pulls the stream and records snapshots itself. Leave empty for a camera that pushes photos or serves a snapshot_url.">
        stream URL
        <input type="text" bind:value={streamUrl} placeholder="rtsp://… (optional)" size="24" />
      </label>
      <label title="How often the server records a snapshot of this camera, e.g. 5s.">
        snapshot every
        <input type="text" bind:value={snapEvery} size="5" />
      </label>
      <label title="Fewest seconds between two saved frames while this printer prints. 0 = default (10 s).">
        frame every (s)
        <input type="number" min="0" max="86400" bind:value={frameInterval} size="5" />
      </label>
      <label title="Frames per second of the assembled timelapse video. 0 = default (24 fps).">
        video fps
        <input type="number" min="0" max="120" bind:value={videoFps} size="4" />
      </label>
    {/if}
    <button onclick={saveSource} disabled={cameras.length === 0 || !camName}>Save camera</button>
    <span class="muted">
      {#if source}
        saved: frames from the snapshots of {source.camera}
      {:else if cameras.length > 0}
        not saved yet: without a saved camera, frames come from the snapshots the server records for this printer's cameras
      {/if}
    </span>
  </div>

  <p class="muted">
    One frame every {frameInterval} s while a print runs, assembled to
    {videoFps} fps H.264 when it ends. Keeps the last {settings?.keep_prints ?? 5} prints.
    {#if !settings?.enabled}Recording is switched off.{/if}
  </p>

  {#if recording}
    <p class="recording">● recording print {recording.job_id.slice(0, 8)}… — {recording.frames} frames saved ({fmtTime(recording.started)})</p>
  {/if}

  <h3>Recordings ({timelapses.length})</h3>
  {#if timelapses.length === 0}
    <p class="muted">No timelapses yet for this printer.</p>
  {/if}
  {#each timelapses as t (t.id)}
    <div class="card">
      {#if t.video_url}
        <video controls preload="metadata" src={`${mount}${t.video_url}`}>
          <track kind="captions" />
        </video>
      {:else}
        <div class="novideo">no video{t.error ? `: ${t.error}` : ''}</div>
      {/if}
      <div class="meta">
        <strong>{t.display_name || t.job_id.slice(0, 8)}</strong>
        <span class="muted">
          {t.state}{t.outcome ? ` · ${t.outcome}` : ''} · {t.frames} frames{t.video_url ? ` · ${videoSec(t)} · ${fmtBytes(t.video_bytes)}` : ''}
          · {fmtTime(t.started)}{t.ended ? ` → ${fmtTime(t.ended)}` : ''}
        </span>
        {#if t.state !== 'recording'}
          {#if t.video_url}
            <button onclick={() => download(t)} disabled={downloading !== null}>
              {downloading === t.id ? 'Downloading…' : 'Download'}
            </button>
          {/if}
          <button onclick={() => remove(t.id)}>Delete</button>
        {/if}
      </div>
    </div>
  {/each}
</div>

<style>
  .timelapse {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    padding: 1rem;
    font-size: 0.875rem;
  }
  h2 {
    font-size: 1rem;
    font-weight: 600;
  }
  h3 {
    font-weight: 600;
    margin-top: 0.5rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 1.25rem;
    align-items: center;
  }
  .row label {
    display: flex;
    gap: 0.4rem;
    align-items: center;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #c00;
  }
  .recording {
    color: #c00;
    font-weight: 600;
  }
  .card {
    border: 1px solid #444;
    border-radius: 6px;
    overflow: hidden;
    max-width: 480px;
  }
  video {
    width: 100%;
    display: block;
    background: #000;
  }
  .novideo {
    padding: 2rem 1rem;
    color: #888;
    text-align: center;
  }
  .meta {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    padding: 0.4rem 0.6rem;
  }
</style>
