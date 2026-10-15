/**
 * Instance Metrics tab: CPU/memory always, plus ALB traffic when the workload
 * is published on a URL. Charts query the portal VictoriaMetrics endpoint.
 */
import { MetricAreaChart, formatCardValue } from '../components/metric-area-chart';
import { MetricsKpiCell, MetricsKpiRow } from '../components/metrics-kpis';
import { MetricsTimeRangeToolbar } from '../components/metrics-toolbar';
import { useInstanceOutlet } from './instance-outlet-context';
import {
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  cpuUsageQuery,
  memoryUsageQuery,
  useInstanceMetricIdentity,
} from '../lib/metrics-queries';
import {
  cpuUsageHint,
  formatCpuUsage,
  formatMemoryUsage,
  memoryUsageHint,
  parseCpuCores,
  parseMemoryBytes,
} from '../lib/resource-usage';
import { useMetricsTimeRange } from '../lib/metrics-time-range';
import { usePrometheusCard, type MetricFormat } from '../lib/prometheus';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { useMemo } from 'react';

const NOT_CONNECTED = 'Not connected';
const NETWORK_IO_UNAVAILABLE = 'Not collected yet';

function useKpi(query: string | undefined, format: MetricFormat, enabled: boolean) {
  return usePrometheusCard(query, format, { enabled: enabled && !!query });
}

export default function InstanceMetrics() {
  const { instance, projectId, proxyId } = useInstanceOutlet();
  const range = useMetricsTimeRange();
  const { identity, isLoading: identityLoading, isDenied: identityDenied } =
    useInstanceMetricIdentity(projectId, instance);

  // Percent needs the instance's size; fall back to cores when it is unknown.
  const allocatedCores = parseCpuCores(instance.cpu);
  const allocatedMemory = parseMemoryBytes(instance.memory);
  const cpuDivisor = useMemo(
    () => (allocatedCores ? () => allocatedCores : undefined),
    [allocatedCores]
  );
  const memoryDivisor = useMemo(
    () => (allocatedMemory ? () => allocatedMemory : undefined),
    [allocatedMemory]
  );

  const cpuQuery = projectId && identity ? cpuUsageQuery(projectId, identity) : undefined;
  const memoryQuery = projectId && identity ? memoryUsageQuery(projectId, identity) : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const p99Query = projectId && proxyId ? albP99Query(projectId, proxyId) : undefined;
  const errorQuery = projectId && proxyId ? albErrorRateQuery(projectId, proxyId) : undefined;

  const cpuCard = useKpi(cpuQuery, 'number', !identityLoading);
  const memoryCard = useKpi(memoryQuery, 'bytes', !identityLoading);
  const rpsCard = useKpi(rpsQuery, 'requestsPerSecond', !!proxyId);
  const p99Card = useKpi(p99Query, 'milliseconds-auto', !!proxyId);
  const errorCard = useKpi(errorQuery, 'percent', !!proxyId);

  const chartsEnabled = !identityLoading && !identityDenied && !!identity;

  return (
    <div className="flex flex-col gap-6" data-testid="compute-plugin-instance-metrics-page">
      <MetricsTimeRangeToolbar range={range} />

      <Card size="sm" sectioned className="w-full overflow-hidden">
        <CardContent>
          <MetricsKpiRow>
            <MetricsKpiCell
              label="CPU"
              value={identityLoading ? 'Loading…' : formatCpuUsage(cpuCard.data?.value, allocatedCores)}
              hint={cpuUsageHint(cpuCard.data?.value, allocatedCores)}
            />
            <MetricsKpiCell
              label="Memory"
              value={identityLoading ? 'Loading…' : formatMemoryUsage(memoryCard.data?.value, allocatedMemory)}
              hint={memoryUsageHint(memoryCard.data?.value, allocatedMemory)}
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
          query={cpuQuery}
          timeRange={range.timeRange}
          format={allocatedCores ? 'percent' : 'cores'}
          seriesDivisor={cpuDivisor}
          rawFormat="cores"
          enabled={chartsEnabled}
          pending={identityLoading}
          denied={identityDenied}
        />
        <MetricAreaChart
          title="Memory"
          query={memoryQuery}
          timeRange={range.timeRange}
          format={allocatedMemory ? 'percent' : 'bytes'}
          seriesDivisor={memoryDivisor}
          rawFormat="bytes"
          enabled={chartsEnabled}
          pending={identityLoading}
          denied={identityDenied}
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
