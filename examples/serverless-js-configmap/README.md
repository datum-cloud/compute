# serverless-js-configmap

A serverless pattern on Datum compute: a **generic Node.js runtime image with no
application code baked in**, where the function (`index.js`) is supplied at
deploy time via a **ConfigMap mounted at `/app`**, and Node executes the mounted
file. Swapping the function is a ConfigMap edit + restart — **no image rebuild**.

This works because Datum ConfigMap/Secret file mounts materialize into the
unikraft microVM as read-only `rom` devices when the image is packaged with
`spec: v0.7`, `runtime: base-compat:latest`, and `rootfs.format: erofs`.

## Files

| File | Purpose |
|------|---------|
| `Dockerfile` | Multi-stage build of the **generic** runtime: a `scratch` image carrying only the `node` interpreter + its musl/libgcc/libstdc++ `.so` files. No app code. |
| `Kraftfile` | `spec: v0.7`, `base-compat:latest`, `rootfs.format: erofs`, `cmd: ["/usr/bin/node", "/app/index.js"]` (a **mounted** path). |
| `index.js` | The function and the **single source of truth** for the ConfigMap: a dependency-free Node stdlib HTTP server on `$PORT` (default 8080). Returns a versioned message; logs a versioned startup line. Mounted into the image, **not** baked in. |
| `kustomization.yaml` | A Kustomize `configMapGenerator` that generates the `js-function` ConfigMap from `index.js` (so there is no hand-maintained copy of the function to drift). `disableNameSuffixHash: true` keeps the name stable for the Workload reference. |
| `workload.yaml` | A `compute.datumapis.com/v1alpha` Workload mounting `js-function` at `/app`, port 8080, `d1-standard-2`, DFW, `minReplicas: 1`. Env intentionally empty (see Pitfalls). |

## 1. Build + push the generic runtime image (push-only)

```sh
export KRAFTKIT_NO_CHECK_UPDATES=true
TOKEN=...   # ash-sore-hamster metro token (ukc-credentials secret)

kraft cloud --metro https://api.ash-sore-hamster.unikraft.cloud/v1 --token "$TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name serverless-js-runtime \
  --runtime base-compat:latest --rootfs ./Dockerfile .
```

Packages as an EroFS archive and pushes to
`index.unikraft.io/datum/serverless-js-runtime:latest`. This image is built
**once** and reused for every function version.

## 2. Generate + apply the ConfigMap on the PROJECT control plane

`kubectl apply -k` runs the `configMapGenerator`, building the `js-function`
ConfigMap from `index.js` and applying it. The ReferencedData resolver that
turns ConfigMap data into the unikernel mount runs project-side, so apply it on
the project plane — applying on the cell is a dead end.

```sh
kubectl --context datum-project-datum-cloud -n default apply -k .
```

(`kubectl kustomize .` renders the generated ConfigMap to stdout if you want to
inspect it first.)

## 3. Deploy the Workload

```sh
datumctl compute deploy -f workload.yaml -y
```

## 4. Validate

```sh
# External FQDN (also wakes the instance from scale-to-zero standby):
curl https://<external-fqdn>/            # -> Hello from a ConfigMap-mounted function — v1
curl https://<external-fqdn>/healthz     # -> ok

# UKC instance: confirm the JS mount is a rom at /app and the VM booted:
kraft cloud --metro <metro>/v1 --token "$TOKEN" instance get <ukc> -o json   # roms[].at == /app, boot_time_us > 0
kraft cloud --metro ash-sore-hamster --token "$TOKEN" instance logs <ukc>    # "mount /dev/ukp_romN -> /app" + "serverless-js: vN listening on :8080"
```

Derive `<ukc>` from the cell Pod annotation
`cloud.unikraft.v1.instances/fqdns` → first DNS label of `privateFqdn`.

## 5. Swap the function (the headline)

Edit `index.js` directly (bump `VERSION` to `v2` and the message) — it is the
only copy. Re-apply with `-k` to regenerate the ConfigMap, then recreate the
instance so the new read-only `rom` is baked at boot:

```sh
kubectl --context datum-project-datum-cloud -n default apply -k .
datumctl compute restart serverless-js-fn        # rolling restart -> recreate instance
curl https://<external-fqdn>/                    # -> ... — v2   (same image, no rebuild)
```

Mounts are read-only roms baked at boot, so a content change only takes effect
once the instance is **recreated** — restarting the workload is the realistic
"redeploy the function" path. The runtime image is never rebuilt or repushed.

## Pitfalls

- **`base-compat:latest`, not `base:latest`** — `node` is a dynamic musl ELF and
  needs the dynamic-loader runtime plus the copied `.so` files. A `scratch`
  image missing those libs won't boot.
- **Keep `env` empty.** In a busy namespace, kraftlet-injected k8s service-link
  env can overflow the UKC kernel-cmdline buffer (`InvalidKernelCommandLine`,
  boot `0.00 ms` / standby). The mount needs no env; `PORT` defaults to 8080.
- **The mount is read-only.** The function must not write into `/app`.
