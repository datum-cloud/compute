/**
 * Instance Metrics tab: CPU/memory always, plus ALB traffic when the workload
 * is published on a URL. Charts query the portal VictoriaMetrics endpoint.
 */
import { MetricAreaChart, formatKpiValue } from '../components/metric-area-chart';
import { useInstanceOutlet } from './instance-outlet-context';
import {
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  cpuUsageQuery,
  memoryUsageQuery,
  networkIoQuery,
  useInstanceMetricIdentity,
} from '../lib/metrics-queries';
import {
  lastHour,
  usePrometheusCard,
  type MetricFormat,
  type PrometheusTimeRange,
} from '../lib/prometheus';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { ChartColumnIncreasingIcon } from 'lucide-react';
import { useMemo } from 'react';

function KpiCell({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="flex min-w-24 flex-1 flex-col gap-1 px-3 py-3">
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</p>
      <p className="text-sm font-medium sm:text-base">{value}</p>
      {hint ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

function useKpi(
  query: string | undefined,
  format: MetricFormat,
  enabled: boolean
) {
  return usePrometheusCard(query, format, { enabled: enabled && !!query });
}

export default function InstanceMetrics() {
  const { instance, projectId, proxyId } = useInstanceOutlet();
  const timeRange = useMemo<PrometheusTimeRange>(() => lastHour(), []);
  const { identity, isLoading: identityLoading } = useInstanceMetricIdentity(
    projectId,
    instance.name
  );

  const cpuQuery = projectId && identity ? cpuUsageQuery(projectId, identity) : undefined;
  const memoryQuery = projectId && identity ? memoryUsageQuery(projectId, identity) : undefined;
  const networkQuery = projectId && identity ? networkIoQuery(projectId, identity) : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const p99Query = projectId && proxyId ? albP99Query(projectId, proxyId) : undefined;
  const errorQuery = projectId && proxyId ? albErrorRateQuery(projectId, proxyId) : undefined;

  const cpuCard = useKpi(cpuQuery, 'number', !identityLoading);
  const memoryCard = useKpi(memoryQuery, 'bytes', !identityLoading);
  const rpsCard = useKpi(rpsQuery, 'requestsPerSecond', !!proxyId);
  const p99Card = useKpi(p99Query, 'milliseconds-auto', !!proxyId);
  const errorCard = useKpi(errorQuery, 'percent', !!proxyId);

  const chartsEnabled = !identityLoading && !!identity;

  return (
    <div className="flex flex-col gap-6" data-testid="compute-plugin-instance-metrics-page">
      <Card size="sm" sectioned className="w-full overflow-hidden">
        <CardHeader size="sm" bordered>
          <CardTitle className="flex items-center gap-2 text-sm">
            <Icon icon={ChartColumnIncreasingIcon} size={16} className="text-secondary" />
            Last hour
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="divide-border border-border flex divide-x overflow-x-auto overscroll-x-contain rounded-lg border">
            <KpiCell label="CPU" value={cpuCard.data?.formattedValue ?? formatKpiValue(cpuCard.data?.value, 'number')} hint="cores" />
            <KpiCell label="Memory" value={memoryCard.data?.formattedValue ?? formatKpiValue(memoryCard.data?.value, 'bytes')} />
            {proxyId ? (
              <>
                <KpiCell
                  label="Requests"
                  value={rpsCard.data?.formattedValue ?? formatKpiValue(rpsCard.data?.value, 'requestsPerSecond')}
                  hint="load balancer"
                />
                <KpiCell
                  label="p99"
                  value={p99Card.data?.formattedValue ?? formatKpiValue(p99Card.data?.value, 'milliseconds-auto')}
                  hint="load balancer"
                />
                <KpiCell
                  label="Errors"
                  value={errorCard.data?.formattedValue ?? formatKpiValue(errorCard.data?.value, 'percent')}
                  hint="load balancer"
                />
              </>
            ) : (
              <>
                <KpiCell label="Requests" value="—" hint="publish a URL" />
                <KpiCell label="p99" value="—" hint="publish a URL" />
                <KpiCell label="Errors" value="—" hint="publish a URL" />
              </>
            )}
          </div>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <MetricAreaChart
          title="CPU"
          query={cpuQuery}
          timeRange={timeRange}
          format="number"
          enabled={chartsEnabled}
        />
        <MetricAreaChart
          title="Memory"
          query={memoryQuery}
          timeRange={timeRange}
          format="bytes"
          enabled={chartsEnabled}
        />
      </div>

      <MetricAreaChart
        title="Network I/O"
        query={networkQuery}
        timeRange={timeRange}
        format="bytesPerSecond"
        enabled={chartsEnabled}
      />

      {proxyId ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <MetricAreaChart
            title="Requests"
            query={rpsQuery}
            timeRange={timeRange}
            format="requestsPerSecond"
            color="var(--color-chart-2)"
          />
          <MetricAreaChart
            title="Latency p99"
            query={p99Query}
            timeRange={timeRange}
            format="milliseconds-auto"
            color="var(--color-chart-1)"
          />
          <MetricAreaChart
            title="Error rate"
            query={errorQuery}
            timeRange={timeRange}
            format="percent"
            color="var(--color-chart-3)"
          />
        </div>
      ) : null}

    </div>
  );
}
