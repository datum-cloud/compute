# referenced-data-mounts — Federated Delivery E2E Test

This Chainsaw scenario validates the **cross-plane delivery path** for referenced
ConfigMap and Secret data, exercised end-to-end across the Kind+Karmada topology.

## What this test validates

The test covers Hops 1–5 of the federated delivery chain:

| Hop | Cluster | What is asserted |
|-----|---------|-----------------|
| 1 | control-plane | Source ConfigMap + Secret created in the project namespace |
| 2 | control-plane | Companion `configmap.app-config` + `secret.app-secret` appear in `ns-{project-uid}` with `compute.datumapis.com/referenced-data: "true"`; WD carries `expected-referenced-data` annotation; WD condition `ReferencedDataReady=True` |
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

1. **Clusters running**: `task e2e:up` has completed successfully. The following
   kubeconfigs must exist under `tmp/e2e/kubeconfigs/`:
   - `control-plane.yaml`
   - `karmada.yaml`
   - `downstream.yaml` — **REQUIRED** by `chainsaw-config.yaml` but written as
     `karmada.yaml` by the Taskfile. Add a copy step (see Harness Gaps below).
   - `pop-dfw.yaml`

2. **Operators running with `enableReferencedDataGate: true`**: start the management
   and cell operators using the dedicated task (see below). The stock
   `task e2e:operator:start` does NOT pass a server config and therefore starts
   operators with `enableReferencedDataGate: false` — the feature is completely
   inert without this flag.

## Running the operators with the feature flag enabled

Use the dedicated Taskfile target that starts both operators with
`featureFlags.enableReferencedDataGate: true`:

```sh
task e2e:operator:start:referenced-data
```

This starts management (`:9091`) and cell (`:9092`) operators with
`--server-config=hack/e2e/operator-config-referenced-data.yaml` and waits
for both health checks before returning.

To stop both operators: `task e2e:operator:stop`.

Alternatively, start them manually:

```sh
# Management operator — control-plane cluster, feature flag on
KUBECONFIG=tmp/e2e/kubeconfigs/control-plane.yaml \
go run ./cmd/main.go \
  --federation-kubeconfig=tmp/e2e/kubeconfigs/karmada.yaml \
  --enable-management-controllers=true \
  --enable-cell-controllers=false \
  --leader-elect=false \
  --health-probe-bind-address=:9091 \
  --server-config=hack/e2e/operator-config-referenced-data.yaml \
  > tmp/e2e/logs/operator-management-refdata.log 2>&1 &

# Cell operator — pop-dfw cluster, feature flag on
KUBECONFIG=tmp/e2e/kubeconfigs/pop-dfw.yaml \
go run ./cmd/main.go \
  --federation-kubeconfig=tmp/e2e/kubeconfigs/karmada.yaml \
  --enable-management-controllers=false \
  --enable-cell-controllers=true \
  --leader-elect=false \
  --health-probe-bind-address=:9092 \
  --server-config=hack/e2e/operator-config-referenced-data.yaml \
  > tmp/e2e/logs/operator-cell-dfw-refdata.log 2>&1 &
```

Wait for health checks on `:9091` and `:9092` before running the test.

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

The following items were gaps at the time this test was written and have since
been fixed in `Taskfile.yaml`:

1. `_e2e:karmada:build-kubeconfig` now copies `karmada.yaml` →
   `downstream.yaml`, so `cluster: downstream` steps work out of the box.
2. `e2e:operator:start` now uses `--federation-kubeconfig` (the correct flag
   name) for both management and cell operators.
3. `e2e:operator:start` now passes `--enable-management-controllers=true` to
   the management operator, enabling the WorkloadDeploymentFederator and
   InstanceProjector controllers.
4. `e2e:operator:start:referenced-data` is a dedicated task that starts both
   operators with the `enableReferencedDataGate` feature flag on.
5. `e2e:crds:install` now installs Milo quota CRDs to all clusters so the
   InstanceReconciler's ResourceClaim watches start cleanly.

## Companion naming convention

The `ReferencedDataController` derives companion names deterministically:

| Source | Companion name |
|--------|---------------|
| `ConfigMap/app-config` | `configmap.app-config` |
| `Secret/app-secret` | `secret.app-secret` |

These names are asserted directly in the test steps.
