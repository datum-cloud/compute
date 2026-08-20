/**
 * Shared loading / error / restricted-access states — ported verbatim from
 * `ui/consumer/src/components/states.tsx` (no server loader here either, so
 * this is written fresh against the plugin's own `ApiError`).
 */
import { ApiError } from '../lib/api';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { LockIcon, ServerCrashIcon } from 'lucide-react';

/** Content-area placeholder only. No page chrome. */
export function LoadingSkeleton() {
  return (
    <div data-testid="provider-plugin-loading" className="flex flex-col gap-4">
      <Skeleton className="h-14 w-full rounded-xl" />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Skeleton className="h-44 w-full rounded-xl" />
        <Skeleton className="h-44 w-full rounded-xl" />
      </div>
    </div>
  );
}

export function RestrictedState({ message }: { message: string }) {
  return (
    <div data-testid="provider-plugin-restricted" className="flex flex-col gap-4">
      <Card className="max-w-md">
        <CardContent className="flex flex-col items-start gap-3">
          <LockIcon className="text-muted-foreground size-6" />
          <div>
            <p className="font-semibold">Access restricted</p>
            <p className="text-muted-foreground mt-1 text-sm">{message}</p>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : 'An unknown error occurred';

  return (
    <div data-testid="provider-plugin-error" className="flex flex-col gap-4">
      <Card className="max-w-md">
        <CardContent className="flex flex-col items-start gap-3">
          <ServerCrashIcon className="text-destructive size-6" />
          <div>
            <p className="font-semibold">Failed to load</p>
            <p className="text-muted-foreground mt-1 text-sm">{message}</p>
          </div>
          <button
            type="button"
            onClick={onRetry}
            className="border-border hover:bg-muted mt-1 rounded-md border px-3 py-1.5 text-sm font-medium transition-colors">
            Retry
          </button>
        </CardContent>
      </Card>
    </div>
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
