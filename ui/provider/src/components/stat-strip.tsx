/**
 * Shared horizontal stat strip — ported verbatim from
 * `ui/consumer/src/components/stat-strip.tsx`.
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
      <div
        style={{ minWidth: '40rem' }}
        className="divide-card-border border-card-border bg-card flex divide-x rounded-xl border shadow">
        {stats.map((s) => (
          <div key={s.label} className="flex min-w-0 flex-1 flex-col gap-1.5 px-5 py-5">
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
