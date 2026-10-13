---
status: provisional
stage: alpha
latest-milestone: "TBD"
---

# Internal DNS for Compute instances

Tracking issue: [Internal DNS for Galactic VPC](https://github.com/datum-cloud/enhancements/issues/921).

- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User stories](#user-stories)
  - [Risks and mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Integration boundaries](#integration-boundaries)
  - [Names and search domains](#names-and-search-domains)
  - [Resolver configuration](#resolver-configuration)
  - [Record publication](#record-publication)
  - [Lifecycle and health](#lifecycle-and-health)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)

## Summary

Compute instances attached to a virtual private cloud (VPC) receive private DNS
names and resolver settings automatically. Compute publishes each instance's
private addresses to the internal DNS platform. Users can reach instances by
name without selecting a DNS zone or managing address records.

## Motivation

Instance addresses change as workloads scale, restart, or move. Automatic names
let users identify individual instances without maintaining address lists.
Resolution must remain isolated to the authorized VPC, including when VPCs use
overlapping addresses or private zone names.

### Goals

- Assign an instance identity name within each attached VPC's managed namespace.
- Resolve short instance names within the attached VPC.
- Configure guest resolvers from verified network-interface settings.
- Publish IPv4 and IPv6 host addresses only while their network attachment is valid.
- Bound stale answers when instances disappear or publishers lose connectivity.

### Non-Goals

- Workload-level service discovery and application health policies.
- Public DNS publication, custom per-instance zone selection, and DNS serving engines.
- Supporting several managed DNS contexts within one guest in the initial release.

## Proposal

Network services selects `datum.internal` as the default domain for VPCs with
managed DNS enabled and requests the zone from DNS. DNS manages a separate zone
within each VPC's context. Compute reads the allocated suffix and reserves an
instance name in that zone. The name remains stable for that instance's
lifetime; a replacement instance receives a distinct identity. Additional names
can follow the VPC's DNS naming policy without adding a zone choice to each
Compute resource.

Workload providers apply the resolver settings supplied by network services.
DNS publication and guest resolver configuration have separate status from
Compute readiness so users can distinguish naming problems from runtime failures.

### User stories

A user attaches a workload to a VPC and queries an instance's assigned name
from another instance in that VPC:

```console
$ cat /etc/resolv.conf
nameserver fd53::53
search datum.internal
$ dig +search +short A web-01
10.20.0.10
$ dig +short AAAA web-01.datum.internal.
fd20::10
```

Instance names and addresses are illustrative; `datum.internal` is the proposed shared
default domain. DNS supplies the canonical name; consumers do not construct it
from a workload display name.
The example resolver file describes effective settings; runtimes can apply them
through their native resolver configuration. `dig` requires
[`+search`](https://bind9.readthedocs.io/en/stable/manpages.html#dig-dns-lookup-utility)
to use the configured search list.

When an address is released, subsequent answers omit it after withdrawal and
client-cache expiry. An application failing its readiness check does not remove
the instance identity name, which can still support diagnostics.

### Risks and mitigations

- Cross-VPC publication: pin project, VPC, context, and registration lifetimes;
  scope publisher permissions through project RBAC.
- Stale addresses: publish explicit eligibility changes and retain the last
  reported state when observations are unavailable.
- Incorrect guest resolution: apply verified settings and reject
  unsupported combinations of managed contexts across interfaces.

## Design Details

### Integration boundaries

```mermaid
flowchart LR
  N[Network services] -->|Context and access| P[Consumer project API]
  C[Compute publisher] -->|Registration, addresses, eligibility| P
  P -->|DNS intent| D[Shared DNS platform]
  P -->|Workload and network desired state| K[Karmada]
  K --> E[Edge interfaces and workload providers]
  N -->|Internal resolver delivery contract| E
  E -->|Verified instance and interface state| C
  E -->|Resolver settings| G[Guest]
  G -->|VPC-authorized private service path| D
```

Compute discovers project identities through Milo, including newly created
projects, and uses authenticated project clients for publication. It identifies the
VPC's context through trusted project and Network UIDs, matching the
`DNSResolverContext.spec.consumerID` contract. DNS does not read Compute objects
to determine addresses or health.

Network services own resolver contexts, regional access, and interface DNS
settings. Galactic provides the authorized private service path. DNS owns zones,
name validation, publication status, and shared query serving. Compute does not
create access bindings or select serving replicas.

See the [DNS architecture proposal](https://github.com/datum-cloud/dns-operator/pull/229),
[VPC DNS integration proposal](https://github.com/datum-cloud/network-services-operator/pull/589),
and [Private Service Connect design](https://github.com/datum-cloud/galactic/blob/main/docs/enhancements/networking/private-service-connect/README.md)
for these contracts.

### Names and search domains

Use `<instance-name>.datum.internal` for instance identity in every
VPC and `datum.internal` as the default search domain. The authorized DNS context
scopes resolution to the attached network. Resource namespaces remain API
ownership boundaries; namespace, VPC, and project labels are unnecessary in the
default hostname. Reserve unique names across product publishers within each
context.

Two VPCs can resolve `web-01.datum.internal` to different addresses.
The authorized DNS context selects the zone before lookup and cache access;
matching domain names do not share records or grant access across VPCs. An
instance attached to several VPCs uses the same label in each context,
with only that VPC's addresses in its answers.

For peered networks, support optional qualified aliases such as
`web-01.application.production.datum.internal`, using
`<instance-name>.<network-name>.<network-namespace>.datum.internal`. Explicit DNS
sharing makes the peer's qualified zone available in the local context. Keep
the local `datum.internal` zone and search domain; do not import a peer's default
zone or automatically add peer search domains. Cross-project peers require a
unique project qualifier or an explicitly chosen unique domain. Peer naming and
sharing authorization require review alongside the peering design.

Network services supply the managed domain and approved additional search
domains. Compute providers apply and report the effective ordered list.
Associating a private zone does not automatically add it to the search list.
Bound list size and qualify resolver search order across runtimes. The default
domain and naming contract require joint DNS and network services API review.

### Resolver configuration

`NetworkInterface` is a public API. Its proposed read-only DNS status exposes
resolver addresses, search domains, and configuration readiness. Customers
select the network; they do not supply DNS authorization or renewal values.
This selected-field example includes proposed controller-written status:

```yaml
apiVersion: networking.datumapis.com/v1alpha
kind: NetworkInterface
metadata:
  name: web-01-eth0
  namespace: production
spec:
  network:
    name: application
  interfaceName: eth0
status:
  # Report effective settings without exposing internal authorization state.
  dns:
    # Galactic exposes this well-known address inside the authorized VPC.
    nameservers: ["fd53::53"]
    # Resolve short names within the attached VPC's DNS context.
    searches:
      - datum.internal
  conditions:
    # The provider reports success after applying authorized settings to the guest.
    - type: DNSConfigured
      status: "True"
      reason: ResolverConfigured
      lastTransitionTime: "2026-10-09T20:00:00Z"
```

Network services deliver resolver intent through an access-restricted provider
contract bound to the project, interface, and VPC lifetimes. Providers verify
the current Galactic network configuration before applying resolver settings.
Public DNS status reports observed configuration; it cannot authorize provider
actions. DNS manages publication ordering within its own control plane.

Providers apply current, authorized settings and report `DNSConfigured` only
after successful application. They reconcile explicit updates and reject stale revisions. Applied settings
persist during control plane outages. Resolver failure must not send private names to a public
resolver. The internal delivery schema and runtime-specific guest configuration
mechanism require joint review with network services before implementation.

### Record publication

Compute creates one registration per instance and VPC, using the managed zone
reported by the matching DNS context. The registration pins the immutable Instance lifetime; the hostname uses the
instance name. A recreated instance can use the same hostname after the previous
registration is withdrawn.

Project RBAC permits the Compute publisher to manage registrations and
contributions. Network services manages resolver access separately. DNS
validates current registration references and orders publication internally.

Consumers read assigned names and publication readiness from `Instance.status.dns`:

```yaml
status:
  dns:
    - network:
        name: application
        namespace: production
      # Display names allocated by DNS within this network.
      hostnames: [web-01.datum.internal]
      # Report publication independently of runtime readiness.
      conditions:
        - type: Ready
          status: "True"
          reason: Published
          message: Private name is published
          observedGeneration: 1
          lastTransitionTime: "2026-10-09T20:00:00Z"
```

These examples use the proposed DNS API. UIDs, generations, and times are
illustrative. Controllers write the resources; `status` shows controller output.

```yaml
apiVersion: dns.networking.miloapis.com/v1alpha1
kind: DNSRegistration
metadata:
  name: instance-web-01
spec:
  # Select the context's DNS-allocated managed zone by name.
  dnsZoneRef:
    name: managed-application
  # Reserve the allocated instance name within this VPC's managed zone.
  name: web-01
  recordTypes: [A, AAAA]
  ttlSeconds: 30
status:
  # DNS-owned output used to display the assigned name to consumers.
  canonicalFQDN: web-01.datum.internal
---
apiVersion: dns.networking.miloapis.com/v1alpha1
kind: DNSRecordContribution
metadata:
  name: web-01-eth0
spec:
  # Pin the reserved name and its current generation.
  registrationRef:
    name: instance-web-01
    uid: 66666666-6666-4666-8666-666666666666
    generation: 1
  # Publish addresses and eligibility together in one update.
  eligible: true
  reason: NetworkInterfaceProgrammed
  # Include only private host addresses allocated and programmed for this VPC.
  recordSets:
    - recordType: A
      records:
        - name: web-01
          ttl: 30
          a:
            content: 10.20.0.10
    - recordType: AAAA
      records:
        - name: web-01
          ttl: 30
          aaaa:
            content: fd20::10
status:
  # DNS-owned acknowledgement of the contribution generation.
  observedGeneration: 1
```

Compute observes the authoritative project Instance and verified interface
state. It updates records and eligibility together using conditional API writes.
Federation and edge copies retain source lifetime identity; their locally
assigned UIDs cannot establish publication authority. DNS owns contribution
status. Unavailable observations retain the last reported contribution.

### Lifecycle and health

Instance identity eligibility requires a live Instance and allocated, programmed
private host addresses. Public addresses and delegated prefixes are excluded.
Each interface contributes only addresses from its own VPC.

Address release, network deprogramming, or instance deletion triggers
an atomic ineligible update and cleanup. Project-side owner references support cleanup. Failed API observations and
unknown runtime health retain published state and report unknown readiness.
Confirmed unhealthy state or deletion withdraws records explicitly. DNS retains
last reported eligibility during publisher outages; client caches follow the
advertised TTL.

Workload service discovery needs a separate registration and explicit application
eligibility policy. It must withdraw unhealthy endpoints and accept recovery
only from a fresh observation. That capability requires a separate Compute API
proposal before implementation.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

Use the default-off `InternalDNSPublishing` feature gate for the publisher.
Provider application of managed resolver settings requires a separate rollout
control. Disabling publication stops updates and retains published records.
To remove published names, explicitly withdraw contributions before disabling
the publisher. Reenabling requires fresh observations. Test both controls
independently.

### Rollout, Upgrade and Rollback Planning

Enable staging for all projects and supported single-context guests. Extend
the shared Kubernetes end-to-end environment with actual Compute publishers
and providers. Require two isolated VPCs with overlapping names and addresses, UDP
and TCP queries from guests, outage continuity, address changes, deletion,
publisher outages, publisher RBAC isolation, stale replay, and restart coverage.
Test the same default domain and hostname with different answers across VPCs,
name collisions between product publishers, short-name resolution, fully
qualified queries, and explicit additional-zone search configuration.
Qualify guest configuration per runtime and mixed controller versions before
enabling production. These are release criteria, not validation claims.
Before enabling peer DNS sharing, test qualified aliases, zone collisions,
sharing revocation, and unchanged local short-name resolution.

### Monitoring Requirements

Expose assigned names and DNS publication conditions through `Instance.status.dns`.
Track publication lag,
observation failures, withdrawal delay, and guest configuration failures without
per-instance metric labels. Set latency and withdrawal targets before release.

### Dependencies

Project APIs and scoped authorization support publication. Network services,
Galactic, and the DNS fleet support resolver access. API outages stop fresh
observations; installed records and access persist until explicit withdrawal. DNS errors requeue independently of Compute runtime readiness.

### Scalability

Budget registrations per instance/VPC and contributions per interface. Measure
project API publication load and controller queues at fleet
scale. Spread observation polls with jitter and bound concurrent project operations.

### Troubleshooting

Trace the Instance and VPC lifetimes through context, registration, and
contribution status. Check network eligibility and publication conditions, then
compare inherited settings with guest configuration and query results.

## Implementation History

2026-10-09: Compute integration enhancement proposed.

## Drawbacks

Publication adds project API traffic and endpoint observation coordination. DNS outages can
interrupt applications that depend on assigned names despite a healthy runtime.

## Alternatives

User-managed records require consumers to track instance address changes.
Automatic publication keeps that lifecycle with Compute.

## Infrastructure Needed

Provision project-scoped publisher credentials, VPC resolver access, shared DNS
capacity, and the local Kubernetes validation
environment. This integration uses shared serving infrastructure across VPCs.
