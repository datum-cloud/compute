import { InstanceShell } from '../components/instance-shell';
import { InstanceShellWindowSkeleton } from '../components/skeletons';
import { ErrorOrRestrictedState } from '../components/states';
import { useInstance } from '../lib/api';
import { instancePageView } from '../lib/instance-page-view';
import { formatLocationName, formatLocationTooltip, useLocationIndex } from '../lib/locations';
import { useShellAvailable } from '../lib/use-shell-available';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { useEffect } from 'react';
import { useParams, useSearchParams } from 'react-router';

export default function InstanceShellWindowPage() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const [searchParams] = useSearchParams();
  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);
  const shellAvailable = useShellAvailable(projectId, instance);
  const locationIndex = useLocationIndex(projectId);
  const view = instancePageView({
    isLoading,
    hasInstance: !!instance,
    errorStatus: error?.status,
  });

  useEffect(() => {
    if (!instanceName) return;
    const previous = document.title;
    document.title = `Shell · ${instanceName}`;
    return () => {
      document.title = previous;
    };
  }, [instanceName]);

  const location = instance?.location;

  return (
    <div data-testid="compute-plugin-shell-window" className="flex min-w-0 flex-col gap-4">
      <PageTitle
        title={instanceName ?? 'Instance'}
        titleClassName="break-all"
        description={
          <span className="text-muted-foreground break-all">
            Instance{workloadName && ` of workload ${workloadName}`}
            {location && (
              <span title={formatLocationTooltip(location, locationIndex)}>
                {' '}
                · {formatLocationName(location, locationIndex)}
              </span>
            )}
          </span>
        }
      />

      {(view === 'loading' || (view === 'ready' && shellAvailable === undefined)) && (
        <InstanceShellWindowSkeleton />
      )}

      {view === 'error' && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this instance."
          onRetry={() => void refetch()}
        />
      )}

      {view === 'ready' && instance && shellAvailable === false && (
        <EmptyContent
          title="Shell isn't available for this instance"
          subtitle="Opening a shell needs permission to create shell sessions and an instance whose runtime supports them."
          size="sm"
          variant="dashed"
          className="min-h-48"
        />
      )}

      {view === 'ready' && instance && projectId && shellAvailable && (
        <InstanceShell
          variant="window"
          projectId={projectId}
          instance={instance}
          container={searchParams.get('container') ?? undefined}
        />
      )}
    </div>
  );
}
