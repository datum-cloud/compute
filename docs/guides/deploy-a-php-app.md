# Deploy a PHP Web Service on Datum Compute

> Last verified: 2026-06-02 against the `hello-php` example and the live `kraft` / `datumctl compute` CLIs.
> The complete, ready-to-deploy example for this guide lives in [`examples/hello-php/`](../../examples/hello-php/).

This guide walks you through taking a PHP HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A PHP application packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- PHP (for local development only — the build happens inside Docker).

---

## 1. Write the application

Create a project directory and add one file. This is a router script for PHP's built-in web server — PHP invokes it for every request.

**`server.php`**

```php
<?php
header('Content-Type: text/plain; charset=utf-8');

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);

if ($path === '/healthz') {
    echo "ok\n";
} else {
    echo "Hello from Datum (PHP)\n";
}
```

The service answers `/healthz` with `ok` and everything else with `Hello from Datum (PHP)`, and has no external dependencies. PHP's built-in server binds the address given on its command line (`0.0.0.0:8080`, set in the Kraftfile below) and prints its own boot marker to the console on start.

---

## 2. Build and publish the unikernel image with `kraft`

### Why PHP ships the interpreter and its library closure

Datum's Unikraft runtime uses an app-elfloader that loads your application as the unikernel entrypoint. Compiled languages (Go, Rust) ship a single fully static binary. PHP is different: the `php` CLI is a **dynamically linked** executable — it needs its loader (`/lib64/ld-linux-x86-64.so.2`), a set of glibc shared libraries, and the libraries its bundled extensions link (e.g. `libssl`/`libcrypto`, `libxml2`, `libsqlite3`, `libcurl`). The rootfs ships the `php` binary, its extensions, the loader, and those shared libraries, and the unikernel runs on the same `base:latest` runtime as Go and Rust.

There is one important constraint to get right. **The unikernel extracts its rootfs into an in-RAM filesystem at boot, so the image must stay small.** Copying the entire `/usr/lib/x86_64-linux-gnu` directory overflows that RAM disk and the boot fails:

```
[libukcpio] ...: Failed to load content: No space left on device (28)
[libposix_vfs_fstab] Failed to extract CPIO to /: -3
```

The fix is to ship **only the precise shared-library closure** the `php` binary and its bundled extensions actually need. The Dockerfile below computes that closure at build time with `ldd`.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

```dockerfile
FROM php:8.3.14-cli-bookworm AS base

# Stage the exact shared-library closure for the php binary + every bundled
# extension .so. Walking ldd over each extension captures libraries the
# extensions link (openssl, libxml2, libsodium, libargon2, ...) that a
# hand-written list would miss. SONAME symlinks are preserved so the loader
# resolves NEEDED entries.
RUN set -eu; \
    extdir="$(php -r 'echo ini_get("extension_dir");')"; \
    mkdir -p /rootfs-libs; \
    { \
      ldd /usr/local/bin/php; \
      for f in "$extdir"/*.so; do [ -e "$f" ] && ldd "$f"; done; \
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

# The php CLI binary.
COPY --from=base /usr/local/bin/php /usr/local/bin/php

# The bundled extensions. Preserve the exact extension_dir path so PHP resolves
# them without an explicit override.
COPY --from=base /usr/local/lib/php /usr/local/lib/php

# glibc dynamic loader (the program interpreter named in the php ELF header).
COPY --from=base /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2

# The precise shared-library closure, under both default loader search paths so
# NEEDED SONAMEs resolve without an ld.so.cache (intentionally not copied: it
# references libraries we did not ship; the loader falls back to its default
# trusted search paths, where these libraries live).
COPY --from=base /rootfs-libs/ /lib/x86_64-linux-gnu/
COPY --from=base /rootfs-libs/ /usr/lib/x86_64-linux-gnu/

COPY ./server.php /server.php
```

> **Note:** pin the interpreter patch version (`php:8.3.14-cli-bookworm`). The bundled extensions live under a version-specific `extension_dir` (e.g. `/usr/local/lib/php/extensions/no-debug-non-zts-<api-no>/`); copying the whole `/usr/local/lib/php` tree preserves that path so PHP finds them.

### Write the Kraftfile

```yaml
spec: v0.6

runtime: base:latest

rootfs: ./Dockerfile

cmd: ["/usr/local/bin/php", "-S", "0.0.0.0:8080", "/server.php"]
```

`runtime: base:latest` is the Unikraft Cloud app-elfloader runtime. The `cmd` runs PHP's built-in web server, which binds the literal `0.0.0.0:8080` and serves every request through `/server.php`. (`php -S` does not read `$PORT`, so the listen port is fixed here — keep it aligned with the workload port.)

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
  deploy --no-start -M 1024 --name hello-php \
  --runtime base:latest --rootfs ./Dockerfile .
```

After this command completes, your image is available at `index.unikraft.io/datum/hello-php:latest`, ready for Datum compute to deploy.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-php
  labels:
    app: hello-php
spec:
  template:
    metadata:
      labels:
        app: hello-php
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-php:latest
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
datumctl compute deploy hello-php \
  --image=index.unikraft.io/datum/hello-php:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-php
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
# -> Hello from Datum (PHP)

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
  deploy --no-start -M 1024 --name hello-php \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-php \
  --image=index.unikraft.io/datum/hello-php:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-php
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-php -y

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

The rootfs is too large for the unikernel's in-RAM filesystem. This happens if you copy the whole `/usr/lib/x86_64-linux-gnu` directory instead of the trimmed library closure. Use the `ldd`-driven closure in the Dockerfile above, and avoid copying static archives or bulk you don't use.

### The application fails with a missing shared library

If the console shows a library-not-found error at boot, an extension's dependency is missing from the closure. Re-run `ldd` over the relevant extension `.so` in PHP's `extension_dir` and confirm each dependency lands in `/rootfs-libs`. If you add a **PECL/third-party extension**, it ships its own `.so` with its own library dependencies — those must be present in the rootfs too.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints PHP's `PHP <ver> Development Server (http://0.0.0.0:8080) started` line. A boot error (the rootfs-size or missing-library cases above) appears here. The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

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
