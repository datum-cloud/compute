# Internal DNS instance publishing

Compute publishes private instance names in each attached network's managed
zone. Network services requests `datum.internal` by default. Compute reads the
allocated suffix, so users attach workloads to networks without selecting zones.
An instance named `web-01` receives `web-01.instances.datum.internal`: compute
publishes instance names under the zone's `instances` scope. Overlapping domains
remain isolated by the network's DNS context.

`Instance.status.dns` reports hostnames and publication readiness per network.
DNS failures do not affect runtime readiness. An instance that stops being
Ready keeps its name, and its addresses are marked ineligible until it is Ready
again.

```yaml
status:
  dns:
    - network:
        name: application
        namespace: production
      hostnames: [web-01.instances.datum.internal]
      conditions:
        - type: Ready
          status: "True"
          reason: Published
          message: Private name is published
```

Enable the default-off `InternalDNSPublishing` feature gate after provisioning
DNS, project permissions, and read-only edge observation credentials. Compute
resolves project identities through Milo discovery, including newly created projects.

```yaml
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
internalDNS:
  observationSources:
    - location: dfw
      # Authenticate to the edge API that owns instance observations.
      kubeconfigPath: /etc/compute/edge-dfw.kubeconfig
```

Compute selects the network's resolver context and reads its managed zone. It
reserves a name through `DNSRegistration` and publishes addresses and eligibility
atomically through `DNSRecordContribution`. Project RBAC authorizes writes.
Object lifetime references and contribution generations prevent stale publishers
from changing replacement resources.

Eligibility follows the project Instance's status, which compute owns and
users cannot write. An interface's private host addresses are eligible only
while the Instance is `Ready`, its `RuntimeReady` condition is True, and the
interface reports `Allocated` and `Programmed`. Instance readiness already
accounts for a crashing runtime, an unready node, and a withdrawn interface, so
the publisher reads nothing from edge clusters. Records have no renewal
requirement. Instance deletion reclaims records through project owner references.

A location is served only when it is listed in `observationSources`. The
publisher no longer reads through the listed credentials.

Workload providers apply the network's resolver address and allocated search
domain to guests. Resolver delivery belongs to network services. The publisher
contract does not expose access deadlines on public NetworkInterfaces.

## Enablement requirements

Enable `InternalDNSPublishing` on both the management controller and the cells
that report instance identity. Only the management controller publishes DNS.
With the gate disabled, instance projections retain their identity and network
interface conditions use the standard status update path.

Before enabling the gate, grant the management controller's authenticated identity
these permissions in **each project control plane**, including newly created
projects:

- `get,list,watch` on `dnsresolvercontexts`.
- `get,list,watch,create,update,patch,delete` on `dnsregistrations` and
  `dnsrecordcontributions`.
- `get,update,patch` on `dnsrecordcontributions/status`.
- `get,patch` on `instances/status`.

The DNS resources use the `dns.networking.miloapis.com` API group. Instances use
`compute.datumapis.com`. `config/components/internal-dns-rbac` supplies the role
for installations where the controller and project API share a cluster. Applying
it to the management cluster does not grant access to Milo project APIs. The
platform's project provisioning configuration must install the corresponding
grant for the controller identity. This repository does not install that Milo
configuration. Verify permissions against each project endpoint with the same
credentials the management controller uses, for example:

```sh
kubectl --kubeconfig="$COMPUTE_PROJECT_KUBECONFIG" auth can-i create dnsregistrations.dns.networking.miloapis.com
kubectl --kubeconfig="$COMPUTE_PROJECT_KUBECONFIG" auth can-i patch dnsrecordcontributions.dns.networking.miloapis.com
kubectl --kubeconfig="$COMPUTE_PROJECT_KUBECONFIG" auth can-i patch instances.compute.datumapis.com --subresource=status
```

Repeat this check for the complete permission list. Keep the feature disabled
until the grants and automatic provisioning for new projects are in place.

DNS owns name allocation. If normalization produces the same name for two
instances, such as `a.b` and `a-b`, DNS keeps the first claim and rejects the
later registration with `NameConflict`. Compute does not assign another name or
report a rejected registration as published.

The publisher reconciles an instance when the Instance, the DNS objects it
owns, or a resolver context in its namespace changes. Each instance also
resyncs every 10–15 minutes, with independent jitter, to bound how long a missed
event can leave publication stale. 1,000 steady instances generate about 1.3
reconciles per second from resyncs. The publisher limits concurrent
reconciliations to four and each reconciliation to ten seconds.
