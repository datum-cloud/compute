# referenced-data-mounts — Federated Delivery E2E Test

This Chainsaw scenario validates the **cross-plane delivery path** for referenced
ConfigMap and Secret data, exercised end-to-end across the Kind+Karmada topology.

## What this test validates

The test covers Hops 1–5 of the federated delivery chain:

| Hop | Cluster | What is asserted |
|-----|---------|-----------------|
| 1 | control-plane | Source ConfigMap + Secret created in the project namespace |
| 2 | control-plane | Companion `app-config` + `app-secret` appear in `ns-{project-uid}` with `compute.datumapis.com/referenced-data: "true"`; WD carries `expected-referenced-data` annotation; WD condition `ReferencedDataReady=True` |
| 3 | downstream (Karmada hub) | Companion ConfigMap + Secret present in `ns-{project-uid}` on the hub; WD carries the annotation; `PropagationPolicy city-dfw` has ConfigMap and Secret resource selectors |
| 4 | pop-dfw (cell) | WD + companions propagated to the cell in `ns-{project-uid}` |
| 5 | pop-dfw (cell) | Instance `test-refdata-wd-0` exists; `ReferencedData` scheduling gate cleared; `ReferencedDataReady=True` condition set |

## What this test does NOT validate

Actual env-var injection and file mounting inside a running Instance is the
**provider + kubelet layer**, not the delivery layer. That path requires the
unikraft-provider to be running with `SameCluster=true` or `SameCluster=false`
against a downstream cluster. See `docs/compute/development/plans/configmap-secret-mounts-e2e.md`
(same-cluster provider path) and `configmap-secret-mounts-e2e-multicluster.md`
(cross-cluster provider path, 4-cluster topology) for the full mount-validation scope.

## Prerequisites

**`task e2e:up` has completed successfully.** In the in-cluster harness this one
target brings up the Kind clusters + Karmada AND deploys the operators from the
real production overlays (plus the local e2e deviations), so there is no separate
operator-start step. It produces these kubeconfigs under `tmp/e2e/kubeconfigs/`:

- `control-plane.yaml` — management cluster (also hosts the Karmada hub)
- `karmada.yaml` — the Karmada hub API server
- `downstream.yaml` — a copy of `karmada.yaml` the Taskfile writes so
  `cluster: downstream` steps resolve (referenced by `chainsaw-config.yaml`)
- `pop-dfw.yaml`, `pop-ord.yaml` — the POP cell clusters

**The cell operator runs with `enableReferencedDataGate: true`.** The e2e cell
deploy layer sets it in `test/e2e/deploy/cell/config_patch.yaml`. The flag's sole
consumer is the cell `WorkloadDeploymentReconciler` (it stamps the `ReferencedData`
scheduling gate); without it the Instance is never gated and Hop 5 cannot pass.
The management deploy layer sets the same flag for parity, though it is inert
there because that overlay enables management controllers only.

## Running just this scenario

```sh
KUBECONFIG=tmp/e2e/kubeconfigs/control-plane.yaml \
bin/chainsaw test \
  --config test/e2e/chainsaw-config.yaml \
  --include-test-regex "referenced-data-mounts" \
  test/e2e/
```

Or via the Taskfile filter target:

```sh
task e2e:test:filter -- --include-test-regex referenced-data-mounts
```

## Harness notes

This test targets the in-cluster harness, where `task e2e:up` builds the operator
image, side-loads it into every Kind node, and deploys the management + cell
operators from the real overlays. Points worth knowing:

1. `_e2e:karmada:build-kubeconfig` copies `karmada.yaml` → `downstream.yaml`, so
   `cluster: downstream` steps work out of the box.
2. The `enableReferencedDataGate` feature flag is delivered through the deploy
   layer (`test/e2e/deploy/{cell,management}/config_patch.yaml`), not a host-side
   `--server-config`. There is no separate operator-start task to run.
3. `e2e:crds:install` installs Milo quota CRDs to all clusters so the
   InstanceReconciler's ResourceClaim watches start cleanly.

## Companion naming convention

The `ReferencedDataController` derives companion names deterministically. When
the source name is already a valid DNS subdomain within the length budget, the
companion keeps that name unchanged (kind is not prefixed):

| Source | Companion name |
|--------|---------------|
| `ConfigMap/app-config` | `app-config` |
| `Secret/app-secret` | `app-secret` |

These names are asserted directly in the test steps.
