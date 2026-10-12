# Native Pod primary VPC attachment

Use `config/overlays/cell-pod-network` in a **cell** only when NSO, Cloud, Multus,
Galactic, the networking CRDs, and a working Compute webhook serving certificate
are present. The component enables `NetworkingIntegration` and
`PodNetworkAttachment`; merge additional feature gates into the Deployment as
needed. Configure the cert-manager CSI issuer on the mounted volume and inject
the webhook configuration's CA bundle in the installing infrastructure. The
webhook service already exists in the manager base.

Opt in a Pod or a DaemonSet's Pod template with both:

```yaml
metadata:
  labels:
    compute.datumapis.com/primary-network-enabled: "true"
  annotations:
    compute.datumapis.com/primary-network: galactic
```

The network must exist in the Pod namespace. Admission adds a scheduling gate.
Compute creates a UID-scoped `NetworkInterfaceClaim` owned by the Pod. Once
Bound, Allocated, and Prepared are true, it writes the namespace-qualified
Cloud NAD as `v1.multus-cni.io/default-network`, then removes the gate in a
separate API write. Kubernetes garbage collection deletes the claim with the
Pod. Do not use a network probe from the host until kubelet reachability to the
VPC address is proven; an exec readiness probe avoids that dependency.

Current Cloud code names its NAD after the VPCAttachment, and publishes the
VPCAttachment name through `NetworkInterface.status.attachmentRef`. This name
equality is a temporary provider contract. A provider-neutral prepared NAD
reference on the interface would remove that dependency. The flow still needs
an end-to-end cell test proving that Multus observes the late annotation after
the gate is removed, and that delete/recreate waits for CNI detach before
releasing an address. Neither is established by unit tests.
