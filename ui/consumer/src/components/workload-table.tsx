/**
 * Table view of the workloads list, built on the same datum-ui `DataTable`
 * the portal's ALB list renders through `Table.Client`: search toolbar,
 * sortable column headers, bordered panel, conditional pagination and
 * whole-row click-through. Columns are kept to what identifies a workload
 * and how it's doing (status, CPU, memory, where it runs, which network it's
 * on) so the list fits
 * without horizontal scrolling; image, load balancer and instance detail
 * live on the workload's own page. `RowsContent` ignores clicks that bubble
 * from an `<a>`, so nested links need no `stopPropagation`.
 *
 * The host does not share `datum-ui/data-table`, so this plugin bundles its
 * own copy (plus the `@tanstack/react-table` / `nuqs` peers). Class names are
 * identical to the host's, so it picks up the same compiled styles.
 */
import { MetricSparkline } from './metric-sparkline';
import { SortableHeader } from './sortable-header';
import { HealthDot, WorkloadStatusBadge } from './workload-status-badge';
import type { LocationIndex } from '../lib/locations';
import type { Allocation } from '../lib/resource-usage';
import {
  type InstanceIdentityLabel,
  workloadCpuSumQuery,
  workloadMemorySumQuery,
} from '../lib/metrics-queries';
import type { PrometheusTimeRange } from '../lib/prometheus';
import { HEALTH_ORDER, regionLabel } from '../lib/workload-presenters';
import type { Workload, WorkloadPlacementRegion } from '../schema';
import {
  DataTable,
  useDataTablePagination,
  useDataTableRows,
  type DataTableFeatures,
} from '@datum-cloud/datum-ui/data-table';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ColumnDef } from '@tanstack/react-table';
import { useCallback, useMemo, type MouseEvent } from 'react';
import { Link } from 'react-router';

type WorkloadColumn = ColumnDef<DataTableFeatures, Workload, unknown>;

/**
 * Row-click delegation, as the portal's `TableContent` does: datum-ui's
 * `DataTable.Content` has no onRowClick, so walk up from the click target to
 * the `<tr>` and look the row up in the store. The nested name link
 * navigates on its own without also opening the row.
 */
function RowsContent({ onOpen }: { onOpen: (workload: Workload) => void }) {
  const { rows } = useDataTableRows<Workload>();

  const handleClick = useCallback(
    (event: MouseEvent<HTMLDivElement>) => {
      const target = event.target as HTMLElement;
      if (target.closest('a')) return;
      const tr = target.closest('tbody tr');
      const tbody = tr?.closest('tbody');
      if (!tr || !tbody) return;
      const index = Array.from(tbody.children).indexOf(tr as HTMLTableRowElement);
      const row = rows[index];
      if (row) onOpen(row.original);
    },
    [onOpen, rows]
  );

  return (
    <div onClick={handleClick} className="[&_tbody_tr]:cursor-pointer">
      <DataTable.Content
        emptyMessage={
          <EmptyContent
            title="Try adjusting your search"
            className="w-full rounded-none border-0"
          />
        }
      />
    </div>
  );
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
    <span className="flex max-w-48 items-center gap-1.5 text-xs">
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
            <span key={region.name} className="flex items-center gap-1.5">
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
    <span className="flex max-w-48 items-center gap-1.5 text-xs">
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
            <span key={network}>{network}</span>
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
              className={cn('truncate font-medium hover:underline', row.original.deleting && 'text-muted-foreground')}
              title={row.original.name}
              data-e2e="workload-name">
              {row.original.name}
            </Link>
          </div>
        ),
      },
      {
        id: 'status',
        accessorFn: (workload) => HEALTH_ORDER[workload.health],
        header: ({ column }) => <SortableHeader column={column} title="Status" />,
        cell: ({ row }) => <WorkloadStatusBadge workload={row.original} dot={false} />,
      },
      {
        id: 'cpu',
        header: 'CPU',
        enableSorting: false,
        cell: ({ row }) => {
          const keys = instanceKeysByWorkload[row.original.name] ?? [];
          return (
            <MetricSparkline
              query={projectId && identityLabel && keys.length > 0 ? workloadCpuSumQuery(projectId, identityLabel, keys) : undefined}
              timeRange={timeRange}
              format="cores"
              allocated={allocationByWorkload[row.original.name]?.cores}
              compact
              pending={identityLoading}
              denied={identityDenied}
              emptyTitle="CPU metrics aren't available yet"
            />
          );
        },
      },
      {
        id: 'memory',
        header: 'Memory',
        enableSorting: false,
        cell: ({ row }) => {
          const keys = instanceKeysByWorkload[row.original.name] ?? [];
          return (
            <MetricSparkline
              query={projectId && identityLabel && keys.length > 0 ? workloadMemorySumQuery(projectId, identityLabel, keys) : undefined}
              timeRange={timeRange}
              format="bytes"
              allocated={allocationByWorkload[row.original.name]?.memoryBytes}
              color="var(--color-chart-1)"
              compact
              pending={identityLoading}
              denied={identityDenied}
              emptyTitle="Memory metrics aren't available yet"
            />
          );
        },
      },
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
    [projectId, instanceKeysByWorkload, allocationByWorkload, identityLabel, identityLoading, identityDenied, timeRange, locationIndex, workloadHref]
  );

  const open = useCallback((workload: Workload) => onOpen(workload.name), [onOpen]);

  return (
    <DataTable.Client
      data={workloads}
      columns={columns}
      getRowId={(workload) => workload.uid || workload.name}
      className="space-y-6">
      <div className="overflow-hidden rounded-lg border" data-testid="compute-plugin-workload-table">
        <RowsContent onOpen={open} />
      </div>
      <ConditionalPagination />
    </DataTable.Client>
  );
}
