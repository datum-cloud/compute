---
name: deploy-container
description: "Deploy a container to Datum Cloud Compute from the person's own code or image and put it on a public HTTPS URL: build and push with datumctl compute build, deploy with datumctl compute deploy --http-port, then confirm it serves. Use when someone wants to ship, host, run or put their app, API or Dockerfile on Datum Cloud."
---

# Deploy a container behind a public URL

This is the shortest path from a Dockerfile, or an image already in a registry,
to a workload answering on a Datum-managed HTTPS URL. It runs at the person's
terminal with `datumctl compute`. Use `workload-create` instead when they want a
manifest, a virtual machine, more than one container, or anything the
`deploy` flags below cannot say.

`deploy` creates real, billable instances in the person's project. Collect the
choices first, show the exact commands you will run, and run them only after
the person agrees.

## 1. Check the ground

Run these, all read-only:

```bash
datumctl compute access
datumctl compute quota
datumctl compute workloads
```

- **`datumctl compute` is not found:** the compute plugin for datumctl is not
  installed. `datumctl plugin install compute` adds it.
- **Not signed in, or the wrong project:** `datumctl` runs against its own
  current organization and project. Pass `--project <name>` on every command if
  there is any doubt, and use the same project datum-mcp's `context` tool reports.
- **Compute is not enabled:** `datumctl services enable compute.datumapis.com`,
  then `datumctl compute access` again. If access has to be requested,
  `datumctl compute access request` does that, and nothing below works until it
  is granted.
- **A workload with the chosen name already exists:** `deploy` updates it. Say
  so, and read it with `datumctl compute workloads describe <name>` first.

## 2. Collect the choices

Ask only for what you cannot read from the repository:

| Choice | Where it comes from | Note |
|---|---|---|
| Workload name | the person; default to the app's directory name | Lowercase letters, digits and `-`. |
| Image | `--image`, fully qualified with registry and tag, e.g. `ghcr.io/acme/api:1.4.2` | `deploy` rejects a short name: write `docker.io/traefik/whoami:v1.10.3`, not `traefik/whoami`. Also where `--build` pushes to, so the person must be able to push there. |
| Port | the port the app listens on; read it from the Dockerfile `EXPOSE`, the start command or the app's config | This is `--http-port`. Getting it wrong publishes a URL that answers with errors. |
| Locations | `--location`, `--city` (e.g. `DFW,IAD`) or `--location-selector` | List what the project may use with datum-mcp's `resource` tool, `group: locations.miloapis.com`, `kind: Location`; load the `resource` toolset with `toolsets` first if it is missing. |
| Instances | `--min` per location (default 1), `--max` to autoscale | Replicas times instance size has to fit in what `datumctl compute quota` shows is left. |
| Environment | `--env KEY=VALUE`, `--env-from-secret NAME` | Never put a secret value in `--env`; create a Secret and import it. |

Two choices are final once the workload exists, so say them out loud:
`--runtime-class` (leave it unset for the platform default) and `--network`
(defaults to `default`). Changing either later means a new workload.

If the app serves HTTP/2 or gRPC, add `--http2`. Without it, gRPC calls through
the URL fail.

## 3. Build the image, if there is no image yet

From the build context, a local check first, since it pushes nothing:

```bash
datumctl compute build .
```

It builds with the BuildKit in the person's container engine (Docker Desktop,
OrbStack, Colima and others), so that has to be running. If it reports a
compatibility problem, `--fix` applies the safe fixes to the Dockerfile and
rebuilds; say which lines it changed. Then either push it:

```bash
datumctl compute build --push --output ghcr.io/acme/api:1.4.2 .
```

or let `deploy --build` build and push it in the same step, below. A private
registry also needs pull credentials on the workload, which `deploy` flags
cannot set; that is a `workload-create` job.

## 4. Deploy

Show the command, get a yes, then run it:

```bash
datumctl compute deploy api \
  --image=ghcr.io/acme/api:1.4.2 \
  --city=DFW \
  --min=1 \
  --http-port=8080 \
  --yes
```

Add `--build` (or `--build=./path`) to build and push from source in the same
step; the workload then runs the pushed digest, not the tag. `--yes` skips the
prompts `deploy` would otherwise ask at the terminal, which you cannot answer,
so leave it off only when the person runs the command themselves.

`--http-port` is what puts the workload on the public internet. `deploy`
creates a load balancer in front of it and prints the URL as its last line,
once the rollout finishes. Without the flag the workload has no URL at all.

On a redeploy, such as an image bump, leave `--http-port`, `--http2` and
`--no-http` off unless the person wants the URL changed. A redeploy without
them keeps the workload served exactly as it was.

## 5. Confirm it serves

```bash
datumctl compute workloads describe api
curl -sS -o /dev/null -w '%{http_code}\n' https://<url-from-deploy>/
```

A first request can take a few minutes while the certificate and DNS settle. If the
workload never becomes available, follow `workload-not-available`. If it is
available but the URL fails, read `datumctl compute logs api` for the app's own
errors, and `datumctl compute logs api --alb --since=15m` for what the URL
served.

## 6. A custom domain

The URL `deploy` prints is Datum's. To serve the app on the person's own
hostname, add it to the load balancer `deploy` created and verify the domain.
That is load balancer work: the `datum-networking` plugin covers it, from
domain verification to DNS and certificates. Do not create a DNS record by hand
for the hostname; the load balancer manages that record itself.

## Reporting

Say what is running, where, and where it answers: "one instance of
`ghcr.io/acme/api:1.4.2` in Dallas, serving on https://...". Name the workload
and the URL, since those are what the person checks or shares. If you stopped
before deploying, say which step is theirs and give the exact command.
