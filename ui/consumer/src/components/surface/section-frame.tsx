/**
 * An open section in the shape of the project home page's
 * `ResourceColumnFrame`: a small heading (optionally linked, with a chevron),
 * a muted count, an optional action on the right, and no border or card.
 * Class strings match the home page's so the host already compiles them.
 */
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Text } from '@datum-cloud/datum-ui/typography';
import { cn } from '@datum-cloud/datum-ui/utils';
import { ChevronRightIcon } from 'lucide-react';
import { useId, type ReactNode } from 'react';
import { Link } from 'react-router';

export interface SectionFrameProps {
  title: string;
  /** Full page for the section. Without it the heading is plain text. */
  href?: string;
  /** Shown next to the heading once loaded, when above zero. */
  count?: number;
  /** Shown at the right of the header, e.g. a button. */
  action?: ReactNode;
  isLoading?: boolean;
  testId?: string;
  className?: string;
  children: ReactNode;
}

export function SectionFrame({
  title,
  href,
  count,
  action,
  isLoading = false,
  testId,
  className,
  children,
}: SectionFrameProps) {
  const headingId = useId();
  return (
    <section
      className={cn('flex min-w-0 flex-col gap-2', className)}
      data-testid={testId}
      aria-labelledby={headingId}
      aria-busy={isLoading}>
      <header className="flex h-7 shrink-0 items-center justify-between gap-2">
        <h2 id={headingId} className="flex min-w-0 items-center gap-2">
          {href ? (
            <Link to={href} className="text-foreground flex min-w-0 items-center gap-1 text-sm font-medium">
              <span className="truncate">{title}</span>
              <Icon icon={ChevronRightIcon} size={14} className="text-icon-quaternary shrink-0" aria-hidden />
            </Link>
          ) : (
            <Text as="span" size="sm" weight="medium" className="truncate">
              {title}
            </Text>
          )}
          {!isLoading && count !== undefined && count > 0 && (
            <Text as="span" size="xs" textColor="muted" className="tabular-nums">
              {count}
            </Text>
          )}
        </h2>
        {action}
      </header>
      {children}
    </section>
  );
}
