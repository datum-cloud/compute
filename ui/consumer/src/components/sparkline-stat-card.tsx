import { formatCardValue, formatKpiValue } from './metric-area-chart';
import {
  transformForRecharts,
  usePrometheusCard,
  usePrometheusChart,
  type MetricFormat,
  type PrometheusTimeRange,
} from '../lib/prometheus';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useId, useMemo } from 'react';
import { Link } from 'react-router';
import { Area, AreaChart, YAxis } from 'recharts';

export function SparklineStatCard({
  title,
  href,
  query,
  headlineQuery,
  format = 'number',
  color = 'var(--primary)',
  idle = false,
  unavailable = false,
  unavailableLabel = 'Not connected',
  pending = false,
  denied: identityDenied = false,
  timeRange,
  rangeLabel,
  value,
}: {
  title: string;
  href?: string;
  query?: string;
  /**
   * Instant query for the headline number when it should differ from the
   * chart series (e.g. a wider rate window). Defaults to `query`.
   */
  headlineQuery?: string;
  format?: MetricFormat;
  color?: string;
  idle?: boolean;
  unavailable?: boolean;
  unavailableLabel?: string;
  pending?: boolean;
  denied?: boolean;
  timeRange: PrometheusTimeRange;
  rangeLabel?: string;
  /** Static headline when this card is a count, not a PromQL series. */
  value?: string;
}) {
  const gradientId = useId().replace(/[^a-zA-Z0-9_-]/g, '');
  const enabled = !!query && !unavailable && !identityDenied && !pending;
  const chart = usePrometheusChart(query, timeRange, { enabled });
  const card = usePrometheusCard(headlineQuery ?? query, format, { enabled });

  const dataKey = chart.data?.series[0]?.name || 'value';
  const series = useMemo(() => {
    if (!chart.data || chart.error) return [];
    const rows = transformForRecharts(chart.data)
      .map((row) => {
        const raw = row[dataKey];
        return typeof raw === 'number' && Number.isFinite(raw) ? { ...row, [dataKey]: raw } : null;
      })
      .filter((row): row is NonNullable<typeof row> => row !== null);
    if (rows.length === 1) {
      return [rows[0], { ...rows[0], timestamp: Number(rows[0].timestamp) + 1 }];
    }
    return rows;
  }, [chart.data, chart.error, dataKey]);

  const yDomain = useMemo<[number, number] | undefined>(() => {
    const values = series
      .map((row) => row[dataKey])
      .filter((item): item is number => typeof item === 'number');
    if (!values.length) return undefined;
    const min = Math.min(...values);
    const max = Math.max(...values);
    const pad = min === max ? Math.max(Math.abs(min) * 0.25, 1) : (max - min) * 0.1;
    return [min - pad, max + pad];
  }, [series, dataKey]);

  const headline = (() => {
    if (value !== undefined) return value;
    if (unavailable) return unavailableLabel;
    const last = [...series].reverse().find((row) => typeof row[dataKey] === 'number')?.[dataKey];
    if (typeof last === 'number') return formatKpiValue(last, format);
    return formatCardValue(card.data, format);
  })();

  const isLoading = pending || (enabled && (chart.isLoading || card.isLoading));
  const denied =
    identityDenied ||
    (chart.error && (chart.error.status === 401 || chart.error.status === 403)) ||
    (card.error && (card.error.status === 401 || card.error.status === 403));
  const showIdle = idle && !unavailable && !denied && !isLoading;
  const windowLabel = rangeLabel ?? 'Last 5m';

  const body = (
    <Card
      size="sm"
      className={cn(
        'relative h-full w-full overflow-hidden',
        href && 'hover:bg-muted/30 transition-colors'
      )}>
      {isLoading ? (
        <div className="absolute inset-0 z-10 flex items-center justify-center">
          <SpinnerIcon size="sm" />
        </div>
      ) : null}
      <CardContent className={cn('flex min-w-0 flex-col gap-2', isLoading && 'invisible')}>
        <div className="flex h-4 items-center justify-between gap-2">
          <span className="text-muted-foreground text-xs font-medium">{title}</span>
          <span className="text-muted-foreground text-2xs">
            {unavailable || value !== undefined ? '\u00a0' : windowLabel}
          </span>
        </div>
        <div className="text-foreground flex h-8 items-center text-2xl font-semibold tabular-nums">
          {unavailable ? (
            <span className="text-muted-foreground text-sm font-medium">{unavailableLabel}</span>
          ) : denied ? (
            <span className="text-muted-foreground text-sm" title="You don't have permission to view metrics">
              —
            </span>
          ) : showIdle ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            (headline ?? '—')
          )}
        </div>
        <div className="h-8 w-full">
          {denied || unavailable || value !== undefined ? null : showIdle ? (
            <div className="flex h-full items-center" aria-hidden="true">
              <div className="bg-border h-px w-full" />
            </div>
          ) : series.length < 2 ? null : (
            <AreaChart
              data={series}
              responsive
              width="100%"
              height={32}
              margin={{ top: 2, right: 0, left: 0, bottom: 2 }}>
              <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={color} stopOpacity={0.3} />
                  <stop offset="100%" stopColor={color} stopOpacity={0} />
                </linearGradient>
              </defs>
              {yDomain ? <YAxis hide domain={yDomain} /> : null}
              <Area
                type="monotone"
                dataKey={dataKey}
                stroke={color}
                strokeWidth={1.5}
                fill={`url(#${gradientId})`}
                fillOpacity={1}
                connectNulls={false}
                dot={false}
                activeDot={false}
                isAnimationActive={false}
              />
            </AreaChart>
          )}
        </div>
      </CardContent>
    </Card>
  );

  if (!href) return body;

  return (
    <Link
      to={href}
      className="focus-visible:ring-ring block h-full rounded-xl focus-visible:ring-2 focus-visible:outline-none">
      {body}
    </Link>
  );
}
