/**
 * Compact stat strip: one row of small muted labels over values, with no box
 * around it, in the type scale the project home page uses. Wraps on narrow
 * viewports instead of scrolling.
 */
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ReactNode } from 'react';
import { Link } from 'react-router';

/** Shown in place of a value that is loading, errored or empty. */
export const STAT_PLACEHOLDER = '—';

/**
 * Inline because the host compiles no `gap-x-8` / `gap-y-3`, and a missing
 * utility fails silently (see ui/CLAUDE.md): the stats would run together.
 */
export const STRIP_GAP = { columnGap: '2rem', rowGap: '0.75rem' } as const;

export interface Stat {
  label: string;
  value: ReactNode;
  /** Makes the value a link, e.g. to the filtered list. */
  href?: string;
  className?: string;
}

export interface StatStripProps {
  items: Stat[];
  isLoading?: boolean;
  testId?: string;
  className?: string;
}

export function StatStrip({ items, isLoading = false, testId, className }: StatStripProps) {
  return (
    <dl
      className={cn('flex flex-wrap items-start px-1', className)}
      style={STRIP_GAP}
      aria-busy={isLoading}
      data-testid={testId}>
      {items.map((s) => {
        const empty = s.value === undefined || s.value === null || s.value === '';
        return (
          <div key={s.label} className="flex min-w-0 flex-col gap-0.5">
            <dt className="text-muted-foreground text-xs">{s.label}</dt>
            <dd className={cn('text-sm font-medium tabular-nums', s.className)}>
              {isLoading ? (
                <Skeleton className="mt-1 h-3 w-10 rounded" />
              ) : empty ? (
                <span className="text-muted-foreground">{STAT_PLACEHOLDER}</span>
              ) : s.href ? (
                <Link to={s.href} className="hover:underline">
                  {s.value}
                </Link>
              ) : (
                s.value
              )}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}
