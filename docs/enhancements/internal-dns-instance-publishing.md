# Internal DNS instance publishing

Compute publishes private instance names in each attached network's managed
zone. Network services requests `datum.internal` by default. Compute reads the
allocated suffix, so users attach workloads to networks without selecting zones.
An instance named `web-01` receives `web-01.datum.internal`. Overlapping domains
remain isolated by the network's DNS context.

`Instance.status.dns` reports hostnames and publication readiness per network.
DNS failures do not affect runtime readiness. Application readiness does not
remove an instance identity name.

```yaml
status:
  dns:
    - network:
        name: application
        namespace: production
      hostnames: [web-01.datum.internal]
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

Only allocated, programmed private host addresses are eligible. Confirmed
interface withdrawal, stopped runtime, or unhealthy node withdraws addresses.
An unavailable observation source or unknown node health leaves published records
unchanged and reports unknown observation health. Records have no renewal
requirement. Instance deletion reclaims records through project owner references.

Observation credentials read Instances, NetworkInterfaces, NetworkInterfaceClaims,
NetworkContexts, Pods, Nodes, and project namespaces. Scope namespaced reads to
supported edge namespaces. These credentials cannot publish DNS records.

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

- `get,list` on `dnsresolvercontexts`.
- `get,list,create,update,patch,delete` on `dnsregistrations` and
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

Healthy instances poll every 30–45 seconds; pending instances poll every 10–15
seconds. Each poll uses independent jitter. Each interface observation uses six edge
reads plus project reads: 1,000 healthy single-interface instances generate about
160 edge reads per second, excluding retries and project API operations. The
publisher limits concurrent reconciliations to four and each reconciliation to
ten seconds. Size the observation API for this load before increasing deployment
scale; jitter spreads traffic but does not reduce the number of reads.
