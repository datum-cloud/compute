# Multi-Cluster E2E Design — ConfigMap/Secret Mounts

Companion to [`configmap-secret-mounts-e2e.md`](./configmap-secret-mounts-e2e.md) and
[`configmap-secret-mounts.md`](./configmap-secret-mounts.md).

The existing e2e plan used the provider in `SameCluster=true` mode as a practical
simplification. This document designs the higher-fidelity alternative: a **four-cluster
Kind topology** that exercises the real federated, cross-plane, cross-cluster path —
including Karmada propagation, `ns-{project-uid}` namespace mapping, and the provider's
cross-cluster companion-mirroring with a real kubelet. Every cluster name, Taskfile
target, and config path below is grounded in the actual harness files.

---

## 1. Actual harness — grounded baseline

### 1.1 What `task e2e:up` creates today (federation worktree)

The federation Taskfile (`/.claude/worktrees/federation/Taskfile.yaml`) creates
**three** Kind clusters:

| Cluster | Kind var | Nodes | Config |
|---|---|---|---|
| `compute-control-plane` | `KIND_CONTROL_PLANE` | 1 control-plane | `hack/e2e/kind-control-plane.yaml` — adds `extraPortMappings: 32443` for the Karmada NodePort |
| `compute-pop-dfw` | `KIND_POP_DFW` | 1 control-plane | default Kind config (single node) |
| `compute-pop-ord` | `KIND_POP_ORD` | 1 control-plane | default Kind config (single node) |

Karmada v1.16.0 is installed into `compute-control-plane` via
`helm install karmada karmada-charts/karmada` (NodePort 32443). The POP clusters
are joined via `karmadactl join` + `hack/e2e/patch-cluster-secret.sh` (which also
patches `spec.apiEndpoint` on the Karmada `Cluster` object to use the Docker-bridge
IP). City-code labels (`topology.datum.net/city-code={dfw,ord}`) are applied via
`kubectl label cluster`.

Kubeconfigs land in `tmp/e2e/kubeconfigs/`:
`control-plane.yaml`, `karmada.yaml`, `pop-dfw.yaml`, `pop-ord.yaml`,
`pop-dfw-internal.yaml`, `pop-ord-internal.yaml`.

**Known discrepancy**: `test/e2e/chainsaw-config.yaml` in the federation worktree
declares the Karmada cluster as `downstream:` pointing to
`tmp/e2e/kubeconfigs/downstream.yaml`, but the Taskfile writes the file as
`karmada.yaml`. Either a symlink step is missing from `e2e:karmada:install` or the
chainsaw config predates a rename. A `cp`/symlink step must be added:

```yaml
# addition to _e2e:karmada:build-kubeconfig
- cp {{.KUBECONFIG_DIR}}/karmada.yaml {{.KUBECONFIG_DIR}}/downstream.yaml
```

### 1.2 Operator processes (`task e2e:operator:start`)

Two `go run ./cmd/main.go` background processes:

- **Management operator** — `KUBECONFIG=control-plane.yaml`,
  `--karmada-kubeconfig=karmada.yaml --enable-cell-controllers=false`
- **Cell operator (dfw)** — `KUBECONFIG=pop-dfw.yaml`,
  `--karmada-kubeconfig=karmada.yaml --enable-management-controllers=false`

The `--downstream-kubeconfig` flag exists in the federation branch `cmd/main.go`
but is not wired in `e2e:operator:start`. It is the flag through which the
`InstanceProjector` reaches Karmada as the downstream write target.

### 1.3 Chainsaw configuration

`test/e2e/chainsaw-config.yaml` declares four named clusters:
`default` (control-plane), `downstream` (Karmada), `pop-dfw`, `pop-ord`.
Test steps use `cluster: pop-dfw` etc. Chainsaw v0.2.15 is pinned in the Taskfile.

### 1.4 What is on `main` vs. the federation worktree only

| Artifact | `main` | Federation worktree |
|---|---|---|
| `Taskfile.yaml` with e2e tasks | No | Yes |
| `hack/e2e/` scripts | No | Yes |
| `test/e2e/chainsaw-config.yaml` | No | Yes |
| `test/e2e/full-federation/` et al. | No | Yes |
| `config/components/federation/` | No | Yes |
| `cmd/main.go` with `--downstream-kubeconfig` | No | Yes |
| `WorkloadDeploymentFederator` + `InstanceProjector` | No | Yes |

The full multi-cluster e2e topology requires the federation code from the B2 merge.
Everything in this document that references federation controllers,
`WorkloadDeploymentFederator`, Karmada propagation, or `InstanceProjector` is
**gated on B2** (the federation branch landing on main).

### 1.5 Unikraft provider topology and `SameCluster` flag

The unikraft-provider (`go.datum.net/unikraft-provider`) config struct
(`internal/config/config.go`) has:

```go
type DownstreamResourceManagementConfig struct {
    KubeconfigPath string `json:"kubeconfigPath"`
    SameCluster    bool   `json:"sameCluster,omitempty"` // +default=false
    ...
}
```

When `KubeconfigPath == ""`, `RestConfig()` falls back to the in-cluster / current
kubeconfig — making upstream == downstream. `SameCluster=true` explicitly short-circuits
companion mirroring. `SameCluster=false` (the default) means the provider mirrors
labeled companions from the cell namespace into the downstream Pod namespace before
creating the Pod.

The provider's existing e2e (`test/e2e/instance/01-create-instance/`) uses a single
Kind cluster (`datum-cloud/test-infra`'s `kind-bootstrap` action) and only asserts
that a Pod materializes — it does not exec into the Pod or validate mounts.

---

## 2. Why multi-cluster matters: what same-cluster cannot prove

The existing e2e plan (`configmap-secret-mounts-e2e.md`) validated the feature with
the provider in `SameCluster=true` mode on `pop-dfw`. That exercises:
- The resolver materializing companions in `ns-{project-uid}` on the management plane
- Karmada propagating the WD + labeled companions to the cell (Hops 3–4)
- The cell gate-clearing reconciler (Hop 5)
- The provider translating Instance volumes/env into a Pod spec (Hop 6)
- The real kubelet mounting them (Hop 7)

What `SameCluster=true` **cannot** prove:

| Gap | Why it matters |
|---|---|
| **Cross-cluster companion mirroring** | The provider's `mirrorCompanions` code path (`SameCluster=false`) is exercised only with fake clients in unit tests (`TestMirrorCompanions_SeparateCluster_Copied`). In production the kraftlet cluster is always separate. |
| **Namespace-mapping invariant across real cluster boundary** | The `ns-{project-uid}` namespace must exist on the kraftlet cluster and companions must be mirrored there. Same-cluster elides this: the namespace trivially already contains companions. |
| **Pod creation blocked by missing mirror** | In `SameCluster=false` mode the provider returns an error if mirroring fails. Only a real separate cluster exposes actual RBAC and network failure modes. |
| **Deletion cleanup on a separate cluster** | `handleDeletion` deletes mirrored companions from the downstream cluster. With same-cluster this is a no-op (`SameCluster=true` skips it). |
| **Karmada propagation of companions by label selector** | The `full-federation` Chainsaw test exercises WD propagation but not CM/Secret propagation. Multi-cluster asserts the always-on label selector in the PropagationPolicy actually delivers companions to the cell. |
| **Real-kubelet mount in the separate kraftlet cluster** | `kubectl exec` into the Pod on the kraftlet cluster — not the cell cluster — is the production-equivalent path. Same-cluster mode has the Pod running in the cell namespace, which differs from production where it runs in the kraftlet cluster's Pod namespace. |

---

## 3. Target cluster topology

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Cluster A: compute-control-plane (Kind, 1 node)                             │
│                                                                              │
│  Management plane:                                                           │
│    go run ./cmd/main.go --enable-cell-controllers=false                      │
│      --karmada-kubeconfig=karmada.yaml                                       │
│      --downstream-kubeconfig=karmada.yaml   (InstanceProjector target)       │
│    ReferencedDataController   (Workstream B — materializes companions)        │
│    WorkloadDeploymentFederator (replicates WD → ns-{project-uid} in Karmada) │
│    InstanceProjector          (reads Instance writebacks from Karmada)        │
│                                                                              │
│  Resources:                                                                  │
│    user namespace: Workload, WorkloadDeployment projections, Instance        │
│                    projections, source ConfigMaps/Secrets                     │
│    ns-{project-uid}: companion ConfigMaps/Secrets (labeled                   │
│                       compute.datumapis.com/referenced-data: "true")          │
│                                                                              │
│  Karmada API server (NodePort 32443):                                        │
│    stores WDs + companions in ns-{project-uid}                               │
│    PropagationPolicy → compute-pop-dfw (city-code=dfw)                       │
└──────────────────────────────┬───────────────────────────────────────────────┘
                               │ Karmada propagates:
                               │   WorkloadDeployment
                               │   companion ConfigMaps (label selector)
                               │   companion Secrets (label selector)
                               ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  Cluster B: compute-pop-dfw (Kind, 1 node)     [cell / edge plane]          │
│                                                                              │
│  Cell operator:                                                              │
│    go run ./cmd/main.go --enable-management-controllers=false                │
│      --karmada-kubeconfig=karmada.yaml                                       │
│    WorkloadDeploymentReconciler (creates Instances, manages gates)           │
│    InstanceReconciler (quota gate, ReferencedData gate clearing,             │
│                        writes Instance back to Karmada)                      │
│                                                                              │
│  Resources (all in ns-{project-uid}):                                       │
│    WorkloadDeployment (propagated by Karmada)                                │
│    Instance (gate: ReferencedData → cleared when companions present)         │
│    companion ConfigMaps/Secrets (propagated by Karmada)                      │
│    NO Pods — provider creates them on Cluster D                              │
│                                                                              │
│  unikraft-provider:                                                          │
│    upstream: pop-dfw.yaml (reads Instances + companions from here)           │
│    downstream: kraftlet.yaml (SameCluster=false)                             │
│    mirrors companions → Cluster D ns-{project-uid}                          │
│    creates Pod on Cluster D                                                  │
└──────────────────────────────┬───────────────────────────────────────────────┘
                               │ provider mirrors companions + creates Pod
                               ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  Cluster D: compute-kraftlet (Kind, 1 node)   [kraftlet / downstream]        │
│                                                                              │
│  Real kubelet (Kind node — not a virtual-kubelet)                            │
│                                                                              │
│  Resources (in ns-{project-uid}):                                            │
│    mirrored companion ConfigMaps/Secrets (written by provider mirror step)   │
│    Pod (created by provider; nodeSelector: kubernetes.io/os=linux)           │
│       volumes: ConfigMap + Secret sources referencing companion names        │
│       volumeMounts: /etc/config, /etc/secret                                 │
│       env.ValueFrom / envFrom                                                │
│                                                                              │
│  kubectl exec → validates file and env contents                              │
└──────────────────────────────────────────────────────────────────────────────┘

Cluster C: compute-pop-ord (Kind, 1 node)  [second cell, optional for this feature]
  Unchanged from the existing harness — used for placement/city-code scenarios.
```

**Kubeconfigs produced** (`tmp/e2e/kubeconfigs/`):

| File | Used by |
|---|---|
| `control-plane.yaml` | Chainsaw default cluster; management operator KUBECONFIG |
| `karmada.yaml` / `downstream.yaml` | Chainsaw `downstream:` cluster; both operators `--karmada-kubeconfig`; management operator `--downstream-kubeconfig` |
| `pop-dfw.yaml` | Chainsaw `pop-dfw:` cluster; cell operator KUBECONFIG |
| `pop-dfw-internal.yaml` | Karmada controller (Docker-IP address) |
| `pop-ord.yaml` | Chainsaw `pop-ord:` cluster |
| `pop-ord-internal.yaml` | Karmada controller |
| `kraftlet.yaml` | Chainsaw `kraftlet:` cluster (new); provider `downstreamResourceManagement.kubeconfigPath` |
| `kraftlet-internal.yaml` | (optional) if the provider runs inside Docker |

---

## 4. What's new vs. what already exists

### 4.1 Already exists (in federation worktree, lands with B2)

- `hack/e2e/kind-control-plane.yaml` — control-plane Kind config
- `hack/e2e/make-internal-kubeconfig.sh` — Docker-IP kubeconfig rewriter
- `hack/e2e/patch-cluster-secret.sh` — Karmada cluster secret + apiEndpoint patcher
- `Taskfile.yaml` — all `e2e:*` targets through `e2e:operator:start`
- `test/e2e/chainsaw-config.yaml` — four-cluster Chainsaw config (missing `kraftlet`)
- `test/e2e/full-federation/`, `workload-deployment-federation/`, etc.

### 4.2 New for multi-cluster companion-mirroring topology

**New Kind cluster: `compute-kraftlet`**

Kind config at `hack/e2e/kind-kraftlet.yaml`:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
```

A single-node cluster is sufficient. The provider creates Pods targeting
`kubernetes.io/os: linux`, which every Kind control-plane node satisfies.

**New Taskfile variable and cluster target** (additions to `Taskfile.yaml`):

```yaml
vars:
  KIND_KRAFTLET: compute-kraftlet

e2e:clusters:create:
  # add after existing cluster creates:
  - task: _e2e:cluster:create
    vars:
      CLUSTER_NAME: "{{.KIND_KRAFTLET}}"
      KIND_CONFIG: hack/e2e/kind-kraftlet.yaml
  # export kubeconfig:
  - kind export kubeconfig --name {{.KIND_KRAFTLET}} --kubeconfig {{.KUBECONFIG_DIR}}/kraftlet.yaml
```

**New `e2e:up:multicluster` task** that wraps `e2e:up` and adds the kraftlet cluster:

```yaml
e2e:up:multicluster:
  desc: "Stand up the full multi-cluster topology (control-plane + Karmada + 2 POPs + kraftlet)"
  cmds:
    - task: e2e:up            # existing: control-plane + Karmada + pop-dfw + pop-ord
    - task: _e2e:cluster:create
      vars:
        CLUSTER_NAME: "{{.KIND_KRAFTLET}}"
        KIND_CONFIG: hack/e2e/kind-kraftlet.yaml
    - kind export kubeconfig --name {{.KIND_KRAFTLET}} --kubeconfig {{.KUBECONFIG_DIR}}/kraftlet.yaml

e2e:down:multicluster:
  desc: "Tear down the full multi-cluster topology"
  cmds:
    - task: e2e:down          # existing: deletes control-plane + pop-dfw + pop-ord
    - kind delete cluster --name {{.KIND_KRAFTLET}} 2>/dev/null || true
```

**Updated `e2e:operator:start`** for the multi-cluster scenario:

The cell operator on pop-dfw needs no change — it uses its own KUBECONFIG and
writes Instances back to Karmada via `--karmada-kubeconfig`. The provider, however,
must be started separately with a config file that sets `SameCluster=false` and
points `kubeconfigPath` at the kraftlet kubeconfig.

Provider config file (`hack/e2e/provider-multicluster.yaml`):

```yaml
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: UnikraftProvider
metricsServer:
  bindAddress: "0"
  secureServing: false
discovery:
  mode: single
downstreamResourceManagement:
  kubeconfigPath: "tmp/e2e/kubeconfigs/kraftlet.yaml"
  sameCluster: false
  managedResourceLabels:
    managed-by: "infra-provider-unikraft"
locationClassName: "self-managed"
```

Taskfile target to start the provider against pop-dfw upstream / kraftlet downstream:

```yaml
e2e:provider:start:multicluster:
  desc: "Start the unikraft-provider with separate upstream (pop-dfw) and downstream (kraftlet)"
  cmds:
    - |
      KUBECONFIG={{.KUBECONFIG_DIR}}/pop-dfw.yaml \
      go run <path-to-unikraft-provider>/cmd/main.go \
        --config=hack/e2e/provider-multicluster.yaml \
        --health-probe-bind-address=:9093 \
        > {{.E2E_DIR}}/logs/provider-multicluster.log 2>&1 &
      echo $! > {{.E2E_DIR}}/pids/provider-multicluster.pid
```

**Updated `test/e2e/chainsaw-config.yaml`** — add the `kraftlet` cluster:

```yaml
spec:
  clusters:
    downstream:
      kubeconfig: tmp/e2e/kubeconfigs/downstream.yaml
    pop-dfw:
      kubeconfig: tmp/e2e/kubeconfigs/pop-dfw.yaml
    pop-ord:
      kubeconfig: tmp/e2e/kubeconfigs/pop-ord.yaml
    kraftlet:                                          # NEW
      kubeconfig: tmp/e2e/kubeconfigs/kraftlet.yaml
```

**CRD installation**: compute CRDs must also be applied to the kraftlet cluster
(the provider creates Instance-referencing Pods there and the provider controller
watches Instances on the upstream). Add `{{.KUBECONFIG_DIR}}/kraftlet.yaml` to
the `_e2e:crds:compute` loop.

**Downstream RBAC on the kraftlet cluster**: the provider needs `create/update/delete`
on ConfigMaps and Secrets in `ns-{project-uid}` on the kraftlet cluster. In the
e2e environment, apply a permissive ClusterRole:

```yaml
# hack/e2e/kraftlet-rbac.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: provider-companion-mirror
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets", "pods", "services"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

Applied by a new Taskfile target `e2e:kraftlet:rbac` called from `e2e:up:multicluster`.

**The `downstream.yaml` symlink gap**: add to `_e2e:karmada:build-kubeconfig`:

```yaml
- cp {{.KUBECONFIG_DIR}}/karmada.yaml {{.KUBECONFIG_DIR}}/downstream.yaml
```

---

## 5. Validation path — hop-by-hop with cluster assignments

The following extends Section 4.1 of `configmap-secret-mounts-e2e.md` by adding
the cross-cluster mirroring hops (6a–6c) and specifying which cluster each
assertion targets.

```
[Hop 1] User creates ConfigMap + Secret + Workload
  Cluster: control-plane (Chainsaw default)
  Assert: Workload admission accepted (or denied for Scenario B)

[Hop 2] ReferencedDataController materializes companions
  Cluster: control-plane (management operator)
  Assert on control-plane:
    – companion configmap.app-config in ns-{project-uid}, label referenced-data=true
    – companion secret.app-secret in ns-{project-uid}, label referenced-data=true
    – WD annotation expected-referenced-data = JSON array of companion names
    – WD condition ReferencedDataReady (reason not SourceNotFound)

[Hop 3] Karmada propagation
  Cluster: downstream (Karmada API server)
  Assert on downstream (cluster: downstream):
    – WD in ns-{project-uid} exists
    – configmap.app-config in ns-{project-uid} exists
    – secret.app-secret in ns-{project-uid} exists
  (Confirms the always-on label-selector in the PropagationPolicy works)

[Hop 4] Karmada → cell propagation
  Cluster: pop-dfw
  Assert on pop-dfw (cluster: pop-dfw):
    – WD in ns-{project-uid} exists
    – companion ConfigMap in ns-{project-uid} exists (same namespace as WD)
    – companion Secret in ns-{project-uid} exists

[Hop 5] Cell InstanceReconciler gate clearing
  Cluster: pop-dfw
  Assert on pop-dfw (cluster: pop-dfw):
    – Instance test-wd-0 exists in ns-{project-uid}
    – Instance initially has SchedulingGate "ReferencedData"
    – After companions present: gate removed, condition ReferencedDataReady=True

[Hop 6a] Provider mirrors companions to kraftlet cluster
  Cluster: kraftlet (NEW assertion)
  Assert on kraftlet (cluster: kraftlet):
    – ns-{project-uid} exists (created by provider or pre-created)
    – configmap.app-config in ns-{project-uid} with label referenced-data=true
    – secret.app-secret in ns-{project-uid} with label referenced-data=true
    – owner references are absent (upstream owners don't exist on kraftlet)
  This is the assertion that SameCluster=true cannot provide.

[Hop 6b] Provider creates Pod on kraftlet cluster
  Cluster: kraftlet
  Assert on kraftlet (cluster: kraftlet):
    – Pod in ns-{project-uid} with label upstream.instance=<instance-uid>
    – Pod spec volumes contain ConfigMap source referencing configmap.app-config
    – Pod spec volumes contain Secret source referencing secret.app-secret
    – Pod spec containers[0].volumeMounts at /etc/config and /etc/secret
    – Pod spec containers[0].env contains ValueFrom.SecretKeyRef entries
    – Pod status.phase == Running

[Hop 7] Real kubelet mounts (kraftlet Kind node)
  Cluster: kraftlet
  Assert via kubectl exec on kraftlet cluster:
    – cat /etc/config/<key> == expected ConfigMap value
    – env | grep SECRET_KEY contains expected Secret value

[Hop 8] Instance writeback (optional, federation chain)
  Cluster: downstream (Karmada)
  Assert: Instance written back with label upstream-cluster-name
  Cluster: control-plane
  Assert: Instance projected with status
```

**Namespace invariant checkpoint** (assert at Hop 6a):

```yaml
# At this step: WD ns = companion ns on pop-dfw = companion ns on kraftlet = ns-{project-uid}
- name: assert-namespace-invariant
  cluster: kraftlet
  try:
  - script:
      content: |
        kubectl --kubeconfig=tmp/e2e/kubeconfigs/control-plane.yaml \
          get namespace "$NAMESPACE" -o template='{{printf "ns-%s" .metadata.uid}}'
      outputs:
      - name: downstreamNS
        value: ($stdout)
  - assert:
      resource:
        apiVersion: v1
        kind: ConfigMap
        metadata:
          namespace: ($downstreamNS)
          name: configmap.app-config
          labels:
            compute.datumapis.com/referenced-data: "true"
```

---

## 6. Asserting "mounted inside the instance" — real-kubelet on Kind

### 6.1 Why a Kind node is a faithful proxy

The production path uses a UKC virtual-kubelet (not exec-able). The Kind control-plane
node has a real kubelet that natively mounts ConfigMaps/Secrets via projected volumes.
What the Kind path validates:

- Provider translates `InstanceVolume` (ConfigMap/Secret sources) → `core.Volume`
- Provider translates `VolumeAttachment` → `core.VolumeMount`
- Provider carries `env.ValueFrom` and `envFrom` through to the Pod container spec
- Provider mirrors companions before Pod creation (SameCluster=false path)
- Real kubelet populates the mount: file content matches the companion data
- `kubectl exec` into the Pod confirms file and env are visible to the process

### 6.2 Provider nodeSelector override for the kraftlet cluster

Production provider config uses `nodeSelector: kubernetes.io/hostname: kraftlet` and
a UKC virtual-kubelet taint toleration. For the e2e environment, `provider-multicluster.yaml`
must override this so Pods schedule on the real Kind node:

```yaml
# In the provider config or as a Kustomize patch on the Instance template
# The provider reads nodeSelector from its ProviderConfig, not from the Instance.
# For e2e, configure the provider with:
nodeSelector:
  kubernetes.io/os: linux   # schedules on any Kind node
tolerations: []             # no UKC taint
```

The provider's `buildPodSpecFromContainers` populates `nodeSelector` from the
`ProviderConfig`. In the e2e config, set `providerConfigStrategy.single.name` to a
test ProviderConfig that overrides the selector. This follows the existing
`config/dev/example-same-cluster.yaml` pattern.

### 6.3 Test image

Use `docker.io/library/busybox:stable` as the test container image — it has `cat`,
`sh`, and `env`. The provider's `buildPodSpecFromContainers` uses the image from
`Instance.Spec.Runtime.Sandbox.Containers[].Image`; set this in the test Workload.

### 6.4 Chainsaw exec step

```yaml
- name: verify-file-and-env-on-kraftlet
  cluster: kraftlet
  try:
  - script:
      timeout: 60s
      content: |
        NS=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/control-plane.yaml \
          get namespace "$NAMESPACE" -o template='{{printf "ns-%s" .metadata.uid}}')
        POD=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/kraftlet.yaml \
          get pod -n "$NS" \
          -l "upstream.instance=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/pop-dfw.yaml \
            get instance -n "$NS" test-wd-0 -o jsonpath='{.metadata.uid}')" \
          -o jsonpath='{.items[0].metadata.name}')
        FILE=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/kraftlet.yaml \
          exec -n "$NS" "$POD" -- cat /etc/config/app.properties)
        test "$FILE" = "expected-value"
        ENV_VAL=$(kubectl --kubeconfig=tmp/e2e/kubeconfigs/kraftlet.yaml \
          exec -n "$NS" "$POD" -- sh -c 'echo $DB_PASSWORD')
        test "$ENV_VAL" = "expected-secret-value"
```

### 6.5 Residual gap vs. live UKC

The Kind kubelet path does NOT validate:
- UKC virtual-kubelet behavior with ConfigMap/Secret projected volumes
- UKC's handling of `envFrom` (memory notes that kraftlet/UKC SDK has no
  registry-auth field and ignores `ImagePullSecrets`; ConfigMap/Secret volume
  support is similarly unconfirmed without a live UKC test)

This gap must be accepted and documented as a manual acceptance step in the
unikraft-provider release checklist, run against a live UKC metro with real
credentials.

---

## 7. Scenarios — multi-cluster vs. same-cluster tier placement

### Scenarios requiring multi-cluster (this doc)

| Scenario | What's unique | Directory |
|---|---|---|
| **MC-A: Federated happy path** | Full Hop 1–8 chain; Karmada companion propagation; provider mirroring (SameCluster=false); real kubelet exec on kraftlet | `test/e2e/referenced-data-multicluster-happy-path/` |
| **MC-B: Cross-cluster mirroring** | Assert companions land on kraftlet BEFORE Pod is created; no gate cleared yet | `test/e2e/referenced-data-mirror-timing/` |
| **MC-C: Namespace-mapping invariant** | Assert ns-{project-uid} matches on control-plane, Karmada, pop-dfw, and kraftlet | part of MC-A |
| **MC-D: Placement by city-code** | WD with city=dfw propagated to pop-dfw only; ord cell gets no WD | extends existing `workload-deployment-federation/` |
| **MC-E: Mirrored companion deletion** | Delete Instance; assert provider removes companions from kraftlet but not from pop-dfw | `test/e2e/referenced-data-mirror-deletion/` |

### Scenarios that stay in the same-cluster / envtest tiers

These do not need the four-cluster topology and are defined in
`configmap-secret-mounts-e2e.md`:

| Scenario | Tier |
|---|---|
| A — Happy path (same-cluster, SameCluster=true) | Full e2e (existing 3-cluster harness) |
| B — Unauthorized reference | Full e2e |
| C — SourceNotFound / recovery | Full e2e |
| D — SourceTooLarge | Full e2e |
| E — Rotation + restart annotation | Full e2e |
| F — Single-cluster collapse (no Karmada) | e2e, control-plane only |
| G — Feature flag off | e2e |
| Resolver unit (collector, reader, SAR) | envtest / unit |
| Cell gate state machine | envtest / unit |
| Pod-spec translation, gate-respect, mirroring | unit (fake clients — instance_controller_phase3_test.go) |

---

## 8. How Chainsaw addresses N clusters

Chainsaw v0.2.15 (pinned in the Taskfile) supports multiple named clusters in
`chainsaw-config.yaml`. Steps select a cluster with `cluster: <name>`. This is
already established in the federation worktree's test suite (see
`test/e2e/full-federation/chainsaw-test.yaml` which uses `cluster: downstream`,
`cluster: pop-dfw`, and the default). Adding `cluster: kraftlet` follows exactly
the same pattern.

Script steps must pass `--kubeconfig=tmp/e2e/kubeconfigs/kraftlet.yaml` explicitly
to `kubectl` calls that target the kraftlet cluster (the `KUBECONFIG` env var in
the test process points to the default/control-plane cluster).

---

## 9. CI tiering and B2 gate

### 9.1 B2 gate

The four-cluster topology is **only runnable after B2** (the federation branch
merges to main). Before B2:

- The `WorkloadDeploymentFederator`, `InstanceProjector`, `ReferencedDataController`,
  and `e2e:operator:start` federation flags do not exist on main.
- The multi-cluster Taskfile targets and `hack/e2e/` scripts are in the federation
  worktree only.

**What can be validated before B2** (two-cluster subset, no Karmada):

Stand up just `compute-pop-dfw` (upstream) and `compute-kraftlet` (downstream),
run the provider with `SameCluster=false`, pre-create a labeled companion ConfigMap
on pop-dfw, and assert:
1. Provider mirrors the companion to kraftlet.
2. Provider creates a Pod on kraftlet with the correct volume reference.
3. Real kubelet mounts the companion; `kubectl exec` confirms file content.

This two-cluster subset validates Workstream D (mirroring + Pod creation) independently
of Karmada. Taskfile target: `e2e:provider:test:two-cluster`.

### 9.2 Runtime cost estimate

| Component | Time |
|---|---|
| `task e2e:up:multicluster` (4 clusters + Karmada) | 5–8 min (first run); ~45s if clusters exist |
| CRDs + RBAC on all 4 clusters | ~1 min |
| Operator + provider start | ~45s |
| Scenario MC-A (full chain + exec) | ~4–6 min |
| Scenarios MC-B, MC-E | ~3 min each |
| Total multi-cluster gate | ~15–20 min |

Resource footprint: 4 Kind clusters × ~500 MB RAM each + Karmada ≈ 3–4 GB total.
This fits on a standard GitHub Actions `ubuntu-latest` (7 GB RAM) but leaves little
headroom. A `ubuntu-latest-8cores` runner or self-hosted runner with ≥8 GB is
recommended.

### 9.3 Proposed CI split

| Test set | Runner | When |
|---|---|---|
| Unit + envtest (all workstreams) | `ubuntu-latest` | Every PR |
| Same-cluster e2e (3 clusters, Scenarios A–G) | `ubuntu-latest` | Main merges, nightly |
| Multi-cluster e2e (4 clusters, Scenarios MC-A–E) | `ubuntu-latest-8cores` or self-hosted | Main merges, nightly |
| Two-cluster provider test (pre-B2) | `ubuntu-latest` | Every PR on provider repo |

Chainsaw filter flag to run only multi-cluster scenarios:

```yaml
e2e:test:multicluster:
  desc: "Run only multi-cluster e2e scenarios"
  deps: [e2e:tools:chainsaw]
  cmds:
    - |
      KUBECONFIG={{.KUBECONFIG_DIR}}/control-plane.yaml \
      {{.CHAINSAW}} test \
        --config test/e2e/chainsaw-config.yaml \
        --include-test-regex "multicluster|mirror" \
        test/e2e/
```

---

## 10. Gaps and risks

| Risk | Severity | Mitigation |
|---|---|---|
| **4-cluster RAM pressure in CI** | High | Use ≥8 GB runner. Consider collapsing pop-ord out of the multi-cluster suite (dfw suffices for companion propagation tests). |
| **Provider namespace pre-creation on kraftlet** | Medium | The provider must create `ns-{project-uid}` on the kraftlet cluster before mirroring. Add namespace creation to `mirrorCompanions` or use a namespace controller. Assert in MC-B that the namespace exists before Pod creation. |
| **Karmada multi-kind selector (CM+Secret+WD in one PP)** | Medium | Plan calls this out (Workstream B, risk R2). Validate with `propagation-policy-lifecycle` scenario against Karmada v1.16.0. The always-on selector must be tested at Hop 3. |
| **Provider kubeconfig → Docker-IP for internal use** | Medium | When the provider runs as a background `go run` process on the macOS host, `kraftlet.yaml` uses localhost — fine. When run in a container (CI), the kraftlet kubeconfig needs the Docker-IP variant (add `make-internal-kubeconfig.sh` call for kraftlet in CI). |
| **downstream.yaml symlink gap** | Low | Add `cp karmada.yaml downstream.yaml` step in `_e2e:karmada:build-kubeconfig`. File this against the federation worktree Taskfile before B2 merge. |
| **Provider RBAC on kraftlet** | Low | `hack/e2e/kraftlet-rbac.yaml` ClusterRole covers the e2e case. Production RBAC is a separate provider deployment concern. |
| **Rotation race on separate cluster** | Low | Companion rotation must propagate: control-plane → Karmada → cell → provider mirror → kraftlet. The `restartedAt` annotation drives the stateful roll independently. Add a deliberate pause in scenario MC-A rotation variant to expose the race. |
| **UKC virtual-kubelet gap** | High (accepted) | Documented explicitly in Section 6.5. Manual acceptance required before provider GA. |
| **B2 dependency** | High (timeline) | The full topology is blocked on federation merge. Two-cluster provider test (Section 9.1) unblocks Workstream D validation before B2. |

---

## 11. File layout — new files and Taskfile additions

```
hack/e2e/
├── kind-control-plane.yaml        existing (federation worktree)
├── kind-kraftlet.yaml             NEW — single control-plane node, no special ports
├── make-internal-kubeconfig.sh    existing
├── patch-cluster-secret.sh        existing
├── kraftlet-rbac.yaml             NEW — ClusterRole for provider mirroring on kraftlet
└── provider-multicluster.yaml     NEW — UnikraftProvider config (SameCluster=false)

test/e2e/
├── chainsaw-config.yaml           extend: add kraftlet cluster entry
├── referenced-data-multicluster-happy-path/
│   ├── chainsaw-test.yaml         NEW — Hops 1–8, cluster: kraftlet assertions
│   ├── source-configmap.yaml      NEW
│   ├── source-secret.yaml         NEW
│   └── workload.yaml              NEW
├── referenced-data-mirror-timing/
│   ├── chainsaw-test.yaml         NEW — companion on kraftlet before gate cleared
│   └── workload.yaml              NEW
└── referenced-data-mirror-deletion/
    ├── chainsaw-test.yaml         NEW — delete Instance, assert kraftlet cleanup
    └── workload.yaml              NEW

Taskfile.yaml additions:
  vars:       KIND_KRAFTLET: compute-kraftlet
  targets:    e2e:up:multicluster
              e2e:down:multicluster
              e2e:provider:start:multicluster
              e2e:kraftlet:rbac
              e2e:test:multicluster
              e2e:provider:test:two-cluster   (pre-B2 provider validation)
```

---

## 12. Prerequisites — what must land before scenarios are runnable

| Phase | Required for | What must exist |
|---|---|---|
| Before B2 | Two-cluster provider test | Workstream D: mirroring (SameCluster=false), Pod-spec translation, gate honoring. `hack/e2e/kind-kraftlet.yaml` + `provider-multicluster.yaml`. |
| B2 merge | All MC scenarios | Federation controllers on main, `Taskfile.yaml` e2e targets, `hack/e2e/` scripts, `test/e2e/chainsaw-config.yaml`. |
| Phase 0 | All scenarios | Gate constant, annotation/label constants, codegen. |
| Phase 1 | MC-A, MC-B, MC-C | `ReferencedDataController` (companions + annotation); federator PropagationPolicy CM/Secret label selector. |
| Phase 2 | MC-A gate-clearing | Cell `InstanceReconciler` `reconcileReferencedDataCondition`. |
| Phase 3 | MC-A exec assert, MC-B, MC-E | Provider mirroring + Pod-spec translation wired to real clusters. |
| Feature flag on | MC-A–MC-E | `--enable-referenced-data-gate=true` in management operator start command. |
