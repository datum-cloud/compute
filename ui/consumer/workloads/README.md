# Compute Portal Plugin

A read-only operational dashboard for the compute service (`compute.datumapis.com`),
shipped as a **Module Federation remote** that the cloud portal loads at
runtime — the [Portal Plugin System](https://github.com/datum-cloud/cloud-portal/blob/main/docs/enhancements/portal-plugin-system.md).
Structural template: [`examples/sample-plugin/`](https://github.com/datum-cloud/cloud-portal/tree/main/examples/sample-plugin)
in the `cloud-portal` repo.

## Scope — v1 is read-only

This is a **CLI-first** service: workload creation, deployment, scaling,
restarts, and deletion all happen via `datumctl compute …`. The plugin's job
is to give operators visibility into what's already running:

- **Workloads** (`workloads`) — list, with health, ready count, placements,
  age.
- **Workload detail** (`workloads/:workloadName`) — Overview tab only: stat
  tiles, configuration, and an embedded running-instances table.
- **Instance detail** (`workloads/:workloadName/instances/:instanceName`) —
  Overview tab only: stat tiles (uptime, region, instance type, image),
  network card, health conditions, runtime config.

No deploy/edit/delete forms, and no Activity/Metrics/Settings tabs in v1 —
those stay out of scope until there's a real telemetry/activity source to back
them. `CliBanner`/`SectionCard` (in `src/components/cli-section.tsx`) point
users at the equivalent `datumctl` commands wherever the portal can't do
something itself.

## Relationship to cloud-portal PR #1315

An earlier, unmerged cloud-portal PR (#1315) built this same dashboard as
**native portal routes**. This plugin ports that UI (Zod schemas, adapters,
JSX) into a standalone remote living in this repo instead, rewriting the
parts that depended on portal internals:

| PR #1315 (portal-internal) | This plugin |
|---|---|
| Generated SDK (`@/modules/control-plane/compute`) | Plain `fetch()` against `/api/proxy/…` (`src/lib/api.ts`) |
| `useResourceWatch` (watch-stream) | `useQuery` with `refetchInterval` (~10s) polling |
| `runDetailLoader`/`runListLoader` server-side RBAC gate | Client-side catch of a non-ok/403 `fetch()` response → inline restricted state |
| `paths.config.ts` + `getPathWithParams` | `useParams()` / `useLocation()` / `useNavigate()` from the shared `react-router` singleton |

`workload.schema.ts`/`instance.schema.ts` (combined into `src/schema.ts`),
`workload.adapter.ts`/`instance.adapter.ts` (combined into `src/adapter.ts`),
`cli-section.tsx`, and `compute.helper.ts` (as `src/lib/format.ts`) ported
over near-verbatim — they were already free of portal internals (aside from
one hook swap: `@/hooks/useCopyToClipboard` → `@datum-cloud/datum-ui/hooks`).

The activity-feed link-resolver changes in PR #1315
(`activity-link-resolvers.ts`/`kinds.ts`) are cloud-portal-side and out of
scope here — a small follow-up for that repo, not this plugin.

## Data fetching — every call goes through Milo

The browser only ever calls the portal origin; the portal mediates all data.
Every API call goes through the portal's existing authenticated proxy at
`/api/proxy/apis/resourcemanager.miloapis.com/v1alpha1/projects/<id>/control-plane/…`,
reaching the compute aggregated apiserver the same way as any other Milo
resource:

```
GET .../control-plane/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads
GET .../control-plane/apis/compute.datumapis.com/v1alpha/namespaces/default/instances?labelSelector=compute.datumapis.com/workload-name=<name>
```

Note the API group version is **`v1alpha`**, not `v1alpha1` — verified against
`api/v1alpha/groupversion_info.go` in this repo.

Data fetching uses `@tanstack/react-query` (a host-shared singleton), so
plugin queries live in the host's cache next to built-in pages. This plugin
does **not** create its own `QueryClient`. In place of PR #1315's watch-stream
hook (`useResourceWatch`, portal-internal and unavailable to plugins), each
query hook in `src/lib/api.ts` sets `refetchInterval: 10_000` for live-ish
updates — a deliberate v1 trade-off, not equivalent to a real watch.

### RBAC — known v1 gap

PR #1315 used a server-side loader for per-resource permission checks with a
friendly "restricted" page on 403. A plugin has no server loader, so this
plugin instead catches a non-ok `fetch()` response in `src/lib/api.ts`
(`ApiError`, carrying the HTTP status) and each page renders an inline
"Access restricted" card on `status === 403` (see
`src/components/states.tsx`). This is coarser-grained than PR #1315's
version — accepted for v1.

## This is its own project

`ui/consumer/workloads/` has its **own** `package.json` and lockfile,
independent of the rest of this repo. It never touches the repo's Makefile,
CI, or other root files. Install and run it in isolation:

```bash
cd ui/consumer/workloads
bun install
bun run dev        # Vite dev server (:7778, HMR) — standalone/direct
bun run preview    # built dist/ served (:7778) — proxy-safe
bun run build      # static dist/ (remoteEntry.js + plugin-manifest.json + chunks)
bun run typecheck  # tsc --noEmit
```

### Serve `dev` or `preview`? (it matters for the portal proxy)

The portal loads plugin assets through its **same-origin asset proxy**
(`/api/plugins/<slug>/…`), never directly from `:7778`. That changes which
server you want running:

- **`preview`** (built) — remote-entry/chunk imports are **proxy-relative**,
  so they load correctly through the portal proxy. Use this whenever the
  portal will load the plugin.
- **`dev`** (Vite, HMR) — the dev remote entry imports host-absolute URLs
  that 404 through the proxy. Use this only for the standalone preview at
  `http://localhost:7778/` (direct, no proxy).

## How it fits the contract

`public/plugin-manifest.json` declares:

- **`portal.nav/project`** — a "Workloads" sidebar item (icon `server`),
  gated on `{compute.datumapis.com, workloads, list}`.
- **`portal.page/project`** ×3 — `workloads` (`WorkloadList`),
  `workloads/:workloadName` (`WorkloadDetail`), and
  `workloads/:workloadName/instances/:instanceName` (`InstanceDetail`, gated
  on `{compute.datumapis.com, instances, get}`).

Three things stay in lockstep, same as the sample plugin:

1. MF container `name` (`vite.config.ts`) = manifest `name` = the
   `PortalPlugin` metadata.name (`workload.compute.datumapis.com`).
2. `filename` (`vite.config.ts`) = manifest `remoteEntry` (`remoteEntry.js`).
3. MF `exposes` keys = manifest `exposedModules` keys = the `$codeRef` values.

### Shared singletons

The host provides `react`, `react-dom`, `react-router`, and
`@tanstack/react-query` as shared singletons (host's exact instances — see
`vite.config.ts`). This plugin bundles its own copy of `@datum-cloud/datum-ui`
(not shared as a singleton in v1), so its CSS ships with the bundle.

## File layout

```
ui/consumer/workloads/
  vite.config.ts          MF container "workload.compute.datumapis.com"
  public/plugin-manifest.json
  src/
    lib/api.ts             fetch wrappers + useQuery hooks (workloads/instances)
    lib/format.ts           formatUptime / splitSlashValue (ported as-is)
    schema.ts                Zod schemas for Workload / Instance
    adapter.ts                raw K8s JSON → schema mappers
    components/cli-section.tsx   CommandBlock / SectionCard / CliBanner
    components/states.tsx    Loading / Error / Restricted state components
    pages/workload-list.tsx
    pages/workload-detail.tsx
    pages/instance-detail.tsx
    main.tsx                 standalone preview harness only
```
