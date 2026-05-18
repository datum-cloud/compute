# Local Kind + Karmada e2e Environment

This document describes the local multi-cluster environment used for end-to-end
testing of the compute federation layer.

---

## Prerequisites

| Tool | Minimum version | Install |
|------|----------------|---------|
| [Docker Desktop](https://www.docker.com/products/docker-desktop/) | 4.x | required for Kind |
| [kind](https://kind.sigs.k8s.io/) | v0.23+ | `brew install kind` |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | v1.28+ | `brew install kubernetes-cli` |
| [helm](https://helm.sh/) | v3.14+ | `brew install helm` |
| [task](https://taskfile.dev/) | v3 | `brew install go-task` |
| Python 3 | 3.9+ | pre-installed on macOS |
| go | 1.24+ | `brew install go` |

`karmadactl` is downloaded automatically by `task e2e:up` into `./bin/`.

---

## Cluster Topology

```
┌─────────────────────────────────────────────────────────────────────┐
│  compute-control-plane  (Kind cluster)                              │
│                                                                     │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  karmada-system namespace                                     │  │
│  │  Karmada API Server  ←── https://localhost:32443              │  │
│  │  Karmada Controller Manager                                   │  │
│  │  Karmada Scheduler                                            │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                     │
│  compute operator (WorkloadReconciler, Federator, InstanceProjector)│
└──────────────────────────┬──────────────────────────────────────────┘
                           │ Karmada propagates WorkloadDeployments
          ┌────────────────┴─────────────────┐
          │                                  │
┌─────────▼──────────┐            ┌──────────▼─────────┐
│  compute-pop-dfw   │            │  compute-pop-ord   │
│  (Kind cluster)    │            │  (Kind cluster)    │
│                    │            │                    │
│  city-code=dfw     │            │  city-code=ord     │
│  Compute CRDs      │            │  Compute CRDs      │
│  NSO CRDs          │            │  NSO CRDs          │
└────────────────────┘            └────────────────────┘
```

### What lives where

| Resource | Cluster |
|----------|---------|
| `Workload`, `WorkloadDeployment` (consumer-facing) | Control Plane Cell |
| `WorkloadDeployment` (federation intent), `PropagationPolicy` | Karmada API Server |
| `WorkloadDeployment` (propagated), `Instance`, `NetworkBinding`, `SubnetClaim` | POP cells |
| `Instance` (write-back for visibility) | Karmada API Server |

---

## Running the environment

### Start

```bash
task e2e:up
```

This is fully idempotent — running it twice will not fail.

What it does, in order:

1. Downloads `karmadactl v1.16.0` into `./bin/` (once).
2. Adds the `karmada-charts` Helm repository.
3. Creates Kind clusters `compute-control-plane`, `compute-pop-dfw`,
   `compute-pop-ord` (skips any that already exist).
4. Exports kubeconfigs to `./tmp/e2e/kubeconfigs/`.
5. Installs Karmada v1.16.0 via the `karmada-charts/karmada` Helm chart into
   `compute-control-plane`, with the API server exposed on NodePort 32443.
6. Registers `compute-pop-dfw` and `compute-pop-ord` as member clusters and
   labels each with `topology.datum.net/city-code`.
7. Installs compute CRDs to all clusters and the Karmada API server.
8. Installs NSO CRDs to the POP cell clusters.

### Stop

```bash
task e2e:down
```

Deletes all three Kind clusters and removes `./tmp/e2e/`.

---

## Kubeconfigs

After `task e2e:up`:

| File | Cluster | Use for |
|------|---------|---------|
| `tmp/e2e/kubeconfigs/control-plane.yaml` | `compute-control-plane` | kubectl, deploying the compute operator |
| `tmp/e2e/kubeconfigs/karmada.yaml` | Karmada API server | kubectl, karmadactl |
| `tmp/e2e/kubeconfigs/pop-dfw.yaml` | `compute-pop-dfw` | kubectl, inspecting POP cell state |
| `tmp/e2e/kubeconfigs/pop-ord.yaml` | `compute-pop-ord` | kubectl, inspecting POP cell state |

The `-internal.yaml` variants use the Kind container's Docker bridge IP and are
intended for the Karmada controller running inside Docker — not for direct
developer use.

### Quick check

```bash
# Verify cluster list in Karmada
kubectl --kubeconfig tmp/e2e/kubeconfigs/karmada.yaml get clusters

# Expected output:
# NAME                READY   AGE
# compute-pop-dfw     True    ...
# compute-pop-ord     True    ...

# Verify city-code labels
kubectl --kubeconfig tmp/e2e/kubeconfigs/karmada.yaml \
  get clusters -L topology.datum.net/city-code
```

---

## Using the environment from e2e tests

Import `go.datum.net/compute/test/e2e/env` in your test suite:

```go
package myfeature_test

import (
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/runtime"
    computev1alpha1 "go.datum.net/compute/api/v1alpha1"

    "go.datum.net/compute/test/e2e/env"
)

var testEnv *env.Environment

func TestMyFeature(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "MyFeature Suite")
}

var _ = BeforeSuite(func() {
    scheme := runtime.NewScheme()
    Expect(corev1.AddToScheme(scheme)).To(Succeed())
    Expect(computev1alpha1.AddToScheme(scheme)).To(Succeed())

    var err error
    testEnv, err = env.New(scheme)
    Expect(err).NotTo(HaveOccurred())
})

var _ = It("creates a workload and propagates it", func() {
    // Control plane cluster client
    cpClient := testEnv.ControlPlane.Client

    // Karmada API server client
    karmadaClient := testEnv.Karmada.Client

    // POP DFW cluster client
    dfwCell, err := testEnv.POPCell(env.CityCodeDFW)
    Expect(err).NotTo(HaveOccurred())
    dfwClient := dfwCell.Client

    _ = cpClient
    _ = karmadaClient
    _ = dfwClient
})
```

### Environment variable override

Set `E2E_KUBECONFIG_DIR` to an absolute path to load kubeconfigs from a
different directory (useful in CI):

```bash
E2E_KUBECONFIG_DIR=/path/to/kubeconfigs go test ./test/e2e/...
```

---

## Networking notes (macOS)

On macOS with Docker Desktop, Kind clusters run as Docker containers. The
container-to-container networking works as follows:

| From | To | Address used |
|------|----|--------------|
| macOS host | Any Kind cluster API server | `localhost:<kind-port>` |
| macOS host | Karmada API server | `https://localhost:32443` (NodePort) |
| Karmada controller (in Docker) | POP cell API servers | Docker bridge IP (`172.18.x.x:6443`) |

The `-internal.yaml` kubeconfig variants use Docker bridge IPs with
`insecure-skip-tls-verify: true` because the node certificates do not include
bridge IPs in their SANs. This is acceptable for a local dev environment.

---

## Troubleshooting

### Karmada API server not reachable

```bash
kubectl --kubeconfig tmp/e2e/kubeconfigs/karmada.yaml get ns
```

If this times out, check:
1. The Kind cluster is running: `kind get clusters`
2. Port 32443 is mapped: `docker port compute-control-plane-control-plane`
3. The karmada-apiserver pod is running:
   ```bash
   kubectl --kubeconfig tmp/e2e/kubeconfigs/control-plane.yaml \
     get pods -n karmada-system
   ```

### POP cluster shows NotReady in Karmada

The Karmada controller manager uses the Docker bridge IP kubeconfig to reach
POP cells. Check:

```bash
kubectl --kubeconfig tmp/e2e/kubeconfigs/karmada.yaml \
  describe cluster compute-pop-dfw
```

Then verify the cluster secret contains the expected Docker IP:

```bash
kubectl --kubeconfig tmp/e2e/kubeconfigs/karmada.yaml \
  get secret -n karmada-system | grep pop-dfw
```

### Start fresh

```bash
task e2e:down && task e2e:up
```
