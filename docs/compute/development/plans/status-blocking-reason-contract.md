# Implementation Plan — Unified Status Blocking-Reason Contract

Companion to the RFC [`../rfcs/status-blocking-reason-contract.md`](../rfcs/status-blocking-reason-contract.md).
All architecture decisions in that RFC are locked. This document is the file-by-file
build sequence an api-dev agent can execute step by step.

---

## 0. Pre-conditions and Sequencing Dependencies

This implementation touches `workloaddeployment_controller.go`,
`instance_controller.go`, and `workload_controller.go`. The branch
`feat/configmap-secret-mounts-federated` (currently in flight, based off commit
`8cf0945`) edits all three controllers and their companion GC / federator
infrastructure. This plan MUST land on a commit that includes
`feat/configmap-secret-mounts-federated` fully merged — do not start implementing
until that branch is merged to `main`.

Files with potential merge contention:

| File | Why |
|------|-----|
| `internal/controller/workloaddeployment_controller.go` | `reconcileInstanceGates` return signature, `Available` condition assignment block |
| `internal/controller/instance_controller.go` | `reconcileReferencedDataCondition`, `reconcileInstanceReadyCondition` |
| `internal/controller/workloaddeployment_federator.go` | Federator reads WD status; Available condition change flows through it |
| `internal/controller/companion_gc_controller.go` | An agent is currently editing this file; no overlap with status logic, but rebase will touch nearby lines |
| `internal/controller/workloaddeployment_controller_test.go` | Test helpers for WD conditions |

Rebase strategy: after `feat/configmap-secret-mounts-federated` merges, create a
new branch `feat/status-blocking-reason-contract` from the updated `main`. Confirm
all existing tests pass before starting Phase 1.

---

## 1. RFC-vs-Code Drift Inventory

Perform this audit before writing any code. The following discrepancies between the
RFC's claims and the current codebase must be noted so the implementer is not
surprised.

### 1.1 Constants claimed by RFC §4.2 as "already present" — verified

All of the following exist in `api/v1alpha/instance_types.go` with the exact string
values the RFC states. No action needed.

| Constant | File:approx-line | Present |
|----------|-----------------|---------|
| `ReferencedDataReasonResolving` | instance_types.go:485 | yes |
| `ReferencedDataReasonAwaitingPropagation` | instance_types.go:489 | yes |
| `ReferencedDataReasonSourceNotFound` | instance_types.go:493 | yes |
| `ReferencedDataReasonSourceUnauthorized` | instance_types.go:497 | yes |
| `ReferencedDataReasonSourceTooLarge` | instance_types.go:501 | yes |
| `ReferencedDataReasonReady` | instance_types.go:505 | yes |
| `InstanceQuotaGrantedReasonQuotaExceeded` | instance_types.go:510 | yes |
| `InstanceQuotaGrantedReasonPendingEvaluation` | instance_types.go:508 | yes |
| `InstanceQuotaGrantedReasonNoBudget` | instance_types.go:544 | yes |
| `InstanceQuotaGrantedReasonBackendUnavailable` | instance_types.go:521 | yes |
| `InstanceQuotaGrantedReasonProjectNotFound` | instance_types.go:524 | yes |
| `InstanceReadyReasonSchedulingGatesPresent` | instance_types.go:549 | yes |
| `InstanceReadyReasonRunning` | instance_types.go:553 | yes |
| `InstanceProgrammedReasonPendingQuota` | instance_types.go:513 | yes |
| `WorkloadDeploymentAvailable` | workloaddeployment_types.go:65 | yes |
| `WorkloadAvailable` | workload_types.go:79 | yes |
| `ReferencedDataReady` (condition type) | instance_types.go:478 | yes |

### 1.2 Inline string literals in controllers — DRIFT from RFC §4.3

The RFC says these reasons are "already used inline" and the new constants replace
them. Verified current state:

| Controller | Location | Inline literal | RFC constant to add |
|------------|----------|----------------|---------------------|
| `workloaddeployment_controller.go:225` | `Available` condition | `"ProvisioningNetwork"` | `WorkloadDeploymentReasonNetworkProvisioning = "NetworkProvisioning"` |
| `workloaddeployment_controller.go:232` | `Available` condition | `"ProvisioningInstances"` | `WorkloadDeploymentReasonInstancesProvisioning = "InstancesProvisioning"` |
| `workloaddeployment_controller.go:218` | `Available` condition | `"StableInstanceFound"` | no constant in RFC; keep inline or add `WorkloadDeploymentReasonStableInstanceFound` (add it for consistency) |
| `workload_controller.go:127` | `Available` condition | `"NetworkNotFound"` | `WorkloadReasonNetworkNotFound = "NetworkNotFound"` |
| `workload_controller.go:247` | `Available` condition | `"NoAvailablePlacements"` | `WorkloadReasonNoAvailablePlacements = "NoAvailablePlacements"` |
| `workload_controller.go:298` | `Available` condition | `"AvailablePlacementFound"` | no constant in RFC; keep inline or add constant |
| `workload_controller.go:279` | placement condition | `"AvailableDeploymentFound"` | no constant in RFC; keep inline |
| `workload_controller.go:247` | placement condition | `"NoAvailableDeployments"` | `WorkloadReasonNoAvailableDeployments = "NoAvailableDeployments"` |
| `instance_controller.go:85` | local const | `"NetworkFailedToCreate"` | RFC lists this as "already implemented"; the local `reasonNetworkFailedToCreate` constant already exists at line 85 — no change needed |

**Important:** RFC §4.3 proposes `WorkloadDeploymentReasonNetworkProvisioning =
"NetworkProvisioning"` as the replacement for the existing inline `"ProvisioningNetwork"`.
These are DIFFERENT strings. The RFC is changing the surfaced value, not just
introducing a constant for the same string. The implementer must verify whether
changing this string is intentional (it changes what clients currently matching
`"ProvisioningNetwork"` observe). If backward-compat is required, keep the string as
`"ProvisioningNetwork"` and just name the constant accordingly. Flag this for review
before committing Phase 1.

Similarly, `"ProvisioningInstances"` → `"InstancesProvisioning"` is a string value
change, not just a rename. Same flag applies.

### 1.3 Missing constant: `WorkloadDeploymentReasonReferencedDataNotReady`

The RFC adds `WorkloadDeploymentReasonReferencedDataNotReady = "ReferencedDataNotReady"`.
This is a genuinely new reason string, not a renamed existing one. Confirmed absent
from the codebase.

### 1.4 Missing constant: `WorkloadDeploymentReasonQuotaNotGranted`

New. Confirmed absent. Maps to string `"QuotaNotGranted"`.

### 1.5 `ObservedGeneration` on WD and Workload Available conditions — DRIFT

The RFC (§6) requires `ObservedGeneration` be set on the `Available` conditions for
`WorkloadDeployment` and `Workload`. Neither controller currently sets
`ObservedGeneration` on any condition (confirmed by grep: zero matches for
`ObservedGeneration` in both `workloaddeployment_controller.go` and
`workload_controller.go`). This is additive and non-breaking.

### 1.6 `workload_controller_test.go` does not exist

The RFC §13 notes "Does not exist yet at time of writing; create for Step 4". This
is confirmed — no `workload_controller_test.go` exists. A new test file must be
created in Phase 4.

### 1.7 Network-ready short-circuit / priority inconsistency

The RFC's §8.1 test section acknowledges: "network-ready check short-circuits FIRST
in the existing control flow, so network-not-ready can win over a higher-priority
referenced-data error, contradicting the priority table." This is the main open
design inconsistency and is resolved by this plan in Phase 5 (see §5 below).

### 1.8 RFC claims `InstanceQuotaGrantedReasonNoBudget` and `InstanceQuotaGrantedReasonBackendUnavailable` exist — verified correct

Both are present in `api/v1alpha/instance_types.go`. No action.

---

## 2. Phase Breakdown

### Phase 0 — Rebase gate (no code changes)

**Goal:** confirm the working branch is clean on top of the merged
`feat/configmap-secret-mounts-federated` work.

Steps:
1. Confirm `feat/configmap-secret-mounts-federated` is merged to `main`.
2. Create `feat/status-blocking-reason-contract` from updated `main`.
3. Run `make test` and confirm all existing tests pass.
4. Commit: none (this is a checkpoint, not a commit).

Done when: `make test` is green on the new branch.

---

### Phase 1 — API constants (`api/v1alpha/instance_types.go`)

**Goal:** add the new reason constants and replace inline literals with constants.
Zero behavior change — this is a pure refactor of string values.

**Files touched:**
- `api/v1alpha/instance_types.go`
- `internal/controller/workloaddeployment_controller.go` (inline → constant substitutions)
- `internal/controller/workload_controller.go` (inline → constant substitutions)
- `internal/controller/instance_controller.go` (one inline at line 85 is already a local const; no change needed)

**Steps:**

1. In `api/v1alpha/instance_types.go`, add a new `const` block after the existing
   reason blocks with the following constants (see RFC §4.3 for the exact names and
   RFC-vs-code notes in §1.2 for the string value caveats):

   ```
   WorkloadReasonNetworkNotFound
   WorkloadDeploymentReasonNetworkProvisioning
   WorkloadDeploymentReasonInstancesProvisioning
   WorkloadDeploymentReasonReferencedDataNotReady
   WorkloadDeploymentReasonQuotaNotGranted
   WorkloadReasonNoAvailablePlacements
   WorkloadReasonNoAvailableDeployments
   ```

   Before committing, resolve the string-value drift noted in §1.2: decide whether
   `"ProvisioningNetwork"` → `"NetworkProvisioning"` is intentional. If it is,
   document the breaking change in the PR description. If it is not, keep the string
   as `"ProvisioningNetwork"` and name the constant `WorkloadDeploymentReasonNetworkProvisioning`
   with value `"ProvisioningNetwork"`.

2. In `workload_controller.go`, replace:
   - inline `"NetworkNotFound"` at line ~127 → `computev1alpha.WorkloadReasonNetworkNotFound`
   - inline `"NoAvailablePlacements"` at line ~292 → `computev1alpha.WorkloadReasonNoAvailablePlacements`
   - inline `"NoAvailableDeployments"` at line ~247 → `computev1alpha.WorkloadReasonNoAvailableDeployments`

3. In `workloaddeployment_controller.go`, replace:
   - inline `"ProvisioningNetwork"` at line ~225 → `computev1alpha.WorkloadDeploymentReasonNetworkProvisioning`
   - inline `"ProvisioningInstances"` at line ~232 → `computev1alpha.WorkloadDeploymentReasonInstancesProvisioning`

   Note: `"StableInstanceFound"` at line ~218 has no RFC constant. Either leave it
   inline or add `WorkloadDeploymentReasonStableInstanceFound = "StableInstanceFound"` — the
   plan recommends adding it for consistency, but it is not required by the RFC.

4. Confirm no behavior change: `make test` must pass unchanged.

**Done when:** all inline reason strings that match RFC §4.3 are replaced with named
constants, `make test` passes, no existing test assertions on reason strings break
(if they do, update the test assertions to use the new constant, verifying the string
value is the same).

**PR title:** `api: add status blocking-reason constants; replace inline literals (no behavior change)`

---

### Phase 2 — Instance.Ready rollup enrichment

**Goal:** `reconcileInstanceReadyCondition` in `instance_controller.go` surfaces the
most specific blocking reason from sub-conditions rather than always collapsing to
`SchedulingGatesPresent`.

**Files touched:**
- `internal/controller/instance_controller.go`
- `internal/controller/instance_controller_test.go`

**Prerequisite:** Phase 1 merged.

**Key functions to modify:**

**A. `reconcileReferencedDataCondition` (line ~331)**

Add a new early-return path after the WD annotation fetch, specifically in the
"gate present" branch (after line ~404 where `wd` is fetched). When the WD's
`ReferencedDataReady` condition has `reason` equal to `ReferencedDataReasonSourceNotFound`,
`ReferencedDataReasonSourceUnauthorized`, or `ReferencedDataReasonSourceTooLarge`,
override the instance's `ReferencedDataReady` condition with the WD's reason and
message verbatim, rather than deriving a cell-side message. This must happen before
the companion diff computation, since for terminal source errors there will never be
companions to arrive.

Concretely: after fetching `wd` at line ~405, read
`apimeta.FindStatusCondition(wd.Status.Conditions, computev1alpha.ReferencedDataReady)`.
If the WD condition is non-nil, `Status == False`, and reason is one of the three
terminal reasons, call `r.setReferencedDataConditionWithTransition(instance,
metav1.ConditionFalse, wdCond.Reason, wdCond.Message)` and return without
proceeding to the companion diff. The logic already fetches the WD annotation on
this path; the condition read is one additional field access on the already-fetched
struct — zero extra API calls.

**B. `reconcileInstanceReadyCondition` (line ~1155)**

The current implementation has this control flow in the "scheduling gates present"
branch:
1. Check `networkFailureChecker` → if failed, set `NetworkFailedToCreate`.
2. Else fall through to `SchedulingGatesPresent`.

The quota check for `QuotaGranted=False` already returns early at the very top of
the function (lines ~1163–1188) before gates are even inspected.

The new control flow in the gates-present branch must evaluate ALL known
sub-conditions and apply `blockingReasonPriority` to select the winner, rather than
short-circuiting on the first match. The concrete steps:

1. Collect candidate `(status, reason, message)` tuples from sub-conditions:
   - From `ReferencedDataReady`: if non-True, record its status/reason/message.
   - From the network failure checker: if failed, record `False/NetworkFailedToCreate/<msg>`.
2. Among all collected candidates plus the `SchedulingGatesPresent` fallback, pick
   the one with the highest `blockingReasonPriority(reason)`.
3. Set `Instance.Ready` to the winning tuple.

The `blockingReasonPriority` function is defined within `instance_controller.go`
(not exported, not shared — see RFC §5.4). Implement it as a simple `switch` on
`reason` returning the integer from the RFC §5.4 priority table. Place it
immediately after `reconcileInstanceReadyCondition`.

Note: the existing `QuotaGranted=False` early return (lines ~1163–1188) sets Ready
AND Programmed AND Running simultaneously. This behavior is correct and must remain
untouched. The priority logic only applies within the scheduling-gates branch. Quota
already wins (because it returns before gates are checked), which is consistent with
it having priority 3 in the table — but quota fires on a separate code path, not
through `blockingReasonPriority`. This is acceptable: the RFC §5.4 priority table
describes user-visible outcomes, not the implementation path.

**Tests to add in `instance_controller_test.go`:**

The existing `TestReconcileInstanceReadyCondition` (line ~59) tests the
`SchedulingGatesPresent` path with a `Network` gate. Extend this test file with:

1. `TestReconcileInstanceReadyCondition_ReferencedDataSourceNotFound`:
   - Instance has `ReferencedData` gate in spec.
   - WD returned by the fake fetcher has `ReferencedDataReady=False/SourceNotFound`
     with message `ConfigMap "X" not found in namespace "default"`.
   - Assert `Ready=False`, `reason=SourceNotFound`,
     `message=ConfigMap "X" not found in namespace "default"`.

2. `TestReconcileInstanceReadyCondition_ReferencedDataSourceTooLarge`:
   - Same setup, WD condition reason `SourceTooLarge`.
   - Assert reason propagates verbatim.

3. `TestReconcileInstanceReadyCondition_ReferencedDataAwaitingPropagation`:
   - WD condition reason `AwaitingPropagation` (propagation lag, not terminal error).
   - Instance has companion diff: 1 of 2 companions missing.
   - Assert `Ready=False/AwaitingPropagation` with the cell-side message listing the
     missing companion token (NOT the WD message).

4. `TestReconcileInstanceReadyCondition_QuotaWinsOverSchedulingGates`:
   - `QuotaGranted=False/QuotaExceeded` AND `ReferencedData` gate present with WD
     `SourceNotFound`.
   - Because quota returns early before gate inspection, assert
     `Ready=False/PendingQuota`. This confirms the existing behavior is preserved.

5. `TestReconcileInstanceReadyCondition_ReferencedDataWinsOverNetwork`:
   - `ReferencedData` gate present, WD `SourceNotFound`.
   - Network failure checker returns `failed=true`.
   - Assert `Ready=False/SourceNotFound` (priority 5 > priority 7 for
     `NetworkFailedToCreate`... wait — RFC table has NetworkFailedToCreate at
     priority 7, which is higher. Re-read RFC §5.4: `SourceNotFound` = 5,
     `NetworkFailedToCreate` = 7. So network failure wins).
   - Correct assertion: `Ready=False/NetworkFailedToCreate`.
   - This test confirms the priority function is applied correctly, not that
     ReferencedData wins — the RFC table is the truth.

6. `TestBlockingReasonPriority`:
   - Unit-test the `blockingReasonPriority` switch exhaustively: every listed reason
     returns its exact integer, an unknown string returns 0.

**Done when:** all new tests pass, existing `TestReconcileInstanceReadyCondition`
and `TestReconcileInstanceReadyConditionWithQuota` still pass.

**PR title:** `controller: enrich Instance.Ready with specific blocking reason from sub-conditions`

---

### Phase 3 — WorkloadDeployment.Available rollup enrichment

**Goal:** the `Available` condition on `WorkloadDeployment` carries the most specific
blocking reason when `readyReplicas == 0`, sourced from the WD's own
`ReferencedDataReady` sub-condition and from `reconcileInstanceGates` counts.

**Files touched:**
- `internal/controller/workloaddeployment_controller.go`
- `internal/controller/workloaddeployment_controller_test.go`

**Prerequisite:** Phase 1 merged. Phase 2 not required (independent).

**Key function to modify: `Reconcile` — the Available condition assignment block**

Current code (lines ~214–235) is a three-branch if/else that checks
`readyReplicas > 0`, then `!networkReady`, then `replicas > 0`. Only the network
check is currently in the `Available` condition; quota and referenced-data go only
into `ReplicasReady`.

Replace this block with logic that:

1. When `readyReplicas > 0`: set `Available=True/StableInstanceFound` (unchanged).
2. When `readyReplicas == 0`: collect candidate blocking reasons and apply
   `blockingReasonPriority` to select the winner:
   - `!networkReady` → candidate `(False, NetworkProvisioning, "Network is being provisioned")`
   - `wdRefDataCond != nil && wdRefDataCond.Status == False` → candidate
     `(False, ReferencedDataNotReady, wdRefDataCond.Message)` where `wdRefDataCond`
     is `apimeta.FindStatusCondition(deployment.Status.Conditions, computev1alpha.ReferencedDataReady)`
     read before the Available block executes. The WD's own `ReferencedDataReady`
     condition is set by `ReferencedDataController` in a prior reconcile pass; it is
     already present on `deployment.Status.Conditions` by the time this code runs.
   - `quotaBlockedReplicas > 0` → candidate `(False, QuotaNotGranted, fmt.Sprintf("%d of %d desired instances pending quota", quotaBlockedReplicas, desiredReplicas))`
   - `referencedDataBlockedReplicas > 0` with WD `ReferencedDataReady == True` or
     absent (propagation lag only) → candidate `(False, ReferencedDataNotReady, fmt.Sprintf("%d of %d desired instances waiting for companion propagation", referencedDataBlockedReplicas, desiredReplicas))`
   - `replicas > 0` (fallback: instances exist but none ready) → candidate
     `(False, InstancesProvisioning, "Instances are being provisioned")`
   - Last-resort default: `(False, InstancesProvisioning, "No instances available yet")` when replicas == 0.
3. Apply `blockingReasonPriority` to all collected candidates; set `Available` to
   the highest-priority tuple.
4. Set `ObservedGeneration: deployment.Generation` on the `Available` condition.

Define `blockingReasonPriority` as a private function in
`workloaddeployment_controller.go` (same switch logic as in `instance_controller.go`,
independently defined per RFC §5.4).

**Tests to add in `workloaddeployment_controller_test.go`:**

The existing tests (`TestReconcileInstanceGates_NilController_DoesNotPanic` etc.)
test `reconcileInstanceGates` in isolation. Add a new test group for the `Available`
condition output:

1. `TestWDAvailableCondition_ReferencedDataSourceNotFound`:
   - `readyReplicas=0`, WD has `ReferencedDataReady=False/SourceNotFound` on status,
     `referencedDataBlockedReplicas=1`.
   - Assert `Available=False`, `reason=ReferencedDataNotReady`, message equals WD's
     `ReferencedDataReady.Message` verbatim.
   - Assert `ObservedGeneration` equals deployment generation.

2. `TestWDAvailableCondition_QuotaNotGranted`:
   - `readyReplicas=0`, `quotaBlockedReplicas=2`, `ReferencedDataReady=True`.
   - Assert `Available=False/QuotaNotGranted`, message contains "2 of N".

3. `TestWDAvailableCondition_ReferencedDataWinsOverQuota`:
   - `readyReplicas=0`, `quotaBlockedReplicas=1`, WD `ReferencedDataReady=False/SourceNotFound`.
   - Assert `Available=False/ReferencedDataNotReady` (priority 4 > priority 3).

4. `TestWDAvailableCondition_NetworkProvisioningVsReferencedData`:
   - `readyReplicas=0`, `!networkReady`, WD `ReferencedDataReady=False/SourceNotFound`.
   - RFC §5.4 table: `SourceNotFound` = 5, `NetworkProvisioning` = 2. ReferencedData wins.
   - Assert `Available=False/ReferencedDataNotReady`.
   - This test is the explicit verification of the control-flow-order fix (Phase 5 below
     ensures the evaluate-all-then-pick logic makes this possible).

5. `TestBlockingReasonPriority_WD`:
   - Exhaustive unit test of `blockingReasonPriority` in workloaddeployment_controller.go.

**Done when:** all new tests pass, existing WD controller tests still pass.

**PR title:** `controller: enrich WorkloadDeployment.Available with specific blocking reason`

---

### Phase 4 — Workload.Available rollup enrichment

**Goal:** `reconcileWorkloadStatus` in `workload_controller.go` propagates the
worst-deployment blocking reason+message to `Workload.Available` instead of
collapsing to a boolean.

**Files touched:**
- `internal/controller/workload_controller.go`
- `internal/controller/workload_controller_test.go` (CREATE NEW FILE)

**Prerequisite:** Phase 3 merged (the WD `Available` condition now carries useful
reason+message that this phase reads).

**Key function to modify: `reconcileWorkloadStatus` (line ~212)**

Current: iterates `placementDeployments`, checks `IsStatusConditionTrue(deployment,
"Available")`, sets Workload `Available` to `True/AvailablePlacementFound` or
`False/NoAvailablePlacements`. The deployment's reason/message is never read.

Required change:

1. Keep the `availablePlacementFound` boolean logic unchanged — if any placement is
   available, `Available=True` fires immediately.

2. In the `!availablePlacementFound` path, collect all non-available deployments'
   `Available` conditions and apply `blockingReasonPriority` to find the worst one.
   Use lexicographic ordering on deployment name as a deterministic tie-break when
   two deployments have equal priority (resolves RFC §12 open question #3 —
   confirmed acceptable by the RFC).

3. Set `Workload.Available` to:
   - `Status: False`, `Reason: worstReason`, `Message: worstMessage`
   - `ObservedGeneration: workload.Generation`

4. Default `worstReason = computev1alpha.WorkloadReasonNoAvailablePlacements` and
   `worstMessage = "No available placements were found for the workload"` — this is
   the RFC §5.3 last-resort default, used when no deployment has a non-nil
   `Available` condition.

5. Define `blockingReasonPriority` as a private function in `workload_controller.go`
   (same switch, independently defined).

Note: `workloadConditionTypeAvailable` is already a local constant (`= "Available"`)
in `workload_controller.go` at line ~38. Use it; do not introduce a duplicate.

Similarly, the `NetworkNotFound` early return at line ~122 already sets
`Workload.Available=False/NetworkNotFound` and returns immediately — this short
circuit fires before `reconcileWorkloadStatus` is called, so it is already "winning"
at the Workload layer for network errors. No change needed to that path.

**Create `internal/controller/workload_controller_test.go`:**

This file does not exist. Create it with package `controller` (matching the other
test files in the package). Test groups:

1. `TestReconcileWorkloadStatus_AllDeploymentsSameReason`:
   - Two deployments, both `Available=False/ReferencedDataNotReady`, same message.
   - Assert Workload `Available=False/ReferencedDataNotReady` with that message.
   - Assert `ObservedGeneration` equals workload generation.

2. `TestReconcileWorkloadStatus_MixedReasons`:
   - Deployment A: `Available=False/InstancesProvisioning` (priority 1).
   - Deployment B: `Available=False/QuotaNotGranted` (priority 3).
   - Assert Workload picks `QuotaNotGranted` (higher priority).

3. `TestReconcileWorkloadStatus_OneAvailableDeployment`:
   - Deployment A: `Available=False/ReferencedDataNotReady`.
   - Deployment B: `Available=True`.
   - Assert Workload `Available=True/AvailablePlacementFound`.

4. `TestReconcileWorkloadStatus_NoDeployments`:
   - `placementDeployments` is empty.
   - Assert Workload `Available=False/NoAvailablePlacements`.

5. `TestReconcileWorkloadStatus_TiebreakerByName`:
   - Two deployments with equal priority `ReferencedDataNotReady`, different messages.
   - Assert the deployment with the lexicographically earlier name wins.

6. `TestBlockingReasonPriority_Workload`:
   - Exhaustive unit test of `blockingReasonPriority` in workload_controller.go.

**Done when:** new test file passes, existing workload controller integration tests
(if any in `suite_test.go`) still pass.

**PR title:** `controller: propagate worst-deployment blocking reason to Workload.Available`

---

### Phase 5 — Control-flow / priority consistency fix

This phase resolves the open inconsistency the RFC acknowledges in §8.1: the
existing control flow in `reconcileInstanceReadyCondition` and the WD `Available`
block both short-circuit on the first gate match rather than evaluating all causes
and applying the priority function.

In practice Phase 5 is not a separate commit — it is embedded in Phases 2 and 3.
This section makes the requirement explicit so it is not overlooked during those
phases.

**Required approach:** in both `reconcileInstanceReadyCondition` (Phase 2) and the
WD `Available` assignment block (Phase 3), the implementation MUST:

1. Evaluate all blocking sub-conditions for the current reconcile object.
2. Collect all non-null candidates as `(status, reason, message, priority)` tuples.
3. Apply `blockingReasonPriority` to select the highest-priority tuple.
4. Set the condition to the winning tuple.

Do NOT short-circuit by returning early when the first blocking condition is found.
The `networkFailureChecker` call in `reconcileInstanceReadyCondition` is a side-
effectful function that may trigger reads — call it once, capture the result as a
local bool, and include it in the candidate set alongside sub-condition reads. Do
not gate the network check on the absence of a referenced-data error.

The only permitted early return in `reconcileInstanceReadyCondition` remains the
`QuotaGranted=False` path at the top of the function (lines ~1163–1188) because
quota blocks all programmatic activity and sets three conditions simultaneously —
this is a deliberate design of the existing code that the RFC does not alter.

**Verification:** Phase 2 test `TestReconcileInstanceReadyCondition_ReferencedDataWinsOverNetwork`
and Phase 3 test `TestWDAvailableCondition_NetworkProvisioningVsReferencedData`
are the acceptance tests for this correctness requirement. Both tests specifically
create a scenario where the old short-circuit would have produced the wrong winner.

---

### Phase 6 — observedGeneration plumbing

**Goal:** ensure `ObservedGeneration` is set on `Available` conditions for
`WorkloadDeployment` and `Workload`. `Instance` conditions already set it.

This is embedded in Phases 3 and 4 respectively — each phase sets
`ObservedGeneration: deployment.Generation` / `workload.Generation` when writing
the `Available` condition. No separate phase is needed; this section documents it
as an explicit requirement so it is not accidentally omitted.

Verification: add an assertion to at least one test per phase confirming
`ObservedGeneration` equals the resource's generation.

---

### Phase 7 — Tests: federated e2e and edge-lab acceptance criteria

**Goal:** add the end-to-end test that proves all three layers flip correctly on
missing-then-created ConfigMap.

**Files touched:**
- The existing federated e2e test suite (locate with `find . -name '*e2e*' -o -name '*federated*test*'`).
  Based on the existing plans in `docs/compute/development/plans/configmap-secret-mounts-e2e-multicluster.md`,
  the federated e2e lives under a path like `test/e2e/` or `test/integration/`.

**Steps:**

1. Add test scenario `TestStatusBlockingReasonContract_MissingConfigMap` (or equivalent naming
   that matches the existing test suite convention):

   a. Create a Workload whose template references a ConfigMap (e.g. `missing-cfg`)
      that does not exist.
   b. Wait (up to 30 s) for the `WorkloadDeployment` to reach
      `ReferencedDataReady=False/SourceNotFound`.
   c. Assert within 30 s:
      - `WorkloadDeployment.Available == False`, `reason == "ReferencedDataNotReady"`,
        message contains `ConfigMap "missing-cfg"`.
      - `Workload.Available == False`, same reason and message.
      - Each `Instance.Ready == False`, `reason == "SourceNotFound"`,
        message contains `ConfigMap "missing-cfg" not found`.
   d. Create the ConfigMap `missing-cfg`.
   e. Assert within 60 s:
      - `WorkloadDeployment.Available == True`.
      - `Workload.Available == True`.
      - All Instances `Ready == True`.

   The `enqueueWDsForSource` watch (already wired in `referenceddata_controller.go`)
   re-triggers the resolver when the source ConfigMap is created, propagates the
   companion, and the cell instance gate clears. The e2e test proves this chain
   end-to-end.

2. This test is the primary acceptance criterion for the RFC. Mark it as such in the
   test's comment.

**Edge-lab verification (manual, performed after e2e passes):**

Run the procedure from RFC §8.3 verbatim on a dev/lab environment. The exact `jq`
commands from the RFC are reproduced here for copy-paste convenience:

```bash
# Step 1: deploy workload referencing a non-existent ConfigMap
datumctl compute deploy --file=workload-missing-config.yaml

# Step 2: check Instance — expect reason=SourceNotFound
kubectl get instances -o json | jq '.items[0].status.conditions[] | select(.type=="Ready")'

# Step 3: check WorkloadDeployment — expect Available=False/ReferencedDataNotReady
kubectl get workloaddeployments -o json | jq '.items[0].status.conditions[] | select(.type=="Available")'

# Step 4: check Workload — expect Available=False/ReferencedDataNotReady
kubectl get workloads -o json | jq '.items[0].status.conditions[] | select(.type=="Available")'

# Step 5: create the ConfigMap
kubectl create configmap missing-cfg --from-literal=key=val

# Step 6: re-check all three layers — all healthy within 60 s
```

The edge-lab run is a gate before the PR for Phase 7 (or whichever PR bundles the
e2e test) is marked "ready for review".

**Done when:** e2e test passes in CI and edge-lab run produces the expected output.

---

### Phase 8 — Handoff note for CLI plugin (task #32)

The datumctl compute plugin in a separate repository currently contains special-case
code (RFC §9.2, before-snippet) that fetches the WorkloadDeployment to surface
`ReferencedDataReady` when `reason == "SchedulingGatesPresent"`. After this RFC
lands, that code can be removed.

This plan does NOT cover the plugin internals. The handoff note for the plugin team
(task #32) is:

- After Phase 2 is merged and deployed, `Instance.Ready` will carry `reason=SourceNotFound`
  directly. The plugin's fallback fetch is no longer needed for error surfacing.
- The plugin should be updated to read `Instance.Ready.reason` and `Instance.Ready.message`
  directly, removing the `if reason == "SchedulingGatesPresent"` branch.
- The `WorkloadDeployment` fetch for city-code and location must be retained; only the
  error-surfacing path is removed.
- No plugin changes are needed before Phase 2 lands — the plugin's existing fallback
  continues to work correctly while the rollout is in progress (additive change, no regression).

---

## 3. Full File/Symbol Inventory

| File | Symbols modified | Phase |
|------|-----------------|-------|
| `api/v1alpha/instance_types.go` | new `const` block: 7 new constants | 1 |
| `internal/controller/workload_controller.go` | `reconcileWorkloadStatus`, 3 inline→constant substitutions, new `blockingReasonPriority` | 1, 4 |
| `internal/controller/workloaddeployment_controller.go` | Available condition assignment block in `Reconcile`, 2 inline→constant substitutions, new `blockingReasonPriority` | 1, 3 |
| `internal/controller/instance_controller.go` | `reconcileReferencedDataCondition` (WD terminal-reason override), `reconcileInstanceReadyCondition` (evaluate-all + priority), new `blockingReasonPriority` | 2 |
| `internal/controller/instance_controller_test.go` | 6 new test functions | 2 |
| `internal/controller/workloaddeployment_controller_test.go` | 5 new test functions | 3 |
| `internal/controller/workload_controller_test.go` | CREATE NEW: 6 test functions | 4 |
| `test/e2e/` (or equivalent) | New e2e test scenario for missing→created ConfigMap | 7 |

**Read-only references (do not modify):**
- `internal/controller/referenceddata_controller.go` — source of truth for resolver errors; no change needed
- `internal/controller/instancecontrol/scheduling_gates.go` — gate name constants; no change needed
- `api/v1alpha/workloaddeployment_types.go` — condition type constants; no change needed
- `api/v1alpha/workload_types.go` — `WorkloadAvailable` constant; no change needed

---

## 4. Backward Compatibility and Rollout

### 4.1 CRD/schema impact

None. No new fields are added to any `*Status` struct. The `conditions` slice is
already `[]metav1.Condition` and accepts any reason string. No CRD regeneration is
required. Confirm this by running `make generate manifests` after Phase 1 and
verifying the diff is empty.

### 4.2 Existing conditions preserved

`ReferencedDataReady`, `QuotaGranted`, `ReplicasReady`, `Programmed`, `Running` all
remain as-is on their respective resources. No condition is removed.

### 4.3 Enriched conditions — client impact

- `WorkloadDeployment.Available`: clients reading only `status == True/False` are
  unaffected. Clients matching `reason == "ProvisioningNetwork"` will see a new value
  if the string rename (§1.2 drift note) is accepted. Audit this before Phase 1 merges.
- `Workload.Available`: `NoAvailablePlacements` remains the fallback; no existing
  client assertion breaks unless they matched the absence of `reason`.
- `Instance.Ready`: `SchedulingGatesPresent` still appears for gates with no
  sub-condition. Clients matching this reason continue to work; they just see it
  less often.

### 4.4 Feature flag

No new feature flag is required. The enriched rollup is purely additive to the
condition content and the feature (referenced-data gate) is already guarded by the
existing `EnableReferencedDataGate` flag in `WorkloadDeploymentReconciler`. When
`EnableReferencedDataGate` is false, the `ReferencedData` scheduling gate is never
added, so the `SourceNotFound` path in Instance.Ready is never reached.

### 4.5 Rollout order

Deploy the management-plane binary (which runs `WorkloadReconciler` and
`ReferencedDataController`) first; then deploy the cell-plane binary (which runs
`InstanceReconciler` and `WorkloadDeploymentReconciler`). The enrichment is additive
at each layer — a partially updated system produces the old behavior (SchedulingGatesPresent)
until both planes are updated.

---

## 5. PR / Commit Structure

| PR | Phases | Review checkpoint |
|----|--------|-------------------|
| PR 1 | Phase 1 (API constants + inline replacements) | Verify string value decisions; approve before any behavior PRs |
| PR 2 | Phase 2 (Instance.Ready enrichment + unit tests) | Review priority logic and WD-reason passthrough |
| PR 3 | Phase 3 (WD.Available enrichment + unit tests) | Review evaluate-all-then-pick logic |
| PR 4 | Phase 4 (Workload.Available enrichment + new test file) | Review tie-break rule |
| PR 5 | Phase 7 (e2e test + edge-lab verification) | Gate on edge-lab run passing |

PRs 2, 3, and 4 are independent of each other once PR 1 merges (no dependency
between Instance.Ready, WD.Available, and Workload.Available rollup logic). They
can be reviewed in parallel and merged in any order. PR 5 requires PRs 2–4 to be
deployed so the e2e scenario can observe all three layers simultaneously.

Natural review checkpoints:
1. After PR 1: confirm no test regressions and string-value decisions are correct.
2. After PRs 2–4 merge: confirm `make test` is green across all three test files.
3. After PR 5: edge-lab manual run produces expected output → RFC status → approved.

---

## 6. Test Strategy Checklist

### Unit tests (per phase)

- [ ] `blockingReasonPriority` in `instance_controller.go` — all 8 priority levels tested, unknown returns 0
- [ ] `blockingReasonPriority` in `workloaddeployment_controller.go` — same
- [ ] `blockingReasonPriority` in `workload_controller.go` — same
- [ ] Instance gate→reason mapping: `SourceNotFound` from WD propagates verbatim
- [ ] Instance gate→reason mapping: `SourceTooLarge` from WD propagates verbatim
- [ ] Instance gate→reason mapping: `AwaitingPropagation` — WD has it but cell uses own message
- [ ] Instance gate→reason mapping: Quota + ReferencedData present → quota wins (existing behavior)
- [ ] Instance gate→reason mapping: network failure + ReferencedData → priority function picks correctly
- [ ] WD Available: `ReferencedDataNotReady` when WD `ReferencedDataReady=False`
- [ ] WD Available: `QuotaNotGranted` when `quotaBlockedReplicas > 0`, no ref-data error
- [ ] WD Available: `ReferencedDataNotReady` beats `QuotaNotGranted`
- [ ] WD Available: `ReferencedDataNotReady` beats `NetworkProvisioning`
- [ ] WD Available: `ObservedGeneration` set correctly
- [ ] Workload Available: worst reason propagated from deployments
- [ ] Workload Available: mixed reasons → highest priority wins
- [ ] Workload Available: at least one available deployment → `True` regardless
- [ ] Workload Available: no deployments → `NoAvailablePlacements`
- [ ] Workload Available: tie-break by deployment name (lexicographic)
- [ ] Workload Available: `ObservedGeneration` set correctly

### Federated e2e

- [ ] `TestStatusBlockingReasonContract_MissingConfigMap` test passes in CI
- [ ] Step 1: Workload with missing ConfigMap — `SourceNotFound` visible at all three layers within 30 s
- [ ] Step 2: ConfigMap created — all three layers healthy within 60 s (resolver re-triggers via `enqueueWDsForSource`)

### Edge-lab acceptance (manual)

- [ ] `datumctl compute instances` shows `reason=SourceNotFound` (not `SchedulingGatesPresent`)
- [ ] `kubectl get workloaddeployments` shows `Available=False`, `reason=ReferencedDataNotReady`
- [ ] `kubectl get workloads` shows `Available=False`, `reason=ReferencedDataNotReady`
- [ ] After `kubectl create configmap`: all three layers healthy within 60 s
- [ ] Verify no regression on a workload with all-present ConfigMaps (should show `Available=True` throughout)
