/**
 * `portal.page/project` extension at `workloads/:workloadName`, exposed as
 * `WorkloadDetail`.
 *
 * Ported from cloud-portal PR #1315's
 * `app/routes/project/detail/compute/workloads/detail/index.tsx` (Overview
 * tab) with its embedded instance list from
 * `.../workloads/detail/instances/index.tsx`'s sibling table in the workload
 * overview. Per the plugin's scope, only the Overview tab is ported — the
 * Activity/Metrics/Settings tabs from PR #1315 are intentionally not
 * included in v1.
 *
 * Rewritten for the plugin runtime:
 *  - `useWorkload`/`useWorkloadInstances` poll `/api/proxy/…` (see
 *    `lib/api.ts`) instead of PR #1315's generated-SDK service +
 *    `useResourceWatch`/`useGuardedRouteData`.
 *  - No server loader — loading/error/restricted states are handled inline
 *    instead of via `defineResourceRoute`/`runDetailLoader`.
 *  - Instance row navigation builds the child path from `useLocation()`
 *    instead of the portal's `paths.config.ts` + `getPathWithParams`.
 */
import { PluginTabs } from '../components/plugin-tabs';
import { Sparkline } from '../components/sparkline';
import { StatStrip, type Stat } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { WorldMap } from '../components/world-map';
import { useWorkload, useWorkloadInstances } from '../lib/api';
import { formatUptime, splitSlashValue } from '../lib/format';
import { instanceStatusToBadgeType, workloadHealthToBadgeType, type Instance } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { cn } from '@datum-cloud/datum-ui/utils';
import { ArrowRightIcon, HomeIcon, MapPinIcon } from 'lucide-react';
import { useLocation, useNavigate, useParams } from 'react-router';

const INSTANCE_STATUS_DOT: Record<Instance['status'], string> = {
  Available: 'bg-green-500',
  Pending: 'bg-yellow-500',
  Failed: 'bg-red-500',
  Unknown: 'bg-muted-foreground',
};

const WORKLOAD_TABS = ['Overview', 'Deployments', 'Metrics', 'Activity'];

function InstanceCard({ instance, onClick }: { instance: Instance; onClick: () => void }) {
  return (
    <div
      className="border-card-border bg-card hover:border-foreground/20 flex cursor-pointer flex-col gap-3 rounded-xl border p-5 shadow transition-colors"
      onClick={onClick}
      data-testid="compute-plugin-instance-card">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-primary truncate font-mono text-sm font-medium">{instance.name}</p>
          <p className="text-muted-foreground mt-0.5 text-xs">{instance.city ?? 'Unknown region'}</p>
        </div>
        <Badge type={instanceStatusToBadgeType(instance.status)} theme="light" className="shrink-0">
          {instance.status}
        </Badge>
      </div>

      <Sparkline seedKey={instance.uid || instance.name} className="h-10 w-full" />

      {(instance.instanceType || instance.ports.length > 0) && (
        <div className="border-card-border flex flex-wrap gap-1.5 border-t pt-3">
          {instance.instanceType && (
            <span className="bg-muted text-muted-foreground rounded-full px-2 py-0.5 text-xs">
              {instance.instanceType}
            </span>
          )}
          {instance.ports.map((port) => (
            <span
              key={port}
              className="bg-muted text-muted-foreground rounded-full px-2 py-0.5 font-mono text-xs">
              {port}
            </span>
          ))}
        </div>
      )}

      <div className="border-card-border text-muted-foreground flex items-center justify-between border-t pt-3 text-xs">
        <span className="flex items-center gap-1.5">
          <span className={cn('size-1.5 shrink-0 rounded-full', INSTANCE_STATUS_DOT[instance.status])} />
          Up {formatUptime(instance.createdAt)}
        </span>
        <span className="flex items-center gap-1">
          View instance
          <ArrowRightIcon className="size-3" />
        </span>
      </div>
    </div>
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

  if (isLoading) return <LoadingSkeleton />;

  if (error || !workload) {
    return (
      <ErrorOrRestrictedState
        error={error}
        restrictedMessage="You don't have permission to view this workload."
        onRetry={() => void refetch()}
      />
    );
  }

  // Health counts — prefer live instance data, fall back to replica counts.
  const healthyCount = instances.length
    ? instances.filter((i) => i.status === 'Available').length
    : workload.currentReplicas;
  const totalCount = instances.length || workload.desiredReplicas;
  const allHealthy = totalCount > 0 && healthyCount === totalCount;

  const regions = workload.regions ?? [];
  const { main: resourceShort } = splitSlashValue(workload.resources ?? '');

  const createdFormatted = workload.createdAt
    ? workload.createdAt.toLocaleDateString('en-US', {
        month: 'long',
        day: 'numeric',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      })
    : null;

  const stats: Stat[] = [
    {
      label: 'Instances',
      value: `${healthyCount}/${totalCount}`,
      className: allHealthy ? 'text-green-600 dark:text-green-500' : undefined,
    },
    { label: 'Regions', value: regions.length > 0 ? regions.join(', ') : '—' },
    ...(workload.resources ? [{ label: 'Resources', value: resourceShort }] : []),
    {
      label: 'Replicas',
      value:
        workload.replicasPerRegion !== undefined
          ? `${workload.replicasPerRegion}/region · ${workload.desiredReplicas} total`
          : `${workload.desiredReplicas} total`,
    },
  ];

  return (
    <div data-testid="compute-plugin-workload-detail" className="flex flex-col gap-4 p-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <HomeIcon className="size-4" />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={workloadsHref}>Workloads</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>{workload.name}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={workload.name}
        description="Workload overview"
        actions={
          <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
            {workload.health}
          </Badge>
        }
      />

      <PluginTabs tabs={WORKLOAD_TABS} testId="compute-plugin-workload-tabs" />

      <StatStrip stats={stats} testId="compute-plugin-workload-stats" />

      {/* Configuration */}
      {/*
      <Card className="rounded-xl shadow-none">
        <CardHeader>
          <CardTitle className="mb-0 pb-0 text-base font-semibold">Configuration</CardTitle>
        </CardHeader>
        <CardContent className="px-5 pt-0 pb-5">
          <dl className="divide-border divide-y text-sm">
            <div className="flex items-baseline justify-between gap-4 py-2.5">
              <dt className="text-muted-foreground shrink-0">Resource name</dt>
              <dd className="min-w-0 truncate text-right font-mono">{workload.name}</dd>
            </div>
            {workload.runtimeType && (
              <div className="flex items-baseline justify-between gap-4 py-2.5">
                <dt className="text-muted-foreground shrink-0">Runtime</dt>
                <dd>{workload.runtimeType}</dd>
              </div>
            )}
            {workload.image && (
              <div className="flex items-baseline justify-between gap-4 py-2.5">
                <dt className="text-muted-foreground shrink-0">Container image</dt>
                <dd className="min-w-0 truncate text-right font-mono text-xs">{workload.image}</dd>
              </div>
            )}
            {regions.length > 0 && (
              <div className="flex items-baseline justify-between gap-4 py-2.5">
                <dt className="text-muted-foreground shrink-0">Regions</dt>
                <dd>{regions.join(', ')}</dd>
              </div>
            )}
            {workload.resources && (
              <div className="flex items-baseline justify-between gap-4 py-2.5">
                <dt className="text-muted-foreground shrink-0">Resources</dt>
                <dd className="font-mono text-xs">{workload.resources}</dd>
              </div>
            )}
            <div className="flex items-baseline justify-between gap-4 py-2.5">
              <dt className="text-muted-foreground shrink-0">Replicas</dt>
              <dd>
                {workload.replicasPerRegion !== undefined
                  ? `${workload.replicasPerRegion} per region · ${workload.desiredReplicas} total`
                  : `${workload.desiredReplicas} total`}
              </dd>
            </div>
            {createdFormatted && (
              <div className="flex items-baseline justify-between gap-4 py-2.5">
                <dt className="text-muted-foreground shrink-0">Created</dt>
                <dd className="text-right">{createdFormatted}</dd>
              </div>
            )}
          </dl>
        </CardContent>
      </Card>
      */}

      {/* Instance Locations */}
      <div className="flex flex-col gap-4">
        <Card className="rounded-xl shadow-none">
          <CardContent className="flex flex-col gap-5">
            <div className="flex items-center gap-2.5">
              <MapPinIcon className="size-5" />
              <span className="text-base font-semibold">Instance Locations</span>
            </div>
            <p className="text-muted-foreground text-sm">
              {regions.length > 0
                ? `Regions where this workload is deployed: ${regions.join(', ')}.`
                : 'Regions where this workload is deployed.'}
            </p>
            <WorldMap className="bg-background aspect-2/1 w-full overflow-hidden rounded-lg border" />
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
      </div>
    </div>
  );
}
