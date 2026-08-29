---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

<!--
TODO (datum-cloud/enhancements process — not yet done):
- Open a tracking issue in datum-cloud/enhancements and link it here.
- If this lands in the enhancements repo, place it under the compute area, e.g.
  `enhancements/compute/runtime-classes/README.md`.
- Assign the PR to sponsoring approvers before moving status -> implementable.
-->

# Runtime Classes

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [What a runtime class promises](#what-a-runtime-class-promises)
  - [The initial classes](#the-initial-classes)
  - [User Stories](#user-stories)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [A platform-owned catalog, not a customer-defined one](#a-platform-owned-catalog-not-a-customer-defined-one)
  - [Cells advertise what they can serve](#cells-advertise-what-they-can-serve)
  - [A dimension for placement, quota, and price](#a-dimension-for-placement-quota-and-price)
  - [Where the platform ends and a provider begins](#where-the-platform-ends-and-a-provider-begins)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)

## Summary

Datum's compute API already lets a customer say **what shape** their workload is — a
sandbox of containers, or a virtual machine booting their own image. It does not let them
say **how that workload should be run**: what isolation boundary surrounds it, how much of
Linux it can use, how fast it starts, and what that costs. Today there is exactly one
answer per shape, whatever the cell's provider happens to run, and the customer discovers
its limits by hitting them.

This enhancement introduces the **runtime class** — a small, named, provider-independent
set of execution tiers that a customer selects on a workload. Each class is a published
promise about isolation, compatibility, startup latency, and price. The platform decides
which providers and which cells can honor each class, places workloads accordingly, and
meters and prices them by class.

The immediate product outcome is that Datum can offer a millisecond-startup unikernel fast
path *and* a general-purpose "your container image just runs" tier as deliberate choices,
instead of shipping one runtime and describing its limits as caveats.

## Motivation

**Customers hit compatibility walls they can't route around.** Datum's fast path runs
unikernels, which require a position-independent binary and hold the root filesystem in
RAM. Plenty of ordinary container images don't satisfy that. Today the platform's only
answer is "that image doesn't work here." With runtime classes the answer becomes "that
image runs in the general-purpose class, at this price and this startup latency" — a
product surface instead of a dead end.

**Isolation is a promise, not an implementation detail.** Multi-tenant customers ask what
separates their workload from their neighbor's. That answer has to be stated in the API
and held stable across providers, so it can be sold, audited, and relied on. A runtime
class is where that promise lives.

**Price and performance are a real trade the customer should make.** Millisecond starts
and low per-instance overhead are worth paying for on bursty, short-lived work, and worth
nothing on a long-running service that needs full Linux. Only the customer knows which
they have.

**Placement needs a shared vocabulary.** Not every cell will run every runtime, and cell
hardware constrains what's possible — some execution options exist only on x86, and edge
sites are not uniform. Federated scheduling already matches workloads to locations; it
needs a declared runtime vocabulary to match on as well, and a clear way to say "no cell
can serve this" rather than leaving a workload silently unplaced.

**The market has already converged on tiered isolation.** Every major provider offers more
than one isolation tier, and the emerging Kubernetes sandbox-workload ecosystem models the
choice at exactly this level. Offering one runtime is the outlier position.

### Goals

- A customer-visible, provider-independent vocabulary for how a workload is executed,
  stable enough to price and to build a roadmap on.
- A workload lands only on cells that can honor its runtime class, and says clearly when
  none can.
- Runtime class is a first-class dimension for quota, metering, and pricing.
- Existing workloads keep running unchanged, on an explicit default class.
- New execution tiers can be added later without a breaking API change.

### Non-Goals

- Choosing the specific technology behind any class (hypervisor, sandbox runtime, VMM).
  That is a separate technical decision, informed by the runtime research this enhancement
  builds on, and deliberately not customer-visible.
- Exposing hypervisor or runtime tuning knobs to customers.
- Selecting a different runtime per container within a single workload.
- Delivering snapshotting, sub-second resume, or scale-to-zero. Those are tracked
  separately; runtime classes are what will eventually let the platform say *which* tier
  offers them.
- Letting customers define their own runtime classes.

## Proposal

A **runtime class** is a named execution tier that a customer selects on a workload. The
platform publishes a fixed, small catalog of them. Each entry carries a documented
contract, and the platform is accountable for honoring it on every cell that advertises
the class.

### What a runtime class promises

Each class documents four things, in customer language:

- **Isolation** — what boundary separates this workload from other tenants.
- **Compatibility** — what will run unmodified, and what won't. This is the honest,
  unglamorous part, and the one customers most need before they commit.
- **Startup and lifecycle** — expected cold-start behavior, and which lifecycle
  capabilities (suspend, resume, snapshot) exist in this tier.
- **Cost shape** — the per-instance overhead the tier carries and how it is priced.

A class that cannot state all four honestly is not ready to ship.

### The initial classes

Two classes, matching the two things the product already needs to say:

- **A unikernel fast path** — today's behavior. Very low startup latency and very low
  per-instance overhead, in exchange for narrow image compatibility. Best for bursty,
  short-lived, purpose-built workloads.
- **A general-purpose class** — arbitrary Linux container images, isolated at a stronger
  boundary than a shared kernel. Slower to start and more expensive per instance, but
  "your image just runs."

The classes are additive. A third, cheaper-but-more-constrained tier is plausible later,
and the model is designed so adding it is a catalog change rather than an API change.

### User Stories

**A customer whose image won't boot on the fast path.** Today they get a failure that
reads as a platform bug. With runtime classes, the platform tells them the image isn't
compatible with the fast path and names the class that will run it, with its price and
startup characteristics. They change one field and deploy.

**A customer running untrusted or customer-supplied code.** They need to state, to their
own auditors, what separates their tenants' work. They select the class whose documented
isolation boundary meets that bar, and the platform holds that promise on every cell it
places them on.

**A customer optimizing spend.** They have a bursty workload and a steady one. They put
the bursty one on the fast path for near-instant starts and low idle cost, and the steady
one on the general-purpose class, and see the difference itemized on their bill.

**An operator bringing up a new cell.** They declare which runtime classes that cell can
serve. Placement immediately respects it — no workload is scheduled somewhere that can't
honor its class, and capacity for each class is visible per site.

### Notes/Constraints/Caveats

- **The product surface is the shape of the workload; the runtime class is how it's
  executed.** These are independent axes and should stay that way. Not every combination
  has to be offered, but the vocabulary shouldn't collapse them.
- **Contracts must be honest about what a tier does not do.** In particular, the
  capabilities the roadmap leans on hardest — snapshot, fast resume, scale-to-zero — are
  not automatically available in a general-purpose isolation tier. Promising them
  class-wide before they're proven would be the most expensive mistake available here.
- **Hardware and architecture narrow the menu.** Some execution options are x86-only in
  practice. The class catalog must reflect what can actually be served across Datum's
  footprint, not what is theoretically supported.
- **Density economics differ per class**, and some of the obvious levers for packing more
  instances onto a host are unavailable in a multi-tenant setting for sound security
  reasons. Per-class pricing has to be grounded in measured per-instance overhead on real
  hardware, not vendor headline numbers.
- **Every additional class is a permanent operational commitment** — patching, upgrades,
  capacity, on-call, and its own security posture. Two classes is a deliberate ceiling for
  the first milestone.

### Risks and Mitigations

- **Fragmenting the product.** Too many classes, or classes that differ in ways customers
  can't reason about, makes compute harder to buy. Mitigation: keep the catalog small,
  define each class by the customer-visible contract rather than by its technology, and
  require a distinct use case before adding one.
- **Capacity islands.** A class that only some cells serve creates places where a workload
  can't be scheduled. Mitigation: make class-by-location availability visible before
  deploy, report an unplaceable workload explicitly rather than leaving it pending, and
  track per-class capacity as a launch metric.
- **A tenant influencing its own runtime.** Stronger isolation is only worth what its
  weakest configuration path is worth; runtime configuration influenced by tenant-supplied
  input is a known and repeatedly-exploited class of escape. Mitigation: runtime
  configuration is platform-owned end to end, and tenant-supplied workload metadata never
  reaches runtime configuration. This gets an explicit security review.
- **Mispricing.** A tier with high fixed per-instance overhead can be sold at a loss.
  Mitigation: measure per-instance overhead on Datum's own hardware before the class is
  offered, and treat that measurement as a prerequisite for GA of each class.
- **Changing what the default means.** Silently moving existing workloads to a different
  tier would change their cost and behavior. Mitigation: the default is pinned to today's
  behavior, and any change to it is an announced migration.

## Design Details

### A platform-owned catalog, not a customer-defined one

The set of runtime classes is published by Datum, versioned, and documented. Customers
select from it by name; they cannot define classes or reach the machinery underneath. This
is what makes the class a durable promise rather than a leak of whatever the cell happens
to run, and it lets the implementation behind a class change without a customer-visible
API change — as long as the published contract still holds.

### Cells advertise what they can serve

A cell declares the runtime classes it can honor. Placement treats that declaration as a
hard constraint, intersected with the location constraints federated scheduling already
applies. A workload whose class no eligible cell can serve is reported as such, with a
reason a customer can act on, rather than waiting indefinitely.

### A dimension for placement, quota, and price

Runtime class joins location and instance type as an axis the platform accounts for.
Quota can be granted per class, so a customer can be entitled to the general-purpose tier
independently of the fast path. Metering and billing carry the class, so per-tier cost is
visible on the bill and per-tier margin is visible internally. Status and observability
report it, so both customers and operators can see what is running where and in what tier.

### Where the platform ends and a provider begins

Adding a second class means a second thing that turns an instance into something
running. The line between what the platform owns and what each provider owns has to be
drawn deliberately, or the two classes drift into two dialects: different words for the
same failure, different sizing for the same instance type, different silent gaps.

**The platform owns the contract.** The class catalog and its published promises, the
instance-type sizing every class must honor, the customer-facing vocabulary for instance
status and failure reasons, and the translation from an instance's declared spec into a
runnable description of it. These are the things a customer experiences identically no
matter which class they chose, so they are defined once, centrally, and shared.

**A provider owns realization.** How its runtime is targeted and configured, its
runtime-specific plumbing, its lifecycle and cleanup, and advertising the capacity it can
serve. Providers stay separately deployed and separately released: a fault or a bad
rollout in one class must not be able to take another class down with it, and a runtime
whose contract changes on a vendor's schedule should not be able to churn the platform's
core.

Deliberately, an instance is *not* required to be realized the same way in every class —
one class may run its instances as containers on a host, another may provision a virtual
machine from a cloud provider. The platform's abstraction is the instance, not any
particular realization of it, and this proposal does not narrow that.

**Capability gaps are validated, not silently dropped.** A class will not support
everything the instance API can express — some won't support disk-backed volumes, some
will constrain where images may be pulled from. Today an unsupported feature can be
quietly skipped while the instance otherwise starts, which is tolerable when there is one
runtime and its limits are documented. Once a class publishes a contract, silently
ignoring part of a customer's request is a violation of it. Unsupported combinations are
rejected when the workload is submitted, naming the class and the unsupported feature, so
the customer learns at apply time rather than from behavior that doesn't match what they
asked for.

## Production Readiness Review Questionnaire

<!-- Completed for alpha; beta/GA sections filled in as the feature matures. -->

### Feature Enablement and Rollback

- [x] Feature gate
  - Components depending on the gate: compute control plane, providers, placement.

Enabling the feature does not change behavior for existing workloads: any workload that
does not select a class gets the default class, which is today's runtime. Disabling it
returns the platform to a single runtime; workloads that had selected a non-default class
would need to be moved or would stop being placeable, so rollback is only safe before any
non-default class is generally available.

### Monitoring Requirements

Operators can tell the feature is in use by the count of instances per runtime class.
Customers can tell it is working for their workload from workload status, which reports
the effective class and, when a workload cannot be placed, names the class as the reason.
Per-class capacity, placement-failure rate, cold-start latency, and per-instance overhead
are the signals that decide whether a class is healthy enough to keep offering.

### Dependencies

- **Providers** must declare and honor the classes they serve.
- **Federated scheduling** must treat runtime class as a placement constraint.
- **Quota and billing** must carry runtime class as a dimension.

### Scalability

Runtime class adds a small, bounded field to existing objects and a per-cell capability
declaration. The scalability question is not API volume but **capacity fragmentation**:
each class consumes host capacity differently, and per-class headroom must be planned per
site rather than in aggregate.

### Troubleshooting

The dominant failure mode is a workload that cannot be placed because no eligible cell
serves its class. That must be reported on the workload as an actionable reason, not
silence. The second is a cell advertising a class it cannot actually honor, which
surfaces as instances failing to start in one class at one site.

## Implementation History

- 2026-08-05/06 — Runtime landscape research: isolation technologies, their operational
  and security models, per-instance overhead, and where they fit a general-purpose compute
  platform. Concluded that a tiered model, not a single runtime, is the product decision.
- 2026-08-29 — This enhancement drafted.

## Drawbacks

Compute is simpler to build, price, operate, and explain with one runtime. Every class is
an ongoing cost: its own patching cadence, security posture, capacity planning, and
support burden. If the general-purpose tier turns out to serve nearly all demand, the fast
path becomes a specialized path carried at full operational cost — and the reverse is
equally possible. The honest counterargument is that customers will otherwise keep hitting
compatibility walls with no route around them, and that a platform whose isolation
guarantee is implicit cannot sell to the customers who most need it stated.

## Alternatives

**Ship one runtime.** Simplest, and today's state. Rejected because it forces the platform
to choose permanently between broad compatibility and fast startup, and neither choice
serves the whole market.

**Infer the runtime from the workload.** The platform could detect whether an image is
compatible with the fast path and route it silently. Rejected as the primary model:
inference makes cost and isolation unpredictable and unexplainable, which is exactly what
customers need stated. It remains attractive as a *recommendation* on top of an explicit
choice.

**Expose the underlying runtime directly.** Naming the specific technology would be
precise, but it makes an implementation detail a customer-facing contract that can never
be changed, and pushes tuning decisions onto customers who cannot evaluate them.

**Split each tier into a separate product.** Clean isolation of concerns, at the cost of
making customers pick a product before they understand their own workload, and duplicating
the workload API. Rejected in favor of one compute product with a runtime dimension.
