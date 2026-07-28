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
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useWorkload, useWorkloadInstances } from '../lib/api';
import { formatUptime, splitSlashValue } from '../lib/format';
import { workloadHealthToBadgeType, type Instance } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { cn } from '@datum-cloud/datum-ui/utils';
import { CopyIcon, HeartPulseIcon, MapPinIcon, ServerIcon } from 'lucide-react';
import { useLocation, useNavigate, useParams } from 'react-router';

function StatCard({
  icon,
  label,
  value,
  sub,
  highlight,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  sub?: string;
  highlight?: boolean;
}) {
  return (
    <Card className="rounded-xl py-0 shadow-none">
      <div className="flex items-start gap-3 p-4">
        <div
          className={cn('mt-0.5 shrink-0', highlight ? 'text-success' : 'text-muted-foreground')}>
          {icon}
        </div>
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            {label}
          </span>
          <span className={cn('font-semibold', highlight ? 'text-success' : 'text-foreground')}>
            {value}
          </span>
          {sub && <span className="text-muted-foreground text-xs">{sub}</span>}
        </div>
      </div>
    </Card>
  );
}

function InstanceStatusDot({ status }: { status: Instance['status'] }) {
  const color =
    status === 'Available'
      ? 'bg-green-500'
      : status === 'Failed'
        ? 'bg-red-500'
        : 'bg-muted-foreground';
  return (
    <span className="flex items-center gap-1.5">
      <span className={cn('size-2 rounded-full', color)} />
      {status}
    </span>
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
  const { main: resourceShort, sub: resourceProvider } = splitSlashValue(workload.resources ?? '');
  const replicas =
    workload.replicasPerRegion !== undefined
      ? {
          value: `${workload.desiredReplicas} total`,
          sub: `${workload.replicasPerRegion} per region`,
        }
      : { value: `${workload.desiredReplicas} total`, sub: undefined };

  const createdFormatted = workload.createdAt
    ? workload.createdAt.toLocaleDateString('en-US', {
        month: 'long',
        day: 'numeric',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      })
    : null;

  return (
    <div data-testid="compute-plugin-workload-detail" className="flex flex-col gap-6 p-6">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{workload.name}</h1>
          <p className="text-muted-foreground mt-1 text-sm">Workload overview</p>
        </div>
        <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
          {workload.health}
        </Badge>
      </div>

      {/* Stat tiles */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard
          icon={<HeartPulseIcon className="size-4" />}
          label="Health"
          value={`${healthyCount} / ${totalCount}`}
          sub="instances healthy"
          highlight={allHealthy}
        />
        <StatCard
          icon={<MapPinIcon className="size-4" />}
          label="Region"
          value={regions.length > 0 ? regions.join(', ') : '—'}
        />
        {workload.resources && (
          <StatCard
            icon={<ServerIcon className="size-4" />}
            label="Resources"
            value={resourceShort}
            sub={resourceProvider || undefined}
          />
        )}
        <StatCard
          icon={<CopyIcon className="size-4" />}
          label="Replicas"
          value={replicas.value}
          sub={replicas.sub}
        />
      </div>

      {/* Two-column layout */}
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        {/* Configuration */}
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
                  <dd className="min-w-0 truncate text-right font-mono text-xs">
                    {workload.image}
                  </dd>
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

        {/* Running Instances */}
        <Card className="rounded-xl shadow-none">
          <CardHeader className="flex flex-row items-center justify-between px-5 pb-3">
            <CardTitle className="text-base font-semibold">Running Instances</CardTitle>
            {instances.length > 0 && (
              <span className="bg-muted text-muted-foreground rounded-md px-2 py-0.5 text-xs font-medium">
                {instances.length}
              </span>
            )}
          </CardHeader>
          <CardContent className="p-0">
            {instances.length === 0 ? (
              <p className="text-muted-foreground px-5 pb-5 text-sm">No running instances</p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-border border-t">
                    {['Instance ID', 'Region', 'Status', 'Uptime'].map((h) => (
                      <th
                        key={h}
                        className="text-muted-foreground px-5 py-2.5 text-left text-xs font-medium tracking-wide uppercase">
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {instances.map((instance) => (
                    <tr
                      key={instance.uid || instance.name}
                      className="border-border hover:bg-muted/50 cursor-pointer border-t transition-colors"
                      onClick={() => navigate(instanceHref(instance.name))}>
                      <td className="px-5 py-3 font-mono text-xs">
                        <span className="text-primary">{instance.name}</span>
                      </td>
                      <td className="px-5 py-3">
                        {instance.city ?? <span className="text-muted-foreground">—</span>}
                      </td>
                      <td className="px-5 py-3">
                        <InstanceStatusDot status={instance.status} />
                      </td>
                      <td className="px-5 py-3">
                        {instance.createdAt ? formatUptime(instance.createdAt) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
