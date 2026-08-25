/**
 * Layout for `workloads/:workloadName/instances/:instanceName/*`.
 *
 * Host mounts this as a splat page; nested routes keep breadcrumbs, title, and
 * tabs mounted while Overview / Logs swap through `<Outlet />`.
 */
import { InstancePageChrome } from '../components/instance-page-chrome';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance } from '../lib/api';
import type { InstanceOutletContext } from './instance-outlet-context';
import InstanceLogs from './instance-logs';
import InstanceOverview from './instance-overview';
import { Outlet, Route, Routes, useLocation, useParams } from 'react-router';

function InstanceLayoutShell({
  projectHref,
  workloadsHref,
  instancesHref,
  overviewHref,
  logsHref,
  titleName,
  workloadName,
}: {
  projectHref: string;
  workloadsHref: string;
  instancesHref: string;
  overviewHref: string;
  logsHref: string;
  titleName: string;
  workloadName?: string;
}) {
  const { projectId, instanceName } = useParams<{
    projectId: string;
    instanceName: string;
  }>();
  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);

  return (
    <InstancePageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      instancesHref={instancesHref}
      overviewHref={overviewHref}
      logsHref={logsHref}
      titleName={instance?.name ?? titleName}
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

      {!isLoading && !error && instance && (
        <Outlet
          context={
            {
              instance,
              workloadName,
              logsHref,
            } satisfies InstanceOutletContext
          }
        />
      )}
    </InstancePageChrome>
  );
}

export default function InstanceDetail() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const location = useLocation();

  const path = location.pathname.replace(/\/$/, '');
  const overviewHref = path.endsWith('/logs') ? path.slice(0, -'/logs'.length) : path;
  const logsHref = `${overviewHref}/logs`;
  const instancesHref = overviewHref.replace(/\/instances\/[^/]+$/, '');
  const workloadsHref = instancesHref.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = instanceName ?? 'Instance';

  return (
    <Routes>
      <Route
        element={
          <InstanceLayoutShell
            projectHref={projectHref}
            workloadsHref={workloadsHref}
            instancesHref={instancesHref}
            overviewHref={overviewHref}
            logsHref={logsHref}
            titleName={titleName}
            workloadName={workloadName}
          />
        }>
        <Route index element={<InstanceOverview />} />
        <Route path="logs" element={<InstanceLogs />} />
      </Route>
    </Routes>
  );
}
