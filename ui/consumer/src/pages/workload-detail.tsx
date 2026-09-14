/**
 * `portal.page/project` extension at `workloads/:workloadName`, exposed as
 * `WorkloadDetail`.
 *
 * Layout follows cloud-portal native overview pages (dense 2-col cards +
 * instance cards). The top strip and instance cards show KPI previews from the
 * same PromQL as instance pages; full charts live on each instance's Metrics
 * tab. The workload Metrics tab stays a placeholder — this page is not a splat
 * route.
 *
 * Breadcrumbs are left to the host `ContentWrapper` — do not re-render them
 * inside the plugin (that double-stacks chrome vs native pages).
 */
import { PluginTabs } from '../components/plugin-tabs';
import { DetailList, StatusBadge } from '../components/detail-list';
import { MetricAreaChart, formatKpiValue } from '../components/metric-area-chart';
import { StatStrip, type Stat } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { usePublishedUrl, useWorkload, useWorkloadInstances, type PublishedUrl } from '../lib/api';
import { splitSlashValue } from '../lib/format';
import { formatLocationName, formatLocationNames, useLocationIndex } from '../lib/locations';
import {
  albRpsQuery,
  cpuUsageQuery,
  memoryUsageQuery,
  useInstanceMetricIdentity,
  type InstanceIdentityLabel,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from '../lib/metrics-queries';
import { lastThirtyMinutesRange, usePrometheusCard } from '../lib/prometheus';
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
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { formatDistanceToNowStrict } from 'date-fns';
import { ArrowRightIcon, HomeIcon, Settings2Icon, SquareLibraryIcon } from 'lucide-react';
import { useMemo } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router';

const COMING_SOON = 'Coming soon';

const WORKLOAD_TABS = [
  { label: 'Overview' },
  { label: 'Deployments' },
  { label: 'Metrics' },
  { label: 'Activity' },
];

function InstanceMetricCell({
  label,
  value,
  placeholder,
}: {
  label: string;
  value?: string;
  placeholder?: boolean;
}) {
  return (
    <div>
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</p>
      <p
        className={cn(
          'mt-0.5 text-xs font-medium sm:text-sm',
          placeholder && 'text-muted-foreground font-normal'
        )}>
        {value ?? '—'}
      </p>
    </div>
  );
}

function InstanceCard({
  instance,
  projectId,
  identityLabel,
  requestsValue,
  requestsPlaceholder,
  locationLabel,
  onClick,
}: {
  instance: Instance;
  projectId?: string;
  identityLabel?: InstanceIdentityLabel;
  requestsValue: string;
  requestsPlaceholder: boolean;
  locationLabel: string;
  onClick: () => void;
}) {
  const timeRange = useMemo(() => lastThirtyMinutesRange(), []);
  const identity = identityLabel
    ? { label: identityLabel, value: instance.name }
    : undefined;
  const enabled = !!identity && !!projectId;
  const cpuQuery = enabled && identity && projectId ? cpuUsageQuery(projectId, identity) : undefined;
  const memoryQuery =
    enabled && identity && projectId ? memoryUsageQuery(projectId, identity) : undefined;
  const cpu = usePrometheusCard(cpuQuery, 'number', { enabled });
  const memory = usePrometheusCard(memoryQuery, 'bytes', { enabled });

  return (
    <Card
      size="sm"
      sectioned
      className="hover:border-foreground/20 cursor-pointer overflow-hidden transition-colors"
      onClick={onClick}
      data-testid="compute-plugin-instance-card">
      <CardHeader size="sm" bordered>
        <CardTitle className="truncate font-mono text-sm font-medium">{instance.name}</CardTitle>
        <CardDescription title={instance.location}>{locationLabel}</CardDescription>
        <CardAction>
          <Badge type={instanceStatusToBadgeType(instance.status)} theme="light" className="w-fit shrink-0">
            {instance.status}
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <MetricAreaChart
          title="CPU"
          query={cpuQuery}
          timeRange={timeRange}
          format="number"
          enabled={enabled}
          embedded
          height={40}
        />
        <div className="grid grid-cols-3 gap-2 sm:gap-3">
          <InstanceMetricCell
            label="CPU"
            value={enabled ? (cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, 'number')) : '—'}
            placeholder={!enabled}
          />
          <InstanceMetricCell
            label="Memory"
            value={enabled ? (memory.data?.formattedValue ?? formatKpiValue(memory.data?.value, 'bytes')) : '—'}
            placeholder={!enabled}
          />
          <InstanceMetricCell
            label="Requests"
            value={requestsValue}
            placeholder={requestsPlaceholder}
          />
        </div>
      </CardContent>
      <CardFooter bordered className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
        <span>Updated {formatDistanceToNowStrict(instance.createdAt, { addSuffix: true })}</span>
        <span className="flex items-center gap-1">
          View
          <Icon icon={ArrowRightIcon} size={12} />
        </span>
      </CardFooter>
    </Card>
  );
}

function albOverviewHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/overview`;
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
            <span key={alb.proxyName} className="text-sm">
              {label}
            </span>
          );
        }
        return (
          <Link
            key={alb.proxyName}
            to={albOverviewHref(projectId, alb.proxyName)}
            className="text-primary text-sm hover:underline">
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

export default function WorkloadDetail() {
  const { projectId, workloadName } = useParams<{ projectId: string; workloadName: string }>();
  const navigate = useNavigate();
  const location = useLocation();

  const { data: workload, isLoading, error, refetch } = useWorkload(projectId, workloadName);
  const { data: instances = [] } = useWorkloadInstances(projectId, workloadName);
  const published = usePublishedUrl(projectId, workloadName);
  const locationIndex = useLocationIndex(projectId);
  const instanceNames = useMemo(() => instances.map((instance) => instance.name), [instances]);
  const { identity, isLoading: identityLoading } = useInstanceMetricIdentity(
    projectId,
    instanceNames[0]
  );
  const chartsEnabled = !identityLoading && !!identity && !!projectId && instanceNames.length > 0;
  const cpuQuery =
    chartsEnabled && identity && projectId
      ? workloadCpuAvgQuery(projectId, identity.label, instanceNames)
      : undefined;
  const memoryQuery =
    chartsEnabled && identity && projectId
      ? workloadMemoryAvgQuery(projectId, identity.label, instanceNames)
      : undefined;
  const proxyId = published.data?.proxyName;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const cpu = usePrometheusCard(cpuQuery, 'number', { enabled: chartsEnabled });
  const memory = usePrometheusCard(memoryQuery, 'bytes', { enabled: chartsEnabled });
  const rps = usePrometheusCard(rpsQuery, 'requestsPerSecond', { enabled: !!proxyId });
  const requestsValue = proxyId
    ? (rps.data?.formattedValue ?? formatKpiValue(rps.data?.value, 'requestsPerSecond'))
    : '—';

  const basePath = location.pathname.replace(/\/$/, '');
  const instanceHref = (name: string) => `${basePath}/instances/${name}`;
  const workloadsHref = basePath.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = workload?.name ?? workloadName ?? 'Workload';

  const healthyCount = workload
    ? instances.length
      ? instances.filter((i) => i.status === 'Available').length
      : workload.readyReplicas
    : 0;
  const totalCount = workload ? instances.length || workload.desiredReplicas : 0;
  const allHealthy = totalCount > 0 && healthyCount === totalCount;

  const locations = workload?.locations ?? [];

  const stats: Stat[] | null = workload
    ? [
        {
          label: 'Instances',
          value: `${healthyCount}/${totalCount}`,
          className: allHealthy ? 'text-green-600 dark:text-green-500' : undefined,
        },
        {
          label: 'Available',
          value: `${healthyCount} / ${totalCount}`,
          className:
            healthyCount < totalCount
              ? 'text-yellow-600 dark:text-yellow-500'
              : allHealthy
                ? 'text-green-600 dark:text-green-500'
                : undefined,
        },
        { label: 'Locations', value: String(locations.length) },
        {
          label: 'Requests',
          value: requestsValue,
        },
        {
          label: 'Avg CPU',
          value: chartsEnabled
            ? (cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, 'number'))
            : '—',
        },
        {
          label: 'Avg Memory',
          value: chartsEnabled
            ? (memory.data?.formattedValue ?? formatKpiValue(memory.data?.value, 'bytes'))
            : '—',
        },
      ]
    : null;

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

      {!isLoading && !error && workload && stats && (
        <>
          <StatStrip stats={stats} testId="compute-plugin-workload-stats" />

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

          {instances.length === 0 ? (
            <p className="text-muted-foreground text-sm">No running instances</p>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
              {instances.map((instance) => (
                <InstanceCard
                  key={instance.uid || instance.name}
                  instance={instance}
                  projectId={projectId}
                  identityLabel={identity?.label}
                  requestsValue={requestsValue}
                  requestsPlaceholder={!proxyId}
                  locationLabel={
                    instance.location
                      ? formatLocationName(instance.location, locationIndex)
                      : 'Unknown location'
                  }
                  onClick={() => navigate(instanceHref(instance.name))}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
