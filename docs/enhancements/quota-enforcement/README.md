---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

<!--
TODO (datum-cloud/enhancements process — not yet done):
- Open a tracking issue in datum-cloud/enhancements and link it here. The doc's
  final location (area directory + short title) is decided in that issue.
- If this lands in the enhancements repo, place it under the compute area, e.g.
  `enhancements/compute/quota-enforcement/README.md`.
- Assign the PR to sponsoring approvers before moving status -> implementable.

Status note: management-plane quota enforcement (writes + a live grant-watch) is already
IMPLEMENTED. The proposed change is the #171 fix — wiring the live grant-watch only where
the quota API is served locally (management-plane mode) and observing grants via the
controller's reconcile requeue everywhere else. Quota is ENFORCED in all modes; only
grant-observation latency differs. Overall status kept `provisional` until approved.
-->

# Quota Enforcement Across Deployment Modes

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Writes are remote API calls, available in every mode](#writes-are-remote-api-calls-available-in-every-mode)
  - [Grant observation: live watch where served, requeue everywhere](#grant-observation-live-watch-where-served-requeue-everywhere)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
  - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
  - [Monitoring Requirements](#monitoring-requirements)
  - [Dependencies](#dependencies)
  - [Scalability](#scalability)
  - [Troubleshooting](#troubleshooting)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)

## Summary

Quota enforcement works across **all** deployment modes — the management plane, edge
cells, and flat single-tenant clusters. An Instance's quota claim is created in the Milo
project control plane that owns the project's quota ledger, and the Instance stays gated
until that claim is granted, wherever the manager runs. Claiming is a remote API call, so
it needs nothing locally.

The problem this enhancement fixes is that enabling quota in a **single/cluster-mode**
deployment crashed compute-manager on startup: it tried to register a live watch for
ResourceClaims on a local cluster that doesn't serve the quota API. The fix wires the live
grant-watch only where the quota API is served locally (management-plane mode); everywhere
else grants are observed through the controller's normal reconcile requeue instead. Writes
are unaffected, enforcement stays real in every mode, and the crash cannot occur.

## Motivation

Quota is how the platform bounds what customers consume, and it must hold wherever compute
runs — not just in the management plane. It already does, because claiming is a remote
call into the owning project's quota control plane. The only thing standing in the way was
a startup crash: pointing a single/cluster-mode deployment at quota made compute-manager
try to watch a resource its local cluster can't serve, and it exited ([#171]). The goal is
to keep enforcement working everywhere while making that crash impossible.

[#171]: https://github.com/datum-cloud/compute/issues/171

### Goals

- Quota enforcement works in every deployment mode: an Instance is gated until its claim
  is granted, whether it runs in the management plane, an edge cell, or a flat
  single-tenant cluster.
- Enabling quota outside the management plane must not crash compute-manager (#171).
- Grant observation is timely where a live watch is available, and correct (if slower)
  where it isn't.

### Non-Goals

- **A live grant-watch in single/cluster mode** — deferred; a per-project-control-plane
  watch/forwarder is the future option (see [Alternatives](#alternatives)).
- **Local (co-located) quota for flat single-tenant** — out of scope; those deployments
  claim against the owning project's remote control plane like any other.
- WorkloadDeployment-level quota (this covers Instance-level claims only).
- Changes to the quota evaluator itself; this produces claims and observes grants, it does
  not decide them.

## Proposal

Quota **writes** are remote calls into the owning project's quota control plane, so they
work identically in every mode. What differs is how a **grant** is observed: a live watch
where the quota API is served locally, and the controller's reconcile requeue elsewhere.
Both modes enforce — an Instance stays gated until its claim is granted.

| Deployment mode | How claims are written | How grants are observed | Enforced? |
|---|---|---|---|
| **Management plane (milo)** | Remote call to the owning project control plane | Live watch on ResourceClaims | Yes |
| **Single / cluster** (edge cells, flat single-tenant) | Remote call to the owning project control plane | Controller reconcile requeue (backing-off re-check) — no local watch | Yes |

### User Stories

- As an operator, I enable quota in the management plane; customer Instances are gated on
  grants, and the gate clears the instant a grant lands.
- As an operator, I stand up compute-manager on a single/cluster-mode deployment
  (edge cell or flat single-tenant) with quota configured; it starts cleanly, Instances
  are gated on grants from their projects' quota ledgers, and the gate clears on the next
  reconcile re-check.

### Notes/Constraints/Caveats

- **Project identity in single/cluster mode** is resolved from the Instance's namespace
  mapping (the existing mechanism); writes target that project's quota control plane. No
  fixed-project or local-quota machinery is involved.
- Grant observation in single/cluster mode is bounded by the reconcile requeue cadence
  rather than a live event, so a grant is noticed within the re-check interval, not
  instantly.

### Risks and Mitigations

- **Grant latency in single/cluster mode.** Without a live watch, a grant is picked up on
  the reconcile requeue (a backing-off re-check), not the moment it lands. Correctness is
  unaffected — the Instance stays gated until granted — only the time-to-notice differs.
  A live grant-watch is the deferred [alternative](#alternatives) if that latency ever
  matters.
- **Reaching the owning ledger.** If a deployment can't reach a project's quota control
  plane, that Instance's claim can't be written or checked and it stays gated until access
  is restored — correct (never a bypass), surfaced as recurring reconcile errors.

## Design Details

### Writes are remote API calls, available in every mode

Claiming quota is a REST call — create/get/delete a ResourceClaim in the owning project's
quota control plane. It needs no local quota API, so it behaves the same in
management-plane and single/cluster modes. The owning project is resolved from the
Instance's namespace mapping, and the claim is both written to and read back from that
same project ledger, so a grant is always looked for where it was recorded.

### Grant observation: live watch where served, requeue everywhere

The one decision: the **live ResourceClaim watch is wired only where the quota API is
served to the manager locally** — management-plane (milo) mode. That watch delivers grant
events promptly. In single/cluster mode there is no local quota API to watch, so the watch
is simply not registered — which is exactly the #171 crash cause removed — and grants are
observed through the controller's reconcile requeue instead. The requeue is the universal
floor (every mode re-checks pending claims on a backing-off schedule) and the sole
grant-observation mechanism where no watch is wired.

## Production Readiness Review Questionnaire

<!-- Trimmed to the subsections relevant at alpha, following the precedent of other
compute enhancements. Beta-targeted subsections (rollout/upgrade/rollback planning) are
deferred. -->

### Feature Enablement and Rollback

- [ ] Feature gate
- [x] Other
  - Enforcement is enabled by configuring quota (a credential for the owning project
    control planes); it then applies in every mode. There is no global gate.
  - Enabling/disabling takes effect on compute-manager restart with the changed
    configuration; no control-plane downtime.
  - Rollback is removing the quota configuration; enforcement stops.

### Monitoring Requirements

- [x] API .status
  - The Instance's quota-granted status condition shows whether an Instance is gated or
    granted, in every mode. A persistently ungated Instance indicates a pending grant or a
    ledger the deployment can't reach.

### Dependencies

- Milo quota evaluator and the per-project quota control planes it serves — the authority
  that grants or denies claims, and the target of every claim write and read. If a
  deployment can't reach them, claims stay pending and Instances stay gated (no bypass).

### Scalability

- Writes add per-Instance claim calls to the owning project control planes (present
  already). Single/cluster mode registers no ResourceClaim watch, so it adds no per-project
  watch load; grant re-checks ride the existing reconcile requeue.

### Troubleshooting

- **compute-manager crashes on startup with quota configured in single/cluster mode:**
  that is #171; with this change the live watch isn't wired in those modes, so it is never
  registered and the crash cannot occur — grants come from the requeue instead.
- **Instances stuck ungated:** confirm the deployment can reach the owning project control
  planes and that those projects are entitled, so claims are granted rather than left
  pending.

## Implementation History

- Management-plane claim routing and the live watch on project control planes:
  **implemented** (verified 2026-05-26).
- The #171 fix — wiring the live grant-watch only where the quota API is served locally and
  observing grants via the reconcile requeue elsewhere: **proposed** here.

## Drawbacks

- In single/cluster mode a grant is noticed on the reconcile requeue cadence rather than
  instantly — a grant-latency cost relative to the management plane's live watch.
  Enforcement is unaffected; only responsiveness differs.

## Alternatives

- **A live grant-watch in single/cluster mode via a per-project-control-plane watch or
  forwarder** — *deferred*. It would deliver grants promptly there too, but needs one watch
  per project a deployment serves (per-project fan-out), and the reconcile requeue already
  keeps enforcement correct. Revisit if grant latency in those modes ever becomes a product
  concern.
- **Local co-located quota for flat single-tenant** — rejected; claims target the owning
  project's control plane like every other mode, so no local quota ledger is needed.

## Infrastructure Needed

- None beyond the shipped management-plane setup. Enforcement in single/cluster mode reuses
  the same remote claim writes and the existing reconcile requeue; a future live grant-watch
  there (see [Alternatives](#alternatives)) would bring its own platform-side access needs,
  tracked then.
