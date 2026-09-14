/**
 * `portal.page/project` extension at `""` (the plugin mount's index),
 * exposed as `WorkloadList` — every Workload in this project, linking into
 * the existing `WorkloadDetail` support view (`:workloadName`, a sibling
 * route under the same mount).
 *
 * `projectName` comes from `useParams()` the same way `WorkloadDetail` gets
 * it — see `../lib/api.ts`'s header comment.
 */
import { StatusBadge } from '../components/detail-list';
import { formatKpiValue } from '../components/metric-area-chart';
import { StatStrip } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstances, usePublishedUrls, useWorkloads } from '../lib/api';
import { formatLocationNames, useLocationIndex } from '../lib/locations';
import {
  albRpsQuery,
  albRpsQueryMany,
  useInstanceMetricIdentity,
  workloadCpuAvgQuery,
} from '../lib/metrics-queries';
import { usePrometheusCard } from '../lib/prometheus';
import { healthToBadgeType, type Workload } from '../schema';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@datum-cloud/datum-ui/table';
import { formatDistanceToNowStrict } from 'date-fns';
import { useMemo } from 'react';
import { Link, useParams } from 'react-router';

function WorkloadRow({
  workload,
  projectName,
  instanceNames,
  proxyId,
  identityLabel,
  locationLabel,
}: {
  workload: Workload;
  projectName?: string;
  instanceNames: string[];
  proxyId?: string;
  identityLabel?: ReturnType<typeof useInstanceMetricIdentity>['identity'];
  locationLabel: string;
}) {
  const enabled = !!projectName && !!identityLabel && instanceNames.length > 0;
  const cpuQuery =
    enabled && identityLabel && projectName
      ? workloadCpuAvgQuery(projectName, identityLabel.label, instanceNames)
      : undefined;
  const rpsQuery = projectName && proxyId ? albRpsQuery(projectName, proxyId) : undefined;
  const cpu = usePrometheusCard(cpuQuery, 'number', { enabled });
  const rps = usePrometheusCard(rpsQuery, 'requestsPerSecond', { enabled: !!proxyId });

  return (
    <TableRow>
      <TableCell>
        <Link to={workload.name} className="hover:underline">
          <span className="font-mono text-sm">{workload.name}</span>
        </Link>
      </TableCell>
      <TableCell>
        <StatusBadge type={healthToBadgeType(workload.health)}>{workload.health}</StatusBadge>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {workload.readyReplicas}/{workload.desiredReplicas}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {proxyId
          ? (rps.data?.formattedValue ?? formatKpiValue(rps.data?.value, 'requestsPerSecond'))
          : '—'}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {enabled
          ? (cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, 'number'))
          : '—'}
      </TableCell>
      <TableCell className="text-muted-foreground" title={workload.locations.join(', ') || undefined}>
        {locationLabel}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {formatDistanceToNowStrict(workload.createdAt, { addSuffix: true })}
      </TableCell>
    </TableRow>
  );
}

export default function WorkloadList() {
  const { projectName } = useParams<{ projectName: string }>();
  const { data: workloads, isLoading, error, refetch } = useWorkloads(projectName);
  const { data: instances = [] } = useInstances(projectName);
  const { data: publishedByWorkload = {} } = usePublishedUrls(projectName);
  const { identity } = useInstanceMetricIdentity(projectName, instances[0]?.name);
  const locationIndex = useLocationIndex(projectName);
  const namesByWorkload = useMemo(() => {
    const map = new Map<string, string[]>();
    for (const instance of instances) {
      const key = instance.workloadName;
      if (!key) continue;
      const names = map.get(key) ?? [];
      names.push(instance.name);
      map.set(key, names);
    }
    return map;
  }, [instances]);
  const fleetProxyIds = useMemo(
    () => Object.values(publishedByWorkload).map((published) => published.proxyName),
    [publishedByWorkload]
  );
  const fleetRpsQuery =
    projectName && fleetProxyIds.length > 0
      ? albRpsQueryMany(projectName, fleetProxyIds)
      : undefined;
  const fleetRps = usePrometheusCard(fleetRpsQuery, 'requestsPerSecond', {
    enabled: fleetProxyIds.length > 0,
  });
  const readyInstances = (workloads ?? []).reduce((sum, w) => sum + w.readyReplicas, 0);
  const desiredInstances = (workloads ?? []).reduce((sum, w) => sum + w.desiredReplicas, 0);

  return (
    <div className="flex min-w-0 flex-col gap-6 p-4 sm:p-6" data-testid="provider-plugin-workload-list">
      <PageTitle title="Workloads" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view workloads in this project."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && workloads && workloads.length === 0 && (
        <EmptyContent title="there are no workloads in this project" size="sm" variant="dashed" />
      )}

      {!isLoading && !error && workloads && workloads.length > 0 && (
        <>
          <StatStrip
            stats={[
              { label: 'Workloads', value: String(workloads.length) },
              {
                label: 'Instances',
                value:
                  desiredInstances > 0
                    ? `${readyInstances} / ${desiredInstances}`
                    : String(readyInstances),
              },
              {
                label: 'Requests',
                value:
                  fleetProxyIds.length > 0
                    ? (fleetRps.data?.formattedValue ??
                      formatKpiValue(fleetRps.data?.value, 'requestsPerSecond'))
                    : '—',
              },
            ]}
            testId="provider-plugin-workload-list-stats"
          />
          <div className="border-border overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Health</TableHead>
                  <TableHead>Ready</TableHead>
                  <TableHead>Requests</TableHead>
                  <TableHead>Avg CPU</TableHead>
                  <TableHead>Locations</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {workloads.map((workload) => (
                  <WorkloadRow
                    key={workload.uid || workload.name}
                    workload={workload}
                    projectName={projectName}
                    instanceNames={namesByWorkload.get(workload.name) ?? []}
                    proxyId={publishedByWorkload[workload.name]?.proxyName}
                    identityLabel={identity}
                    locationLabel={formatLocationNames(workload.locations, locationIndex)}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </div>
  );
}
