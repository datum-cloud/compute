# Deploy a Python Web Service on Datum Compute

> Last verified: 2026-06-02 against the `hello-python` example and the live `kraft` / `datumctl compute` CLIs.
> The complete, ready-to-deploy example for this guide lives in [`examples/hello-python/`](../../examples/hello-python/).

This guide walks you through taking a Python HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A Python application packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- Python (for local development only — the build happens inside Docker).

---

## 1. Write the application

Create a project directory and add one file.

**`server.py`**

```python
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def _respond(self, body):
        payload = body.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == "/healthz":
            self._respond("ok\n")
        else:
            self._respond("Hello from Datum (Python)\n")

    def log_message(self, format, *args):
        pass


def main():
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    print("listening on :%d" % port, flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
```

The service listens on `$PORT` (default `8080`), answers `/healthz` with `ok`, and uses only the standard library — no third-party dependencies.

---

## 2. Build and publish the unikernel image with `kraft`

### Why Python ships the interpreter and its library closure

Datum's Unikraft runtime uses an app-elfloader that loads your application as the unikernel entrypoint. Compiled languages (Go, Rust) ship a single fully static binary. Python is different: the CPython interpreter is a **dynamically linked** executable — it needs its loader (`/lib64/ld-linux-x86-64.so.2`) and a set of glibc shared libraries at runtime. The rootfs ships the interpreter, the standard library, the loader, and those shared libraries, and the unikernel runs on the same `base:latest` runtime as Go and Rust.

There is one important constraint to get right. **The unikernel extracts its rootfs into an in-RAM filesystem at boot, so the image must stay small.** Copying the entire `/usr/lib/x86_64-linux-gnu` directory (hundreds of MB of ICU data, Tcl/Tk, X11, static archives) overflows that RAM disk and the boot fails:

```
[libukcpio] ...: Failed to load content: No space left on device (28)
[libposix_vfs_fstab] Failed to extract CPIO to /: -3
```

The fix is to ship **only the precise shared-library closure** the interpreter and its standard-library C extensions actually need. The Dockerfile below computes that closure at build time with `ldd`.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

```dockerfile
FROM python:3.12.11-bookworm AS base

# Stage the exact shared-library closure for the interpreter + the whole stdlib
# C-extension set. Walking ldd over the interpreter, libpython, and every
# lib-dynload/*.so extension captures libraries the stdlib dlopens lazily (e.g.
# libssl/libcrypto via _ssl, liblzma via _lzma) that a hand-written list misses.
# SONAME symlinks are preserved so the loader resolves NEEDED entries.
RUN set -eu; \
    mkdir -p /rootfs-libs; \
    { \
      ldd /usr/local/bin/python3.12; \
      ldd /usr/local/lib/libpython3.12.so.1.0; \
      for f in /usr/local/lib/python3.12/lib-dynload/*.so; do ldd "$f"; done; \
    } 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | grep -E '^/(usr/)?lib' \
      | sort -u > /tmp/sonames.txt; \
    while read -r p; do \
      [ -n "$p" ] || continue; \
      real="$(readlink -f "$p")"; \
      cp -a "$real" "/rootfs-libs/$(basename "$real")"; \
      if [ "$(basename "$p")" != "$(basename "$real")" ]; then \
        ln -sf "$(basename "$real")" "/rootfs-libs/$(basename "$p")"; \
      fi; \
    done < /tmp/sonames.txt; \
    du -sh /rootfs-libs

FROM scratch

# Interpreter + stdlib (the python3.12 dir version MUST match libpython below).
COPY --from=base /usr/local/bin/python3.12 /usr/local/bin/python3
COPY --from=base /usr/local/lib/python3.12 /usr/local/lib/python3.12
COPY --from=base /usr/local/lib/libpython3.12.so.1.0 /usr/local/lib/libpython3.12.so.1.0

# glibc dynamic loader (the program interpreter named in the python ELF header).
COPY --from=base /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, under both default loader search paths so
# NEEDED SONAMEs resolve without an ld.so.cache. (ld.so.cache is intentionally
# NOT copied: it references libraries we did not ship; the loader falls back to
# the default trusted search paths, where these libraries live.)
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.py /server.py
```

> **Note:** pin the interpreter patch version (`python:3.12.11-bookworm`). The copied `/usr/local/lib/python3.12` standard-library tree must match `libpython3.12.so.1.0`; a version skew between the two breaks imports.

### Write the Kraftfile

```yaml
spec: v0.6

runtime: base:latest

rootfs: ./Dockerfile

cmd: ["/usr/local/bin/python3", "/server.py"]
```

`runtime: base:latest` is the Unikraft Cloud app-elfloader runtime. `rootfs: ./Dockerfile` tells `kraft` to build the rootfs from your Dockerfile.

### Start a BuildKit daemon

`kraft` uses BuildKit to build the rootfs. Start one if you don't already have one running:

```sh
docker run -d --name buildkit --privileged moby/buildkit:latest
```

### Build and publish with `kraft cloud deploy --no-start`

Use `kraft` only to build and publish the image — you deploy the running workload with `datumctl compute` in the next step. The `--no-start` (`-S`) flag builds the unikernel package and pushes it to the metro registry **without** starting an instance. It pushes to `index.unikraft.io/datum/<name>`. The `-M` flag sets the memory allocation in MiB and is required — Python needs more than the static Go/Rust binaries, so use at least `1024`.

```sh
export KRAFTKIT_NO_CHECK_UPDATES=true

kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 1024 --name hello-python \
  --runtime base:latest --rootfs ./Dockerfile .
```

After this command completes, your image is available at `index.unikraft.io/datum/hello-python:latest`, ready for Datum compute to deploy.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-python
  labels:
    app: hello-python
spec:
  template:
    metadata:
      labels:
        app: hello-python
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-python:latest
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
datumctl compute deploy hello-python \
  --image=index.unikraft.io/datum/hello-python:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-python
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
# -> Hello from Datum (Python)

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
  deploy --no-start -M 1024 --name hello-python \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-python \
  --image=index.unikraft.io/datum/hello-python:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-python
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-python -y

# Stop the local BuildKit daemon.
docker rm -f buildkit
```

---

## Troubleshooting

### The image fails to boot: "No space left on device"

```
[libukcpio] ...: Failed to load content: No space left on device (28)
[libposix_vfs_fstab] Failed to extract CPIO to /: -3
```

The rootfs is too large for the unikernel's in-RAM filesystem. This happens if you copy the whole `/usr/lib/x86_64-linux-gnu` directory instead of the trimmed library closure. Use the `ldd`-driven closure in the Dockerfile above (which ships ~20–30 MB of libraries, not hundreds), and avoid copying `.a` static archives, ICU data, or GUI toolkits you don't use.

### The application fails to import a module: missing shared library

If the console shows an `ImportError` about a missing `.so` when your code imports a standard-library module (or a dependency), that library was not included in the closure. Add it by re-running `ldd` over the relevant `lib-dynload/*.so` (or your dependency's compiled extension) and confirming it lands in `/rootfs-libs`. Third-party packages with **C extensions** (numpy, cryptography, …) ship their own `.so` files that may link further system libraries — those must be present in the rootfs too. Pure-Python dependencies need nothing extra.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints your `listening on :8080` line. A boot error (the rootfs-size or missing-library cases above) appears here. The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

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
