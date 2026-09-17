import { useEffect, useMemo, useState } from 'react';

export const OVERVIEW_RANGE_OPTIONS = [
  { value: '5m', label: 'Last 5 minutes', ms: 5 * 60 * 1000 },
  { value: '15m', label: 'Last 15 minutes', ms: 15 * 60 * 1000 },
  { value: '1h', label: 'Last 1 hour', ms: 60 * 60 * 1000 },
  { value: '6h', label: 'Last 6 hours', ms: 6 * 60 * 60 * 1000 },
  { value: '24h', label: 'Last 24 hours', ms: 24 * 60 * 60 * 1000 },
] as const;

export type OverviewRangeValue = (typeof OVERVIEW_RANGE_OPTIONS)[number]['value'];

export const DEFAULT_OVERVIEW_RANGE: OverviewRangeValue = '5m';

const OVERVIEW_TICK_MS = 30_000;

export interface OverviewRange {
  value: OverviewRangeValue;
  label: string;
  shortLabel: string;
  timeRange: { start: Date; end: Date };
}

export function useOverviewRange(value: OverviewRangeValue): OverviewRange {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), OVERVIEW_TICK_MS);
    return () => window.clearInterval(timer);
  }, [value]);

  return useMemo(() => {
    const option =
      OVERVIEW_RANGE_OPTIONS.find((item) => item.value === value) ?? OVERVIEW_RANGE_OPTIONS[0];
    return {
      value: option.value,
      label: option.label,
      shortLabel: option.value,
      timeRange: { start: new Date(now - option.ms), end: new Date(now) },
    };
  }, [now, value]);
}
