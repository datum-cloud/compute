/**
 * Compact area chart over portal-formatted Prometheus series.
 *
 * Host `MetricChart` is portal-internal; this plugin bundles `@datum-cloud/datum-ui/chart`
 * + recharts instead. Height is inline so we do not depend on host Tailwind
 * arbitrary values.
 */
import {
  transformForRecharts,
  usePrometheusChart,
  type MetricFormat,
  type PrometheusTimeRange,
} from '../lib/prometheus';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { ChartContainer, ChartTooltip, type ChartConfig } from '@datum-cloud/datum-ui/chart';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useId, useMemo } from 'react';
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts';

const SERIES_COLORS = [
  'var(--color-chart-2)',
  'var(--color-chart-1)',
  'var(--color-chart-3)',
  'var(--primary)',
] as const;

function formatBytes(value: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let n = value;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

/** SI-scale CPU-style numbers so a 0.00008 domain does not label every tick 0.0. */
function formatCompactNumber(value: number, forTick: boolean): string {
  if (Math.abs(value) < 1e-12) return '0';
  const sign = value < 0 ? '-' : '';
  const n = Math.abs(value);

  const scaled = (x: number, suffix: string) => {
    const digits = x >= 10 ? 0 : forTick ? 1 : 2;
    return `${sign}${Number(x.toFixed(digits))}${suffix}`;
  };

  if (n >= 1_000_000) return scaled(n / 1_000_000, 'M');
  if (n >= 1000) return scaled(n / 1000, 'k');
  if (n >= 10) return `${sign}${Math.round(n)}`;
  if (n >= 1) return `${sign}${n.toFixed(forTick ? 1 : 2)}`;
  if (n >= 0.01) return `${sign}${n.toFixed(2)}`;
  if (n >= 1e-3) return scaled(n * 1e3, 'm');
  if (n >= 1e-6) return scaled(n * 1e6, 'µ');
  return `${sign}${n.toExponential(1)}`;
}

function formatAxisValue(value: number, format: MetricFormat): string {
  if (!Number.isFinite(value)) return '—';
  switch (format) {
    case 'bytes':
      return formatBytes(value);
    case 'bytesPerSecond':
      return `${formatBytes(value)}/s`;
    case 'percent': {
      // Keep one decimal below 10% so a 0.4% error rate does not read as "0%".
      const pct = value * 100;
      return `${pct > 0 && pct < 10 ? pct.toFixed(1) : pct.toFixed(0)}%`;
    }
    case 'requestsPerSecond':
      return value >= 10 ? `${value.toFixed(0)}/s` : `${value.toFixed(2)}/s`;
    case 'milliseconds':
    case 'milliseconds-auto':
      return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${value.toFixed(0)}ms`;
    default:
      return formatCompactNumber(value, false);
  }
}

function formatAxisTick(value: number, format: MetricFormat): string {
  if (!Number.isFinite(value)) return '';
  switch (format) {
    case 'bytes':
      return formatBytes(value);
    case 'bytesPerSecond':
      return `${formatBytes(value)}/s`;
    case 'percent':
      return `${(value * 100).toFixed(0)}%`;
    case 'milliseconds':
    case 'milliseconds-auto':
      return value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}`;
    default:
      return formatCompactNumber(value, true);
  }
}

function axisWidth(format: MetricFormat): number {
  return format === 'bytes' || format === 'bytesPerSecond' ? 48 : 36;
}

const SIX_HOURS_MS = 6 * 60 * 60 * 1000;
const TWO_DAYS_MS = 48 * 60 * 60 * 1000;

function formatTimeTick(timestamp: number, rangeMs: number): string {
  const date = new Date(timestamp);
  if (rangeMs < SIX_HOURS_MS) {
    return date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  }
  if (rangeMs < TWO_DAYS_MS) {
    return date.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function seriesLabel(name: string, title: string): string {
  return !name || name === 'Series' ? title : name;
}

export function MetricAreaChart({
  query,
  timeRange,
  title,
  format = 'number',
  color = 'var(--primary)',
  enabled = true,
  unavailable = false,
  unavailableLabel = 'No data',
  pending = false,
  denied = false,
  className,
  height = 224,
  embedded = false,
  fill = false,
}: {
  query: string | undefined;
  timeRange: PrometheusTimeRange;
  title: string;
  format?: MetricFormat;
  color?: string;
  enabled?: boolean;
  unavailable?: boolean;
  unavailableLabel?: string;
  /** Identity probe (or similar) is still in flight — show Loading instead of No data. */
  pending?: boolean;
  /** Identity probe (or similar) returned 401/403. */
  denied?: boolean;
  className?: string;
  height?: number;
  /** Skip the Card chrome so this can sit inside another card. */
  embedded?: boolean;
  /** Grow to the parent instead of a fixed pixel height. */
  fill?: boolean;
}) {
  const gradientId = useId().replace(/[^a-zA-Z0-9_-]/g, '');
  const { data, isLoading, error } = usePrometheusChart(query, timeRange, {
    enabled: enabled && !unavailable && !denied && !pending && !!query,
  });
  const chartData = useMemo(() => (data ? transformForRecharts(data) : []), [data]);
  const series = data?.series ?? [];
  const rangeMs = Math.max(1, timeRange.end.getTime() - timeRange.start.getTime());
  const xDomain = useMemo<[number, number]>(
    () => [timeRange.start.getTime(), timeRange.end.getTime()],
    [timeRange.start, timeRange.end]
  );

  const chartConfig: ChartConfig = useMemo(() => {
    const config: ChartConfig = {};
    if (series.length === 0) {
      config.value = { label: title, color };
      return config;
    }
    series.forEach((item, index) => {
      config[item.name] = {
        label: seriesLabel(item.name, title),
        color: item.color || SERIES_COLORS[index % SERIES_COLORS.length] || color,
      };
    });
    return config;
  }, [series, title, color]);

  const body = (
    <div
      className={fill ? 'relative min-h-40 w-full flex-1' : undefined}
      style={fill ? undefined : { height }}>
      <div className={fill ? 'absolute inset-0' : 'h-full'}>
      {denied ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          You don't have permission to view metrics
        </div>
      ) : pending ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          Loading…
        </div>
      ) : unavailable ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          {unavailableLabel}
        </div>
      ) : isLoading ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          Loading…
        </div>
      ) : error ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          {error.status === 403 || error.status === 401
            ? "You don't have permission to view metrics"
            : 'Unable to load metrics'}
        </div>
      ) : chartData.length === 0 ? (
        <div className="text-muted-foreground flex h-full items-center justify-center text-xs">
          No data
        </div>
      ) : (
        <ChartContainer
          config={chartConfig}
          className="h-full w-full overflow-visible"
          // Inline: the host does not compile `aspect-auto`; without it ChartContainer keeps aspect-video.
          style={{ aspectRatio: 'auto' }}>
          <AreaChart
            key={`${xDomain[0]}-${xDomain[1]}`}
            data={chartData}
            margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
            <defs>
              {series.map((item, index) => {
                const stroke = item.color || SERIES_COLORS[index % SERIES_COLORS.length] || color;
                return (
                  <linearGradient
                    key={item.name}
                    id={`${gradientId}-${index}`}
                    x1="0"
                    y1="0"
                    x2="0"
                    y2="1">
                    <stop offset="5%" stopColor={stroke} stopOpacity={0.25} />
                    <stop offset="95%" stopColor={stroke} stopOpacity={0} />
                  </linearGradient>
                );
              })}
            </defs>
            <CartesianGrid strokeDasharray="3 3" vertical={false} />
            <XAxis
              dataKey="timestamp"
              type="number"
              scale="time"
              domain={xDomain}
              allowDataOverflow
              tickFormatter={(value: number) => formatTimeTick(value, rangeMs)}
              tickLine={false}
              axisLine={false}
              minTickGap={24}
              tick={{ fill: 'var(--muted-foreground)', fontSize: 11 }}
            />
            <YAxis
              tickFormatter={(value: number) => formatAxisTick(value, format)}
              tickLine={false}
              axisLine={false}
              width={axisWidth(format)}
              tickCount={4}
              tickMargin={8}
              tick={{ fill: 'var(--muted-foreground)', fontSize: 10, textAnchor: 'end' }}
            />
            <ChartTooltip
              content={({ active, payload }) => {
                if (!active || !payload?.length) return null;
                const ts = Number(payload[0]?.payload?.timestamp);
                return (
                  <div className="border-border bg-background rounded-md border px-2 py-1 text-xs shadow-sm">
                    <div className="text-muted-foreground">{formatTimeTick(ts, rangeMs)}</div>
                    {payload.map((point) => (
                      <div key={String(point.dataKey)} className="flex items-center gap-2">
                        <span
                          className="size-1.5 shrink-0 rounded-full"
                          style={{ background: String(point.color || 'var(--primary)') }}
                        />
                        <span className="text-muted-foreground">
                          {seriesLabel(String(point.name), title)}
                        </span>
                        <span className="font-medium">
                          {formatAxisValue(Number(point.value), format)}
                        </span>
                      </div>
                    ))}
                  </div>
                );
              }}
            />
            {series.map((item, index) => {
              const stroke = item.color || SERIES_COLORS[index % SERIES_COLORS.length] || color;
              return (
                <Area
                  key={item.name}
                  type="monotone"
                  dataKey={item.name}
                  name={seriesLabel(item.name, title)}
                  stroke={stroke}
                  strokeWidth={1.5}
                  fill={`url(#${gradientId}-${index})`}
                  fillOpacity={1}
                  dot={false}
                  isAnimationActive={false}
                />
              );
            })}
          </AreaChart>
        </ChartContainer>
      )}
      </div>
    </div>
  );

  if (embedded) {
    return (
      <div
        className={cn(fill && 'flex h-full min-h-0 flex-1 flex-col', className)}
        data-testid={`compute-plugin-metric-chart-${title.toLowerCase().replace(/\s+/g, '-')}`}>
        {series.length > 1 ? (
          <div className="mb-2 flex flex-wrap items-center gap-3">
            {series.map((item, index) => (
              <span key={item.name} className="text-muted-foreground flex items-center gap-1.5 text-xs">
                <span
                  className="size-1.5 rounded-full"
                  style={{ background: item.color || SERIES_COLORS[index % SERIES_COLORS.length] || color }}
                />
                {seriesLabel(item.name, title)}
              </span>
            ))}
          </div>
        ) : null}
        {body}
      </div>
    );
  }

  return (
    <Card
      size="sm"
      sectioned
      className={cn('overflow-hidden', className)}
      data-testid={`compute-plugin-metric-chart-${title.toLowerCase().replace(/\s+/g, '-')}`}>
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-3 text-sm">
          {title}
          {series.length > 1
            ? series.map((item, index) => (
                <span
                  key={item.name}
                  className="text-muted-foreground flex items-center gap-1.5 text-xs font-normal">
                  <span
                    className="size-1.5 rounded-full"
                    style={{ background: item.color || SERIES_COLORS[index % SERIES_COLORS.length] || color }}
                  />
                  {seriesLabel(item.name, title)}
                </span>
              ))
            : null}
        </CardTitle>
      </CardHeader>
      <CardContent>{body}</CardContent>
    </Card>
  );
}

export function formatKpiValue(value: number | undefined, format: MetricFormat): string {
  if (value === undefined || !Number.isFinite(value)) return '—';
  return formatAxisValue(value, format);
}

/** Portal card `formattedValue` rounds tiny CPU to "0"; keep local SI formatting. */
export function formatCardValue(
  data: { value?: number; formattedValue?: string } | undefined,
  format: MetricFormat
): string {
  if (format === 'number') return formatKpiValue(data?.value, format);
  return data?.formattedValue ?? formatKpiValue(data?.value, format);
}
