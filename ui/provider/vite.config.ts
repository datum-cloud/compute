import { federation } from '@module-federation/vite';
import { defineConfig } from 'vite';

// Workloads resource-type plugin — a Module Federation remote loaded by the
// staff-portal host at runtime (see staff-portal's app/modules/plugins/,
// ported from cloud-portal's plugin-host system).
//
// Unlike a typical portal plugin, this one exposes no page/nav/component at
// all — it exists purely to declare a `portal.resource/platform` extension in
// public/plugin-manifest.json, which lets staff-portal's
// `/customers/resources` page query and render Workload rows *itself* (see
// that manifest's comment and app/modules/plugins/types.ts's
// ResourcePlatformExtension in staff-portal for the full design). No plugin
// code executes to produce those rows, so there's nothing to build here
// beyond a valid (empty) remote — `exposes` stays `{}` until this plugin
// grows an actual page.
//
// Assets are fetched server-side by staff-portal's asset proxy and served
// under /api/plugins/<slug>/…, so plain http://localhost during dev is fine
// and the browser never contacts this origin directly.
export default defineConfig({
  server: {
    port: 5199,
    strictPort: true,
    cors: true,
  },
  preview: {
    port: 5199,
    strictPort: true,
    cors: true,
  },
  build: {
    target: 'esnext',
    minify: false,
  },
  plugins: [
    federation({
      // MUST equal the manifest `name` — the host keys the remote by this id.
      name: 'workloads.staff-portal.datumapis.com',
      // The manifest's `remoteEntry` field points the host at this filename,
      // requested through the asset proxy as /api/plugins/workloads/remoteEntry.js.
      filename: 'remoteEntry.js',
      manifest: true,
      exposes: {},
    }),
  ],
});
