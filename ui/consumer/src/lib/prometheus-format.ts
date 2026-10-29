/**
 * Formats raw Prometheus query results into the chart and card shapes the
 * metric components render.
 *
 * Mirrors cloud-portal `app/modules/prometheus/formatter.ts`, which used to run
 * on the portal server behind `POST /api/prometheus`. The plugin now reads
 * queryapi directly, so the same formatting runs here and the components keep
 * the shapes (and series names and colors) they had before.
 */
import type {
  ChartDataPoint,
  ChartSeries,
  FormattedMetricData,
  MetricCardData,
  MetricFormat,
} from './prometheus';

export type PrometheusSample = [number, string];

export interface PrometheusMetric {
  metric: Record<string, string>;
  value?: PrometheusSample;
  values?: PrometheusSample[];
}

export interface PrometheusQueryResult {
  resultType: 'matrix' | 'vector' | 'scalar' | 'string';
  result: PrometheusMetric[];
}

const DEFAULT_TIME_RANGE_MS = 60 * 60 * 1000;
/** Upper bound for a sane sample timestamp (2100-01-01, seconds). */
const MAX_TIMESTAMP_S = 4_102_444_800;

const BASE_COLORS = [
  '#8884d8',
  '#82ca9d',
  '#ffc658',
  '#ff7300',
  '#e74c3c',
  '#9b59b6',
  '#1abc9c',
  '#f39c12',
  '#3498db',
  '#2ecc71',
  '#e67e22',
  '#34495e',
] as const;
const GOLDEN_RATIO = 0.618033988749;
const SATURATION_LEVELS = [70, 85, 60, 90, 75] as const;
const LIGHTNESS_LEVELS = [50, 40, 60, 45, 55] as const;

export function seriesColor(index: number): string {
  if (index < BASE_COLORS.length) return BASE_COLORS[index];
  const dynamicIndex = index - BASE_COLORS.length;
  const hue = Math.round((dynamicIndex * GOLDEN_RATIO * 360) % 360);
  const saturation = SATURATION_LEVELS[dynamicIndex % SATURATION_LEVELS.length];
  const lightness = LIGHTNESS_LEVELS[dynamicIndex % LIGHTNESS_LEVELS.length];
  return `hsl(${hue}, ${saturation}%, ${lightness}%)`;
}

/**
 * Joins the non-empty label values. `sum by (label)` with a missing label still
 * returns the key, so that bucket is "unknown"; a labelless aggregate is "Series".
 */
export function seriesName(labels: Record<string, string>): string {
  const values = Object.values(labels)
    .map((value) => value.trim())
    .filter(Boolean);
  if (values.length > 0) return values.join(' - ');
  return Object.keys(labels).length > 0 ? 'unknown' : 'Series';
}

function isoTime(timestampS: number): string {
  if (timestampS < 0 || timestampS > MAX_TIMESTAMP_S) return new Date().toISOString();
  const date = new Date(timestampS * 1000);
  return Number.isNaN(date.getTime()) ? new Date().toISOString() : date.toISOString();
}

function toPoint([timestampS, value]: PrometheusSample, labels?: Record<string, string>): ChartDataPoint {
  return {
    timestamp: timestampS * 1000,
    value: parseFloat(value),
    formattedTime: isoTime(timestampS),
    ...(labels ? { labels } : {}),
  };
}

function defaultRange(now = Date.now()): { start: number; end: number } {
  return { start: now - DEFAULT_TIME_RANGE_MS, end: now };
}

function rangeOf(series: ChartSeries[]): { start: number; end: number } {
  const timestamps = series.flatMap((s) => s.data.map((point) => point.timestamp));
  return timestamps.length > 0
    ? { start: Math.min(...timestamps), end: Math.max(...timestamps) }
    : defaultRange();
}

function formatMatrix(
  result: PrometheusMetric[],
  timeRange?: { start: number; end: number }
): FormattedMetricData {
  const series: ChartSeries[] = result.map((metric, index) => ({
    name: seriesName(metric.metric),
    data: (metric.values ?? []).map((sample) => toPoint(sample, metric.metric)),
    labels: metric.metric,
    color: seriesColor(index),
  }));
  return { series, timeRange: timeRange ?? rangeOf(series) };
}

function formatVector(
  result: PrometheusMetric[],
  timeRange?: { start: number; end: number }
): FormattedMetricData {
  const nowS = Date.now() / 1000;
  const series: ChartSeries[] = result.map((metric, index) => ({
    name: seriesName(metric.metric),
    data: [toPoint(metric.value ?? [nowS, '0'], metric.metric)],
    labels: metric.metric,
    color: seriesColor(index),
  }));
  return { series, timeRange: timeRange ?? defaultRange() };
}

function formatScalar(
  result: PrometheusMetric[],
  timeRange?: { start: number; end: number }
): FormattedMetricData {
  const [first] = result;
  if (!first) return { series: [], timeRange: timeRange ?? defaultRange() };
  const nowS = Date.now() / 1000;
  return {
    series: [
      { name: 'Value', data: [toPoint(first.value ?? [nowS, '0'])], labels: {}, color: seriesColor(0) },
    ],
    timeRange: timeRange ?? defaultRange(),
  };
}

/**
 * `timeRange` is the queried window in seconds, as `query_range` was sent; when
 * it is omitted, matrix data uses the span of its samples.
 */
export function formatForChart(
  queryResult: PrometheusQueryResult | undefined,
  timeRange?: { start: number; end: number }
): FormattedMetricData {
  if (!queryResult?.result) return { series: [], timeRange: timeRange ?? { start: 0, end: 0 } };
  switch (queryResult.resultType) {
    case 'matrix':
      return formatMatrix(queryResult.result, timeRange);
    case 'vector':
      return formatVector(queryResult.result, timeRange);
    case 'scalar':
      return formatScalar(queryResult.result, timeRange);
    default:
      throw new Error(`Unsupported result type: ${queryResult.resultType}`);
  }
}

function formatBytes(bytes: number, precision: number): string {
  if (bytes === 0) return '0 B';
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return `${parseFloat((bytes / Math.pow(1024, i)).toFixed(precision))} ${sizes[i]}`;
}

export function formatValue(value: number, format: MetricFormat = 'number', precision = 2): string {
  switch (format) {
    case 'percent':
      return `${(value * 100).toFixed(precision)}%`;
    case 'bytes':
      return formatBytes(value, precision);
    case 'requestsPerSecond':
      return `${value.toFixed(precision)} req/s`;
    case 'milliseconds':
      return `${value.toFixed(precision)} ms`;
    case 'milliseconds-auto':
      return value >= 1000 ? `${(value / 1000).toFixed(precision)}s` : `${value.toFixed(0)}ms`;
    case 'number':
    default:
      return value.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: precision });
  }
}

/** Card value: the first series' instant value, or its latest sample for a matrix. */
export function formatForCard(
  queryResult: PrometheusQueryResult | undefined,
  format: MetricFormat = 'number'
): MetricCardData {
  const [first] = queryResult?.result ?? [];
  const sample =
    queryResult?.resultType === 'matrix' ? first?.values?.[first.values.length - 1] : first?.value;

  if (!first || !sample) {
    return { value: 0, formattedValue: formatValue(0, format), timestamp: Date.now() };
  }
  if (queryResult?.resultType === 'string') {
    throw new Error('Unsupported result type for card: string');
  }

  const value = parseFloat(sample[1]);
  return {
    value,
    formattedValue: formatValue(value, format),
    timestamp: sample[0] * 1000,
    labels: first.metric ?? {},
  };
}
