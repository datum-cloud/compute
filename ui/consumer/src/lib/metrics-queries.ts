/**
 * PromQL for instance resource metrics and (when published) ALB traffic.
 *
 * CPU/memory series are `datum_compute_instance_*`, federated into the portal
 * VictoriaMetrics with `resourcemanager_datumapis_com_project_name`. Federation
 * drops `pod`/`name`. Unikraft labels them with `instance_uuid` (same value as
 * `container_id`), which is the unikernel UUID, not Instance.metadata.uid.
 * There is no instance name on these series, so PromQL can only pin an instance
 * when that Unikraft UUID is known.
 *
 * Per-instance CPU/memory is therefore intentionally OFF today: identity only
 * resolves if `Instance.metadata.uid` ever shows up as `instance_uuid` (the
 * probe below), which it currently does not. Until federation keeps `pod`/`name`
 * or Instance status exposes the Unikraft UUID, the UI shows requested
 * resources and "Coming soon" rather than guessing at series.
 *
 * ALB series match cloud-portal edge metrics: Envoy `gateway_name` = HTTPProxy
 * name. Those charts are workload-scoped, not per-instance.
 */
import { PLUGIN_ID } from './api';
import { fetchPrometheusLabelValues } from './prometheus';
import { useQuery } from '@tanstack/react-query';

export const CPU_METRIC = 'datum_compute_instance_cpu_usage_seconds_total';
export const MEMORY_METRIC = 'datum_compute_instance_memory_working_set_bytes';
export const ENVOY_RQ_METRIC = 'envoy_vhost_vcluster_upstream_rq';
export const ENVOY_RQ_TIME_METRIC = 'envoy_vhost_vcluster_upstream_rq_time_bucket';

export const REGION_LABEL = 'label_topology_kubernetes_io_region';

/** Labels that can pin an instance series (see header: only `instance_uuid` survives federation). */
export const INSTANCE_IDENTITY_LABELS = ['instance_uuid'] as const;

export type InstanceIdentityLabel = (typeof INSTANCE_IDENTITY_LABELS)[number];

export interface InstanceMetricIdentity {
  label: InstanceIdentityLabel;
  value: string;
}

export interface InstanceIdentitySource {
  name: string;
  uid: string;
  location?: string;
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

/**
 * Rate window for instant ALB "card" values (topology node, metric tiles).
 * Charts use 1m for resolution; instant queries use this wider window so
 * bursty, low-volume traffic such as a handful of 503s does not fall between
 * scrapes and read as 0. Every headline number on a page should use the same
 * window so they agree with each other.
 */
export const ALB_INSTANT_WINDOW = '5m';

/** `window` is the rate window; see `ALB_INSTANT_WINDOW`. */
export function albRpsQuery(projectId: string, proxyId: string, window = '1m'): string {
  return `sum(rate(${ENVOY_RQ_METRIC}${albSelector(projectId, proxyId)}[${window}]))`;
}

export function albRpsQueryMany(projectId: string, proxyIds: readonly string[]): string | undefined {
  const match = namesMatcher('gateway_name', proxyIds);
  if (!match) return undefined;
  return `sum(rate(${ENVOY_RQ_METRIC}{resourcemanager_datumapis_com_project_name="${escapePromQL(projectId)}",${match},gateway_namespace="default",${REGION_LABEL}!=""}[1m]))`;
}

export function albP99Query(projectId: string, proxyId: string, window = '1m'): string {
  return `histogram_quantile(0.99, sum(rate(${ENVOY_RQ_TIME_METRIC}${albSelector(projectId, proxyId)}[${window}])) by (le))`;
}

export function albErrorRateQuery(projectId: string, proxyId: string, window = '1m'): string {
  const errors = `sum(rate(${ENVOY_RQ_METRIC}${albSelector(projectId, proxyId, { envoy_response_code: '=~"[45].."' })}[${window}]))`;
  const total = albRpsQuery(projectId, proxyId, window);
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

/** Values to match against `instance_uuid` for a set of instances. */
export function identityValues(instances: readonly InstanceIdentitySource[]): string[] {
  return [...new Set(instances.map((instance) => instance.uid).filter(Boolean))];
}

/**
 * Pin an instance only when its Kubernetes UID appears as `instance_uuid` on
 * federated series. Unikraft currently uses a different UUID and does not
 * publish pod/name, so identity stays unset rather than widening to a region.
 */
export function useInstanceMetricIdentity(
  projectId: string | undefined,
  instance: InstanceIdentitySource | undefined
): { identity: InstanceMetricIdentity | undefined; isLoading: boolean } {
  const match = projectId ? instanceSeriesMatch(projectId) : undefined;
  const uuids = useQuery({
    queryKey: [PLUGIN_ID, 'prometheus-labels', 'instance_uuid', match],
    enabled: !!projectId && !!instance && !!match,
    queryFn: () => fetchPrometheusLabelValues('instance_uuid', match as string),
    staleTime: 5 * 60_000,
    retry: false,
  });

  if (!instance) {
    return { identity: undefined, isLoading: false };
  }

  if (uuids.isLoading) {
    return { identity: undefined, isLoading: true };
  }

  if (instance.uid && uuids.data?.includes(instance.uid)) {
    return { identity: { label: 'instance_uuid', value: instance.uid }, isLoading: false };
  }

  return { identity: undefined, isLoading: false };
}
