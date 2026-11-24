/**
 * Ported near-verbatim from cloud-portal PR #1315's
 * `app/features/workload/cli-section.tsx`. Only the imports changed:
 *  - `@/hooks/useCopyToClipboard` (portal-internal) → `@datum-cloud/datum-ui/hooks`,
 *    which exports the same hook.
 *  - Icons rendered via `@datum-cloud/datum-ui/icons` `Icon` wrapper.
 * The banner and section cards were later reworked into open `SectionFrame`s
 * to match the project home page.
 */
import { useCopyToClipboard } from "@datum-cloud/datum-ui/hooks";
import { SectionFrame } from "./surface/section-frame";
import { LinkButton } from "@datum-cloud/datum-ui/button";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { toast } from "@datum-cloud/datum-ui/toast";
import { cn } from "@datum-cloud/datum-ui/utils";
import {
  BookOpenIcon,
  CheckIcon,
  CopyIcon,
  DownloadIcon,
} from "lucide-react";
import { useState } from "react";

export function CommandBlock({
  value,
  danger,
}: {
  value: string;
  danger?: boolean;
}) {
  const [, copy] = useCopyToClipboard();
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    copy(value).then(() => {
      toast.success("Copied to clipboard");
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <div className="bg-background flex items-start gap-3 rounded-lg border px-3 py-3 sm:items-center sm:px-4">
      <span
        className={cn(
          "min-w-0 flex-1 break-all font-mono text-xs leading-relaxed sm:text-sm",
          danger ? "text-red-500" : "text-foreground",
        )}
      >
        <span className="text-muted-foreground mr-2">$</span>
        {value}
      </span>
      <button
        type="button"
        onClick={handleCopy}
        className="text-muted-foreground hover:text-foreground mt-0.5 shrink-0 transition-colors sm:mt-0"
        aria-label="Copy command"
      >
        {copied ? (
          <Icon icon={CheckIcon} size={16} />
        ) : (
          <Icon icon={CopyIcon} size={16} />
        )}
      </button>
    </div>
  );
}

/**
 * A CLI how-to as an open section (no card): the home page's small heading,
 * a muted description, then the commands to copy.
 */
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
    <SectionFrame
      title={title}
      action={
        <span className={cn("shrink-0", danger ? "text-red-500" : "text-muted-foreground")} aria-hidden>
          {icon}
        </span>
      }
    >
      <p className="text-muted-foreground px-1 text-sm">{description}</p>
      <div className="flex flex-col gap-2">
        {commands.map((cmd) => (
          <CommandBlock key={cmd} value={cmd} danger={danger} />
        ))}
      </div>
    </SectionFrame>
  );
}

/**
 * Points users at the datumctl docs wherever a resource is CLI-managed only:
 * an open section with the install and docs links as its action.
 */
export function CliBanner({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <SectionFrame
      title={title}
      testId="compute-plugin-cli-banner"
      action={
        <div className="flex shrink-0 items-center gap-2">
          <LinkButton
            href="https://www.datum.net/docs/datumctl/quickstart"
            target="_blank"
            rel="noreferrer"
            type="primary"
            theme="solid"
            size="xs"
            icon={<Icon icon={DownloadIcon} size={14} aria-hidden />}
          >
            Install CLI
            <span className="sr-only"> (opens in a new tab)</span>
          </LinkButton>
          <LinkButton
            href="https://www.datum.net/docs/datumctl/overview"
            target="_blank"
            rel="noreferrer"
            type="quaternary"
            theme="borderless"
            size="xs"
            icon={<Icon icon={BookOpenIcon} size={14} aria-hidden />}
          >
            CLI docs
            <span className="sr-only"> (opens in a new tab)</span>
          </LinkButton>
        </div>
      }
    >
      <p className="text-muted-foreground px-1 text-sm">{description}</p>
    </SectionFrame>
  );
}
