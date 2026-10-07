/** Truncated monospace value with a copy-to-clipboard button. */
import { Button } from '@datum-cloud/datum-ui/button';
import { CheckIcon, CopyIcon } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';

export function CopyableMono({ value }: { value: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const resetTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(resetTimer.current), []);

  const flash = (next: 'copied' | 'failed') => {
    setState(next);
    clearTimeout(resetTimer.current);
    resetTimer.current = setTimeout(() => setState('idle'), 1500);
  };
  const copy = () => {
    if (!navigator.clipboard) {
      flash('failed');
      return;
    }
    navigator.clipboard.writeText(value).then(
      () => flash('copied'),
      () => flash('failed')
    );
  };
  const label = state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : 'Copy';

  return (
    <div className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 truncate font-mono text-xs" title={value}>
        {value}
      </span>
      <Button
        type="quaternary"
        theme="borderless"
        size="xs"
        aria-label={label}
        title={label}
        onClick={copy}
        icon={
          state === 'copied' ? (
            <CheckIcon className="size-3.5" />
          ) : (
            <CopyIcon className="size-3.5" />
          )
        }
      />
    </div>
  );
}
