/**
 * `portal.page/project` extension at `:workloadName`, exposed as
 * `WorkloadDetail`.
 *
 * Layout follows cloud-portal ALB overview pages (health strip, sparkline
 * metrics, 2×2 dashboard). General/Configuration stay below the dashboard.
 *
 * Breadcrumbs are left to the host `ContentWrapper` — do not re-render them
 * inside the plugin (that double-stacks chrome vs native pages).
 */
import { PluginTabs } from '../components/plugin-tabs';
import { DetailList, StatusBadge } from '../components/detail-list';
import { WorkloadHealthStrip } from '../components/health-strip';
import { RecentInstanceLogs } from '../components/instance-logs';
import { MetricAreaChart } from '../components/metric-area-chart';
import { WorkloadMetricsStrip } from '../components/metrics-strip';
import { TopologyCard } from '../components/topology-card';
import {
  DEFAULT_OVERVIEW_RANGE,
  useOverviewRange,
  type OverviewRangeValue,
} from '../components/overview-range';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { usePublishedUrl, useWorkload, useWorkloadInstances, type PublishedUrl } from '../lib/api';
import { splitSlashValue } from '../lib/format';
import { formatLocationCountry, formatLocationName, formatLocationNames, formatLocationTooltip, useLocationIndex, type LocationIndex } from '../lib/locations';
import {
  albRpsQuery,
  identityValues,
  useInstanceMetricIdentity,
} from '../lib/metrics-queries';
import {
  instanceStatusToBadgeType,
  workloadHealthToBadgeType,
  type Instance,
  type Workload,
} from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Icon } from '@datum-cloud/datum-ui/icons';
import {
  ActivityIcon,
  ArrowRightIcon,
  GlobeIcon,
  HomeIcon,
  MapPinIcon,
  Settings2Icon,
  SquareLibraryIcon,
} from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router';

const COMING_SOON = 'Coming soon';
// Inline rather than `h-[27rem]`: the host only compiles that class because its
// own ALB overview happens to use it today.
const PANEL_STYLE = { height: '27rem' } as const;

const WORKLOAD_TABS = [
  { label: 'Overview' },
  { label: 'Deployments' },
  { label: 'Metrics' },
  { label: 'Activity' },
];

function albOverviewHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/overview`;
}

function albMetricsHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/metrics`;
}

function LoadBalancerValue({
  projectId,
  published,
  isLoading,
}: {
  projectId?: string;
  published?: PublishedUrl | null;
  isLoading: boolean;
}) {
  if (isLoading) {
    return <span className="text-muted-foreground">—</span>;
  }
  if (!published?.proxies.length) {
    return <span className="text-muted-foreground">Not connected</span>;
  }

  return (
    <div className="flex flex-col gap-1">
      {published.proxies.map((alb) => {
        const label = alb.displayName || alb.proxyName;
        if (!projectId) {
          return (
            <span key={alb.proxyName} className="inline-flex items-center gap-1.5 text-sm">
              <Icon icon={GlobeIcon} size={14} className="text-muted-foreground shrink-0" />
              {label}
            </span>
          );
        }
        return (
          <Link
            key={alb.proxyName}
            to={albOverviewHref(projectId, alb.proxyName)}
            className="text-primary inline-flex items-center gap-1.5 text-sm hover:underline">
            <Icon icon={GlobeIcon} size={14} className="shrink-0" />
            {label}
          </Link>
        );
      })}
    </div>
  );
}

function GeneralCard({
  workload,
  healthyCount,
  totalCount,
  projectId,
  published,
  publishedLoading,
}: {
  workload: Workload;
  healthyCount: number;
  totalCount: number;
  projectId?: string;
  published?: PublishedUrl | null;
  publishedLoading: boolean;
}) {
  return (
    <Card
      size="sm"
      sectioned
      className="h-full w-full overflow-hidden"
      data-testid="compute-plugin-workload-general">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={SquareLibraryIcon} size={16} className="text-secondary" />
          General
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            {
              label: 'Status',
              content: (
                <StatusBadge type={workloadHealthToBadgeType(workload.health)}>
                  {workload.health}
                </StatusBadge>
              ),
            },
            {
              label: 'Resource Name',
              content: <span className="font-mono text-sm">{workload.name}</span>,
            },
            {
              label: 'Load balancer',
              content: (
                <LoadBalancerValue
                  projectId={projectId}
                  published={published}
                  isLoading={publishedLoading}
                />
              ),
            },
            {
              label: 'Instances',
              content: `${healthyCount}/${totalCount}`,
            },
            {
              label: 'Created At',
              content: workload.createdAt.toLocaleDateString('en-GB', {
                day: '2-digit',
                month: 'short',
                year: '2-digit',
                hour: '2-digit',
                minute: '2-digit',
                second: '2-digit',
                hour12: false,
              }),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function ConfigurationCard({
  workload,
  locationLabel,
}: {
  workload: Workload;
  locationLabel: string;
}) {
  const { main: resourceShort } = splitSlashValue(workload.resources ?? '');

  return (
    <Card
      size="sm"
      sectioned
      className="h-full w-full overflow-hidden"
      data-testid="compute-plugin-workload-configuration">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={Settings2Icon} size={16} className="text-secondary" />
          Configuration
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            {
              label: 'Runtime',
              content: workload.runtimeType ?? (
                <span className="text-muted-foreground">{COMING_SOON}</span>
              ),
            },
            {
              label: 'Image',
              content: workload.image ? (
                <span className="block max-w-full truncate font-mono text-xs" title={workload.image}>
                  {workload.image}
                </span>
              ) : (
                <span className="text-muted-foreground">{COMING_SOON}</span>
              ),
            },
            {
              label: 'Resources',
              content: resourceShort || workload.resources || (
                <span className="text-muted-foreground">{COMING_SOON}</span>
              ),
            },
            {
              label: 'Replicas',
              content:
                workload.replicasPerRegion !== undefined
                  ? `${workload.replicasPerRegion}/location · ${workload.desiredReplicas} total`
                  : `${workload.desiredReplicas} total`,
            },
            {
              label: 'Locations',
              content:
                workload.locations.length > 0 ? (
                  <span title={workload.locations.join(', ')}>{locationLabel}</span>
                ) : (
                  <span className="text-muted-foreground">—</span>
                ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function LiveTrafficCard({
  projectId,
  proxyId,
  range,
}: {
  projectId?: string;
  proxyId?: string;
  range: { start: Date; end: Date };
}) {
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;

  return (
    <Card size="sm" sectioned className="flex h-full flex-col overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={ActivityIcon} size={16} className="text-secondary" />
          Live traffic
        </CardTitle>
      </CardHeader>
      <CardContent className="relative flex min-h-0 flex-1 flex-col">
        {rpsQuery ? (
          <MetricAreaChart
            title="Requests"
            query={rpsQuery}
            timeRange={range}
            format="requestsPerSecond"
            enabled
            embedded
            fill
          />
        ) : (
          <p className="text-muted-foreground flex flex-1 items-center justify-center text-sm">
            Coming soon
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function InstancesPanel({
  instances,
  locationIndex,
  onOpen,
}: {
  instances: Instance[];
  locationIndex: LocationIndex;
  onOpen: (name: string) => void;
}) {
  return (
    <Card size="sm" sectioned className="flex h-full flex-col overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={SquareLibraryIcon} size={16} className="text-secondary" />
          Instances
        </CardTitle>
        <CardAction>
          <span className="text-muted-foreground text-xs tabular-nums">{instances.length}</span>
        </CardAction>
      </CardHeader>
      <CardContent padding="none" className="min-h-0 flex-1 overflow-y-auto">
        {instances.length === 0 ? (
          <p className="text-muted-foreground px-4 py-6 text-sm">No running instances</p>
        ) : (
          <ul className="divide-border divide-y">
            {instances.map((instance) => (
              <li key={instance.uid || instance.name}>
                <button
                  type="button"
                  className="hover:bg-muted/40 flex w-full items-center gap-3 px-4 py-3 text-left"
                  onClick={() => onOpen(instance.name)}
                  data-testid="compute-plugin-instance-card">
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-sm">{instance.name}</span>
                    <span
                      className="text-muted-foreground text-xs"
                      title={
                        instance.location
                          ? formatLocationTooltip(instance.location, locationIndex)
                          : undefined
                      }>
                      {instance.location
                        ? formatLocationName(instance.location, locationIndex)
                        : 'Unknown location'}
                    </span>
                  </span>
                  <Badge type={instanceStatusToBadgeType(instance.status)} theme="light" className="w-fit shrink-0">
                    {instance.status}
                  </Badge>
                  <Icon icon={ArrowRightIcon} size={14} className="text-muted-foreground shrink-0" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function LocationsPanel({
  instances,
  locations,
  locationIndex,
}: {
  instances: Instance[];
  locations: string[];
  locationIndex: LocationIndex;
}) {
  const rows = (locations.length > 0 ? locations : [...new Set(instances.map((i) => i.location).filter(Boolean))]).map(
    (name) => {
      const atLocation = instances.filter((instance) => instance.location === name);
      const healthy = atLocation.filter((instance) => instance.status === 'Available').length;
      return {
        name,
        label: formatLocationName(name, locationIndex),
        country: formatLocationCountry(name, locationIndex),
        tooltip: formatLocationTooltip(name, locationIndex),
        healthy,
        total: atLocation.length,
      };
    }
  );

  return (
    <Card size="sm" sectioned className="flex h-full flex-col overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={MapPinIcon} size={16} className="text-secondary" />
          Locations
        </CardTitle>
      </CardHeader>
      <CardContent padding="none" className="min-h-0 flex-1 overflow-y-auto">
        {rows.length === 0 ? (
          <p className="text-muted-foreground px-4 py-6 text-sm">No locations yet</p>
        ) : (
          <ul className="divide-border divide-y">
            {rows.map((row) => (
              <li key={row.name} className="flex items-center gap-3 px-4 py-3" title={row.tooltip}>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm">{row.label}</span>
                  {row.country ? (
                    <span className="text-muted-foreground block truncate text-xs">{row.country}</span>
                  ) : null}
                </span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {row.total > 0 ? `${row.healthy}/${row.total}` : '—'}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

export default function WorkloadDetail() {
  const { projectId, workloadName } = useParams<{ projectId: string; workloadName: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const [rangeValue, setRangeValue] = useState<OverviewRangeValue>(DEFAULT_OVERVIEW_RANGE);
  const range = useOverviewRange(rangeValue);

  const { data: workload, isLoading, error, refetch } = useWorkload(projectId, workloadName);
  const { data: instances = [] } = useWorkloadInstances(projectId, workloadName);
  const published = usePublishedUrl(projectId, workloadName);
  const locationIndex = useLocationIndex(projectId);
  const { identity } = useInstanceMetricIdentity(projectId, instances[0]);
  const metricKeys = useMemo(
    () => (identity ? identityValues(instances) : []),
    [instances, identity]
  );
  const proxyId = published.data?.proxyName;
  const albLabel = published.data?.displayName || published.data?.proxyName;

  const basePath = location.pathname.replace(/\/$/, '');
  // Stable builders: TopologyCard memoises its node lists on these.
  const instanceHref = useCallback((name: string) => `${basePath}/instances/${name}`, [basePath]);
  const instanceMetricsHref = useCallback(
    (name: string) => `${basePath}/instances/${name}/metrics`,
    [basePath]
  );
  const albHrefFor = useMemo(
    () => (projectId ? (proxyName: string) => albOverviewHref(projectId, proxyName) : undefined),
    [projectId]
  );
  const albMetricsHrefFor = useMemo(
    () => (projectId ? (proxyName: string) => albMetricsHref(projectId, proxyName) : undefined),
    [projectId]
  );
  const workloadsHref = basePath.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = workload?.name ?? workloadName ?? 'Workload';
  const logsHref = instances[0] ? `${instanceHref(instances[0].name)}/logs` : basePath;

  const healthyCount = workload
    ? instances.length
      ? instances.filter((i) => i.status === 'Available').length
      : workload.readyReplicas
    : 0;
  const totalCount = workload ? instances.length || workload.desiredReplicas : 0;
  const idle = !proxyId;

  return (
    <div data-testid="compute-plugin-workload-detail" className="flex min-w-0 flex-col gap-6">
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <Icon icon={HomeIcon} size={16} />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={workloadsHref}>Workloads</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbPage className="truncate">{titleName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={titleName}
        titleClassName="break-all sm:break-normal"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        description="Workload overview"
        actions={
          workload ? (
            <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
              {workload.health}
            </Badge>
          ) : undefined
        }
      />

      <PluginTabs tabs={WORKLOAD_TABS} testId="compute-plugin-workload-tabs" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && (error || !workload) && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this workload."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && workload && (
        <>
          <WorkloadHealthStrip
            health={workload.health}
            healthyCount={healthyCount}
            totalCount={totalCount}
            locationCount={workload.locations.length}
            albHref={projectId && proxyId ? albOverviewHref(projectId, proxyId) : undefined}
            albLabel={albLabel}
          />

          <WorkloadMetricsStrip
            projectId={projectId}
            proxyId={proxyId}
            identityLabel={identity?.label}
            instanceKeys={metricKeys}
            range={range}
            onRangeChange={setRangeValue}
            idle={idle}
          />

          <TopologyCard
            projectId={projectId}
            workload={workload}
            instances={instances}
            albs={published.data?.proxies ?? []}
            locationIndex={locationIndex}
            instanceHref={instanceHref}
            instanceMetricsHref={instanceMetricsHref}
            albHref={albHrefFor}
            albMetricsHref={albMetricsHrefFor}
          />

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div style={PANEL_STYLE}>
              <LiveTrafficCard
                projectId={projectId}
                proxyId={proxyId}
                range={range.timeRange}
              />
            </div>
            <div style={PANEL_STYLE}>
              <InstancesPanel
                instances={instances}
                locationIndex={locationIndex}
                onOpen={(name) => navigate(instanceHref(name))}
              />
            </div>
            <div style={PANEL_STYLE}>
              <LocationsPanel
                instances={instances}
                locations={workload.locations}
                locationIndex={locationIndex}
              />
            </div>
            <div style={PANEL_STYLE}>
              <RecentInstanceLogs
                logsHref={logsHref}
                projectId={projectId}
                proxyId={proxyId}
                albHostname={published.data?.hostname}
              />
            </div>
          </div>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <GeneralCard
              workload={workload}
              healthyCount={healthyCount}
              totalCount={totalCount}
              projectId={projectId}
              published={published.data}
              publishedLoading={published.isLoading}
            />
            <ConfigurationCard
              workload={workload}
              locationLabel={formatLocationNames(workload.locations, locationIndex)}
            />
          </div>
        </>
      )}
    </div>
  );
}
