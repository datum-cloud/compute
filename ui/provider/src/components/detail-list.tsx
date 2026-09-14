/**
 * Field list — ported from `ui/consumer/src/components/detail-list.tsx`
 * (matches cloud-portal's `app/components/list/list.tsx`: datum-ui `CardField`
 * rows so a list inside a `sectioned` Card lines up with neighbouring cards).
 */
import { Badge } from '@datum-cloud/datum-ui/badge';
import { CardField, CardFieldLabel, CardFieldValue } from '@datum-cloud/datum-ui/card';
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
          <CardField key={index} className={cn(itemClassName, item.className)}>
            <CardFieldLabel className={labelClassName}>{item.label}</CardFieldLabel>
            <CardFieldValue className="min-w-0 wrap-break-word">{item.content}</CardFieldValue>
          </CardField>
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
