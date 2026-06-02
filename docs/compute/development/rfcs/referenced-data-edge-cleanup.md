---
id: rfc-referenced-data-edge-cleanup
title: Referenced-Data Edge Cleanup — Hub/Federation-Side Orphan GC
status: draft
created: 2026-06-01
author: architect
supersedes: cell-side CompanionGCReconciler (commits f97a3cb + 9a064cc, feat/configmap-secret-mounts-federated)
---

# Referenced-Data Edge Cleanup — Hub/Federation-Side Orphan GC

## Problem Statement

### Observed Symptom

After deleting a Workload and its source ConfigMap/Secret from the project plane,
companion ConfigMaps/Secrets created by the referenced-data feature are correctly
removed from the **Karmada hub** (`ns-{project-uid}`), but their copies on the
**cell cluster** can persist indefinitely. In a concrete lab reproduction:

- Source `cm-pristine` + `secret-pristine` + Workload `mount-pristine` were
  created in the project plane.
- Companions propagated to cell `datum-us-central-1-lab`, namespace
  `ns-efdf8ca1-9c2d-4ac8-b161-1951503a2879`.
- All project-plane resources were deleted.
- Result: hub companions gone, project WD gone — but `cm-pristine` and
  `secret-pristine` still on the cell. A `kubectl delete` of the cell copy
  caused Karmada to **re-create it within ~5 seconds** from the still-live Work
  object. The cell copy was in a permanent delete/recreate thrash.

### Why the Current Cell-Side GC Is Architecturally Wrong

The current implementation on `feat/configmap-secret-mounts-federated` (commits
f97a3cb + 9a064cc) added a `CompanionGCReconciler` running on each cell cluster
(`internal/controller/companion_gc_controller.go`). This controller deletes
orphaned companion copies directly from the cell cluster.

**This is the wrong layer for three reasons:**

1. **A cell companion copy is a Karmada-owned replica.** It is continuously
   enforced by a `Work` object sitting in namespace `karmada-es-{cluster}` on
   the Karmada hub. Karmada's execution controller watches the Work and
   immediately re-applies its manifest to the member cluster. Deleting the cell
   copy directly does not remove the Work; the Work wins. The delete/recreate
   loop observed in the lab is the proof — `cm-pristine` was being re-created
   within ~5 seconds of every cell-side delete because its Work was still live.

2. **You do not manage member-cluster objects directly in Karmada; you manage
   them through the federation control plane.** The correct deletion signal is to
   remove (or update) the Work object on the hub. The Work is what carries the
   deletion down to the cell. A cell-side delete cannot win against a live Work.

3. **The only scenario where a cell orphan persists is when Karmada's deletion
   cascade has already failed** — specifically when a `ResourceBinding` or `Work`
   is stranded with no live owner to trigger GC. That is exactly the case a
   cell-side delete cannot win either, because Karmada immediately re-creates the
   cell object from the stranded Work. The cell GC is ineffective against the one
   failure mode it targets, and pure churn otherwise.

**Operational consequence (observed in lab):** The OOM crash described in the
project memory (`project_unikraft_provider_worker_wedge.md`) was a direct symptom
of this architectural error — the `CompanionGCReconciler` used a cluster-wide
Secret/ConfigMap informer (`For(ConfigMap/Secret)`) which either wedged on RBAC
denial or OOM-crashed on the 128Mi limit. The 9a064cc "fix" switched to an
uncached APIReader + periodic backstop, which resolved the OOM but left the
underlying architecture wrong.

### Root Cause Mechanism

The correct mechanism to understand is **how Karmada's cascade deletion works**
for labeled-selector propagation:

```
delete hub companion (source)
  → Karmada detector marks ResourceBinding as needing reconcile
    → binding-controller updates RB to remove reference to deleted source
      → binding-controller deletes Work for that source on each cluster
        → execution controller deletes the cell copy
```

**Gap 1: Karmada's source→ResourceBinding cleanup is event-driven, not
steady-state reconciled.** When the hub source (companion) is deleted, Karmada's
detector fires an event that drives binding-controller reconcile. But this
mechanism has NO periodic re-validation: it does not scan existing ResourceBindings
to find ones whose hub source has vanished. If the deletion event is missed (e.g.
the binding-controller was restarted at exactly the wrong moment, or the PP was
deleted before the RB reconcile completed), the RB and its child Works remain
stranded permanently. Nothing ever picks them up again.

**Gap 2: Deleting a PropagationPolicy does NOT cascade-delete ResourceBindings
or Works.** Karmada deliberately treats PP deletion as "stop managing" rather than
"destroy everything." When `cleanupPropagationPolicyIfUnused` in
`workloaddeployment_federator.go` deletes the PropagationPolicy, any
ResourceBindings the PP created remain in place with their Works. They are now
orphaned: no PP owns them, but they still exist and Karmada's execution controller
continues enforcing them on the cells.

**The interacting race (confirmed by lab evidence):** The federator's `Finalize`
method (`workloaddeployment_federator.go:220`) deletes the downstream WD (which
may trigger the PP cleanup if this was the last WD for the city code) at roughly
the same time that the `referencedDataFinalizer` on the project WD triggers
`reconcileDeleted` → `releaseCompanions` → `releaseOneCompanion` →
`downstreamCompanionWriter.DeleteConfigMap/Secret` (hub companion deletion at
`referenceddata_controller.go:234–241`). If `cleanupPropagationPolicyIfUnused`
fires first (deleting the PP) before Karmada's binding-controller has reconciled
the now-absent companion source, the RB and Work are stranded. The cell copy
persists forever — or until a manual delete triggers the thrash observed in lab.

**The ordering guard already in the code:**
`workloaddeployment_federator.go:259–282` has `companionsStillPresent` /
`isLastDeploymentForCity` that delays PP deletion until hub companions are gone.
This reduces the race window. But it does NOT close it: if the referenced-data
controller's finalizer runs and deletes the hub companion, then the binding-
controller races to reconcile before Karmada registers the deletion event, the RB
can still strand. And it provides zero coverage for existing stranded RBs/Works
(the current lab orphans).

### Karmada Object Inventory (grounded in lab evidence and code)

For a companion named `cm-pristine` propagated by city code `dfw`:

| Object | Kind | API Group | Namespace | Name |
|--------|------|-----------|-----------|------|
| Hub companion | ConfigMap | core/v1 | `ns-efdf8ca1-...` | `cm-pristine` |
| PropagationPolicy | PropagationPolicy | policy.karmada.io | `ns-efdf8ca1-...` | `city-dfw` |
| ResourceBinding | ResourceBinding | work.karmada.io | `ns-efdf8ca1-...` | `cm-pristine-configmap` |
| Work | Work | work.karmada.io | `karmada-es-us-central-1-lab` | `cm-pristine-b65578bfc` |
| Cell copy | ConfigMap | core/v1 | `ns-efdf8ca1-...` | `cm-pristine` |

Labels on the ResourceBinding (set by Karmada's binding-controller):
- `propagationpolicy.karmada.io/name: city-dfw`
- `propagationpolicy.karmada.io/namespace: ns-efdf8ca1-...`

The ResourceBinding name pattern is `{objectName}-{kindLowercase}` (e.g.
`cm-pristine-configmap`, `secret-pristine-secret`). The Work name pattern is
`{objectName}-{shortHash}` (e.g. `cm-pristine-b65578bfc`). The Work lives in the
execution namespace `karmada-es-{clusterName}` on the Karmada hub.

The `PropagationPolicy` name is produced by `propagationPolicyNameFor(cityCode)`
(`workloaddeployment_federator.go:608–615`):
```go
func propagationPolicyNameFor(cityCode string) string {
    sanitized := strings.ToLower(strings.ReplaceAll(cityCode, " ", "-"))
    return fmt.Sprintf("city-%s", sanitized)
}
```

The label linking an RB to a companion is `propagationpolicy.karmada.io/name`
(value: `city-dfw`). The annotation linking a companion to its referrers is
`compute.datumapis.com/referenced-by` (value: JSON array of `"ns/wdName"` keys).

---

## Requirements

### Functional Requirements

- FR1: When all WorkloadDeployments referencing a companion ConfigMap/Secret are
  deleted, the companion MUST eventually be deleted from every cell cluster it was
  propagated to, and MUST stay deleted (no recreate loop).
- FR2: Hub-side deletion must drive cell-side deletion through Karmada's native
  cascade so that the Work is removed before the cell copy is deleted.
- FR3: Existing stranded ResourceBindings/Works (already in the lab) must be
  cleaned up by the new hub-side GC without requiring manual kubectl intervention.
- FR4: The cleanup must not require any controller running on cell clusters for
  GC purposes (cell controllers may still read companions for gate-clearing).
- FR5: The fix must be safe under rapid delete-then-recreate of the same-named
  source object.
- FR6: Multi-referrer correctness: a companion shared by two WDs targeting
  different city codes must only be fully deleted when the last referrer WD is gone.

### Non-Functional Requirements

- NFR1: No new persistent informers on cell clusters for cleanup purposes.
- NFR2: The cleanup mechanism must be observable (logs, conditions, or events).
- NFR3: Hub controller complexity should be bounded; no O(cells * companions)
  polling per reconcile cycle.

---

## Before/After Architecture

### Before (Current — Cell-Side GC)

```
[Project plane]                    [Karmada hub]              [Cell cluster]
delete WD ──► referencedDataFinalizer
                    │
                    ▼
              Delete hub companion ──► (cascade should fire but may be missed)
                                            │
              federatorFinalizer            ▼
                    │           binding-controller reconcile?
                    ▼                (event-driven, may miss)
              Delete downstream WD          │
                    │                       ▼ (if cascade fires)
              cleanupPropagationPolicy   Delete Work
              (may delete PP before RB   (if cascade fails: Work stranded)
               reconcile completes)               │
                                                  ▼
                                      [cell copy persists]
                                                  │
                                      CompanionGCReconciler (cell)
                                      deletes cell copy ──► Karmada re-creates
                                                            from live Work (thrash)
```

### After (Proposed — Hub-Side GC)

```
[Project plane]                    [Karmada hub]              [Cell cluster]
delete WD ──► referencedDataFinalizer
                    │
                    ▼
              Delete hub companion
                    │
                    ├──► Karmada cascade (event-driven, best-effort)
                    │         │
                    │         ▼
                    │    Delete ResourceBinding
                    │         │
                    │         ▼
                    │    Delete Work ──────────────────► Delete cell copy
                    │                                   (stays deleted: no Work)
                    │
                    └──► ReferencedDataController: also explicitly
                         deletes RB (if still present after hub-obj delete)
                                   │
                                   ▼ (deterministic, no race)
                              Delete Work ──────────────► Delete cell copy

[OrphanRBReconciler on hub] ◄── periodic/event-driven scan
   │  (backstop for stranded RBs whose companion is already gone)
   ▼
Delete stranded RBs ──► binding-controller deletes Works ──► Delete cell copies
   │
   ▼ (also cleans current lab orphans on first run)
```

---

## Corrected Design

### Component 1: ReferencedDataController — keep hub companion lifecycle (no change)

`internal/controller/referenceddata_controller.go` already correctly ref-counts
companions via the `compute.datumapis.com/referenced-by` annotation and deletes
the hub companion when the last referrer WD is gone:

- `reconcileDeleted` → `releaseCompanions` → `releaseOneCompanion` drops the WD
  key from the ref-count annotation (line 493–519).
- When ref-count reaches `[]`, `DeleteConfigMap`/`DeleteSecret` is called on the
  `downstreamCompanionWriter` which calls `hubClient.Delete(ctx, cm)` (line 234–241).

**This stays exactly as-is.** The hub ref-count model is correct.

### Component 2: Federator ordering guard — keep, reassess necessity

`workloaddeployment_federator.go:259–282` currently delays PP deletion (via
`cleanupPropagationPolicyIfUnused`) until hub companions are gone. This is the
`companionsStillPresent` / `isLastDeploymentForCity` / `companionGuardTimeout`
mechanism.

With the hub-side explicit RB teardown (Component 3) and the orphan-RB sweep
(Component 4), this guard becomes belt-and-suspenders rather than critical path:

- Component 3 makes companion deletion deterministically tear down its RB.
- Component 4 sweeps any RBs that outlive the PP deletion.

**Recommendation: keep the ordering guard unchanged.** It is low overhead, already
correct, and provides an additional window for Karmada's native cascade to fire
before the PP is gone. The `companionGuardTimeout = 2*time.Minute` prevents it
from blocking deletion indefinitely. Removing it would require Component 4 to be
the sole backstop; keeping it in reduces the number of orphans Component 4 needs
to clean up.

### Component 3: Explicit ResourceBinding teardown at companion-deletion time

**This is the primary deterministic fix.**

When the `ReferencedDataController` deletes a hub companion (hub object delete
confirmed), it must also explicitly delete the companion's ResourceBinding on the
hub. This short-circuits Karmada's event-driven cascade and ensures the Work is
deleted (via ResourceBinding owner reference) regardless of PP state.

**Mechanism:**

At the point where `downstreamCompanionWriter.DeleteConfigMap/DeleteSecret` is
called (`referenceddata_controller.go:234–241`), after the hub object is deleted,
the `ReferencedDataController` calls `hubClient.Delete` on the associated
ResourceBinding:

```
ResourceBinding name: {companionName}-{kind-lowercase}
Namespace: downstreamNamespace (same ns as the companion)

For cm-pristine (ConfigMap): rb name = "cm-pristine-configmap"
For secret-pristine (Secret): rb name = "secret-pristine-secret"
```

The RB naming convention (`{name}-{kind-lowercase}`) is the Karmada default for
namespace-scoped resources propagated by a `PropagationPolicy`. This is confirmed
by the lab evidence: RB `cm-pristine-configmap` in `ns-efdf8ca1-...`.

**Implementation location:** The `downstreamCompanionWriter.DeleteConfigMap` and
`DeleteSecret` methods should be extended (or a new step added in
`releaseOneCompanion`) to also delete the corresponding ResourceBinding. The
`companionWriter` interface would gain `DeleteResourceBinding(ctx, namespace, name)`.
Alternatively, a helper on `ReferencedDataController` itself can issue the RB
delete after the companion delete, using the same `hubClient`.

**RB delete semantics:** Use `client.IgnoreNotFound` — the RB may already be gone
if Karmada's cascade ran first. The delete is unconditional (not conditional on
the companion already being absent) because we are at the companion-deletion call
site.

**Work cascade:** When the ResourceBinding is deleted, Karmada's binding-controller
(which owns Works via RB-as-owner-reference on Work objects) will delete the Work.
The Work deletion drives the execution-controller to remove the cell copy. The
cell copy deletion is now **permanent** — there is no longer a Work to re-create it.

**RBAC requirement (Component 3):** The compute-manager ClusterRole in
`config/base/downstream-rbac/rbac.yaml` currently has:

```yaml
- apiGroups: ["work.karmada.io"]
  resources: ["resourcebindings", "clusterresourcebindings"]
  verbs: ["get", "list", "watch"]
```

This must be extended to include `delete`:

```yaml
- apiGroups: ["work.karmada.io"]
  resources: ["resourcebindings", "clusterresourcebindings"]
  verbs: ["get", "list", "watch", "delete"]
```

No change to Work verbs is needed — the RB owner-ref cascade handles Work deletion
automatically; we never directly delete Works.

### Component 4: Hub-side orphan-RB garbage collector

**This is the backstop sweep — the federation-side replacement for the cell GC.**

A new reconciler (`OrphanRBReconciler` or similar) on the management plane hub
watches `ResourceBinding` objects in hub namespaces and deletes any ResourceBinding
whose companion source no longer exists AND that was created for referenced-data
propagation.

**What it watches:** ResourceBindings with the label
`propagationpolicy.karmada.io/name` matching the pattern `city-*` in namespaces
that the compute-manager manages (i.e., namespaces with the
`downstreamclient.UpstreamOwnerNamespaceLabel` label). Alternatively, it watches
all ResourceBindings and filters by the presence of a companion object.

**How it identifies orphaned referenced-data RBs:**

An RB is a referenced-data orphan when ALL of the following are true:
1. Its name ends with `-configmap` or `-secret` (Karmada's convention for
   namespace-scoped ConfigMap/Secret RBs).
2. The hub companion it references (derived by stripping the kind suffix from the
   RB name: `cm-pristine-configmap` → companion `cm-pristine`) does NOT exist in
   the same namespace.
3. The companion's expected label `compute.datumapis.com/referenced-data=true`
   would have been present (inferrable from the PP label on the RB:
   `propagationpolicy.karmada.io/name` matches `city-*`).

**More robust identification:** Label the ResourceBinding at creation time.
Currently Karmada auto-labels RBs with the PP name/namespace but not with our
custom labels. The `ensurePropagationPolicy` PP spec does not control RB labels.
However, the combination of (a) RB name suffix `-configmap`/`-secret`, (b) the
PP label `propagationpolicy.karmada.io/name: city-*`, and (c) absence of the
hub companion object is sufficient to identify orphaned companion RBs without
false positives. WorkloadDeployment RBs have kind suffix `-workloaddeployment`,
which is distinct.

**How to avoid deleting RBs for live companions:** Before deleting, confirm the
hub companion is absent (GET the companion by name derived from RB name, in the
RB's namespace). If the companion exists (even with a deletionTimestamp), do not
delete the RB — Karmada's cascade is still in progress or Component 3 will handle it.

**Trigger:** The sweep runs on:
- ResourceBinding delete events (enqueue siblings in the same namespace), and
- A periodic tick (e.g., every 5 minutes) as backstop for RBs whose companion was
  deleted before the controller started.

**Effect on existing lab orphans:** On the first run after deployment, the sweep
will find `cm-pristine-configmap` and `secret-pristine-secret` in
`ns-efdf8ca1-...` (the lab stranded RBs). It will confirm the hub companions are
absent, delete the RBs, and Karmada's binding-controller will delete the Works in
`karmada-es-us-central-1-lab`. The execution controller will delete the cell
copies. **No manual kubectl is needed.**

**RBAC requirement (Component 4):** Same `delete` verb on `work.karmada.io`
`resourcebindings` as Component 3 — the same RBAC rule addition covers both.

### Component 5: Remove the cell-side CompanionGCReconciler entirely

The cell-side `CompanionGCReconciler` must be removed. It solves the wrong problem
at the wrong layer and has been demonstrated (by the OOM and delete/recreate loop)
to be harmful.

**Files to delete:**
- `internal/controller/companion_gc_controller.go` (entire file)
- `internal/controller/companion_gc_controller_test.go` (entire file)

**Wiring to remove in `cmd/main.go`:**
```go
// Lines 318-323 on feat/configmap-secret-mounts-federated — remove entire block:
if enableCellControllers {
    if err = (&controller.CompanionGCReconciler{}).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "CompanionGC")
        os.Exit(1)
    }
}
```

**RBAC reassessment after removal:**
The cell `compute-manager` RBAC was extended (by the now-removed commits) to grant
`get;list;delete` on ConfigMaps and Secrets specifically to support the
`CompanionGCReconciler`. After removal:

- ConfigMap/Secret `get;list;watch` on the cell: **keep** — the cell's
  `InstanceReconciler` reads companion ConfigMaps/Secrets to gate-clear instances.
- ConfigMap/Secret `delete` on the cell: **remove** — no remaining cell controller
  issues deletes on ConfigMaps or Secrets.

The RBAC is in `config/base/downstream-rbac/rbac.yaml`. The `delete` verb on
ConfigMap/Secret for the cell manager should be dropped.

**Note on the 9a064cc OOM fix:** The commit 9a064cc that switched from
`For(ConfigMap)` to APIReader to resolve the OOM is now moot — the entire
controller is being removed. The OOM root cause (cluster-wide ConfigMap informer
on a memory-constrained cell) is gone by deletion, not by workaround.

---

## Decision: Explicit RB Teardown vs. Orphan Sweep vs. Both

**Option A: Explicit RB teardown only (Component 3).**
Deterministic, fires synchronously at companion-deletion time. Requires the
`ReferencedDataController` to know the Karmada RB naming convention
(`{name}-{kind-lowercase}`). If the naming convention changes in a Karmada
upgrade, the teardown silently no-ops (IgnoreNotFound). Does not handle existing
lab orphans (RBs already stranded before this code ships).

**Option B: Orphan-RB sweep only (Component 4).**
Eventually consistent (minutes). Handles existing lab orphans. Does not require
knowing the RB naming convention at companion-deletion time (it discovers RBs by
label). But eventual: there is a window between companion hub-deletion and the
next sweep tick where the cell copy exists with a live Work ready to re-create it.
If the PP is deleted during this window, the RB is stranded until the next tick.

**Option C: Both (recommended).**

Both components are complementary and address different failure modes:
- Component 3 (explicit teardown) handles the common case deterministically and
  synchronously. A successful RB delete at companion-deletion time means there is
  zero window where the cell copy can be re-created.
- Component 4 (orphan sweep) handles existing stranded RBs, Karmada upgrade edge
  cases, and any scenario where Component 3's delete was skipped (e.g., the hub
  companion was deleted externally rather than through the controller).

**Recommendation: implement both.** The marginal cost of Component 4 (a simple
reconciler watching RBs with a periodic backstop) is low, and it closes the gap
against existing lab orphans and future regressions. Component 3 without Component 4
would require a one-time manual cleanup script for existing stranded objects.

---

## RBAC Changes (Summary)

**Hub compute-manager ClusterRole (`config/base/downstream-rbac/rbac.yaml`):**

Current:
```yaml
- apiGroups: ["work.karmada.io"]
  resources: ["resourcebindings", "clusterresourcebindings"]
  verbs: ["get", "list", "watch"]
```

Required (add `delete`):
```yaml
- apiGroups: ["work.karmada.io"]
  resources: ["resourcebindings", "clusterresourcebindings"]
  verbs: ["get", "list", "watch", "delete"]
```

No other changes to the hub RBAC. The `policy.karmada.io` `propagationpolicies`
rule already has `delete` (used by `cleanupPropagationPolicyIfUnused`). The
`cluster.karmada.io` rule does not need change.

**Cell compute-manager ClusterRole (same file, applied to cell via overlay):**

Remove `delete` from ConfigMap and Secret verbs. The `get;list;watch` is still
needed for gate-clearing (InstanceReconciler reads companions). The final cell rule:
```yaml
- apiGroups: [""]
  resources: ["configmaps", "secrets"]
  verbs: ["get", "list", "watch"]
```

No `work.karmada.io` verbs are needed on the cell — the cell does not interact
with Karmada APIs.

---

## Migration / Removal Plan

The following steps are ordered to prevent regressions during the transition:

1. **Remove `companion_gc_controller.go` and its test** from
   `internal/controller/`. Delete both files entirely.

2. **Remove the `enableCellControllers` wiring** for `CompanionGCReconciler` from
   `cmd/main.go` (the 5-line block at lines 318-323 on the current branch).

3. **Add explicit RB teardown (Component 3)** to the `ReferencedDataController`.
   Specifically, extend `releaseOneCompanion` (or `downstreamCompanionWriter`) to
   issue a `hubClient.Delete` on the RB after the companion object is deleted. The
   RB name is derived as `{companionName}-configmap` or `{companionName}-secret`.
   Wrap in `client.IgnoreNotFound`. The `companionWriter` interface may need a new
   `DeleteResourceBinding` method, or this can live directly in `releaseOneCompanion`
   conditional on the writer being a `downstreamCompanionWriter`.

4. **Implement `OrphanRBReconciler` (Component 4)** as a new file in
   `internal/controller/`. Register it under `enableManagementControllers` in
   `cmd/main.go`. It runs on the hub, watches ResourceBindings, and sweeps for
   orphaned companion RBs as described above. On first run after deployment it
   will automatically clean the existing stranded lab RBs.

5. **Update RBAC** in `config/base/downstream-rbac/rbac.yaml`: add `delete` to
   `work.karmada.io` resourcebindings; remove `delete` from the cell-side
   ConfigMap/Secret rule.

6. **Verify ordering guard** in `workloaddeployment_federator.go` (`companionsStillPresent`
   / `companionGuardTimeout`) — keep as-is. After Step 3 ships, the guard is belt-
   and-suspenders. It can be removed in a future cleanup but there is no urgency.

The ordering of steps 1-2 before steps 3-4 ensures the broken cell GC is removed
before the hub GC is in place. The window between these two steps is covered by
the existing ordering guard (Part 1 of the original design, which we keep).

---

## Task / Issue Tracking

This RFC absorbs and supersedes:
- **Task #35 (stranded-Work cleanup):** Component 4 (orphan-RB sweep) directly
  addresses this. The sweep finds stranded RBs (which own Works) and deletes them,
  cascading to Works and cell copies. No separate stranded-Work effort needed.
- **Cell-GC portion of Task #31:** The cell GC (`companion_gc_controller.go`) is
  the mechanism being removed. Task #31's requirements are fulfilled by Component 3
  (explicit RB teardown) and Component 4 (orphan-RB sweep) at the hub layer.

---

## Test Plan

### Unit Tests

**Component 3 — Explicit RB teardown (new tests in `referenceddata_controller_test.go`):**
- `releaseOneCompanion` deletes the hub companion AND issues a delete on the
  `{name}-configmap`/`{name}-secret` RB in the downstream namespace.
- RB delete uses `IgnoreNotFound`: if the RB is already gone (Karmada beat us),
  no error.
- `localCompanionWriter` path (single-cluster, no federation): RB teardown is
  NOT issued (no Karmada hub in single-cluster mode). Verify the code path is
  gated on the writer type or on `FederationClient != nil`.

**Component 4 — OrphanRBReconciler (new file `orphan_rb_controller_test.go`):**
- ResourceBinding named `cm-foo-configmap` where companion `cm-foo` does not exist
  in the namespace → RB is deleted.
- ResourceBinding named `cm-foo-configmap` where companion `cm-foo` exists
  (live, no deletionTimestamp) → RB is NOT deleted.
- ResourceBinding named `cm-foo-configmap` where companion `cm-foo` exists with
  `deletionTimestamp` set → RB is NOT deleted (companion deletion in progress;
  Karmada cascade may still fire).
- ResourceBinding named `wd-foo-workloaddeployment` (a WD RB, not a companion RB)
  → NOT deleted regardless of companion presence.
- ResourceBinding without the `propagationpolicy.karmada.io/name: city-*` label
  → NOT touched (not a referenced-data RB).
- Periodic sweep trigger: a companion that was deleted while the controller was
  not running is cleaned up on the next tick.

**Cell RBAC regression test:** Verify that after removal of `delete` from cell
ConfigMap/Secret RBAC, the `InstanceReconciler` still functions (reads companions
for gate-clearing). This is a review check, not a unit test, but should be called
out explicitly.

### Federated E2E Tests

- **Full lifecycle:** Create Workload with ConfigMap/Secret refs → verify companion
  propagated to cell → delete Workload → assert within N seconds:
  1. Hub companion is gone.
  2. ResourceBinding for companion is gone (Component 3 deleted it).
  3. Work for companion is gone (binding-controller cascaded).
  4. Cell copy is gone AND STAYS GONE (no recreate loop on poll over 30s).
- **Multi-cell:** Create Workload targeting two city codes → verify companions on
  both cells → delete Workload for one city → assert:
  - Companion deleted from that city's cell.
  - Companion PRESENT on the other city's cell (multi-referrer safety).
  - RB for the deleted city's companion: gone.
  - RB for the surviving city's companion: still present.
- **Delete-then-recreate:** Delete Workload → immediately recreate same name →
  verify companion is present on cell after recreate stabilizes (RB teardown did
  not permanently destroy the companion that the new WD needs; the
  `ReferencedDataController` re-creates the hub companion and its RB for the new WD).
- **Orphan sweep (lab scenario):** Manually strand a ResourceBinding (delete the hub
  companion, then immediately delete the PP before Karmada reconciles) → verify
  the orphan-RB sweep picks up the stranded RB within one sweep interval and
  cascades deletion to the cell.

### Edge-Lab Verification (mirrors the "pristine" lab scenario)

The existing lab orphans (`cm-pristine-configmap` and `secret-pristine-secret` as
ResourceBindings, `cm-pristine` and `secret-pristine` as Works in
`karmada-es-us-central-1-lab`) should be resolved automatically by Component 4's
first sweep after deployment:

1. Deploy the updated compute-manager with Component 4 (OrphanRBReconciler) active.
2. Observe logs for `"deleting orphaned companion ResourceBinding"` for the
   stranded lab RBs within one sweep interval (≤5 minutes).
3. Verify via `kubectl get resourcebinding -n ns-efdf8ca1-...` that
   `cm-pristine-configmap` and `secret-pristine-secret` are gone.
4. Verify via Karmada hub `kubectl get work -n karmada-es-us-central-1-lab` that
   the corresponding Works are gone.
5. Verify via cell cluster `kubectl get cm,secret -n ns-efdf8ca1-...` that
   `cm-pristine` and `secret-pristine` are gone AND remain gone for 60+ seconds
   (no Work to re-create them).

---

## Out of Scope

- Changing the companion ref-count model from global to per-city. The global model
  is correct for the hub; per-cell semantics are handled correctly by the existing
  ref-count logic (a companion is deleted hub-wide only when all referrers are gone,
  which is correct — each city's copy is destroyed by the Work cascade when the
  hub source is gone).
- Addressing the federator deletion-path gap (fire-and-forget Finalize,
  deterministic WD names). That is tracked separately in the project memory
  (`federator_deletion_path_gaps.md`).
- PropagationPolicy per-resource instead of label-based. This would be
  architecturally cleaner but is a larger change tracked separately.
- Image pull credential flows and other unrelated referenced-data extensions.

---

## Future Considerations

- **Hub-side confirmation wait:** A future enhancement could have the
  `ReferencedDataController` finalizer poll for Work absence (via
  `hubClient.List(works, ns=karmada-es-{cluster})` checking for the companion Work)
  before releasing the finalizer. This would give stronger completion guarantees
  at the cost of Karmada internal API coupling and finalization latency.
- **Remove the ordering guard:** Once Component 3 and Component 4 have been in
  production for a release, the `companionsStillPresent` / `isLastDeploymentForCity`
  ordering guard in `workloaddeployment_federator.go` can be removed as no longer
  needed. That simplification is deferred until the hub-side GC is proven stable.

---

## Handoff

### Decisions Made

- **Hub-side explicit RB teardown is the primary fix (Component 3)**, not the
  cell-side GC (which is being removed). Rationale: the Work is what enforces
  the cell copy; only deleting the RB (which owns the Work) produces a permanent
  deletion. Cell-side deletes cannot win against a live Work.
- **Both Component 3 and Component 4 are required.** Component 3 is deterministic
  but does not handle existing stranded RBs. Component 4 handles existing orphans
  and future regressions. Together they are defense-in-depth.
- **The ordering guard (companionsStillPresent) is kept unchanged.** It is belt-
  and-suspenders. Removing it before Components 3+4 are proven stable would
  reduce the safety margin.
- **`companion_gc_controller.go` and its test are deleted entirely** — not refactored
  or gated. The controller is wrong at the architectural level.
- **RBAC delta: add `delete` on `work.karmada.io/resourcebindings` for the hub;
  remove `delete` on `configmaps`/`secrets` for the cell.**
- **RB name convention:** `{companionName}-{kindLowercase}`. ConfigMap companion
  `cm-foo` → RB `cm-foo-configmap`. Secret companion `secret-foo` → RB
  `secret-foo-secret`. This matches the Karmada binding-controller default and is
  confirmed by the lab `cm-pristine-configmap` evidence.

### Open Questions

- **Non-blocking:** Should the OrphanRBReconciler be scoped to only namespaces the
  compute-manager manages (by the `UpstreamOwnerNamespaceLabel` label on the
  namespace) to avoid scanning unrelated Karmada ResourceBindings? Recommended yes,
  for efficiency, but not strictly required for correctness.
- **Non-blocking:** Should the RB teardown in Component 3 live in the
  `companionWriter` interface (a new `DeleteResourceBinding` method on
  `downstreamCompanionWriter`) or directly in `releaseOneCompanion` as a
  conditional on the `downstreamCompanionWriter` type? Either works; the
  interface approach is cleaner for testing.
- **Non-blocking:** What is the correct sweep interval for Component 4? 5 minutes
  matches the `companionGCPeriodicInterval` used in the removed cell GC. This is
  reasonable; the orphan sweep is a backstop, not a primary path.
- **Potentially blocking:** Does `github.com/karmada-io/api v1.15.0` provide a
  typed client-compatible `ResourceBinding` struct for `work.karmada.io/v1alpha2`?
  Verify the import path before implementing Component 3/4. The existing hub
  ClusterRole already references `work.karmada.io` resourcebindings (it has
  `get;list;watch`), confirming the API server recognizes this group, but the
  Go type registration in the scheme used by `FederationClient` needs to be
  confirmed.

### Implementation Notes for api-dev

- The `companionRefCountAnnotation` constant and `decodeRefCount` function in
  `referenceddata_controller.go` are the source of truth for the annotation format.
  The OrphanRBReconciler does NOT need to parse this annotation; it only needs the
  RB name and the hub companion existence check.
- The `propagationPolicyNameFor` function in `workloaddeployment_federator.go` is
  the source of truth for PP naming (`city-{cityCode}`). The OrphanRBReconciler
  filters RBs by the `propagationpolicy.karmada.io/name` label matching this
  pattern to scope its work to referenced-data RBs only.
- The `downstreamCompanionWriter.DeleteConfigMap` (line 234) and `DeleteSecret`
  (line 239) are the call sites where Component 3's RB teardown should be appended.
  The companion name is already known at this point (`name` parameter). The RB name
  is `name + "-configmap"` or `name + "-secret"`.
- The `localCompanionWriter` does NOT interact with Karmada. The RB teardown must
  only be issued when using the `downstreamCompanionWriter` (federation mode). Gate
  on `FederationClient != nil` or on the writer type.
- For the OrphanRBReconciler, the management-plane manager (`enableManagementControllers`)
  has `FederationClient` as its primary client. Register OrphanRBReconciler with
  the hub client, NOT the cell manager. The existing `WorkloadDeploymentFederator`
  registration pattern in `cmd/main.go` is the correct template.
