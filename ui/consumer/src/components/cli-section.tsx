/**
 * Ported near-verbatim from cloud-portal PR #1315's
 * `app/features/workload/cli-section.tsx`. Only the imports changed:
 *  - `@/hooks/useCopyToClipboard` (portal-internal) → `@datum-cloud/datum-ui/hooks`,
 *    which exports the same hook.
 *  - Icons rendered via `@datum-cloud/datum-ui/icons` `Icon` wrapper.
 */
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { cn } from '@datum-cloud/datum-ui/utils';
import { BookOpenIcon, CheckIcon, CopyIcon, DownloadIcon, SquareTerminalIcon } from 'lucide-react';
import { useState } from 'react';

export function CommandBlock({ value, danger }: { value: string; danger?: boolean }) {
  const [, copy] = useCopyToClipboard();
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    copy(value).then(() => {
      toast.success('Copied to clipboard');
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div className="bg-background flex items-start gap-3 rounded-lg border px-3 py-3 sm:items-center sm:px-4">
      <span
        className={cn(
          'min-w-0 flex-1 break-all font-mono text-xs leading-relaxed sm:text-sm',
          danger ? 'text-red-500' : 'text-foreground'
        )}>
        <span className="text-muted-foreground mr-2">$</span>
        {value}
      </span>
      <button
        type="button"
        onClick={handleCopy}
        className="text-muted-foreground hover:text-foreground mt-0.5 shrink-0 transition-colors sm:mt-0"
        aria-label="Copy command">
        {copied ? <Icon icon={CheckIcon} size={16} /> : <Icon icon={CopyIcon} size={16} />}
      </button>
    </div>
  );
}

export function SectionCard({
  icon,
  title,
  description,
  commands,
  danger,
}: {
  icon: React.ReactNode;
  title: string;
  description: string;
  commands: string[];
  danger?: boolean;
}) {
  return (
    <Card className={cn(danger && 'border-red-200 dark:border-red-900')}>
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <span className={cn('shrink-0', danger ? 'text-red-500' : 'text-foreground')}>
            {icon}
          </span>
          <h3 className={cn('font-semibold', danger && 'text-red-500')}>{title}</h3>
        </div>
        <p className="text-muted-foreground text-sm">{description}</p>
        <div className="flex flex-col gap-2">
          {commands.map((cmd) => (
            <CommandBlock key={cmd} value={cmd} danger={danger} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * Shared shell for the plugin's full-width banners: icon + title/description
 * on the left, an actions slot on the right. Used by `CliBanner` below and by
 * `ComputeEnablementBanner` (compute-enablement-banner.tsx) so the two share
 * one place to adjust spacing/colors/responsive behavior instead of drifting
 * apart as separately hand-rolled markup.
 */
export function Banner({
  icon,
  title,
  description,
  actions,
  testId,
}: {
  icon: React.ReactNode;
  title: string;
  description: string;
  actions: React.ReactNode;
  testId?: string;
}) {
  return (
    <div
      className="bg-primary/5 border-primary/20 flex flex-col gap-4 rounded-xl border p-4 sm:flex-row sm:items-center"
      data-testid={testId}>
      {icon}
      <div className="min-w-0 flex-1">
        <p className="text-primary font-semibold">{title}</p>
        <p className="text-muted-foreground text-sm">{description}</p>
      </div>
      <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">{actions}</div>
    </div>
  );
}

/** Banner pointing users at the datumctl CLI docs — shown wherever a resource is CLI-managed only. */
export function CliBanner({ title, description }: { title: string; description: string }) {
  return (
    <Banner
      icon={<Icon icon={SquareTerminalIcon} size={32} className="text-primary shrink-0" />}
      title={title}
      description={description}
      actions={
        <>
          <a
            href="https://docs.datum.net/cli/install"
            target="_blank"
            rel="noreferrer"
            className="bg-primary text-primary-foreground hover:bg-primary/90 inline-flex items-center justify-center gap-1.5 rounded-md px-3 py-2 text-sm font-medium transition-colors">
            <Icon icon={DownloadIcon} size={16} />
            Install CLI
          </a>
          <a
            href="https://docs.datum.net/cli"
            target="_blank"
            rel="noreferrer"
            className="border-border hover:bg-muted inline-flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-sm font-medium transition-colors">
            <Icon icon={BookOpenIcon} size={16} />
            CLI Docs
          </a>
        </>
      }
    />
  );
}
