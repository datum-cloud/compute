/**
 * Field list — ported verbatim from `ui/consumer/src/components/detail-list.tsx`
 * (matches cloud-portal's `app/components/list/list.tsx` styling; staff-portal
 * has no equivalent exported to plugins, so this bundles its own copy, same
 * convention as the consumer plugin).
 */
import { Badge } from '@datum-cloud/datum-ui/badge';
import { cn } from '@datum-cloud/datum-ui/utils';

export interface DetailListItem {
  label: React.ReactNode;
  content: React.ReactNode;
  hidden?: boolean;
  className?: string;
}

export function DetailList({
  items,
  className,
  itemClassName,
  labelClassName,
}: {
  items: DetailListItem[];
  className?: string;
  itemClassName?: string;
  labelClassName?: string;
}) {
  return (
    <div className={cn('flex flex-col', className)}>
      {items
        .filter((item) => !item.hidden)
        .map((item, index) => (
          <div
            key={index}
            className={cn(
              'border-border flex w-full flex-col gap-2 py-3 not-last:border-b sm:flex-row sm:items-center',
              itemClassName,
              item.className
            )}>
            <div
              style={{ minWidth: '200px' }}
              className={cn(
                'flex items-center justify-start gap-1.5 text-left text-sm font-semibold',
                labelClassName
              )}>
              {item.label}
            </div>
            <div className="flex min-w-0 max-w-full justify-start overflow-hidden text-left text-sm font-normal wrap-break-word sm:justify-end sm:text-right">
              {item.content}
            </div>
          </div>
        ))}
    </div>
  );
}

/** Compact status badge matching portal `BadgeStatus` sizing. */
export function StatusBadge({
  type,
  children,
}: {
  type: 'success' | 'warning' | 'danger' | 'info' | 'secondary' | 'muted' | 'primary';
  children: React.ReactNode;
}) {
  return (
    <div className="w-fit">
      <Badge
        type={type}
        theme="light"
        className="text-2xs flex cursor-default items-center gap-1.5 px-1 py-0.5 font-bold tracking-wide uppercase">
        {children}
      </Badge>
    </div>
  );
}
