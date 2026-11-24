const UNITS = ['b/s', 'kb/s', 'Mb/s', 'Gb/s', 'Tb/s'];

/**
 * Bits per second in SI units (base 1000). Bare b/s values are integers; scaled
 * values show two decimals below 10, one below 100, and none from 100 up.
 * Non-finite values show the chart placeholder; a counter rate is never
 * negative, so negatives clamp to zero.
 */
export function formatBitsPerSecond(value: number): string {
  if (!Number.isFinite(value)) return '—';
  let n = Math.max(0, value);
  let i = 0;
  while (n >= 1000 && i < UNITS.length - 1) {
    n /= 1000;
    i += 1;
  }
  const decimals = i === 0 ? 0 : n < 10 ? 2 : n < 100 ? 1 : 0;
  return `${n.toFixed(decimals)} ${UNITS[i]}`;
}
