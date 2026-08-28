# hello-php

A minimal PHP HTTP service packaged as a Unikraft unikernel and deployed on
Datum compute. It responds `Hello from Datum (PHP)` on `/` and `ok` on
`/healthz`, served by PHP's built-in web server on `0.0.0.0:8080`. No
third-party dependencies — just the stock `php` CLI and its bundled extensions.

This is the runnable companion to the step-by-step guide:
[Deploy a PHP Web Service on Datum Compute](../../docs/guides/deploy-a-php-app.md).

## How this differs from hello-go / hello-rust

The Go and Rust examples ship a single fully static **PIE** binary with no
interpreter — the app *is* the unikernel entrypoint. The PHP CLI is different
(and identical in shape to `hello-python`): it is a **dynamically-linked glibc
ELF** that needs its dynamic loader (`/lib64/ld-linux-x86-64.so.2`) and a set of
glibc shared objects at runtime, plus the libraries its bundled extensions link
against.

This example proves that the `base:latest` app-elfloader runtime **does** boot a
dynamic glibc executable, provided the loader and every needed `.so` are present
in the rootfs (the same `base:latest` runtime as Go/Rust/Python).

Two things are load-bearing in the `Dockerfile`:

1. **Ship the loader + the shared-library closure.** The rootfs includes the
   `php` binary, its bundled extensions (`extension_dir`), the glibc loader, and
   the closure of shared libraries that the binary *and* every bundled
   extension link at runtime (e.g. `libssl`/`libcrypto`, `libxml2`, `libz`,
   `libsodium`, `libargon2`). The closure is computed at build time by walking
   `ldd` over the `php` binary and each extension `*.so`.
2. **Ship ONLY that closure, not the whole `/usr/lib/x86_64-linux-gnu`.** The
   unikernel extracts its rootfs into an in-RAM filesystem at boot. Copying the
   entire glibc lib directory overflows that RAM disk and the boot **fails**
   with `[libukcpio] ...: No space left on device (28)` /
   `[libposix_vfs_fstab] Failed to extract CPIO to /: -3`. The trimmed closure
   boots in tens of milliseconds.

The PHP patch version is pinned (`php:8.3.14-cli-bookworm`) so the copied
extensions match the `php` binary's extension API.

## Files

- `server.php` — the router script for PHP's built-in web server.
- `Dockerfile` — multi-stage build; stage 1 stages the exact shared-library
  closure, stage 2 is a `FROM scratch` rootfs with the `php` binary, its
  extensions, the loader, and that closure.
- `Kraftfile` — runs `php -S 0.0.0.0:8080 /server.php` on `base:latest`.
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
#    -M 1024: PHP needs more memory than the static Go/Rust binaries.
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 1024 --name hello-php \
  --runtime base:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-php
curl -k https://<EXTERNAL-IP>/          # -> Hello from Datum (PHP)
curl -k https://<EXTERNAL-IP>/healthz   # -> ok
```

A healthy boot prints PHP's own
`PHP 8.3.x Development Server (http://0.0.0.0:8080) started` line on the
unikernel console (`kraft cloud instance logs <ukc-instance-name>`). `php -S`
binds the literal `0.0.0.0:8080`, so the workload port must be `8080` to match.
