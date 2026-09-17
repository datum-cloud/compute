import {
  DEFAULT_OVERVIEW_RANGE,
  OVERVIEW_RANGE_OPTIONS,
  type OverviewRange,
  type OverviewRangeValue,
} from './overview-range';
import { SparklineStatCard } from './sparkline-stat-card';
import {
  ALB_INSTANT_WINDOW,
  albErrorRateQuery,
  albRpsQuery,
  type InstanceIdentityLabel,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from '../lib/metrics-queries';
import { Icon } from '@datum-cloud/datum-ui/icons';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@datum-cloud/datum-ui/select';
import { HistoryIcon } from 'lucide-react';

export function WorkloadMetricsStrip({
  projectId,
  proxyId,
  identityLabel,
  instanceKeys,
  range,
  onRangeChange,
  idle = false,
}: {
  projectId?: string;
  proxyId?: string;
  identityLabel?: InstanceIdentityLabel;
  instanceKeys: string[];
  range: OverviewRange;
  onRangeChange: (value: OverviewRangeValue) => void;
  idle?: boolean;
}) {
  const chartsEnabled = !!projectId && !!identityLabel && instanceKeys.length > 0;
  const cpuQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadCpuAvgQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const memoryQuery =
    chartsEnabled && identityLabel && projectId
      ? workloadMemoryAvgQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const errorQuery = projectId && proxyId ? albErrorRateQuery(projectId, proxyId) : undefined;
  // Headline numbers share the topology card's instant window so the two agree.
  const rpsHeadline =
    projectId && proxyId ? albRpsQuery(projectId, proxyId, ALB_INSTANT_WINDOW) : undefined;
  const errorHeadline =
    projectId && proxyId ? albErrorRateQuery(projectId, proxyId, ALB_INSTANT_WINDOW) : undefined;
  const windowLabel = `Last ${range.shortLabel}`;

  return (
    <section className="flex flex-col gap-6" aria-labelledby="compute-live-metrics-heading">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <h2 id="compute-live-metrics-heading" className="shrink-0 text-sm font-semibold">
            Live metrics
          </h2>
          {idle ? (
            <p className="text-muted-foreground truncate text-xs">
              <span aria-hidden="true">· </span>
              No data yet — metrics start streaming with the first request
            </p>
          ) : null}
        </div>
        <Select value={range.value} onValueChange={(value) => onRangeChange(value as OverviewRangeValue)}>
          <SelectTrigger
            className="bg-card h-8 w-auto gap-2 text-xs"
            aria-label="Live metrics time range"
            data-e2e="compute-overview-range"
            data-testid="compute-plugin-overview-range">
            <Icon icon={HistoryIcon} size={14} className="text-muted-foreground" />
            <SelectValue />
          </SelectTrigger>
          <SelectContent align="end">
            {OVERVIEW_RANGE_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value} className="text-xs">
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="grid grid-cols-2 gap-6 lg:grid-cols-4">
        <SparklineStatCard
          title="Requests"
          query={rpsQuery}
          headlineQuery={rpsHeadline}
          format="requestsPerSecond"
          color="var(--primary)"
          timeRange={range.timeRange}
          rangeLabel={windowLabel}
          idle={idle}
          unavailable={!proxyId}
        />
        <SparklineStatCard
          title="Error rate"
          query={errorQuery}
          headlineQuery={errorHeadline}
          format="percent"
          color="var(--color-chart-1)"
          timeRange={range.timeRange}
          rangeLabel={windowLabel}
          idle={idle}
          unavailable={!proxyId}
        />
        <SparklineStatCard
          title="Avg CPU"
          query={cpuQuery}
          format="number"
          color="var(--primary)"
          timeRange={range.timeRange}
          rangeLabel={windowLabel}
          unavailable={!chartsEnabled}
          unavailableLabel="Coming soon"
        />
        <SparklineStatCard
          title="Avg Memory"
          query={memoryQuery}
          format="bytes"
          color="var(--color-chart-1)"
          timeRange={range.timeRange}
          rangeLabel={windowLabel}
          unavailable={!chartsEnabled}
          unavailableLabel="Coming soon"
        />
      </div>
    </section>
  );
}

export { DEFAULT_OVERVIEW_RANGE };
