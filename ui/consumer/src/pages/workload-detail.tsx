/**
 * `portal.page/project` extension at `workloads/:workloadName`, exposed as
 * `WorkloadDetail`.
 *
 * Layout follows cloud-portal native overview pages (dense 2-col cards +
 * full-width sections) and the workloads mockup (stat strip, locations map,
 * instance cards). Telemetry slots show Coming soon.
 *
 * Breadcrumbs are left to the host `ContentWrapper` — do not re-render them
 * inside the plugin (that double-stacks chrome vs native pages).
 */
import { PluginTabs } from '../components/plugin-tabs';
import { DetailList, StatusBadge } from '../components/detail-list';
import { StatStrip, type Stat } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { WorldMap } from '../components/world-map';
import { useWorkload, useWorkloadInstances } from '../lib/api';
import { splitSlashValue } from '../lib/format';
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
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { formatDistanceToNowStrict } from 'date-fns';
import {
  ArrowRightIcon,
  HomeIcon,
  MapPinIcon,
  Settings2Icon,
  SquareLibraryIcon,
} from 'lucide-react';
import { useLocation, useNavigate, useParams } from 'react-router';

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
        {value ?? COMING_SOON}
      </p>
    </div>
  );
}

function InstanceCard({ instance, onClick }: { instance: Instance; onClick: () => void }) {
  return (
    <div
      className="border-card-border bg-card hover:border-foreground/20 flex cursor-pointer flex-col gap-4 rounded-xl border p-4 shadow transition-colors sm:p-5"
      onClick={onClick}
      data-testid="compute-plugin-instance-card">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-3">
        <div className="min-w-0">
          <p className="truncate font-mono text-sm font-medium">{instance.name}</p>
          <p className="text-muted-foreground mt-0.5 text-xs">{instance.location ?? 'Unknown location'}</p>
        </div>
        <Badge type={instanceStatusToBadgeType(instance.status)} theme="light" className="w-fit shrink-0">
          {instance.status}
        </Badge>
      </div>

      <div
        className="border-border bg-muted/40 text-muted-foreground flex h-10 items-center justify-center rounded-md border border-dashed text-xs"
        data-testid="compute-plugin-instance-metrics-placeholder">
        {COMING_SOON}
      </div>

      <div className="border-border grid grid-cols-3 gap-2 border-t pt-4 sm:gap-3">
        <InstanceMetricCell label="CPU" placeholder />
        <InstanceMetricCell label="Memory" placeholder />
        <InstanceMetricCell label="Requests" placeholder />
      </div>

      <div className="border-border text-muted-foreground flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-xs">
        <span>Updated {formatDistanceToNowStrict(instance.createdAt, { addSuffix: true })}</span>
        <span className="flex items-center gap-1">
          View
          <Icon icon={ArrowRightIcon} size={12} />
        </span>
      </div>
    </div>
  );
}

function GeneralCard({
  workload,
  healthyCount,
  totalCount,
}: {
  workload: Workload;
  healthyCount: number;
  totalCount: number;
}) {
  return (
    <Card
      className="h-full w-full gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-workload-general">
      <CardContent className="p-0 sm:px-6 sm:pb-4">
        <div className="mb-4 flex items-center gap-2.5">
          <Icon icon={SquareLibraryIcon} size={20} className="text-muted-foreground" />
          <span className="text-base font-semibold">General</span>
        </div>
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

function ConfigurationCard({ workload }: { workload: Workload }) {
  const { main: resourceShort } = splitSlashValue(workload.resources ?? '');

  return (
    <Card
      className="h-full w-full gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-workload-configuration">
      <CardContent className="p-0 sm:px-6 sm:pb-4">
        <div className="mb-4 flex items-center gap-2.5">
          <Icon icon={Settings2Icon} size={20} className="text-muted-foreground" />
          <span className="text-base font-semibold">Configuration</span>
        </div>
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
                  workload.locations.join(', ')
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
          value: COMING_SOON,
          className: 'text-muted-foreground text-sm font-medium',
        },
        {
          label: 'Avg CPU',
          value: COMING_SOON,
          className: 'text-muted-foreground text-sm font-medium',
        },
        {
          label: 'Avg Memory',
          value: COMING_SOON,
          className: 'text-muted-foreground text-sm font-medium',
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
            />
            <ConfigurationCard workload={workload} />
          </div>

          <Card
            className="w-full overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
            data-testid="compute-plugin-workload-locations">
            <CardContent className="flex flex-col gap-4 p-0 sm:px-6 sm:pb-4">
              <div className="flex items-center gap-2.5">
                <Icon icon={MapPinIcon} size={20} className="text-muted-foreground" />
                <span className="text-base font-semibold">Instance Locations</span>
              </div>
              <p className="text-muted-foreground text-sm">
                {locations.length > 0
                  ? `Locations where this workload is deployed: ${locations.join(', ')}.`
                  : 'Locations where this workload is deployed.'}
              </p>
              <WorldMap className="bg-background aspect-[16/9] w-full overflow-hidden rounded-lg border sm:aspect-[2.5/1]" />
            </CardContent>
          </Card>

          {instances.length === 0 ? (
            <p className="text-muted-foreground text-sm">No running instances</p>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
              {instances.map((instance) => (
                <InstanceCard
                  key={instance.uid || instance.name}
                  instance={instance}
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
