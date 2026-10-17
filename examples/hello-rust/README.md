# hello-rust

A minimal Rust HTTP service packaged as a Unikraft unikernel and deployed on Datum
compute. It responds `Hello from Datum (Rust)` on `/` and `ok` on `/healthz`,
listening on `$PORT` (default `8080`).

This is the runnable companion to the step-by-step guide:
[Deploy a Rust Web Service on Datum Compute](../../docs/guides/deploy-a-rust-app.md).

## Files

- `src/main.rs` — the service (standard library only, no dependencies).
- `Cargo.toml` — package definition with a size-optimized release profile.
- `Dockerfile` — multi-stage build producing a fully static, position-independent
  (`x86_64-unknown-linux-musl`) binary packaged `FROM scratch`. Includes a build-time
  self-check that fails unless the binary is a static PIE, so a wrong-shaped binary
  can never be published.
- `Kraftfile` — packages the rootfs on the `base:latest` runtime.
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-rust \
  --runtime base:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-rust
curl https://<EXTERNAL-IP>/
```

See the [guide](../../docs/guides/deploy-a-rust-app.md) for prerequisites, why the binary
must be a static PIE (the `static-pie linked` vs `dynamically linked` distinction),
and troubleshooting.
