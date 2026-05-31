# E2E Validation Plan — ConfigMap/Secret Mounts

Companion to [`configmap-secret-mounts.md`](./configmap-secret-mounts.md).
This document scopes the end-to-end validation environment for the
ConfigMap/Secret mounts feature, grounded in the actual existing harness.

## 1. Existing harness — what's already there

### 1.1 Cluster topology (`task e2e:up`)

The Taskfile (currently in the `federation` worktree at
`Taskfile.yaml`, targeted to land in main) stands up three Kind clusters:

| Cluster name | Kind cluster var | Role |
|---|---|---|
| `compute-control-plane` | `KIND_CONTROL_PLANE` | Management plane — runs the management operator, hosts Karmada, holds Workload/WD/Instance projections |
| `compute-pop-dfw` | `KIND_POP_DFW` | Edge cell, city-code `dfw` |
| `compute-pop-ord` | `KIND_POP_ORD` | Edge cell, city-code `ord` |

Karmada is installed into `compute-control-plane` via Helm, exposed on
NodePort 32443, and the two POP clusters are joined as member clusters
labeled `topology.datum.net/city-code={dfw,ord}`. The federation
Kustomization component (`config/components/federation/`) is applied to
the Karmada API server (`task e2e:karmada:configure`).

Kubeconfigs are written to `tmp/e2e/kubeconfigs/`:
`control-plane.yaml`, `karmada.yaml` (rewritten to
`https://localhost:32443`), `pop-dfw.yaml`, `pop-ord.yaml`, and
`pop-{dfw,ord}-internal.yaml` (Docker-IP variants used by the Karmada
controller).

### 1.2 Operator processes (`task e2e:operator:start`)

Two `go run ./cmd/main.go` processes start in the background:

- **Management operator** — `KUBECONFIG=control-plane.yaml`, flags
  `--karmada-kubeconfig=karmada.yaml --enable-cell-controllers=false`
- **Cell operator (dfw)** — `KUBECONFIG=pop-dfw.yaml`, flags
  `--karmada-kubeconfig=karmada.yaml --enable-management-controllers=false`

Each is health-checked before tests proceed.

### 1.3 Chainsaw configuration (`test/e2e/chainsaw-config.yaml`)

The global configuration declares four named clusters:

- default (implicit): `control-plane.yaml`
- `downstream`: `tmp/e2e/kubeconfigs/downstream.yaml` (the Karmada API server)
- `pop-dfw`: `tmp/e2e/kubeconfigs/pop-dfw.yaml`
- `pop-ord`: `tmp/e2e/kubeconfigs/pop-ord.yaml`

Test steps select a cluster via `cluster: pop-dfw` (etc.).

### 1.4 Existing federation Chainsaw tests

Located in `test/e2e/` (federation worktree):

| Directory | What it exercises |
|---|---|
| `full-federation/` | Workload → WD → Karmada propagation → Instance on pop-dfw → writeback → projection |
| `workload-deployment-federation/` | WD replication and PropagationPolicy lifecycle |
| `instance-writeback/` | Cell writes Instance back to Karmada |
| `instance-projection/` | InstanceProjector creates projection on control-plane |
| `propagation-policy-lifecycle/` | PropagationPolicy create/update/delete |
| `deletion-cascade/` | GC through the federation chain |

These are the precedent for how new scenarios are structured.

### 1.5 CRD installation

`task e2e:crds:install` applies compute CRDs to all three Kind clusters
**and** to the Karmada API server (so Karmada can store and propagate
WDs). NSO CRDs (NetworkBinding, SubnetClaim) are applied to
control-plane and POP clusters.

### 1.6 What does NOT exist yet

- No `hack/e2e/` scripts exist in the main branch (only in the federation
  worktree).
- No Chainsaw tests exist in `test/e2e/` on main.
- No `test/e2e/chainsaw-config.yaml` on main.
- No `ReferencedDataController`, no companion materialization, no
  scheduling gate for referenced data — all Phase 0–3 work from the plan
  must land before these e2e scenarios can run.

---

## 2. Unikraft provider — the "mounted inside" question

### 2.1 Provider topology

The unikraft-provider (`go.datum.net/unikraft-provider`) maintains two
cluster handles:

- **Upstream cluster** — the cell (e.g., `pop-dfw`). Instances live here.
- **Downstream cluster** — where Pods are created. Configured via
  `DownstreamResourceManagement.KubeconfigPath`. When the path is empty,
  the provider uses its own kubeconfig, making upstream == downstream.

In the `single`/lab configuration (`config/dev/example-same-cluster.yaml`)
both are the same cluster. In production, the downstream is a separate
kraftlet cluster whose kubelet is the UKC virtual-kubelet.

The provider creates a Pod named `string(instance.UID)` in the same
namespace as the Instance, with:
- `nodeSelector: kubernetes.io/hostname: kraftlet`
- `tolerations` for `virtual-kubelet.io/provider=ukc`
- Container env vars mapped from `SandboxContainer.Env`

The real kraftlet/UKC virtual-kubelet is a closed, external service
requiring UKC credentials (`UKC_METRO`, `UKC_TOKEN`). The existing
provider e2e (`test/e2e/instance/01-create-instance/`) only asserts that
a Pod materializes with the correct labels — it does not validate that
files or env vars are visible inside a running UKC unikernel.

### 2.2 What ConfigMap/Secret mounts requires of the provider (Workstream D)

From the plan:
- Gate honoring: return before Pod creation while any `SchedulingGates`
  remain on the Instance.
- Faithful Pod-spec translation: carry `env.ValueFrom` through;
  map `InstanceVolume` ConfigMap/Secret sources → `core.Volume`; map
  `VolumeAttachment` → `core.VolumeMount`.
- Companion mirroring: when `SameCluster=false`, mirror labeled companions
  from the cell namespace into the downstream Pod namespace before creating
  the Pod.
- In `SameCluster=true` mode (same-cluster / lab), companions are already
  in the namespace — no mirroring needed.

### 2.3 Practical stand-in for "mounted inside the instance"

Running the real UKC virtual-kubelet in CI is not practical: it requires
credentials to an external service, connects to real UKC infrastructure,
and has no way to exec into a running unikernel. Even the provider's own
e2e tests accept this limitation.

**Recommended approach: use a real kubelet node (Kind) with same-cluster
mode.**

Configure the provider with `SameCluster=true` / `KubeconfigPath: ""`
targeting a standard Kind node (no kraftlet taint). In this mode:

1. The Pod lands on a real Kind node with a real kubelet.
2. The kubelet mounts the ConfigMap/Secret natively via the projected
   volumes in the Pod spec.
3. An `exec` step in the Chainsaw test or a `kubectl exec` in a script
   step can verify the files and env vars inside the running container.

This is the most faithful practical validation of the full path: the
provider translates `InstanceVolume`/`VolumeAttachment`/`EnvFrom` into
a Pod spec, and the kubelet consumes it. The only gap is the UKC
virtual-kubelet — which cannot be validated without live UKC credentials.
That gap is documented explicitly in the risk table (section 7) and
tracked as a separate acceptance step in the provider repo.

**Test image**: use a small standard image (e.g.,
`gcr.io/distroless/static-debian12:nonroot` for the gate/condition
assertions, `docker.io/library/busybox:stable` or an equivalent image
that can `cat` files for mount verification). The provider's `nodeSelector`
targeting `kubernetes.io/hostname: kraftlet` must be **overridden** in the
test config so the Pod schedules on the real Kind node.

**Mechanism for the `exec` assert**: Chainsaw v0.2.15 supports `script`
steps; use a `kubectl exec` in a shell script step to cat a mounted file
and compare the output.

---

## 3. Cluster topology for ConfigMap/Secret mounts e2e

The existing three-cluster topology is sufficient. No new clusters are
needed for federated scenarios. The provider runs in same-cluster mode
on pop-dfw, targeting its own API server.

```
┌─────────────────────────────────────────────────────────────────────┐
│  compute-control-plane (Kind)                                       │
│  ┌──────────────┐  ┌────────────────────────────────────────────┐  │
│  │ Management   │  │ Karmada API server (NodePort 32443)        │  │
│  │ operator     │  │ – stores WDs in ns-{project-uid}           │  │
│  │ (management  │  │ – PropagationPolicy → pop-dfw/pop-ord      │  │
│  │  controllers)│  │ – companions propagated via label selector │  │
│  └──────────────┘  └────────────────────────────────────────────┘  │
│  – Workloads, WorkloadDeployments (user-facing)                     │
│  – Instance projections (read-only)                                 │
│  – Source ConfigMaps / Secrets (user-created)                       │
└─────────────────────────────────────────────────────────────────────┘
         │ Karmada propagates WD + labeled companions
         ▼
┌─────────────────────────────────────────────────────────────────────┐
│  compute-pop-dfw (Kind)                                             │
│  ┌──────────────────────┐  ┌─────────────────────────────────────┐ │
│  │ Cell operator        │  │ unikraft-provider (same-cluster)    │ │
│  │ (cell controllers)   │  │ – SameCluster=true                  │ │
│  │ – reconciles WD      │  │ – creates Pods on real Kind node    │ │
│  │ – gate: ReferencedData│ │ – translates volumes/env            │ │
│  │ – gate: Network      │  │ – no mirroring needed               │ │
│  │ – gate: Quota        │  └─────────────────────────────────────┘ │
│  └──────────────────────┘                                           │
│  – ns-{project-uid}: WD, Instances, companion ConfigMaps/Secrets   │
│  – Pods with native kubelet mounts                                  │
└─────────────────────────────────────────────────────────────────────┘
```

**For the single-cluster collapse scenario**, the management operator
runs against `control-plane.yaml` with discovery mode `single` (no
Karmada) and the provider runs in the same cluster. This can use an
isolated Chainsaw test namespace without standing up a separate cluster.

---

## 4. Validation path — hop-by-hop assertions

### 4.1 Complete federated flow

```
User creates ConfigMap + Secret + Workload (control-plane)
    │
    ▼
[Hop 1] Admission webhook (control-plane)
    – SubjectAccessReview: caller has get on the referenced CM/Secret
    – Assert: denied if caller lacks permission

    │ (if allowed)
    ▼
[Hop 2] ReferencedDataController (management plane, control-plane)
    – Reads CM/Secret from project namespace
    – Materializes companion ConfigMap / companion Secret in ns-{project-uid}
      labeled compute.datumapis.com/referenced-data: "true"
    – Stamps annotation compute.datumapis.com/expected-referenced-data on WD
      (JSON array of companion names, e.g. ["configmap.app-config","secret.app-secret"])
    – Sets WD status: ReferencedDataReady / reason Resolving → Ready (or error reason)

    Assert on control-plane:
    – Companion ConfigMap configmap.app-config in ns-{project-uid} exists
    – Companion Secret secret.app-secret in ns-{project-uid} exists
    – Both carry label compute.datumapis.com/referenced-data: "true"
    – WD annotation expected-referenced-data == JSON array of companion names
    – WD status condition ReferencedDataReady → reason not SourceNotFound etc.

    │
    ▼
[Hop 3] Karmada propagation (Karmada API server)
    – WD propagated to pop-dfw (existing PropagationPolicy mechanism)
    – Labeled companions propagated by always-on CM/Secret label selector
      in the PropagationPolicy (Workstream B)

    Assert on downstream (Karmada):
    – WD exists in ns-{project-uid}
    – Companion ConfigMap and Secret exist in ns-{project-uid}
      (confirms Karmada picked them up via label selector)

    │
    ▼
[Hop 4] WorkloadDeploymentReconciler creates Instance (pop-dfw)
    – Instance created with scheduling gate "ReferencedData" (feature-flag on)
    – Instance also has gates "Network", "Quota" potentially

    Assert on pop-dfw:
    – Instance test-wd-0 exists in ns-{project-uid}
    – Spec.Controller.SchedulingGates contains "ReferencedData"

    │
    ▼
[Hop 5] Cell InstanceReconciler gate clearing (pop-dfw)
    – reconcileReferencedDataCondition: reads expected-referenced-data from WD
    – Lists labeled companions in ns-{project-uid} on the cell
    – All expected names present → condition ReferencedDataReady=True,
      reason Ready → removes "ReferencedData" gate

    Assert on pop-dfw:
    – Instance condition ReferencedDataReady == True
    – SchedulingGates no longer contains "ReferencedData"

    │
    ▼
[Hop 6] unikraft-provider creates Pod (pop-dfw, same-cluster)
    – Reads Instance spec: volumes, volume attachments, env, envFrom
    – Translates to Pod spec: volumes (ConfigMap/Secret sources),
      volumeMounts, env.ValueFrom, envFrom
    – Companion ConfigMap/Secret are in the same namespace (same-cluster)
    – Pod scheduled on real Kind node (nodeSelector overridden for test)

    Assert on pop-dfw:
    – Pod exists with label upstream.instance: <instance-name>
    – Pod spec has volume referencing companion ConfigMap name
    – Pod spec has volumeMount at expected path
    – Pod spec has env.ValueFrom or envFrom referencing companion Secret

    │
    ▼
[Hop 7] Kubelet mounts (pop-dfw Kind node)
    – Real kubelet mounts ConfigMap as file, Secret as env/file
    – Pod transitions Running

    Assert (kubectl exec into the Pod):
    – cat /etc/config/<key> == expected ConfigMap value
    – env | grep <KEY> contains expected Secret value
```

### 4.2 Namespace invariant checkpoint

At Hop 5, assert explicitly:

```yaml
# WD, Instance, and companions all share ns-{project-uid}
- assert:
    resource:
      apiVersion: v1
      kind: ConfigMap
      metadata:
        namespace: ($downstreamNS)   # same as WD and Instance
        name: configmap.app-config
        labels:
          compute.datumapis.com/referenced-data: "true"
```

---

## 5. Chainsaw scenarios

The following scenarios map to `test/e2e/` subdirectories. Each
references `cluster: pop-dfw` for cell-side assertions and the default
cluster for control-plane assertions, following the pattern established
in `full-federation/chainsaw-test.yaml`.

### Scenario A — Happy path: env injection + file mount

**Directory**: `test/e2e/referenced-data-happy-path/`

**Preconditions**: Feature flag enabled on the management operator
(`--enable-referenced-data-gate=true`), unikraft-provider running in
same-cluster mode on pop-dfw with nodeSelector override.

**Steps**:

1. `apply`: source ConfigMap `app-config` and Secret `app-secret` in the
   test namespace (control-plane).
2. `apply`: Workload referencing both in a volume and an `envFrom`.
3. `assert` (control-plane, timeout 30s): Workload creates WD.
4. `assert` (control-plane, timeout 30s): companion
   `configmap.app-config` in `ns-{project-uid}` exists with
   `referenced-data: "true"` label.
5. `assert` (control-plane, timeout 30s): WD annotation
   `expected-referenced-data` present and non-empty.
6. `assert` (control-plane, timeout 30s): WD condition
   `ReferencedDataReady` with reason not `SourceNotFound`.
7. `assert` (`cluster: pop-dfw`, timeout 60s): WD and companions
   present in `ns-{project-uid}`.
8. `assert` (`cluster: pop-dfw`, timeout 30s): Instance
   `SchedulingGates` no longer contains `ReferencedData`.
9. `assert` (`cluster: pop-dfw`, timeout 30s): Instance condition
   `ReferencedDataReady=True`.
10. `assert` (`cluster: pop-dfw`, timeout 60s): Pod running, spec
    contains expected volume and env source.
11. `script`: `kubectl exec <pod> -- cat /etc/config/<key>` output
    equals expected ConfigMap value.
12. `script`: `kubectl exec <pod> -- sh -c 'echo $SECRET_KEY'` output
    equals expected Secret value.

### Scenario B — Unauthorized reference rejected at admission

**Directory**: `test/e2e/referenced-data-unauthorized/`

**Steps**:

1. Create a ConfigMap `restricted-config` owned by a different namespace
   or user (or simulate via a webhook test user without `get` permission).
2. `apply` a Workload referencing `restricted-config`, expect admission
   rejection (Chainsaw `error:` block).
3. Assert no WD is created, no companion materialized.

Note: exercising the SAR path cleanly in a Kind cluster without
impersonation support may require a dedicated ServiceAccount with a
restrictive ClusterRole. The webhook must be deployed and reachable.

### Scenario C — Source not found → SourceNotFound held

**Directory**: `test/e2e/referenced-data-source-not-found/`

**Steps**:

1. `apply`: Workload referencing a ConfigMap `missing-config` that does
   not exist.
2. `assert` (control-plane, timeout 30s): WD condition
   `ReferencedDataReady=False` with `reason: SourceNotFound`.
3. `assert` (`cluster: pop-dfw`, timeout 60s): Instance exists with
   `SchedulingGates` still containing `ReferencedData`.
4. Instance condition `ReferencedDataReady` not `True` (gate held).
5. `apply`: create `missing-config` ConfigMap.
6. `assert` (control-plane, timeout 30s): WD condition transitions to
   `ReferencedDataReady=True`.
7. `assert` (`cluster: pop-dfw`, timeout 60s): gate cleared, Instance
   proceeds.

### Scenario D — Oversized source → SourceTooLarge held

**Directory**: `test/e2e/referenced-data-too-large/`

**Steps**:

1. Create a ConfigMap with a data value exceeding the 256 KiB per-object
   limit (generate inline in the Chainsaw YAML using a `script` apply
   step that produces the large ConfigMap via `kubectl`).
2. `apply`: Workload referencing the oversized ConfigMap.
3. `assert` (control-plane): WD condition `ReferencedDataReady=False`
   with `reason: SourceTooLarge`.
4. `assert` (`cluster: pop-dfw`): Instance gate `ReferencedData` held.
5. `apply`: replace ConfigMap with a small-data version.
6. `assert`: gate clears, condition transitions to `Ready`.

### Scenario E — Rotation + restart annotation

**Directory**: `test/e2e/referenced-data-rotation/`

**Steps**:

1. Happy-path setup: ConfigMap `app-config` v1 → instance running with
   v1 value mounted.
2. `apply`: update `app-config` with v2 value.
3. `assert` (control-plane, timeout 30s): companion ConfigMap updated
   (resolver watches source; rotation re-triggers reconcile).
4. `apply`: patch Workload template with
   `compute.datumapis.com/restartedAt: <RFC3339-timestamp>` annotation
   (triggers the stateful ordered-roll via template hash change).
5. `assert` (`cluster: pop-dfw`): new Instance `test-wd-1` (or updated
   `test-wd-0` after roll) reaches `Ready`.
6. `script`: `kubectl exec <new-pod> -- cat /etc/config/<key>` == v2
   value.

### Scenario F — Single-cluster collapse (no Karmada)

**Directory**: `test/e2e/referenced-data-single-cluster/`

**Preconditions**: Management operator started with discovery mode
`single` (no `--karmada-kubeconfig`), feature flag on. Provider in
same-cluster mode. Can run against the control-plane cluster alone; no
pop-dfw required.

**Steps**:

1. `apply`: source ConfigMap + Secret + Workload (control-plane only).
2. `assert`: companion in same namespace (no `ns-{project-uid}` fan-out).
3. `assert`: WD annotation and condition set.
4. `assert`: gate cleared by cell reconciler (running in same binary).
5. `assert`: Instance condition `ReferencedDataReady=True`.
6. `script`: exec to verify mount and env.

This scenario validates the single-cluster path from Workstream B
(nil-fallback project reader, local companion, no PropagationPolicy).

### Scenario G — Feature flag off → no gate, unchanged behavior

**Directory**: `test/e2e/referenced-data-flag-off/`

**Preconditions**: Management operator started **without**
`--enable-referenced-data-gate` (default off).

**Steps**:

1. `apply`: Workload referencing a ConfigMap.
2. `assert`: Instance created without `ReferencedData` gate in
   `SchedulingGates`.
3. `assert`: Provider creates Pod without blocking (volumes still absent
   — this validates the flag-off path doesn't break existing behavior).
4. Assert no `ReferencedDataReady` condition present.

---

## 6. Asserting "mounted inside the instance"

This is the trickiest part of the validation.

### 6.1 Mechanism

The selected approach is a `script` step in Chainsaw that runs
`kubectl exec` into the running Pod:

```yaml
- name: verify-file-mount
  try:
  - script:
      timeout: 30s
      content: |
        POD=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/pop-dfw.yaml \
          get pod -n "$DOWNSTREAM_NS" \
          -l upstream.instance="$INSTANCE_NAME" \
          -o jsonpath='{.items[0].metadata.name}')
        VALUE=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/pop-dfw.yaml \
          exec -n "$DOWNSTREAM_NS" "$POD" -- cat /etc/config/app.properties)
        test "$VALUE" = "expected-value"
```

### 6.2 Requirements for this to work

- The Pod must be running on a real node (not a virtual-kubelet node),
  so `kubectl exec` succeeds.
- The container image must include a shell or `cat`. Use `busybox` or
  a purpose-built test image rather than `distroless`.
- The nodeSelector override must be applied in the provider's config for
  the test environment, removing or replacing the
  `kubernetes.io/hostname: kraftlet` selector and the UKC taint
  toleration.

**Recommended test-specific provider config** (applied as a Kustomize
patch in the test overlay):

```yaml
spec:
  nodeSelector:
    kubernetes.io/os: linux   # schedules on any Kind node
  tolerations: []             # no UKC taint needed
```

Or, more practically, configure the provider with a test-mode flag or
use a separate test-infra provider config (following the unikraft-provider
pattern in `config/overlays/test-infra/`).

### 6.3 Gap vs. production UKC validation

The kubelet path validates:
- Volume projection wiring (ConfigMap → `core.Volume` → `core.VolumeMount`)
- Env injection wiring (`env.ValueFrom`, `envFrom` → Pod container spec)
- Companion namespace co-location
- Gate timing (companion present before Pod creation)

The kubelet path does NOT validate:
- UKC virtual-kubelet behavior with ConfigMap/Secret volumes
- UKC's handling of `envFrom` and `ValueFrom` (which it may or may not
  support natively — the plan notes no pull-secret support exists today)

The UKC gap must be accepted as an integration test outside this harness,
run manually against a live UKC metro with real credentials as part of the
provider's acceptance process.

---

## 7. CI integration

### 7.1 Test tier split

| Test | Tier | Runtime | Tool | When to run |
|---|---|---|---|---|
| API validation (A: types, constants, validation) | Unit/envtest | ~10s | `make test` | Every PR |
| Resolver unit (B: collector, reader, SAR) | Unit | ~5s | `go test ./internal/referenceddata/...` | Every PR |
| Resolver integration (B: happy path, source-not-found, rotation, ref-count) | envtest | ~60s | `make test` | Every PR |
| Cell gate unit (C: condition state machine) | Unit | ~5s | `go test` | Every PR |
| Cell gate integration (C: gate clears/holds, reason transitions) | envtest | ~60s | `make test` | Every PR |
| SAR allow/deny (E) | Unit / envtest | ~10s | `make test` | Every PR |
| Provider unit (D: Pod-spec translation, gate-respect) | Unit | ~10s | `go test` in unikraft-provider | Every PR |
| Scenarios A, B, C, D, E, G | Full e2e (Kind+Karmada) | ~8–15min | `task e2e:test` | Main merges, nightly |
| Scenario F (single-cluster) | Full e2e (Kind only) | ~3–5min | `task e2e:test:filter` | Main merges, nightly |

### 7.2 Taskfile additions

Add to `Taskfile.yaml`:

```yaml
e2e:referenced-data:test:
  desc: "Run only the referenced-data e2e scenarios"
  deps: [e2e:tools:chainsaw]
  cmds:
    - |
      KUBECONFIG={{.KUBECONFIG_DIR}}/control-plane.yaml \
      {{.CHAINSAW}} test \
        --config test/e2e/chainsaw-config.yaml \
        --include-test-regex "referenced-data" \
        test/e2e/
```

### 7.3 Provider flag addition

The management operator needs a feature flag to enable gate insertion.
Add `--enable-referenced-data-gate` (default `false`) to the operator
start command in `task e2e:operator:start`. Tests in scenarios A–E set
this flag; scenario G explicitly omits it.

### 7.4 Runtime cost estimate

| Component | Time |
|---|---|
| `task e2e:up` (clusters + Karmada) | 3–5 min (first run); ~30s if clusters already exist |
| `task e2e:operator:start` | ~20s |
| Provider deploy (kubectl apply) | ~30s |
| Scenario A (happy path, with exec) | ~3–4 min |
| Scenarios B–G combined | ~6–8 min |
| Total e2e gate | ~12–15 min |

This fits within a standard CI job timeout of 30 minutes.

### 7.5 What can run in the lighter envtest tier

The envtest tier (`internal/controller/suite_test.go` + Ginkgo) already
runs against a real apiserver binary. The following can be validated
without Kind:

- Resolver reconcile loop (stub out the `ProjectConfigSecretReader` with
  a fake; use a real envtest apiserver for WD + companion CRUD)
- Gate clearing state machine (fake companion list, real Instance update)
- PropagationPolicy contents (assert the generated PP has the correct
  label selector for CM/Secret companions)
- Ref-count annotation changes across two WDs
- Finalizer-driven delete when last reference drops

The scenarios that genuinely require Kind+Karmada are those asserting
cross-cluster propagation (Hop 3) and the `kubectl exec` inside a
running Pod (Hop 7).

---

## 8. Gaps and risks

| Risk | Severity | Mitigation |
|---|---|---|
| **UKC virtual-kubelet does not support ConfigMap/Secret volumes** | High | Accepted gap in Kind-based e2e. Must be validated manually against a live UKC metro. Track as a required acceptance step in the unikraft-provider release checklist. |
| **Karmada CM/Secret label selector semantics** | Medium | The plan calls this out (Workstream B, risk R2). Validate against the deployed Karmada version (v1.16.0 per Taskfile) in the PropagationPolicy lifecycle scenario. |
| **Companion propagation races the WD** | Low | The plan's always-on label selector means a companion materialized after the WD is still picked up. The gate holds the Instance during the race window. Validate with a synthetic delay in scenario A (add a `sleep` between WD apply and companion apply to force the race). |
| **Namespace invariant drift** | Medium | Companion namespace must equal WD/Instance namespace (`ns-{project-uid}`). Assert explicitly in scenario A (Hop 5). Any drift causes gate to never clear. |
| **nodeSelector override for test Pods** | Low | Provider must have a test-mode config disabling the kraftlet selector. Use the `config/overlays/test-infra/` pattern from the unikraft-provider. |
| **Feature flag ordering in CI** | Low | The flag must be on in the management operator process before any scenario A–E test starts. Task orchestration handles this; document the dependency in test README. |
| **Admission SAR in Kind** | Medium | The Kind control-plane does not run Milo IAM. The SAR check is a `SubjectAccessReview` against the Kubernetes RBAC system, which Kind supports. Test with a dedicated ServiceAccount missing the `get` permission on the target ConfigMap. |
| **Rotation race (scenario E)** | Low | The resolver watches sources; a rotation may arrive at the cell before the instance is restarted. The stateful roll is driven by template hash, not companion content. Document that the companion update propagates eventually; the restart annotation is what triggers the roll. |
| **envtest cannot validate cross-cluster gate clearing** | Low | Expected and acceptable. The envtest tier covers the gate state machine in isolation; the full cross-cluster flow requires Kind+Karmada. |

---

## 9. File layout

```
test/e2e/
├── chainsaw-config.yaml               # existing (federation worktree → main)
├── referenced-data-happy-path/
│   ├── chainsaw-test.yaml
│   ├── source-configmap.yaml
│   ├── source-secret.yaml
│   └── workload.yaml
├── referenced-data-unauthorized/
│   ├── chainsaw-test.yaml
│   ├── restricted-configmap.yaml
│   └── workload.yaml
├── referenced-data-source-not-found/
│   ├── chainsaw-test.yaml
│   └── workload.yaml
├── referenced-data-too-large/
│   ├── chainsaw-test.yaml             # uses script step to generate large CM
│   └── workload.yaml
├── referenced-data-rotation/
│   ├── chainsaw-test.yaml
│   ├── source-configmap-v1.yaml
│   ├── source-configmap-v2.yaml
│   └── workload.yaml
├── referenced-data-single-cluster/
│   ├── chainsaw-test.yaml
│   ├── source-configmap.yaml
│   └── workload.yaml
└── referenced-data-flag-off/
    ├── chainsaw-test.yaml
    └── workload.yaml
```

hack additions (following the federation worktree pattern):
```
hack/e2e/
├── kind-control-plane.yaml            # existing (federation worktree → main)
├── make-internal-kubeconfig.sh        # existing
└── patch-cluster-secret.sh           # existing
```

---

## 10. Prerequisites before any scenario can run

The following implementation milestones from the plan must be complete
before the e2e scenarios above are executable:

| Phase | What must land |
|---|---|
| Phase 0 | A: gate constant `ReferencedData`, condition `ReferencedDataReady`, label/annotation constants, codegen |
| Phase 0 | A: gate insertion behind `--enable-referenced-data-gate` flag |
| Phase 1 | B: `ReferencedDataController` (materialize companions, annotate WD) |
| Phase 1 | B: federator PropagationPolicy extension (CM/Secret label selector) |
| Phase 2 | C: cell gate-clearing in `InstanceReconciler` |
| Phase 3 | D: gate honoring, Pod-spec translation, companion mirroring (same-cluster path) |
| Phase 3 | E: SAR admission check |

Scenario F (single-cluster) can run after Phases 0+1+2+3 without Karmada.
Scenario G (flag off) can run after Phase 0 alone.
Scenario B (unauthorized) requires Phase 3 (E: SAR).
All others require all phases.
