# hello-ruby

A minimal Ruby (MRI/CRuby) HTTP service packaged as a Unikraft unikernel and
deployed on Datum compute. It responds `Hello from Datum (Ruby)` on `/` and `ok`
on `/healthz`, listening on `$PORT` (default `8080`). Standard library only
(`socket` / `TCPServer`), no gems. (We avoid `webrick` because it is no longer a
default gem in Ruby 3.x.)

This is the runnable companion to the step-by-step guide:
[Deploy a Ruby Web Service on Datum Compute](../../docs/guides/deploy-a-ruby-app.md).

## How this differs from hello-go / hello-rust

The Go and Rust examples ship a single fully static **PIE** binary with no
interpreter — the app *is* the unikernel entrypoint. MRI Ruby is different (and
just like CPython): it is a **dynamically-linked glibc ELF** that needs its
dynamic loader (`/lib64/ld-linux-x86-64.so.2`) and a set of glibc/system shared
objects at runtime.

This example proves that the `base:latest` app-elfloader runtime **does** boot a
dynamic glibc Ruby executable, provided the loader and every needed `.so` are
present in the rootfs (Variant A: reuse the same `base:latest` runtime as
Go/Rust).

Two things are load-bearing in the `Dockerfile`:

1. **Ship the loader + the shared-library closure.** The rootfs includes the
   `ruby` binary, the full stdlib (`/usr/local/lib/ruby`), `libruby.so`, the
   glibc loader, and the closure of shared libraries that the interpreter *and*
   its stdlib C-extension modules (`rubyarchdir/**/*.so`, including the
   `enc/*.so` encodings Ruby autoloads) link against (`libz`, `libssl`/
   `libcrypto`, `libffi`, `libyaml`, `libgmp`, `libcrypt`, ...). The closure is
   computed at build time by walking `ldd`.
2. **Ship ONLY that closure, not the whole `/usr/lib/x86_64-linux-gnu`.** The
   unikernel extracts its rootfs into an in-RAM filesystem at boot. Copying the
   entire glibc lib directory overflows that RAM disk and the boot **fails**
   with `[libukcpio] ...: No space left on device (28)` /
   `[libposix_vfs_fstab] Failed to extract CPIO to /: -3`. Ruby's trimmed
   closure is tiny (a handful of small libs) and boots in tens of milliseconds.

The interpreter patch version is pinned (`ruby:3.3.6-bookworm`) so the copied
`/usr/local/lib/ruby/3.3.0` stdlib tree matches `libruby.so.3.3.6`. The stdlib
arch dir is `/usr/local/lib/ruby/3.3.0/x86_64-linux`.

## Files

- `server.rb` — the service (stdlib `socket` / `TCPServer` only).
- `Dockerfile` — multi-stage build; stage 1 stages the exact shared-library
  closure, stage 2 is a `FROM scratch` rootfs with the interpreter, stdlib,
  `libruby`, loader, and that closure.
- `Kraftfile` — runs `["/usr/local/bin/ruby", "/server.rb"]` on `base:latest`.
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 1024 --name hello-ruby \
  --runtime base:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-ruby
curl -k https://<EXTERNAL-IP>/          # -> Hello from Datum (Ruby)
curl -k https://<EXTERNAL-IP>/healthz   # -> ok
```

A healthy boot prints `listening on :8080` on the unikernel console
(`kraft cloud instance logs <ukc-instance-name>`).
