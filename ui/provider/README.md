# Workloads resource-type plugin

Compute-authored UI meant for a host other than cloud-portal — as opposed to
[`ui/consumer`](../consumer), which holds compute's own cloud-portal-facing
plugin(s). This directory *is* the plugin (no further nesting): a Module
Federation remote loaded by `staff-portal`'s plugin host
(`app/modules/plugins/` there, ported from cloud-portal's
[Portal Plugin System](https://github.com/datum-cloud/cloud-portal/blob/main/docs/enhancements/portal-plugin-system.md)),
declaring five extensions:

- **`portal.resource/platform`** — a data-only extension: label, icon, and the
  `search.miloapis.com` target GVK for compute Workloads
  (`compute.datumapis.com`). staff-portal runs the search itself — with the
  *viewing staff user's own* credentials — and lists Workloads as a Type
  filter option on `/customers/resources`, across every project. See
  `staff-portal/app/modules/plugins/types.ts`'s `ResourcePlatformExtension`
  for the full design and its trust-boundary reasoning.
- **`portal.page/project`** (`WorkloadList`, `src/pages/workload-list.tsx`,
  path `""` — the mount's index) — every Workload in one project, linking
  into `WorkloadDetail` below. Reached from staff-portal's own project detail
  nav (a native "Compute › Workloads" tab pointing at the plugin mount).
- **`portal.page/project`** (`WorkloadDetail`, `src/pages/workload-detail.tsx`,
  path `:workloadName`) — the actual support view for a single Workload,
  reached either from `WorkloadList` or by clicking a Workload row on
  `/customers/resources`.
- **`portal.page/service`** (`FleetWorkloads`, `src/pages/fleet-workloads.tsx`,
  path `"workloads"`) — every Workload across every active consumer project
  of compute, sortable/paginated. Rendered as a "Workloads" tab on
  staff-portal's `/admin/service-catalog/compute` detail page.
- **`portal.page/service`** (`ServiceOverview`, `src/pages/service-overview.tsx`,
  path `""` — the reserved "replace the built-in Overview" convention) —
  fleet-wide stats, the worst unhealthy workloads, and a handful of
  service-catalog facts (phase, conditions, quota, pricing, meters), in place
  of staff-portal's built-in Overview content for compute.

The two project-scoped pages are mounted under
`/customers/projects/:projectName/plugins/<slug>/…` by staff-portal's
project-scoped plugin mount — `projectName` reaches them via `useParams()`
resolving the ancestor route match (shared react-router singleton, no extra
plumbing), and `:workloadName` (on the detail page only) from that
extension's own declared `path`. The two service-scoped pages are similarly
mounted under `/admin/service-catalog/:name/plugins/<slug>/…` (or, for the
Overview override, rendered directly at `/admin/service-catalog/:name`) —
see `src/lib/fleet-health.ts`'s header comment for how `serviceName` reaches
them the same way.

## The support view

Built for a staff member fielding "why isn't my workload starting" / "what's
wrong with this workload" from a customer, not for general browsing —
Overview and Instances surface raw conditions (type/status/reason/message),
placements, network assignments, and scheduling gates, not just a coarse
health enum. The Logs tab mounts the empty datum-ui explorer until a Loki
query is wired; Events/Metrics stay honest "Coming Soon" placeholders. YAML
dumps the raw resource (minus `metadata.managedFields`) as an escape hatch. All data is read client-side, polled via
`refetchInterval` (`src/lib/api.ts`) through staff-portal's own same-origin
proxy — no new credential, no plugin-owned backend.

## Local dev

```
bun install
bun run build
bun run preview   # built dist/ served at :5199
bun run dev       # standalone preview harness at :5199, direct (no proxy)
```

### Serve `dev` or `preview`?

staff-portal loads plugin assets through its **same-origin asset proxy**
(`/api/plugins/workloads/…`), never directly from `:5199`. `dev` (Vite, HMR)
emits a remote entry with host-absolute chunk URLs that 404 once proxied;
`preview` (built) is proxy-relative and safe. Use `dev` only for the
standalone harness at `http://localhost:5199/` (direct, no proxy — data
calls 404 there since there's no staff-portal proxy to reach); use
`build && preview` whenever staff-portal will load the plugin.

To register it with a local staff-portal:

```
bun run build && bun run preview
```

```
# in staff-portal — PORTAL_PLUGINS_JSON, not the simpler PORTAL_PLUGINS, since
# the portal.page/service extensions need a serviceRef the simple slug=url
# syntax has no way to express (see static-source.ts's own doc comment).
PORTAL_PLUGINS_JSON='[{"slug":"workloads","assets":{"baseURL":"http://localhost:5199"},"serviceRef":{"name":"compute"}}]'
```

then load `/customers/resources` in staff-portal, check "Workload" appears in
the Type filter, and click a Workload row to reach the support view. Load
`/admin/service-catalog/compute` to see the Overview override and the
Workloads tab.
