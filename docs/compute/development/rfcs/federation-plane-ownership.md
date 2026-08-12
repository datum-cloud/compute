---
status: proposed
---

# Federation-Plane Ownership

> Tracks [datum-cloud/compute#218](https://github.com/datum-cloud/compute/issues/218), supersedes [#207](https://github.com/datum-cloud/compute/issues/207), and subsumes [#194](https://github.com/datum-cloud/compute/issues/194).

## Table of Contents

- [Summary](#summary)
- [What this means for users](#what-this-means-for-users)
- [The invariant](#the-invariant)
- [Stated dependency: the platform deletion guarantee](#stated-dependency-the-platform-deletion-guarantee)
- [Design](#design)
  - [Hub-local ownership of write-back Instances](#hub-local-ownership-of-write-back-instances)
  - [Owner-gated write-back](#owner-gated-write-back)
  - [A non-bypassable finalizer on the hub WorkloadDeployment](#a-non-bypassable-finalizer-on-the-hub-workloaddeployment)
  - [The projector as a defensive assertion](#the-projector-as-a-defensive-assertion)
- [Rejected alternatives](#rejected-alternatives)
- [Relationship to #194](#relationship-to-194)
- [Rollout](#rollout)
- [Failure modes](#failure-modes)
- [Acceptance criteria](#acceptance-criteria)
- [Test strategy](#test-strategy)
- [Decisions](#decisions)
- [Open questions](#open-questions)

---

## Summary

**Orphaned federation-plane resources are structurally impossible, so no cleanup process is needed.**

Compute's federation plane is an internal implementation detail: a Karmada hub through which a user's `Workload` reaches the edge cells that run it. Users never see it, and it holds no state they authored. What they do see is the consequence of it going wrong — instances that keep running and keep billing after the workload that asked for them is gone, and a control plane that reports itself broken because a handful of unreachable records pin its error ratio at 100%.

This RFC establishes ownership as the reclaim mechanism, and states the invariant that makes a garbage collector unnecessary. Every write-back `Instance` on the hub is owned, in-cluster, by the hub `WorkloadDeployment` it derives from, so the hub's own garbage collector reclaims it. Write-back refuses to create a copy whose owner does not exist, so a new orphan cannot be produced. The hub `WorkloadDeployment` — the only object in the hub tree that nothing on the hub owns — is removed by a finalizer that cannot be skipped. Everything else on the federation plane hangs beneath it.

The `InstanceProjector` deletes nothing. It classifies why it cannot project and, for a state no retry can change, reports the object once and quarantines it: an invariant violation must be loud and diagnosable, but it is a ticket, not an outage, and not a work queue.

## What this means for users

- Deleting a `Workload` stops the machines and stops the billing, and that holds even when a cell is unreachable or a cell-side finalizer never runs.
- Deleting a project takes its compute resources with it, through the same mechanism and with no separate cleanup path.
- A stranded record cannot make the compute control plane page as an outage, so real availability incidents stay legible.

## The invariant

Every compute object on the federation hub is either owned by another hub object, or is the hub `WorkloadDeployment` at the root of that tree. The hub `WorkloadDeployment` is removed by the federator's finalizer on the project-plane original. Therefore no compute object outlives the intent that created it.

Three mechanisms hold that invariant, and they are the whole design:

1. **Hub-local ownership** makes reclamation of everything below the root an ordinary apiserver garbage collection, requiring no controller to be alive and no cross-plane cascade.
2. **Owner-gated write-back** makes an orphan un-creatable: an `Instance` copy without its owner is not a thing the system can produce.
3. **A non-bypassable finalizer** removes the root, which is the only object that could be orphaned, because its original lives in a different cluster and nothing on the hub owns it.

The first two are structural: they hold with no controller running. The third is a control-loop obligation, and it is where the design's risk is concentrated — which is why the bypasses that existed have been closed and why the platform guarantee below is called out explicitly.

## Stated dependency: the platform deletion guarantee

**The whole argument rests on one assumption: the project control plane waits for a project's resources to be deleted before the project itself is deleted.**

That guarantee is what ensures the federator's finalizer always gets a chance to run, and it is what makes a single finalizer sufficient for both cases that matter — a deleted workload while the project lives, and a deleted project. Without it, the deleted-project case would need its own reclaim mechanism, because there would be no surviving object left to finalize.

If that guarantee ever stops holding, mechanism (3) degrades and hub `WorkloadDeployment`s can be stranded — and with them, everything they own and everything they keep propagating to the cells. This is the single named dependency of this design. It belongs in any review of project-deletion semantics on the platform, and a change to it is a change to compute's correctness.

## Design

### Hub-local ownership of write-back Instances

The hub `WorkloadDeployment` and the write-back `Instance` copies derived from it live in the **same namespace of the same cluster**. Cross-cluster ownership is impossible, but this relationship is not cross-cluster: the federated deployment is a first-class hub object with a hub UID, and it is precisely the object whose existence justifies the copies.

The cell's write-back therefore resolves the hub deployment by name — an identifier already carried on every cell `Instance` and stable across all planes — and sets an ordinary in-cluster controller reference from it to the copy. Delete the hub deployment and the hub's garbage collector removes every write-back copy underneath it, in every city, with no cross-plane coordination.

Critically, this does **not** depend on the cell-side finalizer running. That dependence is the failure reported in [#207](https://github.com/datum-cloud/compute/issues/207): a five-link cross-plane cascade ending in a finalizer on a plane that may be unreachable. The cell-side finalizer is retained as the fast, precise path for single-`Instance` deletions — scale-down, replacement — where the deployment legitimately survives. It is no longer load-bearing for correctness.

This does depend on the hub apiserver's garbage collector running with compute's CRDs in discovery. Karmada's control plane ships a `kube-controller-manager` with the GC controller enabled and compute's CRDs are installed on the hub, so it holds in both the e2e topology and production, but it is a dependency on a component compute does not own.

### Owner-gated write-back

Write-back is conditional: **no hub `WorkloadDeployment`, no write-back copy.**

An absent hub deployment is not an error and not a retry. It is the correct steady state for a cell whose deployment has been withdrawn, and write-back simply does not run. This is what makes a new orphan un-creatable, and it makes the ownership rule self-healing: a copy the hub garbage collector removes cannot come back while its owner is gone.

It also makes withdrawn propagation **self-limiting**. A cell being torn down stops regenerating copies rather than racing the teardown, which removes the class of failure where deleting a hub record by hand accomplishes nothing because a live cell recreates it within a reconcile interval.

The cost is that during a normal deletion the hub copies disappear as soon as the hub deployment is removed, slightly ahead of the cell finishing its teardown, so the project loses per-`Instance` status visibility for the last seconds of a delete. The project-side projections are being cascaded away by the same deletion anyway.

### A non-bypassable finalizer on the hub WorkloadDeployment

The hub `WorkloadDeployment` is the only object that can orphan. It is the root of the hub ownership tree, and its original lives in a different cluster, so nothing on the hub owns it. Its removal rests entirely on the federator's finalizer on the project-plane `WorkloadDeployment` — which means that finalizer must not be skippable. A finalizer that releases without doing its work is indistinguishable, from the outside, from work that succeeded.

That matters beyond tidiness. Removing the hub deployment removes the propagation, which stops the cell deployment, which deletes the cell `Instance` through its normal path, which releases the resource claim and actually stops the customer's machine. Reclaiming a hub record without reclaiming the deployment would hide a leak while continuing to bill for it.

Three ways the finalizer could be bypassed are closed:

**Silent release when no federation client is wired.** Whether a deployment federates at all is a startup fact, so "this deployment never federates, nothing to remove" is now distinguishable from "the client that should exist is missing". The first releases; the second holds the finalizer and reports a wiring fault. A misconfiguration surfaces as an error instead of being absorbed as a guaranteed leak.

**Consumer teardown stripping finalizers outright.** Service-deactivation teardown now completes the work each finalizer would have done before releasing it, and any failure aborts teardown so it is retried. `WorkloadDeployment` finalizers are left alone entirely, so the federator's ordinary deletion path runs and removes the hub deployment — which the platform deletion guarantee ensures it gets to do.

**Resolving the hub namespace from an object the finalizer does not own.** The hub namespace is recorded on the project `WorkloadDeployment` at federation time, under the annotation `compute.datumapis.com/federation-namespace`, and it is stamped before anything is written into that namespace. Finalization then reads only the object being finalized. This is defence in depth rather than the closing of a known live hole: it removes a remote read from the finalization path, so finalization cannot wedge on state that has disappeared, and the object at the root of the hub tree cannot be stranded by a lookup failure. Live resolution remains as a fallback for objects federated before the annotation existed.

### The projector as a defensive assertion

The `InstanceProjector` reads hub write-back copies and projects their status into the project. It deletes nothing, and it never decides that an object is garbage. It decides only whether it can project, and classifies why not.

*Retryable* — a legitimately transient state: a project cluster not yet resolvable or not yet engaged, a project `WorkloadDeployment` absent within a creation-ordering grace window, conflicts, transport and API errors. These keep an ordinary error and backoff and count in the error ratio, which is what that signal is for.

*Terminal* — a state no retry can change: an object missing an identity label that is stamped atomically at creation and therefore can never appear later, an undecodable identity, or a project `WorkloadDeployment` proven absent past the grace window. These are reported **once** — an error-level log line, a Kubernetes event, and a latched entry in the `compute_federation_quarantined_objects` gauge — and the object is quarantined so it stops being retried.

Quarantine is a **defensive assertion, not a cleanup mechanism**. Given hub ownership and owner-gated write-back, a hub object the projector can never resolve means one of those invariants has broken. That must be loud and diagnosable. It must not pin the reconcile error ratio at 100% and page as an outage for a condition no retry can clear — in a low-volume control plane, a handful of such objects reads as a total failure of the service. And it must not become a work queue: quarantine is a ticket for a human, not an inbox for a deleting controller.

The verdict is recorded on the object along with a fingerprint of the identity state it was drawn from. If that state changes, the verdict is discarded and the object is evaluated from scratch — an operator who repairs a label gets an immediate retry; an operator who does nothing gets a metric that does not stop telling the truth.

An object nobody can explain is never automatically deleted. **An object nobody can explain is not proven garbage.** It is named once and held in the gauge, and removing it is an operator decision made against evidence the controllers do not have.

## Rejected alternatives

**A federation reaper.** A level-triggered controller that proves hub objects orphaned — by a direct, uncached read returning NotFound for the project registry or the project `WorkloadDeployment` — and deletes them, with observe-only and enforcing modes, a minimum object age, a settle interval across two independent observations, a per-window deletion budget, a per-namespace cap, a circuit breaker and an operator kill switch. **Rejected.**

The reason is the thesis of this RFC. *A cleanup process that must exist forever is an admission that the invariant is not real.* If orphans can accumulate, the ownership model does not hold, and the honest fix is the model, not a process that sweeps up after it.

The reaper also buys permanent risk and permanent cost for coverage of states that ownership plus a non-bypassable finalizer make unreachable. The risk is not incidental: it is a control loop authorised to delete customer workloads, whose safety rests entirely on the correctness of its own proofs of absence — proofs drawn across control planes that can be unreachable, stale, or mid-engagement. Every one of those proofs is a place a bug deletes a running machine. The cost is a controller, its guardrails, its metrics, its alerts and its runbook, maintained forever.

The guardrail machinery it needed was itself the evidence. A mechanism that requires a mandatory observe-only soak, a deletion budget and a circuit breaker before anyone will let it run is a mechanism nobody trusts, protecting against a class of object that ownership prevents from existing.

**Milo's anchor ConfigMap.** Rejected. The anchor still has to be deleted by something — the same project-side finalizer whose non-execution is the concern — so it removes cascade hops but survives none of the failure modes that strand objects. Anchor deletion also resolves its namespace through an upstream lookup, so it cannot clean up once the project is gone. And for the cell's write-back to own a copy by anchor it would need the project-plane deployment UID, which the cell does not have; the only cross-plane UID available is the `Workload`'s, and a `Workload` spans many deployments and cities, so its anchor would not clear when one deployment is withdrawn. Hub-local ownership wins on every axis: a real owner reference rather than a proxy object, no UID translation, no extra object per owner, and no dependence on a finalizer running.

**Owning the write-back copy by the federation namespace.** Rejected. The namespace is per project namespace, not per deployment, so it only cascades when an entire project namespace goes away. The hub `WorkloadDeployment` is the correct granularity and is already created and deleted by the same controller.

**Reconciling hub copies against live cell Instances.** Rejected. It requires the management plane to read every cell, turning cell unreachability — the exact condition that strands objects — into an ambiguity that must be treated as "keep", which is precisely when the mechanism would be needed.

**TTL or lease on write-back copies.** Rejected. A lease deletes on writer silence, which makes a transient cell outage indistinguishable from a decommission and can reclaim records for machines that are still running. It also cannot reclaim the hub deployment, so it does not address the root of the tree at all.

## Relationship to #194

[#194](https://github.com/datum-cloud/compute/issues/194) is the project-deletion case. Under this design it is not a separate problem with a separate mechanism: it is the same invariant, covered by the same mechanism.

When a project is deleted, its control plane waits for the project's resources to be deleted first. The project `WorkloadDeployment` is therefore deleted while the federator can still act on it, its finalizer removes the hub `WorkloadDeployment`, and hub ownership reclaims every write-back `Instance` beneath it. There is nothing left to collect and nothing to prove — which is why the mechanism that #194 assumed it needed is not built.

The empty leftover hub namespace (`ns-<project-namespace-uid>`) is explicitly **out of scope**. It is cosmetic: it holds no workload state, costs nothing, and cannot regenerate anything. The namespace convention belongs to the networking/federation layer that creates and shares it, and reclaiming it is that layer's decision, not compute's.

[#207](https://github.com/datum-cloud/compute/issues/207) is superseded in full: hub-local ownership satisfies its requirement that reclamation not depend on the cell-side finalizer, and quarantine satisfies its requirement that any unreclaimable state be observable.

## Rollout

There is no deleting controller, so there is no phased enablement and no soak. This is a straight deploy.

- **Ownership and owner-gating take effect as objects are next reconciled.** New write-back copies are created owned; the invariant holds for them from the first write.
- **Pre-existing unowned copies are adopted as an ordinary side effect of the normal write-back update path** — the same reconcile that keeps a copy's spec and labels current stamps the owner reference if it is missing. No migration job, no adoption pass, no separate mode. A copy already controlled by something else is left alone.
- **The finalizer changes take effect immediately** and are behaviour-preserving in the healthy case; they change outcomes only in the cases that were silently leaking.

**What an operator should watch:** `compute_federation_quarantined_objects`. It is a latching gauge, so it stays non-zero for as long as a quarantined object exists, and its reason label distinguishes the classes.

**A non-zero value means an invariant has broken** — a hub object exists that ownership and owner-gated write-back say should not. It is a ticket, not a page: nothing is degrading for users while it sits there, and no retry will clear it. The reason label points at which invariant: a missing or undecodable identity label means the write-back path stamped an object it should not have been able to stamp, which is a compute bug worth fixing; a project deployment absent past the grace window means a hub object outlived its project original, which means ownership or the finalizer did not do its job on that object and the surrounding objects need inspecting. The gauge should be alerted at ticket severity and should normally read zero.

**Pre-existing production leftovers from before this change are handled as a one-off manual deletion, not by code.** They are a small, finite, known set. The design's job is that no new ones can be created; writing and maintaining a controller to clean up a bounded historical set would be the reaper by another name.

## Failure modes

**Hub garbage collector disabled or lagging.** Write-back copies are not reclaimed when their deployment is removed. Owner-gated write-back means they are not regenerated, so this is a stalled reclaim rather than a growing leak, and the projector quarantines the affected objects, which surfaces it in the gauge.

**The platform deletion guarantee stops holding.** A project could be deleted without its `WorkloadDeployment`s being finalized, stranding hub deployments and everything they propagate. This is the design's one named dependency; it is stated above rather than mitigated here, because the correct response is to restore the guarantee, not to build a sweeper.

**A federation client is missing where federation is configured.** Both finalizers hold rather than release, and the deletion visibly does not complete. This is deliberate: a stuck deletion is diagnosable, a silent leak is not.

**Hub deployment removed while a cell is unreachable.** Karmada cannot un-propagate, so the cell keeps running the machine until it returns. This is pre-existing behaviour and out of scope; the hub objects are reclaimed correctly and the cell converges when it reconnects.

**Quarantine hides a real bug.** It cannot hide silently: the gauge latches for the object's lifetime and is alertable, and the reason label separates a compute stamping bug from an object only a human can explain.

## Acceptance criteria

- Deleting a `Workload` leaves no `WorkloadDeployment` and no `Instance` on the federation plane, and this holds when the cell-side finalizer never runs.
- A hub write-back `Instance` is never created while the hub `WorkloadDeployment` that would own it is absent, and a deleted hub copy is not recreated under those conditions.
- Every hub write-back `Instance` carries a controller reference to its hub `WorkloadDeployment`, including copies that predate this design.
- Neither finalizer releases without doing its work when federation is configured, and consumer teardown does not release a finalizer whose work it has not completed.
- Finalization of a project `WorkloadDeployment` succeeds without reading any object other than the one being finalized, for any deployment federated after this change.
- Deleting a project leaves no federation-plane compute objects behind, apart from the out-of-scope empty namespace.
- No federation-plane object produces unbounded reconcile errors; an unresolvable object cannot pin the projector's error ratio or page as an outage.
- Every unresolvable object is observable in a latching metric with a reason and named once in the log and an event.
- No component deletes a hub object on an inference about absence.

## Test strategy

**Unit.** The projector's terminal-versus-retryable classification, table-driven across every combination of identity labels, cluster resolvability, project deployment presence and object age, with the default for any unenumerated combination being *retryable*. Separately: quarantine fingerprint invalidation, the owner-gated write-back decision, owner-reference adoption versus leaving a foreign controller alone, and each finalizer's behaviour with and without a configured federation client.

**Chainsaw e2e**, on the Karmada hub plus two member cells, alongside the existing deletion-cascade, instance-writeback and full-federation suites:

- *Ownership on the ordinary path* — federate, and assert both that the write-back copies carry a controller reference to the hub deployment and that the hub namespace is recorded on the project deployment.
- *Hub reclaims its own copies* — delete the hub deployment alone and assert the copies are collected by the hub, with no compute controller asked to do it.
- *Orphans are un-creatable* — with the owner gone but the cell Instance still live and reconciling, assert no copy is manufactured.
- *Cell-side finalizer never runs* (the reproduction for [#207](https://github.com/datum-cloud/compute/issues/207)) — stop the cell operator, delete the deployment, and assert nothing remains on any plane.
- *No wedge on finalization* — delete the project namespace and assert it leaves Terminating with the hub copy removed, which is the observable form of the finalizer releasing only after its work is done.

Two things the harness cannot express, and which therefore rest on unit tests alone. Consumer teardown does not run in the Kind topology, because there is no Milo control plane and the deployment runs in single-cluster discovery mode, so the teardown path has no end-to-end coverage. And a genuinely vanished project namespace is not constructible in a single cluster, since a namespace cannot finish terminating while an object inside it holds a finalizer — which is why the recorded hub namespace is framed as defence in depth rather than as the fix for an observable failure.

## Decisions

- **Orphaned federation-plane resources are structurally impossible.** The design's job is the invariant, not a process that compensates for its absence.
- **The hub `WorkloadDeployment` owns the write-back `Instance`s in its hub namespace** via an ordinary in-cluster controller reference.
- **Write-back is owner-gated**: no hub deployment, no copy. This is what makes a new orphan un-creatable and makes withdrawn propagation self-limiting.
- **The federator's finalizer is the sole removal path for the hub `WorkloadDeployment`, and it is not skippable.** Configuration distinguishes "never federates" from "misconfigured"; teardown honours finalizers instead of stripping them; finalization reads only the object being finalized.
- **The design depends on the platform guarantee that a project's resources are deleted before the project is.** This is stated as an assumption, not engineered around.
- **The projector never garbage-collects.** It classifies and quarantines; quarantine is a defensive assertion that an invariant has broken.
- **A federation reaper is rejected.** A permanent cleanup process is an admission that the invariant is not real, and it trades permanent risk and cost for states ownership makes unreachable.
- **Pre-existing leftovers are a one-off manual deletion.** No code ships to clean up a bounded historical set.
- **The leftover empty hub namespace is out of scope**, and belongs to the layer that owns the namespace convention.

## Open questions

1. Confirm the platform deletion guarantee is contractual rather than incidental, and that it is covered by tests on the project control plane. This design's correctness is downstream of it.
2. Confirm the production Karmada hub runs its garbage collector with compute CRDs in discovery. It holds in the e2e topology; production should be verified rather than assumed.
3. The projection grace period is currently sized by intuition. It should be set from measured project-deployment creation latency in staging, since it is the boundary between a retry and a quarantine verdict.
4. Whether quarantine state should also surface as a status condition on the hub object, which is queryable and survives restarts, in addition to the annotation, event and gauge.
