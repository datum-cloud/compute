# Internal DNS publisher smoke test

This test runs the production Compute publisher against a disposable Kubernetes
API and the shared DNS serving fleet in the sibling `dns-operator` repository.
The DNS checkout must include the internal DNS implementation and its local test
harness. The test requires Go, Docker Compose, Kind, kubectl, OpenSSL, and an
ARM64 Docker runtime.

```sh
go build -o /tmp/compute-dns-publisher ./test/internaldns/publisher
python3 test/internaldns/fleet_smoke.py \
  --dns-repo ../dns-operator \
  --publisher-binary /tmp/compute-dns-publisher
```

The runner starts only the production publisher controller. Its default feature
gate is off, and the test first checks that it exits without constructing a
Kubernetes client. The enabled phase uses a scoped Compute service account and
the real DNS admission, publication, transport, and serving components.

The checks cover IPv4 and IPv6 over UDP and TCP, VPC isolation, application
readiness independence, address changes, allocation withdrawal and recovery,
publisher lease expiry and restart, and instance deletion with DNS resource
cleanup. Serving process identities must remain constant.

The recorded run passed all 23 checks. Both VPCs served their own Compute
records. A query for the other VPC's managed suffix returned `SERVFAIL` with no
private answer because the fixture's public fallback is unavailable. The test
records the response code and checks isolation; it does not qualify public
recursion or require a specific negative response code.

Instance objects and allocated-address observations are fixtures. This test does
not boot a guest, allocate addresses through NSO, validate Milo IAM deployment,
or test regional replication. The full Compute binary separately requires Milo
discovery and management controllers when the feature gate is enabled.

The harness removes its own Kind cluster, containers, volumes, and temporary
files. Results and redacted evidence are saved under `test/internaldns/results`.
