import { InstanceShell } from '../components/instance-shell';
import { InstanceShellWindowSkeleton } from '../components/skeletons';
import { ErrorOrRestrictedState } from '../components/states';
import { useInstance } from '../lib/api';
import { instancePageView } from '../lib/instance-page-view';
import { formatLocationName, formatLocationTooltip, useLocationIndex } from '../lib/locations';
import { handoffStorage, handoffTarget, shellWindowStart } from '../lib/shell-popout';
import { useShellAvailable } from '../lib/use-shell-available';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useEffect } from 'react';
import { useParams, useSearchParams } from 'react-router';

function ShellWindowTitle({
  instanceName,
  workloadName,
  location,
  locationTooltip,
}: {
  instanceName: string;
  workloadName?: string;
  location?: string;
  locationTooltip?: string;
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
      <h1 className="truncate font-mono text-sm font-semibold">{instanceName}</h1>
      <p className="text-muted-foreground truncate text-xs">
        {workloadName && <>Workload {workloadName}</>}
        {location && (
          <span title={locationTooltip}>
            {workloadName && ' · '}
            {location}
          </span>
        )}
      </p>
    </div>
  );
}

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
  const title = (
    <ShellWindowTitle
      instanceName={instanceName ?? 'Instance'}
      workloadName={workloadName}
      location={location ? formatLocationName(location, locationIndex) : undefined}
      locationTooltip={location ? formatLocationTooltip(location, locationIndex) : undefined}
    />
  );

  if (view === 'ready' && instance && projectId && shellAvailable) {
    return (
      <div data-testid="compute-plugin-shell-window" className="flex min-w-0 flex-col">
        <InstanceShell
          variant="window"
          title={title}
          projectId={projectId}
          instance={instance}
          start={shellWindowStart(searchParams, instance.containers, {
            storage: handoffStorage(),
            target: handoffTarget(projectId, instance.name),
            now: Date.now(),
          })}
        />
      </div>
    );
  }

  return (
    <div data-testid="compute-plugin-shell-window" className="flex min-w-0 flex-col">
      <header className="border-b px-4 py-2">{title}</header>
      <div className="p-4">
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
      </div>
    </div>
  );
}
