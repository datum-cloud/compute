import { formatKpiValue } from './metric-area-chart';
import {
  isPrometheusDenied,
  transformForRecharts,
  usePrometheusChart,
  type MetricFormat,
  type PrometheusTimeRange,
} from '../lib/prometheus';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useId, useMemo, type ReactNode } from 'react';
import { Area, AreaChart, YAxis } from 'recharts';

/** Compact last-hour sparkline plus current value — table cells and list cards. */
export function MetricSparkline({
  query,
  timeRange,
  format,
  color = 'var(--primary)',
  emptyTitle,
  flatWhenZero = false,
  label,
  compact = false,
  wide = false,
  pending = false,
  denied = false,
}: {
  query?: string;
  timeRange: PrometheusTimeRange;
  format: MetricFormat;
  color?: string;
  emptyTitle?: string;
  /** Draw a baseline instead of a spark when every sample is 0 (ALB idle). */
  flatWhenZero?: boolean;
  label?: string;
  compact?: boolean;
  /** Grow the spark to the remaining row width (card view). */
  wide?: boolean;
  pending?: boolean;
  denied?: boolean;
}) {
  const gradientId = useId().replace(/[^a-zA-Z0-9_-]/g, '');
  const chart = usePrometheusChart(query, timeRange, { enabled: !!query && !denied });

  const dataKey = chart.data?.series[0]?.name || 'value';
  const rows = useMemo(() => {
    if (!chart.data || chart.error) return [];
    return transformForRecharts(chart.data).filter((row) => {
      const v = row[dataKey];
      return typeof v === 'number' && Number.isFinite(v);
    });
  }, [chart.data, chart.error, dataKey]);

  const max = rows.reduce((m, row) => Math.max(m, Number(row[dataKey])), 0);
  const last = rows.length > 0 ? Number(rows[rows.length - 1]?.[dataKey]) : undefined;
  const queryDenied = isPrometheusDenied(chart.error);
  const sparkClass = cn(compact ? 'h-6' : 'h-8', wide ? 'min-w-0 flex-1' : compact ? 'w-28' : 'w-40');
  const sparkHeight = compact ? 24 : 32;

  const labeled = (content: ReactNode) => (
    <div className="flex items-center gap-2">
      {label ? <span className="text-muted-foreground w-8 shrink-0 text-xs">{label}</span> : null}
      {content}
    </div>
  );

  if (denied || queryDenied) {
    return labeled(
      <span className="text-muted-foreground text-xs" title="You don't have permission to view metrics">
        —
      </span>
    );
  }
  if (pending || chart.isLoading) {
    return labeled(
      <div className={cn('flex items-center justify-center', sparkClass)}>
        <SpinnerIcon size="sm" />
      </div>
    );
  }
  if (!query) {
    return labeled(
      <span className="text-muted-foreground text-xs" title={emptyTitle}>
        —
      </span>
    );
  }

  const idle = rows.length < 2 || (flatWhenZero && max === 0);
  const yMax = max === 0 ? 1 : max * 1.1;
  return labeled(
    <>
      <div className={sparkClass} aria-hidden>
        {idle ? (
          <div className="flex h-full items-center">
            <div className="bg-border h-px w-full" />
          </div>
        ) : (
          <AreaChart
            data={rows}
            responsive
            width="100%"
            height={sparkHeight}
            margin={{ top: 2, right: 0, left: 0, bottom: 2 }}>
            <defs>
              <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={color} stopOpacity={0.3} />
                <stop offset="100%" stopColor={color} stopOpacity={0} />
              </linearGradient>
            </defs>
            <YAxis hide domain={[0, yMax]} />
            <Area
              type="monotone"
              dataKey={dataKey}
              stroke={color}
              strokeWidth={1.5}
              fill={`url(#${gradientId})`}
              fillOpacity={1}
              dot={false}
              activeDot={false}
              isAnimationActive={false}
            />
          </AreaChart>
        )}
      </div>
      <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
        {formatKpiValue(Number.isFinite(last) ? last : undefined, format)}
      </span>
    </>
  );
}

export function CpuMemorySparks({
  cpuQuery,
  memoryQuery,
  timeRange,
  compact = false,
  wide = false,
  pending = false,
  denied = false,
}: {
  cpuQuery?: string;
  memoryQuery?: string;
  timeRange: PrometheusTimeRange;
  compact?: boolean;
  wide?: boolean;
  pending?: boolean;
  denied?: boolean;
}) {
  if (denied) {
    return (
      <span className="text-muted-foreground text-xs" title="You don't have permission to view metrics">
        —
      </span>
    );
  }
  if (!pending && !cpuQuery && !memoryQuery) {
    return (
      <span className="text-muted-foreground text-xs" title="CPU and memory metrics aren't available yet">
        —
      </span>
    );
  }
  return (
    <div className={cn('flex flex-col', compact ? 'gap-1 py-0.5' : 'gap-2')}>
      <MetricSparkline
        query={cpuQuery}
        timeRange={timeRange}
        format="number"
        label="CPU"
        compact={compact}
        wide={wide}
        pending={pending}
      />
      <MetricSparkline
        query={memoryQuery}
        timeRange={timeRange}
        format="bytes"
        color="var(--color-chart-1)"
        label="Mem"
        compact={compact}
        wide={wide}
        pending={pending}
      />
    </div>
  );
}
