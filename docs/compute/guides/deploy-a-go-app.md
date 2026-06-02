# Deploy a Go Web Service on Datum Compute

> Last verified: 2026-06-02 against the `config-secret-probe` example and the live `kraft` / `datumctl compute` CLIs.

This guide walks you through taking a Go HTTP service from source code to a live, reachable instance on Datum compute. By the end you will have:

- A fully static Go binary packaged as a Unikraft unikernel image
- The image published to the Unikraft Cloud metro registry
- A running workload deployed with `datumctl compute deploy`
- A verified HTTP response from your instance

**What you need before starting:**

- `kraft` (KraftKit) installed and authenticated to your Unikraft Cloud metro. The metro URL and token are supplied to `kraft cloud` commands; this guide assumes they are available as `$UKC_METRO` and `$UKC_TOKEN` in your shell.
- `datumctl` installed with the compute plugin, authenticated to your Datum Cloud project.
- Docker (with BuildKit) running locally.
- Go 1.22+ (for local development only — the build happens inside Docker).

---

## 1. Write the application

Create a project directory and add two files.

**`main.go`**

```go
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello from Datum")
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	fmt.Printf("listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

**`go.mod`**

```
module github.com/your-org/hello-datum

go 1.22
```

Replace `your-org` with your actual module path. The service has no external dependencies.

---

## 2. Build and publish the unikernel image with `kraft`

### Why the binary must be fully static

Datum's Unikraft runtime uses an app-elfloader that loads your binary directly as the unikernel entrypoint. If the binary has any dynamic linking (shared libraries, a C runtime interpreter), the elfloader rejects it at boot:

```
[appelfloader] ELF executable is not position-independent! ... Exec format error (-8)
```

The reliable solution is a fully static binary (`CGO_ENABLED=0`) packaged in a `FROM scratch` image. With no interpreter for the elfloader to find, the app IS the entrypoint. This is the shape that the platform's known-good Go examples use.

A plain `docker build` OCI image will NOT boot on the runtime. The image must be in the Unikraft Cloud format produced by `kraft`. The `Kraftfile` and `kraft cloud deploy` command handle this packaging.

### Write the Dockerfile

```dockerfile
# Stage 1: build a fully static binary.
# CGO_ENABLED=0 produces a static Go binary with no libc dependency.
# GOOS/GOARCH target the linux/amd64 architecture UKC expects.
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o /server ./main.go

# Stage 2: a minimal rootfs containing only the binary.
# FROM scratch ensures there are no dynamic libraries for the elfloader to reject.
# kraft turns this rootfs + the chosen runtime into a Unikraft unikernel package.
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
  deploy --no-start -M 512 --name hello-datum \
  --runtime base:latest --rootfs ./Dockerfile .
```

After this command completes, your image is available at `index.unikraft.io/datum/hello-datum:latest`, ready for Datum compute to deploy.

---

## 3. Deploy on Datum compute

You have two options: a manifest file (recommended for repeatability) or flags.

### Option A — manifest file (recommended)

Create `workload.yaml`:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-datum
  labels:
    app: hello-datum
spec:
  template:
    metadata:
      labels:
        app: hello-datum
    spec:
      runtime:
        resources:
          instanceType: datumcloud/d1-standard-2
        sandbox:
          containers:
            - name: app
              image: index.unikraft.io/datum/hello-datum:latest
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
datumctl compute deploy hello-datum \
  --image=index.unikraft.io/datum/hello-datum:latest \
  --city=DFW \
  --port=8080 \
  --min=1
```

Both forms create (or update) the workload. The `--city` flag accepts one or more city codes; `DFW` targets the US Central region.

---

## 4. Verify the instance is running

List instances and watch for the status to reach `Running`:

```sh
datumctl compute instances --workload=hello-datum
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
# -> Hello from Datum

curl https://<EXTERNAL-IP>/healthz
# -> ok
```

Use `-k` if the TLS certificate is self-signed in your metro:

```sh
curl -k https://<EXTERNAL-IP>/
```

---

## 5. Update the workload

To deploy a new version, rebuild and push the image (repeating step 2), then redeploy. Using the manifest:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  --buildkit-host docker-container://buildkit \
  deploy --no-start -M 512 --name hello-datum \
  --runtime base:latest --rootfs ./Dockerfile .

datumctl compute deploy -f workload.yaml -y
```

Or with flags:

```sh
datumctl compute deploy hello-datum \
  --image=index.unikraft.io/datum/hello-datum:latest \
  --city=DFW \
  --port=8080
```

Watch the rollout progress:

```sh
datumctl compute rollout hello-datum
```

---

## 6. Clean up

```sh
# Delete the workload and all its instances.
datumctl compute destroy hello-datum -y

# Stop the local BuildKit daemon.
docker rm -f buildkit
```

---

## Troubleshooting

### The image fails to boot: "ELF not position-independent" or page fault

```
[appelfloader] probe: ELF executable is not position-independent! ... Exec format error (-8)
```

This means the binary is dynamically linked or has the wrong ELF shape. Check:

- `CGO_ENABLED=0` was set during the build. A binary built with CGO has a dynamic C runtime dependency.
- The `FROM scratch` stage is present. If the rootfs contains a Linux filesystem with shared libraries, the elfloader may find an interpreter and try (and fail) to use it.
- The image was built with `kraft cloud deploy`, not plain `docker build`. A plain OCI image pushed to a container registry will not boot on the Unikraft runtime regardless of how the binary is built.

Rebuild from the Dockerfile in this guide, re-publish (step 2), and redeploy. The boot error appears on the unikernel console — see "Instance shows `Ready` but the endpoint doesn't respond" below to read it.

### Instance shows `Ready` but the endpoint doesn't respond

If an instance reports `Ready` but a `curl` to its endpoint hangs or fails, the unikernel may not have booted cleanly. The unikernel console is the source of truth — read it directly:

```sh
kraft cloud --metro "$UKC_METRO" --token "$UKC_TOKEN" \
  instance logs <ukc-instance-name>
```

A healthy boot prints your `listening on :8080` line. A boot error such as `appelfloader ... not position-independent` means the image must be rebuilt as a fully static binary (see the first troubleshooting entry). The `<ukc-instance-name>` appears in the instance's details from `datumctl compute instances describe <instance-name>`.

### Image pull failures on the instance

`datumctl compute instances describe <instance-name>` reports a condition with reason `ImagePullFailed` or similar when the platform cannot reach the image. Confirm:

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
