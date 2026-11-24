/**
 * A round dot followed by muted sentence-case text, so state is never shown
 * by color alone. The dot is decorative (`aria-hidden`); the label carries
 * the meaning. Colors are datum-ui's badge tokens (`STATUS_TONE_COLOR`).
 */
import { STATUS_TONE_COLOR, type StatusTone } from '../../lib/workload-presenters';
import { cn } from '@datum-cloud/datum-ui/utils';

export interface StatusDotProps {
  tone: StatusTone;
  label: string;
  /** Why the state is what it is, shown after the label. */
  reason?: string;
  className?: string;
}

export function StatusDot({ tone, label, reason, className }: StatusDotProps) {
  return (
    <span className={cn('text-muted-foreground flex min-w-0 items-center gap-2 text-sm', className)}>
      <span
        className="size-2 shrink-0 rounded-full"
        style={{ background: STATUS_TONE_COLOR[tone] }}
        aria-hidden
      />
      <span className="shrink-0">{label}</span>
      {reason ? (
        <span className="truncate" title={reason}>
          · {reason}
        </span>
      ) : null}
    </span>
  );
}
