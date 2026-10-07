# Internal DNS instance publishing

Compute can publish stable private instance identity names through the internal
DNS API. The integration is disabled by default and supports `Instance`
resources only. It does not infer service discovery intent or publish public
addresses.

Enable it with `--feature-gates=InternalDNSPublishing=true` and configure the
identity that the DNS control plane trusts for each Milo project:

```yaml
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
internalDNS:
  principalSubject: system:serviceaccount:compute-system:compute-manager
  leaseDuration: 60s
  projects:
    - name: example-project
      projectUID: 00000000-0000-0000-0000-000000000001
      sourceClusterUID: 00000000-0000-0000-0000-000000000010
```

`projectUID` and `sourceClusterUID` must match the trusted project entry in the
DNS control-plane configuration. The principal must be the authenticated
Kubernetes username used for project API writes. These values select and
authorize an existing DNS-owned namespace; they do not authenticate the
publisher by themselves.

The project API must separately authorize the authenticated principal to read
managed namespaces and to manage registrations, grants, contributions, and
producer-owned contribution status. In Milo this requires the corresponding DNS
`ProtectedResource`/IAM policy (or equivalent project authorization) for the
client certificate or token subject. The
`config/components/internal-dns-rbac` component supplies direct Kubernetes RBAC
for a same-API development environment; applying it only to Compute's deployment
cluster does not grant access through a Milo project control-plane endpoint.
Production rollout therefore requires matching infra/IAM provisioning before
the feature gate is enabled.

The first integration requires `discovery.mode: milo` and management
controllers. It uses the engaged project client's authenticated connection and
renews leases only from an uncached read of the authoritative project Instance.
An edge or federation copy with a different UID is rejected, preventing an old
copy from publishing into a replacement Instance lifetime.

For each VPC attachment, the publisher reads the project `Network` UID and
selects an accepted `DNSManagedNamespace` whose project and VPC UIDs both
match. It creates a generation-pinned `DNSRegistration`,
`DNSContributionGrant`, and `DNSRecordContribution`. The name combines the
immutable Instance UID with its allocated name, so display-name reuse cannot
adopt an earlier identity. A separate registration is used for every VPC.

Only allocated and programmed private host addresses are published. IPv4 and
IPv6 are supported; a delegated prefix is not treated as one instance address.
Application readiness does not affect instance identity. Address release or
network deprogramming writes an ineligible observation, and deletion is
garbage-collected through the project-side Instance owner reference. The
publisher refreshes a bounded lease from current Instance/interface state, so a
disconnected publisher cannot retain an address indefinitely.

DNS reconciliation errors requeue this controller and do not modify Compute
readiness. Publication state remains available on the DNS registration and
contribution resources. With the feature gate disabled, Compute does not
register the publisher, discover DNS resources, make DNS API calls, or require
the optional DNS RBAC component.
