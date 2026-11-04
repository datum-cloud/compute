/**
 * A mono value with a small button that copies it, for addresses a person
 * will paste into a client. The button stays visible: unlike the instance
 * page's hover-reveal rows, these values sit in lines with no row to hover.
 */
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import { CheckIcon, CopyIcon } from 'lucide-react';

export function CopyValue({ value, className }: { value: string; className?: string }) {
  const [copied, copy] = useCopyToClipboard();
  return (
    <span className={cn('inline-flex min-w-0 items-center gap-1.5', className)}>
      <span className="truncate font-mono text-xs" title={value}>
        {value}
      </span>
      <Tooltip message={copied ? 'Copied' : 'Copy'}>
        <button
          type="button"
          aria-label={copied ? 'Copied' : `Copy ${value}`}
          className="text-muted-foreground hover:text-foreground shrink-0 rounded p-0.5"
          onClick={() => void copy(value)}>
          <Icon icon={copied ? CheckIcon : CopyIcon} size={12} aria-hidden />
        </button>
      </Tooltip>
    </span>
  );
}
