# Deploy a Ruby Web Service on Datum Compute

> Last verified: 2026-06-02 against the `hello-ruby` example and the live `kraft` / `datumctl compute` CLIs.
> The complete, ready-to-deploy example for this guide lives in [`examples/hello-ruby/`](../../examples/hello-ruby/).

This guide walks you through taking a Ruby HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A Ruby application packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- Ruby (for local development only — the build happens inside Docker).

---

## 1. Write the application

Create a project directory and add one file.

**`server.rb`**

```ruby
require "socket"

port = (ENV["PORT"] || "8080").to_i
server = TCPServer.new("0.0.0.0", port)

$stdout.puts "listening on :#{port}"
$stdout.flush

def respond(client, body)
  client.write "HTTP/1.1 200 OK\r\n"
  client.write "Content-Type: text/plain; charset=utf-8\r\n"
  client.write "Content-Length: #{body.bytesize}\r\n"
  client.write "Connection: close\r\n"
  client.write "\r\n"
  client.write body
end

loop do
  client = server.accept
  begin
    request_line = client.gets
    while (line = client.gets) && line != "\r\n"
    end

    path = request_line ? request_line.split(" ")[1].to_s : "/"
    if path == "/healthz"
      respond(client, "ok\n")
    else
      respond(client, "Hello from Datum (Ruby)\n")
    end
  rescue StandardError
  ensure
    client.close
  end
end
```

The service listens on `$PORT` (default `8080`), answers `/healthz` with `ok`, and uses only the standard library — no gems. (It uses `socket`/`TCPServer` directly; `webrick` is no longer a default gem in Ruby 3.x.)

---

## 2. Build and publish the unikernel image with `kraft`

### Why Ruby ships the interpreter and its library closure

Datum's Unikraft runtime uses an app-elfloader that loads your application as the unikernel entrypoint. Compiled languages (Go, Rust) ship a single fully static binary. Ruby is different: the MRI interpreter is a **dynamically linked** executable — it needs its loader (`/lib64/ld-linux-x86-64.so.2`) and a set of glibc/system shared libraries at runtime. The rootfs ships the interpreter, its standard library, `libruby`, the loader, and those shared libraries, and the unikernel runs on the same `base:latest` runtime as Go and Rust.

There is one important constraint to get right. **The unikernel extracts its rootfs into an in-RAM filesystem at boot, so the image must stay small.** Copying the entire `/usr/lib/x86_64-linux-gnu` directory (hundreds of MB of ICU data, static archives, etc.) overflows that RAM disk and the boot fails:

```
[libukcpio] ...: Failed to load content: No space left on device (28)
[libposix_vfs_fstab] Failed to extract CPIO to /: -3
```

The fix is to ship **only the precise shared-library closure** the interpreter, `libruby`, and the standard-library C extensions actually need. The Dockerfile below computes that closure at build time with `ldd`.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

```dockerfile
FROM ruby:3.3.6-bookworm AS base

# Stage the exact shared-library closure for the interpreter + libruby + the full
# stdlib C-extension set (rubyarchdir, which includes the enc/*.so encoding
# modules Ruby autoloads). Walking ldd over each captures libraries the stdlib
# links lazily that a hand-written list would miss. SONAME symlinks are preserved
# so the loader resolves NEEDED entries.
RUN set -eu; \
    mkdir -p /rootfs-libs; \
    archdir="$(ruby -e 'print RbConfig::CONFIG["rubyarchdir"]')"; \
    { \
      ldd /usr/local/bin/ruby; \
      ldd /usr/local/lib/libruby.so.3.3; \
      for f in $(find "$archdir" -name '*.so'); do ldd "$f"; done; \
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

# Interpreter + stdlib + libruby (all pinned via ruby:3.3.6-bookworm so the
# /usr/local/lib/ruby tree and libruby version match the ruby binary).
COPY --from=base /usr/local/bin/ruby /usr/local/bin/ruby
COPY --from=base /usr/local/lib/ruby /usr/local/lib/ruby
COPY --from=base /usr/local/lib/libruby.so.3.3.6 /usr/local/lib/libruby.so.3.3.6
COPY --from=base /usr/local/lib/libruby.so.3.3 /usr/local/lib/libruby.so.3.3
COPY --from=base /usr/local/lib/libruby.so /usr/local/lib/libruby.so

# glibc dynamic loader (the program interpreter named in the ruby ELF header).
COPY --from=base /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, under both default loader search paths so
# NEEDED SONAMEs resolve without an ld.so.cache (intentionally not copied: it
# references libraries we did not ship; the loader falls back to its default
# trusted search paths, where these libraries live).
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.rb /server.rb
```

> **Note:** pin the interpreter patch version (`ruby:3.3.6-bookworm`). The copied `/usr/local/lib/ruby` standard-library tree and `libruby.so.3.3.6` must match the `ruby` binary; a version skew breaks `require`.

### Write the Kraftfile

```yaml
spec: v0.6

runtime: base:latest

rootfs: ./Dockerfile

cmd: ["/usr/local/bin/ruby", "/server.rb"]
```

`runtime: base:latest` is the Unikraft Cloud app-elfloader runtime. `rootfs: ./Dockerfile` tells `kraft` to build the rootfs from your Dockerfile.

### Start a BuildKit daemon

`kraft` uses BuildKit to build the rootfs. Start one if you don't already have one running:

```sh
docker run -d --name buildkit --privileged moby/buildkit:latest
```

### Build and publish with `kraft cloud deploy --no-start`

Use `kraft` only to build and publish the image — you deploy the running workload with `datumctl compute` in the next step. The `--no-start` (`-S`) flag builds the unikernel package and pushes it to the metro registry **without** starting an instance. It pushes to `index.unikraft.io/datum/<name>`. The `-M` flag sets the memory allocation in MiB and is required — use at least `1024`.

```sh
export KRAFTKIT_NO_CHECK_UPDATES=true

kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 1024 --name hello-ruby \
  --runtime base:latest --rootfs ./Dockerfile .
```

After this command completes, your image is available at `index.unikraft.io/datum/hello-ruby:latest`, ready for Datum compute to deploy.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-ruby
  labels:
    app: hello-ruby
spec:
  template:
    metadata:
      labels:
        app: hello-ruby
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-ruby:latest
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
datumctl compute deploy hello-ruby \
  --image=index.unikraft.io/datum/hello-ruby:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-ruby
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
# -> Hello from Datum (Ruby)

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
  deploy --no-start -M 1024 --name hello-ruby \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-ruby \
  --image=index.unikraft.io/datum/hello-ruby:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-ruby
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-ruby -y

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

The rootfs is too large for the unikernel's in-RAM filesystem. This happens if you copy the whole `/usr/lib/x86_64-linux-gnu` directory instead of the trimmed library closure. Use the `ldd`-driven closure in the Dockerfile above, and avoid copying static `.a` archives, ICU data, or other bulk you don't use.

### The application fails to `require` a module: missing shared library

If the console shows a load error about a missing `.so` when your code uses a standard-library module (or a gem), that library was not included in the closure. Add it by re-running `ldd` over the relevant stdlib extension (`rubyarchdir/**/*.so`) or your gem's compiled extension and confirming it lands in `/rootfs-libs`. Gems with **C extensions** ship their own `.so` files that may link further system libraries — those must be present in the rootfs too. Pure-Ruby gems need nothing extra.

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
