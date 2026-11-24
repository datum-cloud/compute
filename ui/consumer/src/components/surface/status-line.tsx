/**
 * One fixed-height status line in the shape of the home page's
 * `ProjectHealth`: an icon, a bold phrase, a muted detail. Plain while
 * things are fine; a tinted, bordered box only when `tone` needs attention.
 * The height is `h-9` in every state, so the page never shifts.
 */
import type { StatusTone } from '../../lib/workload-presenters';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { Text } from '@datum-cloud/datum-ui/typography';
import { cn } from '@datum-cloud/datum-ui/utils';
import { CircleCheckIcon, CircleIcon, CircleXIcon, TriangleAlertIcon } from 'lucide-react';
import type { ReactNode } from 'react';

const TONE_ICON = {
  ok: <Icon icon={CircleCheckIcon} size={16} className="shrink-0 text-green-600" aria-hidden />,
  warn: <Icon icon={TriangleAlertIcon} size={16} className="shrink-0 text-amber-500" aria-hidden />,
  error: <Icon icon={CircleXIcon} size={16} className="text-destructive shrink-0" aria-hidden />,
  muted: <Icon icon={CircleIcon} size={16} className="text-muted-foreground shrink-0" aria-hidden />,
} satisfies Record<StatusTone, ReactNode>;

const ATTENTION_CLASS: Partial<Record<StatusTone, string>> = {
  warn: 'border border-amber-500/30 bg-amber-500/5',
  error: 'border-destructive/30 bg-destructive/5 border',
};

export interface StatusLineProps {
  tone: StatusTone;
  title: string;
  detail?: ReactNode;
  action?: ReactNode;
  isLoading?: boolean;
  className?: string;
}

export function StatusLine({ tone, title, detail, action, isLoading = false, className }: StatusLineProps) {
  const attention = !isLoading ? ATTENTION_CLASS[tone] : undefined;
  return (
    <section
      aria-label="Status"
      aria-busy={isLoading}
      className={cn('overflow-hidden rounded-xl', attention, className)}>
      <div className={cn('flex h-9 items-center gap-3', attention ? 'px-4' : 'px-1')}>
        {isLoading ? (
          <>
            <Skeleton className="size-4 shrink-0 rounded-full" />
            <Skeleton className="h-3 w-48 rounded" />
            <span className="sr-only" role="status">
              Loading status
            </span>
          </>
        ) : (
          <>
            {TONE_ICON[tone]}
            <Text size="sm" weight="medium" className="shrink-0">
              {title}
            </Text>
            {detail ? (
              <span className="text-muted-foreground min-w-0 truncate text-sm">{detail}</span>
            ) : null}
            {action ? <span className="ml-auto flex min-w-0 items-center gap-2">{action}</span> : null}
          </>
        )}
      </div>
    </section>
  );
}
