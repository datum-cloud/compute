# Deploy a Rust Web Service on Datum Compute

> Last verified: 2026-06-02 against the `hello-rust` example and the live `kraft` / `datumctl compute` CLIs.
> The complete, ready-to-deploy example for this guide lives in [`examples/hello-rust/`](../../examples/hello-rust/).

This guide walks you through taking a Rust HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A fully static, position-independent Rust binary packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- Rust (for local development only — the release build happens inside Docker).

---

## 1. Write the application

Create a project directory and add two files.

**`Cargo.toml`**

```toml
[package]
name = "hello-rust"
version = "0.1.0"
edition = "2021"

[profile.release]
strip = true
opt-level = "z"
lto = true
```

**`src/main.rs`**

```rust
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};

fn main() {
    let port = std::env::var("PORT").unwrap_or_else(|_| "8080".to_string());
    let addr = format!("0.0.0.0:{port}");

    let listener = TcpListener::bind(&addr).unwrap_or_else(|e| {
        eprintln!("failed to bind {addr}: {e}");
        std::process::exit(1);
    });

    println!("listening on :{port}");

    for stream in listener.incoming() {
        match stream {
            Ok(stream) => {
                if let Err(e) = handle(stream) {
                    eprintln!("connection error: {e}");
                }
            }
            Err(e) => eprintln!("accept error: {e}"),
        }
    }
}

fn handle(mut stream: TcpStream) -> std::io::Result<()> {
    let mut buf = [0u8; 4096];
    let mut filled = 0usize;
    loop {
        if filled == buf.len() {
            break;
        }
        let n = stream.read(&mut buf[filled..])?;
        if n == 0 {
            break;
        }
        filled += n;
        if buf[..filled].windows(4).any(|w| w == b"\r\n\r\n") {
            break;
        }
    }

    let req = String::from_utf8_lossy(&buf[..filled]);
    let path = req
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .unwrap_or("/");

    let body = match path {
        "/healthz" => "ok",
        _ => "Hello from Datum (Rust)",
    };

    let response = format!(
        "HTTP/1.1 200 OK\r\n\
         Content-Type: text/plain; charset=utf-8\r\n\
         Content-Length: {}\r\n\
         Connection: close\r\n\
         \r\n\
         {}",
        body.len(),
        body
    );

    stream.write_all(response.as_bytes())?;
    stream.flush()?;
    Ok(())
}
```

The service listens on `$PORT` (default `8080`), answers `/healthz` with `ok`, and uses only the standard library — no external dependencies.

---

## 2. Build and publish the unikernel image with `kraft`

### Why the binary must be static and position-independent

Datum's Unikraft runtime uses an app-elfloader that loads your binary directly as the unikernel entrypoint. If the binary is dynamically linked — it expects a separate interpreter (`ld-linux` / `ld-musl`) to be present at boot — the elfloader rejects it:

```
[appelfloader] ELF executable is not position-independent! ... Exec format error (-8)
```

The reliable solution is a fully static, **position-independent (PIE)** binary packaged in a `FROM scratch` image. Rust's `x86_64-unknown-linux-musl` target produces exactly this — a static PIE — by default (Rust ≥ 1.46). With no interpreter for the elfloader to find, the app IS the entrypoint.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

The build compiles against the musl target and then verifies the binary's shape before it can be published — a `readelf`/`file` self-check that fails the build unless the binary is a static PIE. This catches a wrong-shaped binary at build time rather than as a boot failure later.

```dockerfile
# Stage 1: build a fully static, position-independent binary for the musl target.
# x86_64-unknown-linux-musl produces a static PIE by default (Rust >= 1.46), so
# there is no dynamic interpreter for the elfloader to reject at boot.
FROM rust:1.83-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends binutils file \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add x86_64-unknown-linux-musl
WORKDIR /src
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release --target x86_64-unknown-linux-musl
RUN cp target/x86_64-unknown-linux-musl/release/hello-rust /server

# Verify the shape before publishing: the binary must be ET_DYN (PIE), statically
# linked, and have no program interpreter. A correct musl static-PIE binary is
# reported by `file` as "static-pie linked" (not "statically linked") and by
# `readelf -h` as Type: DYN.
RUN readelf -h /server | grep -q 'Type:[[:space:]]*DYN' \
        || (echo "FAIL: binary is not ET_DYN (not PIE)"; exit 1)
RUN if file /server | grep -q 'dynamically linked'; then \
        echo "FAIL: binary is dynamically linked"; exit 1; \
    fi
RUN file /server | grep -qE 'static-pie linked|statically linked' \
        || (echo "FAIL: binary is not statically linked / static-pie"; exit 1)
RUN if readelf -l /server | grep -qi 'INTERP'; then \
        echo "FAIL: binary has a program interpreter (INTERP segment)"; exit 1; \
    fi
RUN echo "OK: /server is a static-PIE ELF (ET_DYN, static-pie linked, no INTERP)"

# Stage 2: a minimal rootfs containing only the binary.
# FROM scratch ensures there are no dynamic libraries for the elfloader to reject.
FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
```

### Write the Kraftfile

```yaml
spec: v0.6

runtime: base:latest

rootfs: ./Dockerfile

cmd: ["/server"]
```

`runtime: base:latest` is the Unikraft Cloud app-elfloader runtime. `rootfs: ./Dockerfile` tells `kraft` to build the rootfs from your Dockerfile rather than expecting a pre-built image.

### Start a BuildKit daemon

`kraft` uses BuildKit to build the rootfs. Start one if you don't already have one running:

```sh
docker run -d --name buildkit --privileged moby/buildkit:latest
```

### Build and publish with `kraft cloud deploy --no-start`

Use `kraft` only to build and publish the image — you deploy the running workload with `datumctl compute` in the next step. The `--no-start` (`-S`) flag builds the unikernel package and pushes it to the metro registry **without** starting an instance, so `kraft` never runs your workload. It pushes to `index.unikraft.io/datum/<name>`. The `-M` flag sets the memory allocation in MiB and is required.

```sh
export KRAFTKIT_NO_CHECK_UPDATES=true

kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-rust \
  --runtime base:latest --rootfs ./Dockerfile .
```

After this command completes, your image is available at `index.unikraft.io/datum/hello-rust:latest`, ready for Datum compute to deploy.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-rust
  labels:
    app: hello-rust
spec:
  template:
    metadata:
      labels:
        app: hello-rust
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-rust:latest
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
datumctl compute deploy hello-rust \
  --image=index.unikraft.io/datum/hello-rust:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-rust
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
# -> Hello from Datum (Rust)

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
  deploy --no-start -M 512 --name hello-rust \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-rust \
  --image=index.unikraft.io/datum/hello-rust:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-rust
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-rust -y

# Stop the local BuildKit daemon.
docker rm -f buildkit
```

---

## Troubleshooting

### The image fails to boot: "ELF not position-independent" or page fault

```
[appelfloader] probe: ELF executable is not position-independent! ... Exec format error (-8)
```

This means the binary is dynamically linked or has the wrong ELF shape. If your build printed the `OK: /server is a static-PIE ELF` line from the Dockerfile self-check, the shape is correct. If you removed that check, confirm:

- You built for the `x86_64-unknown-linux-musl` target, not the default glibc target. A glibc binary is dynamically linked.
- `file /server` reports `static-pie linked` (not `dynamically linked`) and `readelf -h /server` shows `Type: DYN`.
- The `FROM scratch` stage is present. If the rootfs contains a Linux filesystem with shared libraries, the elfloader may find an interpreter and try (and fail) to use it.
- The image was built with `kraft cloud deploy`, not plain `docker build`. A plain OCI image pushed to a container registry will not boot on the Unikraft runtime regardless of how the binary is built.

Rebuild from the Dockerfile in this guide, re-publish (step 2), and redeploy.

### A dependency broke the static build

A correct musl static-PIE binary can quietly become a dynamic one. The two common causes:

- A crate links a C shared library (for example an OpenSSL-backed TLS crate). Under static musl those symbols resolve to null and the binary crashes. Prefer pure-Rust crates; for TLS use `rustls` rather than OpenSSL.
- `-C target-feature=-crt-static` is set somewhere (a `.cargo/config.toml`, `RUSTFLAGS`), which links glibc dynamically.

`tokio` and `hyper` build fine under static musl, so a typical async web stack works. The Dockerfile self-check will fail the build before publishing if a dependency changes the binary's shape.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints your `listening on :8080` line. A boot error such as `appelfloader ... not position-independent` means the image must be rebuilt as a static PIE (see the first troubleshooting entry). The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

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
