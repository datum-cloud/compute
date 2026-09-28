/**
 * Workload Metrics tab: CPU/memory aggregated across every instance, plus
 * ALB traffic when the workload is published. Region / instance filters
 * scope the CPU and memory numbers; the per-instance charts draw the busiest
 * few replicas until specific instances are picked.
 */
import { InstanceFilters } from '../components/instance-filters';
import { MetricAreaChart, formatCardValue } from '../components/metric-area-chart';
import { MetricsKpiCell, MetricsKpiRow } from '../components/metrics-kpis';
import { MetricsTimeRangeToolbar } from '../components/metrics-toolbar';
import { useWorkloadOutlet } from './workload-outlet-context';
import { useInstanceFilters } from '../lib/instance-filters';
import {
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  identityValues,
  workloadCpuByInstanceQuery,
  workloadCpuSumQuery,
  workloadMemoryByInstanceQuery,
  workloadMemorySumQuery,
} from '../lib/metrics-queries';
import { useMetricsTimeRange } from '../lib/metrics-time-range';
import { usePrometheusCard, type MetricFormat } from '../lib/prometheus';
import { MAX_INSTANCE_SERIES, shortInstanceLabels } from '../lib/series-view';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { useMemo } from 'react';

const NOT_CONNECTED = 'Not connected';
const NETWORK_IO_UNAVAILABLE = 'Not collected yet';
const NO_MATCHING_INSTANCES = 'No instances match these filters';

function resourceKpi(
  loading: boolean,
  value: Parameters<typeof formatCardValue>[0],
  format: Parameters<typeof formatCardValue>[1]
): string {
  if (loading) return 'Loading…';
  return formatCardValue(value, format);
}

function useKpi(query: string | undefined, format: MetricFormat, enabled: boolean) {
  return usePrometheusCard(query, format, { enabled: enabled && !!query });
}

export default function WorkloadMetrics() {
  const {
    projectId,
    proxyId,
    instances,
    locationIndex,
    identityLabel,
    identityLoading,
    identityDenied,
  } = useWorkloadOutlet();
  const range = useMetricsTimeRange();
  const filters = useInstanceFilters(instances, locationIndex);
  const metricKeys = useMemo(() => identityValues(filters.matching), [filters.matching]);
  // Labels come from every instance so a series keeps its name as filters change.
  const seriesLabels = useMemo(
    () => shortInstanceLabels(instances.map((instance) => instance.name)),
    [instances]
  );
  const noMatch = filters.isFiltered && filters.matching.length === 0;
  const scopeHint = filters.isFiltered
    ? `${filters.matching.length} of ${instances.length} instances`
    : undefined;

  const chartsEnabled =
    !identityLoading && !identityDenied && !!identityLabel && !!projectId && metricKeys.length > 0;
  const cpuSumQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadCpuSumQuery(projectId, identityLabel, metricKeys)
      : undefined;
  const memorySumQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadMemorySumQuery(projectId, identityLabel, metricKeys)
      : undefined;
  const cpuByInstanceQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadCpuByInstanceQuery(projectId, identityLabel, metricKeys)
      : undefined;
  const memoryByInstanceQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadMemoryByInstanceQuery(projectId, identityLabel, metricKeys)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const p99Query = projectId && proxyId ? albP99Query(projectId, proxyId) : undefined;
  const errorQuery = projectId && proxyId ? albErrorRateQuery(projectId, proxyId) : undefined;

  const cpuCard = useKpi(cpuSumQuery, 'number', chartsEnabled);
  const memoryCard = useKpi(memorySumQuery, 'bytes', chartsEnabled);
  const rpsCard = useKpi(rpsQuery, 'requestsPerSecond', !!proxyId);
  const p99Card = useKpi(p99Query, 'milliseconds-auto', !!proxyId);
  const errorCard = useKpi(errorQuery, 'percent', !!proxyId);

  return (
    <div className="flex flex-col gap-6" data-testid="compute-plugin-workload-metrics-page">
      <MetricsTimeRangeToolbar
        range={range}
        filters={
          instances.length > 1 ? (
            <InstanceFilters filters={filters} />
          ) : null
        }
      />

      <Card size="sm" sectioned className="w-full overflow-hidden">
        <CardContent>
          <MetricsKpiRow>
            <MetricsKpiCell
              label="CPU"
              value={noMatch ? '—' : resourceKpi(identityLoading, cpuCard.data, 'number')}
              hint={scopeHint ? `cores · ${scopeHint}` : 'total cores'}
            />
            <MetricsKpiCell
              label="Memory"
              value={noMatch ? '—' : resourceKpi(identityLoading, memoryCard.data, 'bytes')}
              hint={scopeHint ?? 'total'}
            />
            {proxyId ? (
              <>
                <MetricsKpiCell
                  label="Requests"
                  value={formatCardValue(rpsCard.data, 'requestsPerSecond')}
                  hint="load balancer"
                />
                <MetricsKpiCell
                  label="p99"
                  value={formatCardValue(p99Card.data, 'milliseconds-auto')}
                  hint="load balancer"
                />
                <MetricsKpiCell
                  label="Errors"
                  value={formatCardValue(errorCard.data, 'percent')}
                  hint="load balancer"
                />
              </>
            ) : (
              <>
                <MetricsKpiCell label="Requests" value={NOT_CONNECTED} />
                <MetricsKpiCell label="p99" value={NOT_CONNECTED} />
                <MetricsKpiCell label="Errors" value={NOT_CONNECTED} />
              </>
            )}
          </MetricsKpiRow>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <MetricAreaChart
          title="CPU"
          query={cpuByInstanceQuery}
          timeRange={range.timeRange}
          format="number"
          enabled={chartsEnabled}
          pending={identityLoading}
          denied={identityDenied}
          unavailable={noMatch}
          unavailableLabel={NO_MATCHING_INSTANCES}
          maxSeries={filters.instancesPicked ? undefined : MAX_INSTANCE_SERIES}
          seriesLabels={seriesLabels}
        />
        <MetricAreaChart
          title="Memory"
          query={memoryByInstanceQuery}
          timeRange={range.timeRange}
          format="bytes"
          enabled={chartsEnabled}
          pending={identityLoading}
          denied={identityDenied}
          unavailable={noMatch}
          unavailableLabel={NO_MATCHING_INSTANCES}
          maxSeries={filters.instancesPicked ? undefined : MAX_INSTANCE_SERIES}
          seriesLabels={seriesLabels}
        />
      </div>

      <MetricAreaChart
        title="Network I/O"
        query={undefined}
        timeRange={range.timeRange}
        format="bytesPerSecond"
        enabled={false}
        unavailable
        unavailableLabel={NETWORK_IO_UNAVAILABLE}
      />

      {proxyId ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <MetricAreaChart
            title="Requests"
            query={rpsQuery}
            timeRange={range.timeRange}
            format="requestsPerSecond"
            color="var(--color-chart-2)"
          />
          <MetricAreaChart
            title="Latency p99"
            query={p99Query}
            timeRange={range.timeRange}
            format="milliseconds-auto"
            color="var(--color-chart-1)"
          />
          <MetricAreaChart
            title="Error rate"
            query={errorQuery}
            timeRange={range.timeRange}
            format="percent"
            color="var(--color-chart-3)"
          />
        </div>
      ) : null}
    </div>
  );
}
