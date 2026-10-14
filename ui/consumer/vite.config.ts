/// <reference types="node" />
import { federation } from '@module-federation/vite';
import { sentryVitePlugin } from '@sentry/vite-plugin';
import react from '@vitejs/plugin-react';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defineConfig, type Plugin } from 'vite';

const CONSOLE_WASM_DIR = resolve(__dirname, process.env.CONSOLE_WASM_DIR ?? '../../bin');
const CONSOLE_WASM = 'console-session.wasm';
const CONSOLE_WASM_EXEC = 'console-session-wasm_exec.js';
const CONSOLE_ASSETS_ID = 'virtual:console-client-assets';

// Ships the shell's wasm client from `make build-console-wasm` as hashed
// assets, so the portal's asset proxy caches it across visits. Without it the
// plugin builds and the Shell tab stays hidden.
function consoleWasm(): Plugin {
  let command: 'build' | 'serve' = 'build';
  const path = (file: string) => resolve(CONSOLE_WASM_DIR, file);
  const present = () => [CONSOLE_WASM, CONSOLE_WASM_EXEC].every((f) => existsSync(path(f)));
  return {
    name: 'compute-console-wasm',
    configResolved(config) {
      command = config.command;
    },
    resolveId(id) {
      return id === CONSOLE_ASSETS_ID ? `\0${CONSOLE_ASSETS_ID}` : undefined;
    },
    load(id) {
      if (id !== `\0${CONSOLE_ASSETS_ID}`) return undefined;
      if (!present()) {
        this.warn(
          `shell client not bundled: run make build-console-wasm or set CONSOLE_WASM_DIR (looked in ${CONSOLE_WASM_DIR})`
        );
        return 'export const consoleClientAssets = null;';
      }
      if (command === 'serve') {
        return `export const consoleClientAssets = { wasmUrl: new URL('/console/${CONSOLE_WASM}', location.href).href, runtimeUrl: new URL('/console/${CONSOLE_WASM_EXEC}', location.href).href };`;
      }
      const wasm = this.emitFile({
        type: 'asset',
        name: CONSOLE_WASM,
        source: readFileSync(path(CONSOLE_WASM)),
      });
      const runtime = this.emitFile({
        type: 'asset',
        name: CONSOLE_WASM_EXEC,
        source: readFileSync(path(CONSOLE_WASM_EXEC)),
      });
      return `export const consoleClientAssets = { wasmUrl: new URL(import.meta.ROLLUP_FILE_URL_${wasm}, location.href).href, runtimeUrl: new URL(import.meta.ROLLUP_FILE_URL_${runtime}, location.href).href };`;
    },
    configureServer(server) {
      server.middlewares.use('/console', (req, res, next) => {
        const file = [CONSOLE_WASM, CONSOLE_WASM_EXEC].find((f) => req.url === `/${f}`);
        if (!file || !existsSync(path(file))) return next();
        res.setHeader(
          'Content-Type',
          file.endsWith('.wasm') ? 'application/wasm' : 'text/javascript'
        );
        res.end(readFileSync(path(file)));
      });
    },
  };
}

// Compute Portal Plugin — a Module Federation remote loaded by the cloud-portal
// host at runtime. Structural template: examples/sample-plugin/ in the
// cloud-portal repo (see its README.md and
// docs/enhancements/portal-plugin-system.md there).
//
// The host (cloud-portal) loads this remote via @module-federation/runtime and
// provides react / react-dom / react-router / @tanstack/react-query as shared
// singletons, so the plugin renders with the host's exact React, router, and
// query client. `shared` below marks those as singletons with the host's
// version so the host copy always wins and React is never duplicated (two
// React instances break hooks).
//
// Assets are fetched server-side by the portal's asset proxy and served under
// /api/plugins/<slug>/…, so plain http://localhost during dev is fine and the
// browser never contacts this origin directly. MF's automatic publicPath makes
// federated chunks resolve relative to remoteEntry.js, which is what lets them
// load correctly through that same-origin proxy prefix.
export default defineConfig(({ command }) => {
  const hostShared = (requiredVersion: string | false) => ({
    singleton: true,
    requiredVersion,
    ...(command === 'build' ? { import: false as const } : {}),
  });

  return {
    server: {
      port: 7778,
      strictPort: true,
      // Allow cross-origin fetches of the manifest/remote during Tier 0/standalone.
      cors: true,
    },
    preview: {
      port: 7778,
      // Bind IPv4 explicitly — "localhost"-only binding resolves to IPv6-only
      // ([::1]) on some hosts, and the portal's server-side manifest fetch to
      // http://localhost:7778 then gets ECONNREFUSED on IPv4, silently dropping
      // the plugin out of the registry with no visible error.
      host: '127.0.0.1',
      strictPort: true,
      cors: true,
    },
    build: {
      target: 'esnext',
      // Maps for Sentry (below); no sourceMappingURL comment, so browsers
      // never request them. The Dockerfile drops them from the image.
      sourcemap: 'hidden',
    },
    experimental: {
      renderBuiltUrl(_filename, { hostType }) {
        return hostType === 'js' ? { relative: true } : undefined;
      },
    },
    plugins: [
      react(),
      consoleWasm(),
      federation({
        // MUST equal the manifest `name` — the host keys the remote by this id.
        name: 'workload.compute.datumapis.com',
        // The manifest's `remoteEntry` field points the host at this filename,
        // requested through the asset proxy as /api/plugins/compute/remoteEntry.js.
        filename: 'remoteEntry.js',
        manifest: true,
        // Exposed keys map 1:1 to the manifest's `exposedModules` keys / $codeRefs.
        // The host loads e.g. loadRemote('workload.compute.datumapis.com/WorkloadList').
        exposes: {
          './WorkloadList': './src/pages/workload-list.tsx',
          './WorkloadDetail': './src/pages/workload-detail.tsx',
          './InstanceDetail': './src/pages/instance-detail.tsx',
          './InstanceShellWindow': './src/pages/instance-shell-window.tsx',
          './WorkloadsHomeColumn': './src/cards/workloads-home-column.tsx',
        },
        // Host-pinned singletons. requiredVersion tracks the host's majors
        // (react 19, react-router 8, react-query 5) — cloud-portal moved
        // react-router 7 -> 8 in fabef049 without bumping this, which silently
        // broke every plugin page here (Module Federation rejects a singleton
        // whose requiredVersion the host's actual version doesn't satisfy).
        // Keep this in lockstep with cloud-portal's package.json majors.
        // singleton:true guarantees one instance — the host provides all of
        // these, so plugin queries share the host's QueryClient cache.
        // Builds rely on the host for these (import: false), so they bundle no
        // fallback copy: each was its own chunk, about 300KB gzipped in all,
        // fetched and parsed on every load though the host's copy always wins.
        // Dev keeps the fallbacks so the standalone preview runs without a host.
        shared: {
          react: hostShared('^19.0.0'),
          // The host does not share this subpath, so it keeps its (small)
          // fallback; left implicit it would inherit import: false from
          // `react` and throw "must be provided by host".
          'react/jsx-runtime': { singleton: true, requiredVersion: '^19.0.0' },
          'react-dom': hostShared('^19.0.0'),
          'react-router': hostShared('^8.0.0'),
          '@tanstack/react-query': hostShared('^5.0.0'),
          // Curated datum-ui subset shared by the host (see the host's
          // federation-host.ts DATUM_UI_SHARED). requiredVersion:false — the
          // host's copy always wins, which is what keeps styling identical to
          // built-in pages; the local install is types + standalone fallback.
          '@datum-cloud/datum-ui/badge': hostShared(false),
          '@datum-cloud/datum-ui/button': hostShared(false),
          '@datum-cloud/datum-ui/card': hostShared(false),
          '@datum-cloud/datum-ui/empty-content': hostShared(false),
          '@datum-cloud/datum-ui/icons': hostShared(false),
          // Do not share `logs`: MF colocates lucide-react / date-fns into the
          // logs loadShare chunk, so a host-provided logs module replaces those
          // exports and crashes WorkloadDetail / related pages.
          // `select` is shared by the host (see cloud-portal `federation-host.ts`) so
          // the overview range control renders the same Select as ALB.
          '@datum-cloud/datum-ui/select': hostShared(false),
          '@datum-cloud/datum-ui/separator': hostShared(false),
          '@datum-cloud/datum-ui/skeleton': hostShared(false),
          '@datum-cloud/datum-ui/table': hostShared(false),
        },
      }),
      // The plugin runs inside cloud-portal, so its errors reach the portal's
      // Sentry project. Debug IDs tie frames to these maps without a release,
      // since the portal's release names its own build, not this plugin's.
      // No token (local builds, forks) skips the upload.
      sentryVitePlugin({
        org: 'sentry',
        project: 'cloud-portal',
        authToken: process.env.SENTRY_AUTH_TOKEN,
        disable: command !== 'build' || !process.env.SENTRY_AUTH_TOKEN,
        release: { create: false, finalize: false, inject: false },
        telemetry: false,
      }),
    ],
  };
});
