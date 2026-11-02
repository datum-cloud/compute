/**
 * A muted one- or two-line notice for empty or informational states, with an
 * optional action. Stands in for datum-ui's illustrated `EmptyContent`, in the
 * style of the home page's empty column row.
 */
import { Text } from '@datum-cloud/datum-ui/typography';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ReactNode } from 'react';

export interface QuietNoticeProps {
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
  testId?: string;
}

export function QuietNotice({ title, description, action, className, testId }: QuietNoticeProps) {
  return (
    <div
      className={cn('flex min-h-10 flex-wrap items-center justify-between gap-3 px-2 py-2', className)}
      data-testid={testId}>
      <div className="flex min-w-0 flex-col gap-0.5">
        <Text as="p" size="sm" textColor="muted">
          {title}
        </Text>
        {description ? (
          <Text as="p" size="xs" textColor="muted">
            {description}
          </Text>
        ) : null}
      </div>
      {action ? <div className="flex shrink-0 items-center gap-2">{action}</div> : null}
    </div>
  );
}
