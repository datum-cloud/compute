/**
 * Layout for `:workloadName/instances/:instanceName/*`.
 *
 * Host mounts this as a splat page; nested routes keep breadcrumbs, title, and
 * tabs mounted while Overview / Logs / Metrics swap through `<Outlet />`.
 */
import { InstancePageChrome } from '../components/instance-page-chrome';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance, usePublishedUrl } from '../lib/api';
import { formatLocationName, formatLocationTooltip, useLocationIndex } from '../lib/locations';
import type { InstanceOutletContext } from './instance-outlet-context';
import InstanceLogs from './instance-logs';
import InstanceMetrics from './instance-metrics';
import InstanceOverview from './instance-overview';
import { Outlet, Route, Routes, useLocation, useParams } from 'react-router';

function InstanceLayoutShell({
  projectHref,
  workloadsHref,
  instancesHref,
  overviewHref,
  logsHref,
  metricsHref,
  titleName,
  workloadName,
}: {
  projectHref: string;
  workloadsHref: string;
  instancesHref: string;
  overviewHref: string;
  logsHref: string;
  metricsHref: string;
  titleName: string;
  workloadName?: string;
}) {
  const { projectId, instanceName } = useParams<{
    projectId: string;
    instanceName: string;
  }>();
  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);
  const published = usePublishedUrl(projectId, workloadName ?? instance?.workloadName);
  const locationIndex = useLocationIndex(projectId);

  return (
    <InstancePageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      instancesHref={instancesHref}
      overviewHref={overviewHref}
      logsHref={logsHref}
      metricsHref={metricsHref}
      titleName={instance?.name ?? titleName}
      workloadName={workloadName}
      instance={instance}
      locationLabel={
        instance?.location ? formatLocationName(instance.location, locationIndex) : undefined
      }
      locationTooltip={
        instance?.location ? formatLocationTooltip(instance.location, locationIndex) : undefined
      }>
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
              projectId,
              logsHref,
              metricsHref,
              proxyId: published.data?.proxyName,
              albHostname: published.data?.hostname,
              albDisplayName: published.data?.displayName,
              albLoading: published.isLoading,
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
  const overviewHref = path.replace(/\/(logs|metrics)$/, '');
  const logsHref = `${overviewHref}/logs`;
  const metricsHref = `${overviewHref}/metrics`;
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
            metricsHref={metricsHref}
            titleName={titleName}
            workloadName={workloadName}
          />
        }>
        <Route index element={<InstanceOverview />} />
        <Route path="logs" element={<InstanceLogs />} />
        <Route path="metrics" element={<InstanceMetrics />} />
      </Route>
    </Routes>
  );
}
