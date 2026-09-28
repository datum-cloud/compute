/**
 * Helpers for charts that plot one series per instance. With dozens of
 * replicas a chart of every series is unreadable, so we draw the busiest few
 * and label them with the part of the name that tells them apart.
 */
import type { ChartSeries } from './prometheus';

/** Default cap on per-instance series when the viewer has not picked instances. */
export const MAX_INSTANCE_SERIES = 8;

const naturalCompare = (a: string, b: string) =>
  a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });

export function sortNatural(values: readonly string[]): string[] {
  return [...values].sort(naturalCompare);
}

function peak(series: ChartSeries): number {
  let max = -Infinity;
  for (const point of series.data) {
    if (Number.isFinite(point.value) && point.value > max) max = point.value;
  }
  return max;
}

/**
 * The `limit` series with the highest peak in the window, in natural name
 * order so the legend reads 0, 1, 2 … 10 rather than by rank. Everything is
 * returned (still name-sorted) when there are no more than `limit`.
 */
export function busiestSeries(series: readonly ChartSeries[], limit?: number): ChartSeries[] {
  const byName = (a: ChartSeries, b: ChartSeries) => naturalCompare(a.name, b.name);
  if (!limit || series.length <= limit) return [...series].sort(byName);
  return [...series]
    .map((item) => ({ item, peak: peak(item) }))
    .sort((a, b) => b.peak - a.peak || byName(a.item, b.item))
    .slice(0, limit)
    .map(({ item }) => item)
    .sort(byName);
}

export type SeriesLegendModifiers = {
  shiftKey?: boolean;
  metaKey?: boolean;
  ctrlKey?: boolean;
};

/**
 * Grafana-style legend clicks, same as the portal's ALB charts:
 * - click isolates the series (click it again to show all)
 * - shift/ctrl/cmd-click toggles that series (never hides the last one)
 */
export function nextHiddenSeries(
  names: readonly string[],
  hidden: ReadonlySet<string>,
  clicked: string,
  modifiers: SeriesLegendModifiers = {}
): Set<string> {
  if (!names.includes(clicked)) return new Set(hidden);

  if (modifiers.shiftKey || modifiers.metaKey || modifiers.ctrlKey) {
    const next = new Set(hidden);
    if (next.has(clicked)) {
      next.delete(clicked);
      return next;
    }
    const visible = names.filter((name) => !next.has(name)).length;
    if (visible > 1) next.add(clicked);
    return next;
  }

  const onlyThisVisible = names.every((name) => name === clicked || hidden.has(name));
  if (onlyThisVisible) return new Set();
  return new Set(names.filter((name) => name !== clicked));
}

/**
 * Drops the dash-separated prefix every name shares, so
 * `counter-default-us-central-1-0` / `…-us-east-1-0` read as `us-central-1-0`
 * / `us-east-1-0`. When only an ordinal is left it reads as `#0`.
 * Names are returned as-is when there is nothing common to strip.
 */
export function shortInstanceLabels(names: readonly string[]): Record<string, string> {
  const labels: Record<string, string> = {};
  const unique = [...new Set(names.filter(Boolean))];
  if (unique.length < 2) {
    for (const name of unique) labels[name] = name;
    return labels;
  }

  const split = unique.map((name) => name.split('-'));
  const shortest = Math.min(...split.map((parts) => parts.length));
  let common = 0;
  // Always leave at least one segment behind.
  while (common < shortest - 1 && split.every((parts) => parts[common] === split[0][common])) {
    common += 1;
  }

  for (const [index, name] of unique.entries()) {
    const rest = split[index].slice(common).join('-');
    labels[name] = /^\d+$/.test(rest) ? `#${rest}` : rest;
  }
  return labels;
}
