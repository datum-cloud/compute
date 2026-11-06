/**
 * The workload Overview's Instances card: a table with one row per instance
 * saying whether it is serving and why not, where it runs, its last hour of
 * network traffic, how to reach it on the private network, and links to its Logs, Shell
 * and Metrics. The workload-name prefix every instance shares is muted so the
 * part that tells them apart stands out.
 */
import { formatLocationName, formatLocationTooltip, type LocationIndex } from '../lib/locations';
import type { ApiError } from '../lib/api';
import { seriesByWorkload } from '../lib/metrics-queries';
import { useWorkloadNetworkMetrics } from '../lib/network-interface-metrics';
import { usePrometheusChart } from '../lib/prometheus';
import {
  instanceNameParts,
  instanceReadiness,
  instancesSectionState,
  STATUS_TONE_COLOR,
  type StatusTone,
} from '../lib/workload-presenters';
import type { Instance } from '../schema';
import { StatusBadge } from './detail-list';
import { MetricSparkline } from './metric-sparkline';
import { useOverviewRange } from './overview-range';
import { InstanceRowsSkeleton } from './skeletons';
import { QuietNotice } from './surface';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { Card, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { ChartLineIcon, LogsIcon, ServerIcon, SquareTerminalIcon } from 'lucide-react';
import { useMemo } from 'react';
import { Link } from 'react-router';

/** The address a person would dial first: the in-network one, else the external. */
function primaryAddress(instance: Instance): string | undefined {
  return instance.addresses[0] ?? instance.internalIP ?? instance.externalIPs[0] ?? instance.externalIP;
}

/** Badge colours per readiness tone; anything still coming up reads as info. */
const BADGE_TYPE: Record<StatusTone, 'success' | 'warning' | 'danger' | 'info'> = {
  ok: 'success',
  warn: 'warning',
  error: 'danger',
  muted: 'info',
};

const ROW_ACTIONS = [
  { label: 'Logs', suffix: '/logs', icon: LogsIcon },
  { label: 'Shell', suffix: '/shell', icon: SquareTerminalIcon },
  { label: 'Metrics', suffix: '/metrics', icon: ChartLineIcon },
] as const;

// Column headers as the ALB Backends card sets them.
const HEAD = 'text-muted-foreground text-3xs h-9 font-semibold uppercase';
// Inline: the host compiles no fixed-width sparkline column.
const SPARK_CELL = { width: '8rem', minWidth: '7rem' } as const;

function InstanceRow({
  instance,
  workloadName,
  href,
  locationIndex,
  projectId,
  traffic,
  trafficLoading,
  showTraffic,
  timeRange,
  deploying,
}: {
  instance: Instance;
  workloadName: string;
  href: string;
  locationIndex: LocationIndex;
  projectId?: string;
  traffic: Parameters<typeof MetricSparkline>[0]['series'];
  trafficLoading: boolean;
  showTraffic: boolean;
  timeRange: { start: Date; end: Date };
  deploying: boolean;
}) {
  const readiness = instanceReadiness(instance);
  const address = primaryAddress(instance);
  const name = instanceNameParts(instance.name, workloadName);
  // Not ready is expected while a deploy is under way, so only tint it afterwards.
  const tinted = readiness.tone === 'error' || (readiness.tone === 'warn' && !deploying);
  return (
    <TableRow
      data-testid="compute-plugin-instance-row"
      style={
        tinted ? { background: `color-mix(in oklab, ${STATUS_TONE_COLOR[readiness.tone]} 6%, transparent)` } : undefined
      }>
      <TableCell className="pl-(--card-px)">
        <Link to={href} className="flex min-w-0 items-center font-mono text-xs hover:underline" title={instance.name}>
          <span className="truncate">
            {name.prefix ? <span className="text-muted-foreground hidden md:inline">{name.prefix}</span> : null}
            <span className="font-medium">{name.rest}</span>
          </span>
        </Link>
      </TableCell>
      <TableCell
        className="text-muted-foreground hidden text-xs md:table-cell"
        title={instance.location ? formatLocationTooltip(instance.location, locationIndex) : undefined}>
        {instance.location ? formatLocationName(instance.location, locationIndex) : 'Unknown location'}
      </TableCell>
      <TableCell>
        {readiness.reason ? (
          // Focusable so keyboard users can open the reason too.
          <Tooltip message={readiness.reason}>
            <span tabIndex={0} className="inline-flex cursor-help" aria-label={`${readiness.label}: ${readiness.reason}`}>
              <StatusBadge type={BADGE_TYPE[readiness.tone]}>{readiness.label}</StatusBadge>
            </span>
          </Tooltip>
        ) : (
          <StatusBadge type={BADGE_TYPE[readiness.tone]}>{readiness.label}</StatusBadge>
        )}
      </TableCell>
      {showTraffic ? (
        <TableCell className="hidden md:table-cell" style={SPARK_CELL}>
          <MetricSparkline
            projectId={projectId}
            timeRange={timeRange}
            format="bitsPerSecond"
            series={traffic ?? []}
            pending={trafficLoading && !traffic}
            color="var(--color-badge-info)"
            compact
            wide
            hideValue
          />
        </TableCell>
      ) : null}
      <TableCell className="text-muted-foreground hidden font-mono text-xs md:table-cell" title={instance.addresses.join('\n') || undefined}>
        {address ?? '—'}
      </TableCell>
      <TableCell className="pr-(--card-px)">
        <span className="flex items-center justify-end gap-0.5">
          {ROW_ACTIONS.map((action) => (
            <Tooltip key={action.label} message={action.label}>
              <span className="inline-flex">
                <LinkButton
                  as={Link}
                  href={`${href}${action.suffix}`}
                  type="quaternary"
                  theme="borderless"
                  size="xs"
                  aria-label={`${action.label} for ${instance.name}`}
                  icon={<Icon icon={action.icon} size={14} aria-hidden />}
                />
              </span>
            </Tooltip>
          ))}
        </span>
      </TableCell>
    </TableRow>
  );
}

/**
 * The Instances card: a table once any instance is known, placeholder rows
 * while the query is unanswered, and a quiet notice for none or a failure.
 */
export function WorkloadInstances({
  workloadName,
  instances,
  loading,
  error,
  locationIndex,
  instanceHref,
  deploying,
  deleting,
  projectId,
  metricsDenied,
}: {
  workloadName: string;
  instances: readonly Instance[];
  /** The instances query hasn't answered yet. */
  loading: boolean;
  error: ApiError | null;
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  /** Still coming up, so no instances yet is expected. */
  deploying: boolean;
  deleting: boolean;
  projectId?: string;
  metricsDenied: boolean;
}) {
  const state = instancesSectionState({ count: instances.length, loading, error, deploying, deleting });
  const range = useOverviewRange('1h');
  const network = useWorkloadNetworkMetrics(metricsDenied ? undefined : projectId, instances);
  const showTraffic = !!network.throughputQuery;
  const chart = usePrometheusChart(projectId, network.throughputQuery, range.timeRange, {
    enabled: showTraffic,
    keepPrevious: true,
  });
  // Interface series folded into one per instance (an instance may have several NICs).
  const trafficByInstance = useMemo(
    () => (chart.error ? {} : seriesByWorkload(chart.data, 'network', network.refsByInstance)),
    [chart.data, chart.error, network.refsByInstance]
  );

  return (
    <Card sectioned size="sm" className="overflow-hidden" data-testid="compute-plugin-workload-instances">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={ServerIcon} size={16} className="text-secondary" aria-hidden />
          Instances
          {state.kind !== 'loading' && instances.length > 0 ? (
            <Badge type="muted" theme="solid" className="text-3xs h-5 rounded-md px-1.5 tabular-nums">
              {instances.length}
            </Badge>
          ) : null}
        </CardTitle>
      </CardHeader>
      {state.kind === 'loading' ? (
        <div className="px-2 py-2">
          <InstanceRowsSkeleton />
        </div>
      ) : state.kind === 'notice' ? (
        <div className="px-4 py-4">
          <QuietNotice testId="compute-plugin-instances-empty" title={state.title} description={state.description} />
        </div>
      ) : (
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow className="hover:bg-transparent">
              <TableHead className={cn(HEAD, 'pl-(--card-px)')}>Instance</TableHead>
              <TableHead className={cn(HEAD, 'hidden md:table-cell')}>Location</TableHead>
              <TableHead className={HEAD}>State</TableHead>
              {showTraffic ? (
                <TableHead className={cn(HEAD, 'hidden md:table-cell')}>
                  <Tooltip message="Network in and out over the last hour">
                    <span>Network · 1h</span>
                  </Tooltip>
                </TableHead>
              ) : null}
              <TableHead className={cn(HEAD, 'hidden md:table-cell')}>Address</TableHead>
              <TableHead className="pr-(--card-px)">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {instances.map((instance) => (
              <InstanceRow
                key={instance.uid || instance.name}
                instance={instance}
                workloadName={workloadName}
                href={instanceHref(instance.name)}
                locationIndex={locationIndex}
                projectId={projectId}
                traffic={trafficByInstance[instance.name]}
                trafficLoading={chart.isLoading}
                showTraffic={showTraffic}
                timeRange={range.timeRange}
                deploying={deploying}
              />
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}
