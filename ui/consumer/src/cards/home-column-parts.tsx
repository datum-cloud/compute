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

const SKELETON_LABEL_WIDTHS = ['w-3/5', 'w-2/5', 'w-1/2'];

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

/** A centred icon tile, title, one line of text and an optional button. */
export function ColumnEmpty({
  icon,
  title,
  children,
  action,
  testId,
}: {
  icon?: ReactNode;
  title?: string;
  children: ReactNode;
  action?: ReactNode;
  testId?: string;
}) {
  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center"
      data-testid={testId}
    >
      {icon && (
        <div className="bg-muted border-border text-icon-secondary flex size-9 items-center justify-center rounded-lg border">
          {icon}
        </div>
      )}
      <div className="flex flex-col gap-1">
        {title && <p className="text-sm font-medium">{title}</p>}
        <p className="text-muted-foreground text-xs text-balance">{children}</p>
      </div>
      {action}
    </div>
  );
}
