/**
 * `portal.page/project` extension at `:workloadName`, exposed as
 * `WorkloadDetail` — the staff-portal support view for a single Workload.
 *
 * Built for a staff member fielding "why isn't my workload starting" /
 * "what's wrong with this workload" from a customer: Overview surfaces raw
 * conditions (not just a coarse health enum), Instances is the direct
 * per-instance drill-down (image pull failures, crash loops, scheduling
 * gates, quota), Logs merges ALB access logs with instance stdout, Metrics
 * charts CPU/memory/network (and ALB traffic when published), and YAML
 * gives an escape hatch to the raw resource for anything the tabs don't
 * surface.
 *
 * `projectName` comes from `useParams()` resolving the ancestor route param
 * from staff-portal's project-scoped plugin mount
 * (`/customers/projects/:projectName/plugins/workloads/:workloadName`) —
 * see `../lib/api.ts`'s header comment for why this works with no extra
 * prop/context plumbing.
 */
import type { RawWorkload } from '../adapter';
import { ConditionsTable } from '../components/conditions-table';
import { DetailList, StatusBadge } from '../components/detail-list';
import { formatKpiValue } from '../components/metric-area-chart';
import { StatStrip, type Stat } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { WorkloadLogsExplorer } from '../components/workload-logs';
import { WorkloadMetrics } from '../components/workload-metrics';
import { usePublishedUrl, useWorkload, useWorkloadInstances, useWorkloadRaw } from '../lib/api';
import {
  formatLocationName,
  formatLocationNames,
  formatLocationSelector,
  useLocationIndex,
  type LocationIndex,
} from '../lib/locations';
import {
  albRpsQuery,
  useInstanceMetricIdentity,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from '../lib/metrics-queries';
import { usePrometheusCard } from '../lib/prometheus';
import { healthToBadgeType, type Instance, type Workload, type WorkloadPlacement } from '../schema';
import { Card, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { CodeEditor } from '@datum-cloud/datum-ui/code-editor';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@datum-cloud/datum-ui/tabs';
import { formatDistanceToNowStrict } from 'date-fns';
import { dump } from 'js-yaml';
import { BoxIcon, MapPinIcon, Settings2Icon } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useParams } from 'react-router';

const TABS = ['Overview', 'Instances', 'Events', 'Logs', 'Metrics', 'YAML'] as const;
type Tab = (typeof TABS)[number];

function GeneralCard({ workload }: { workload: Workload }) {
  return (
    <Card size="sm" sectioned className="h-full w-full overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <BoxIcon className="text-secondary size-4 stroke-2" />
          General
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            {
              label: 'Status',
              content: (
                <StatusBadge type={healthToBadgeType(workload.health)}>
                  {workload.health}
                </StatusBadge>
              ),
            },
            {
              label: 'Resource Name',
              content: <span className="font-mono text-sm">{workload.name}</span>,
            },
            {
              label: 'Ready',
              content: `${workload.readyReplicas}/${workload.desiredReplicas}`,
            },
            {
              label: 'Updated',
              content: `${workload.updatedReplicas}/${workload.desiredReplicas}`,
            },
            {
              label: 'Created',
              content: formatDistanceToNowStrict(workload.createdAt, { addSuffix: true }),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function placementLocationLabel(placement: WorkloadPlacement, index: LocationIndex): string {
  if (placement.locations.length > 0) return formatLocationNames(placement.locations, index);
  if (placement.locationSelector) {
    return formatLocationSelector(placement.locationSelector, index) ?? placement.locationSelector;
  }
  return 'no locations';
}

function ConfigurationCard({
  workload,
  locationLabel,
}: {
  workload: Workload;
  locationLabel: string;
}) {
  return (
    <Card size="sm" sectioned className="h-full w-full overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Settings2Icon className="text-secondary size-4 stroke-2" />
          Configuration
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            { label: 'Runtime', content: workload.runtimeType ?? '—' },
            {
              label: 'Image',
              content: workload.image ? (
                <span className="block max-w-full truncate font-mono text-xs" title={workload.image}>
                  {workload.image}
                </span>
              ) : (
                '—'
              ),
            },
            { label: 'Resources', content: workload.resources ?? '—' },
            {
              label: 'Replicas',
              content:
                workload.replicasPerRegion !== undefined
                  ? `${workload.replicasPerRegion}/location · ${workload.desiredReplicas} total`
                  : `${workload.desiredReplicas} total`,
            },
            {
              label: 'Locations',
              content: (
                <span title={workload.locations.join(', ') || undefined}>{locationLabel}</span>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function PlacementsCard({
  workload,
  locationIndex,
}: {
  workload: Workload;
  locationIndex: LocationIndex;
}) {
  if (workload.placements.length === 0) return null;

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <MapPinIcon className="text-secondary size-4 stroke-2" />
          Placements
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {workload.placements.map((p) => (
          <div key={p.name} className="border-border rounded-lg border p-3">
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm font-medium">{p.name}</span>
                <span
                  className="text-muted-foreground text-xs"
                  title={p.locations.join(', ') || p.locationSelector}>
                  {placementLocationLabel(p, locationIndex)}
                </span>
              </div>
              <div className="flex items-center gap-3">
                <span className="text-muted-foreground text-xs">
                  {p.readyReplicas}/{p.desiredReplicas} ready
                </span>
                <StatusBadge type={healthToBadgeType(p.health)}>{p.health}</StatusBadge>
              </div>
            </div>
            {p.conditions.length > 0 && <ConditionsTable conditions={p.conditions} />}
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function ConditionsCard({ workload }: { workload: Workload }) {
  return (
    <Card size="sm" sectioned className="w-full overflow-hidden">
      <CardHeader size="sm" bordered>
        <CardTitle className="text-sm">Conditions</CardTitle>
      </CardHeader>
      <CardContent>
        <ConditionsTable conditions={workload.conditions} />
      </CardContent>
    </Card>
  );
}

function OverviewTab({
  workload,
  requests,
  avgCpu,
  avgMemory,
  locationIndex,
}: {
  workload: Workload;
  requests: string;
  avgCpu: string;
  avgMemory: string;
  locationIndex: LocationIndex;
}) {
  const stats: Stat[] = [
    { label: 'Ready', value: `${workload.readyReplicas}/${workload.desiredReplicas}` },
    { label: 'Current', value: `${workload.currentReplicas}/${workload.desiredReplicas}` },
    { label: 'Updated', value: `${workload.updatedReplicas}/${workload.desiredReplicas}` },
    { label: 'Locations', value: String(workload.locations.length) },
    { label: 'Requests', value: requests },
    { label: 'Avg CPU', value: avgCpu },
    { label: 'Avg Memory', value: avgMemory },
  ];

  return (
    <div className="flex flex-col gap-6">
      <StatStrip stats={stats} />
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <GeneralCard workload={workload} />
        <ConfigurationCard
          workload={workload}
          locationLabel={formatLocationNames(workload.locations, locationIndex)}
        />
      </div>
      <PlacementsCard workload={workload} locationIndex={locationIndex} />
      <ConditionsCard workload={workload} />
    </div>
  );
}

function InstanceRow({
  instance,
  locationLabel,
}: {
  instance: Instance;
  locationLabel: string;
}) {
  const [expanded, setExpanded] = useState(false);

  return (
    <>
      <TableRow className="cursor-pointer" onClick={() => setExpanded((v) => !v)}>
        <TableCell>
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="truncate font-mono text-sm">{instance.name}</span>
            {instance.suspended && <StatusBadge type="muted">Suspended</StatusBadge>}
            {instance.schedulingGates.length > 0 && (
              <StatusBadge type="warning">
                Gated: {instance.schedulingGates.join(', ')}
              </StatusBadge>
            )}
          </div>
        </TableCell>
        <TableCell className="text-muted-foreground" title={instance.location}>
          {locationLabel}
        </TableCell>
        <TableCell className="text-muted-foreground font-mono">
          {instance.internalIP ?? '—'}
        </TableCell>
        <TableCell
          className="text-muted-foreground max-w-40 truncate font-mono"
          title={instance.externalIP}>
          {instance.externalIP ?? '—'}
        </TableCell>
        <TableCell
          className="text-muted-foreground max-w-64 truncate"
          title={instance.statusMessage ? (instance.statusReason ?? instance.status) : undefined}>
          {instance.statusMessage ?? '—'}
        </TableCell>
        <TableCell className="text-muted-foreground">
          {formatDistanceToNowStrict(instance.createdAt, { addSuffix: true })}
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow>
          <TableCell colSpan={6} className="bg-muted/20 whitespace-normal">
            <ConditionsTable conditions={instance.conditions} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function InstancesTab({
  instances,
  locationIndex,
}: {
  instances: Instance[];
  locationIndex: LocationIndex;
}) {
  if (instances.length === 0) {
    return (
      <EmptyContent title="there are no instances for this workload" size="sm" variant="dashed" />
    );
  }

  return (
    <div className="border-border overflow-hidden rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Instance</TableHead>
            <TableHead>Location</TableHead>
            <TableHead>Internal IP</TableHead>
            <TableHead>External IP</TableHead>
            <TableHead>Message</TableHead>
            <TableHead>Created</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {instances.map((instance) => (
            <InstanceRow
              key={instance.uid || instance.name}
              instance={instance}
              locationLabel={formatLocationName(instance.location, locationIndex)}
            />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

/** Strips the noisy, rarely-useful `metadata.managedFields` before display. */
function withoutManagedFields(raw: unknown): unknown {
  if (!raw || typeof raw !== 'object') return raw;
  const { metadata, ...rest } = raw as { metadata?: Record<string, unknown> };
  if (!metadata) return raw;
  const { managedFields, ...restMetadata } = metadata;
  return { ...rest, metadata: restMetadata };
}

/** Same `dump` convention as staff-portal's own `edge-yaml-card.tsx`. */
function YamlTab({ raw }: { raw: RawWorkload | undefined }) {
  const yaml = useMemo(
    () => (raw ? dump(withoutManagedFields(raw), { indent: 2, lineWidth: -1, noRefs: true }) : ''),
    [raw]
  );

  if (!raw) return <LoadingSkeleton />;
  return <CodeEditor value={yaml} language="yaml" readOnly minHeight="500px" />;
}

export default function WorkloadDetail() {
  const { projectName, workloadName } = useParams<{
    projectName: string;
    workloadName: string;
  }>();
  const [tab, setTab] = useState<Tab>('Overview');

  const { data: workload, isLoading, error, refetch } = useWorkload(projectName, workloadName);
  const { data: instances = [] } = useWorkloadInstances(projectName, workloadName);
  const { data: raw } = useWorkloadRaw(projectName, workloadName);
  const published = usePublishedUrl(projectName, workloadName);
  const instanceNames = useMemo(() => instances.map((instance) => instance.name), [instances]);
  const { identity, isLoading: identityLoading } = useInstanceMetricIdentity(
    projectName,
    instanceNames[0]
  );
  const chartsEnabled =
    !identityLoading && !!identity && !!projectName && instanceNames.length > 0;
  const cpuQuery =
    chartsEnabled && identity && projectName
      ? workloadCpuAvgQuery(projectName, identity.label, instanceNames)
      : undefined;
  const memoryQuery =
    chartsEnabled && identity && projectName
      ? workloadMemoryAvgQuery(projectName, identity.label, instanceNames)
      : undefined;
  const proxyId = published.data?.proxyName;
  const rpsQuery = projectName && proxyId ? albRpsQuery(projectName, proxyId) : undefined;
  const cpu = usePrometheusCard(cpuQuery, 'number', { enabled: chartsEnabled });
  const memory = usePrometheusCard(memoryQuery, 'bytes', { enabled: chartsEnabled });
  const rps = usePrometheusCard(rpsQuery, 'requestsPerSecond', { enabled: !!proxyId });
  const requestsValue = proxyId
    ? (rps.data?.formattedValue ?? formatKpiValue(rps.data?.value, 'requestsPerSecond'))
    : '—';
  const avgCpu = chartsEnabled
    ? (cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, 'number'))
    : '—';
  const avgMemory = chartsEnabled
    ? (memory.data?.formattedValue ?? formatKpiValue(memory.data?.value, 'bytes'))
    : '—';

  const titleName = workload?.name ?? workloadName ?? 'Workload';
  const locationIndex = useLocationIndex(projectName);

  return (
    <div
      className="flex min-w-0 flex-col gap-6 p-4 sm:p-6"
      data-testid="provider-plugin-workload-detail">
      <PageTitle title={titleName} titleClassName="break-all sm:break-normal" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && (error || !workload) && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this workload."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && workload && (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="gap-6">
          <div className="border-border -mx-4 border-b px-4 sm:-mx-6 sm:px-6">
            <TabsList variant="line">
              {TABS.map((t) => (
                <TabsTrigger key={t} value={t} className="text-xs md:py-2">
                  {t}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>

          <TabsContent value="Overview">
            <OverviewTab
              workload={workload}
              requests={requestsValue}
              avgCpu={avgCpu}
              avgMemory={avgMemory}
              locationIndex={locationIndex}
            />
          </TabsContent>
          <TabsContent value="Instances">
            <InstancesTab instances={instances} locationIndex={locationIndex} />
          </TabsContent>
          <TabsContent value="Events">
            <EmptyContent
              title="event history isn't available yet"
              subtitle="Workload and instance Kubernetes events aren't wired up in this view yet."
              size="sm"
              variant="dashed"
            />
          </TabsContent>
          <TabsContent value="Logs">
            <WorkloadLogsExplorer
              projectName={projectName}
              proxyId={proxyId}
              instanceNames={instanceNames}
            />
          </TabsContent>
          <TabsContent value="Metrics">
            <WorkloadMetrics
              projectName={projectName}
              instanceNames={instanceNames}
              identityLabel={identity?.label}
              identityLoading={identityLoading}
              proxyId={proxyId}
            />
          </TabsContent>
          <TabsContent value="YAML">
            <YamlTab raw={raw} />
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}
