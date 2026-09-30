export type InstancePageView = 'loading' | 'error' | 'ready';

export function instancePageView({
  isLoading,
  hasInstance,
  errorStatus,
}: {
  isLoading: boolean;
  hasInstance: boolean;
  errorStatus: number | undefined;
}): InstancePageView {
  if (isLoading) return 'loading';
  if (!hasInstance || errorStatus === 404) return 'error';
  return 'ready';
}
