# Per-instance CPU / memory metrics: `resource_name` join

Status: unikraft-provider#180 and infra#5699 have merged. The consumer/provider
plugins now pin series on `resource_name` = `Instance.metadata.name`. Queries
stay gated on a project-wide label-values probe: if the label is missing, the
UI shows an empty state instead of empty series. Workload and list views
enable the join when *any* `resource_name` exists in the project, not only
when `instances[0]` happens to be scraped.

## Join key

Infra allowlists the Pod label `upstream.instance` on `kube_pod_labels`. The
unikraft recording rules join that onto `datum_compute_instance_cpu_usage_seconds_total`
and `datum_compute_instance_memory_working_set_bytes` as `resource_name`.
Federation still drops `pod` / `name`; `resource_name` is the label that is
meant to survive.

There is no workload name on the series. A workload query is the same selector
with every instance name from the API.

## Queries the plugin runs once the probe matches

Per instance:

```promql
sum(rate(datum_compute_instance_cpu_usage_seconds_total{resourcemanager_datumapis_com_project_name="<projectId>",resource_name="<Instance.metadata.name>"}[2m]))

sum(datum_compute_instance_memory_working_set_bytes{resourcemanager_datumapis_com_project_name="<projectId>",resource_name="<Instance.metadata.name>"})
```

Per workload (instance names we already have):

```promql
avg(rate(datum_compute_instance_cpu_usage_seconds_total{resourcemanager_datumapis_com_project_name="<projectId>",resource_name=~"<inst1>|<inst2>"}[2m]))
```

Probe (label values of `resource_name` on):

```promql
{__name__=~"datum_compute_instance_.+",resourcemanager_datumapis_com_project_name="<projectId>"}
```

## Rollout check

```promql
count by (resource_name) (datum_compute_instance_cpu_usage_seconds_total{resourcemanager_datumapis_com_project_name="<projectId>"})
```

If that returns instance names, the UI should light up. If `resource_name` is
empty or the metric disappears, the recording rule or federation is still
dropping it.

Code: `ui/consumer/src/lib/metrics-queries.ts` and the provider copy
(`useProjectResourceIdentity`, `useInstanceMetricIdentity`, `cpuUsageQuery`,
`memoryUsageQuery`).
