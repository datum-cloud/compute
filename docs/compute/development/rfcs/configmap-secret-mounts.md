---
status: proposed
---

# Mounting ConfigMaps and Secrets into Compute Instances (Unikraft Provider)

> Drafted 2026-05-30, revised 2026-05-31; revised 2026-06-10 to move hub-to-cell delivery onto Karmada's native dependency propagation. **This is the foundational referenced-data delivery design for compute, and it ships before [Image Pull Credentials](./image-pull-credentials.md)** — it introduces the resolver, companion delivery, and the scheduling gate; pull secrets become a later consumer of the same path.

## Table of Contents

- [Summary](#summary)
- [What this enables for users](#what-this-enables-for-users)
- [End-to-end flow](#end-to-end-flow)
- [The gap: cross-plane delivery](#the-gap-cross-plane-delivery)
- [Design](#design)
  - [The referenced-data resolver](#the-referenced-data-resolver)
  - [Hub-to-cell delivery: native dependency propagation](#hub-to-cell-delivery-native-dependency-propagation)
  - [Consumption on the provider](#consumption-on-the-provider)
  - [Scheduling gate](#scheduling-gate)
  - [Rotation and restart](#rotation-and-restart)
- [Platform direction](#platform-direction)
- [Security](#security)
- [Alternatives](#alternatives)
- [Failure modes](#failure-modes)
- [What gets built](#what-gets-built)
- [Decisions](#decisions)
- [Open questions](#open-questions)

---

## Summary

A compute `Workload` can already *describe* config and secret mounts: a volume
sourced from a ConfigMap or Secret, a container attachment with a mount path, and
environment variables that reference a key. The runtimes can already *consume* them —
the Unikraft runtime runs instances from Pod specs through its kubelet integration,
which honors ConfigMap/Secret references as both environment variables and volume
mounts, and the GCP provider mounts them as files too. So the API is real and the
runtimes support it.

The one thing missing is in the middle: **the referenced data never reaches the cell
where the instance runs.** It lives in the user's project; the instance runs on an
edge cell; federation propagates only the `WorkloadDeployment`. The instance comes up
referencing data that isn't there.

This RFC closes that gap. It keeps the user contract unchanged (create a
ConfigMap/Secret, reference it by name), resolves the reference in the trusted
management plane, and delivers the data to the edge as a derived companion object —
secret bytes **never enter the Workload or Instance spec**. From the federation hub
to each cell, the companion rides Karmada's native dependency propagation, so shared
data follows every workload that references it — to every location it runs. Both
environment variables and file mounts work, because once the data is present the
runtime's existing Pod-spec consumption handles the rest.

## What this enables for users

Today users can only set literal environment variables, so configuration and
credentials get baked into images or pasted in as plaintext. After this:

- A user creates a `ConfigMap` and `Secret` in their project and references them
  from the Workload; the platform delivers that data to every instance in every POP
  cell, without the user ever knowing federation exists.
- **Both forms work** — keys surfaced as environment variables (the twelve-factor
  case) and ConfigMaps/Secrets mounted as files at a path (config files,
  certificates).
- **Secrets stay secret** — values never appear in the Workload or Instance the user
  sees; they travel only as Secret objects.

## End-to-end flow

The decided design: management-plane resolution → companion object → federation →
cell → provider. The `WorkloadReconciler`, `ReferencedDataController`, and
`Federator` run in the management plane; the edge cell and the compute provider run
on the POP cell.

```mermaid
sequenceDiagram
    actor User
    participant P as Project plane
    participant WC as WorkloadReconciler
    participant RDC as ReferencedDataController
    participant F as Federator
    participant K as Karmada hub
    participant C as Edge cell
    participant PR as Compute provider
    participant U as kraftlet / UKC

    User->>P: 1. Create ConfigMap + Secret
    User->>P: 2. Create Workload referencing them
    Note over P: Admission check —<br/>author may read the referenced objects
    WC->>P: 3. Create a WorkloadDeployment per placement
    RDC->>P: 4. Read referenced ConfigMap/Secret (scoped, trusted)
    RDC->>K: 5. Materialize a companion copy in the project's federation namespace
    RDC->>P: 6. Record the expected companion set on the WorkloadDeployment
    F->>K: 7. Replicate the WorkloadDeployment + placement policy (dependencies follow)
    K->>C: 8. Propagate the deployment to each matching cell; companions follow as its declared dependencies
    C->>C: 9. Create the Instance, held by a referenced-data gate
    C->>C: 10. Companions present? clear the gate, mark data ready
    PR->>C: 11. Translate the Instance into a Pod spec referencing the companions
    Note over PR,U: Kubelet integration mounts the volumes and<br/>injects the env vars natively from the present data
    PR->>U: 12. Launch the instance with config/secret applied
    U-->>User: 13. Instance running with config/secret applied
```

## The gap: cross-plane delivery

The referenced ConfigMap/Secret lives in the user's project namespace; the instance
runs on an edge cell, possibly thousands of miles away. The federation channel
carries only the `WorkloadDeployment`, so the data has no path to the cell. There is
also no gate guarding this — the instance isn't even held back; it simply launches
with the reference unresolved.

Consumption is *not* a gap: the Unikraft runtime's kubelet integration already
resolves ConfigMap/Secret references in a Pod spec — env vars and volume mounts
alike — provided the referenced objects are present where it resolves them. So the
whole problem is getting the data to the cell, and faithfully carrying the
references through into the Pod spec the runtime consumes.

## Design

### The referenced-data resolver

A new management-plane controller is the heart of delivery. For each
WorkloadDeployment it:

1. **Collects** every ConfigMap/Secret the template references — environment
   references and volume sources today; image pull secrets later.
2. **Reads** them with a scoped, trusted project-plane identity. The management plane
   already has legitimate project access, so broad project-secret read never leaves
   it.
3. **Materializes** one labeled companion per referenced object in the project's
   federation namespace.
4. **Records** the expected companion set on the WorkloadDeployment, so the cell
   knows exactly what to wait for rather than guessing.
5. **Declares** the companions as the deployment's dependencies — the same expected
   set recorded in step 4 is what the federation layer reads to deliver each
   companion wherever the deployment goes (see
   [Hub-to-cell delivery](#hub-to-cell-delivery-native-dependency-propagation)).

One companion exists per referenced object and is replicated to each placed cell — a
single object to create, update, and delete. When several deployments reference the
same object the companion is shared and reference-counted, removed only when the last
reference goes away. In single-cluster mode the same resolver runs and the companion
is simply a local copy.

### Hub-to-cell delivery: native dependency propagation

Companions travel from the federation hub to cells through Karmada's native
[dependency propagation](https://karmada.io/docs/userguide/scheduling/propagate-dependencies/),
not through hand-built routing rules:

- The deployment's placement policy sets `propagateDeps: true` — "wherever this
  deployment goes, its dependencies go too."
- A declarative dependency-interpretation hook — a small Lua script added to the
  WorkloadDeployment's existing
  [resource-interpreter customization](https://karmada.io/docs/userguide/globalview/customizing-resource-interpreter/)
  — tells the engine *what* those dependencies are: it reads the expected companion
  set the resolver recorded on the deployment and returns the companion
  ConfigMap/Secret references, pinned to the deployment's own namespace.
- For each companion, the engine maintains an *attached* binding whose destinations
  are the **union of every referencing deployment's placements** (`spec.requiredBy`).
  A shared companion lands on every cell that runs any workload referencing it — and
  only those cells. When one referencing deployment goes away, only its destinations
  are pruned; deleting the companion itself (last reference released) drains the
  binding and every cell copy through the engine's own garbage collection.

Because the interpreter runs inside the federation engine — code compute does not
deploy — the expected companion set graduates from internal bookkeeping to a
**versioned contract**: a compact JSON array of `Kind/name` tokens (e.g.
`["ConfigMap/app-config","Secret/db-creds"]`), written only by the resolver and
consumed by the interpreter on the hub and the scheduling gate on the cell.
Consumers never re-derive companion names; the resolver's name derivation is the
single source of truth. Any change to the format is a lockstep, versioned change
across all three parties.

**Why this replaced per-city routing policies.** The first implementation
([#129](https://github.com/datum-cloud/compute/pull/129)) delivered companions by
adding them as resource selectors on each city's routing policy. That works when a
project runs in a single city — but Karmada lets exactly one policy claim a given
object, exclusively and stickily. In a project running workloads in two or more
cities, a shared companion is claimed by one city's policy and delivered to that
city alone; instances everywhere else wait on data that never arrives, and no error
is raised anywhere — the hub considers the companion successfully propagated
([#155](https://github.com/datum-cloud/compute/issues/155)). Dependency propagation
removes the failure dimension rather than patching it: no policy ever claims a
companion, so there is no claim to win or lose — destinations are computed per
referencing deployment and unioned. The defect cannot be reintroduced by adding
cities or reordering policy creation.

**Prerequisites (design requirements):**

- **Hub Karmada ≥ v1.15.3.** v1.15.0 silently fails to sync changes when a
  deployment declares multiple dependencies of the same kind — rotating one of two
  referenced ConfigMaps would leave a stale copy on the cell indefinitely. Fixed
  upstream in [karmada#6931](https://github.com/karmada-io/karmada/pull/6931).
- **The `PropagateDeps` feature gate enabled on the hub** (currently pinned off).
  The mechanism is opt-in per policy, so enabling the gate changes nothing for
  other hub tenants until a policy asks for it.
- **Webhook protection for the expected-companion annotation.** Under this design
  the annotation authorizes propagation (see [Security](#security)); writes by
  anything other than the resolver must be rejected.
- **Annotation mirroring on removal.** When a deployment drops its last reference,
  the federator must delete the annotation downstream as well — a stale declaration
  would otherwise keep delivering data the workload no longer uses. Already
  implemented in [#129](https://github.com/datum-cloud/compute/pull/129).

### Consumption on the provider

Once the companions are present on the cell, the provider translates the Instance
into the Pod spec the runtime consumes — carrying the volume sources, volume mounts,
and environment references through faithfully and pointing them at the delivered
companions, with the referenced data present in the namespace the runtime resolves
from. The kubelet integration then mounts the volumes and injects the environment
variables natively; there is no provider-side inlining of secret values. (The
provider does not do this faithful translation today — it drops volumes and copies
only literal env values — so this is the provider-side work this RFC covers. The GCP
provider performs the equivalent translation already, which is why the same Workload
runs on either substrate.)

### Scheduling gate

An instance that references any ConfigMap/Secret is held by a **referenced-data
scheduling gate**, alongside the existing network and quota gates. The cell clears it
once exactly the expected companion set is present, and surfaces a
`ReferencedDataReady` status with clear reasons — resolving, awaiting propagation,
source not found, source unauthorized, source too large, or ready — backed by events
and metrics so a held instance is diagnosable, not a silent hang. The compute
provider must respect scheduling gates so an instance is never launched with its data
missing; this RFC adds that behavior.

### Rotation and restart

Decided: **no automatic roll; an explicit restart instead.** When a source changes,
the resolver re-reads it and refreshes the companion, so the latest values are staged
at the edge for the next instance launch. Running instances are not rolled
automatically — a fleet-wide restart on every edit is surprising, and a running
instance's environment isn't mutated in place regardless.

Compute already performs ordered, in-place rolling updates when a Workload's template
changes. The restart reuses that: a conventional restart annotation on the template
rolls the instances, which pick up the refreshed values — no new machinery. An
opt-in automatic roll on content change is a possible future addition, not part of
this RFC.

## Platform direction

The delivery half of this design — follow references, read them in the trusted
plane, materialize derived companions, route them to the cells where the resource is
placed, and signal readiness — is **not specific to compute**. It's a recurring
platform need: image pull credentials want the same thing next, and the network
operator already propagates derived Secrets/ConfigMaps to cells by label today. The
building blocks are already platform-level — the shared namespace-mapping and
downstream-delivery library, the label-based propagation pattern, and the
established policy-driven capabilities (quota, activity, insights) that a delivery
policy would sit naturally beside.

We deliberately **do not** build that generic capability now. With a single consumer
in hand the abstraction's seams aren't yet known, and a cross-cutting platform
capability would slow the first ship and widen the security review. Instead, this RFC
builds toward it on purpose:

- **Build the resolver in compute now, behind a narrow, capability-shaped
  interface** — in: a subject, its set of referenced objects, and its placement
  targets; out: companions delivered plus a readiness signal. It reuses the existing
  platform delivery library rather than inventing its own placement and cleanup.
- **Keep delivery cleanly separable from consumption.** The scheduling gate and the
  translation into the runtime's Pod spec stay in compute and depend only on the
  readiness signal, so the delivery component carries no compute-specific knowledge.
- **Promote on the second consumer.** When a second user of this pattern appears
  (image pull credentials, or another service), lift the delivery component into the
  platform as a capability — most likely an admin-authored delivery policy that
  declares, per resource kind, which references to follow and where to deliver them,
  fitting the existing capability-policy pattern. Two real consumers is when the
  abstraction can be shaped correctly.

This keeps compute shippable and autonomous today while making the design a
deliberate step toward a shared capability, not a one-off to untangle later. A
governance benefit falls out: when the policy lands, *what may be propagated, and
where* becomes an inspectable, access-controlled object rather than logic buried in a
controller.

## Security

- **Bytes never in user-visible specs.** The Workload and the Instance the user sees
  carry references only. Values exist as Secret objects in the project's federation
  namespace and on the cell where the runtime mounts them — never in anything
  projected back to the user.
- **Companion Secrets stay Secrets** end to end; ConfigMap companions carry only
  non-secret config. The runtime mounts the companions directly, so the provider
  never has to inline secret values itself.
- **Authorization.** Admission verifies the submitting user can read each referenced
  object — the same check already used for referenced Networks. A user cannot pull in
  an object they couldn't read themselves; the resolver's system identity is never
  the authority.
- **Trust boundary at the edge.** Resolving in the management plane is deliberate, so
  the shared, lower-trust edge never holds a credential that can read project
  ConfigMaps/Secrets. Companions are isolated per project namespace on each cell.
- **The expected-companion annotation is propagation-authorizing.** With native
  dependency propagation, the recorded companion set is what the federation engine
  delivers — naming an object there is what causes it to ship to cells. That is a
  shift from the previous label-selector design, where only resolver-labeled objects
  could ever leave the hub. The mitigation is a validating webhook that rejects
  writes to the annotation by anyone but the resolver, keeping the resolver — and
  its admission-checked inputs — the sole authority over what propagates.
- **Delivery stays need-to-know.** A companion lands only on cells that run a
  workload referencing it, never on every cell — the broadcast alternative was
  rejected for exactly this reason (see [Alternatives](#alternatives)).
- **At rest.** Companions live in storage on the project plane, the hub, and each
  cell; this presumes encryption at rest on every plane.

## Alternatives

- **Let the provider read the originals from the edge (no companions).** The leanest
  option — it removes the resolver, companions, routing changes, and the data gate,
  and it is how the GCP provider already works. **Rejected for secret bytes:** it
  requires the shared edge to hold a credential that reads project ConfigMaps and
  Secrets, exactly the trust boundary this design keeps in the management plane. (A
  config-only hybrid was considered and rejected to avoid maintaining two delivery
  paths.)
- **Inline resolved values into the Instance.** Rejected — leaks secret bytes into
  storage everywhere and into what the user sees.
- **Propagate the user's original objects directly.** Rejected — couples cell
  contents to arbitrary project objects and loses the scoping boundary.
- **A separate controller for pull secrets.** Rejected — same machinery; pull secrets
  become a thin consumer of this resolver instead.

On the hub-to-cell delivery mechanism specifically:

- **Per-city routing-policy selectors (the first implementation,
  [#129](https://github.com/datum-cloud/compute/pull/129)).** Companions ride each
  city's routing policy as extra resource selectors. Works for a single city;
  structurally broken for shared data across cities because policy claiming is
  exclusive ([#155](https://github.com/datum-cloud/compute/issues/155)). Retained
  behind a validation guard (referenced data limited to single-city projects) until
  the native mechanism lands.
- **Broadcast via a single static policy.** One (Cluster)PropagationPolicy sends
  every labeled companion to every cell — zero bookkeeping, but it places Secrets on
  cells that never run the referencing workload: a least-privilege regression at the
  most exposed tier of the platform. Rejected.
- **Hand-maintained union placement.** A compute controller maintains a companions
  policy whose destination list is the computed union of every referencing
  deployment's placements. Rejected — it re-implements the engine's `requiredBy`
  union by hand, with its own claiming, cleanup, and lifecycle bugs to own.
- **Platform-level dependency federation.** A platform-owned, declarative mechanism
  (e.g. CEL-declared dependencies, Milo-owned) could subsume this per-service wiring
  — a natural future home consistent with
  [Platform direction](#platform-direction). This design is implementable now and
  does not preclude it.

## Failure modes

- **Source missing, unauthorized, or too large** → gate held, status names the
  offending object; optional sources are skipped.
- **Companion not yet on the cell** → gate held (awaiting propagation); a normal
  transient state during placement.
- **Source changed, instances not rolled** → stale by design until restarted;
  last-synced state is surfaced so it's observable.
- **Dependency interpretation fails** (a script error on the hub) → the engine
  records a Warning event (`GetDependenciesFailed`) on the hub deployment and
  retries; on the cell the gate holds with awaiting-propagation. Visible at both
  layers, never a silent drop.
- **Companion name too long** → companion names are capped at **243 characters**:
  the engine appends a kind suffix when naming a companion's binding, and a longer
  name would exceed Kubernetes' 253-character limit and never propagate. The
  resolver's name derivation enforces the cap.
- **Rollback** → if the native delivery path is rolled back, attached bindings drain
  through the engine itself and cell copies are removed; hub companions are
  untouched, so re-enabling re-delivers without data loss.
- **Single-cluster mode** → the local-copy path must be exercised so the absence of
  federation never silently disables delivery.

## What gets built

- A **referenced-data resolver** in the management plane: collect, read, materialize,
  reference-count, and clean up companions.
- A **scoped project-plane read identity** for the resolver (built here; reused later
  by image pull credentials).
- **Dependency-propagation wiring**: `propagateDeps` on the deployment's placement
  policy, and the dependency-interpretation hook that turns the expected companion
  set into the engine's delivery list — replacing companion selectors on per-city
  routing policies.
- **Webhook protection** for the expected-companion annotation (resolver-only
  writes).
- A **referenced-data scheduling gate**, cell-side clearing, and the
  `ReferencedDataReady` status with reasons, events, and metrics.
- **API additions**: a bulk "import all keys" env form, and completing volume
  validation (secret volumes, key→path selection, file mode).
- **Provider changes**: respect scheduling gates, and faithfully translate the
  Instance's volumes, mounts, and env references into the Pod spec the runtime
  consumes.
- A **restart** path (a conventional template annotation) so a rotated source can be
  picked up on demand.

## Decisions

- **Delivery:** management-plane companions (not edge-read).
- **Hub-to-cell mechanism:** Karmada native dependency propagation (`propagateDeps`
  + a dependency-interpretation hook), replacing per-city routing-policy selectors
  — *proposed pending review (revised 2026-06-10)*. Prerequisites are design
  requirements: hub Karmada ≥ v1.15.3, the `PropagateDeps` feature gate enabled,
  webhook protection for the expected-companion annotation, and downstream
  annotation mirroring on removal. The per-city selector path stays behind a
  single-city validation guard until these land.
- **Rotation:** no auto-roll; explicit restart.
- **Gate contract:** an explicit expected-companion set recorded on the deployment,
  not guessed — and, under native delivery, the same set is the versioned contract
  the dependency interpreter consumes.
- **One resolver, not two:** pull secrets are a later consumer.
- **Platform direction:** build delivery behind a capability-shaped seam in compute
  now; promote it to a platform-owned, policy-driven capability when a second
  consumer appears — not before.
- **Sequencing:** ships before image pull credentials; owns the scoped read identity
  and provider gate-honoring.

## Open questions

1. **Scoped-read granularity:** can the resolver's project read be scoped to specific
   object types or labels, or is it broad config/secret read?
2. **Companion size limits** and behavior when exceeded.
3. **Bulk env import in v1**, or per-key references only for the first release?
4. **VM runtime** consumption — out of scope for Unikraft (sandbox-only); confirm
   deferral.
5. **Lab hub gate state:** is `PropagateDeps` already enabled on the lab hub, or
   pinned off as on staging? Determines whether the infrastructure change there is a
   flip or just a confirmation.
