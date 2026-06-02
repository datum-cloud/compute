# Deploy a Rust Web Service on Datum Compute

> Last verified: 2026-06-02 — the `hello-rust` example built, published, deployed,
> and served (HTTP 200) on the lab; boot time 14.73 ms.

This guide walks you through taking a Rust HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A fully static, position-independent Rust binary packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

The build, publish, and deploy mechanics are identical to the [Go guide](./deploy-a-go-app.md) — only the build stage (how you produce the static binary) differs. The runnable companion to this guide is the [`hello-rust`](../examples/hello-rust/) example.

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

**`src/main.rs`** — a minimal HTTP service using only the standard library, listening on `$PORT` (default `8080`):

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

The service has no external dependencies. Keeping it dependency-light isolates the runtime from FFI and async-runtime variables; `tokio`/`hyper` also work under static musl, but see [Troubleshooting](#a-dependency-broke-the-static-build) for the constraint on crates that link C libraries.

---

## 2. Build and publish the unikernel image with `kraft`

### Why the binary must be static and position-independent

Datum's Unikraft runtime uses an app-elfloader that loads your binary directly as the unikernel entrypoint. If the binary is dynamically linked — it expects a separate interpreter (`ld-linux` / `ld-musl`) at boot — the elfloader rejects it:

```
[appelfloader] ELF executable is not position-independent! ... Exec format error (-8)
```

The reliable solution is a fully static, **position-independent (PIE)** binary packaged `FROM scratch`. Rust's `x86_64-unknown-linux-musl` target produces exactly this — a static **PIE** ELF — by default (Rust ≥ 1.46). With no interpreter to find, the app IS the entrypoint.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`.

### Write the Dockerfile

The Dockerfile builds against the musl target and includes a **self-check that fails the build unless the binary is a static PIE** — so a wrong-shaped binary can never reach the registry:

```dockerfile
FROM rust:1.83-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends binutils file \
    && rm -rf /var/lib/apt/lists/*
RUN rustup target add x86_64-unknown-linux-musl
WORKDIR /src
COPY Cargo.toml ./
COPY src ./src
RUN cargo build --release --target x86_64-unknown-linux-musl
RUN cp target/x86_64-unknown-linux-musl/release/hello-rust /server

# Static-PIE self-check: fail the build unless the binary is ET_DYN (PIE),
# statically linked, and has no program interpreter. A dynamically-linked binary
# or one with an INTERP segment would be rejected by the elfloader at boot.
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

FROM scratch
COPY --from=build /server /server
ENTRYPOINT ["/server"]
```

> **Note:** a correct musl static-PIE binary is reported by `file` as `static-pie linked` — **not** `statically linked`. `readelf -h` shows `Type: DYN` (a position-independent executable, not a shared library). That `DYN` + static-pie + no `INTERP` combination is exactly what the elfloader accepts.

### Write the Kraftfile

```yaml
spec: v0.6

runtime: base:latest

rootfs: ./Dockerfile

cmd: ["/server"]
```

`runtime: base:latest` is the Unikraft Cloud app-elfloader runtime. `rootfs: ./Dockerfile` tells `kraft` to build the rootfs from your Dockerfile.

### Start a BuildKit daemon

`kraft` uses BuildKit to build the rootfs. Start one if you don't already have one running:

```sh
docker run -d --name buildkit --privileged moby/buildkit:latest
```

### Build and publish with `kraft cloud deploy --no-start`

Use `kraft` only to build and publish the image — you deploy the running workload with `datumctl compute` in the next step. The `--no-start` (`-S`) flag builds the unikernel package and pushes it to the metro registry **without** starting an instance. It pushes to `index.unikraft.io/datum/<name>`. The `-M` flag sets the memory allocation in MiB and is required.

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

Once the instance is live, curl the external endpoint. UKC fronts the service with TLS on port 443:

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

For a detailed view of a single instance, including conditions and any failure reason:

```sh
datumctl compute instances describe <instance-name>
```

---

## 5. Update the workload

To deploy a new version, rebuild and publish the image (repeating step 2), then redeploy:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-rust \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
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

This means the binary is dynamically linked or otherwise not a static PIE. The self-check in the Dockerfile above is designed to catch this at build time — if the build passed the `OK: /server is a static-PIE ELF` line, the binary shape is correct. If you removed the self-check, confirm:

- You built for `x86_64-unknown-linux-musl` (not the default glibc target).
- `file /server` reports `static-pie linked` (not `dynamically linked`), and `readelf -h /server` shows `Type: DYN`.
- The `FROM scratch` stage is present.

### A dependency broke the static build

musl static-PIE linking breaks if a crate links a C shared library (the symbols resolve to null and the binary segfaults), or if `-C target-feature=-crt-static` is set (which silently links glibc dynamically). Keep dependencies pure-Rust; for TLS use `rustls` rather than an OpenSSL-backed crate. `tokio`/`hyper` are fine under static musl.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints your `listening on :8080` line. A boot error such as `appelfloader ... not position-independent` means the image must be rebuilt as a static PIE (see above). The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

### Image pull failures on the instance

`datumctl compute instances describe <instance-name>` reports a condition with reason `ImageUnavailable` when the platform cannot pull the image. Confirm the image was pushed to `index.unikraft.io/datum/<name>` (the metro registry) and that the image name in `workload.yaml` matches exactly, including the `latest` tag.
