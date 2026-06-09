# Deploy a Node.js Web Service on Datum Compute

> Last verified: 2026-06-02 against the `hello-node` example and the live `kraft` / `datumctl compute` CLIs.
> The complete, ready-to-deploy example for this guide lives in [`examples/hello-node/`](../../examples/hello-node/).

This guide walks you through taking a Node.js HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A Node.js application packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- Node.js (for local development only — the build happens inside Docker).

---

## 1. Write the application

Create a project directory and add two files.

**`app.js`**

```js
const http = require('http');

const port = parseInt(process.env.PORT, 10) || 8080;

const server = http.createServer((req, res) => {
  if (req.url === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('ok\n');
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('Hello from Datum (Node)\n');
});

server.listen(port, () => {
  console.log('listening on :' + port);
});
```

**`package.json`**

```json
{
  "name": "hello-node",
  "version": "1.0.0",
  "private": true,
  "main": "app.js",
  "scripts": {
    "start": "node app.js"
  }
}
```

The service listens on `$PORT` (default `8080`), answers `/healthz` with `ok`, and has no external dependencies.

---

## 2. Build and publish the unikernel image with `kraft`

### Why Node runs on the `base-compat` runtime

Datum's Unikraft runtime uses an app-elfloader that loads your application as the unikernel entrypoint. Compiled languages (Go, Rust) ship a single fully static binary on the `base:latest` runtime. Node is different: the `node` interpreter is a **dynamically linked** executable — it needs its loader (`ld-musl`) and a few shared libraries present at boot.

For that, Node uses the **`base-compat:latest`** runtime (the binary-compatibility variant of the elfloader) and the rootfs ships the `node` interpreter together with the shared libraries it links. With the loader and libraries present, the dynamic executable boots.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

The build installs your dependencies in a regular `node` image, then assembles a minimal `FROM scratch` rootfs containing the interpreter, your app, and exactly the shared libraries `node` needs:

The build stage is pinned to `linux/amd64` because the target is x86_64 (`base-compat` is `kraftcloud/x86_64`) and the rootfs copies x86_64 musl libraries. On an arm64 host (Apple Silicon) this cross-builds to x86_64; without the pin the `ld-musl-x86_64.so.1` COPYs fail or a wrong-architecture binary boots.

```dockerfile
FROM --platform=linux/amd64 node:22-alpine AS build
WORKDIR /usr/src
COPY package*.json ./
RUN npm install
# npm install with zero deps creates no node_modules dir; ensure it exists so the
# COPY below succeeds and adding deps later needs no Dockerfile change.
RUN mkdir -p node_modules
COPY app.js ./
# Record node's dynamic-library requirements in the build log for auditing.
RUN echo "=== ldd node ===" && ldd /usr/local/bin/node || true

FROM scratch
# The node interpreter.
COPY --from=build /usr/local/bin/node /usr/bin/node
# musl dynamic loader + libc (ld-musl is both loader and libc on alpine).
COPY --from=build /lib/ld-musl-x86_64.so.1 /lib/ld-musl-x86_64.so.1
# C++/GCC runtime libraries node links against.
COPY --from=build /usr/lib/libgcc_s.so.1   /usr/lib/libgcc_s.so.1
COPY --from=build /usr/lib/libstdc++.so.6  /usr/lib/libstdc++.so.6
COPY --from=build /etc/os-release          /etc/os-release
# Application + dependency tree.
COPY --from=build /usr/src/node_modules /usr/src/node_modules
COPY --from=build /usr/src/app.js       /usr/src/server.js
```

> **Note:** the scratch image has no package manager, so every shared library `node` links must be copied explicitly — a missing `.so` makes the instance fail to boot. The `ldd /usr/local/bin/node` line in the build log shows exactly which libraries are needed; for stock `node:22-alpine` the three above are the full set.

### Write the Kraftfile

```yaml
spec: v0.7

name: hello-node

runtime: base-compat:latest

rootfs:
  source: ./Dockerfile

cmd: ["/usr/bin/node", "/usr/src/server.js"]
```

`runtime: base-compat:latest` is the binary-compatibility elfloader runtime that loads the dynamic `node` executable. `rootfs.source: ./Dockerfile` tells `kraft` to build the rootfs from your Dockerfile.

> **Warning — use a CPIO rootfs, not erofs.** The rootfs must be packaged as a **CPIO** initramfs. An **erofs** initrd does NOT boot on `base-compat:latest`: the instance fails with an instant platform assertion `(i0 EXP)` at `0.00ms` and **no console logs** at all. If you see that symptom — an immediate exit with no boot output — your image was packaged as erofs. Omitting `rootfs.format` leaves `kraft` on CPIO; the build command below also passes `--rootfs-type cpio` explicitly.

### Start a BuildKit daemon

`kraft` uses BuildKit to build the rootfs. Start one if you don't already have one running:

```sh
docker run -d --name buildkit --privileged moby/buildkit:latest
```

### Build and publish with `kraft cloud deploy --no-start`

Use `kraft` only to build and publish the image — you deploy the running workload with `datumctl compute` in the next step. The `--no-start` (`-S`) flag builds the unikernel package and pushes it to the metro registry **without** starting an instance. The `--rootfs-type cpio` flag packages the rootfs as a CPIO initramfs (an erofs rootfs does not boot — see the warning above). The `-M` flag is a build-time memory hint; it does **not** affect boot success, and the effective runtime memory is set by the Workload `instanceType`, not by `-M`.

```sh
export KRAFTKIT_NO_CHECK_UPDATES=true

kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start --rootfs-type cpio -M 512 --name hello-node \
  --runtime base-compat:latest --rootfs ./Dockerfile .
```

After this command completes, your image is ready for Datum compute to deploy.

> **Registry: publish vs. pull.** The build may publish to the metro registry (`index.<metro>.unikraft.cloud/datum/...`) while the cell pulls from `index.unikraft.io/datum/...`. The Workload should reference `index.unikraft.io/datum/<name>:<tag>`; if that reference does not resolve at deploy time, an `unikraft image copy` (or equivalent) may be needed to make the published image available under the `index.unikraft.io` path.

---

## Image size: stay under the initrd boot ceiling

There is an empirical **~150 MiB** ceiling on the initrd (rootfs) for it to boot. Working images land around 125–145 MiB; images over ~570 MiB fail to boot with the same `(i0 EXP)` assertion described above. The `-M` memory flag does **not** raise this ceiling — it is a property of the initrd, not the runtime memory.

The `hello-node` example is tiny, but a real application must keep its rootfs small:

- Bundle the server to a single file (e.g. with [esbuild](https://esbuild.github.io/)) so you do not have to ship `node_modules`.
- Drop dev and build-only dependencies; copy only the built output into the `FROM scratch` stage, never the source tree or the full `node_modules`.
- Verify the final image size before deploying; if it approaches the ceiling, slim further.

> The CPIO initramfs rootfs is RAM-backed: it is writable at runtime but **ephemeral** — nothing written at runtime persists across reboots or redeploys.

## Deploying a framework / SSR app

The example ships a single dependency-free `app.js`. A real framework app (Astro, Next.js SSR, etc.) needs a few extra steps:

1. **Run the framework build** (`npm run build`) in the build stage to produce the server bundle plus the client/static assets.
2. **Ship a small server entrypoint** that serves the static assets *and* invokes the framework's SSR handler.
3. **Copy only the built output** into the `FROM scratch` rootfs — not the source tree, and not the full `node_modules`. Combined with bundling, this keeps you under the size ceiling above.
4. **Watch the build toolchain.** A repo's `build` script may invoke a tool that is not present in `node:22-alpine` (for example `bun`). Invoke the toolchain directly, or install it in the build stage, rather than relying on the repo's wrapper script.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-node
  labels:
    app: hello-node
spec:
  template:
    metadata:
      labels:
        app: hello-node
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-node:latest
              ports:
                - name: http
                  port: 8080
                  protocol: TCP
      networkInterfaces:
        - network:
            name: default
  placements:
    - name: default
      cityCodes:
        - DFW
      scaleSettings:
        minReplicas: 1
        instanceManagementPolicy: OrderedReady
```

Deploy it:

```sh
datumctl compute deploy -f workload.yaml -y
```

### Option B — flags

```sh
datumctl compute deploy hello-node \
  --image=index.unikraft.io/datum/hello-node:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

### Environment variables and secrets

Inject configuration and secrets through the Workload's `env` list, sourcing values from a Secret with `valueFrom.secretKeyRef`. The referenced Secret must **already exist** in the project's `default` namespace; the platform's referenced-data resolver creates a companion and propagates it to the cell.

```yaml
containers:
  - name: app
    image: index.unikraft.io/datum/hello-node:latest
    env:
      - name: API_TOKEN
        valueFrom:
          secretKeyRef:
            name: app-secrets
            key: api-token
```

Env values ride on the kernel command line, which has room for a multi-KB value (a PEM certificate fits).

> **Use env-from-secret, not file mounts, on this runtime.** Mounting a Secret or ConfigMap as a file (a volume mount) requires an **erofs** rootfs, and erofs does not boot on `base-compat:latest` (the `(i0 EXP)` failure described above). On the CPIO runtime, inject configuration via `env` / `secretKeyRef`, not file mounts.

**Troubleshooting.** If a Secret-referencing Workload is rejected by the `vworkload` admission webhook with a SubjectAccessReview error, or the companion Secret never appears (the instance reports `MandatorySecretNotFound`), the platform's hub RBAC (compute-manager access to `secrets`/`configmaps`) and the project plane's discovery of the `authorization.k8s.io` API group must be in place. Contact your platform operator.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-node
```

A healthy instance shows `Ready: true` and `Running`. The `EXTERNAL IP` column is populated once the instance is live.

For a detailed view of a single instance, including conditions and any failure reason:

```sh
datumctl compute instances describe <instance-name>
```

Once the instance is `Running`, curl the external endpoint. UKC fronts the service with TLS on port 443 and redirects plain HTTP on port 80:

```sh
# Get the external IP or hostname from the instance list, then:
curl https://<EXTERNAL-IP>/
# -> Hello from Datum (Node)

curl https://<EXTERNAL-IP>/healthz
# -> ok
```

Use `-k` if the TLS certificate is self-signed in your metro:

```sh
curl -k https://<EXTERNAL-IP>/
```

---

## 5. Update the workload

To deploy a new version, rebuild and publish the image (repeating step 2), then redeploy. Using the manifest:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start --rootfs-type cpio -M 512 --name hello-node \
  --runtime base-compat:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-node \
  --image=index.unikraft.io/datum/hello-node:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-node
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-node -y

# Stop the local BuildKit daemon.
docker rm -f buildkit
```

---

## Troubleshooting

### The image fails to boot: missing shared library

If the unikernel console shows a library-not-found error at boot, the rootfs is missing a shared library that `node` (or one of your dependencies) needs. The scratch image has no package manager, so every `.so` must be copied in explicitly. Check:

- The `ldd /usr/local/bin/node` output in the build log lists the libraries `node` itself needs — confirm each is copied into the scratch stage.
- If you added a **native (node-gyp) addon**, it compiles to additional `.so` files with their own library dependencies. Run `ldd` over the addon's `.node`/`.so` files and copy any libraries they pull in. Pure-JS dependencies need nothing extra.
- The image was built with `kraft cloud deploy`, not plain `docker build`.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints your `listening on :8080` line. A library-not-found error means a `.so` is missing from the rootfs (see above). The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

### Image pull failures on the instance

`datumctl compute instances describe <instance-name>` reports a condition with reason `ImageUnavailable` when the platform cannot pull the image. Confirm:

- The image was pushed to `index.unikraft.io/datum/<name>` (the metro registry), not to an external container registry like GHCR or Docker Hub. The platform pulls from the UKC metro registry.
- The `kraft cloud deploy` command completed without errors and printed the image reference.
- The image name in `workload.yaml` matches exactly what `kraft cloud deploy` reported, including the `latest` tag.

### Instance is stuck and not progressing

```sh
datumctl compute instances describe <instance-name>
```

Look at the conditions in the output. Common states:

- `QuotaGranted: False` — compute quota has not been provisioned for the project. Contact your platform operator.
- `Programmed: False` — the instance has not been scheduled to a node yet. This is normal for a few seconds after deploy; if it persists, check that the city code in your workload matches an available location.
- `Ready: False, reason: SchedulingGatesPresent` — a scheduling prerequisite (such as a network) has not been satisfied. Confirm your project has a `default` Network resource provisioned.
