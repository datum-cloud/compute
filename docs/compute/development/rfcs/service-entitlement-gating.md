---
status: proposed
---

# Gate Project Engagement on Service Entitlement

> Tracks [datum-cloud/compute#165](https://github.com/datum-cloud/compute/issues/165).

## Table of Contents

- [Summary](#summary)
- [Problem](#problem)
- [Design](#design)
  - [Entitlement signal](#entitlement-signal)
  - [Library adoption](#library-adoption)
  - [Teardown](#teardown)
  - [Prerequisite refactors](#prerequisite-refactors)
  - [Rollout](#rollout)
- [Alternatives](#alternatives)
- [Failure modes](#failure-modes)
- [What gets built](#what-gets-built)
- [Decisions](#decisions)
- [Open questions](#open-questions)

---

## Summary

Compute today engages every project on the platform the moment it becomes ready, regardless of whether anyone in that project has enabled Compute. This RFC gates engagement on an active Milo service entitlement by adopting the consumer multicluster provider library from [milo-os/service-catalog#38](https://github.com/milo-os/service-catalog/pull/38). A project is engaged only while it holds an active `ServiceConsumer` for the Compute service; when that entitlement is revoked, Compute stops reconciling the project and cleans up what it projected into it.

## Problem

The Compute controller gates project engagement on a single condition: the project's `Ready` status. Every ready project gets a live watch connection, an in-memory cache, and a set of running reconciler goroutines — whether or not the project owner ever enabled Compute. Compute's resource footprint therefore scales with the size of the whole platform rather than with actual usage, and "enabling Compute" has no effect on whether the controller engages a project.

## Design

### Entitlement signal

The signal is `ServiceConsumer.status.phase == "Active"` with no `DeletionTimestamp`, on a `ServiceConsumer` object in the **Compute service provider project**. A single watch on that project covers all consumers.

| Phase             | Action                       |
| ----------------- | ---------------------------- |
| `PendingApproval` | Neither engage nor tear down |
| `Active`          | Engage the project           |
| `Denied`          | Tear down and disengage      |

A project also enters the revoked set when its `ServiceConsumer` has a non-zero `DeletionTimestamp`. This covers revocations that happen while the operator is down — on restart the active and revoked sets are re-derived from live objects, so nothing leaks regardless of what was missed.

### Library adoption

[milo-os/service-catalog#38](https://github.com/milo-os/service-catalog/pull/38) provides a `consumer.Provider` that watches `ServiceConsumer` objects in a provider project and engages or disengages consumer projects as entitlement state changes. Compute is the intended first adopter.

Adoption replaces the existing all-projects discovery provider with the consumer provider, gated behind a nil-able `ConsumerScopedProjection` block in `DiscoveryConfig`. When the block is absent the operator behaves exactly as today; when present it restricts engagement to entitled projects. Existing per-project reconcilers are registered on the consumer-scoped manager unchanged.

```yaml
discovery:
  mode: milo
  consumerScopedProjection:        # nil by default
    providerProject: <compute-provider-project>
    serviceNames:
      - compute.datumapis.com
```

All resources Compute projects into consumer projects must carry the label `services.miloapis.com/service-name=compute.datumapis.com` — the library scopes its deactivation cleanup to this label.

### Teardown

When entitlement is revoked the library cancels the per-cluster context, then runs registered `Teardown` implementations via a fresh direct client. The context passed to `TeardownConsumer` is the live provider context, not the cancelled per-cluster one, so the Teardown can do real work after the per-project reconcilers have stopped.

Compute's Teardown handles the external cleanup that a label-scoped delete cannot express: releasing the `ResourceClaim` backing each `Instance` and deleting downstream write-back instances. Once external cleanup is done, it strips finalizers so objects are fully removed. The library retries the Teardown with backoff until it succeeds.

### Prerequisites

Two changes would need to land before the Teardown approach works cleanly:

**Add `WorkloadDeployment` → `Instance` owner references.** WDs and Instances are currently linked only by label; the WD finalizer manually cascades deletion to compensate. Owner references let Kubernetes GC handle the cascade, removing the need for explicit delete ordering in Teardown and the manual cascade logic from the WD finalizer.

**Decouple `WorkloadDeploymentReconciler.Finalize` from the multicluster context.** `Finalize` currently derives its client from the per-cluster context, which is cancelled before Teardown runs. It needs to accept a `client.Client` directly — the same pattern `InstanceReconciler.reconcileDeletion` already follows.

### Rollout

The `ConsumerScopedProjection` block is nil by default; existing environments are unaffected until it is set. Enabling in an existing environment requires a one-time backfill of `ServiceConsumer` objects for all projects currently running Compute workloads — enabling without this immediately disengages all active projects.

## Alternatives

**Filter by a label stamped on `Project` objects.** The service catalog controller could stamp a label when a `ServiceConsumer` becomes active, and the existing `milomulticluster` provider's label selector could filter on it. Rejected — creates a cross-system derived label that must be kept in sync; watching `ServiceConsumer` directly is more explicit and authoritative.

**Drain before cancel.** Keep the per-cluster context alive while WDs and Instances are deleted via the existing finalizer cascade, then disengage once the project is empty. Avoids the need for a `Teardown` implementation entirely, but requires Compute to own a draining state machine and prevent the library's automatic disengage from racing the drain. Rejected — with the prerequisite refactors in place the `Teardown` path is straightforward, and the library's `Teardown` interface explicitly targets this case.

## Failure modes

**Permanent Teardown failure.** The library retries the Teardown indefinitely with backoff. The only realistic permanent failure is unresolvable project identity — the labels needed to locate a project's `ResourceClaim` are missing or malformed, meaning no retry will ever succeed. Compute's Teardown must handle this the same way `reconcileDeletion` handles `errProjectIdentityUnresolvable` today: log at error, skip the cleanup, and remove the finalizer so the object is not permanently stuck.

**Operator restart mid-teardown.** Safe. On restart the active and revoked sets are re-derived from live `ServiceConsumer` objects, so a project whose consumer is `Denied` or deleting is picked up again and `disengage` runs with a fresh direct client. Teardown idempotency handles any partial work from before the restart.

## Decisions

- **Signal**: `ServiceConsumer.status.phase == "Active"` in the Compute provider project, not `ServiceEntitlement` on the consumer side.
- **Library**: adopt `milo-os/service-catalog` consumer multicluster provider; do not reimplement.
- **Teardown**: use the library's `Teardown` interface with a direct client; clean up external resources imperatively rather than through the reconciler loop.
- **Cascade**: add WD→Instance owner references to eliminate manual delete ordering in Teardown and resolve existing finalizer debt.
- **Gate**: nil-able config block; absent = unchanged behavior.
- **Revocation semantics**: revocation is a hard cutoff — no drain period. Operators should treat entitlement revocation as equivalent to force-deleting all workloads in the project.

## Open questions

1. **Backfill for existing projects.** Compute is pre-GA; a manual backfill at enable time should be straightforward if needed.
