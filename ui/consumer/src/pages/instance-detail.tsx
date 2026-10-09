/**
 * Layout for `:workloadName/instances/:instanceName/*`.
 *
 * Host mounts this as a splat page; nested routes keep breadcrumbs, title, and
 * tabs mounted while Overview / Logs / Metrics swap through `<Outlet />`.
 */
import { InstancePageChrome } from '../components/instance-page-chrome';
import {
  InstanceLogsSkeleton,
  InstanceMetricsSkeleton,
  InstanceOverviewSkeleton,
} from '../components/skeletons';
import { ErrorOrRestrictedState } from '../components/states';
import { useInstance, usePublishedUrl } from '../lib/api';
import { instancePageView } from '../lib/instance-page-view';
import { useShellAvailable } from '../lib/use-shell-available';
import { formatLocationName, formatLocationTooltip, useLocationIndex } from '../lib/locations';
import type { InstanceOutletContext } from './instance-outlet-context';
import InstanceLogs from './instance-logs';
import InstanceMetrics from './instance-metrics';
import InstanceOverview from './instance-overview';
import { lazy, Suspense } from 'react';
import { Outlet, Route, Routes, useLocation, useParams } from 'react-router';

const InstanceShellPage = lazy(() => import('./instance-shell'));
// activity-ui is sizeable; load it only when the tab is opened.
const InstanceActivity = lazy(() => import('./instance-activity'));

function InstanceLayoutShell({
  projectHref,
  workloadsHref,
  instancesHref,
  overviewHref,
  logsHref,
  metricsHref,
  shellHref,
  titleName,
  workloadName,
}: {
  projectHref: string;
  workloadsHref: string;
  instancesHref: string;
  overviewHref: string;
  logsHref: string;
  metricsHref: string;
  shellHref: string;
  titleName: string;
  workloadName?: string;
}) {
  const { pathname } = useLocation();
  const { projectId, instanceName } = useParams<{
    projectId: string;
    instanceName: string;
  }>();
  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);
  const published = usePublishedUrl(projectId, workloadName ?? instance?.workloadName);
  const locationIndex = useLocationIndex(projectId);
  const shellAvailable = useShellAvailable(projectId, instance);
  const view = instancePageView({
    isLoading,
    hasInstance: !!instance,
    errorStatus: error?.status,
  });

  return (
    <InstancePageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      instancesHref={instancesHref}
      overviewHref={overviewHref}
      logsHref={logsHref}
      metricsHref={metricsHref}
      shellHref={shellAvailable ? shellHref : undefined}
      titleName={instance?.name ?? titleName}
      workloadName={workloadName}
      instance={instance}
      locationLabel={
        instance?.location ? formatLocationName(instance.location, locationIndex) : undefined
      }
      locationTooltip={
        instance?.location ? formatLocationTooltip(instance.location, locationIndex) : undefined
      }>
      {view === 'loading' &&
        (pathname === metricsHref || pathname.startsWith(`${metricsHref}/`) ? (
          <InstanceMetricsSkeleton />
        ) : pathname === logsHref || pathname.startsWith(`${logsHref}/`) ? (
          <InstanceLogsSkeleton />
        ) : (
          <InstanceOverviewSkeleton />
        ))}

      {view === 'error' && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this instance."
          onRetry={() => void refetch()}
        />
      )}

      {view === 'ready' && instance && (
        <Outlet
          context={
            {
              instance,
              workloadName,
              workloadHref: instancesHref,
              projectId,
              logsHref,
              metricsHref,
              shellAvailable,
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
  const overviewHref = path.replace(/\/(logs|metrics|shell|activity)$/, '');
  const logsHref = `${overviewHref}/logs`;
  const metricsHref = `${overviewHref}/metrics`;
  const shellHref = `${overviewHref}/shell`;
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
            shellHref={shellHref}
            titleName={titleName}
            workloadName={workloadName}
          />
        }>
        <Route index element={<InstanceOverview />} />
        <Route path="logs" element={<InstanceLogs />} />
        <Route path="metrics" element={<InstanceMetrics />} />
        <Route
          path="activity"
          element={
            <Suspense fallback={null}>
              <InstanceActivity />
            </Suspense>
          }
        />
        <Route
          path="shell"
          element={
            <Suspense fallback={null}>
              <InstanceShellPage />
            </Suspense>
          }
        />
      </Route>
    </Routes>
  );
}
