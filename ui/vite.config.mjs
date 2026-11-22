import { copyFileSync, mkdirSync, rmSync } from 'node:fs';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The bare specifiers the app's import map answers; the bundle keeps them
// external so the page holds one Svelte runtime and one library
// (packages/svelte/build/specifiers.ts in prusa-connect).
const SHARED = [
  'svelte',
  'svelte/internal/client',
  'svelte/internal/disclose-version',
  '@prusa/connect',
  '@prusa/connect-ui',
  '@prusa/connect-ui/i18n',
  '@prusa/connect-ui/domain',
  '@prusa/connect-ui/data',
  '@prusa/connect-ui/slots',
  '@prusa/connect-ui/primitives',
  '@prusa/connect-ui/router',
  '@prusa/connect-ui/shell',
];

export default {
  plugins: [
    svelte(),
    {
      // The bundle root is dist/: the manifest at its top (the host reads it
      // there) and the entry at dist/ui/index.js, matching `ui.entry` in
      // module.yaml.
      name: 'bundle-meta',
      buildStart: {
        order: 'pre',
        handler() {
          rmSync('dist', { recursive: true, force: true });
          mkdirSync('dist', { recursive: true });
        },
      },
      closeBundle: {
        order: 'post',
        handler() {
          copyFileSync('../module.yaml', 'dist/module.yaml');
        },
      },
    },
    {
      // Styles travel inside the one entry module: the manifest names a
      // single file, so a separate CSS asset would never be fetched.
      name: 'inline-css',
      enforce: 'post',
      generateBundle: {
        order: 'post',
        handler(_options, bundle) {
          for (const [name, out] of Object.entries(bundle)) {
            if (out.type !== 'asset' || !name.endsWith('.css')) continue;
            const css = String(out.source);
            delete bundle[name];
            for (const o of Object.values(bundle)) {
              if (o.type === 'chunk' && o.isEntry) {
                o.code +=
                  `\nif (typeof document !== 'undefined') {` +
                  `const s = document.createElement('style');` +
                  `s.textContent = ${JSON.stringify(css)};` +
                  `document.head.appendChild(s); }`;
              }
            }
          }
        },
      },
    },
  ],
  build: {
    outDir: 'dist/ui',
    cssCodeSplit: false,
    minify: true,
    lib: { entry: 'src/index.ts', formats: ['es'], fileName: () => 'index.js' },
    rollupOptions: { external: (id) => SHARED.includes(id) },
  },
};
