/**
 * Building blocks for this plugin's project home column, copied from the
 * host's own home columns (cloud-portal
 * `app/features/project/home/resource-column.tsx`) so the Workloads column
 * looks the same as Domains and ALB beside it. The host wraps the body in a
 * fixed-height frame, so each state fills that height. Keep the two in step.
 */
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ReactNode } from 'react';

/** One row: a 40px line with an icon or dot, the label and a short status. */
export const COLUMN_ROW_CLASS = 'flex h-10 items-center gap-2 rounded-md px-2';

// One row, matching the host: columns size to their content, so loading only ever grows them.
const SKELETON_LABEL_WIDTHS = ['w-3/5'];

export function ColumnSkeleton({ label }: { label: string }) {
  return (
    <div className="flex flex-col" role="status">
      <span className="sr-only">Loading {label.toLowerCase()}</span>
      {SKELETON_LABEL_WIDTHS.map((width) => (
        <div key={width} className={COLUMN_ROW_CLASS} aria-hidden>
          <Skeleton className="size-3.5 shrink-0 rounded" />
          <div className="min-w-0 flex-1">
            <Skeleton className={cn('h-3 rounded', width)} />
          </div>
          <Skeleton className="h-4 w-12 shrink-0 rounded-full" />
        </div>
      ))}
    </div>
  );
}

/**
 * One row in place of the list: an optional small icon, a line of text and an
 * optional button, the same height and alignment as a real row so an empty
 * column lines up with the host's columns beside it.
 */
export function ColumnEmpty({
  icon,
  children,
  action,
  testId,
}: {
  icon?: ReactNode;
  children: ReactNode;
  action?: ReactNode;
  testId?: string;
}) {
  return (
    <div
      className="flex min-h-10 items-center justify-between gap-3 rounded-md px-2"
      data-testid={testId}
    >
      <p className="text-muted-foreground flex min-w-0 items-center gap-2 text-sm">
        {icon && <span className="text-icon-secondary shrink-0">{icon}</span>}
        <span>{children}</span>
      </p>
      {action}
    </div>
  );
}
