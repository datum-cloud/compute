/**
 * Same-origin VictoriaMetrics access via the portal's `POST /api/prometheus`.
 *
 * Plugins cannot import cloud-portal's `usePrometheusChart` / `MetricChart`.
 * This module mirrors that HTTP contract so instance CPU/memory (and ALB
 * traffic when a URL is published) query the same `vmsingle-datum-resource-metrics`
 * the built-in edge metrics pages use.
 */
import { ApiError, PLUGIN_ID } from './api';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

const PROMETHEUS_ROUTE_PATH = '/api/prometheus';

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

interface PrometheusAPIResponse<T> {
  success: boolean;
  data: T;
  error?: string;
  type?: string;
  details?: unknown;
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

async function prometheusRequest<T>(body: Record<string, unknown>): Promise<T> {
  const res = await fetch(PROMETHEUS_ROUTE_PATH, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  let payload: PrometheusAPIResponse<T> | undefined;
  try {
    payload = (await res.json()) as PrometheusAPIResponse<T>;
  } catch {
    throw new PrometheusError(`Prometheus request failed (${res.status})`, res.status);
  }

  if (!res.ok || !payload.success) {
    throw new PrometheusError(payload.error || `Prometheus request failed (${res.status})`, res.status);
  }

  return payload.data;
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
  query: string | undefined,
  timeRange: PrometheusTimeRange,
  options?: { enabled?: boolean; refetchInterval?: number | false }
): UseQueryResult<FormattedMetricData, PrometheusError> {
  const step = chartStepFor(timeRange);
  return useQuery({
    queryKey: [
      PLUGIN_ID,
      'prometheus-chart',
      query,
      timeRange.start.getTime(),
      timeRange.end.getTime(),
      step,
    ],
    enabled: (options?.enabled ?? true) && !!query,
    queryFn: () =>
      prometheusRequest<FormattedMetricData>({
        type: 'chart',
        query,
        timeRange: toUnixRange(timeRange),
        step,
      }),
    refetchInterval: options?.refetchInterval ?? 30_000,
    staleTime: 15_000,
    retry: (failureCount, error) => {
      if (error instanceof PrometheusError && (error.status === 401 || error.status === 403)) {
        return false;
      }
      return failureCount < 2;
    },
  });
}

export function usePrometheusCard(
  query: string | undefined,
  metricFormat: MetricFormat,
  options?: { enabled?: boolean; time?: Date; refetchInterval?: number | false }
): UseQueryResult<MetricCardData, PrometheusError> {
  const time = options?.time;
  return useQuery({
    queryKey: [PLUGIN_ID, 'prometheus-card', query, metricFormat, time?.getTime()],
    enabled: (options?.enabled ?? true) && !!query,
    queryFn: () =>
      prometheusRequest<MetricCardData>({
        type: 'card',
        query,
        metricFormat,
        ...(time
          ? { timeRange: { start: Math.floor(time.getTime() / 1000), end: Math.floor(time.getTime() / 1000) } }
          : {}),
      }),
    refetchInterval: options?.refetchInterval ?? 30_000,
    staleTime: 15_000,
    retry: (failureCount, error) => {
      if (error instanceof PrometheusError && (error.status === 401 || error.status === 403)) {
        return false;
      }
      return failureCount < 2;
    },
  });
}

export function fetchPrometheusLabelValues(label: string, match: string): Promise<string[]> {
  return prometheusRequest<string[]>({
    type: 'labels',
    label,
    match,
  });
}

export function usePrometheusLabelValues(
  label: string,
  match: string | undefined,
  options?: { enabled?: boolean }
): UseQueryResult<string[], PrometheusError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'prometheus-labels', label, match],
    enabled: (options?.enabled ?? true) && !!match,
    queryFn: () => fetchPrometheusLabelValues(label, match as string),
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
