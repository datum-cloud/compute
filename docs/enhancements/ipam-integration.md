---
status: provisional
stage: alpha
latest-milestone: "v0.x"
---

# Address space consumers can actually ask for

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [What it feels like](#what-it-feels-like)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
- [Design Details](#design-details)
  - [Class configuration](#class-configuration)
  - [Address families](#address-families)
  - [Where the address comes from](#where-the-address-comes-from)
  - [Instance addresses](#instance-addresses)
  - [A workload in two locations](#a-workload-in-two-locations)
- [What this depends on](#what-this-depends-on)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Open Questions](#open-questions)
- [References](#references)

## Summary

A consumer deploying a workload names the **class** of address it should get —
`public-unicast-ipv4`, `tenant-endpoint-ipv6`. The platform returns an address in
the create response and tracks it until release. Consumers never name a pool, a
prefix length, a region, or a CIDR. Operators define each class once and can change
what backs it without touching a consumer's manifest.

The capability this unlocks is **an address a workload keeps**: a published endpoint
that survives a redeploy, a stable outbound address a customer can allowlist, an
inventory an operator can query.

This document is written for platform operators who author classes and for the
compute and network layers that claim on a consumer's behalf.

## Motivation

Addressing gets decided implicitly the first time a workload boots, and changing it
afterwards means renumbering. Settling it while it is still a question of API design
costs one field.

An address a consumer can rely on needs three things.

**A way to express intent.** "Give this a public address," "keep this address when I
redeploy," and "make it IPv6" are one-line requests, and the interface a consumer
writes should carry them. The platform is IPv6-first by design, so asking for IPv6
should be the easy path.

**A system of record.** Who holds an address, when they got it, what happens when
they release it, and how much of a kind of space is left. On-call engineers and
finance owners both ask these questions. The answers are cheap to record during
allocation and expensive to reconstruct from the data plane afterwards.

**A unit to govern.** Quota, budgets, and utilization need to reason about "public
addresses" in terms a consumer recognises. A budget introduced alongside a
capability lands better than one introduced after consumption is established.

### Goals

- Let a consumer request address space by naming a class, with no knowledge of pools
  or topology.
- Let a consumer hold an address across a redeploy.
- Track every address the platform assigns to a workload, from allocation to release.
- Give operators one inventory: what exists, what is used, who holds it, and how much
  of each class is left.
- Let an operator move a class onto new space by attaching a pool and draining the
  old one, with no consumer change.
- Make address space a unit that quota and utilization can reason about.

### Non-Goals

- **Fabric and infrastructure addressing.** Node loopbacks, routing locators,
  underlay links, and the per-site blocks they come from are platform-internal,
  have no consumer, and are covered by the
  [fabric addressing plan](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/fabric.md).
  They are out of scope for this document but not for the model it proposes: they form
  a hierarchy of prefixes scoped by sites, nodes, and links — opaque references like
  any other. The design was checked against that plan and needs no change to carry it.
- **Non-address numbering.** AS numbers, forwarding-instance identifiers, and MAC
  assignments are allocatable resources with the same claim semantics, but a class
  as designed here is prefix-shaped. They need a sibling model.
- **Globally-routable or consumer-owned tenant space.** Bring-your-own prefixes and
  public tenant address space carry a mandatory validation and export regime this
  design does not express.
- **Anycast.** A single address held by many locations at once inverts the rule every
  class here follows. Adding it later is additive: `public-anycast-ipv4` joins the
  catalog beside `public-unicast-ipv4`, and nothing already named changes.
- **Tracking addresses inside an endpoint.** An interface receives a block and
  assigns within it, which keeps containers and secondary addresses off the
  control-plane path.
- **Programming the data plane.** The platform decides addresses; other systems
  install and advertise them.

## Proposal

Consumers ask for a *class* of address. Operators define what each class means.
The platform answers immediately and remembers.

### What it feels like

Deploying a sandbox that keeps its public address across redeploys:

```yaml
apiVersion: compute.datumapis.com/v1alpha
kind: Workload
metadata:
  name: hello-sandbox
spec:
  template:
    spec:
      runtime:
        sandbox:
          containers:
            - name: app
              image: ghcr.io/datum-cloud/hello-unikraft:latest
      networkInterfaces:
        - network:
            name: default
          # dual-stack; IPv6 is primary
          ipFamilies:
            - IPv6
            - IPv4
          # keep the addresses across redeploys
          reclaimPolicy: Retain
          addresses:
            # a class, never an address
            # (omit this whole block for ordinary private addressing)
            - class: public-unicast-ipv4
  placements:
    - name: default
      locations:
        - us-central-1
      scaleSettings:
        minReplicas: 1
```

Three lines are new, and none mention a pool, a prefix length, a CIDR, or which
site serves the location. Asking for both families is one list. An interface that
wants ordinary private addressing names no class at all.

The result appears on the instance:

```console
$ kubectl get instance hello-sandbox-default-us-central-1-0 -o yaml
status:
  networkInterfaces:
    - addresses:
        - family: IPv6   address: fd20:a1b:2c3d:1:0:1::/96   primary: true
        - family: IPv4   address: 10.128.0.2/32
      external:
        - family: IPv4   address: 198.51.100.11
      conditions:
        - type: Allocated   status: "True"
        - type: Programmed  status: "True"
```

Both conditions matter. `Allocated` means the platform assigned the address;
`Programmed` means the network can carry it. The two stay separate because
allocation is synchronous and programming is not, and an interface must not report
ready on allocation alone.

Every address above is a tracked allocation, so the earlier questions have answers:

```console
$ datumctl ipam address show 198.51.100.11
  class:      public-unicast-ipv4
  claimed by: Instance hello-sandbox-default-us-central-1-0 (uid 4f2a…)
              project acme/app-team
  policy:     Retain — survives instance deletion
```

And operators get an inventory:

```console
$ datumctl ipam class list
NAME                    FAMILY   UNIT   BACKED BY   USED     WORST LOCATION
tenant-endpoint-ipv6    IPv6     /96    12 pools    41,208   <1%
tenant-endpoint-ipv4    IPv4     /32    12 pools    38,104   61%  us-central-1
public-unicast-ipv4     IPv4     /32    1 pool      148      58%  us-central-1
```

The last column is the number that matters. Averaged across locations, a class
always reads healthy; one location filling up is what pages someone. The view
reports the worst occupant rather than the mean.

### Notes/Constraints/Caveats

- **Allocation is synchronous.** A claim returns its address in the create
  response. That is the property the service exists to provide.
- **A consumer never writes an addressing resource.** They name a class on a
  workload or network; the platform creates and owns the claim.
- **An interface can hold several families at once**, each a separate address with
  its own lifecycle, requested as one list.
- **All requested families must succeed.** A partial result is not published; the
  interface reports which family could not be satisfied.
- **The platform is IPv6-first.** An interface that says nothing gets IPv6.
- **A class sets the default reclaim policy; an interface can override it.**
- **A retained address is still held and still counts** against its holder's budget.
  Otherwise nothing pressures anyone to release it.

## Design Details

### Class configuration

This section defines each field and the rules the allocator applies, using the
following terms consistently:

- **Class**: the policy object naming a kind of address space.
- **Pool**: a block of capacity many claims draw from, offering itself to one or
  more classes.
- **Claim**: a long-lived request for an address of a named class, bound to one
  allocation for as long as it exists.
- **Allocation**: the record of an address handed out.
- **Scope**: the references a claim carries, each under a role name — the network
  and location it is made for. A class names the roles it needs; the allocator
  indexes their values without interpreting them.

The fields below extend the
[`IPClass` type the IPAM service already ships](https://github.com/milo-os/ipam/blob/main/pkg/apis/ipam/v1alpha1/types.go).
Everything already on it —
`provisioner`, `parameters`, `ipFamily`, `strategy`, `allowedPrefixLengths`,
`defaultPrefixLength`, `reclaimPolicy`, `visibility` — keeps its name, its values,
and its meaning.

```yaml
apiVersion: ipam.miloapis.com/v1alpha1
kind: IPClass
metadata:
  # The name consumers write. It carries the address family, and any property
  # the consumer chooses between real alternatives — so public-unicast-ipv4
  # rather than public.
  name: tenant-endpoint-ipv6

  annotations:
    # Marks this class as the default for its family. A claim naming no class
    # gets the default for each family it requested. At most one class per
    # family may carry this.
    ipam.miloapis.com/is-default-class: "true"

spec:
  # The single address family this class hands out. Required. Immutable.
  ipFamily: IPv6

  # The class whose allocations this one carves from. Empty means allocations
  # come from the pools that offer this class via IPPool.spec.classNames.
  # Immutable — changing it strands every existing allocation outside its
  # declared ancestry.
  parentClassName: tenant-subnet-ipv6

  # Nothing here says which allocation a claim gets; see "How a claim finds its
  # allocation". A class that other classes carve from sets `poolPer` instead —
  # see tenant-subnet-ipv6 in the worked example.

  # What defines one independent address space. Two allocations may hold the
  # same address if, and only if, they differ in one of these references.
  # Empty means one space platform-wide.
  #
  # This field states the guarantee; the allocator's search follows from it.
  # Defaults to empty, the strictest. Immutable.
  uniqueWithin:
    - network

  # The sizes a claim of this class may request, and the size used when a claim
  # asks for none. A fixed-size class sets min and max equal.
  allowedPrefixLengths:
    min: 96
    max: 96
  defaultPrefixLength: 96

  # Positions in the parent this class does not allocate from, counted in units
  # of this class's own allocation size. Each becomes a real allocation held by
  # the parent, so reserved space has an owner, appears in utilization, and can
  # be programmed. It is inventory, not an invisible hole.
  #
  # The parent holds the reservation and excludes it from every space carved
  # from that parent, whatever `uniqueWithin` says — one reservation per parent,
  # not one per network.
  #
  # Loosening is always safe. Tightening strands allocations already sitting in
  # newly-reserved positions, and warns.
  reservations:
    leading: 1     # the subnet's gateway and its all-zeros address live here
    trailing: 0

  # What routing does with the address. Advertisement is stated separately for
  # inside a location and beyond it, because the two are frequently opposite:
  # a per-instance address is a distinct route within its location and must
  # never appear outside it. Only the covering block leaves.
  routing:
    internal: None       # None | Host
    external: None       # None | Aggregate
  # An aggregate must be originated with a discard route. A class advertising an
  # aggregate it cannot fully resolve blackholes the unallocated space inside it.

  # What happens to the allocation when its claim is deleted. Delete releases
  # the address; Retain keeps it held so it can be handed back deliberately.
  #
  # This is not how an address survives a redeploy — a redeploy does not delete
  # the claim. See "Instance addresses".
  # A claim can override this.
  reclaimPolicy: Delete

  # The allocator that satisfies claims of this class. Defaults to the
  # platform's own, and is immutable.
  provisioner: ipam.miloapis.com/native
```

**Pools gain two fields:**

- `location` names the location a pool serves, so a claim made in one location
  reaches that location's space without anyone naming it. A pool with no location
  serves everywhere. A pool declaring a location is not eligible for a claim from a
  different one, and an unlocated ancestor is never eligible in its child's place.
- `parentPoolName` lets pools nest, so a continent's block contains its locations'
  ranges and stays summarisable as one route.

**How a claim finds its allocation.** It does not look one up. A claim binds to one
allocation and an allocation to one claim, each recording the other, and the binding
is made once when the claim is created — exactly as a `PersistentVolumeClaim` binds
to a `PersistentVolume`. Storage does not reconstruct which volume a claim should
get, and neither should this: the claim object *is* the identity. Its optional
`address` field plays the part `volumeName` plays for storage.

So nothing on the class selects an allocation, because the claim already has one. The
class carries only what the allocator needs to hand out an address in the first place:
which space it comes from, and what it must not collide with.

**What `uniqueWithin` means.** Both endpoint classes in the example below set
`uniqueWithin: [network]`, and the field does different amounts of work in each. An
IPv6 endpoint is carved from a `/64` belonging to one network and no other, so the
parent already separates the space and the result is unique platform-wide regardless.
An IPv4 endpoint is carved from a range every network in the location shares, so the
setting is load-bearing: two networks reach the same address and both keep it.

Setting `uniqueWithin` wider than the parent requires is safe and wasteful. Setting
it narrower is how two holders end up with one address, which IPv4 tenant space wants
and nothing else does.

**What a claim carries.** `uniqueWithin` and parent resolution key off the same
references, so the claim carries them by role:

```yaml
kind: IPClaim
metadata:
  # Deterministic, derived from the slot and the interface it is for. A
  # replacement instance finds this name, which makes it the durable identity.
  name: hello-sandbox-americas-us-central-1-0-eth0-ipv4
  ownerReferences:
    - kind: NetworkInterface
      name: …
spec:
  className: tenant-endpoint-ipv4
  # Optional. Names a specific address to bind. Omitted, the allocator chooses.
  address: ""
  scope:
    network:
      apiGroup: networking.datumapis.com
      kind: Network
      name: default
    location:
      apiGroup: networking.datumapis.com
      kind: Location
      name: us-central-1
```

No consumer writes any of this. The network layer at the location supplies the scope,
because it is the only thing that knows it: it holds the deployment, so it knows the
location, and it resolved the interface's network reference before claiming. The
references are immutable, since a claim whose network or location changed after
allocation is incoherent.

Roles are only names a class refers to. The allocator does not know what a network or a
location is; it knows that `tenant-endpoint-ipv4` is unique within whatever arrives
under `network`. So a class can be scoped by something this document never mentions —
which is how the same model covers infrastructure addressing, where the roles are
sites, nodes, and links.

A name is enough, and nobody types an identifier. Every claim is already scoped to the
project it was made for, and a network name is unique within a project, so `default`
in one project and `default` in another are different address spaces. One case a name
does not settle is a network deleted and recreated under the same name: the new
network inherits its predecessor's space and allocations, which is usually what
someone wants and occasionally not. Settle that before retention ships, because that
is where it starts to matter.

The allocator rejects a claim that omits a role its class names in `uniqueWithin` or
that its parent chain needs, rather than falling back to a wider comparison. A wider
comparison would look correct while refusing addresses the narrow one was meant to
allow, surfacing as unexplained exhaustion rather than a missing field.

**Resolving a parent.** With `parentClassName` empty, the allocator takes every
pool offering this class, discards those whose family differs, discards those
declaring a different location, and picks among the rest by the class's strategy.
With `parentClassName` set, it projects this claim's scope onto the parent class's
`poolPer` and looks for the pool with those values — for an endpoint claim on
network `default` in `us-central-1`, the `tenant-subnet-ipv6` pool for that network
and location.

Note what a parent is. A parent class does not hand out addresses; it provisions
pools, one per distinct combination of its `poolPer` references. That is why `poolPer`
appears only on classes named as a parent, and why it is not a property of claims.

If the pool does not exist, the allocator creates it first, applying the parent
class's configuration. Creation cascades: a claim in a location a network has never
used creates that location's subnet, and a claim on a new network creates the
network's prefix too.

**The cascade needs two concurrency rules:**

- **A unique index over the `poolPer` references.** `poolPer` is a constraint, not a
  lookup. Two simultaneous claims for the same network and location both observe no
  subnet pool and both try to create one; the loser reads the winner's pool rather
  than failing. Because the set of references varies by class, the index covers a
  canonical serialization of them rather than a fixed set of columns.
- **A deterministic lock order across levels.** A cascade takes a lock at every
  level. Without a fixed order, two cascades touching the same chain from different
  directions deadlock.

Chain depth is capped and cycles are rejected at class-write time, not at claim time.

**The allocator enforces three rules:**

- A class and its parent share an address family.
- A class's prefix lengths are longer than its parent's.
- When a parent is exhausted, the error names the level that ran out, not the level
  that was asked for.

**Class health is computed, never stored.** A class reports whether any pool backs it
and how full its worst location is, both as aggregates over pool status read at query
time. Neither is a counter maintained during allocation, deliberately: a class-level
counter would be one row every pool of that class contends on, turning independent
claims in different locations into a queue and destroying the per-pool locking the
service depends on. A counter also cannot express "the worst location," which is the
number that matters.

### Address families

**A class is single-family. The interface asks for families; the platform picks a
class for each.**

The tempting alternative is one class spanning both families with sizes declared per
family. It holds up until it meets the
[tenant addressing plan](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/tenant.md).
A tenant endpoint in IPv6 gets a block carved from **that tenant's own prefix**,
unique to them, which the instance subdivides. In IPv4 it gets a single address from a
**location-wide range every tenant reuses**, with no sub-block, because IPv4 scarcity
makes per-tenant uniqueness impossible at scale. Different parent, different uniqueness
rule, different hierarchy — two classes serving one interface, not one class with two
sizes.

So the interface names families and each family resolves to a class. Where the consumer
names no class — the common case — the platform's default for that family applies.

**The family belongs in the name.** Naming a class means choosing a family, so
`public-unicast-ipv4`, not `public`. The rule reaches past the family: `unicast`
appears because a consumer can really choose the alternative, and the two behave
visibly differently — a unicast address is one per instance per location, an anycast
address is one address live everywhere. Name the property where the consumer chooses
between real alternatives, and leave it out where only one exists. Tenant addressing is
never advertised, so it carries no routing qualifier.

### Where the address comes from

**One allocator, in the middle, for everything.** Not a copy per location.

The tempting alternative pushes allocation out to each location so it keeps working
alone. It does not pay for itself. The high-volume cases that seem to need it are not
ours, because pod addresses belong to container networking; what remains is a few
addresses per interface at instance-creation rate. A copy per location would mean a
database at every location, two versions of the truth, and a reconciliation problem
nobody has scoped. It would also give up the platform-wide inventory that motivates
the work — the only way to answer "who has this address," enforce quota on the real
resource, and report utilization honestly.

State the trade plainly: **while the central service is unreachable, no new addresses
are handed out.** A location cannot start a new instance. Live traffic is untouched —
existing addresses keep working and routes keep being advertised — and instance
creation already carries the same dependency on
[central quota enforcement](quota-enforcement/README.md). If the outage window proves
to matter, the answer is a small pre-reserved buffer per location: a cache, not a
second allocator.

**Claims are made where the work lands.** A consumer declares intent in their project.
That intent
[travels to the location the workload is placed at](federated-deployment-scheduling.md),
and the network layer there turns it into a claim, because that layer knows the
location, the network, and the family. A consumer never writes a location, since the
system asking already is one.

That layer claims on the consumer's behalf, so two things must hold:

- **The placements delivered to it bound its authority.** A claim is valid only for a
  network and location it holds a deployment for.
- **The address is attributed to the consumer's project**, not to the platform
  identity that made the call. Otherwise quota and ownership both attach to the wrong
  party.

### Instance addresses

Every address on an instance is a real allocation, and that change makes the rest of
the design work. Today the runtime picks the address — under the sandbox runtime,
simply the container platform's own address — and the platform records it afterwards,
if at all. An address chosen that way cannot be held across a redeploy or counted
against a budget. Reversing the order — **the platform decides the address, and the
runtime is told** — turns an address into something a consumer can ask for and keep.

This is more tractable on the platform's own compute than against a third party.
Every address in play is space the platform already owns, so no external allocator
needs reconciling and the platform never records an address it did not issue.

The unit is the interface, not the address: an interface gets a block and assigns
within it, so tracking costs one record per interface rather than one per container.

Three things follow.

**A claim must be able to ask for a specific address.** A claim normally asks for a
size. Two cases need one that names an address: recording an address already in use,
and handing a specific address back deliberately.

**The runtime stops choosing.** An instance's address arrives with its interface
configuration rather than being invented at boot. That is a change to the runtime
contract, not just to what the platform records.

**The claim outlives the instance.** An address survives a redeploy because nothing
released it, not because anything reconstructs who used to hold it — the same reason a
StatefulSet returns a volume to a replaced pod. No matching step can go wrong and no
window leaves the address loose.

So the network layer names an interface's claims from the slot, the interface, and the
family, all of which compose from the workload, placement, location, and ordinal and
are stable across every replacement filling that slot. A replacement finds claims that
already exist and already hold addresses. Deleting the workload deletes the claims
through ownership, and `reclaimPolicy` then decides whether the addresses are released
or held.

Two consequences follow:

- **A late release from an already-replaced instance is rejected.** The release is
  checked against the claim that currently holds the binding, not against a
  remembered holder.
- **A deleted workload recreated under the same name produces identical claim
  names.** It inherits its predecessor's retained addresses — the same recreation
  question a network name raises, wanting the same answer.

A retained allocation still needs an expiry, because an address held against a
location's public range takes that range out of service for everyone. It carries a
lease, keeps consuming its holder's budget, and can be force-released by an operator
with an audit record.

One part of the storage model should not be copied: a `Retain` volume whose claim is
deleted becomes `Released` and cannot be bound again until someone clears the stale
reference by hand. A retained address must return to a claimable state without an
operator in the path.

### A workload in two locations

A consumer runs one workload on one network in `us-central-1` and `eu-west-1`, two
replicas each, dual-stack, with a public address per instance. The sections below
list everything that exists, and where.

#### The platform, authored once

Five classes and the pools that back them. Consumers see the names, never these
objects.

```yaml
# The tenant chain. The first two classes are containers: nothing claims them
# directly, and each provisions a pool the next class down carves from.
kind: IPClass
metadata:
  name: tenant-network-ipv6
spec:
  ipFamily: IPv6
  # No parentClassName — the top of a chain draws from the pools that offer it,
  # here IPPool/tenant-v6.
  # One pool per network.
  poolPer:
    - network
  # One space platform-wide.
  uniqueWithin: []
  allowedPrefixLengths:
    min: 48
    max: 48
  reclaimPolicy: Retain
---
kind: IPClass
metadata:
  name: tenant-subnet-ipv6
spec:
  ipFamily: IPv6
  parentClassName: tenant-network-ipv6
  # One pool per network, per location. The second interface on this network in
  # this location finds the pool the first one caused to be created.
  poolPer:
    - network
    - location
  uniqueWithin: []
  allowedPrefixLengths:
    min: 64
    max: 64
  # A location's subnet is never renumbered.
  reclaimPolicy: Retain
---
kind: IPClass
metadata:
  name: tenant-endpoint-ipv6
  annotations:
    ipam.miloapis.com/is-default-class: "true"
spec:
  ipFamily: IPv6
  parentClassName: tenant-subnet-ipv6
  # No poolPer — claims bind allocations of this class directly, one per claim.
  # The parent /64 is this network's alone, so this is still platform-unique.
  uniqueWithin:
    - network
  allowedPrefixLengths:
    min: 96
    max: 96
  # The subnet gateway lives in the first block.
  reservations:
    leading: 1
---
kind: IPClass
metadata:
  name: tenant-endpoint-ipv4
  annotations:
    ipam.miloapis.com/is-default-class: "true"
spec:
  ipFamily: IPv4
  # No parentClassName, which is the whole IPv4 story: no per-network IPv4 space
  # exists to carve, so an endpoint draws straight from the location's shared
  # range. The IPv6 endpoint above sits three levels down its chain; this is a
  # chain of one.
  # Sharing that range makes uniqueWithin load-bearing here — two networks reach
  # the same address and both keep it.
  uniqueWithin:
    - network
  allowedPrefixLengths:
    min: 32
    max: 32
  reservations:
    leading: 2
    trailing: 2
---
kind: IPClass
metadata:
  name: public-unicast-ipv4
spec:
  ipFamily: IPv4
  # Also no parentClassName — public addresses come from the location's public
  # pool, not from anything the network owns.
  # Routable, so unique everywhere.
  uniqueWithin: []
  allowedPrefixLengths:
    min: 32
    max: 32
  routing:
    internal: Host
    external: Aggregate
  reclaimPolicy: Retain
```

The pools. IPv6 tenant space is one root that networks carve from; IPv4 tenant
space and public space are per-location, nested so a continent summarises as one
route.

```
IPPool/tenant-v6                        fd20::/20        classNames: [tenant-network-ipv6]

IPPool/tenant-v4                        10.128.0.0/9
├── IPPool/tenant-v4-americas           10.128.0.0/12
│   └── IPPool/tenant-v4-us-central-1   10.128.0.0/20    location: us-central-1
└── IPPool/tenant-v4-emea               10.144.0.0/12
    └── IPPool/tenant-v4-eu-west-1      10.144.0.0/20    location: eu-west-1
                                                         classNames: [tenant-endpoint-ipv4]

IPPool/public-v4-us-central-1           198.51.100.0/24  location: us-central-1
IPPool/public-v4-eu-west-1              203.0.113.0/24   location: eu-west-1
                                                         classNames: [public-unicast-ipv4]
```

#### The consumer's project

Two objects, both written by the consumer:

```yaml
kind: Network
metadata:
  name: default
spec:
  ipam:
    mode: Auto
---
kind: Workload
metadata:
  name: hello-sandbox
spec:
  template:
    spec:
      runtime:
        sandbox:
          containers:
            - name: app
              image: …
      networkInterfaces:
        - network:
            name: default
          ipFamilies:
            - IPv6
            - IPv4
          reclaimPolicy: Retain
          addresses:
            - class: public-unicast-ipv4
  placements:
    - name: americas
      locations:
        - us-central-1
      scaleSettings:
        minReplicas: 2
    - name: europe
      locations:
        - eu-west-1
      scaleSettings:
        minReplicas: 2
```

Three more objects appear in the project that the consumer did not write — their
network's own space, and its presence in each location it reaches:

```
IPPool/network-default                  fd20:a1b:2c3d::/48
├── IPPool/network-default-us-central-1 fd20:a1b:2c3d:1::/64   location: us-central-1
└── IPPool/network-default-eu-west-1    fd20:a1b:2c3d:2::/64   location: eu-west-1
```

These pools are project-scoped, so the consumer can see what their network holds and
how much of it is used. The location subnets appear on first use — a location the
workload never runs in never gets one — and are never renumbered afterwards.

Note the missing IPv4 equivalent. IPv4 endpoints come from the location's shared range
directly.

#### Each location

The deployment arrives by placement, and everything below it is created there:

```
us-central-1                              eu-west-1
  WorkloadDeployment/…-americas             WorkloadDeployment/…-europe
  Instance/…-americas-us-central-1-0        Instance/…-europe-eu-west-1-0
  Instance/…-americas-us-central-1-1        Instance/…-europe-eu-west-1-1
  NetworkInterfaceClaim ×2                  NetworkInterfaceClaim ×2
```

Each interface claim carries the consumer's intent — network `default`, families
`[IPv6, IPv4]`, class `public-unicast-ipv4`, retain. The network layer turns each
into three claims: one per family, plus the named public class. It supplies the
location itself, because it is one.

#### What comes back

Twelve allocations, each attributed to the instance holding it:

```
us-central-1
  …-americas-us-central-1-0   fd20:a1b:2c3d:1:0:1::/96   10.128.0.2/32   198.51.100.11
  …-americas-us-central-1-1   fd20:a1b:2c3d:1:0:2::/96   10.128.0.3/32   198.51.100.12
eu-west-1
  …-europe-eu-west-1-0        fd20:a1b:2c3d:2:0:1::/96   10.144.0.2/32   203.0.113.10
  …-europe-eu-west-1-1        fd20:a1b:2c3d:2:0:2::/96   10.144.0.3/32   203.0.113.11
```

Allocation starts at the second block of each subnet because the first holds the
gateway and the subnet's all-zeros address. Note also that **a public address is per
instance, per location**: four replicas means four routable addresses out of two
locations' space. That cost and its quota consequence belong in the request rather
than in a later discovery.

The addresses land on each instance and travel back to the project control plane,
which is the only place the consumer looks.

#### What the shape shows

The IPv6 address is carved from a `/64` belonging to this network alone; the IPv4
address comes from a `/20` every network in that location draws from. That is what
`uniqueWithin` states: **an IPv6 endpoint block is unique platform-wide because the
network's prefix is; an IPv4 address is compared only within its network.** Two
networks can hold `10.128.0.2` in `us-central-1` at once and never meet, because the
routing domain separates them. Reaching across locations comes from that same routing
domain, for both families, not from the prefixes being contiguous.

The shared IPv4 range sets a ceiling worth stating as a product fact: a network cannot
exceed roughly four thousand IPv4 endpoints in one location. IPv6 has no comparable
limit.

Removing a placement releases the addresses its instances held, but not the location's
subnet. The subnet belongs to the network, and other workloads on it draw from it.


## What this depends on

Allocating an address is necessary and nowhere near sufficient. The design assumes
each of the following and provides none of them. Listing them is the point: a design
that quietly assumed them would look finished and behave otherwise.

- **A [network](https://github.com/datum-cloud/network-services-operator/blob/main/api/v1alpha/network_types.go)
  needs one routing identity across every location it reaches**, unique
  platform-wide, or the two halves of a multi-location workload are unrelated
  networks sharing a name. This identity scopes route import and export. It is not
  the [per-location forwarding-instance identifier](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/srv6.md),
  which is deliberately reused in every location; conflating the two would cap the
  platform at a few thousand networks in total rather than per location.
- **A moved instance needs its old route withdrawn before the new one is trusted.**
  A route reflector keeps both advertisements and traffic splits between the two
  nodes. Retention makes the split worse by keeping the address valid across the
  move. The routes need a sequence number so the newer advertisement wins.
- **Endpoint reachability costs per-node state quadratic in network size**, and
  nothing reclaims it. This is the real ceiling, far below any address-space limit,
  and it needs a stated budget and a cap on endpoints per network per node.
- **Subnets and their gateways need programming, not just allocation.** A location's
  subnet appearing on first use writes a record and nothing more; nothing provisions
  the gateway, the forwarding instance, or the route-table entry. That gap is why the
  interface reports `Allocated` and `Programmed` separately. Without a gateway that
  answers, oversized packets are dropped silently: handshakes succeed and large
  transfers hang.
- **An endpoint's block is only reachable at its first address.** Distribution
  flattens the block to a single address, so "assigns within it" stays aspirational
  until distribution preserves the block.
- **Public addresses need a path to the instance** — advertisement, in-location
  steering, and translation — and that path must move when the instance is
  rescheduled. A released public address also needs a quarantine before reissue:
  DNS caches, customer allowlists, and reputation data all still point the old way.
- **Consuming a class must be a privilege.** Once consumers name classes instead of
  pools, the class name is the only authorization boundary left. The check must fail
  closed.

## Drawbacks

- **New allocation stops during a central outage.** Covered above; the mitigation,
  if measurement justifies it, is a small reserve per location.
- **More concepts.** Consumers gain a name to think about, operators gain a catalog
  to curate. Per-family defaults keep the common path free of both.
- **A held address is capacity nobody else can use.** That is the price of an address
  that survives a redeploy, and on a finite public range it is the cost that matters.
  Retention therefore carries a lease rather than lasting forever.
- **A public address per instance per location adds up.** The design makes the cost
  explicit rather than hiding it, but consumers will still meet it.

## Alternatives

- **Allocation at every location.** Rejected: a database per location, two sources
  of truth, and it gives up the platform-wide inventory that motivates the work.
- **Delegating pools to locations through the federation layer.** Rejected: it needs
  the federation tooling to carry information in a direction it is not built to
  carry, and still requires the full service at every location.
- **Leaving instance addresses to the runtime.** Rejected: it is the status quo,
  and it is why no one can hold an address across a redeploy or count one against a
  budget.
- **A multi-family class with sizes per family.** Rejected: it assumes the families
  differ only in size, and they differ in parent, uniqueness rule, and hierarchy.
- **Class-level utilization maintained during allocation.** Rejected: it makes one
  row every pool of a class contends on, and cannot express the per-location number
  that actually matters.
- **A class field naming what identifies an allocation**, tried first as an enum
  (`PerClaim`, `PerNetwork`, `PerNetworkLocation`) and then as a list of scope
  references. Rejected: both let the allocator re-derive a binding that is already a
  recorded fact, and the enum needed a new value for every kind of thing that can hold
  an address, putting nodes and sites inside the allocator.
- **Retention by rebinding rather than by not unbinding.** Rejected: releasing an
  address on instance deletion and re-matching it later opens a window where the
  address is loose, needs a durable identity distinct from the holder, and lands in
  the state storage calls `Released`.
- **Reserved positions as policy rather than allocations.** Rejected: it leaves space
  owned by nothing, absent from inventory, and impossible to program, and it cannot
  express a reservation away from the start or end of the parent.

## Open Questions

**What should a network default to?** The platform is IPv6-first, but a network
currently defaults to IPv4 only while interfaces are proposed to default to IPv6.
Those cannot both be right, and the mismatch surfaces as an interface requesting a
family its network does not carry.

**What is the interface's configured prefix length and next-hop model?** The answer
decides whether the gateway is on-link, whether reservations protect anything real,
and how much per-node endpoint state each interface costs. Every reservation question
resolves once it is settled.

**Where does export policy live?** A class is shared by every consumer that names
it, so per-consumer export rules cannot live on one. Anything beyond "advertised or
not" needs a per-holder object the class points at.

**Should a container class be an `IPClass` at all?** No claim ever names
`tenant-network-ipv6` or `tenant-subnet-ipv6`; they exist to provision pools. They
share most of a class's fields, which argues for one kind. But a consumer listing
classes should not see them, and `poolPer` is meaningless on every other class. A
separate kind, or a marker on the class, may read better than a field that applies to
half of them.

## References

**The address space this draws on**

- [Platform addressing plan](https://github.com/datum-cloud/enhancements/tree/main/architecture/design/network/addressing)
  — the overview tying the three plans below together.
- [Tenant addressing](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/tenant.md)
  — the per-network IPv6 prefix and the shared per-location IPv4 range that the
  example classes carve from, and the reason the two families need separate classes.
- [Fabric addressing](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/fabric.md)
  — the platform-internal blocks this design explicitly leaves alone.
- [SRv6 uSID plan](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/srv6.md)
  — the forwarding-instance identifier that must not be confused with a network's
  platform-wide routing identity.

**The systems that hold the pieces**

- [IPAM API types](https://github.com/milo-os/ipam/blob/main/pkg/apis/ipam/v1alpha1/types.go)
  — `IPClass`, `IPPool`, `IPClaim`, and `IPAllocation` as they exist today, which
  the class fields here extend.
- [Network API](https://github.com/datum-cloud/network-services-operator/blob/main/api/v1alpha/network_types.go)
  — the network a claim is scoped to, and where a network's addressing intent is
  declared.
- [Federated Deployment Scheduling](federated-deployment-scheduling.md)
  — how a placement reaches the location whose network layer makes the claim.
- [Quota Enforcement](quota-enforcement/README.md)
  — the existing central dependency in the instance-creation path, and where
  address space becomes a budgeted unit.
