# hello-go

A minimal Go HTTP service packaged as a Unikraft unikernel and deployed on Datum
compute. It responds `Hello from Datum (Go)` on `/` and `ok` on `/healthz`, listening
on `$PORT` (default `8080`).

This is the runnable companion to the step-by-step guide:
[Deploy a Go Web Service on Datum Compute](../../docs/guides/deploy-a-go-app.md).

## Files

- `main.go` — the service (standard library only, no dependencies).
- `go.mod` — module definition.
- `Dockerfile` — multi-stage build producing a static-PIE binary (linked against
  musl) packaged `FROM scratch`, so the app is the unikernel entrypoint. A static
  PIE is required: the `base:latest` elfloader rejects a plain `CGO_ENABLED=0`
  (`ET_EXEC`, non-PIE) binary. Includes a build-time self-check.
- `Kraftfile` — packages the rootfs on the `base:latest` runtime.
- `workload.yaml` — the Datum compute Workload manifest.

## Quick start

```sh
# 1. Build and publish the image (kraft builds + pushes; it does not run it).
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-go \
  --runtime base:latest --rootfs ./Dockerfile .

# 2. Deploy on Datum compute.
datumctl compute deploy -f workload.yaml -y

# 3. Verify.
datumctl compute instances --workload=hello-go
curl https://<EXTERNAL-IP>/
```

See the [guide](../../docs/guides/deploy-a-go-app.md) for prerequisites, the
explanation of why the binary must be static, and troubleshooting.
