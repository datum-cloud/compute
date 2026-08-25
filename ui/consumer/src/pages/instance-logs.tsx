/**
 * `portal.page/project` extension at
 * `workloads/:workloadName/instances/:instanceName/logs`, exposed as
 * `InstanceLogs`. Full datum-ui Logs explorer for the instance.
 */
import { InstanceLogsExplorer } from '../components/instance-logs';
import { InstancePageChrome } from '../components/instance-page-chrome';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance } from '../lib/api';
import { useLocation, useParams } from 'react-router';

export default function InstanceLogs() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const location = useLocation();

  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);

  const logsHref = location.pathname.replace(/\/$/, '');
  const overviewHref = logsHref.replace(/\/logs$/, '');
  const instancesHref = overviewHref.replace(/\/instances\/[^/]+$/, '');
  const workloadsHref = instancesHref.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = instance?.name ?? instanceName ?? 'Instance';

  return (
    <InstancePageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      instancesHref={instancesHref}
      overviewHref={overviewHref}
      logsHref={logsHref}
      titleName={titleName}
      workloadName={workloadName}
      instance={instance}
      onRefresh={() => void refetch()}>
      {isLoading && <LoadingSkeleton />}

      {!isLoading && (error || !instance) && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this instance."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && instance && <InstanceLogsExplorer />}
    </InstancePageChrome>
  );
}
