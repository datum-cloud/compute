# Implementation Plan — ConfigMap/Secret Mounts

Companion to the RFC [`../rfcs/configmap-secret-mounts.md`](../rfcs/configmap-secret-mounts.md).
This is the engineering breakdown: five workstreams, the contracts that bind them,
the critical path, and the decisions to settle before coding. Produced from a
parallel planning pass across the compute, unikraft-provider, and milo codebases.

## Workstreams

| # | Workstream | Repo / plane | Owner agent |
|---|---|---|---|
| A | API types, validation, gate insertion | compute (api/, validation/, instancecontrol/) | api |
| B | Referenced-data resolver + federation delivery | compute (management plane) | resolver |
| C | Cell-side gate clearing + observability | compute (cell plane) | cell |
| D | Unikraft provider consumption | unikraft-provider | provider |
| E | Scoped read client, IAM, admission authz | compute + milo | security |

## Reconciled cross-workstream contracts

These are the shared symbols every workstream codes against. They are pinned here
to resolve divergences surfaced during planning.

- **Scheduling gate** (on `Instance.Spec.Controller.SchedulingGates[].Name`):
  bare value **`ReferencedData`**, matching the existing `Network`/`Quota` gate
  convention (the existing gates are *not* domain-qualified). Defined in
  `internal/controller/instancecontrol/scheduling_gates.go`. (The RFC's qualified
  spelling referred to the concept, not the literal gate value; normalizing all
  three gates to qualified names is a separate, migration-bearing follow-up.)
- **Companion label** (on derived ConfigMaps/Secrets and on the WD):
  `compute.datumapis.com/referenced-data: "true"`. Qualified, because it's a label.
- **Expected-set annotation** (on the WorkloadDeployment, set by B, read by C):
  `compute.datumapis.com/expected-referenced-data`, value = a **JSON array of the
  expected companion object names** — the actual names that will land on the cell,
  sorted deterministically. The cell does a pure set-membership check against
  labeled companions, with **no name recomputation** (this is the whole reason for an
  explicit annotation). Companion names are the deterministic `<kind>.<source-name>`
  the resolver produces. Pin the constant and encoding in `api/v1alpha/annotations.go`.
- **Restart annotation** (on the template): `compute.datumapis.com/restartedAt`
  (RFC3339). No controller code needed — template metadata already feeds the
  template hash, so the existing ordered in-place roll triggers.
- **Condition**: `ReferencedDataReady` (string value), reason constants
  `Resolving`, `AwaitingPropagation`, `SourceNotFound`, `SourceUnauthorized`,
  `SourceTooLarge`, `Ready`. Defined once in `api/v1alpha` and used on **both** the
  WorkloadDeployment status (resolver view) and the Instance status (cell view).
- **Where each reason is set** (resolves the C↔B ambiguity):
  - *Resolver (B)* owns resolution outcomes it alone can see — `SourceNotFound`,
    `SourceUnauthorized`, `SourceTooLarge`, `Resolving` — written to the **WD**
    status. It does **not** encode per-entry errors into the expected-set
    annotation; the annotation is purely the set of names.
  - *Cell (C)* owns presence on the cell — `AwaitingPropagation` (expected set not
    yet all present) → `Ready` — written to the **Instance** status, and clears the
    gate on `Ready`.
- **Scoped read interface** (E provides, B consumes):
  ```
  type ProjectConfigSecretReader interface {
      GetConfigMap(ctx, projectID, namespace, name) (*corev1.ConfigMap, error)
      GetSecret(ctx, projectID, namespace, name) (*corev1.Secret, error)
  }
  ```
  with sentinel errors `ErrSourceNotFound` / `ErrSourceUnauthorized` that map to the
  reasons above. B falls back to the project cluster's own client when the reader is
  nil (single-cluster / dev), so B is not hard-blocked on E.
- **Package**: standardize on `internal/referenceddata/` for the capability seam
  (collector, types, the `ProjectConfigSecretReader`, metrics). This is the
  promotable unit (see RFC "Platform direction").
- **Capability seam types** (so the delivery half is later promotable):
  `DeliveryRequest{ Subject, ReferencedObjects, DownstreamNamespace }` →
  `DeliveryResult{ CompanionNames, AllDelivered }`. Consumption (gate, Pod-spec
  translation) lives outside this package.
- **Namespace invariant**: on the cell, the WorkloadDeployment, its Instances, and
  the companions all live in the same `ns-{project-uid}` namespace. C lists
  companions there; D mirrors from there. Any divergence breaks gate clearing — this
  invariant must hold and be asserted in tests.
- **Metrics prefix**: `compute_referenced_data_*`.

## Critical path and sequencing

```
Phase 0 (parallel, unblocks everything)
  A: constants + condition/reasons + gate constant + label/annotation + validation + codegen
  E: ProjectConfigSecretReader (interface + impl) + sentinel errors  + admission SAR
  B: capability-seam types + reference collector (no controller yet)
  EXTERNAL: Milo grant for core ConfigMap/Secret read on project planes — CLEARED
            (management identity already has it; no provisioner change needed)

Phase 1  (needs Phase-0 constants + reader interface)
  B: ReferencedDataController (collect→read→materialize→annotate→ref-count→finalizer→single-cluster)
  B: federator PropagationPolicy extension (always-on referenced-data selector)

Phase 2  (needs Phase-0 constants + Phase-1 companion label & annotation format)
  C: cell gate-clearing reconciler + ReferencedDataReady on Instance + events + metrics

Phase 3  (provider; gate-honoring & Pod-spec translation can start in Phase 0)
  D: honor scheduling gates  (independent — land early)
  D: faithful Pod-spec translation: volumes, mounts, env ValueFrom/EnvFrom  (independent)
  D: companion mirroring into the kraftlet cluster + SameCluster flag  (needs label + companions)
  D: watches on companions  (needs label)

Phase 4
  E2E across planes; single-cluster collapse; restart/rotation; rollout enablement
```

**Deployment-ordering safeguard (cross-cutting) — settled: feature flag.** Stamping
the gate (A) must not go live before both gate-clearing (C) and provider
gate-honoring (D) are deployed — otherwise gated instances either launch without data
(D not yet honoring) or hang forever (C not yet clearing). Because the management
plane, cells, and provider roll **independently** in a federated deployment, merge
ordering alone can't guarantee runtime ordering. Gate **insertion** is therefore
behind a **management-plane feature flag (default off)**, enabled fleet-wide only
after C + D are confirmed deployed everywhere. The `AwaitingPropagation` condition
should carry age so a stuck cell is visible. (The single-cluster debut is atomic, so
the flag isn't strictly required there — but we build it once, up front.)

## Workstream A — API, validation, gate insertion

- New types/constants: bulk env `EnvFrom` on `SandboxContainer` (thin Datum wrapper
  types to keep the CRD clean — mirrors the existing `Env`/`corev1.EnvVar`
  pragmatism); `ReferencedDataReady` condition + reason constants; the
  `ReferencedData` gate constant; the `referenced-data` label and
  `expected-referenced-data` / `restartedAt` annotation constants.
- Validation (currently stubbed/forbidden): secret-volume validation; ConfigMap
  **and** Secret `items` key→path projection; `defaultMode`/per-item mode range;
  path safety (relative, no `..`). Table-driven tests.
- Gate insertion: `templateReferencesData(template)` helper; add the `ReferencedData`
  gate at instance creation in `stateful/stateful_control.go` only when the template
  references any ConfigMap/Secret **and** the management-plane feature flag is on
  (see the rollout safeguard).
- `EnvFrom` is **in v1**: add the field on `SandboxContainer` (thin Datum wrapper
  types), wire it into `templateReferencesData` and the collector, validate it, and
  map it in the provider. The delivery machinery is unchanged — the resolver collects
  `(kind, name)` regardless of reference shape.
- Codegen: `make generate manifests` after type changes.

## Workstream B — Resolver + federation delivery (the core)

- New `internal/referenceddata/` (types, collector) + a `ReferencedDataController`
  in `internal/controller/`. Watches WorkloadDeployments (all project clusters) plus
  source ConfigMaps/Secrets (for rotation), via new indexers.
- Reconcile: collect refs → read via `ProjectConfigSecretReader` (nil-fallback to
  project client) → **enforce size limits** (per-object 256 KiB, aggregate 1 MiB per
  WD, both configurable; on breach set `SourceTooLarge` and do **not** materialize —
  the instance holds) → materialize **one shared companion per (kind, source-name)**
  in `ns-{project-uid}` via Milo's `downstreamclient` → stamp the expected-set
  annotation on the WD with the **companion names** (not source refs) →
  ref-count referencing WDs in a companion annotation → finalizer-driven delete when
  the last reference drops. Set the WD's `ReferencedDataReady`/reason on read or size
  failures (`SourceNotFound`/`SourceUnauthorized`/`SourceTooLarge`).
- Federation: extend the federator's existing PropagationPolicy with always-on
  `ConfigMap`/`Secret` selectors matching the `referenced-data` label — this makes
  the federator-vs-resolver ordering race tolerable (a companion materialized later
  is still picked up; the gate holds the instance meanwhile). Prior art: the NSO
  `ClusterPropagationPolicy` already label-propagates secrets/configmaps.
- Single-cluster: same controller, companion is a local copy in the project
  namespace, no PropagationPolicy.
- Wire in `cmd/main.go` (`setupManagementControllers`) with `ProjectReader: nil`
  until E lands. RBAC markers for WD finalizers + core CM/Secret CRUD on the hub.

## Workstream C — Cell gate clearing + observability

- Extend the **cell** `InstanceReconciler` (beside the existing quota-gate clear),
  not a new reconciler. Add `reconcileReferencedDataCondition`: read the
  `expected-referenced-data` annotation off the owning WD (already fetched on the
  cell), list labeled companions in the namespace, diff; set
  `AwaitingPropagation`/`Ready`; clear the `ReferencedData` gate on `Ready` using the
  established two-pass (status-then-spec) discipline.
- Watch labeled companions, mapping to Instances via the
  `workload-deployment-uid` label (new Instance index); add a `RequeueAfter` safety
  net for the case where the annotation arrives after instance creation.
- Events on transitions (gated on `changed`); metrics
  (`compute_referenced_data_*`: companions-present ratio, gate-wait histogram,
  reason-transition counter) following the quota-metrics pattern.
- Refactor: a shared `fetchOwnerWorkloadDeployment` helper (network-failure check and
  this both fetch the WD).

## Workstream D — Unikraft provider consumption

- **Topology (confirmed):** kraftlet resolves Pod ConfigMap/Secret references from
  the cluster the Pod lives in — the **downstream** kraftlet cluster. Companions are
  delivered to the **upstream/cell** namespace. So:
  - Single-cluster/lab (downstream == upstream): companions already present; no
    mirroring.
  - Separate kraftlet cluster: the provider must **mirror** the labeled companions
    from the cell namespace into the downstream Pod namespace before creating the
    Pod. Add a `SameCluster bool` to the downstream config to choose the path
    explicitly.
- Gate honoring: in `Reconcile`, before Pod creation, return (no requeue) while any
  `Spec.Controller.SchedulingGates` remain — the Instance update re-triggers.
- Faithful Pod-spec translation in `buildPodSpecFromContainers`: carry
  `env.ValueFrom` through; map `InstanceVolume` ConfigMap/Secret sources →
  `core.Volume`; map `VolumeAttachment` → `core.VolumeMount`. Reference companions
  natively (kubelet mounts them — no provider-side inlining of secret bytes).
- Watches on labeled companions (upstream) → enqueue Instances in the namespace, so a
  rotated companion re-mirrors. Delete mirrored copies on Instance deletion.
- **External:** downstream RBAC must grant the provider create/update/delete on
  ConfigMaps/Secrets in the Pod namespace (mirroring). `EnvFrom` consumption waits on
  A adding the field.

## Workstream E — Scoped read client, IAM, admission authz

- `internal/referenceddata/` reader: per-project `client.Client` built by
  host-rewriting the management identity's base config to the project control-plane
  path (the same rewrite Milo's multicluster provider uses, **without** the
  quota sub-path — the quota client cannot read core objects). Cache per projectID;
  classify 404→`ErrSourceNotFound`, 403/401→`ErrSourceUnauthorized`. Single-cluster
  variant wraps the local client.
- **IAM (key finding):** core ConfigMaps/Secrets are native Kubernetes objects, so
  access is **Kubernetes RBAC on each project control plane**, *not* Milo IAM
  objects. Need a `ClusterRole` granting `get,list` on `configmaps`/`secrets` bound
  to the management identity on every project plane — provisioned by Milo's project
  provisioner (same mechanism that already grants the operator WD access). **Confirmed
  available** — the grant exists; no provisioner change required.
- Admission SAR: mirror the Network check exactly — build a `SubjectAccessReview`
  from `AdmissionRequest.UserInfo` for `get` on the referenced `configmaps`/`secrets`
  in the Workload namespace; deny if not allowed. Dedupe references; also wire into
  `ValidateUpdate`. References are name-only / same-namespace.

## Decisions (settled 2026-05-31)

1. **Gate value**: bare **`ReferencedData`**, matching the existing `Network`/`Quota`
   gates. Normalizing all three to qualified names is a separate migration if ever
   wanted.
2. **`EnvFrom`**: **in v1** — light lift, delivery machinery unaffected.
3. **Expected-set annotation**: JSON array of the **expected companion names** (not
   source refs), so the cell does pure set-membership with no recomputation.
4. **Gate-enablement rollout**: **management-plane feature flag (default off)**,
   enabled fleet-wide only after C + D are deployed.
5. **Companion size limit**: **resolver-enforced**, per-object **256 KiB** /
   aggregate **1 MiB** per WD (both configurable); on breach **hold** the instance
   and surface `SourceTooLarge` (the `optional` source flag remains the best-effort
   escape hatch).

## Risks / external dependencies

- **Milo grant (E/R1) — CLEARED:** the management identity already has core
  CM/Secret read on project planes; no project-provisioner change needed. No longer a
  blocker on the critical path.
- **Karmada multi-kind selector semantics (B):** one PropagationPolicy selecting WD +
  labeled CM/Secret — standard but validate against the deployed Karmada version.
- **Deployment ordering (cross-cutting):** see safeguard above.
- **Downstream RBAC for mirroring (D):** provider needs CM/Secret write on the
  kraftlet cluster.
- **Namespace invariant (C/D):** companions must co-locate with WD/Instance in
  `ns-{project-uid}`; assert in tests.

## Test & rollout strategy

- **Unit:** validation tables (A); collector + reader + SAR (B/E); condition state
  machine (C); Pod-spec translation + gate-respect (D).
- **Integration (envtest + fake Karmada/downstream):** resolver happy path,
  source-not-found, rotation, ref-count across two WDs, WD deletion cleanup,
  single-cluster, PropagationPolicy contents (B); gate clears when companions
  present / holds when missing / reason transitions (C); SAR allow/deny (E).
- **E2E (Kind + Karmada, Chainsaw):** user creates CM+Secret → Workload references →
  instance comes up with env + file mounts applied, across a federated cell;
  unauthorized-reference rejected at admission; restart picks up a rotated source.
- **Rollout:** enable gate insertion only after C + D are deployed (the ordering
  safeguard); ship single-cluster path first as the lowest-risk validation.
