/**
 * PromQL for instance resource metrics and (when published) ALB traffic.
 *
 * CPU/memory series are `datum_compute_instance_*`, federated into the portal
 * VictoriaMetrics with `resourcemanager_datumapis_com_project_name`. Federation
 * drops `pod`/`container`, so identity is discovered at runtime among the
 * labels that survive (`name`, `exported_pod`, `pod`, `k8s_pod_name`).
 *
 * ALB series match cloud-portal edge metrics: Envoy `gateway_name` = HTTPProxy
 * name. Those charts are workload-scoped, not per-instance.
 */
import { PLUGIN_ID } from './api';
import { fetchPrometheusLabelValues } from './prometheus';
import { useQueries } from '@tanstack/react-query';

export const CPU_METRIC = 'datum_compute_instance_cpu_usage_seconds_total';
export const MEMORY_METRIC = 'datum_compute_instance_memory_working_set_bytes';
export const NETWORK_RX_METRIC = 'datum_compute_instance_network_receive_bytes_total';
export const NETWORK_TX_METRIC = 'datum_compute_instance_network_transmit_bytes_total';
export const ENVOY_RQ_METRIC = 'envoy_vhost_vcluster_upstream_rq';
export const ENVOY_RQ_TIME_METRIC = 'envoy_vhost_vcluster_upstream_rq_time_bucket';

export const REGION_LABEL = 'label_topology_kubernetes_io_region';

/** Labels tried, in order, when pinning an instance series. */
export const INSTANCE_IDENTITY_LABELS = ['name', 'exported_pod', 'pod', 'k8s_pod_name'] as const;

export type InstanceIdentityLabel = (typeof INSTANCE_IDENTITY_LABELS)[number];

export interface InstanceMetricIdentity {
  label: InstanceIdentityLabel;
  value: string;
}

function escapePromQL(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

function escapePromQLRegexp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

export function namesMatcher(label: string, names: readonly string[]): string | undefined {
  const unique = [...new Set(names.filter(Boolean))];
  if (unique.length === 0) return undefined;
  if (unique.length === 1) return `${label}="${escapePromQL(unique[0])}"`;
  return `${label}=~"${unique.map(escapePromQLRegexp).join('|')}"`;
}

export function instanceSeriesMatch(projectId: string): string {
  return `{__name__=~"datum_compute_instance_.+",resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}"}`;
}

export function instanceSelector(projectId: string, identity: InstanceMetricIdentity): string {
  return `{resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}",${identity.label}="${escapePromQL(identity.value)}"}`;
}

export function scopedInstanceSelector(
  projectId: string,
  label: InstanceIdentityLabel,
  names: readonly string[]
): string | undefined {
  const match = namesMatcher(label, names);
  if (!match) return undefined;
  return `{resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}",${match}}`;
}

export function cpuUsageQuery(projectId: string, identity: InstanceMetricIdentity): string {
  return `sum(rate(${CPU_METRIC}${instanceSelector(projectId, identity)}[2m]))`;
}

export function memoryUsageQuery(projectId: string, identity: InstanceMetricIdentity): string {
  return `sum(${MEMORY_METRIC}${instanceSelector(projectId, identity)})`;
}

export function networkReceiveQuery(projectId: string, identity: InstanceMetricIdentity): string {
  return `sum(rate(${NETWORK_RX_METRIC}${instanceSelector(projectId, identity)}[2m]))`;
}

export function networkTransmitQuery(projectId: string, identity: InstanceMetricIdentity): string {
  return `sum(rate(${NETWORK_TX_METRIC}${instanceSelector(projectId, identity)}[2m]))`;
}

/** In/Out series for the Network I/O chart. `sum()` drops identity labels so the portal names series In and Out. */
export function networkIoQuery(projectId: string, identity: InstanceMetricIdentity): string {
  const sel = instanceSelector(projectId, identity);
  return (
    `label_replace(sum(rate(${NETWORK_RX_METRIC}${sel}[2m])),"direction","In","","")` +
    ` or ` +
    `label_replace(sum(rate(${NETWORK_TX_METRIC}${sel}[2m])),"direction","Out","","")`
  );
}

export function workloadCpuAvgQuery(
  projectId: string,
  label: InstanceIdentityLabel,
  names: readonly string[]
): string | undefined {
  const sel = scopedInstanceSelector(projectId, label, names);
  if (!sel) return undefined;
  return `avg(rate(${CPU_METRIC}${sel}[2m]))`;
}

export function workloadCpuSumQuery(
  projectId: string,
  label: InstanceIdentityLabel,
  names: readonly string[]
): string | undefined {
  const sel = scopedInstanceSelector(projectId, label, names);
  if (!sel) return undefined;
  return `sum(rate(${CPU_METRIC}${sel}[2m]))`;
}

export function workloadMemoryAvgQuery(
  projectId: string,
  label: InstanceIdentityLabel,
  names: readonly string[]
): string | undefined {
  const sel = scopedInstanceSelector(projectId, label, names);
  if (!sel) return undefined;
  return `avg(${MEMORY_METRIC}${sel})`;
}

export function workloadNetworkIoQuery(
  projectId: string,
  label: InstanceIdentityLabel,
  names: readonly string[]
): string | undefined {
  const sel = scopedInstanceSelector(projectId, label, names);
  if (!sel) return undefined;
  return (
    `label_replace(sum(rate(${NETWORK_RX_METRIC}${sel}[2m])),"direction","In","","")` +
    ` or ` +
    `label_replace(sum(rate(${NETWORK_TX_METRIC}${sel}[2m])),"direction","Out","","")`
  );
}

function albSelector(projectId: string, proxyId: string, extra: Record<string, string> = {}): string {
  const labels: string[] = [
    `resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}"`,
    `gateway_name="${escapePromQL(proxyId)}"`,
    `gateway_namespace="default"`,
    `${REGION_LABEL}!=""`,
  ];
  for (const [key, value] of Object.entries(extra)) {
    if (value.startsWith('=~') || value.startsWith('!=') || value.startsWith('!~')) {
      labels.push(`${key}${value}`);
    } else {
      labels.push(`${key}="${escapePromQL(value)}"`);
    }
  }
  return `{${labels.join(',')}}`;
}

export function albRpsQuery(projectId: string, proxyId: string): string {
  return `sum(rate(${ENVOY_RQ_METRIC}${albSelector(projectId, proxyId)}[1m]))`;
}

export function albRpsQueryMany(projectId: string, proxyIds: readonly string[]): string | undefined {
  const match = namesMatcher('gateway_name', proxyIds);
  if (!match) return undefined;
  return `sum(rate(${ENVOY_RQ_METRIC}{resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}",${match},gateway_namespace="default",${REGION_LABEL}!=""}[1m]))`;
}

export function albP99Query(projectId: string, proxyId: string): string {
  return `histogram_quantile(0.99, sum(rate(${ENVOY_RQ_TIME_METRIC}${albSelector(projectId, proxyId)}[1m])) by (le))`;
}

export function albErrorRateQuery(projectId: string, proxyId: string): string {
  const errors = `sum(rate(${ENVOY_RQ_METRIC}${albSelector(projectId, proxyId, { envoy_response_code: '=~"[45].."' })}[1m]))`;
  const total = albRpsQuery(projectId, proxyId);
  return `${errors} / ${total}`;
}

export function albRpsByClassQuery(projectId: string, proxyId: string): string {
  const selector = albSelector(projectId, proxyId);
  return (
    `sum by (envoy_response_code_class) (` +
    `label_replace(` +
    `rate(${ENVOY_RQ_METRIC}${selector}[1m]),` +
    `"envoy_response_code_class","$\{1}XX","envoy_response_code","([0-9]).*"` +
    `))`
  );
}

/**
 * Pick the surviving identity label whose values include this instance name.
 * Falls back to `name=<instance>` so PromQL stays instance-scoped even when
 * the labels API is empty or denied.
 */
export function useInstanceMetricIdentity(
  projectId: string | undefined,
  instanceName: string | undefined
): { identity: InstanceMetricIdentity | undefined; isLoading: boolean } {
  const match = projectId ? instanceSeriesMatch(projectId) : undefined;
  const results = useQueries({
    queries: INSTANCE_IDENTITY_LABELS.map((label) => ({
      queryKey: [PLUGIN_ID, 'prometheus-labels', label, match],
      enabled: !!projectId && !!instanceName && !!match,
      queryFn: () => fetchPrometheusLabelValues(label, match as string),
      staleTime: 5 * 60_000,
      retry: false,
    })),
  });

  const isLoading = results.some((result) => result.isLoading);
  if (!instanceName) {
    return { identity: undefined, isLoading };
  }

  for (let i = 0; i < INSTANCE_IDENTITY_LABELS.length; i += 1) {
    const values = results[i]?.data;
    if (values?.includes(instanceName)) {
      return {
        identity: { label: INSTANCE_IDENTITY_LABELS[i], value: instanceName },
        isLoading,
      };
    }
  }

  if (isLoading) {
    return { identity: undefined, isLoading };
  }

  return { identity: { label: 'name', value: instanceName }, isLoading: false };
}
