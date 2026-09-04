import { federation } from '@module-federation/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// Workloads resource-type plugin — a Module Federation remote loaded by the
// staff-portal host at runtime (see staff-portal's app/modules/plugins/,
// ported from cloud-portal's plugin-host system).
//
// Declares five extensions: `portal.resource/platform` (data-only, lets
// staff-portal's /customers/resources page query and render Workload rows
// itself — no plugin code executes for that), two `portal.page/project`
// pages — `WorkloadList` (the mount's index) and `WorkloadDetail`
// (`:workloadName`) — the per-consumer support views, and two
// `portal.page/service` pages: `FleetWorkloads` (path "workloads") — every
// workload across the fleet, sortable/paginated — and `ServiceOverview`
// (path "" — the reserved "replace the built-in Overview" convention, see
// staff-portal's types.ts) — the fleet "at a glance" numbers, rendered in
// place of staff-portal's built-in Overview content on its own
// /admin/service-catalog/compute detail page.
//
// Assets are fetched server-side by staff-portal's asset proxy and served
// under /api/plugins/<slug>/…, so plain http://localhost during dev is fine
// and the browser never contacts this origin directly. `shared` mirrors
// staff-portal's `federation-host.ts` DATUM_UI_SHARED set exactly — those are
// the only `@datum-cloud/datum-ui` subpaths the host actually provides.
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
    react(),
    federation({
      // MUST equal the manifest `name` — the host keys the remote by this id.
      name: 'workloads.staff-portal.datumapis.com',
      // The manifest's `remoteEntry` field points the host at this filename,
      // requested through the asset proxy as /api/plugins/workloads/remoteEntry.js.
      filename: 'remoteEntry.js',
      manifest: true,
      // Exposed keys map 1:1 to the manifest's `exposedModules` keys / $codeRefs.
      exposes: {
        './WorkloadList': './src/pages/workload-list.tsx',
        './WorkloadDetail': './src/pages/workload-detail.tsx',
        './FleetWorkloads': './src/pages/fleet-workloads.tsx',
        './ServiceOverview': './src/pages/service-overview.tsx',
      },
      shared: {
        react: { singleton: true, requiredVersion: '^19.0.0' },
        'react-dom': { singleton: true, requiredVersion: '^19.0.0' },
        // `react-dom/client` is only ever imported by this plugin's
        // standalone preview harness (main.tsx, never loaded by the real
        // host) — but Vite/MF still discovers it in the build graph and, left
        // unconfigured, auto-shares it with a strict version check that hard
        // -fails on any host/plugin react-dom patch drift (confirmed: staff-
        // portal runs 19.2.3, this plugin's own installed react-dom is
        // 19.2.7 — "Failed to bridge external shared module" at container
        // load, before any exposed component even runs). requiredVersion:
        // false — same rule as the datum-ui entries below — makes this a
        // no-op version check instead of a crash.
        'react-dom/client': { singleton: true, requiredVersion: false },
        'react-router': { singleton: true, requiredVersion: '^7.0.0' },
        '@tanstack/react-query': { singleton: true, requiredVersion: '^5.0.0' },
        '@datum-cloud/datum-ui/badge': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/button': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/card': { singleton: true, requiredVersion: false },
        '@datum-cloud/datum-ui/icons': { singleton: true, requiredVersion: false },
        // Do not share `logs`: MF colocates lucide-react / date-fns into the
        // logs loadShare chunk, so a host-provided logs module replaces those
        // exports and crashes WorkloadDetail / related pages.
        '@datum-cloud/datum-ui/skeleton': { singleton: true, requiredVersion: false },
      },
    }),
  ],
});
