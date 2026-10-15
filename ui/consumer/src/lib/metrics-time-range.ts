/**
 * ALB-style metrics time window: `now-1h` presets or an absolute
 * `<unix>_<unix>` range, stored on the `timeRange` search param.
 */
import { subDays, subHours, subMinutes } from 'date-fns';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import type { PickerPreset } from '@datum-cloud/datum-ui/picker';

export const DEFAULT_METRICS_RANGE = 'now-1h';
export const METRICS_TIME_PARAM = 'timeRange';

const TICK_MS = 30_000;

export const METRICS_RANGE_PRESETS: readonly PickerPreset[] = [
  { key: 'now-5m', label: 'Last 5m', shortcut: '1', getRange: () => relativeRange(5, 'm') },
  { key: 'now-15m', label: 'Last 15m', shortcut: '2', getRange: () => relativeRange(15, 'm') },
  { key: 'now-30m', label: 'Last 30m', shortcut: '3', getRange: () => relativeRange(30, 'm') },
  { key: 'now-1h', label: 'Last 1h', shortcut: 'h', getRange: () => relativeRange(1, 'h') },
  { key: 'now-3h', label: 'Last 3h', shortcut: '4', getRange: () => relativeRange(3, 'h') },
  { key: 'now-6h', label: 'Last 6h', shortcut: '5', getRange: () => relativeRange(6, 'h') },
  { key: 'now-12h', label: 'Last 12h', shortcut: '6', getRange: () => relativeRange(12, 'h') },
  { key: 'now-24h', label: 'Last 24h', shortcut: 'd', getRange: () => relativeRange(1, 'd') },
  { key: 'now-2d', label: 'Last 2d', shortcut: '7', getRange: () => relativeRange(2, 'd') },
  { key: 'now-7d', label: 'Last 7d', shortcut: 'w', getRange: () => relativeRange(7, 'd') },
];

const PRESET_KEYS = new Set(METRICS_RANGE_PRESETS.map((preset) => preset.key));

function relativeRange(amount: number, unit: 'm' | 'h' | 'd'): { from: Date; to: Date } {
  const to = new Date();
  const from =
    unit === 'm' ? subMinutes(to, amount) : unit === 'h' ? subHours(to, amount) : subDays(to, amount);
  return { from, to };
}

export type MetricsPickerValue = {
  from: string;
  to: string;
  preset?: string;
};

function parseDurationMs(raw: string): number | undefined {
  const match = raw.match(/^(\d+)([smhd])$/);
  if (!match) return undefined;
  const value = Number(match[1]);
  switch (match[2]) {
    case 's':
      return value * 1000;
    case 'm':
      return value * 60 * 1000;
    case 'h':
      return value * 60 * 60 * 1000;
    case 'd':
      return value * 24 * 60 * 60 * 1000;
    default:
      return undefined;
  }
}

export function parseMetricsRange(
  raw: string | null,
  now = Date.now()
): { start: Date; end: Date; preset?: string } {
  const value = raw && raw.length > 0 ? raw : DEFAULT_METRICS_RANGE;

  if (value.startsWith('now-')) {
    const duration = parseDurationMs(value.slice(4));
    if (duration) {
      return {
        start: new Date(now - duration),
        end: new Date(now),
        preset: PRESET_KEYS.has(value) ? value : undefined,
      };
    }
  }

  const absolute = value.match(/^(\d+)_(\d+)$/);
  if (absolute) {
    const start = Number(absolute[1]) * 1000;
    const end = Number(absolute[2]) * 1000;
    if (end > start) {
      return { start: new Date(start), end: new Date(end) };
    }
  }

  return {
    start: new Date(now - 60 * 60 * 1000),
    end: new Date(now),
    preset: DEFAULT_METRICS_RANGE,
  };
}

function inferPreset(fromMs: number, toMs: number, now = Date.now()): string | undefined {
  const duration = toMs - fromMs;
  if (duration <= 0) return undefined;
  if (Math.abs(toMs - now) > 90_000) return undefined;
  for (const preset of METRICS_RANGE_PRESETS) {
    const expected = parseDurationMs(preset.key.slice(4));
    if (expected && Math.abs(expected - duration) < 2_000) return preset.key;
  }
  return undefined;
}

export function serializeMetricsRange(value: MetricsPickerValue): string {
  if (value.preset && PRESET_KEYS.has(value.preset)) return value.preset;
  const startMs = new Date(value.from).getTime();
  const endMs = new Date(value.to).getTime();
  if (!Number.isFinite(startMs) || !Number.isFinite(endMs) || endMs <= startMs) {
    return DEFAULT_METRICS_RANGE;
  }
  const inferred = inferPreset(startMs, endMs);
  if (inferred) return inferred;
  return `${Math.floor(startMs / 1000)}_${Math.floor(endMs / 1000)}`;
}

function tickNow(): number {
  return Math.floor(Date.now() / TICK_MS) * TICK_MS;
}

export function useMetricsTimeRange() {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get(METRICS_TIME_PARAM);
  const isPreset = !raw || raw.startsWith('now-');
  const [now, setNow] = useState(tickNow);

  useEffect(() => {
    if (!isPreset) return;
    setNow(tickNow());
    const timer = window.setInterval(() => setNow(tickNow()), TICK_MS);
    return () => window.clearInterval(timer);
  }, [isPreset]);

  const parsed = useMemo(() => parseMetricsRange(raw, now), [raw, now]);

  const pickerValue = useMemo<MetricsPickerValue>(
    () => ({
      from: parsed.start.toISOString(),
      to: parsed.end.toISOString(),
      ...(parsed.preset ? { preset: parsed.preset } : {}),
    }),
    [parsed]
  );

  const setPickerValue = useCallback(
    (value: MetricsPickerValue | null) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          const serialized = value ? serializeMetricsRange(value) : DEFAULT_METRICS_RANGE;
          if (serialized === DEFAULT_METRICS_RANGE) next.delete(METRICS_TIME_PARAM);
          else next.set(METRICS_TIME_PARAM, serialized);
          return next;
        },
        { replace: true }
      );
    },
    [setSearchParams]
  );

  return {
    timeRange: { start: parsed.start, end: parsed.end },
    pickerValue,
    setPickerValue,
    presets: METRICS_RANGE_PRESETS,
  };
}
