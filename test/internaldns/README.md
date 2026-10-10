# Internal DNS publisher validation

The publisher uses the shared Kubernetes internal DNS environment in the
`dns-operator` repository. Use that environment's cluster lifecycle, TLS
admission, NATS transport, and shared serving fleet. Select every API through an
explicit kubeconfig.

Run the Compute contract tests with Kubernetes envtest assets installed:

```sh
make envtest
bin/setup-envtest use 1.31.0 --bin-dir bin -p path
go test ./internal/controller ./internal/config ./cmd/...
```

The tests cover context identity, flat instance names, independently issued
grants, fenced contribution renewal, live edge observations, source lifetime
replacement, and project DNS status ownership. They use fixtures and do not
qualify guest resolution.

The standalone runner starts the production publisher on an existing project API:

```sh
go build -o /tmp/compute-dns-publisher ./test/internaldns/publisher
/tmp/compute-dns-publisher --feature-gates=InternalDNSPublishing=true \
  --kubeconfig /tmp/project-publisher.kubeconfig \
  --namespace production --project-uid PROJECT_UID \
  --source-uid PROJECT_API_UID \
  --subject system:serviceaccount:compute-system:compute-manager \
  --edge-kubeconfig /tmp/edge-observer.kubeconfig \
  --edge-uid EDGE_KUBE_SYSTEM_UID --location dfw
```

Provision a resolver context with a `datum.internal` managed zone, the scoped
project publisher role, and a separate trusted grant issuer. Instances must
carry their source lifetime and location labels. Edge namespaces use the project
namespace UID. The observer requires allocated, programmed interfaces, live
claims, Instance-owned runtime Pods, and current node heartbeat leases.
The disabled runner starts without creating Kubernetes clients.

Release qualification must exercise two VPCs using the same default domain,
real address allocation, guest resolver application, and UDP/TCP queries through
Galactic. Include address changes, withdrawal, publisher outages, grant
revocation, stale replay, source replacement, and controller restarts. Keep this
qualification local and reuse the shared Kubernetes foundation. The publisher
contract tests and runner do not establish that those release criteria pass.
