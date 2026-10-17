# Workload Status Reasons

**Status:** Draft

---

## Summary

When a workload isn't running, the user gets a single, consistent, actionable reason and a plain-language message explaining why — instead of digging through raw, nested Kubernetes conditions across three different resources.

Every compute resource — Instance, WorkloadDeployment, and Workload — exposes exactly one readiness condition. When that condition isn't healthy, it carries a stable machine-readable reason (e.g. `ReferencedDataNotReady`, `QuotaNotGranted`, `NetworkNotFound`) and a human-readable message that names the specific thing that's wrong (e.g. `ConfigMap "X" not found in namespace "default"`). The platform rolls the most relevant blocking reason up from the Instance to the WorkloadDeployment to the Workload, so the user sees the real "why" at whatever level they're looking. The same answer is surfaced the same way everywhere — `datumctl compute` today, the web console later, and any API consumer.

---

## The problem today

When a workload is stuck, the platform usually knows exactly what's wrong — but the user can't see it without doing the platform's job for it.

Consider a workload that references a ConfigMap that doesn't exist. The information lives in three different places, none of which is where the user is looking:

- The **Instance** — the resource the user actually inspects with `datumctl compute instances` — shows only a generic `Ready: False` with reason `SchedulingGatesPresent` and a message that lists gate *names* ("Scheduling gates present: ReferencedData"). The specific object that's missing is invisible.
- The **WorkloadDeployment** has the detail, but on a different, type-specific condition (`ReferencedDataReady: False / SourceNotFound`) that the user has to know to go look for.
- The **Workload** collapses all of that to a boolean. When no deployment is available it reports a hardcoded `NoAvailablePlacements` and throws away the deployment-specific message entirely.

So to answer "why isn't my workload running," a user (or a client building on the API) has to know to fetch a secondary resource, know the exact type-specific condition to read on it, and parse free text. A quota block or a missing network looks like the same opaque `Ready: False`. Every client — the CLI, the console, automation — reimplements this archaeology, and each one does it slightly differently.

This is the platform-side gap behind the developer-experience promise in [`datumctl-compute-dx.md`](./datumctl-compute-dx.md): one of its core workflows is *"understanding why something isn't running,"* and it shows the CLI explaining a stuck rollout in plain terms with a next step. That CLI experience is only as good as the data underneath it. This enhancement is the contract that makes the data good — a single, consistent, actionable reason at every level — so the CLI (and every other client) can stop guessing and just read it.

---

## Who this is for

The primary audience is the **backend developer** deploying a containerized service to Datum Cloud. When their workload doesn't come up, they want one clear reason and one clear next step — not a tour of three resources and a dozen nested conditions. They should never need to know the platform's internal resource model to find out why something is blocked.

The secondary audience is the **platform operator** and **automation / API consumer** who need a stable, machine-readable contract: a single readiness condition per resource with a predictable reason vocabulary they can branch on, alert on, and display — without writing resource-type-specific or condition-type-specific special cases.

---

## The contract

### One readiness condition per resource

Each user-facing resource exposes a single top-level readiness condition. When that condition's status is not `True`, it always carries a stable, machine-readable `reason` and a human-readable `message`.

| Resource | Readiness condition | Healthy status |
|----------|---------------------|----------------|
| `Instance` | `Ready` | `True` |
| `WorkloadDeployment` | `Available` | `True` |
| `Workload` | `Available` | `True` |

The condition-type names differ by resource because they reflect existing API surface that must keep working — but the rule a client follows is identical for all three: **find the resource's readiness condition; if its status isn't `True`, show the `reason` and `message`.** A client handling multiple kinds uses a small per-kind lookup table to find the condition type, and that is the entire branching logic it needs. No secondary resource fetch, no condition-type archaeology.

### A stable, shared reason vocabulary

The reasons are a fixed vocabulary defined once in the platform API package (`api/v1alpha`) and used consistently across all three resources — so the same situation always produces the same reason, no matter which resource you read it from. A client can branch on the reason; a UI can map it to a badge; an operator can alert on it.

The vocabulary spans the real blocking causes:

| Reason | What it means |
|--------|---------------|
| `ReferencedDataNotReady` | A referenced ConfigMap or Secret isn't available yet — the rollup reason at the WorkloadDeployment and Workload levels for any referenced-data problem. |
| `SourceNotFound` | A referenced ConfigMap or Secret does not exist. **Terminal** — it will not clear on its own. The message names the object, e.g. `ConfigMap "X" not found in namespace "default"`. |
| `SourceUnauthorized` | The referenced source object exists but access was denied. **Terminal.** |
| `SourceTooLarge` | The referenced source object exceeds the allowed size. **Terminal.** |
| `AwaitingPropagation` | A referenced source is valid and is still being propagated to the location running the Instance. **Transient** — expected to clear. |
| `Resolving` | The platform is still reading the referenced source objects. **Transient.** |
| `QuotaNotGranted` | One or more Instances are blocked waiting on quota — the rollup reason at the WorkloadDeployment and Workload levels. |
| `PendingQuota` | An Instance is held pending a quota grant. The message carries the quota constraint (e.g. requested vs. available CPU). |
| `NetworkProvisioning` | The Instance's network binding or subnet is still being provisioned. **Transient.** |
| `NetworkNotFound` | A network referenced by an interface does not exist. **Terminal** — user action required. |
| `NetworkFailedToCreate` | Network creation failed. **Terminal** — needs operator attention. |
| `InstancesProvisioning` | Instances exist but none are ready yet — normal startup. **Transient.** |
| `NoAvailablePlacements` | Last-resort fallback at the Workload level when nothing more specific is known. |

Every message is plain language and names the specific object or constraint at issue — a missing `ConfigMap "X"`, a quota shortfall in a city — rather than restating the reason code.

### Terminal vs. transient

Reasons are classified by whether they will clear on their own. **Transient** causes (`InstancesProvisioning`, `NetworkProvisioning`, `AwaitingPropagation`, `Resolving`) are expected to resolve with time as the platform finishes work. **Terminal** causes (`SourceNotFound`, `SourceTooLarge`, `SourceUnauthorized`, `NetworkNotFound`, `NetworkFailedToCreate`) will not clear without a change by the user or operator — a fixed manifest, a corrected reference, a granted quota. This distinction is what lets a client tell "wait" apart from "act," and it drives which reason the platform chooses to surface when several things are wrong at once.

### The priority rollup

When more than one thing is blocking simultaneously, the **platform** — not the client — decides which reason and message to surface. The guiding principle:

> Permanent, user-actionable causes outrank transient or infrastructure causes; among permanent causes, surface the one the viewer can act on directly.

A genuinely missing ConfigMap (`SourceNotFound`, terminal) outranks "still provisioning" or "awaiting propagation," because the missing ConfigMap will never resolve on its own and a transient blocker on some other reference must not mask it. The platform applies this same ranking as it rolls reasons up the ownership chain:

- **Instance → WorkloadDeployment:** a WorkloadDeployment whose Instances are all blocked reports the worst blocking reason across its fleet (and its own sub-conditions), so its `Available` condition names the real cause — e.g. `ReferencedDataNotReady` with the missing-ConfigMap message verbatim — instead of a generic "no instances ready."
- **WorkloadDeployment → Workload:** a Workload with no available deployment reports the worst deployment's reason and message, instead of collapsing to a bare `NoAvailablePlacements`.

The result: a user looking at the top-level Workload sees the same actionable reason and message that's true at the Instance underneath it. They get the most relevant blocker wherever they look, without drilling down.

This ranking is a platform-internal detail, not part of the API contract. Clients only ever read the winning condition's `reason` and `message`; they never observe the ranking. That means the platform can tune the ordering over time — informed by which blockers actually co-occur in practice — without a breaking change or any client coordination.

### How the CLI surfaces it

Because the answer lives on the resource the user already fetches, the CLI reads it directly. `datumctl compute instances` shows the real reason in its `REASON` column — `SourceNotFound` instead of `SchedulingGatesPresent` — and `instances describe` renders the reason and the message that names the missing object. The CLI needs no special-case code to chase down a referenced-data failure on a secondary resource; it applies one rule — read the readiness condition, and if it isn't `True`, show the reason and message — at every level. A future web console "why isn't this running" panel implements exactly the same single rule.

---

## Backward compatibility

This contract is additive. Nothing existing is removed or renamed.

- The detail conditions stay: `ReferencedDataReady` and `ReplicasReady` on WorkloadDeployment, `QuotaGranted`, `Programmed`, and `Running` on Instance. Clients reading them continue to work unchanged.
- `WorkloadDeployment.Available` and `Workload.Available` gain a richer reason and message when nothing is ready; clients that read only `status == True/False` are unaffected. The existing `NoAvailablePlacements` reason still appears as a fallback.
- `Instance.Ready` gains more specific reasons when scheduling gates are present; the generic `SchedulingGatesPresent` reason still appears when nothing more specific is available.
- The reason vocabulary is purely additive to the API package. Several reasons that were previously inline strings in the controllers simply became named constants with identical values.
- No new fields are added to any status struct and no CRD regeneration is required — the contract rides on the existing `conditions` slice.

Conditions touched by this contract also carry `observedGeneration`, so advanced consumers can detect a stale condition (one reflecting a previous spec) by comparing it against the resource's generation. Ordinary clients do not need this check.

---

## Out of scope

- **A structured `status.blockers[]` field.** A typed array surfacing multiple simultaneous blockers without a priority rollup would be a larger API change (new fields, schema bump, automation migration). The priority-rollup model here delivers the same single-answer client experience without a schema change.
- **Cross-layer correlation IDs.** Stamping a shared trace ID across Instance, WorkloadDeployment, and Workload. The ownership chain already links them for the platform's own rollup.
- **Condition history / timeline.** Recording when a condition last changed reason (beyond the existing `lastTransitionTime`, which only moves when status flips) belongs to a future activity/event API.
