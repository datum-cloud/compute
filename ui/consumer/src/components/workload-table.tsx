/**
 * Table view of the workloads list, built on datum-ui's `DataTable` and
 * styled like the project home page: a plain header, `h-10` rows with thin
 * lines, status as a dot and muted text, sortable column headers,
 * conditional pagination and whole-row click-through (`onRowClick`, which
 * leaves clicks on links and buttons to them). Columns are kept to what
 * identifies a workload and how it's doing (status, CPU, memory, where it
 * runs, which network it's on) so the list fits without horizontal
 * scrolling; image, load balancer and instance detail live on the
 * workload's own page.
 *
 * CPU and Memory come from one request per metric for the whole table
 * (`useUsageSeries`), and both columns stay hidden until that request shows
 * at least one row has data, so an empty fleet never shows them.
 *
 * The host does not share `datum-ui/data-table`, so this plugin bundles its
 * own copy (plus the `@tanstack/react-table` / `nuqs` peers). Class names are
 * identical to the host's, so it picks up the same compiled styles.
 */
import { MetricSparkline } from './metric-sparkline';
import { SortableHeader } from './sortable-header';
import { QuietNotice } from './surface/quiet-notice';
import { HealthDot, WorkloadStatusDot } from './workload-status-badge';
import type { LocationIndex } from '../lib/locations';
import { usageColumnsVisible, type Allocation } from '../lib/resource-usage';
import {
  type InstanceIdentityLabel,
  seriesByWorkload,
  workloadCpuByInstanceQuery,
  workloadMemoryByInstanceQuery,
} from '../lib/metrics-queries';
import { usePrometheusChart, type ChartDataPoint, type PrometheusTimeRange } from '../lib/prometheus';
import { HEALTH_ORDER, regionLabel, workloadStatusSortWeight } from '../lib/workload-presenters';
import type { Workload, WorkloadPlacementRegion } from '../schema';
import {
  DataTable,
  useDataTablePagination,
  type DataTableFeatures,
} from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ColumnDef } from '@tanstack/react-table';
import { useCallback, useMemo } from 'react';
import { Link } from 'react-router';

type WorkloadColumn = ColumnDef<DataTableFeatures, Workload, unknown>;

const TABLE_ID = 'compute-plugin-workload-table';

/**
 * Caps the Network and Locations lines so long names truncate. Inline
 * because the host compiles no `max-w-48`, and a missing utility fails
 * silently (see ui/CLAUDE.md).
 */
const CELL_LINE_STYLE = { maxWidth: '12rem' } as const;

/**
 * The host styles every DataTable through `.datum-ui-data-table [data-slot=…]`
 * rules (filled header, bordered header cells, `py-3` cells), which outrank
 * any utility class passed in. A rule scoped to this table outranks those and
 * gives it the home page's look: a plain muted header, `h-10` rows, thin
 * lines and `bg-accent` on hover.
 */
const TABLE_STYLES = `
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-header-row'],
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-row'] {
  border-bottom: 1px solid color-mix(in oklab, var(--border) 60%, transparent);
}
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-header-cell'] {
  background: transparent;
  border-right: 0;
  height: 2.5rem;
  padding: 0 0.5rem;
  color: var(--muted-foreground);
  font-size: 0.75rem;
  font-weight: 500;
  text-transform: none;
  letter-spacing: normal;
}
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-row'] {
  background: transparent;
}
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-row']:hover {
  background: var(--accent);
}
[data-testid='${TABLE_ID}'] .datum-ui-data-table [data-slot='dt-cell'] {
  height: 2.5rem;
  padding: 0 0.5rem;
}
`;

interface UsageSeries {
  cpu: Record<string, ChartDataPoint[]>;
  memory: Record<string, ChartDataPoint[]>;
  /** At least one row has a CPU or memory point. */
  visible: boolean;
}

/**
 * CPU and memory for every row in two requests (one per metric) instead of
 * two per row: a per-instance query over every workload's instances, split
 * back into per-workload sums for the rows shown. The last result is kept
 * while a changed query (new instances) loads, so the columns don't drop out
 * and return.
 */
function useUsageSeries({
  projectId,
  identityLabel,
  instanceKeysByWorkload,
  workloads,
  timeRange,
  denied,
}: {
  projectId?: string;
  identityLabel?: InstanceIdentityLabel;
  instanceKeysByWorkload: Record<string, string[]>;
  workloads: Workload[];
  timeRange: PrometheusTimeRange;
  denied: boolean;
}): UsageSeries {
  const keys = useMemo(
    () => Object.fromEntries(workloads.map((w) => [w.name, instanceKeysByWorkload[w.name] ?? []])),
    [workloads, instanceKeysByWorkload]
  );
  // The query covers every workload, not just the ones the search leaves, so
  // typing in the search doesn't send a new request per keystroke.
  const allKeys = useMemo(() => Object.values(instanceKeysByWorkload).flat(), [instanceKeysByWorkload]);
  const ready = !!projectId && !!identityLabel && !denied && allKeys.length > 0;
  const cpuQuery = ready ? workloadCpuByInstanceQuery(projectId, identityLabel, allKeys) : undefined;
  const memoryQuery = ready ? workloadMemoryByInstanceQuery(projectId, identityLabel, allKeys) : undefined;
  const cpuChart = usePrometheusChart(projectId, cpuQuery, timeRange, { enabled: ready, keepPrevious: true });
  const memoryChart = usePrometheusChart(projectId, memoryQuery, timeRange, { enabled: ready, keepPrevious: true });

  return useMemo(() => {
    if (!identityLabel || !ready) return { cpu: {}, memory: {}, visible: false };
    const cpu = seriesByWorkload(cpuChart.error ? undefined : cpuChart.data, identityLabel, keys);
    const memory = seriesByWorkload(memoryChart.error ? undefined : memoryChart.data, identityLabel, keys);
    const visible = usageColumnsVisible(
      [...Object.values(cpu), ...Object.values(memory)].map((points) => ({ points }))
    );
    return { cpu, memory, visible };
  }, [identityLabel, ready, keys, cpuChart.data, cpuChart.error, memoryChart.data, memoryChart.error]);
}

/**
 * One line however many regions a workload spans, so a multi-region workload
 * doesn't stretch its row: the least healthy region (so trouble is what
 * shows), then "+N" with every region listed in the tooltip.
 */
function LocationsCell({
  regions,
  locationIndex,
}: {
  regions: WorkloadPlacementRegion[];
  locationIndex: LocationIndex;
}) {
  if (regions.length === 0) return <span className="text-muted-foreground">—</span>;
  const sorted = [...regions].sort((a, b) => HEALTH_ORDER[a.health] - HEALTH_ORDER[b.health]);
  const [first] = sorted;
  const extra = sorted.length - 1;
  const line = (
    <span className="flex items-center gap-1.5 font-mono text-xs" style={CELL_LINE_STYLE}>
      <HealthDot health={first.health} className="size-1.5" label={first.health} />
      <span className="truncate">{regionLabel(first, locationIndex)}</span>
      {extra > 0 ? <span className="text-muted-foreground shrink-0">+{extra}</span> : null}
    </span>
  );
  return (
    <Tooltip
      message={
        <span className="flex flex-col gap-0.5">
          {sorted.map((region) => (
            <span key={region.name} className="flex items-center gap-1.5 font-mono">
              <HealthDot health={region.health} className="size-1.5" label={region.health} />
              {region.locations.join(', ') || regionLabel(region, locationIndex)}
            </span>
          ))}
        </span>
      }>
      {line}
    </Tooltip>
  );
}

/**
 * The Galactic VPC plugin's network detail page (network-services-operator),
 * which the host mounts under the project's `services/networking-datumapis-com`.
 */
function networkHref(projectId: string, networkName: string): string {
  return `/project/${projectId}/services/networking-datumapis-com/networks/${encodeURIComponent(networkName)}`;
}

/**
 * The network on the template's first interface (`deriveNetworks` keeps
 * interface order), linked to its Galactic VPC page, then "+N" with every
 * network in the tooltip when the template attaches to more than one.
 */
function NetworkCell({ networks, projectId }: { networks: string[]; projectId?: string }) {
  if (networks.length === 0) return <span className="text-muted-foreground">—</span>;
  const [first] = networks;
  const extra = networks.length - 1;
  const name = projectId ? (
    <Link to={networkHref(projectId, first)} className="truncate hover:underline" title={first} data-e2e="workload-network">
      {first}
    </Link>
  ) : (
    <span className="truncate" title={first}>
      {first}
    </span>
  );
  const line = (
    <span className="flex items-center gap-1.5 font-mono text-xs" style={CELL_LINE_STYLE}>
      {name}
      {extra > 0 ? <span className="text-muted-foreground shrink-0">+{extra}</span> : null}
    </span>
  );
  if (extra === 0) return line;
  return (
    <Tooltip
      message={
        <span className="flex flex-col gap-0.5">
          {networks.map((network) => (
            <span key={network} className="font-mono">
              {network}
            </span>
          ))}
        </span>
      }>
      {line}
    </Tooltip>
  );
}

/** Same rule as the portal: no pagination bar when everything fits on one page. */
function ConditionalPagination() {
  const { pageCount } = useDataTablePagination();
  if (pageCount <= 1) return null;
  return <DataTable.Pagination className="datum-ui-data-table__pagination" />;
}

export function WorkloadTable({
  workloads,
  projectId,
  instanceKeysByWorkload,
  allocationByWorkload,
  identityLabel,
  identityLoading = false,
  identityDenied = false,
  timeRange,
  locationIndex,
  workloadHref,
  onOpen,
}: {
  workloads: Workload[];
  projectId?: string;
  instanceKeysByWorkload: Record<string, string[]>;
  /** Allocated vCPU / memory per workload, so the sparks read as a percentage. */
  allocationByWorkload: Record<string, Allocation>;
  identityLabel?: InstanceIdentityLabel;
  identityLoading?: boolean;
  identityDenied?: boolean;
  timeRange: PrometheusTimeRange;
  locationIndex: LocationIndex;
  workloadHref: (name: string) => string;
  onOpen: (name: string) => void;
}) {
  const usage = useUsageSeries({
    projectId,
    identityLabel,
    instanceKeysByWorkload,
    workloads,
    timeRange,
    denied: identityDenied || identityLoading,
  });

  const columns = useMemo<WorkloadColumn[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: ({ column }) => <SortableHeader column={column} title="Name" />,
        cell: ({ row }) => (
          <div className="flex min-w-0" style={{ minWidth: 160 }}>
            <Link
              to={workloadHref(row.original.name)}
              className={cn('truncate text-sm font-medium hover:underline', row.original.deleting && 'text-muted-foreground')}
              title={row.original.name}
              data-e2e="workload-name">
              {row.original.name}
            </Link>
          </div>
        ),
      },
      {
        id: 'status',
        accessorFn: (workload) => workloadStatusSortWeight(workload),
        header: ({ column }) => <SortableHeader column={column} title="Status" />,
        cell: ({ row }) => <WorkloadStatusDot workload={row.original} />,
      },
      ...(usage.visible
        ? ([
            {
              id: 'cpu',
              header: 'CPU',
              enableSorting: false,
              cell: ({ row }) => (
                <MetricSparkline
                  projectId={projectId}
                  series={usage.cpu[row.original.name] ?? []}
                  timeRange={timeRange}
                  format="cores"
                  allocated={allocationByWorkload[row.original.name]?.cores}
                  compact
                />
              ),
            },
            {
              id: 'memory',
              header: 'Memory',
              enableSorting: false,
              cell: ({ row }) => (
                <MetricSparkline
                  projectId={projectId}
                  series={usage.memory[row.original.name] ?? []}
                  timeRange={timeRange}
                  format="bytes"
                  allocated={allocationByWorkload[row.original.name]?.memoryBytes}
                  color="var(--color-chart-1)"
                  compact
                />
              ),
            },
          ] satisfies WorkloadColumn[])
        : []),
      {
        id: 'network',
        accessorFn: (workload) => workload.networks[0] ?? '',
        header: ({ column }) => <SortableHeader column={column} title="Network" />,
        cell: ({ row }) => <NetworkCell networks={row.original.networks} projectId={projectId} />,
      },
      {
        id: 'locations',
        accessorFn: (workload) => workload.locations.length,
        header: ({ column }) => <SortableHeader column={column} title="Locations" />,
        cell: ({ row }) => <LocationsCell regions={row.original.placementRegions} locationIndex={locationIndex} />,
      },
    ],
    [projectId, usage, allocationByWorkload, timeRange, locationIndex, workloadHref]
  );

  const open = useCallback((workload: Workload) => onOpen(workload.name), [onOpen]);

  return (
    <DataTable.Client
      data={workloads}
      columns={columns}
      getRowId={(workload) => workload.uid || workload.name}
      className="space-y-6">
      <div data-testid={TABLE_ID}>
        <style>{TABLE_STYLES}</style>
        <DataTable.Content<Workload>
          onRowClick={open}
          emptyMessage={
            <QuietNotice title="No workloads match your search" description="Try a different name, image or location." />
          }
        />
      </div>
      <ConditionalPagination />
    </DataTable.Client>
  );
}
