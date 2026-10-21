---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

<!--
TODO (datum-cloud/enhancements process, not yet done):
- Open a tracking issue in datum-cloud/enhancements and link it here.
- If this lands in the enhancements repo, place it under the compute area, e.g.
  `enhancements/compute/direct-instances/README.md`.
- Assign the PR to sponsoring approvers before moving status -> implementable.
-->

# Direct Instances

## Summary

A customer who wants one machine in one city has to describe a fleet to get it. Today the
only thing they can create is a Workload, which fans out into a WorkloadDeployment per
location, which a cell turns into Instances. The Instance is the thing they actually
wanted, and it is the one object in the chain they are not allowed to write: in their
project it is a read-only projection with a controller-chosen name.

This enhancement makes Instance a first-class, user-creatable resource. A customer writes
an Instance with a location, and the platform federates that Instance directly to the
serving cell, runs it with the same gates, quota, and networking that Workload-born
Instances get today, and reports status back under the same name in every plane. Workload
and WorkloadDeployment are untouched and continue to produce Instances exactly as they do
now. The two origins share one kind, one cell reconciler, and one set of providers.

## Motivation

**The unit customers reason about is the Instance.** SSH keys, addresses, readiness, and
restarts all live on the Instance. Every consumer surface (CLI, agent tools, the console)
already leads with Instances and has to join back through Workload and WorkloadDeployment
labels to explain where one came from. A customer running a single sandbox for an agent, a
dev box, or a one-off job gains nothing from the fleet abstraction and pays for it in
three objects, a generated name, and status that arrives as aggregate counts.

**Agent-driven and API-first use is single-Instance shaped.** The assistant tooling and
every "give me a machine now" workflow create one thing and wait for it to be ready.
Forcing those through a Workload means rendering a Workload nobody will ever scale and
then polling a projection whose name cannot be known in advance.

**The engine already runs Instances, not Workloads.** On a cell, the Instance is the
executing object: it is spec-complete, providers watch it, quota is claimed for it, and
network interfaces are claimed for it. What blocks a direct Instance is not the runtime
but a set of controller assumptions that an Instance always has a WorkloadDeployment
parent, and a projection path that would overwrite a customer's object.

### Goals

- A customer can create, read, update, and delete an Instance in their project, and the
  name they chose is the name of the running Instance on the cell.
- A user-born Instance is placed, gated, quota-claimed, networked, executed, and reported
  by the same cell machinery and providers as a Workload-born Instance.
- Workload and WorkloadDeployment keep producing Instances with no behavior change, and
  both origins coexist in the same project and cell.
- Admission validates a direct Instance against the same catalogs (Locations,
  RuntimeClasses, InstanceTypes) and rules as a Workload template, at apply time.
- The change is feature-gated and off by default.

### Non-Goals

- Changing the Workload or WorkloadDeployment API, or retiring them.
- Scaling, rolling updates, or ordinal identity for direct Instances. A customer who
  wants replicas wants a Workload.
- Moving an existing Workload-born Instance out from under its WorkloadDeployment.
- Multi-cell-per-location placement. One location still maps to one cell.
- A new user-facing kind. The customer creates an Instance, not a Machine or Server.

## Proposal

A customer creates an Instance in their project with a location. Admission defaults its
runtime class, validates it, stamps the platform's scheduling gates on it, and marks it as
user-born. A management-plane Instance federator writes a copy to the federation hub,
labeled for location and runtime class, and reuses or creates the Karmada
PropagationPolicy and per-location network presence binding that WorkloadDeployments
already use. Karmada propagates the Instance to the serving cell. There the cell Instance
reconciler treats it exactly as it treats an Instance created by a WorkloadDeployment:
claims quota, claims network interfaces, clears gates, and rolls up status. Status returns
through Karmada status reflection to the hub, and the federator syncs it to the customer's
object.

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Instance
metadata:
  name: agent-sandbox
spec:
  location:
    name: dfw
  runtime:
    resources:
      instanceType: datumcloud/d1-standard-2
    sandbox:
      containers:
      - name: app
        image: docker.io/library/busybox:stable
  networkInterfaces:
  - network:
      name: default
```

### User Stories

**A customer launches one sandbox.** They apply an Instance with a location and an image.
Within seconds it appears in their project as the same object they wrote, with status
filling in as networking and quota clear. They never see a Workload or a
WorkloadDeployment.

**An agent provisions a machine on a customer's behalf.** The assistant renders an
Instance, applies it, and watches the object it created for readiness and an address. It
knows the name in advance, so it can hand that name to the next step without listing and
joining.

**A customer runs a fleet and a one-off side by side.** Their web tier is a Workload with
placements in three cities. Their debugging box is a direct Instance in one of them. Both
show up in the same Instances list, each labeled with its origin, and the one-off does not
count against their Workload limit or appear as a fleet of one.

**An operator upgrades a cell.** Nothing changes for them. The cell reconciler and the
providers handle both origins with one code path, and a direct Instance carries the same
runtime class label the providers already partition on.

### Risks and Mitigations

- **Hub name collision.** A propagated hub Instance and the cell's write-back copy share a
  namespace and name. Mitigation: write-back is skipped for any Instance without a
  WorkloadDeployment controller owner, and the Karmada resource interpreter for Instance
  reflects cell status into the hub copy.
- **The projector overwrites a customer's spec or retries forever.** Mitigation: the
  projector ignores hub Instances that carry the user-born marker; an integration test
  asserts a customer's spec survives a status change.
- **The cell reconciler hard-requires a controller owner.** Today it errors when an
  Instance has no owner, which would wedge every direct Instance on arrival. Mitigation:
  phase 1 makes the owner lookup optional before any ownerless Instance can reach a cell,
  guarded by the existing federation e2e suites.
- **Cell-to-hub identity RBAC.** Propagating Instances adds a kind to what Karmada must
  read and write on cells, and hand edits to the generated roles have regressed before.
  Mitigation: the Instance grants are added through the generated RBAC source, and a
  chainsaw suite fails when the hub identity cannot list Instances on a cell.
- **Label backfill.** New labels are not backfilled on existing Instances. Mitigation: the
  discriminator is primarily the presence of a controller owner reference, which every
  existing Instance already has; the marker label is a secondary, admission-stamped hint
  and nothing treats its absence as "user-born".
- **Deletion ordering.** There are no orphan sweepers. A stuck cell finalizer blocks
  Karmada removal, which blocks the project finalizer, and the Instance shows as
  Terminating until the cell recovers. Mitigation: this is the same guarantee the
  WorkloadDeployment path gives and is preferred over releasing early; the condition is
  surfaced on the project Instance with the blocking plane named.
- **Silent capability gaps.** Referenced data is not supported for direct Instances at
  alpha. Mitigation: admission rejects ConfigMap and Secret volumes on a user-born
  Instance with a reason, rather than admitting and never clearing the gate.

## Design Details

### One kind, two origins

Instance stays a single kind. What distinguishes a user-born Instance from a
Workload-born one is the absence of a controller owner reference to a WorkloadDeployment;
admission additionally stamps a managed-by marker on user-born Instances so cross-plane
readers do not have to reason about owner references that do not survive propagation. As
with WorkloadDeployment, an Instance exists in the project, on the hub, and on the cell
with a different UID in each; the name is the stable key, nothing in the data path is
keyed on Instance UID today, and that must stay true. The table is the single source of
truth for how the two origins differ.

| Behavior | Workload-born Instance | User-born Instance |
|---|---|---|
| Created in the project by | Management-plane projector (read-only projection) | The customer |
| Created on the cell by | Cell WorkloadDeployment reconciler | Karmada propagation of the hub copy |
| Hub copy written by | Cell write-back after each status update | Management-plane Instance federator |
| Controller owner | WorkloadDeployment on every plane | None; project finalizer held by the federator |
| Placement | Copied from the WorkloadDeployment location at creation | Customer-set location, immutable after creation |
| Runtime class label on the hub | Carried by the hub WorkloadDeployment | Stamped on the hub Instance by the federator |
| PropagationPolicy | Per location and class, selects WorkloadDeployments | Same policy, selector extended to Instances |
| Network presence binding | One per hub WorkloadDeployment | One per hub Instance |
| Network attachment mode | Read from the WorkloadDeployment on the cell | Resolved from the runtime class at admission, carried on the Instance |
| Scheduling gates | Stamped by the cell at creation | Stamped by admission |
| Quota claim | Cell Instance reconciler, per Instance | Same |
| Network interface claims and Network gate | Cell WorkloadDeployment reconciler | Cell Instance reconciler (moved in phase 1, serves both origins) |
| Referenced data (ConfigMap, Secret) | Companions keyed on the WorkloadDeployment | Rejected at admission at alpha; parity in phase 5 |
| Suspension | Annotation on the project WorkloadDeployment, propagated to the cell | Annotation on the project Instance, mirrored to the hub, propagated to the cell |
| Status to the project | Cell write-back, projector copies to the projection | Karmada status reflection to the hub, federator syncs to the project |
| Deletion | Workload finalizer, WorkloadDeployment federator finalizer, Karmada, cell finalizers | Project Instance finalizer, hub removal, Karmada, cell finalizers |
| Counts against | Workload limit (per Workload) plus instance, vCPU, memory claims | Instance, vCPU, memory claims only |
| Identity labels | Workload, deployment, placement, index | Location, runtime class, service name; no workload labels |

### Placement is the existing location field

The Instance already carries a location, set by the cell today and documented as the
location the Instance was scheduled to. Rather than add a second placement field that
would hold the same value, the location field becomes the customer's placement request on
a user-born Instance: required, validated against the project's ready Locations, and
immutable after creation. On a Workload-born Instance the cell keeps stamping it. Desired
and observed placement coincide because one location maps to exactly one cell; if that
ever changes, the observed cell belongs in status, not in a second spec field.

### Admission

Instance gains mutating and validating admission, which it has none of today. Admission
runs on every Instance write in a project and branches on origin. For a user-born
Instance it defaults the runtime class from the catalog, resolves the class's network
attachment mode onto the Instance, validates the spec with the shared instance validation
the Workload webhook already uses, rejects referenced-data volumes, stamps the scheduling
gates the cell expects (network and quota), stamps the service-name label that suspension
and teardown select on, and protects controller-owned fields (gates, template hash, the
identity labels) from customer edits after creation. For a controller-born write (the
projector's projection, which arrives with a WorkloadDeployment owner) admission is a
pass-through. While the feature gate is off, admission rejects every user-born create with
an explained reason, so a stray write is never silently admitted.

### Federation

A management-plane Instance federator is the Instance counterpart of the
WorkloadDeployment federator. It watches user-born Instances in every project, writes the
hub copy in the project's mapped hub namespace with the location and runtime class labels,
records the hub namespace on the project object so finalization does not depend on a
separate lookup, ensures the PropagationPolicy for that location and class exists, and
ensures the per-Instance network presence binding on the hub. The PropagationPolicy the
WorkloadDeployment federator already manages is extended to select Instances carrying the
same labels; the policy stays one per location and class, shared by both kinds, and is
removed only when neither kind targets that pair.

A Karmada resource interpreter for Instance is required. Retention keeps cell-owned
fields the cell mutates (cleared scheduling gates and cell-stamped labels) from being
reverted by the hub's desired state, and status reflection carries the cell's status
into the hub copy. Without it the hub would re-add gates the cell cleared, the same class
of bug the WorkloadDeployment interpreter exists to prevent.

The federator syncs hub status back to the project Instance. The projector, which watches
hub Instances for Workload-born write-backs, skips any hub Instance carrying the user-born
marker.

### The cell serves both origins

Phase 1 decouples the cell Instance reconciler from its parent with no user-visible
change, so that a direct Instance is not a special case when it arrives:

- The owner lookup becomes optional. Referenced-data expectations and the terminal
  referenced-data error are read from annotations on the Instance, which the stateful
  control strategy stamps at creation, rather than from the parent.
- Network interface claim creation and Network gate clearing move from the cell
  WorkloadDeployment reconciler into the cell Instance reconciler. The attachment mode is
  read from the Instance. The WorkloadDeployment reconciler keeps replica management,
  ordered rollout, and the scale subresource.
- Write-back to the hub is skipped for Instances without a WorkloadDeployment controller
  owner.
- The cell's serving-location guard, which rejects a WorkloadDeployment for a location
  the cell does not serve, gains an Instance equivalent.

Providers need no change. They partition on the runtime class label, defer while any
scheduling gate remains, and copy whichever identity labels exist. A direct Instance
carries the class label and the gates and simply lacks the workload labels.

### Quota

Quota is already Instance-native. The cell Instance reconciler creates one claim per
Instance for instance count, vCPUs, and memory regardless of origin, and the Instance
stays gated until the grant lands. The Workload-count limit is a metric rule on Workload
objects and does not apply to a direct Instance, by design. No Instance-count metric rule
is added: the instance claim already bounds count, and a per-object rule on the Instance
kind in the project would also count Workload-born projections and double-charge them.

### Suspension

Project suspension today annotates every WorkloadDeployment in the project; the cell
reads the annotation on its propagated copy and sets the Instance's suspended status
locally. The same annotation on a user-born project Instance is mirrored to the hub copy
by the federator, rides the metadata channel Karmada already propagates, and is read by
the cell Instance reconciler, which sets suspended status itself. The suspend and resume
hooks select direct Instances by the same service-name label they use for
WorkloadDeployments.

### Deletion

A customer deletes their Instance. The project finalizer held by the federator removes
the hub copy and, when it is the last object for the pair, the network presence binding
and the PropagationPolicy. Karmada removes the cell copy, which runs the cell finalizers:
the quota claim is released and the provider tears down the runtime. The hub copy is gone
only when Karmada has confirmed cell removal, and the project finalizer releases only when
the hub copy is gone. No plane deletes early and nothing sweeps orphans; a stuck cell is
visible as a Terminating Instance in the project.

### Feature gate and IAM

The feature is gated behind a new gate, default off, in the compute operator's existing
gate registry. When off, the Instance federator is not registered and admission rejects
user-born creates. When on, the compute admin role gains create, update, patch, and delete
on Instances; the viewer role is unchanged. The IAM protected resource for Instance
already declares those permissions and no role grants them today.

### Phased plan

1. **Decouple the cell from the parent.** Optional owner lookup, referenced-data
   expectations and errors on the Instance, interface claims and Network gate in the
   Instance reconciler, write-back skipped for ownerless Instances, serving-location guard
   for Instances. No user-visible change; existing federation suites must stay green.
2. **API and admission.** Instance mutating and validating admission, location as
   required and immutable placement for user-born Instances, protected controller fields,
   the feature gate, the admin role grants.
3. **Federation for Instances.** The Instance federator, the extended PropagationPolicy
   selector, the Karmada interpreter for Instance, per-Instance network presence binding,
   projector skip, hub-to-project status sync, deletion finalizer. Chainsaw suites
   mirroring full federation, write-back, projection, and deletion cascade for the direct
   path.
4. **Consumers.** CLI create, get, describe, and delete verbs for Instances and an
   Instances list that no longer joins through WorkloadDeployments; agent tools that
   render and diagnose an Instance; a top-level Instances page and create flow in the
   console; API docs, agent skills, and the federation design doc updated to say Instance
   is writable.
5. **Parity and cleanup.** Referenced data for direct Instances, suspension e2e, and a
   decision on retiring the Workload-born write-back in favor of Karmada status reflection
   for all Instances once both paths are proven.

## Production Readiness Review Questionnaire

<!-- Trimmed to the subsections relevant at alpha, following the precedent of other
compute enhancements. -->

### Feature Enablement and Rollback

- [x] Feature gate
  - Components depending on the gate: management-plane admission and the Instance
    federator; the cell changes in phase 1 are unconditional and origin-neutral.

Enabling the gate changes nothing for existing Workloads and Instances. Disabling it stops
new direct Instances at admission and unregisters the federator; direct Instances that
already exist keep running on their cells but no longer receive status or deletion
propagation until the gate is re-enabled, so rollback should be preceded by deleting
them.

### Monitoring Requirements

Operators can tell the feature is in use from the count of Instances per origin. A
customer can tell it is working from the Instance's own conditions: placed, network
ready, quota granted, ready. Time from create to ready for direct Instances, the number of
direct Instances stuck in Terminating, and federator sync errors are the signals that
decide whether the path is healthy.

### Dependencies

- **Karmada** must propagate Instances and honor the Instance resource interpreter.
- **Cell hub identity RBAC** must include Instances.
- **Milo IAM** must roll the updated admin role.
- **Providers** need no change but must tolerate Instances without workload labels.

### Scalability

A direct Instance adds one hub object, one network presence binding, and one Karmada work
object per Instance, where a WorkloadDeployment amortizes those over its replicas. At the
sizes direct Instances are meant for this is negligible; the per-Instance presence binding
is the first thing to share if customers run hundreds of them in one location.

### Troubleshooting

The dominant failure modes are a direct Instance that never leaves its gates (quota
ledger unreachable, network not ready, or a cell that cannot serve the class), a hub name
collision from a write-back that was not skipped, and a Terminating Instance blocked by a
cell finalizer. Each is reported on the project Instance with the plane and reason named.

## Implementation History

- 2026-09-30: Architectural investigation of the Workload, WorkloadDeployment, and
  Instance chain across the project, hub, and cell planes; options A through D compared;
  this enhancement drafted proposing option C.

## Drawbacks

Two origins on one kind is more to explain than one. Every reader of an Instance, from
admission to the console, now has to know which origin it is looking at, and the
discriminator has to be right on every plane. The Karmada surface grows by a kind and an
interpreter, and the cell hub identity's RBAC with it. Phase 1 moves network claiming out
of a controller that has been stable in production into one that has not owned it before.
The counterargument is that the cell already executes Instances, not Workloads, and every
consumer already leads with Instances; the platform is currently paying the cost of the
fleet abstraction on the one path where it buys nothing.

## Alternatives

**A: a single-instance shorthand on Workload.** CLI, console, and agent sugar that renders
a Workload with one placement, one location, and one replica. Cheapest and safest, and
worth shipping as a stopgap. Rejected as the architecture because the customer still gets
three objects, a generated Instance name, and aggregate status, and the Instance remains
unwritable.

**B: user-created Instance backed by a synthesized hidden WorkloadDeployment.** The
customer writes an Instance and a management controller synthesizes a WorkloadDeployment
of one replica that the existing pipeline federates. Rejected because the cell then creates
a second Instance with a controller-chosen name, so the customer's object and the running
object are different things with different names, the projector has to be rewritten to
merge status onto the customer's object, and the synthetic WorkloadDeployment is visible
in the project.

**D: a new top-level kind that produces one Instance.** A Machine or Server kind, with
Instance staying a read-only execution record. Avoids the two-origins discriminator and
leaves the projector untouched. Rejected because it adds a noun the product does not want,
the projection still carries a controller-chosen name, and under the hood it is either B
or C with an extra layer.

## Infrastructure Needed

- Karmada configuration for the Instance resource interpreter and the extended
  PropagationPolicy selector, rolled through the infra environments alongside the
  operator.
- The cell hub identity ClusterRole regenerated with Instance grants.
- The updated compute admin IAM role published through Milo.
