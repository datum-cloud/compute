# Mount ConfigMaps and Secrets into Compute Instances

> Last verified: 2026-06-03 on the `ash-sore-hamster` lab metro against the live `datumctl compute` and `kraft` CLIs.

This guide shows how to deliver configuration files and secret material to a running instance — without baking them into the image. You create a `ConfigMap` or `Secret` in your project, reference it by name from a `Workload`, and the platform delivers the data to every cell where the instance is placed. By the end you will know how to:

- Mount a ConfigMap or Secret as **files** at a path inside the instance (volumes)
- Inject ConfigMap/Secret keys as **environment variables**
- Update a value and roll instances to pick it up
- Diagnose an instance that is held waiting on its data

**What you need before starting:**

- `datumctl` with the compute plugin, authenticated to your Datum Cloud project.
- A project with a `default` Network provisioned (the usual workload prerequisite).
- For **file mounts**: an image built on the `base-compat:latest` runtime with an erofs rootfs — see [The runtime requirement](#the-runtime-requirement-base-compatlatest--erofs) below. Environment-variable injection works on any runtime.

---

## How referenced data reaches your instance

You only ever touch two things: a `ConfigMap`/`Secret` in your project, and a `Workload` that references it by name. Everything between is the platform's job.

```
┌────────────────────────────────────────────┐
│ Your project plane                         │
│   ConfigMap  /  Secret                     │
│   Workload  (references them by name)      │
└────────────────────────────────────────────┘
                      |
              resolve + deliver
                      v
┌────────────────────────────────────────────┐
│ Edge cell  (where the instance runs)       │
│   companion ConfigMap / Secret             │
│   Instance  (gated until data is ready)    │
│     +- runtime mounts files / injects env  │
└────────────────────────────────────────────┘
```

The referenced object lives in your project; the instance runs on an edge cell, potentially thousands of miles away. A management-plane resolver reads the referenced object with a trusted, scoped identity, materializes a derived **companion** copy, and routes it to each cell where the instance is placed. The runtime then mounts the companion natively — as files, as env vars, or both.

Three properties fall out of this design and are worth internalizing:

- **You reference by name; bytes never enter the spec.** The `Workload` and `Instance` you see carry references only. Secret values travel as `Secret` objects on the delivery path and are mounted on the cell — they never appear in anything projected back to you.
- **You must be able to read what you reference.** Admission verifies the submitting user can read each referenced ConfigMap/Secret — the resolver's system identity is never the authority. You cannot pull in an object you couldn't read yourself.
- **An instance is held until its data is present.** An instance that references any ConfigMap/Secret is gated by `ReferencedDataReady` and will not launch with its data missing — see [Diagnose a held instance](#diagnose-a-held-instance).

The full design — resolver, companions, federation routing, and the scheduling gate — is in the [ConfigMap/Secret mounts RFC](../compute/development/rfcs/configmap-secret-mounts.md).

---

## Mount a ConfigMap or Secret as files

A ConfigMap/Secret volume mounts at a path **as a directory**: each data key becomes one file under the mount path, with the value as its contents. The mount is **read-only**.

### The runtime requirement: `base-compat:latest` + erofs

> [!IMPORTANT]
> Inline file mounts are only enabled on the **`base-compat:latest`** runtime with an **erofs** rootfs today. On the default `base:latest` runtime a ConfigMap/Secret volume will not appear inside the instance at all.

This is the part to get right. File mounts are delivered into the unikernel as inline read-only ROM devices, and inline ROMs are only enabled on the `base-compat:latest` runtime today. The default `base:latest` runtime (the app-elfloader) does not yet support them — the kernel team is working on enabling inline ROMs for `base` as well, but until then a ConfigMap/Secret volume will simply not appear inside a `base:latest` instance.

Build the image with the `base-compat:latest` runtime and an **erofs** rootfs. The `Kraftfile`:

```yaml
spec: v0.7

runtime: base-compat:latest

rootfs:
  source: ./Dockerfile
  format: erofs

cmd: ["/usr/bin/node", "/app/index.js"]
```

Two things differ from a plain `base:latest` image:

- **`runtime: base-compat:latest`** is the compatibility runtime that ships a dynamic loader, so it runs ordinary dynamically linked musl ELF binaries (e.g. `node`, `python`) — and it is the runtime where inline ROM mounts are enabled. `base:latest` requires a static-PIE binary and has no ROM-mount support yet.
- **`rootfs.format: erofs`** packages the rootfs as an erofs image. The mounted ConfigMap/Secret arrives as an additional read-only ROM device alongside it.

> [!TIP]
> A complete, runnable image built this way — a generic Node.js runtime that executes a function supplied entirely via a mounted ConfigMap — is in [`examples/serverless-js-configmap/`](../../examples/serverless-js-configmap/). Use it as a reference for the `Dockerfile`/`Kraftfile` shape.

### Create the source objects

Create a ConfigMap and a Secret in your project namespace:

```sh
kubectl apply -f - <<'YAML'
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  app.conf: |
    log_level = info
    feature_flags = beta
---
apiVersion: v1
kind: Secret
metadata:
  name: app-tls
stringData:
  tls.crt: |
    -----BEGIN CERTIFICATE-----
    ...
    -----END CERTIFICATE-----
  tls.key: |
    -----BEGIN PRIVATE KEY-----
    ...
    -----END PRIVATE KEY-----
YAML
```

### Define the volume and attach it

In the `Workload`, a volume is declared once under `spec.template.spec.volumes`, then attached to a container at a mount path under `runtime.sandbox.containers[].volumeAttachments`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: configured-app
spec:
  template:
    spec:
      volumes:
        - name: config-vol
          configMap:
            name: app-config          # ConfigMap volumes use `name`
        - name: tls-vol
          secret:
            secretName: app-tls        # Secret volumes use `secretName`
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/configured-app:latest
              ports:
                - name: http
                  port: 8080
                  protocol: TCP
              volumeAttachments:
                - name: config-vol
                  mountPath: /etc/app
                - name: tls-vol
                  mountPath: /etc/tls
      networkInterfaces:
        - network:
            name: default
  placements:
    - name: default
      cityCodes: [DFW]
      scaleSettings:
        minReplicas: 1
        instanceManagementPolicy: OrderedReady
```

Deploy it:

```sh
datumctl compute deploy -f workload.yaml -y
```

Inside the running instance this produces:

```
/etc/app/app.conf  # from ConfigMap key app.conf (read-only)
/etc/tls/tls.crt   # from Secret key tls.crt     (read-only)
/etc/tls/tls.key   # from Secret key tls.key     (read-only)
```

A few details that trip people up:

- **The mount path is a directory, not a file.** `mountPath: /etc/app` gives you `/etc/app/<key>` per data key — it does not write the whole ConfigMap to a single file at `/etc/app`.
- **ConfigMap uses `name`; Secret uses `secretName`.** They are different fields (the volume sources are the Kubernetes `ConfigMapVolumeSource` / `SecretVolumeSource` types).
- **`volumeAttachments` is per container**, even though the volume is declared once at the spec level. Attach the same volume into multiple containers if more than one needs it.
- **The mount is read-only.** The instance cannot write back to a mounted ConfigMap/Secret.

To select or rename specific keys, set `defaultMode`, or pick a subset, use the standard volume-source fields (`items` for key→path mapping and per-file mode, `defaultMode` for the directory default). See the [Workload API reference](../api/workloads.md) for the full `configMap`/`secret` volume source shape.

---

## Inject keys as environment variables

For the twelve-factor case — a value the app reads from the environment — reference a single key with `configMapKeyRef` or `secretKeyRef`:

```yaml
runtime:
  sandbox:
    containers:
      - name: app
        image: index.unikraft.io/datum/configured-app:latest
        env:
          - name: LOG_LEVEL
            valueFrom:
              configMapKeyRef:
                name: app-config
                key: log_level
          - name: API_TOKEN
            valueFrom:
              secretKeyRef:
                name: api-credentials
                key: token
```

The same delivery path backs both forms — once the companion is present on the cell, the runtime injects the env var natively from it, exactly as it mounts a file. Env injection is **not** restricted to `base-compat:latest`; it works on any runtime.

> [!WARNING]
> **Env capacity in busy cells.** Environment variables are passed to the unikernel through the kernel command line, which has a fixed capacity. In a busy cell, Kubernetes service-link injection (`*_SERVICE_HOST` / `*_PORT_*` for every sibling Service) can add several KB of env on its own and overflow that buffer, surfacing as a boot failure:
>
> ```
> RunWithoutApiError(... InvalidKernelCommandLine("Invalid cmdline capacity provided."))
> ```
>
> This is a platform limitation, not a defect in your workload — the same overflow reproduces with a known-good image once enough env is present. If you hit it, keep your own env minimal; the long-term fix (passing env via initrd/file rather than the kernel cmdline, and disabling service links) is tracked separately.

---

## Update a value (rotation and restart)

When you edit a referenced ConfigMap/Secret, the resolver re-reads it and refreshes the companion at the edge, so the latest values are staged for the next instance launch. **Running instances are not rolled automatically** — a fleet-wide restart on every edit would be surprising, and a running instance's environment is not mutated in place regardless.

To pick up a changed value, roll the instances:

```sh
# 1. Edit the source object.
kubectl edit configmap app-config

# 2. Roll the workload's instances to pick up the refreshed data.
datumctl compute restart configured-app
```

The restart reuses compute's existing ordered, in-place rolling-update path — a conventional restart annotation on the template rolls the instances, which come up against the refreshed companion. There is no automatic-roll-on-change behavior; that is a possible future opt-in.

For file mounts specifically, the mounted ROM is fixed at boot, so a new value only takes effect on a freshly launched instance — which is exactly what the restart produces.

---

## Diagnose a held instance

An instance that references any ConfigMap/Secret is held by a `ReferencedDataReady` scheduling gate until exactly the expected set of companions is present on its cell. This is deliberate: an instance is never launched with its data missing. Inspect it with:

```sh
datumctl compute instances describe <instance-name>
```

Look at the `ReferencedDataReady` condition. The reason tells you what is happening:

| Reason                | Meaning                                                                 |
| --------------------- | ----------------------------------------------------------------------- |
| `Resolving`           | The resolver is reading the referenced objects.                         |
| `AwaitingPropagation` | The companion is en route to the cell — a normal transient during placement. |
| `SourceNotFound`      | A referenced ConfigMap/Secret does not exist. Check the name and namespace. |
| `SourceUnauthorized`  | The submitting user could not read a referenced object. Fix RBAC, or reference an object you can read. |
| `SourceTooLarge`      | A referenced object exceeds the companion size limit.                   |
| `Ready`               | All expected companions are present; the gate is cleared.               |

`AwaitingPropagation` clearing on its own within a few seconds is the normal happy path. A reason that sticks (`SourceNotFound`, `SourceUnauthorized`, `SourceTooLarge`) names the offending object and needs your action — it is a diagnosable hold, not a silent hang. Optional sources (`optional: true` on the volume/env reference) are skipped rather than held.

---

## Image pull secrets

Pulling an image from a private registry needs the same machinery — deliver a referenced `Secret` to the cell where the instance runs — so image pull credentials build on this exact delivery path as a later consumer rather than a separate mechanism. See the [ConfigMap/Secret mounts RFC](../compute/development/rfcs/configmap-secret-mounts.md), which sequences pull-secret support on top of the resolver this guide describes.

---

## Troubleshooting

### A mounted ConfigMap/Secret directory is empty inside the instance

The most common cause is the runtime. Inline file mounts are only enabled on `base-compat:latest` with an erofs rootfs today; on `base:latest` the volume simply does not appear. Confirm the image's `Kraftfile` has `runtime: base-compat:latest` and `rootfs.format: erofs`, rebuild, and redeploy. See [The runtime requirement](#the-runtime-requirement-base-compatlatest--erofs).

If the runtime is correct but a specific file is missing, check that the data key exists on the source object — each key becomes one file, so a missing key means a missing file.

### The instance is stuck and never becomes `Running`

Read the `ReferencedDataReady` condition with `datumctl compute instances describe <instance-name>` and match the reason against the [table above](#diagnose-a-held-instance). `SourceNotFound` and `SourceUnauthorized` are the usual culprits — a typo'd name, the object in the wrong namespace, or an RBAC gap that prevented the admission read.

### A workload with env references fails to boot with `InvalidKernelCommandLine`

The kernel command line overflowed — see the [env capacity limitation](#inject-keys-as-environment-variables). Reduce your own environment footprint; in a busy cell, service-link env alone can be enough to tip it over.

### I edited the ConfigMap/Secret but the instance still serves the old value

Running instances are not rolled automatically. Run `datumctl compute restart <workload>` to roll them onto the refreshed companion. See [Update a value](#update-a-value-rotation-and-restart).

---

## References

- [ConfigMap/Secret mounts RFC](../compute/development/rfcs/configmap-secret-mounts.md) — the delivery design (resolver, companions, scheduling gate, rotation).
- [Workload API reference](../api/workloads.md) — `spec.template.spec.volumes[]` and `runtime.sandbox.containers[].volumeAttachments[]`.
- [Instance API reference](../api/instances.md) — the same volume/attachment shapes on the resulting Instance.
- [`examples/serverless-js-configmap/`](../../examples/serverless-js-configmap/) — a runnable `base-compat:latest` + erofs example that delivers application code via a mounted ConfigMap.
- [`examples/config-secret-probe/`](../../examples/config-secret-probe/) — a probe that verifies, byte-for-byte, that referenced ConfigMap/Secret data lands as mounted files and injected environment variables.
