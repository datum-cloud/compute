import { lastHour, type PrometheusTimeRange } from './prometheus';
import { useEffect, useState } from 'react';

/** Window used for refetches; advanced on this interval so sparklines don't freeze. */
const RANGE_TICK_MS = 60_000;

export function useRollingLastHour(): PrometheusTimeRange {
  const [range, setRange] = useState<PrometheusTimeRange>(() => lastHour());
  useEffect(() => {
    const id = setInterval(() => setRange(lastHour()), RANGE_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return range;
}
