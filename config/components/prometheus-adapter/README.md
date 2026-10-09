# Prometheus Adapter Resource Metrics

`metrics.k8s.io` implementation backed by Prometheus Adapter. Edge clusters use
this component to serve compute resource metrics from edge-local VictoriaMetrics.

This component owns the cluster-wide `v1beta1.metrics.k8s.io` APIService when
installed. Do not install it alongside another owner of the same APIService.

The adapter expects Datum instance resource metrics with Kubernetes identity
labels:

- `datum_compute_instance_cpu_usage_seconds_total{namespace, pod, container, node}`
- `datum_compute_instance_memory_working_set_bytes{namespace, pod, container, node}`

Runtime-specific producers, such as Unikraft telemetry, provide raw resource
measurements that infra records into this shape before the adapter queries them.
The adapter then exposes those samples through the standard Kubernetes Resource
Metrics API used by HPA.

Infrastructure overlays must patch before enabling this component:

- `PROMETHEUS_URL` should point at the edge-local VictoriaMetrics query endpoint.
- `Certificate.spec.issuerRef` should point at the cluster's serving certificate
  issuer. The default value is a placeholder `ClusterIssuer` named
  `placeholder-issuer`.
- The component defaults to `compute-system`. If an overlay deploys it in a
  different namespace, patch the `Certificate.spec.dnsNames` and the
  `APIService` service namespace / `cert-manager.io/inject-ca-from` annotation
  in the consuming overlay.

## Kata collection gaps

General-purpose Pods are returned only when every container in
the spec-derived CPU and memory request inventory has valid CPU-rate and memory samples. Both queries
use the same complete-Pod guard, so a missing container or resource omits the
whole Pod instead of becoming a zero usage value. Genuine zero CPU remains
valid. The existing Unikraft query path is unchanged.

Containers without positive CPU and memory requests fail closed, including a declared
container whose runtime status has not appeared yet.

Kata recording rules must carry the current Pod UID and the
`datum_compute_instance_{cpu,memory}_source_timestamp_seconds` gauges. Source
samples, recorded samples, and Kubernetes identity must be less than 90 seconds
old; samples must also be collected after the current Pod was created. This
budget allows the existing 30-second scrape and rule intervals with room for
one delay. A missing gauge or identity fails closed. Deploy the matching Kata
provider recording rules before enabling this adapter configuration.

Prometheus Adapter v0.12 still reports query time in `PodMetrics.Timestamp`.
The source timestamp guards bound collection age; they do not recover the
actual collection timestamp in that API response. `--metrics-max-age` controls
custom-metric discovery and does not enforce resource-metric freshness.

The ConfigMap is generated with a content hash so changing queries also changes
the adapter Pod template and restarts the process that reads them at startup.

Run the exact shipped query templates with `promtool` on PATH:

```sh
go test -count=1 ./test/resource-metrics
```
