/**
 * `portal.page/project` extension at `:workloadName`, exposed as
 * `WorkloadDetail` — the staff-portal support view for a single Workload.
 *
 * Built for a staff member fielding "why isn't my workload starting" /
 * "what's wrong with this workload" from a customer: Overview leads with
 * anything that needs attention (non-True conditions, failing or gated
 * instances), then live metrics and instances in the main column, with the
 * static spec and endpoint in a side column. Instances is the full
 * per-instance drill-down (image pull failures, crash loops, scheduling
 * gates, quota), Logs merges ALB access logs with instance stdout, Metrics
 * charts CPU/memory/network (and ALB traffic when published), and YAML
 * gives an escape hatch to the raw resource for anything the tabs don't
 * surface.
 *
 * `projectName` comes from `useParams()` resolving the ancestor route param
 * from staff-portal's project-scoped plugin mount
 * (`/customers/projects/:projectName/plugins/:slug/:workloadName`) —
 * see `../lib/api.ts`'s header comment for why this works with no extra
 * prop/context plumbing.
 */
import type { RawWorkload } from '../adapter';
import { ConditionsTable } from '../components/conditions-table';
import { CopyableMono } from '../components/copyable-mono';
import { DetailList, StatusBadge } from '../components/detail-list';
import { CpuMemorySparks } from '../components/metric-sparkline';
import { SparklineStatCard } from '../components/sparkline-stat-card';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { WorkloadLogsExplorer } from '../components/workload-logs';
import { WorkloadMetrics } from '../components/workload-metrics';
import {
  usePublishedUrl,
  useWorkload,
  useWorkloadInstances,
  useWorkloadRaw,
  type PublishedUrl,
} from '../lib/api';
import {
  formatLocationName,
  formatLocationNames,
  formatLocationSelector,
  useLocationIndex,
  type LocationIndex,
} from '../lib/locations';
import {
  ALB_INSTANT_WINDOW,
  albP99Query,
  albRpsQuery,
  cpuUsageQuery,
  identityValues,
  memoryUsageQuery,
  useProjectResourceIdentity,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
  type InstanceIdentityLabel,
} from '../lib/metrics-queries';
import { type PrometheusTimeRange } from '../lib/prometheus';
import { useRollingLastHour } from '../lib/use-rolling-range';
import {
  healthToBadgeType,
  type Condition,
  type Instance,
  type InstanceStatusValue,
  type Workload,
  type WorkloadPlacement,
} from '../schema';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { CodeEditor } from '@datum-cloud/datum-ui/code-editor';
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@datum-cloud/datum-ui/tabs';
import { formatDistanceToNowStrict } from 'date-fns';
import { dump } from 'js-yaml';
import {
  BoxIcon,
  CheckIcon,
  GlobeIcon,
  ListChecksIcon,
  ServerIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useParams } from 'react-router';

const TABS = ['Overview', 'Instances', 'Events', 'Logs', 'Metrics', 'YAML'] as const;
type Tab = (typeof TABS)[number];

/** Instances shown inline on Overview before deferring to the Instances tab. */
const OVERVIEW_INSTANCE_LIMIT = 8;

function instanceBadgeType(
  status: InstanceStatusValue
): 'success' | 'warning' | 'danger' | 'muted' {
  switch (status) {
    case 'Available':
      return 'success';
    case 'Pending':
      return 'warning';
    case 'Failed':
      return 'danger';
    default:
      return 'muted';
  }
}

/** Relative to the workload detail route (`:workloadName`). */
function instancePath(instanceName: string): string {
  return `instances/${encodeURIComponent(instanceName)}`;
}

function placementLocationLabel(placement: WorkloadPlacement, index: LocationIndex): string {
  if (placement.locations.length > 0) return formatLocationNames(placement.locations, index);
  if (placement.locationSelector) {
    return formatLocationSelector(placement.locationSelector, index) ?? placement.locationSelector;
  }
  return 'no locations';
}

function unhealthyConditions(conditions: Condition[]): Condition[] {
  return conditions.filter((c) => c.status !== 'True');
}

// ── Header ───────────────────────────────────────────────────────────────

function WorkloadSummary({
  workload,
  locationLabel,
}: {
  workload: Workload;
  locationLabel: string;
}) {
  return (
    <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
      <StatusBadge type={healthToBadgeType(workload.health)}>{workload.health}</StatusBadge>
      <span>
        {workload.readyReplicas}/{workload.desiredReplicas} ready
      </span>
      <span aria-hidden>·</span>
      <span title={workload.locations.join(', ') || undefined}>{locationLabel}</span>
      <span aria-hidden>·</span>
      <span>created {formatDistanceToNowStrict(workload.createdAt, { addSuffix: true })}</span>
    </div>
  );
}

// ── Attention ────────────────────────────────────────────────────────────

interface AttentionItem {
  key: string;
  scope: string;
  title: string;
  detail?: string;
}

/** A Pending instance younger than this is a normal rollout, not a problem. */
const PENDING_GRACE_MS = 5 * 60_000;

function attentionItems(
  workload: Workload,
  instances: Instance[],
  now = Date.now()
): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const c of unhealthyConditions(workload.conditions)) {
    items.push({
      key: `workload-${c.type}`,
      scope: 'Workload',
      title: `${c.type}: ${c.reason ?? c.status}`,
      detail: c.message,
    });
  }
  for (const p of workload.placements) {
    for (const c of unhealthyConditions(p.conditions)) {
      items.push({
        key: `placement-${p.name}-${c.type}`,
        scope: `Placement ${p.name}`,
        title: `${c.type}: ${c.reason ?? c.status}`,
        detail: c.message,
      });
    }
  }
  for (const i of instances) {
    if (i.schedulingGates.length > 0) {
      items.push({
        key: `instance-${i.name}-gated`,
        scope: `Instance ${i.name}`,
        title: `Gated: ${i.schedulingGates.join(', ')}`,
      });
    } else if (
      !i.suspended &&
      (i.status === 'Failed' ||
        i.status === 'Unknown' ||
        (i.status === 'Pending' && now - i.createdAt.getTime() > PENDING_GRACE_MS))
    ) {
      items.push({
        key: `instance-${i.name}`,
        scope: `Instance ${i.name}`,
        title: `${i.status}${i.statusReason ? `: ${i.statusReason}` : ''}`,
        detail: i.statusMessage,
      });
    }
  }
  return items;
}

function AttentionCard({ items }: { items: AttentionItem[] }) {
  if (items.length === 0) return null;
  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="workload-attention">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <TriangleAlertIcon className="text-destructive size-4 stroke-2" />
          Needs attention
          <span className="text-muted-foreground font-normal">({items.length})</span>
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <ul className="divide-border divide-y">
          {items.map((item) => (
            <li key={item.key} className="flex flex-col gap-0.5 px-4 py-3">
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="text-muted-foreground font-mono text-xs">{item.scope}</span>
                <span className="text-sm font-medium">{item.title}</span>
              </div>
              {item.detail ? (
                <p className="text-muted-foreground text-sm whitespace-normal">{item.detail}</p>
              ) : null}
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

// ── Main column ──────────────────────────────────────────────────────────

function MetricCards({
  projectName,
  instanceKeys,
  identityLabel,
  identityLoading,
  identityDenied,
  proxyId,
  publishedState,
  publishedLoading,
  timeRange,
  onOpenMetrics,
}: {
  projectName?: string;
  instanceKeys: readonly string[];
  identityLabel?: InstanceIdentityLabel;
  identityLoading: boolean;
  identityDenied: boolean;
  proxyId?: string;
  publishedState: PublishedUrl | null | undefined;
  publishedLoading: boolean;
  timeRange: PrometheusTimeRange;
  onOpenMetrics: () => void;
}) {
  const scoped = !!projectName && !!identityLabel && instanceKeys.length > 0;
  const cpuQuery = scoped
    ? workloadCpuAvgQuery(projectName, identityLabel, instanceKeys)
    : undefined;
  const memoryQuery = scoped
    ? workloadMemoryAvgQuery(projectName, identityLabel, instanceKeys)
    : undefined;
  const published = !!projectName && !!proxyId;
  // `null` means confirmed unpublished; `undefined` after loading means the lookup failed.
  const notPublished = !publishedLoading && publishedState === null;
  const lookupFailed = !publishedLoading && publishedState === undefined;
  const albUnavailable = notPublished || lookupFailed;
  const albUnavailableLabel = lookupFailed ? 'Unavailable' : 'Not published';

  return (
    <div className="grid grid-cols-2 gap-3 xl:grid-cols-4" data-testid="workload-metric-cards">
      <SparklineStatCard
        title="Avg CPU (cores)"
        query={cpuQuery}
        format="number"
        timeRange={timeRange}
        rangeLabel="Last 1h"
        pending={identityLoading}
        denied={identityDenied}
        idle={!identityLoading && !cpuQuery}
        onSelect={onOpenMetrics}
      />
      <SparklineStatCard
        title="Avg memory"
        query={memoryQuery}
        format="bytes"
        color="var(--color-chart-1)"
        timeRange={timeRange}
        rangeLabel="Last 1h"
        pending={identityLoading}
        denied={identityDenied}
        idle={!identityLoading && !memoryQuery}
        onSelect={onOpenMetrics}
      />
      <SparklineStatCard
        title="Requests/s"
        query={published ? albRpsQuery(projectName, proxyId) : undefined}
        headlineQuery={
          published ? albRpsQuery(projectName, proxyId, ALB_INSTANT_WINDOW) : undefined
        }
        format="requestsPerSecond"
        color="var(--color-chart-2)"
        timeRange={timeRange}
        rangeLabel="Last 1h"
        pending={publishedLoading}
        unavailable={albUnavailable}
        unavailableLabel={albUnavailableLabel}
        onSelect={onOpenMetrics}
      />
      <SparklineStatCard
        title="p99 latency"
        query={published ? albP99Query(projectName, proxyId) : undefined}
        headlineQuery={
          published ? albP99Query(projectName, proxyId, ALB_INSTANT_WINDOW) : undefined
        }
        format="milliseconds-auto"
        color="var(--color-chart-3)"
        timeRange={timeRange}
        rangeLabel="Last 1h"
        pending={publishedLoading}
        unavailable={albUnavailable}
        unavailableLabel={albUnavailableLabel}
        onSelect={onOpenMetrics}
      />
    </div>
  );
}

function InstancesOverviewCard({
  instances,
  projectName,
  identityLabel,
  resourceNames,
  identityLoading,
  identityDenied,
  locationIndex,
  timeRange,
  onViewAll,
}: {
  instances: Instance[];
  projectName?: string;
  identityLabel?: InstanceIdentityLabel;
  resourceNames: readonly string[];
  identityLoading: boolean;
  identityDenied: boolean;
  locationIndex: LocationIndex;
  timeRange: PrometheusTimeRange;
  onViewAll: () => void;
}) {
  const shown = instances.slice(0, OVERVIEW_INSTANCE_LIMIT);

  return (
    <Card
      size="sm"
      sectioned
      className="w-full overflow-hidden"
      data-testid="workload-instances-overview">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <ServerIcon className="text-secondary size-4 stroke-2" />
          Instances
          <span className="text-muted-foreground font-normal">({instances.length})</span>
        </CardTitle>
        {instances.length > shown.length ? (
          <CardAction>
            <Button type="secondary" theme="link" size="link" onClick={onViewAll}>
              View all {instances.length}
            </Button>
          </CardAction>
        ) : null}
      </CardHeader>
      <CardContent padding="none">
        {instances.length === 0 ? (
          <p className="text-muted-foreground px-4 py-3 text-sm">No instances yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Instance</TableHead>
                <TableHead>Location</TableHead>
                <TableHead>CPU / Memory (1h)</TableHead>
                <TableHead>Age</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shown.map((instance) => {
                const identity =
                  projectName && identityLabel && resourceNames.includes(instance.name)
                    ? { label: identityLabel, value: instance.name }
                    : undefined;
                return (
                  <TableRow key={instance.uid || instance.name}>
                    <TableCell className="whitespace-normal">
                      <div className="flex min-w-0 flex-col gap-1">
                        <div className="flex min-w-0 flex-wrap items-center gap-2">
                          <Link
                            to={instancePath(instance.name)}
                            className="text-primary truncate font-mono text-sm hover:underline">
                            {instance.name}
                          </Link>
                          <StatusBadge
                            type={
                              instance.suspended ? 'muted' : instanceBadgeType(instance.status)
                            }>
                            {instance.suspended ? 'Suspended' : instance.status}
                          </StatusBadge>
                        </div>
                        {instance.status !== 'Available' && instance.statusMessage ? (
                          <span className="text-muted-foreground text-xs">
                            {instance.statusMessage}
                          </span>
                        ) : null}
                      </div>
                    </TableCell>
                    <TableCell className="text-muted-foreground" title={instance.location}>
                      {formatLocationName(instance.location, locationIndex)}
                    </TableCell>
                    <TableCell>
                      <CpuMemorySparks
                        cpuQuery={
                          identity && projectName ? cpuUsageQuery(projectName, identity) : undefined
                        }
                        memoryQuery={
                          identity && projectName
                            ? memoryUsageQuery(projectName, identity)
                            : undefined
                        }
                        timeRange={timeRange}
                        compact
                        pending={identityLoading}
                        denied={identityDenied}
                      />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatDistanceToNowStrict(instance.createdAt)}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function ConditionsSection({
  title,
  subtitle,
  badge,
  conditions,
  showHealthy,
}: {
  title: string;
  subtitle?: string;
  badge?: React.ReactNode;
  conditions: Condition[];
  showHealthy: boolean;
}) {
  const unhealthy = unhealthyConditions(conditions);
  const visible = showHealthy ? conditions : unhealthy;
  const hiddenCount = conditions.length - visible.length;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className="font-mono text-sm font-medium">{title}</span>
          {subtitle ? <span className="text-muted-foreground text-xs">{subtitle}</span> : null}
        </div>
        {badge}
      </div>
      {visible.length > 0 ? <ConditionsTable conditions={visible} /> : null}
      {hiddenCount > 0 ? (
        <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
          <CheckIcon className="size-3.5" />
          {hiddenCount} healthy condition{hiddenCount === 1 ? '' : 's'} hidden
        </p>
      ) : null}
      {conditions.length === 0 ? (
        <p className="text-muted-foreground text-xs">No conditions reported.</p>
      ) : null}
    </div>
  );
}

function ConditionsCard({
  workload,
  locationIndex,
}: {
  workload: Workload;
  locationIndex: LocationIndex;
}) {
  const [showHealthy, setShowHealthy] = useState(false);

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="workload-conditions">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <ListChecksIcon className="text-secondary size-4 stroke-2" />
          Conditions
        </CardTitle>
        <CardAction>
          <Button
            type="secondary"
            theme="link"
            size="link"
            onClick={() => setShowHealthy((v) => !v)}>
            {showHealthy ? 'Hide healthy' : 'Show all'}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <ConditionsSection
          title="Workload"
          conditions={workload.conditions}
          showHealthy={showHealthy}
        />
        {workload.placements.map((p) => (
          <ConditionsSection
            key={p.name}
            title={`Placement ${p.name}`}
            subtitle={placementLocationLabel(p, locationIndex)}
            badge={
              <div className="flex items-center gap-3">
                <span className="text-muted-foreground text-xs">
                  {p.readyReplicas}/{p.desiredReplicas} ready
                </span>
                <StatusBadge type={healthToBadgeType(p.health)}>{p.health}</StatusBadge>
              </div>
            }
            conditions={p.conditions}
            showHealthy={showHealthy}
          />
        ))}
      </CardContent>
    </Card>
  );
}

// ── Side column ──────────────────────────────────────────────────────────

function DetailsCard({ workload, locationLabel }: { workload: Workload; locationLabel: string }) {
  const replicas =
    workload.replicasPerRegion !== undefined
      ? `${workload.replicasPerRegion}/location · ${workload.desiredReplicas} total`
      : `${workload.desiredReplicas} total`;

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="workload-details">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <BoxIcon className="text-secondary size-4 stroke-2" />
          Details
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          labelWidth="7rem"
          items={[
            { label: 'Name', content: <CopyableMono value={workload.name} /> },
            { label: 'Runtime', content: workload.runtimeType ?? '—' },
            {
              label: 'Image',
              content: workload.image ? <CopyableMono value={workload.image} /> : '—',
            },
            { label: 'Resources', content: workload.resources ?? '—' },
            { label: 'Replicas', content: replicas },
            {
              label: 'Rollout',
              content: `${workload.readyReplicas} ready · ${workload.currentReplicas} current · ${workload.updatedReplicas} updated`,
            },
            {
              label: 'Locations',
              content: (
                <span title={workload.locations.join(', ') || undefined}>{locationLabel}</span>
              ),
            },
            {
              label: 'Created',
              content: (
                <span title={workload.createdAt.toISOString()}>
                  {formatDistanceToNowStrict(workload.createdAt, {
                    addSuffix: true,
                  })}
                </span>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function EndpointCard({
  published,
  isLoading,
}: {
  published: PublishedUrl | null | undefined;
  isLoading: boolean;
}) {
  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="workload-endpoint">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <GlobeIcon className="text-secondary size-4 stroke-2" />
          Endpoint
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        {isLoading ? (
          <p className="text-muted-foreground px-4 py-3 text-sm">Loading…</p>
        ) : published === undefined ? (
          <p className="text-muted-foreground px-4 py-3 text-sm">
            Couldn't load the published endpoint.
          </p>
        ) : published === null ? (
          <p className="text-muted-foreground px-4 py-3 text-sm">
            Not published through a load balancer.
          </p>
        ) : (
          <DetailList
            labelWidth="7rem"
            items={[
              {
                label: 'URL',
                content: published.hostname ? (
                  <a
                    href={`https://${published.hostname}`}
                    target="_blank"
                    rel="noreferrer"
                    className="text-primary block truncate text-sm hover:underline"
                    title={published.hostname}>
                    {published.hostname}
                  </a>
                ) : (
                  '—'
                ),
              },
              {
                label: 'Proxy',
                content: <CopyableMono value={published.proxyName} />,
              },
            ]}
          />
        )}
      </CardContent>
    </Card>
  );
}

// ── Overview ─────────────────────────────────────────────────────────────

function OverviewTab({
  workload,
  instances,
  projectName,
  identityLabel,
  resourceNames,
  identityLoading,
  identityDenied,
  published,
  publishedLoading,
  locationIndex,
  locationLabel,
  onSelectTab,
}: {
  workload: Workload;
  instances: Instance[];
  projectName?: string;
  identityLabel?: InstanceIdentityLabel;
  resourceNames: readonly string[];
  identityLoading: boolean;
  identityDenied: boolean;
  published: PublishedUrl | null | undefined;
  publishedLoading: boolean;
  locationIndex: LocationIndex;
  locationLabel: string;
  onSelectTab: (tab: Tab) => void;
}) {
  const timeRange = useRollingLastHour();
  const instanceKeys = useMemo(() => identityValues(instances), [instances]);
  const attention = useMemo(() => attentionItems(workload, instances), [workload, instances]);

  return (
    <div className="flex flex-col gap-6">
      <AttentionCard items={attention} />
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="flex min-w-0 flex-col gap-6 lg:col-span-2">
          <MetricCards
            projectName={projectName}
            instanceKeys={instanceKeys}
            identityLabel={identityLabel}
            identityLoading={identityLoading}
            identityDenied={identityDenied}
            proxyId={published?.proxyName}
            publishedState={published}
            publishedLoading={publishedLoading}
            timeRange={timeRange}
            onOpenMetrics={() => onSelectTab('Metrics')}
          />
          <InstancesOverviewCard
            instances={instances}
            projectName={projectName}
            identityLabel={identityLabel}
            resourceNames={resourceNames}
            identityLoading={identityLoading}
            identityDenied={identityDenied}
            locationIndex={locationIndex}
            timeRange={timeRange}
            onViewAll={() => onSelectTab('Instances')}
          />
          <ConditionsCard workload={workload} locationIndex={locationIndex} />
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <DetailsCard workload={workload} locationLabel={locationLabel} />
          <EndpointCard published={published} isLoading={publishedLoading} />
        </div>
      </div>
    </div>
  );
}

// ── Instances tab ────────────────────────────────────────────────────────

function InstanceRow({ instance, locationLabel }: { instance: Instance; locationLabel: string }) {
  const [expanded, setExpanded] = useState(false);

  return (
    <>
      <TableRow className="cursor-pointer" onClick={() => setExpanded((v) => !v)}>
        <TableCell>
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Link
              to={instancePath(instance.name)}
              onClick={(e) => e.stopPropagation()}
              className="text-primary truncate font-mono text-sm hover:underline">
              {instance.name}
            </Link>
            {instance.suspended && <StatusBadge type="muted">Suspended</StatusBadge>}
            {instance.schedulingGates.length > 0 && (
              <StatusBadge type="warning">Gated: {instance.schedulingGates.join(', ')}</StatusBadge>
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
    () =>
      raw
        ? dump(withoutManagedFields(raw), {
            indent: 2,
            lineWidth: -1,
            noRefs: true,
          })
        : '',
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
  const {
    identityLabel,
    resourceNames,
    isLoading: identityLoading,
    isDenied: identityDenied,
  } = useProjectResourceIdentity(projectName);
  const metricKeys = useMemo(() => identityValues(instances), [instances]);
  const proxyId = published.data?.proxyName;

  const titleName = workload?.name ?? workloadName ?? 'Workload';
  const locationIndex = useLocationIndex(projectName);
  const locationLabel = workload ? formatLocationNames(workload.locations, locationIndex) : '';

  return (
    <div
      className="flex min-w-0 flex-col gap-6 p-4 sm:p-6"
      // Size from the host's column, not from content: without inline-size
      // containment a long instance name or table row widens the whole
      // host page instead of scrolling inside its own card.
      style={{ containerType: 'inline-size' }}
      data-testid="provider-plugin-workload-detail">
      <PageTitle
        title={titleName}
        titleClassName="break-all sm:break-normal"
        description={
          workload ? (
            <WorkloadSummary workload={workload} locationLabel={locationLabel} />
          ) : undefined
        }
        descriptionClassName="max-w-none"
      />

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
              instances={instances}
              projectName={projectName}
              identityLabel={identityLabel}
              resourceNames={resourceNames}
              identityLoading={identityLoading}
              identityDenied={identityDenied}
              published={published.data}
              publishedLoading={published.isLoading}
              locationIndex={locationIndex}
              locationLabel={locationLabel}
              onSelectTab={setTab}
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
              instanceKeys={metricKeys}
              identityLabel={identityLabel}
              identityLoading={identityLoading}
              identityDenied={identityDenied}
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
