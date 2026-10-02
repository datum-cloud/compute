# `datumctl compute` — Workload URLs

**Status:** Draft
**Companion:** [`datumctl-compute-dx.md`](./datumctl-compute-dx.md) (the DX arc this extends)

---

## Summary

`datumctl compute deploy --port=8080` ran a container across multiple cities and gave the developer nothing to point a browser at. This closes that gap with the smallest surface that does it: **declaring an HTTP port publishes the workload on a Datum-managed HTTPS URL.**

One breaking change — `--port` becomes `--http-port` — and no new commands.

---

## Scope: this plugin publishes a URL, it does not configure a proxy

Advanced proxy configuration belongs to dedicated ALB tooling: custom hostnames and their DNS verification, path and header routing, multiple backends, certificates, header rewriting, timeouts. This plugin deliberately owns none of it.

What it owns is the zero-config path, because that is the part a *compute* user needs and the part that was missing: run a container, get a working URL. Anything beyond that is a proxy configuration question, and answering it here would mean this plugin growing a second product inside it.

That boundary is why there is no `compute domains` command group and no `compute open`. It also sets one hard requirement in the other direction: **because hostnames are configured out of band, publishing must never clobber them.** `deploy` reads the custom hostnames already on the proxy and carries them forward on every redeploy, and fails closed if it cannot read them — a redeploy that silently detached someone's production hostname would be far worse than a redeploy that stops and says why.

---

## Product principles

**1. Declaring an HTTP port is declaring a web service.** One flag, one meaning. `--port` said *what is listening*, not *who can reach it*; in Kubernetes, which this platform is, `containerPort` exposes nothing at all. Every comparable platform makes the declared role decide exposure instead — Heroku routes `web:`, Render makes you pick Web Service or Private Service, Fly's port field lives inside `[http_service]`. `--http-port` carries that contract in the name, so no second confirmation flag is needed.

**2. The URL is free, so it is automatic.** A managed URL costs the developer nothing, which is what licenses issuing one without asking.

**3. The URL is the deliverable.** Last line of output, on its own, copy-pasteable.

**4. Never name the machinery.** The developer sees "URL", "backends", "certificate" — never `NetworkService`, `HTTPProxy`, or a raw condition reason. Blocking text is routed through `url.HumanBlock`, which shows the server's message and never its reason.

**5. Never invent resources that don't exist.** No `endpoint/api created` for a kind nobody can `datumctl get`.

---

## The experience

### Deploy and get a URL

```
$ datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1 --min=2 --http-port=8080

Resolving workload "api" in project acme-prod...
  Placement "default": locations=[us-east-1, eu-west-1], min=2
  HTTP service:        port 8080 → Datum-managed URL

Apply? (Y/n): y
  workload/api created

Waiting for rollout. Ctrl-C to detach (rollout continues in background).

  PLACEMENT  LOCATION     UPDATED  READY  OLD  PHASE
  default    us-east-1          2      2    0  Done
  default    eu-west-1          2      2    0  Done

Rollout complete in 47s.

Publishing...
  Backends     4 healthy across us-east-1, eu-west-1
  Edge         programmed
  Certificate  issued

  https://a1b2c3d4.datumproxy.net
```

The URL objects are written alongside the workload, not after the rollout, so backends register as instances come up and the URL answers moments after the last city reaches `Done`.

A workload with no `--http-port` deploys as it always did, and says so rather than leaving the developer guessing:

```
Rollout complete in 47s.

  No HTTP port declared — this workload is not reachable from the internet.
  To publish it:  datumctl compute deploy api --http-port 8080
```

### Find the URL again

There is no `status` command in this CLI (the DX doc proposes one that was never built), so the URL surfaces where a developer already looks. In the list:

```
$ datumctl compute workloads

  NAME     LOCATIONS              READY  IMAGE                     URL
  api      us-east-1, eu-west-1   4/4    ghcr.io/acme/api:1.4.2    https://api.example.com
  worker   us-east-1              1/1    ghcr.io/acme/worker:2.0   —
```

As a field, so `| jq -r .url` works: `datumctl compute workloads -o json`.

And in `describe`, with per-location backend health — the view that makes multi-location serving visible, which it previously was not:

```
$ datumctl compute workloads describe api

Workload     api                             project: acme-prod
Type         sandbox/datumcloud-d1-standard-2
Updated      4m ago

Health       Available

URL          https://api.example.com
Backend      port 8080/tcp

Serving      Degraded — 2 of 4 backends healthy

             LOCATION   BACKENDS  HEALTHY  SERVING
             us-east-1         2        2  yes
             eu-west-1         2        0  no

  eu-west-1: no healthy backends — instances are running but not passing health checks.
             Traffic is being served from us-east-1 only.

  Next steps:
    Check instances:  datumctl compute instances --workload=api --location=eu-west-1
```

### Stop serving

`--no-http` removes the HTTP service and the URL with it, naming what will stop answering before the prompt. `destroy` does the same as part of its summary:

```
$ datumctl compute destroy api

Workload:      api
Placements:    1  Locations: us-east-1, eu-west-1
Min replicas:  2
URLs:          https://api.example.com, https://a1b2c3d4.datumproxy.net

This will delete the workload, all its instances, and its URLs. Continue? (y/N):
```

The URL resources are deleted explicitly rather than by owner-reference GC, which is unverified in project virtual control planes. Domain objects are left alone — a verified domain is a project asset that outlives any one workload.

---

## Command surface

Changed, and nothing added:

```
datumctl compute deploy               --port → --http-port (breaking); --no-http removes it
datumctl compute workloads            URL column, and a `url` field in -o json/yaml
datumctl compute workloads describe   URL, backend port, and per-location backend health
datumctl compute destroy              lists URLs in its summary and deletes them
```

### On breaking `--port`

`--port` is removed, not silently aliased. Aliasing would publish every existing workload on the next plugin upgrade — precisely the surprise this design exists to avoid. For one release it stays registered and hard-errors:

```
Error: --port has been replaced by --http-port, which publishes the workload on a public
HTTPS URL. Use --http-port 8080 to publish, or --no-http to keep it internal
```

Redeploys are idempotent: an unchanged redeploy writes nothing, omitting `--http-port` inherits the port the workload already declares, and the managed hostname is never reissued — anything already pointing at that URL keeps working.

---

## What this deliberately does not do

- **No proxy configuration.** Custom hostnames, path and header routing, multiple backends, certificates, rewrites, timeouts. All ALB tooling's job; see the scope section.
- **No TCP or UDP exposure.** HTTP/HTTPS only, matching the platform's proxy.
- **No plaintext HTTP.** Always `https://`. No `--insecure`.
- **No backend TLS.** The edge reaches instances over plaintext inside the network, and the platform rejects backend TLS for this backend form. A container terminating TLS itself will not work, and the CLI says so on the publishing path.
- **One workload, one URL.**

---

## Open question

**What should the managed hostname look like?** `<uid>.datumproxy.net` is stable and collision-free but unmemorable and awkward to share. Heroku moved off `<name>.herokuapp.com` for squatting reasons; Fly uses `<app>.fly.dev` and accepts the collision namespace. This is a platform decision the CLI inherits, but it shapes the first-run experience more than anything else here.
