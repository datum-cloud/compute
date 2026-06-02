# ConfigMap/Secret mount-verification probe

A small Bun/TypeScript probe that proves, at runtime inside a UKC unikernel
instance, that referenced `ConfigMap`/`Secret` data actually lands as mounted
files and injected env vars. It implements the design in
`docs/compute/development/rfcs/mount-runtime-probe.md`.

| File           | Purpose                                                              |
| -------------- | ------------------------------------------------------------------- |
| `probe.ts`     | Reads `PROBE_FILES` + `PROBE_ENV_VARS`, prints `PROBE_RESULT {json}`.|
| `Dockerfile`   | Bun-based sandbox image (entrypoint runs `probe.ts`).               |
| `Kraftfile`    | Drives `kraft cloud deploy` to build the UKC (`os: kraftcloud`) pkg. |
| `workload.yaml`| ConfigMap + Secret + Workload that mounts both as files AND env.    |

## What the probe does

On startup it prints ONE line `PROBE_RESULT {json}` to stdout (the UKC console,
observable via `kraft cloud instance logs`), reporting per item
`{name|path, exists, size, sha256, preview}`:

- `PROBE_FILES` (comma-separated paths) — each is stat'd + read.
- `PROBE_ENV_VARS` (comma-separated names) — each is read from the environment.
- Items listed in `PROBE_SECRET_PATHS` / `PROBE_SECRET_ENV_VARS` report sha256
  only (`preview: "<redacted>"`), so secret bytes are never written to the
  console/logs. The source sha256 is computed the same way, so a byte-exact
  mount is provable by comparing `sha256("<sentinel>")` to the reported sha256.

It then stays alive (Bun HTTP server) and serves the same JSON at `GET /probe`
(secondary path; the UKC service FQDN is not reliably reachable today, so the
console line is the primary observation channel) and `GET /healthz` → `ok`.

Local sanity check (proves the probe LOGIC, independent of UKC):

```sh
printf 'probe-cm-OK' > /tmp/g; printf 'probe-secret-OK' > /tmp/t
PROBE_FILES=/tmp/g,/tmp/t PROBE_ENV_VARS=GREETING,APP_TOKEN \
  PROBE_SECRET_PATHS=/tmp/t GREETING=probe-cm-OK APP_TOKEN=probe-secret-OK \
  PORT=13099 bun run probe.ts
# -> PROBE_RESULT {... /tmp/g sha256 == sha256("probe-cm-OK"), /tmp/t redacted ...}
```

## IMPORTANT: two UKC image requirements (validated the hard way on the lab)

A plain `docker build` image **does not boot** on this lab's UKC metro. Both of
these must hold, or the instance fails to boot:

1. **kraftkit packaging (`os: kraftcloud`), not a raw OCI image.** A raw
   `linux/amd64` image fails with `boot time: 0.00ms` and `instance get` →
   "exited (assertion error)". Build with `kraft cloud deploy` (see below); the
   known-good `datum/bun` image carries `platform.os: kraftcloud` +
   `sh.kraftkit.version`.
2. **The app ELF must be position-independent (PIE).** UKC's loader rejects a
   non-PIE binary:
   `[appelfloader] bun: ELF executable is not position-independent! ... Exec format error (-8)`.
   **Vanilla `oven/bun` is NOT PIE**, so the `oven/bun`-based image here boots
   the kraftcloud wrapper but fails to exec bun. A working image must use a
   PIE-built runtime (e.g. base the image on the known-good `datum/bun` rather
   than `oven/bun`, or build bun/the runtime as PIE). **This is the open blocker
   for end-to-end runtime verification.**

## Build + push (kraftkit, the only format UKC boots)

```sh
# kraft needs its own buildkit host:
docker run -d --name buildkit --privileged moby/buildkit:latest

export UKC_METRO="$(kubectl -n unikraft-system get secret ukc-credentials \
  -o go-template='{{index .data "values.yaml"}}' | base64 -d \
  | sed -n 's/.*metro: "\(.*\)"/\1/p')/v1"          # NOTE: /v1 appended
export UKC_TOKEN="$(kubectl -n unikraft-system get secret ukc-credentials \
  -o go-template='{{index .data "values.yaml"}}' | base64 -d \
  | sed -n 's/.*token: "\(.*\)"/\1/p')"             # base64(user:token)

# Builds rootfs from ./Dockerfile, packages kraftcloud/x86_64, pushes to
# index.unikraft.io/datum/<name>. -M (memory) is REQUIRED even with --no-start.
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name config-secret-test \
  --runtime base:latest --rootfs ./Dockerfile .
```

The lab metro pulls `oci://unikraft.io/datum/<name>` ==
**`index.unikraft.io/datum/<name>`** (robot `robot$datum.users.kraftcloud`).
`unikraft.org` (robot `datum2`) authenticates but is NOT authorized to push to
the `datum/` namespace.

## Run the e2e

```sh
kubectl apply -f workload.yaml   # ConfigMap probe-cm + Secret probe-secret + Workload
kubectl get instance -w          # until READY True / Running
```

The Workload mounts `probe-cm` at `/etc/cm` (file `/etc/cm/greeting`) and
`probe-secret` at `/etc/secret` (file `/etc/secret/token`) — a ConfigMap/Secret
volume mountPath is a DIRECTORY; each data key becomes a file under it. It also
injects `GREETING` (configMapKeyRef) and `APP_TOKEN` (secretKeyRef). NOTE:
container-level `resources` is rejected by the compute validating webhook
("not implemented") on the configmap-secret-mounts branch, so it is omitted
(memory defaults to 128Mi in the unikraft-provider).

Observe (the UKC instance name = first label of the pod's
`cloud.unikraft.v1.instances/fqdns` → `app.privateFqdn`):

```sh
POD_NS=$(kubectl get pods -A | awk '/config-secret-probe/{print $1; exit}')
POD=$(kubectl get pods -A | awk '/config-secret-probe/{print $2; exit}')
UKC_NAME=$(kubectl -n "$POD_NS" get pod "$POD" \
  -o jsonpath='{.metadata.annotations.cloud\.unikraft\.v1\.instances/fqdns}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["app"]["privateFqdn"].split(".")[0])')

kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" instance logs "$UKC_NAME" \
  | grep PROBE_RESULT | tail -1
# Assert: /etc/cm/greeting sha256 == sha256("probe-cm-OK"),
#         /etc/secret/token sha256 == sha256("probe-secret-OK"),
#         GREETING/APP_TOKEN sha256 match the same.
```

## Status

- Probe logic: validated locally (sha256 match, secret redaction).
- Image: built + pushed as `index.unikraft.io/datum/config-secret-test:latest`
  in the correct kraftcloud format; boots the unikernel wrapper (`boot time
  ~865ms`) but the `oven/bun` binary is non-PIE so bun fails to exec.
- End-to-end byte-level mount proof: **BLOCKED** on a PIE bun image. Once the
  image uses a PIE runtime, the e2e above completes the assertion.

## Cleanup

```sh
kubectl delete -f workload.yaml          # Workload + probe-cm + probe-secret
docker rm -f buildkit
```
