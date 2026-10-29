/**
 * Project-scoped metrics via telemetry queryapi's Prometheus-shaped routes.
 *
 * Mirrors cloud-portal `app/resources/o11y-metrics` and the formatting in
 * `app/modules/metrics` without importing portal internals. Requests go through
 * the portal's `/api/proxy` (see `getProjectScopedBase`), which adds the user's
 * token; queryapi scopes every query to the project from that identity.
 * `query` and `query_range` must be form-encoded POSTs.
 */
import { ApiError, PLUGIN_ID, getProjectScopedBase } from './api';
import {
  formatForCard,
  formatForChart,
  type PrometheusQueryResult,
} from './prometheus-format';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

export const O11Y_METRICS_API_PATH = '/apis/o11y.miloapis.com/v1alpha1/metrics/api/v1';

export type MetricFormat =
  | 'number'
  | 'bytes'
  | 'bytesPerSecond'
  | 'percent'
  | 'requestsPerSecond'
  | 'milliseconds'
  | 'milliseconds-auto';

export interface PrometheusTimeRange {
  start: Date;
  end: Date;
}

export interface ChartDataPoint {
  timestamp: number;
  value: number;
  formattedTime: string;
  labels?: Record<string, string>;
}

export interface ChartSeries {
  name: string;
  data: ChartDataPoint[];
  color?: string;
  labels: Record<string, string>;
}

export interface FormattedMetricData {
  series: ChartSeries[];
  timeRange: { start: number; end: number };
}

export interface MetricCardData {
  value: number;
  formattedValue: string;
  timestamp: number;
  labels?: Record<string, string>;
}

interface PrometheusEnvelope<T> {
  status: 'success' | 'error';
  data?: T;
  error?: string;
  errorType?: string;
}

export class PrometheusError extends Error {
  status?: number;

  constructor(message: string, status?: number) {
    super(message);
    this.name = 'PrometheusError';
    this.status = status;
  }
}

function toUnixRange(timeRange: PrometheusTimeRange): { start: number; end: number } {
  return {
    start: Math.floor(timeRange.start.getTime() / 1000),
    end: Math.floor(timeRange.end.getTime() / 1000),
  };
}

/** Reads queryapi's error text from a Prometheus envelope or a Kubernetes Status. */
async function errorText(res: Response): Promise<string | undefined> {
  try {
    const body = (await res.json()) as { error?: unknown; message?: unknown };
    if (typeof body.error === 'string') return body.error;
    if (typeof body.message === 'string') return body.message;
  } catch {
    // Non-JSON error body; fall back to the status.
  }
  return undefined;
}

async function metricsRequest<T>(projectId: string, path: string, init: RequestInit): Promise<T> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${O11Y_METRICS_API_PATH}${path}`, {
    ...init,
    headers: { Accept: 'application/json', ...init.headers },
  });

  if (!res.ok) {
    throw new PrometheusError(
      (await errorText(res)) ?? `Metrics request failed (${res.status})`,
      res.status
    );
  }

  const body = (await res.json()) as PrometheusEnvelope<T>;
  if (body.status === 'error') {
    throw new PrometheusError(body.error || 'Metrics query failed', 400);
  }
  return body.data as T;
}

function postForm<T>(projectId: string, path: string, params: Record<string, string>): Promise<T> {
  return metricsRequest<T>(projectId, path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams(params).toString(),
  });
}

export async function queryRange(
  projectId: string,
  query: string,
  timeRange: PrometheusTimeRange,
  step: string
): Promise<FormattedMetricData> {
  const range = toUnixRange(timeRange);
  const data = await postForm<PrometheusQueryResult>(projectId, '/query_range', {
    query,
    start: String(range.start),
    end: String(range.end),
    step,
  });
  return formatForChart(data, range);
}

export async function queryInstant(
  projectId: string,
  query: string,
  metricFormat: MetricFormat,
  time?: Date
): Promise<MetricCardData> {
  const data = await postForm<PrometheusQueryResult>(projectId, '/query', {
    query,
    ...(time ? { time: String(Math.floor(time.getTime() / 1000)) } : {}),
  });
  return formatForCard(data, metricFormat);
}

/** Statuses a retry won't change: auth, a bad query, too much data, a timeout. */
const NO_RETRY_STATUSES = new Set([400, 401, 403, 422, 503, 504]);

export function shouldRetryMetrics(failureCount: number, error: unknown): boolean {
  if (error instanceof PrometheusError && error.status !== undefined && NO_RETRY_STATUSES.has(error.status)) {
    return false;
  }
  return failureCount < 2;
}

/** User-facing text for a failed chart, other than the permission case. */
export function metricErrorMessage(error: unknown): string {
  const status = error instanceof PrometheusError ? error.status : undefined;
  if (status === 422) return 'Too much data to show. Try a shorter time range';
  if (status === 503 || status === 504) return 'Took too long to load. Try a shorter time range';
  return 'Unable to load metrics';
}

/** Last hour, used as the default Metrics tab window. */
export function lastHour(now = new Date()): PrometheusTimeRange {
  return { start: new Date(now.getTime() - 60 * 60 * 1000), end: now };
}

export function lastThirtyMinutesRange(now = new Date()): PrometheusTimeRange {
  return { start: new Date(now.getTime() - 30 * 60 * 1000), end: now };
}

export function chartStepFor(timeRange: PrometheusTimeRange): string {
  const durationSec = Math.max(1, Math.floor((timeRange.end.getTime() - timeRange.start.getTime()) / 1000));
  const step = Math.max(15, Math.floor(durationSec / 400));
  if (step < 60) return `${step}s`;
  if (step < 3600) return `${Math.floor(step / 60)}m`;
  return `${Math.floor(step / 3600)}h`;
}

export function usePrometheusChart(
  projectId: string | undefined,
  query: string | undefined,
  timeRange: PrometheusTimeRange,
  options?: { enabled?: boolean; refetchInterval?: number | false }
): UseQueryResult<FormattedMetricData, PrometheusError> {
  const step = chartStepFor(timeRange);
  return useQuery<FormattedMetricData, PrometheusError>({
    queryKey: [
      PLUGIN_ID,
      'prometheus-chart',
      projectId,
      query,
      timeRange.start.getTime(),
      timeRange.end.getTime(),
      step,
    ],
    enabled: (options?.enabled ?? true) && !!projectId && !!query,
    queryFn: () => queryRange(projectId as string, query as string, timeRange, step),
    refetchInterval: options?.refetchInterval ?? 30_000,
    staleTime: 15_000,
    retry: shouldRetryMetrics,
  });
}

export function usePrometheusCard(
  projectId: string | undefined,
  query: string | undefined,
  metricFormat: MetricFormat,
  options?: { enabled?: boolean; time?: Date; refetchInterval?: number | false }
): UseQueryResult<MetricCardData, PrometheusError> {
  const time = options?.time;
  return useQuery<MetricCardData, PrometheusError>({
    queryKey: [PLUGIN_ID, 'prometheus-card', projectId, query, metricFormat, time?.getTime()],
    enabled: (options?.enabled ?? true) && !!projectId && !!query,
    queryFn: () => queryInstant(projectId as string, query as string, metricFormat, time),
    refetchInterval: options?.refetchInterval ?? 30_000,
    staleTime: 15_000,
    retry: shouldRetryMetrics,
  });
}

export async function fetchPrometheusLabelValues(
  projectId: string,
  label: string,
  match: string
): Promise<string[]> {
  const search = new URLSearchParams({ 'match[]': match });
  const values = await metricsRequest<string[]>(
    projectId,
    `/label/${encodeURIComponent(label)}/values?${search.toString()}`,
    { method: 'GET' }
  );
  return values ?? [];
}

export function usePrometheusLabelValues(
  projectId: string | undefined,
  label: string,
  match: string | undefined,
  options?: { enabled?: boolean }
): UseQueryResult<string[], PrometheusError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'prometheus-labels', projectId, label, match],
    enabled: (options?.enabled ?? true) && !!projectId && !!match,
    queryFn: () => fetchPrometheusLabelValues(projectId as string, label, match as string),
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function isPrometheusDenied(error: unknown): boolean {
  if (error instanceof PrometheusError) {
    return error.status === 401 || error.status === 403;
  }
  return error instanceof ApiError && (error.status === 401 || error.status === 403);
}

/** Flatten portal chart series into Recharts rows keyed by series name. */
export function transformForRecharts(data: FormattedMetricData): Array<Record<string, number | string>> {
  if (data.series.length === 0) return [];

  if (data.series.length === 1) {
    const series = data.series[0];
    return series.data.map((point) => ({
      timestamp: point.timestamp,
      time: point.formattedTime,
      [series.name]: point.value,
    }));
  }

  const byTimestamp = new Map<number, Record<string, number | string>>();
  for (const series of data.series) {
    for (const point of series.data) {
      const row = byTimestamp.get(point.timestamp) ?? {
        timestamp: point.timestamp,
        time: point.formattedTime,
      };
      row[series.name] = point.value;
      byTimestamp.set(point.timestamp, row);
    }
  }
  return [...byTimestamp.values()].sort((a, b) => Number(a.timestamp) - Number(b.timestamp));
}
