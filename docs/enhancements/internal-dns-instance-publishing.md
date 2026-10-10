# Internal DNS instance publishing

Compute publishes private instance names in each attached VPC's `datum.internal`
zone. Users attach a workload to a network; they do not select a DNS zone. The
assigned label combines the Instance name with its immutable lifetime identity.
For example, an instance can receive `web-01-43ce24f81d265cdf.datum.internal`.
Each VPC uses an independent DNS context, including when zone names overlap.

`Instance.status.dns` reports assigned hostnames and publication readiness per
network. DNS errors do not change runtime readiness. Application readiness does
not remove an instance's identity name.

```yaml
status:
  dns:
    - network:
        name: application
        namespace: production
      networkUID: 00000000-0000-0000-0000-000000000020
      hostnames: [web-01-43ce24f81d265cdf.datum.internal]
      conditions:
        - type: Ready
          status: "True"
          reason: Published
          message: Private name is published
          observedGeneration: 1
          lastTransitionTime: "2026-10-09T20:00:00Z"
```

Enable the default-off `InternalDNSPublishing` feature gate only after
provisioning the DNS platform, project permissions, and edge observation access.
The management controller requires Milo discovery.

```yaml
apiVersion: apiserver.config.datumapis.com/v1alpha1
kind: WorkloadOperator
internalDNS:
  # Match the authenticated subject used for project DNS writes.
  principalSubject: system:serviceaccount:compute-system:compute-manager
  leaseDuration: 60s
  projects:
    - name: example-project
      projectUID: 00000000-0000-0000-0000-000000000001
      # Match the project source API identity trusted by DNS admission.
      sourceClusterUID: 00000000-0000-0000-0000-000000000010
  observationSources:
    - location: dfw
      # Pin the edge cluster's kube-system namespace lifetime.
      clusterUID: 00000000-0000-0000-0000-000000000030
      # Use separately provisioned read-only edge credentials.
      kubeconfigPath: /etc/compute/edge-dfw.kubeconfig
```

Compute selects the ready resolver context whose `consumerID` is
`<projectUID>/<NetworkUID>` and reads its managed zone. It reserves a name through
`DNSRegistration` and waits for a trusted issuer's scoped `DNSContributionGrant`.
Compute can read grants but cannot create, update, or delete them. Contributions
pin both registration and grant lifetimes and generations. DNS admission checks
the authenticated publisher subject and source identity.

The optional `config/components/internal-dns-rbac` component grants DNS access
on a direct Kubernetes API. Milo project authorization must grant equivalent
permissions separately, including Instance status writes. Applying a role in
the deployment cluster does not authorize project API access.

Edge observation credentials require reads of Instances, NetworkInterfaces,
NetworkInterfaceClaims, NetworkContexts, Pods, Nodes, node leases, and the `kube-system` namespace.
Scope namespaced reads to the supported projects' edge namespaces. Credentials
cannot publish records or issue grants. Cluster-wide reads are limited to nodes
and the pinned namespace.

Only private IPv4 and IPv6 host addresses from live allocated, programmed
interfaces are eligible. Public addresses and delegated prefixes are excluded.
The observer checks the source Instance lifetime, interface claim, running
runtime Pod, ready Node, and its original heartbeat. Publication cannot outlive
that heartbeat by more than 45 seconds or exceed the configured lease.
Unsupported runtimes, cross-namespace network attachments, ambiguous runtime
Pods, and observation failures are ineligible. Projected positive status cannot
renew a lease. This requires a runtime represented by an Instance-owned Pod and
a node heartbeat lease.

Interface withdrawal makes the contribution ineligible. Instance deletion and
replacement reclaim records through project-side owner references. DNS retains
ownership of publication status; Compute patches only its observation fields.

Guest resolver application belongs to workload providers. It requires the
access-restricted network services delivery contract described in the
[Compute enhancement](https://github.com/datum-cloud/compute/pull/470).
This publisher does not configure guests, expose authorization leases on public
NetworkInterfaces, or implement peer DNS aliases. Supported guests use
`datum.internal` as their default search domain when resolver delivery is enabled.
