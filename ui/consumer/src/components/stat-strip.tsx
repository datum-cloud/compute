/**
 * Shared horizontal stat strip (a divided row of label/value pairs) used as
 * the "fleet bar" on both the workload list and workload detail pages.
 *
 * Matches the workloads mockup: equal-width columns in one row. On narrow
 * viewports the strip scrolls horizontally instead of reflowing into a grid.
 */
import { cn } from '@datum-cloud/datum-ui/utils';

export interface Stat {
  label: string;
  value: string;
  className?: string;
}

export function StatStrip({ stats, testId }: { stats: Stat[]; testId?: string }) {
  return (
    <div className="min-w-0 overflow-x-auto overscroll-x-contain" data-testid={testId}>
      <div className="divide-card-border border-card-border bg-card flex min-w-[40rem] divide-x rounded-xl border shadow sm:min-w-0">
        {stats.map((s) => (
          <div key={s.label} className="flex min-w-0 flex-1 flex-col gap-1 px-5 py-3.5">
            <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
              {s.label}
            </span>
            <span className={cn('text-lg font-semibold', s.className)}>{s.value}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
