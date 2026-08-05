# Prometheus Adapter Resource Metrics

Experimental `metrics.k8s.io` implementation backed by Prometheus Adapter.
Enable only in clusters where Prometheus or VictoriaMetrics is the source of
truth for Pod and Node CPU/memory usage.

This component owns the cluster-wide `v1beta1.metrics.k8s.io` APIService when
installed. Do not install it alongside another owner of the same APIService.

The adapter expects resource metrics with cAdvisor-compatible names and labels:

- `container_cpu_usage_seconds_total{namespace, pod, container, node}`
- `container_memory_working_set_bytes{namespace, pod, container, node}`

Runtime-specific producers, such as Unikraft telemetry, should translate their
local measurements into that shape before ingestion. The adapter then exposes
those samples through the standard Kubernetes Resource Metrics API used by HPA.

Infrastructure overlays must patch before enabling this component:

- `PROMETHEUS_URL` should point at the local Prometheus or VictoriaMetrics query
  endpoint.
- `Certificate.spec.issuerRef` should point at the cluster's serving certificate
  issuer. The default value is a placeholder `ClusterIssuer` named
  `placeholder-issuer`.
- The component defaults to `compute-system`. If an overlay deploys it in a
  different namespace, patch the `Certificate.spec.dnsNames` and the
  `APIService` service namespace / `cert-manager.io/inject-ca-from` annotation
  in the consuming overlay.
