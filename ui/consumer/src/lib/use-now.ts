import { useEffect, useState } from 'react';

/**
 * The current time, re-read every `intervalMs` while `enabled`. For copy that
 * depends on elapsed time ("Started 45s ago", the stalled-deploy warning) —
 * polled queries alone don't re-render when the data hasn't changed.
 */
export function useNow(enabled: boolean, intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [enabled, intervalMs]);
  return now;
}
