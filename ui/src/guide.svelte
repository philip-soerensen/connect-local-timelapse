<script lang="ts">
  // The "Timelapse guide" page in the left-hand Extensions menu. A page takes
  // no props; it is static instructions, kept minimal on purpose.
</script>

<div class="guide">
  <h1>Timelapse — how to use</h1>

  <blockquote class="warn">
    <strong>Warning: This project was 100% vibecoded by Mistral Large 4.</strong> I have done my
    best to check that everything works and behaves as intended, but consider this potential AI
    slop. There may be bugs.
  </blockquote>

  <h2>1. Open the tab</h2>
  <p>
    Go to any printer's page in Prusa Connect and open the <strong>Timelapse</strong> tab.
    Everything about timelapses lives there.
  </p>

  <h2>2. Configure the camera</h2>
  <p>On the Timelapse tab:</p>
  <ul>
    <li>pick the printer's <strong>camera</strong> from the dropdown,</li>
    <li>optionally set its <strong>stream URL</strong> (e.g. <code>rtsp://192.0.2.10/live</code>) — the Connect server pulls the stream itself and records snapshots of it locally,</li>
    <li>set <strong>snapshot every</strong> (e.g. <code>5s</code>) — how often the server records a snapshot,</li>
    <li>set <strong>frame every (s)</strong> and <strong>video fps</strong> — how often frames are captured during a print and the frame rate of the final video,</li>
    <li>click <strong>Save camera</strong>.</li>
  </ul>
  <p class="muted">
    Cameras that push photos or serve a single snapshot image need no stream URL.
    No global settings are needed; the camera is configured right here on the tab.
  </p>

  <h2>3. Print and watch</h2>
  <p>
    While a print runs, the tab shows a red <strong>● recording print …</strong> line with the
    live frame count. When the print ends, the frames are assembled into one MP4
    (this needs <code>ffmpeg</code> installed on the server).
  </p>

  <h2>4. Get the timelapse</h2>
  <p>
    Finished prints appear under <strong>Recordings</strong>. Play the video right in the page,
    or use <strong>Download</strong> to save the MP4 to your computer. <strong>Delete</strong>
    removes a recording. Only the last few prints are kept (the <code>keep_prints</code>
    setting); older ones are removed automatically.
  </p>
</div>

<style>
  .guide {
    max-width: 42rem;
    padding: 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    font-size: 0.9375rem;
    line-height: 1.5;
  }
  h1 {
    font-size: 1.25rem;
    font-weight: 650;
  }
  h2 {
    font-size: 1rem;
    font-weight: 600;
    margin-top: 0.75rem;
  }
  ul {
    padding-left: 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .muted {
    color: #888;
  }
  code {
    background: rgb(128 128 128 / 0.18);
    padding: 0 0.3rem;
    border-radius: 4px;
  }
  .warn {
    border-left: 3px solid #c90;
    /* translucent amber reads on both light and dark themes */
    background: rgb(255 170 0 / 0.14);
    color: inherit;
    padding: 0.5rem 0.75rem;
  }
</style>
