# RFC: Unified Status Blocking-Reason Contract

**Status:** Draft  
**Date:** 2026-06-01  
**Scope:** `api/v1alpha`, `internal/controller/workload_controller.go`, `internal/controller/workloaddeployment_controller.go`, `internal/controller/instance_controller.go`

---

## 1. Problem Statement

When a Workload references a ConfigMap or Secret that does not exist, the platform knows exactly
what is wrong. Clients cannot surface it without writing resource-type- and condition-type-specific
code:

- The `WorkloadDeployment` has `ReferencedDataReady=False, reason=SourceNotFound,
  message='ConfigMap "X" not found in namespace "default"'`. This is set by
  `ReferencedDataController.setConditionAndReturn` in `internal/controller/referenceddata_controller.go`.
- That rolls up to the Workload as a boolean only. `reconcileWorkloadStatus` in
  `internal/controller/workload_controller.go` reads only `Available=True/False` from each deployment;
  when no deployment is available the reason becomes the hardcoded `NoAvailablePlacements` and the
  deployment-specific message is discarded entirely.
- The Instance — the resource that `datumctl compute instances` actually fetches — has only a
  generic `Ready=False/SchedulingGatesPresent` with a message that lists gate names (e.g.
  `"Scheduling gates present: ReferencedData"`). The specific object that is missing is invisible
  at the Instance level. This is produced by `reconcileInstanceReadyCondition` in
  `internal/controller/instance_controller.go`.

A client must therefore (1) know to fetch a WorkloadDeployment as a secondary resource, (2) know
the specific condition type `ReferencedDataReady`, and (3) parse free text. Every client
reimplements this — the `datumctl compute` CLI, the web console, and API consumers.

---

## 2. Locked Decisions

The following architectural decisions are made. This RFC designs the mechanism that realizes them;
it does not revisit them.

1. **Surface = Ready-condition rollup.** Each user-facing resource (Instance, WorkloadDeployment,
   Workload) exposes a single top-level readiness condition that, when `status != True`, carries a
   stable machine-readable PascalCase `reason` and a human-readable `message`. The single generic
   client rule is: read the readiness condition; if `status != True`, display `reason + message`.

2. **Depth = all three layers are self-describing.** The Instance's readiness condition must reflect
   its holding gate's specific reason and message, including the name of the offending object. The
   WorkloadDeployment aggregates its worst blocking sub-condition. The Workload rolls the
   reason+message up from its worst deployment instead of collapsing to a boolean.

3. **Shared stable reason vocabulary defined once in the API package.** The same constants are used
   consistently across all three resources.

---

## 3. Client Contract

### 3.1 Readiness Condition per Kind

| Kind | Readiness Condition Type | Status when healthy | Where printed today |
|------|--------------------------|--------------------|--------------------|
| `Instance` | `Ready` (`InstanceReady = "Ready"`) | `True` | `kubectl get instances` "READY" column, `instances.go` CLI |
| `WorkloadDeployment` | `Available` (`WorkloadDeploymentAvailable = "Available"`) | `True` | `kubectl get workloaddeployments` "AVAILABLE" column |
| `Workload` | `Available` (`WorkloadAvailable = "Available"`) | `True` | `kubectl get workloads` "AVAILABLE" column, `status.go` CLI |

The condition-type names differ by kind because they reflect existing API surface that must not
break. A generic client identifies the readiness condition using the following documented
convention:

> **Convention:** For any compute resource, the readiness condition is the one whose `type` is
> declared as the primary readiness sentinel for that kind. Clients that handle multiple kinds use
> a per-kind lookup table keyed on `GroupVersionKind`:
>
> | GVK | Readiness condition type |
> |-----|--------------------------|
> | `compute.datumapis.com/v1alpha/Instance` | `Ready` |
> | `compute.datumapis.com/v1alpha/WorkloadDeployment` | `Available` |
> | `compute.datumapis.com/v1alpha/Workload` | `Available` |
>
> This table is the entire branching logic a client needs. Once the condition is found, the display
> rule is identical for all three: if `status != "True"`, show `reason` (machine-readable) and
> `message` (human-readable).

The condition types are already declared as constants in `api/v1alpha/instance_types.go`
(`InstanceReady`, `WorkloadDeploymentAvailable`, `WorkloadAvailable`). No new types are added.

### 3.2 What "blocking reason" means at each layer

| Layer | `status` when blocked | `reason` | `message` |
|-------|-----------------------|----------|-----------|
| `Instance.Ready` | `False` or `Unknown` | Stable enum (§4) | Human description naming the specific object or quota constraint |
| `WorkloadDeployment.Available` | `False` | Stable enum | Worst blocking reason from sub-conditions, e.g. "ConfigMap X not found" |
| `Workload.Available` | `False` | Stable enum | Propagated from worst WorkloadDeployment |

---

## 4. Shared Reason Vocabulary

### 4.1 Constants Location

All reason constants live in `api/v1alpha/instance_types.go` under a new `const` block, following
the existing pattern of named blocks for each concern. This is the single source of truth. No
controller may use inline string literals for reasons that appear in readiness conditions.

### 4.2 Existing Constants to Reuse (no change)

The following constants already exist in `api/v1alpha/instance_types.go` and are already
well-named. They are reused as-is in the expanded rollup:

```
// ReferencedData reasons (already in api/v1alpha/instance_types.go)
ReferencedDataReasonResolving              = "Resolving"
ReferencedDataReasonAwaitingPropagation    = "AwaitingPropagation"
ReferencedDataReasonSourceNotFound         = "SourceNotFound"
ReferencedDataReasonSourceUnauthorized     = "SourceUnauthorized"
ReferencedDataReasonSourceTooLarge         = "SourceTooLarge"
ReferencedDataReasonReady                  = "Ready"

// Quota reasons (already in api/v1alpha/instance_types.go)
InstanceQuotaGrantedReasonQuotaExceeded    = "QuotaExceeded"
InstanceQuotaGrantedReasonPendingEvaluation = "PendingEvaluation"
InstanceQuotaGrantedReasonNoBudget         = "QuotaNoBudget"
InstanceQuotaGrantedReasonBackendUnavailable = "QuotaBackendUnavailable"
InstanceQuotaGrantedReasonProjectNotFound  = "QuotaProjectNotFound"

// Instance Ready reasons (already in api/v1alpha/instance_types.go)
InstanceReadyReasonSchedulingGatesPresent  = "SchedulingGatesPresent"
InstanceReadyReasonRunning                 = "Running"
InstanceProgrammedReasonPendingQuota       = "PendingQuota"
```

### 4.3 New Constants to Add

The following new reason constants are added to `api/v1alpha/instance_types.go`. They appear on
the top-level readiness condition when the corresponding sub-system is blocking.

```go
const (
    // NetworkNotFound is set on Workload.Available when one or more networks
    // referenced by network interfaces do not exist. Already used inline in
    // workload_controller.go as "NetworkNotFound" — this constant replaces the
    // inline string.
    WorkloadReasonNetworkNotFound = "NetworkNotFound"

    // NetworkProvisioning is set on WorkloadDeployment.Available while the
    // network binding or subnet is still being provisioned. Already used inline
    // in workloaddeployment_controller.go as "ProvisioningNetwork".
    WorkloadDeploymentReasonNetworkProvisioning = "NetworkProvisioning"

    // InstancesProvisioning is set on WorkloadDeployment.Available while
    // instances exist but none are ready yet. Already used inline as
    // "ProvisioningInstances".
    WorkloadDeploymentReasonInstancesProvisioning = "InstancesProvisioning"

    // ReferencedDataNotReady is set on WorkloadDeployment.Available and
    // Workload.Available when the worst-blocking sub-condition is a
    // ReferencedData failure. This is a new rollup reason that carries the
    // ReferencedDataReady sub-condition's reason+message into the top-level
    // readiness condition.
    WorkloadDeploymentReasonReferencedDataNotReady = "ReferencedDataNotReady"

    // QuotaNotGranted is set on WorkloadDeployment.Available and
    // Workload.Available when quota is blocking one or more instances.
    WorkloadDeploymentReasonQuotaNotGranted = "QuotaNotGranted"

    // NoAvailablePlacements is set on Workload.Available when all placements
    // report no available deployments. Already used inline; this constant
    // replaces the inline string.
    WorkloadReasonNoAvailablePlacements = "NoAvailablePlacements"

    // NoAvailableDeployments is set on placement-level Available condition
    // when no deployment in a placement is available. Already used inline;
    // this constant replaces the inline string.
    WorkloadReasonNoAvailableDeployments = "NoAvailableDeployments"
)
```

### 4.4 Reason-to-Failure-Mode Mapping

| Failure mode | Sub-condition | Reason on top-level readiness condition |
|---|---|---|
| Source ConfigMap/Secret not found | `ReferencedDataReady=False/SourceNotFound` | `ReferencedDataNotReady` (WD, Workload); maps to existing `SourceNotFound`-derived message |
| Source ConfigMap/Secret unauthorized | `ReferencedDataReady=False/SourceUnauthorized` | `ReferencedDataNotReady` |
| Source too large | `ReferencedDataReady=False/SourceTooLarge` | `ReferencedDataNotReady` |
| Companion not yet propagated to cell | `ReferencedDataReady=False/AwaitingPropagation` | `ReferencedDataNotReady` |
| Resolver not finished | `ReferencedDataReady=Unknown/Resolving` | `ReferencedDataNotReady` |
| Quota exceeded | `QuotaGranted=False/QuotaExceeded` | `QuotaNotGranted` (WD, Workload); `PendingQuota` (Instance, already set) |
| Quota disabled or pending | `QuotaGranted=Unknown/PendingEvaluation` | passthrough |
| Network not ready | `Available=False/NetworkProvisioning` on WD | `NetworkProvisioning` |
| Network not found | inline on Workload | `NetworkNotFound` |
| Instance not yet programmed | `Programmed=False/PendingProgramming` | `PendingProgramming` (already set on Instance.Ready) |

---

## 5. Server-Side Rollup Mechanism

### 5.1 Instance.Ready (cell controller: `internal/controller/instance_controller.go`)

**Current behavior (`reconcileInstanceReadyCondition`):** when scheduling gates are present, the
`Ready` condition is set to `SchedulingGatesPresent` with a message listing gate names (e.g.
`"Scheduling gates present: ReferencedData, Quota"`). This is the generic fallback; it does not
surface which specific object is missing.

**Required change:** when scheduling gates are present, the controller selects the most specific
reason from the instance's own sub-conditions rather than falling back to the generic gate-list
message. The priority ordering within `reconcileInstanceReadyCondition` becomes:

1. `QuotaGranted=False` → `Ready=False/PendingQuota` with the quota sub-condition's message.
   (Already implemented for `QuotaGranted=False`. No change required here.)
2. `ReferencedDataReady=False` or `ReferencedDataReady=Unknown` → `Ready=False` or
   `Ready=Unknown` with reason and message taken directly from the `ReferencedDataReady`
   sub-condition. The reason is preserved verbatim (e.g. `SourceNotFound`) and the message (e.g.
   `ConfigMap "X" not found in namespace "default"`) is copied.
3. Network gate present + network creation failure → `Ready=False/NetworkFailedToCreate` (already
   implemented).
4. Any remaining gate → `Ready=False/SchedulingGatesPresent` with message listing remaining gate
   names.

**Source of the Instance's ReferencedDataReady message:** The cell `InstanceReconciler` already
sets `ReferencedDataReady` with specific messages in `reconcileReferencedDataCondition`:
- `SourceNotFound`: "Waiting for N companion(s) to arrive on cell: ConfigMap/X" (from
  `ReferencedDataReasonAwaitingPropagation`). At this level the cell knows which companions are
  missing.
- For the `SourceNotFound` reason from the resolver (visible on the HUB WD), the cell sets
  `AwaitingPropagation` by default because from the cell's perspective the companion simply hasn't
  arrived — it cannot distinguish "missing at source" from "slow propagation".

**Federation boundary — why conditions are not enough:** In federation, Karmada's
`statusAggregation` interpreter is cell→hub only. The HUB WD carries
`ReferencedDataReady=False/SourceNotFound` as a status condition, but this condition is **not**
propagated back to the cell WD copy. Annotations DO flow hub→cell via Karmada's metadata
propagation (carried on the federated WorkloadDeployment object).

**Propagating the terminal error via annotation (hub→cell):** The `ReferencedDataController`
(management/hub plane) stamps a `compute.datumapis.com/referenced-data-error` annotation on the
HUB WD whenever it sets a terminal error condition (`SourceNotFound`, `SourceUnauthorized`,
`SourceTooLarge`). The annotation carries a JSON payload `{"reason":"...","message":"..."}`.
Karmada propagates this annotation to the cell WD copy alongside the WorkloadDeployment object.

The cell `InstanceReconciler` reads this annotation from the cell WD (returned by
`fetchOwnerWorkloadDeployment`) via the `applyTerminalErrorFromWD` helper. When the annotation
is present with a terminal reason, the Instance's `ReferencedDataReady` condition is set directly:

```
ReferencedDataReady = False
reason  = SourceNotFound   (from annotation "reason" field)
message = ConfigMap "X" not found in namespace "default"   (from annotation "message" field)
```

A condition-based fallback (reading `wd.Status.Conditions` directly) is retained for
single-cluster deployments where the condition is locally readable, and for the controller upgrade
window before the annotation was introduced.

When no terminal-error annotation is present and the WD condition reason is `AwaitingPropagation`
or `Resolving`, the Instance uses its own companion-diff message (the list of missing "Kind/name"
tokens) because that is more specific.

The annotation is cleared by the resolver when the error resolves (companion materialises /
`ReferencedDataReady` flips True) and when `reconcileEmpty` runs (WD template has no references).
This prevents a stale annotation from permanently blocking a healthy Instance.

This approach requires zero extra API calls: `fetchOwnerWorkloadDeployment` is already called
every reconcile for this instance, and the annotation read is a map lookup on the returned object.

**Resulting Instance.Ready states after this RFC:**

| Gate | Sub-condition | Instance.Ready status/reason/message |
|---|---|---|
| `Quota` present | `QuotaGranted=False/QuotaExceeded` | `False/PendingQuota/<quota message>` |
| `ReferencedData` present | WD `ReferencedDataReady=False/SourceNotFound` | `False/SourceNotFound/ConfigMap "X" not found in namespace "default"` |
| `ReferencedData` present | WD `ReferencedDataReady=False/SourceTooLarge` | `False/SourceTooLarge/<size message>` |
| `ReferencedData` present | Cell companions missing | `False/AwaitingPropagation/Waiting for N companion(s) to arrive on cell: ConfigMap/X` |
| `ReferencedData` present | WD annotation absent | `Unknown/Resolving/Waiting for the resolver to finish reading source objects` |
| `Network` present, creation failed | — | `False/NetworkFailedToCreate/<network message>` |
| Any remaining gate | — | `False/SchedulingGatesPresent/Scheduling gates present: <gate names>` |
| No gates, `Programmed=False` | — | `False/PendingProgramming/<programmed message>` |
| No gates, `Running=False` | — | `False/<running reason>/<running message>` |
| All conditions true | — | `True/Running/Instance is ready` |

### 5.2 WorkloadDeployment.Available (cell controller: `internal/controller/workloaddeployment_controller.go`)

**Current behavior (`Reconcile`):** `Available` is set to `True/StableInstanceFound` when
`readyReplicas > 0`, or `False/ProvisioningNetwork`, `False/ProvisioningInstances` otherwise. The
`ReferencedDataReady` and `QuotaGranted` sub-conditions are reflected only in the separate
`ReplicasReady` condition, not in `Available`.

**Required change:** When `readyReplicas == 0` and all replicas are blocked, the `Available`
condition is enriched with the worst blocking reason from the instance fleet or from the WD's own
sub-conditions, in priority order (see §5.4).

The WD controller already has the counts: `quotaBlockedReplicas` and `referencedDataBlockedReplicas`
from `reconcileInstanceGates`. It additionally has `deployment.Status.Conditions` which holds the
WD-level `ReferencedDataReady` condition set by `ReferencedDataController`. The rollup logic uses
both:

**Priority ordering in `Available` condition (lowest `readyReplicas` path):**

1. Network not yet ready → `False/NetworkProvisioning/"Network is being provisioned"` (existing,
   unchanged).
2. `ReferencedDataReady` sub-condition is `False` on the WD → `False/ReferencedDataNotReady` with
   the WD's `ReferencedDataReady` message verbatim (e.g. `ConfigMap "X" not found in namespace
   "default"`). This fires before quota because a missing config is a hard stop; quota may still
   succeed after the config is fixed.
3. `quotaBlockedReplicas > 0` → `False/QuotaNotGranted/"N of M desired instances pending quota"`.
4. `referencedDataBlockedReplicas > 0` with `ReferencedDataReady=True` on WD (propagation lag) →
   `False/ReferencedDataNotReady/"N of M desired instances waiting for companion propagation"`.
5. Replicas exist but none are ready → `False/InstancesProvisioning/"Instances are being
   provisioned"` (existing behavior, renamed from `ProvisioningInstances`).
6. No replicas yet → same.

The `ReplicasReady` condition remains a detail condition alongside `Available`. It is not removed
(backward compatible).

### 5.3 Workload.Available (management controller: `internal/controller/workload_controller.go`)

**Current behavior (`reconcileWorkloadStatus`):** Iterates over `placementDeployments`, checks
`apimeta.IsStatusConditionTrue(deployment.Status.Conditions, "Available")`, and sets the workload
`Available` condition to `True/AvailablePlacementFound` or `False/NoAvailablePlacements`. The WD's
reason and message are never read.

**Required change:** When no placement has an available deployment, read the worst deployment's
`Available` condition and propagate its `reason` and `message` to the Workload `Available`
condition. The word "worst" is defined by the priority ordering in §5.4.

Algorithm in `reconcileWorkloadStatus`:

```
worstReason  = "NoAvailablePlacements"
worstMessage = "No available placements were found for the workload"

for each deployment in all placements:
    availCond = FindStatusCondition(deployment.Status.Conditions, "Available")
    if availCond == nil || availCond.Status == True:
        continue
    if priority(availCond.Reason) > priority(worstReason):
        worstReason  = availCond.Reason
        worstMessage = availCond.Message

Workload.Available = {Status: False, Reason: worstReason, Message: worstMessage}
```

The priority function is defined in §5.4. The workload-level condition type is still `Available`.
The existing `NoAvailablePlacements` reason becomes the last-resort default, used only when no
deployment has a more specific condition.

The workload `Available` condition also carries `ObservedGeneration` set to
`workload.Generation`, matching the existing pattern for Instance conditions.

### 5.4 Priority Ordering

When multiple causes block simultaneously, the server — not the client — decides which
`reason`+`message` to surface. The guiding principle:

> **Permanent, user-actionable causes outrank transient or infra causes; among permanent
> causes, surface the one the viewer can act on directly.** A cause that will never clear on
> its own (a missing or rejected source object, a missing network) is more important to show
> than one expected to clear with time (provisioning, in-flight propagation).

New reasons classify themselves against this principle rather than being appended ad hoc.
Higher number = higher priority (wins):

| Priority | Reason | Rationale |
|----------|--------|-----------|
| 0 | `NoAvailablePlacements` / `NoAvailableDeployments` | Default fallback; no information |
| 1 | `InstancesProvisioning` | Transient startup; expected to clear |
| 2 | `NetworkProvisioning` | Infra provisioning; expected to clear |
| 3 | `PendingQuota` / `QuotaNotGranted` | Operator action may be needed |
| 4 | `ReferencedDataNotReady` / `AwaitingPropagation` / `Resolving` | Transient — companion in flight; expected to clear |
| 5 | `SourceNotFound` / `SourceTooLarge` / `SourceUnauthorized` | Hard referenced-data spec error; will not clear without user action |
| 6 | `NetworkNotFound` | Hard error at spec level; user action required |
| 7 | `NetworkFailedToCreate` | Hard infra error; operator attention |

`SourceNotFound` is a *terminal* referenced-data error — the resolver has determined the source
object does not exist, so the companion will never arrive — and therefore ranks with the other hard
errors (priority 5), above the transient `AwaitingPropagation`/`Resolving` states (priority 4). A
genuinely missing ConfigMap must not be masked by "awaiting propagation" just because some other
reference is still in flight.

The ordering is implemented as a private `blockingReasonPriority` function in each controller
that uses it. The function signature is:

```go
func blockingReasonPriority(reason string) int
```

It is a simple switch/case returning the integer from the table above. Cases not listed return 0.
This function is co-located with the rollup logic in each controller file; it is not shared across
packages because it is cheap to define twice and avoids coupling the two controller packages.

**This ordering is a server-internal implementation detail, not part of the API contract.**
Clients only ever read the winning condition's `reason`+`message`; they never observe the ranking.
The order can therefore be tuned post-hoc — e.g. informed by telemetry on which blockers actually
co-occur — without a breaking change or client coordination. Treat the table as a sensible default,
not a frozen interface.

---

## 6. observedGeneration / Staleness

All conditions modified as part of this RFC set `ObservedGeneration` to the resource's current
`metadata.generation`. This is already done for Instance conditions (e.g.
`reconcileInstanceReadyCondition` passes `ObservedGeneration: instance.Generation`).

The WD and Workload controllers do not currently set `ObservedGeneration` on all conditions.
As part of this RFC, the `Available` conditions on WorkloadDeployment and Workload must include
`ObservedGeneration`. This allows clients to detect whether the condition reflects the current spec
or is stale from a previous generation.

Clients that need staleness detection check `condition.ObservedGeneration == resource.Generation`.
The client contract (§3) does not require clients to do this check — it is an optional signal for
advanced consumers.

---

## 7. Backward Compatibility

### 7.1 What is preserved

- `ReferencedDataReady` remains as a detail condition on both WorkloadDeployment and Instance. It is
  not removed. Existing clients that read this condition continue to work.
- `QuotaGranted` remains on Instance.
- `ReplicasReady` remains on WorkloadDeployment.
- `Programmed` and `Running` remain on Instance.
- All existing constant names in `api/v1alpha/instance_types.go` are preserved.

### 7.2 What changes

- `WorkloadDeployment.Available` gains a richer reason+message when `readyReplicas == 0`. Clients
  that read only `status == True/False` are unaffected.
- `Workload.Available` gains a reason+message sourced from the worst WD. Clients that read only
  `status == True/False` are unaffected. The existing `NoAvailablePlacements` reason continues to
  appear as a fallback — it is not removed.
- `Instance.Ready` gains more specific reasons when scheduling gates are present. The existing
  `SchedulingGatesPresent` reason still appears when no more-specific sub-condition is available.
- New string constants are added to `api/v1alpha/instance_types.go`. No existing constants are
  renamed or removed.
- Several reasons that were previously inline string literals in controllers become named
  constants. The string values are identical to what was already being written.

### 7.3 CRD / schema impact

No new fields are added to any `*Status` struct. The `conditions` slice already accepts any
`metav1.Condition`. No CRD regeneration is required for the contract changes. The new reason
constants are purely additive to the API package.

---

## 8. Test Plan

### 8.1 Unit Tests

**`instance_controller_test.go` additions:**

- `reconcileInstanceReadyCondition` with `ReferencedData` gate present and WD
  `ReferencedDataReady=False/SourceNotFound` → assert `Ready=False/SourceNotFound` with the WD
  message verbatim.
- Same with WD `ReferencedDataReady=False/SourceTooLarge` → assert `SourceTooLarge` propagates.
- `ReferencedData` gate present, WD `ReferencedDataReady=False/AwaitingPropagation`, cell
  companions partially present → assert `Ready=False/AwaitingPropagation` with cell-side message
  listing missing companions.
- `Quota` gate present AND `ReferencedData` gate present with WD `SourceNotFound` → assert
  `ReferencedData` wins (`SourceNotFound` priority 5 > quota priority 3): `Ready=False/SourceNotFound`.
- `Quota` gate present, `QuotaGranted=False/QuotaExceeded` → `Ready=False/PendingQuota` (existing
  test, confirm still passes).

**`workloaddeployment_controller_test.go` additions:**

- WD `ReferencedDataReady=False/SourceNotFound` and `readyReplicas==0` → assert
  `Available=False/ReferencedDataNotReady` with the WD `ReferencedDataReady.Message` verbatim.
- `quotaBlockedReplicas > 0`, `ReferencedDataReady=True`, `readyReplicas==0` → assert
  `Available=False/QuotaNotGranted`.
- WD `ReferencedDataReady=False/SourceNotFound` AND `quotaBlockedReplicas > 0` → assert
  `Available=False/ReferencedDataNotReady` (referenced-data wins at priority 4 vs quota at 3).
- `networkReady=false`, `ReferencedDataReady=False/SourceNotFound` → assert
  `Available=False/NetworkProvisioning` (network wins path is before referenced-data check, but
  network-provisioning has lower priority than referenced-data; see note below).

  Note on network priority: The network-ready check happens first in the existing control flow
  because networking is a prerequisite for instance creation. The priority ordering in §5.4 reflects
  the final user-facing importance, not the control-flow order. When networking is not ready AND
  a referenced-data error exists simultaneously, the implementation must evaluate both and apply the
  priority function to select the message. In practice this state is rare (WD is newly created;
  networking and data resolution start together).

**`workload_controller_test.go` additions:**

- All deployments `Available=False/ReferencedDataNotReady` with identical messages →
  Workload `Available=False/ReferencedDataNotReady` with that message.
- Deployments with mixed reasons → Workload picks the highest-priority reason.
- At least one deployment `Available=True` → Workload `Available=True/AvailablePlacementFound`
  regardless of other deployments.

**`blockingReasonPriority` function:** unit test the priority table exhaustively (all defined
reasons return their expected integer; an unknown string returns 0).

### 8.2 Federated e2e Tests

A new e2e test scenario under the existing federated test suite:

1. Create a Workload whose template references a ConfigMap that does not exist.
2. Wait for the WorkloadDeployment to reach `ReferencedDataReady=False/SourceNotFound`.
3. Assert that within 30 s:
   - `WorkloadDeployment.Available=False`, `reason=ReferencedDataNotReady`,
     `message` contains `ConfigMap "X" not found`.
   - `Workload.Available=False`, same `reason` and `message`.
   - Each `Instance.Ready=False`, `reason=SourceNotFound`,
     `message` contains `ConfigMap "X" not found in namespace "default"`.
4. Create the ConfigMap.
5. Assert that within 60 s all three layers flip to their ready/available states.

### 8.3 Edge-Lab Verification (mirrors today's missing-ConfigMap repro)

Manual verification procedure:

```bash
# 1. Deploy a workload referencing a non-existent ConfigMap.
datumctl compute deploy --file=workload-missing-config.yaml

# 2. Check Instance — expect reason=SourceNotFound, not SchedulingGatesPresent.
kubectl get instances -o json | jq '.items[0].status.conditions[] | select(.type=="Ready")'
# Expected: {"type":"Ready","status":"False","reason":"SourceNotFound","message":"ConfigMap \"X\" not found in namespace \"default\""}

# 3. Check WorkloadDeployment — expect Available=False/ReferencedDataNotReady.
kubectl get workloaddeployments -o json | jq '.items[0].status.conditions[] | select(.type=="Available")'
# Expected: {"type":"Available","status":"False","reason":"ReferencedDataNotReady","message":"ConfigMap \"X\" not found in namespace \"default\""}

# 4. Check Workload — expect Available=False/ReferencedDataNotReady.
kubectl get workloads -o json | jq '.items[0].status.conditions[] | select(.type=="Available")'
# Expected: {"type":"Available","status":"False","reason":"ReferencedDataNotReady","message":"ConfigMap \"X\" not found in namespace \"default\""}

# 5. Create the ConfigMap.
kubectl create configmap X --from-literal=key=val

# 6. Re-check all three layers — all should be healthy within 60 s.
```

---

## 9. Client Impact

### 9.1 Before this RFC

The `datumctl compute instances` command (in `internal/cmd/compute/instances/instances.go`) shows
`Ready=False/SchedulingGatesPresent` for a gated instance. The operator must then:

1. Know that an Instance has a `ReferencedData` gate.
2. Find the owning WorkloadDeployment (`kubectl get workloaddeployments -l
   compute.datumapis.com/workload-uid=<uid>`).
3. Read `conditions[type==ReferencedDataReady]` on the WD.
4. Parse the message to learn that `ConfigMap "X"` is missing.

This requires knowledge of four resource types, two API calls, and a condition-type-specific lookup.

**Before — `datumctl compute instances` output:**

```
NAME           READY     REASON                    AGE
api-dfw-0      False     SchedulingGatesPresent     2m
```

**Before — `datumctl compute instances describe api-dfw-0` (abbreviated):**

```
Ready:   False
Reason:  SchedulingGatesPresent
Message: Scheduling gates present: ReferencedData
```

### 9.2 After this RFC

The Instance's `Ready` condition directly carries the blocking reason. A single fetch of the
Instance is sufficient.

**After — `datumctl compute instances` output:**

```
NAME           READY     REASON             AGE
api-dfw-0      False     SourceNotFound     2m
```

**After — `datumctl compute instances describe api-dfw-0` (abbreviated):**

```
Ready:   False
Reason:  SourceNotFound
Message: ConfigMap "X" not found in namespace "default"
```

**Simplified plugin code:**

The `datumctl compute` plugin currently needs special-case code to handle the `ReferencedData`
scheduling gate:

```go
// BEFORE: special-case to surface referenced-data failures
if reason == "SchedulingGatesPresent" {
    wd, _ := fetchWorkloadDeployment(ctx, instance)
    if cond := findCondition(wd.Status.Conditions, "ReferencedDataReady"); cond != nil && cond.Status == "False" {
        reason = cond.Reason
        message = cond.Message
    }
}
```

After this RFC the plugin reads the `Ready` condition directly:

```go
// AFTER: one rule for all blocking reasons
cond := findCondition(instance.Status.Conditions, "Ready")
if cond != nil && cond.Status != "True" {
    displayBlockingReason(cond.Reason, cond.Message)
}
```

The `WorkloadDeployment` fetch in the plugin is no longer needed for "why isn't this running"
display. It is still needed for other purposes (city code, location) but the error-surfacing path
is eliminated.

### 9.3 UI / API Consumer Contract

A UI team implementing a "why isn't this instance running" panel needs to implement exactly one
rule:

1. `GET /apis/compute.datumapis.com/v1alpha/namespaces/{ns}/instances/{name}`
2. Find `status.conditions` where `type == "Ready"`.
3. If `status != "True"`: display `reason` (badge) and `message` (prose).

No secondary resource fetch. No condition-type branching. The same pattern works at the Workload
level by reading `type == "Available"` instead.

---

## 10. Future / Out of Scope

The following are explicitly deferred and are mentioned only to record that they were considered:

**Structured `status.blockers[]` field:** A typed array of blocker objects (each with `type`,
`reason`, `message`, `since`) on each resource would allow multiple simultaneous blocking reasons
to be surfaced without priority ordering. This is a larger API change (new CRD fields, schema
bump, migration of existing automation). It is not part of this RFC. The reason-priority model
in §5.4 is the simpler mechanism that realizes the same client experience without a schema change.

**Cross-layer correlation IDs:** Stamping a shared trace/correlation ID across Instance, WD, and
Workload so clients can correlate conditions without knowing the owner-reference chain. Deferred;
the owner-reference chain already provides this linkage for controllers.

**Condition history / timeline:** Recording when each condition last changed reason (beyond
`lastTransitionTime`, which only changes when `status` flips). Deferred to a future activity/event
API.

---

## 11. Implementation Plan

The following steps are ordered for a safe, reviewable rollout. Each step is independently
mergeable.

**Step 1: API constants** — Add new reason constants to `api/v1alpha/instance_types.go`. Replace
all inline string literals in controllers that match existing constant values with the named
constants. No behavior change.

**Step 2: Instance.Ready enrichment** — Modify `reconcileInstanceReadyCondition` in
`internal/controller/instance_controller.go` to implement the priority ordering in §5.1.
Extend `reconcileReferencedDataCondition` to prefer WD `SourceNotFound`/`SourceTooLarge`/
`SourceUnauthorized` reasons. Add unit tests.

**Step 3: WorkloadDeployment.Available enrichment** — Modify the `Available` condition assignment
in `WorkloadDeploymentReconciler.Reconcile` in `internal/controller/workloaddeployment_controller.go`
to implement §5.2 priority ordering. Add unit tests.

**Step 4: Workload.Available enrichment** — Modify `reconcileWorkloadStatus` in
`internal/controller/workload_controller.go` to implement §5.3 worst-deployment propagation.
Add `ObservedGeneration` to the `Available` condition. Add unit tests.

**Step 5: e2e test** — Add the federated e2e scenario from §8.2.

**Step 6: Edge-lab verification** — Run the manual repro from §8.3 against the dev/lab
environment. Mark RFC as approved.

**Step 7: CLI / UI update** — Remove the special-case `ReferencedDataReady` fetch from the
`datumctl compute` instances display path. Update `instances describe` to render `reason` and
`message` from the `Ready` condition directly. (Tracked separately in the CLI plugin repo.)

---

## 12. Open Questions

1. **(Non-blocking)** Should `blockingReasonPriority` be a shared function in a new
   `internal/conditions` package to avoid the two-copy pattern? Given that the function body is
   a 15-line switch, duplication is acceptable. If a third controller needs it, extract then.

2. **(Non-blocking)** Should `Workload.Available` carry the city code of the worst deployment so
   the message is more actionable (e.g. "ConfigMap X not found — affecting DFW")? The WD message
   already names the object. City attribution is additive and can be appended to the message
   string without a schema change.

3. **(Blocking for Step 4)** The workload controller iterates over `placementDeployments` which is
   a `map[string][]WorkloadDeployment`. The worst-deployment selection requires a deterministic
   tie-break when two deployments have equal priority. Tie-break rule: lexicographic by deployment
   name. Confirm this is acceptable before implementing Step 4.

---

## 13. Relevant Files

| File | Role |
|------|------|
| `api/v1alpha/instance_types.go` | All condition type and reason constants; new constants added here |
| `api/v1alpha/workload_types.go` | `WorkloadAvailable = "Available"` constant; `WorkloadStatus.Conditions` |
| `api/v1alpha/workloaddeployment_types.go` | `WorkloadDeploymentAvailable`, `WorkloadDeploymentReplicasReady` constants |
| `internal/controller/instance_controller.go` | `reconcileInstanceReadyCondition`, `reconcileReferencedDataCondition`, `fetchOwnerWorkloadDeployment` |
| `internal/controller/workloaddeployment_controller.go` | `Reconcile` (Available condition assignment), `reconcileInstanceGates` |
| `internal/controller/workload_controller.go` | `reconcileWorkloadStatus` |
| `internal/controller/referenceddata_controller.go` | Sets `ReferencedDataReady` on WD — source of truth for resolver errors |
| `internal/controller/instancecontrol/scheduling_gates.go` | Gate name constants (`ReferencedDataSchedulingGate`, `QuotaSchedulingGate`, `NetworkSchedulingGate`) |
| `internal/controller/instance_controller_test.go` | Unit tests for Instance.Ready rollup |
| `internal/controller/workloaddeployment_controller_test.go` | Unit tests for WD.Available rollup |
| `internal/controller/workload_controller_test.go` | (Does not exist yet at time of writing; create for Step 4) |
