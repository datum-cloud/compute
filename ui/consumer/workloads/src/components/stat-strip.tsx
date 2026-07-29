/**
 * Shared horizontal stat strip (a divided row of label/value pairs) used as
 * the "fleet bar" on both the workload list and workload detail pages.
 */
import { cn } from '@datum-cloud/datum-ui/utils';

export interface Stat {
  label: string;
  value: string;
  className?: string;
}

export function StatStrip({ stats, testId }: { stats: Stat[]; testId?: string }) {
  return (
    <div
      className="divide-card-border border-card-border bg-card flex divide-x rounded-xl border shadow"
      data-testid={testId}>
      {stats.map((s) => (
        <div key={s.label} className="flex flex-1 flex-col gap-1 px-5 py-3.5">
          <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            {s.label}
          </span>
          <span className={cn('text-lg font-semibold', s.className)}>{s.value}</span>
        </div>
      ))}
    </div>
  );
}
