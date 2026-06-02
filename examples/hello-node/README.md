# hello-node

A minimal Node.js HTTP service packaged as a Unikraft unikernel and deployed on
Datum compute. It responds `Hello from Datum (Node)` on `/` and `ok` on
`/healthz`, listening on `$PORT` (default `8080`).

This is the runnable companion to the step-by-step guide:
[Deploy a Node.js Web Service on Datum Compute](../../docs/guides/deploy-a-node-app.md).

Unlike the [Go](../hello-go/) and [Rust](../hello-rust/) examples — which are static
binaries on the `base:latest` runtime — Node is a dynamic musl ELF. It runs on the
**`base-compat:latest`** runtime (the binary-compatibility / dynamic-loader variant
of the elfloader), and the rootfs ships the `node` interpreter plus every shared
library it links.

## Files

- `app.js` — the service (standard library only, no dependencies).
- `package.json` — package metadata.
- `Dockerfile` — multi-stage build: an `node:22-alpine` stage installs deps and
  records `ldd node`, then a `FROM scratch` rootfs copies the interpreter and its
  shared libraries (`ld-musl`, `libstdc++`, `libgcc_s`).
- `Kraftfile` — packages the rootfs on the `base-compat:latest` runtime (`erofs`).
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-node \
  --runtime base-compat:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-node
curl https://<EXTERNAL-IP>/
```

See the [guide](../../docs/guides/deploy-a-node-app.md) for the general workflow,
prerequisites, and troubleshooting — the build/publish/deploy mechanics are the
same; only the runtime (`base-compat:latest`) and the libraries copied into the
rootfs differ.

## Note on dependencies

`npm install` with zero dependencies creates no `node_modules` directory, so the
Dockerfile runs `mkdir -p node_modules` before the `COPY`. When you add real
dependencies they are installed in the build stage and copied into the rootfs with
no Dockerfile change. Native (node-gyp) addons need their own compiled `.so` files
copied into the scratch rootfs as well — pure-JS dependencies need nothing extra.
