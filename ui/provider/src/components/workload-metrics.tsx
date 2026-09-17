/**
 * Workload Metrics tab: CPU/memory always, plus ALB traffic when published.
 * Charts query staff-portal `POST /api/metrics`.
 */
import { MetricAreaChart, formatKpiValue } from './metric-area-chart';
import {
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  type InstanceIdentityLabel,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from '../lib/metrics-queries';
import {
  lastHour,
  usePrometheusCard,
  type MetricFormat,
  type PrometheusTimeRange,
} from '../lib/prometheus';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { ChartColumnIncreasingIcon } from 'lucide-react';
import { useMemo } from 'react';

const COMING_SOON = 'Coming soon';

function KpiCell({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  const comingSoon = value === 'Coming soon';
  return (
    <div className="flex min-w-24 flex-1 flex-col gap-1 px-3 py-3">
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</p>
      <p
        className={
          comingSoon
            ? 'text-muted-foreground text-sm font-medium'
            : 'text-sm font-medium sm:text-base'
        }>
        {value}
      </p>
      {hint && !comingSoon ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

function useKpi(query: string | undefined, format: MetricFormat, enabled: boolean) {
  return usePrometheusCard(query, format, { enabled: enabled && !!query });
}

export function WorkloadMetrics({
  projectName,
  instanceKeys,
  identityLabel,
  identityLoading,
  proxyId,
}: {
  projectName?: string;
  instanceKeys: readonly string[];
  identityLabel?: InstanceIdentityLabel;
  identityLoading: boolean;
  proxyId?: string;
}) {
  const timeRange = useMemo<PrometheusTimeRange>(() => lastHour(), []);
  const chartsEnabled = !identityLoading && !!identityLabel && !!projectName && instanceKeys.length > 0;
  const cpuQuery =
    chartsEnabled && identityLabel && projectName
      ? workloadCpuAvgQuery(projectName, identityLabel, instanceKeys)
      : undefined;
  const memoryQuery =
    chartsEnabled && identityLabel && projectName
      ? workloadMemoryAvgQuery(projectName, identityLabel, instanceKeys)
      : undefined;
  const rpsQuery = projectName && proxyId ? albRpsQuery(projectName, proxyId) : undefined;
  const p99Query = projectName && proxyId ? albP99Query(projectName, proxyId) : undefined;
  const errorQuery = projectName && proxyId ? albErrorRateQuery(projectName, proxyId) : undefined;

  const cpuCard = useKpi(cpuQuery, 'number', chartsEnabled);
  const memoryCard = useKpi(memoryQuery, 'bytes', chartsEnabled);
  const rpsCard = useKpi(rpsQuery, 'requestsPerSecond', !!proxyId);
  const p99Card = useKpi(p99Query, 'milliseconds-auto', !!proxyId);
  const errorCard = useKpi(errorQuery, 'percent', !!proxyId);
  const resourceSoon = !identityLoading && !identityLabel;

  return (
    <div className="flex flex-col gap-6" data-testid="provider-plugin-workload-metrics">
      <Card size="sm" sectioned className="w-full overflow-hidden">
        <CardHeader size="sm" bordered>
          <CardTitle className="flex items-center gap-2 text-sm">
            <ChartColumnIncreasingIcon className="text-secondary size-4 stroke-2" />
            Last hour
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="divide-border border-border flex divide-x overflow-x-auto overscroll-x-contain rounded-lg border">
            <KpiCell
              label="CPU"
              value={resourceSoon ? COMING_SOON : (cpuCard.data?.formattedValue ?? formatKpiValue(cpuCard.data?.value, 'number'))}
              hint="avg cores"
            />
            <KpiCell
              label="Memory"
              value={resourceSoon ? COMING_SOON : (memoryCard.data?.formattedValue ?? formatKpiValue(memoryCard.data?.value, 'bytes'))}
              hint="avg"
            />
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
                <KpiCell label="Requests" value={COMING_SOON} />
                <KpiCell label="p99" value={COMING_SOON} />
                <KpiCell label="Errors" value={COMING_SOON} />
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
          unavailable={resourceSoon}
        />
        <MetricAreaChart
          title="Memory"
          query={memoryQuery}
          timeRange={timeRange}
          format="bytes"
          enabled={chartsEnabled}
          unavailable={resourceSoon}
        />
      </div>

      <MetricAreaChart
        title="Network I/O"
        query={undefined}
        timeRange={timeRange}
        format="bytesPerSecond"
        enabled={false}
        unavailable
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
