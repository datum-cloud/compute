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
DNS, project permissions, and read-only edge observation credentials.

```yaml
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
internalDNS:
  projects:
    - name: example-project
      projectUID: 00000000-0000-0000-0000-000000000001
  observationSources:
    - location: dfw
      # Authenticate to the edge API that owns instance observations.
      kubeconfigPath: /etc/compute/edge-dfw.kubeconfig
```

Compute selects the network's resolver context and reads its managed zone. It
reserves a name through `DNSRegistration` and publishes endpoint observations
through `DNSRecordContribution`. Project RBAC authorizes writes. Object lifetime
references and observation sequences prevent
stale publishers from changing replacement resources.

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
