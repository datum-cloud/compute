# Workloads resource-type plugin

Compute-authored UI meant for a host other than cloud-portal — as opposed to
[`ui/consumer`](../consumer), which holds compute's own cloud-portal-facing
plugin(s). This directory *is* the plugin (no further nesting): it declares a
`portal.resource/platform` extension so `staff-portal`'s
`/customers/resources` page can list compute Workloads (`compute.datumapis.com`)
across every project as a Type filter option, alongside its native AI
Edge/DNS/Domain types — shipped as a **Module Federation remote** loaded by
`staff-portal`'s plugin host (`app/modules/plugins/` there, ported from
cloud-portal's
[Portal Plugin System](https://github.com/datum-cloud/cloud-portal/blob/main/docs/enhancements/portal-plugin-system.md)).

## No page, no component — manifest only

Unlike a typical portal plugin (and unlike this repo's own
`compute/ui/consumer`, the per-project consumer-facing version of
this same data), this plugin exposes nothing at all. `portal.resource/platform`
is a data-only extension: it declares a label, an icon name, and the
`search.miloapis.com` target GVK (`{group, version, kind}`), and staff-portal
runs the search itself — with the *viewing staff user's own* credentials —
and renders the rows in its own trusted table. See
`staff-portal/app/modules/plugins/types.ts`'s `ResourcePlatformExtension` for
the full design and its trust-boundary reasoning, and
`staff-portal/app/routes/customer/resource/index.tsx` for where it's consumed.

`public/plugin-manifest.json` is the entire plugin. `exposedModules` is `{}`
and there's no `src/` — nothing here executes at runtime. `vite.config.ts`
still runs a full Module Federation build (a valid, empty remote) since the
host's plugin registry pipeline expects a working `remoteEntry.js` to exist,
even though it's never actually fetched unless this plugin grows a page.

## Local dev

```
bun install
bun run build
bun run preview   # built dist/ served at :5199 — see below for why not `dev`
```

### Serve `dev` or `preview`?

staff-portal loads plugin assets through its **same-origin asset proxy**
(`/api/plugins/workloads/…`), never directly from `:5199`. `dev` (Vite, HMR)
emits a remote entry with host-absolute chunk URLs that 404 once proxied;
`preview` (built) is proxy-relative and safe. Since this plugin has no page to
preview standalone anyway, always use `build && preview` — same rule as the
sibling `compute/ui/consumer` plugin, see its README for the full
explanation.

To register it with a local staff-portal:

```
bun run build && bun run preview
```

```
# in staff-portal
PORTAL_PLUGINS=workloads=http://localhost:5199
```

then load `/customers/resources` in staff-portal and check "Workload" appears
in the Type filter.
