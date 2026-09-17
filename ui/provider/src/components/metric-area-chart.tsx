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

function formatAxisValue(value: number, format: MetricFormat): string {
  if (!Number.isFinite(value)) return '—';
  switch (format) {
    case 'bytes':
      return formatBytes(value);
    case 'bytesPerSecond':
      return `${formatBytes(value)}/s`;
    case 'percent':
      return `${(value * 100).toFixed(0)}%`;
    case 'requestsPerSecond':
      return value >= 10 ? `${value.toFixed(0)}/s` : `${value.toFixed(2)}/s`;
    case 'milliseconds':
    case 'milliseconds-auto':
      return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${value.toFixed(0)}ms`;
    default:
      return value >= 10 ? value.toFixed(0) : value.toFixed(2);
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
      return value >= 10 ? value.toFixed(0) : value.toFixed(1);
  }
}

function axisWidth(format: MetricFormat): number {
  return format === 'bytes' || format === 'bytesPerSecond' ? 48 : 36;
}

function formatTimeTick(timestamp: number): string {
  const date = new Date(timestamp);
  return date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

export function MetricAreaChart({
  query,
  timeRange,
  title,
  format = 'number',
  color = 'var(--primary)',
  enabled = true,
  unavailable = false,
  unavailableLabel = 'Coming soon',
  className,
  height = 224,
  embedded = false,
}: {
  query: string | undefined;
  timeRange: PrometheusTimeRange;
  title: string;
  format?: MetricFormat;
  color?: string;
  enabled?: boolean;
  unavailable?: boolean;
  unavailableLabel?: string;
  className?: string;
  height?: number;
  /** Skip the Card chrome so this can sit inside another card. */
  embedded?: boolean;
}) {
  const gradientId = useId().replace(/:/g, '');
  const { data, isLoading, error } = usePrometheusChart(query, timeRange, {
    enabled: enabled && !unavailable && !!query,
  });
  const chartData = useMemo(() => (data ? transformForRecharts(data) : []), [data]);
  const series = data?.series ?? [];

  const chartConfig: ChartConfig = useMemo(() => {
    const config: ChartConfig = {};
    if (series.length === 0) {
      config.value = { label: title, color };
      return config;
    }
    series.forEach((item, index) => {
      config[item.name] = {
        label: item.name,
        color: item.color || SERIES_COLORS[index] || color,
      };
    });
    return config;
  }, [series, title, color]);

  const body = (
    <div style={{ height }}>
      {unavailable ? (
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
        <ChartContainer config={chartConfig} className="aspect-auto h-full w-full overflow-visible">
          <AreaChart data={chartData} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
            <defs>
              {series.map((item, index) => {
                const stroke = item.color || SERIES_COLORS[index] || color;
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
              domain={['dataMin', 'dataMax']}
              tickFormatter={formatTimeTick}
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
                    <div className="text-muted-foreground">{formatTimeTick(ts)}</div>
                    {payload.map((point) => (
                      <div key={String(point.dataKey)} className="flex items-center gap-2">
                        <span
                          className="size-1.5 shrink-0 rounded-full"
                          style={{ background: String(point.color || 'var(--primary)') }}
                        />
                        <span className="text-muted-foreground">{String(point.name)}</span>
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
              const stroke = item.color || SERIES_COLORS[index] || color;
              return (
                <Area
                  key={item.name}
                  type="monotone"
                  dataKey={item.name}
                  name={item.name}
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
  );

  if (embedded) {
    return (
      <div
        className={className}
        data-testid={`provider-plugin-metric-chart-${title.toLowerCase().replace(/\s+/g, '-')}`}>
        {series.length > 1 ? (
          <div className="mb-2 flex flex-wrap items-center gap-3">
            {series.map((item, index) => (
              <span key={item.name} className="text-muted-foreground flex items-center gap-1.5 text-xs">
                <span
                  className="size-1.5 rounded-full"
                  style={{ background: item.color || SERIES_COLORS[index] || color }}
                />
                {item.name}
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
      data-testid={`provider-plugin-metric-chart-${title.toLowerCase().replace(/\s+/g, '-')}`}>
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
                    style={{ background: item.color || SERIES_COLORS[index] || color }}
                  />
                  {item.name}
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
