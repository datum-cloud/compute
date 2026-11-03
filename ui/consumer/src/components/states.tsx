/**
 * Shared loading / empty / error / restricted-access states for the plugin's
 * pages. Not part of PR #1315 (which used the portal's server-side
 * `runDetailLoader`/`runListLoader` RBAC gate + datum-ui's empty state for this) —
 * written fresh here since a plugin has no server loader. See `lib/api.ts`'s
 * `ApiError` for how a 403 response reaches these.
 *
 * Page chrome stays mounted. Loading placeholders live in `skeletons.tsx`.
 */
import { ApiError } from '../lib/api';
import { QuietNotice } from './surface';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { LockIcon, ServerCrashIcon, SquareTerminalIcon } from 'lucide-react';
import type { ReactNode } from 'react';

/** A `QuietNotice` with a leading icon: open, no card, in the home page's style. */
function IconNotice({
  icon,
  iconClassName = 'text-muted-foreground',
  testId,
  ...notice
}: {
  icon: typeof LockIcon;
  iconClassName?: string;
  testId: string;
  title: string;
  description?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div data-testid={testId} className="flex items-start gap-1 px-1">
      <span className="flex h-10 shrink-0 items-center">
        <Icon icon={icon} size={16} className={iconClassName} aria-hidden />
      </span>
      <QuietNotice className="min-w-0 flex-1" {...notice} />
    </div>
  );
}

/** The open notice for a 403: the page has nothing it may show. */
export function RestrictedState({ message }: { message: string }) {
  return <IconNotice testId="compute-plugin-restricted" icon={LockIcon} title="Access restricted" description={message} />;
}

/** The open notice for a failed load, with a Retry button. */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : 'An unknown error occurred';

  return (
    <IconNotice
      testId="compute-plugin-error"
      icon={ServerCrashIcon}
      iconClassName="text-destructive"
      title="Failed to load"
      description={message}
      action={
        <Button type="secondary" theme="outline" size="xs" onClick={onRetry}>
          Retry
        </Button>
      }
    />
  );
}

/** The instance has no shell: either the user may not open one, or its runtime can't. */
export function ShellUnavailableNotice() {
  return (
    <IconNotice
      testId="compute-plugin-shell-unavailable"
      icon={SquareTerminalIcon}
      title="Shell isn't available for this instance"
      description="Opening a shell needs permission to create shell sessions and an instance whose runtime supports them."
    />
  );
}

/**
 * Renders the restricted state for a 403 `ApiError`, otherwise the generic
 * error state. Call once `error` is truthy — keep page chrome outside.
 */
export function ErrorOrRestrictedState({
  error,
  restrictedMessage,
  onRetry,
}: {
  error: unknown;
  restrictedMessage: string;
  onRetry: () => void;
}) {
  if (error instanceof ApiError && error.status === 403) {
    return <RestrictedState message={restrictedMessage} />;
  }
  return <ErrorState error={error} onRetry={onRetry} />;
}
