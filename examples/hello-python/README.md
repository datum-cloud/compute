# hello-python

A minimal Python HTTP service packaged as a Unikraft unikernel and deployed on
Datum compute. It responds `Hello from Datum (Python)` on `/` and `ok` on
`/healthz`, listening on `$PORT` (default `8080`). Standard library only
(`http.server`), no third-party dependencies.

This is the runnable companion to the step-by-step guide:
[Deploy a Python Web Service on Datum Compute](../../docs/guides/deploy-a-python-app.md).

## How this differs from hello-go / hello-rust

The Go and Rust examples ship a single fully static **PIE** binary with no
interpreter — the app *is* the unikernel entrypoint. CPython is different: it is
a **dynamically-linked glibc ELF** that needs its dynamic loader
(`/lib64/ld-linux-x86-64.so.2`) and a set of glibc shared objects at runtime.

This example proves that the `base:latest` app-elfloader runtime **does** boot a
dynamic glibc executable, provided the loader and every needed `.so` are present
in the rootfs (Variant A: reuse the same `base:latest` runtime as Go/Rust).

Two things are load-bearing in the `Dockerfile`:

1. **Ship the loader + the shared-library closure.** The rootfs includes the
   interpreter, the full stdlib, the glibc loader, and the closure of shared
   libraries that the interpreter *and* its stdlib C extension modules
   (`lib-dynload/*.so`) dlopen at runtime (e.g. `libz`, `libbz2`, `liblzma`,
   `libssl`/`libcrypto`). The closure is computed at build time by walking `ldd`.
2. **Ship ONLY that closure, not the whole `/usr/lib/x86_64-linux-gnu`.** The
   unikernel extracts its rootfs into an in-RAM filesystem at boot. Copying the
   entire glibc lib directory (358 MB: ICU data, tcl/tk, X11, static `.a`
   archives) overflows that RAM disk and the boot **fails** with
   `[libukcpio] ...: No space left on device (28)` /
   `[libposix_vfs_fstab] Failed to extract CPIO to /: -3`. The trimmed closure is
   ~21 MB and boots in tens of milliseconds.

The interpreter patch version is pinned (`python:3.12.11-bookworm`) so the copied
`/usr/local/lib/python3.12` stdlib tree matches `libpython3.12.so.1.0`.

## Files

- `server.py` — the service (stdlib `http.server` only).
- `Dockerfile` — multi-stage build; stage 1 stages the exact shared-library
  closure, stage 2 is a `FROM scratch` rootfs with the interpreter, stdlib,
  loader, and that closure.
- `Kraftfile` — runs `["/usr/local/bin/python3", "/server.py"]` on `base:latest`.
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
#    -M 1024: Python needs more memory than the static Go/Rust binaries.
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 1024 --name hello-python \
  --runtime base:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-python
curl -k https://<EXTERNAL-IP>/          # -> Hello from Datum (Python)
curl -k https://<EXTERNAL-IP>/healthz   # -> ok
```

A healthy boot prints `listening on :8080` on the unikernel console
(`kraft cloud instance logs <ukc-instance-name>`).
