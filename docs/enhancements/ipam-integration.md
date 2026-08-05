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
  - [User Stories](#user-stories)
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
`public-unicast-ipv4`, `tenant-endpoint-ipv6` — and the platform returns one in the
create response, tracked from that moment until it is released. They never name a
pool, a prefix length, a region, or a CIDR. Operators define what each class means,
once, and can change what backs it without touching a consumer's manifest.

The capability it unlocks is small to describe: **an address a workload keeps.** A
published endpoint that survives a redeploy, a stable outbound address a customer
can allowlist, an inventory an operator can query.

## Motivation

Addressing is the kind of decision that gets made implicitly the first time a
workload boots and then has to be migrated. Settling it while it is still a
question of API design costs a field; settling it afterwards costs a renumbering.

Three things make an address something a consumer can rely on.

**A way to express intent.** "Give this a public address," "keep this address when
I redeploy," and "make it IPv6" are one-line requests, and the interface a consumer
writes should carry them. The IPv6 point is the sharpest — the platform is
IPv6-first by design, so asking for it should be the easy path.

**A system of record.** Who holds an address, when they got it, what happens when
they release it, how much of a kind of space is left. An on-call engineer and a
finance owner both ask these, and the answers are cheap to keep while allocations
are being made and expensive to reconstruct from the data plane afterwards.

**A unit to govern.** Quota, budgets, and utilization want to reason about "public
addresses" as a thing, in terms a consumer would recognise — and a budget
introduced alongside a capability lands very differently from one introduced after
consumption is established.

### Goals

- Let a consumer request address space by naming a class, with no knowledge of
  pools or topology.
- Let a consumer hold an address across a redeploy.
- Track every address the platform assigns to a workload, from allocation to
  release.
- Give operators one inventory: what exists, what is used, who holds it, and how
  much of each class is left.
- Make address space a unit that quota and utilization can reason about.

### Non-Goals

- **Fabric and infrastructure addressing.** Node loopbacks, routing locators,
  underlay links, and the per-site blocks they come from are platform-internal and
  are covered by the [fabric addressing plan](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/fabric.md).
  They can move onto classes later; nothing here depends on it.
- **Non-address numbering.** AS numbers, forwarding-instance identifiers, and MAC
  assignments are allocatable resources with the same claim semantics, but a class
  as designed here is prefix-shaped. They need a sibling model, not this one.
- **Globally-routable or consumer-owned tenant space.** Bring-your-own prefixes
  and public tenant address space carry a mandatory validation and export regime
  this design does not attempt to express.
- **Anycast.** A single address held by many locations at once is the inverse of
  the rule every class here follows. Adding it later is additive:
  `public-anycast-ipv4` joins the catalog beside `public-unicast-ipv4`, and nothing
  already named changes.
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
        - network: { name: default }
          ipFamilies: [IPv6, IPv4]       # dual-stack; IPv6 is primary
          reclaimPolicy: Retain          # keep the addresses across redeploys
          addresses:
            - class: public-unicast-ipv4 # a class, never an address
                                         # (omit for ordinary private addressing)
  placements:
    - name: default
      locations: [us-central-1]
      scaleSettings: { minReplicas: 1 }
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
`Programmed` means the network can carry it. They are separate because allocation
is synchronous and programming is not, and an interface must not report ready on
allocation alone.

Every address there is a tracked allocation, so the questions above have answers:

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

The last column is the number that matters. A class averaged across locations
always reads healthy; what pages someone is one location filling up, so the view
reports the worst occupant rather than the mean.

### User Stories

- **As a developer**, I ask for a public address by naming a class, and I keep it
  when I redeploy — so the endpoint I published to customers stays valid.
- **As a developer**, I get IPv6 by default and add IPv4 to the same interface when
  I need to reach something that has not moved yet.
- **As a platform operator**, I define what `public-unicast-ipv4` means once —
  which space backs it, how it is advertised, what happens on release — and every
  team consumes it by name.
- **As a platform operator**, I move a class onto new space by attaching a pool and
  draining the old one, with no consumer change.
- **As an operator on call**, I can answer "who has this address" in one command.
- **As a governance owner**, address space appears in quota in terms people
  recognise, so it can be budgeted like anything else.

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
- **A retained address is still held and still counts** against its holder's
  budget, or nothing pressures anyone to release it.

## Design Details

### Class configuration

This section defines each field and the rules the allocator applies. The audience
is a platform operator authoring classes.

Terminology, used consistently:

- **Class**: the policy object naming a kind of address space.
- **Pool**: a block of capacity offering itself to one or more classes.
- **Claim**: a request for an address of a named class.
- **Allocation**: the record of an address handed out.
- **Context**: the network and location a claim is made for.

The fields below extend the
[`IPClass` type the IPAM service already ships](https://github.com/milo-os/ipam/blob/main/pkg/apis/ipam/v1alpha1/types.go).
Everything already on it —
`provisioner`, `parameters`, `ipFamily`, `strategy`, `allowedPrefixLengths`,
`defaultPrefixLength`, `reclaimPolicy`, `visibility` — keeps its current meaning.

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

  # How many allocations of this class exist.
  #   PerClaim            one per claim — what an interface address wants
  #   PerNetwork          at most one per network
  #   PerNetworkLocation  at most one per network per location
  # Defaults to PerClaim. Immutable.
  allocationScope: PerClaim

  # How far the allocator has to look to be sure an address is free.
  #   Platform  compare against every allocation on the platform
  #   Location  compare against allocations in the same location
  #   Network   compare against allocations in the same network
  # This is a search scope, not a guarantee — what the guarantee turns out to be
  # depends on the parent. Defaults to Platform, the strictest. Immutable.
  collisionDomain: Network

  # The sizes a claim of this class may request, and the size used when a claim
  # asks for none. A fixed-size class sets min and max equal.
  allowedPrefixLengths: { min: 96, max: 96 }
  defaultPrefixLength: 96

  # Positions in the parent this class must not allocate, counted in units of
  # this class's own allocation size. Loosening is always safe; tightening
  # strands allocations already sitting in newly-reserved positions and warns.
  reservations:
    leading: 1     # the subnet's gateway and its all-zeros address live here
    trailing: 0

  # What routing does with the address. Advertisement is stated separately for
  # inside a location and beyond it, because the two are frequently opposite:
  # a per-instance address is a distinct route within its location and must
  # never appear outside it, while only the covering block leaves.
  routing:
    internal: None       # None | Host
    external: None       # None | Aggregate
  # An aggregate must be originated with a discard route. A class advertising an
  # aggregate it cannot fully resolve blackholes the unallocated space inside it.

  # What happens to the address when its claim goes away. Delete releases it.
  # Retain holds it against the claimant's identity so a replacement gets the
  # same address back. A claim can override this.
  reclaimPolicy: Delete

  # The allocator that satisfies claims of this class. Defaults to the
  # platform's own, and is immutable.
  provisioner: ipam.miloapis.com/native
```

**Pools gain two fields.** `location` names the location a pool serves, so a claim
made in one location reaches that location's space without anyone naming it; a
pool with no location serves everywhere. `parentPoolName` lets pools nest, which is
how a continent's block contains its locations' ranges and stays summarisable as
one route. A pool declaring a location is not eligible for a claim from a different
one, and an unlocated ancestor is never eligible in its child's place.

**What the collision domain means.** It is how far the allocator looks before
handing an address out, and the parent decides how far is far enough.

Both endpoint classes in the example below set `collisionDomain: Network`, and they produce
opposite results. An IPv6 endpoint is carved from a `/64` that belongs to one
network and no other, so comparing within that network is the only comparison that
could matter — nothing outside it can allocate from that space, and the result is
unique platform-wide. An IPv4 endpoint is carved from a range every network in the
location shares, so comparing within the network is a genuine narrowing: two
networks reach the same address and both keep it.

So the field says how hard to look, not how unique the answer is. Setting it wider
than the parent requires is safe and wasteful; setting it narrower is how two
holders end up with one address, which is exactly what IPv4 wants and nothing else
does.

**What a claim carries.** The collision domain, the allocation scope, and parent
resolution all key off the same two values, so the claim carries them explicitly:

```yaml
kind: IPClaim
spec:
  className: tenant-endpoint-ipv4
  networkRef: { name: default }
  location: us-central-1
```

Neither is written by a consumer. The network layer at the location supplies both,
because it is the one thing that knows them: it holds the deployment, so it knows
the location, and it resolved the interface's network reference before claiming.
Both are immutable — a claim whose network or location changed after allocation is
incoherent.

A name is enough here, and nobody types an identifier. Every claim is already
scoped to the project it was made for, and a network name is unique within a
project, so `default` in one project and `default` in another are different
collision domains without anything extra being carried. That is the same scoping
every pool and allocation already uses.

The one case a name does not settle is a network deleted and recreated under the
same name. It inherits its predecessor's space and its allocations, which is
usually what someone wants and occasionally not. Deciding otherwise means the
reference carries something stable across recreation rather than a name — worth
settling before retention ships, since that is where it starts to matter.

A class using `collisionDomain: Network`, or an `allocationScope` naming the
network, cannot be satisfied by a claim that omits the reference. The claim is
rejected rather than falling back to a wider comparison, because a wider comparison
would look correct while refusing addresses the narrow one was meant to allow — and
the error would surface as unexplained exhaustion rather than a missing field.

**Resolving a parent.** With `parentClassName` empty, the allocator takes every
pool offering this class, discards those whose family differs, discards those
declaring a different location, and picks among the rest by the class's strategy.
With `parentClassName` set, it finds the parent allocation whose scope keys match
this claim's context — for an endpoint claim on network `default` in
`us-central-1`, the `tenant-subnet-ipv6` allocation for that network and location.

If the parent does not exist, the allocator creates it first, applying the parent
class's configuration. Creation cascades: a claim in a location a network has never
used creates that location's subnet, and a claim on a new network creates the
network's prefix too.

**Two concurrency rules the cascade requires.** `allocationScope` is a uniqueness
constraint, not a lookup — two simultaneous claims for the same network and
location both observe no subnet and both try to create one, so it needs a partial
unique index on the scope keys, with the loser reading the winner's allocation
rather than failing. And a cascade takes a lock at every level, so the levels must
be locked in a deterministic order; without one, two cascades touching the same
chain from different directions deadlock. Chain depth is capped and cycles are
rejected at class-write time, not at claim time.

**Rules the allocator enforces.** A class and its parent share an address family.
A class's prefix lengths are longer than its parent's. When a parent is exhausted,
the error names the level that ran out, not the level that was asked for.

**Class health is computed, never stored.** A class reports whether any pool backs
it and how full its worst location is. Both are aggregates over pool status, read
at query time. They are deliberately not counters maintained during allocation: a
class is backed by many pools, so a class-level counter would be one row every pool
contends on — turning independent claims in different locations into a queue and
destroying the per-pool locking the service depends on. A counter also cannot
express "the worst location," which is the number that matters.

### Address families

**A class is single-family. The interface asks for families; the platform picks a
class for each.**

The tempting alternative is one class spanning both families with sizes declared
per family. It is appealing until checked against the
[tenant addressing plan](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/tenant.md),
where the two families are not the same kind of thing.

A tenant endpoint in IPv6 gets a block carved from **that tenant's own prefix**,
unique to them, which the instance subdivides. In IPv4 it gets a single address
from a **location-wide range every tenant reuses**, with no sub-block, because IPv4
scarcity makes per-tenant uniqueness impossible at scale. Different parent,
different collision domain, different hierarchy. That is not one class with two
sizes — it is two classes serving the same interface.

So the interface names families, each family resolves to a class, and where the
consumer names none — the common case — the platform's default for that family
applies. A consumer names a class only for something non-default, and names one per
family if they want that in both.

**The family belongs in the name.** Naming a class is choosing a family, so
`public-unicast-ipv4`, not `public`. The rule reaches past the family: `unicast` is
there because there is a real alternative a consumer could choose, and the two
behave visibly differently — a unicast address is one per instance per location, an
anycast address is one address live everywhere. Name the property where the
consumer is choosing between real alternatives, and leave it out where there is
only one. Tenant addressing is never advertised, so it carries no routing
qualifier.

### Where the address comes from

**One allocator, in the middle, for everything.** Not a copy per location.

The tempting alternative is pushing allocation out to each location so it keeps
working alone. It does not pay for itself. The high-volume cases that seem to need
it are not ours — pod addresses belong to container networking. What is left is a
few addresses per interface at instance-creation rate. A copy per location would
mean a database at every location, two versions of the truth, and a reconciliation
problem nobody has scoped.

It would also cost the thing this is for. One allocator is the only way to answer
"who has this address" across the platform, enforce quota on the real resource, and
report utilization honestly.

The trade is worth stating plainly: **while the central service is unreachable, no
new addresses are handed out.** A location cannot start a new instance. It does not
touch live traffic — existing addresses keep working and routes keep being
advertised — and it is the same dependency instance creation already has on
[central quota enforcement](quota-enforcement/README.md). If it proves to matter,
the answer is a small pre-reserved buffer per location: a cache, not a second
allocator.

**Claims are made where the work lands.** A consumer declares intent in their
project; that intent
[travels to the location the workload is placed at](federated-deployment-scheduling.md),
and the network layer there — which knows the location, the network, and the
family — turns it into a claim. That is why a consumer never writes a location: the
system asking already is one.

Because that system claims on the consumer's behalf, two things must hold. Its
authority is bounded by the placements actually delivered to it — a claim is valid
only for a network and location it holds a deployment for. And the address is
attributed to the consumer's project, not to the platform identity that made the
call, or quota and ownership both attach to the wrong party.

### Instance addresses

Every address on an instance is a real allocation, and that is the change making
the rest of the design work. As things stand the runtime picks the address — under
the sandbox runtime it is simply the container platform's own address — and the
platform records it afterwards, if at all. An address chosen that way cannot be
held across a redeploy or counted against a budget. Reversing it — **the platform
decides the address, and the runtime is told** — is what turns an address into
something a consumer can ask for and keep.

This is more tractable on the platform's own compute than it would be against a
third party. Every address in play is space the platform already owns, so there is
no external allocator to reconcile with and no address the platform records
without having issued.

The unit is the interface, not the address. An interface gets a block and assigns
within it, so tracking is one record per interface rather than one per container.

Three things follow.

**A claim must be able to ask for a specific address.** A claim asks for a size, not
an address. Handing the same address back to a replacement, and recording an address
already in use, both need a claim that names one. Without it the retention
experience above does not work.

**The runtime stops choosing.** An instance's address arrives with its interface
configuration rather than being invented at boot, and the container platform's own
address stops standing in for it. That is a change to the runtime contract, not
just to what the platform records.

**Retention needs an identity that is not a name.** Instance names are composed
from workload, placement, location, and ordinal, and are reused freely — a deleted
workload and a new one under the same name produce identical instance names. So
retention binds to the instance's unique identity, and a late release carrying a
stale identity is rejected rather than honoured. The allocation records that
identity opaquely; nothing about the consumer's type system crosses into the
allocator.

Retention also needs an expiry. An address held forever against a location's public
range takes that range out of service for everyone, so a retained allocation
carries a lease, keeps consuming its holder's budget while it lives, and can be
force-released by an operator with an audit record.

### A workload in two locations

A consumer runs one workload on one network in `us-central-1` and `eu-west-1`, two
replicas each, dual-stack, with a public address per instance. Here is everything
that exists, and where.

#### The platform, authored once

Five classes and the pools that back them. Consumers never see these objects; they
see the names.

```yaml
# The tenant chain. A class names the class it carves from; the top of a chain
# names none and draws from a pool instead.
kind: IPClass
metadata: { name: tenant-network-ipv6 }
spec:
  ipFamily: IPv6
  # No parentClassName — the top of a chain draws from the pools that offer it,
  # here IPPool/tenant-v6.
  allocationScope: PerNetwork          # one prefix per network
  collisionDomain: Platform
  allowedPrefixLengths: { min: 48, max: 48 }
  reclaimPolicy: Retain
---
kind: IPClass
metadata: { name: tenant-subnet-ipv6 }
spec:
  ipFamily: IPv6
  parentClassName: tenant-network-ipv6
  allocationScope: PerNetworkLocation  # one subnet per network, per location
  collisionDomain: Platform
  allowedPrefixLengths: { min: 64, max: 64 }
  reclaimPolicy: Retain                # a location's subnet is never renumbered
---
kind: IPClass
metadata:
  name: tenant-endpoint-ipv6
  annotations: { ipam.miloapis.com/is-default-class: "true" }
spec:
  ipFamily: IPv6
  parentClassName: tenant-subnet-ipv6
  collisionDomain: Network             # the parent /64 is this network's alone,
                                       # so this is still platform-unique
  allowedPrefixLengths: { min: 96, max: 96 }
  reservations: { leading: 1 }         # the subnet gateway lives in the first block
---
kind: IPClass
metadata:
  name: tenant-endpoint-ipv4
  annotations: { ipam.miloapis.com/is-default-class: "true" }
spec:
  ipFamily: IPv4
  # No parentClassName, and that is the whole IPv4 story: there is no per-network
  # IPv4 space to carve, so an endpoint draws straight from the location's shared
  # range. The IPv6 endpoint above sits three levels down its own chain; this one
  # is a chain of one.
  collisionDomain: Network             # the shared range makes this a real
                                       # narrowing — two networks reach the same
                                       # address and both keep it
  allowedPrefixLengths: { min: 32, max: 32 }
  reservations: { leading: 2, trailing: 2 }
---
kind: IPClass
metadata: { name: public-unicast-ipv4 }
spec:
  ipFamily: IPv4
  # Also no parentClassName — public addresses come from the location's public
  # pool, not from anything the network owns.
  collisionDomain: Platform            # routable, so unique everywhere
  allowedPrefixLengths: { min: 32, max: 32 }
  routing: { internal: Host, external: Aggregate }
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
metadata: { name: default }
spec:
  ipam: { mode: Auto }
---
kind: Workload
metadata: { name: hello-sandbox }
spec:
  template:
    spec:
      runtime: { sandbox: { containers: [{ name: app, image: … }] } }
      networkInterfaces:
        - network: { name: default }
          ipFamilies: [IPv6, IPv4]
          reclaimPolicy: Retain
          addresses:
            - class: public-unicast-ipv4
  placements:
    - { name: americas, locations: [us-central-1], scaleSettings: { minReplicas: 2 } }
    - { name: europe,   locations: [eu-west-1],    scaleSettings: { minReplicas: 2 } }
```

Three more objects appear in the project that the consumer did not write — their
network's own space, and its presence in each location it reaches:

```
IPPool/network-default                  fd20:a1b:2c3d::/48
├── IPPool/network-default-us-central-1 fd20:a1b:2c3d:1::/64   location: us-central-1
└── IPPool/network-default-eu-west-1    fd20:a1b:2c3d:2::/64   location: eu-west-1
```

These are project-scoped, so the consumer can see what their network holds and how
much of it is used. The location subnets appear on first use — a location the
workload never runs in never gets one — and are never renumbered afterwards.

Notice there is no IPv4 equivalent. IPv4 endpoints come from the location's shared
range directly, which is why the two families resolve different classes.

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
`[IPv6, IPv4]`, class `public-unicast-ipv4`, retain — and the network layer turns
each into three claims: one per family, plus the named public class. It supplies
the location itself, because it is one.

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
gateway and the subnet's all-zeros address. And **a public address is per instance,
per location** — four replicas means four routable addresses out of two locations'
space, which is a cost and a quota consequence that belongs in the request rather
than in a later discovery.

The addresses land on each instance and travel back to the project control plane,
which is the only place the consumer looks.

#### What the shape shows

The IPv6 address is carved from a `/64` belonging to this network alone; the IPv4
address comes from a `/20` every network in that location draws from. Same
interface, same request, two genuinely different arrangements — which is why each
family resolves its own class.

That difference is the collision domain: **an IPv6 endpoint block is unique
platform-wide because the network's prefix is; an IPv4 address is compared only
within its network.** Two networks can hold `10.128.0.2` in `us-central-1` at once
and never meet, because the routing domain separates them — and reaching across
locations comes from that same routing domain, for both families, not from the
prefixes being contiguous.

It also carries a ceiling worth stating as a product fact: a network cannot exceed
roughly four thousand IPv4 endpoints in one location, because every network draws
from the same location-wide range. IPv6 has no comparable limit.

Removing a placement releases the addresses its instances held, but not the
location's subnet — that belongs to the network, and other workloads on it draw
from the same subnet.


## What this depends on

Allocating an address is necessary and nowhere near sufficient. These are the pieces
the design assumes and does not provide. Listing them is the point — a design that
quietly assumed them would look finished and behave otherwise.

**A [network](https://github.com/datum-cloud/network-services-operator/blob/main/api/v1alpha/network_types.go)
needs one routing identity across every location it reaches**, unique
platform-wide, or the two halves of a multi-location workload are unrelated networks
sharing a name. This is the identity scoping route import and export — not the
[per-location forwarding-instance identifier](https://github.com/datum-cloud/enhancements/blob/main/architecture/design/network/addressing/srv6.md),
which is deliberately reused in every location and is a much smaller space.
Conflating the two would cap the platform at a few thousand networks in total
rather than per location.

**A moved instance needs its old route withdrawn before the new one is trusted.**
Each node advertises with a distinct identity, so a route reflector keeps both
advertisements when an instance moves, and traffic splits between the node that has
it and the node that does not. Retention makes this worse by keeping the address
valid across the move. The routes need a sequence number so the newer advertisement
demonstrably wins.

**Endpoint reachability has a per-node cost quadratic in network size.** Reaching a
remote endpoint installs per-endpoint state on every node that talks to it, and that
state is not reclaimed automatically. It is the real ceiling — far below any
address-space limit — and it needs a stated budget and a cap on endpoints per
network per node.

**Subnets need programming, not just allocation.** A location's subnet appearing on
first use currently means a record is written; nothing provisions the gateway, the
forwarding instance, or the route-table entry. That is why the interface reports
`Allocated` and `Programmed` separately.

**The gateway should be a real allocation.** It is currently a hole — space that is
reserved, owned by nothing, and configured by nothing. Making it an allocation held
by the subnet gives it an owner, lets it be programmed, and gives path-MTU discovery
a source address inside the network. Without one, oversized packets are dropped
silently: handshakes succeed and large transfers hang.

**An endpoint's block is only reachable at its first address.** The block a class
hands an interface is flattened to a single address before distribution, so an
address self-assigned inside it works locally and nowhere else. Until distribution
preserves the block, "assigns within it" is aspirational.

**Public addresses need a path to the instance** — advertisement, in-location
steering to the node holding it, and translation — and it has to move when the
instance is rescheduled. A released public address also needs a quarantine before
reissue: no route changes when it is handed to someone else, but DNS caches,
customer allowlists, and reputation data all still point the old way.

**Consuming a class must be a privilege.** Once consumers name classes instead of
pools, the class name is the only authorization boundary left. Naming a class must
be checked, and it must fail closed.

## Drawbacks

- **New allocation stops during a central outage.** Covered above; the mitigation,
  if measurement justifies it, is a small reserve per location.
- **More concepts.** Consumers gain a name to think about, operators gain a catalog
  to curate. Per-family defaults keep the common path free of both.
- **A held address is capacity nobody else can use.** That is the price of an
  address that survives a redeploy, and on a finite public range it is the cost that
  matters — which is why retention carries a lease rather than lasting forever.
- **A public address per instance per location adds up.** The design makes that
  explicit rather than hiding it, but it is a real cost consumers will meet.

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
  differ only in size, and they differ in parent, collision domain, and hierarchy.
- **Class-level utilization maintained during allocation.** Rejected: it makes one
  row every pool of a class contends on, and cannot express the per-location number
  that actually matters.

## Open Questions

**What should a network default to?** The platform is IPv6-first, but a network
currently defaults to IPv4 only while interfaces are proposed to default to IPv6.
Those cannot both be right, and the mismatch surfaces as an interface requesting a
family its network does not carry.

**What is the interface's configured prefix length and next-hop model?** It decides
whether the gateway is on-link, whether reservations protect anything real, and how
much per-node endpoint state each interface costs. Every reservation question
resolves once it is answered.

**Where does export policy live?** A class is shared by every consumer that names
it, so per-consumer export rules cannot live on one. Anything beyond "advertised or
not" needs a per-holder object the class points at.

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
