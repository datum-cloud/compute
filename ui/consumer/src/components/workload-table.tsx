/**
 * Table view of the workloads list, built on the same datum-ui `DataTable`
 * the portal's ALB list renders through `Table.Client`: search toolbar,
 * sortable column headers, bordered panel, conditional pagination and
 * whole-row click-through. `RowsContent` ignores clicks that bubble from an
 * `<a>`, so nested links need no `stopPropagation`.
 *
 * The host does not share `datum-ui/data-table`, so this plugin bundles its
 * own copy (plus the `@tanstack/react-table` / `nuqs` peers). Class names are
 * identical to the host's, so it picks up the same compiled styles.
 */
import { CpuMemorySparks, MetricSparkline } from './metric-sparkline';
import type { PublishedUrl } from '../lib/api';
import type { LocationIndex } from '../lib/locations';
import {
  albRpsQuery,
  type InstanceIdentityLabel,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from '../lib/metrics-queries';
import type { PrometheusTimeRange } from '../lib/prometheus';
import {
  HEALTH_DOT_CLASS,
  HEALTH_ORDER,
  imageShortName,
  regionLabel,
  statusLabel,
} from '../lib/workload-presenters';
import { workloadHealthToBadgeType, type Workload } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  DataTable,
  useDataTablePagination,
  useDataTableRows,
  type DataTableFeatures,
} from '@datum-cloud/datum-ui/data-table';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import type { ColumnDef, Row } from '@tanstack/react-table';
import { formatDistanceToNowStrict } from 'date-fns';
import { GlobeIcon } from 'lucide-react';
import { useCallback, useMemo, type MouseEvent } from 'react';
import { Link } from 'react-router';

type WorkloadColumn = ColumnDef<DataTableFeatures, Workload, unknown>;

/**
 * Row-click delegation, as the portal's `TableContent` does: datum-ui's
 * `DataTable.Content` has no onRowClick, so walk up from the click target to
 * the `<tr>` and look the row up in the store. Nested links (load balancer
 * pill, name) navigate on their own without also opening the row.
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

/** Same rule as the portal: no pagination bar when everything fits on one page. */
function ConditionalPagination() {
  const { pageCount } = useDataTablePagination();
  if (pageCount <= 1) return null;
  return <DataTable.Pagination className="datum-ui-data-table__pagination" />;
}

export function WorkloadTable({
  workloads,
  projectId,
  publishedByWorkload,
  instanceKeysByWorkload,
  identityLabel,
  identityLoading = false,
  identityDenied = false,
  timeRange,
  locationIndex,
  workloadHref,
  albHref,
  onOpen,
}: {
  workloads: Workload[];
  projectId?: string;
  publishedByWorkload: Record<string, PublishedUrl>;
  instanceKeysByWorkload: Record<string, string[]>;
  identityLabel?: InstanceIdentityLabel;
  identityLoading?: boolean;
  identityDenied?: boolean;
  timeRange: PrometheusTimeRange;
  locationIndex: LocationIndex;
  workloadHref: (name: string) => string;
  albHref?: (proxyName: string) => string;
  onOpen: (name: string) => void;
}) {
  const columns = useMemo<WorkloadColumn[]>(
    () => [
      {
        id: 'name',
        accessorKey: 'name',
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Name" />,
        cell: ({ row }) => (
          <div className="flex min-w-0 flex-col" style={{ minWidth: 160 }}>
            <Link
              to={workloadHref(row.original.name)}
              className="truncate font-medium hover:underline"
              title={row.original.name}
              data-e2e="workload-name">
              {row.original.name}
            </Link>
            {row.original.runtimeType ? (
              <span className="text-muted-foreground truncate text-xs">{row.original.runtimeType}</span>
            ) : null}
          </div>
        ),
      },
      {
        id: 'status',
        accessorFn: (workload) => HEALTH_ORDER[workload.health],
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Status" />,
        cell: ({ row }) => (
          <div className="flex items-center gap-2">
            <span
              className={cn('size-2 shrink-0 rounded-full', HEALTH_DOT_CLASS[row.original.health])}
              aria-hidden
            />
            <Badge type={workloadHealthToBadgeType(row.original.health)} theme="light">
              {statusLabel(row.original)}
            </Badge>
          </div>
        ),
      },
      {
        id: 'activity',
        header: 'Activity',
        enableSorting: false,
        cell: ({ row }) => {
          const published = publishedByWorkload[row.original.name];
          if (!projectId || !published) {
            return (
              <span className="text-muted-foreground text-xs" title="No load balancer connected">
                —
              </span>
            );
          }
          return (
            <MetricSparkline
              query={albRpsQuery(projectId, published.proxyName)}
              timeRange={timeRange}
              format="requestsPerSecond"
              flatWhenZero
              emptyTitle="No load balancer connected"
            />
          );
        },
      },
      {
        id: 'resources',
        header: 'CPU / Memory',
        enableSorting: false,
        cell: ({ row }) => {
          const keys = instanceKeysByWorkload[row.original.name] ?? [];
          const ready = projectId && identityLabel && keys.length > 0;
          const cpuQuery = ready
            ? workloadCpuAvgQuery(projectId, identityLabel, keys)
            : undefined;
          const memoryQuery = ready
            ? workloadMemoryAvgQuery(projectId, identityLabel, keys)
            : undefined;
          return (
            <CpuMemorySparks
              cpuQuery={cpuQuery}
              memoryQuery={memoryQuery}
              timeRange={timeRange}
              compact
              pending={identityLoading}
              denied={identityDenied}
            />
          );
        },
      },
      {
        id: 'instances',
        accessorFn: (workload) => workload,
        sortingFn: (a: Row<DataTableFeatures, Workload>, b: Row<DataTableFeatures, Workload>) =>
          a.original.desiredReplicas - b.original.desiredReplicas ||
          a.original.readyReplicas - b.original.readyReplicas,
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Instances" />,
        cell: ({ row }) => {
          const { readyReplicas: ready, desiredReplicas: desired } = row.original;
          return (
            <span className="tabular-nums">
              <span className={cn(desired > 0 && ready < desired && 'text-yellow-600')}>{ready}</span>
              <span className="text-muted-foreground"> / {desired}</span>
            </span>
          );
        },
      },
      {
        id: 'locations',
        accessorFn: (workload) => workload.locations.length,
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Locations" />,
        cell: ({ row }) => {
          const regions = row.original.placementRegions;
          if (regions.length === 0) return <span className="text-muted-foreground">—</span>;
          return (
            <div className="flex max-w-48 flex-col gap-0.5">
              {regions.map((region) => (
                <span key={region.name} className="flex items-center gap-1.5 text-xs">
                  <span
                    className={cn('size-1.5 shrink-0 rounded-full', HEALTH_DOT_CLASS[region.health])}
                    aria-label={region.health}
                  />
                  <span className="truncate" title={region.locations.join(', ') || region.locationSelector}>
                    {regionLabel(region, locationIndex)}
                  </span>
                </span>
              ))}
            </div>
          );
        },
      },
      {
        id: 'loadBalancer',
        header: 'Load balancer',
        enableSorting: false,
        cell: ({ row }) => {
          const published = publishedByWorkload[row.original.name];
          if (!published) return <span className="text-muted-foreground">—</span>;
          const label = published.hostname ?? published.displayName;
          // Same pill the ALB list uses for its Compute workload origin link.
          const pill = (
            <Badge
              type="quaternary"
              theme="outline"
              className="h-6 max-w-full gap-1.5 rounded-xl px-2 text-xs font-normal">
              <Icon icon={GlobeIcon} size={12} className="shrink-0" />
              <span className="truncate">{label}</span>
            </Badge>
          );
          if (!albHref) return <span className="inline-flex max-w-full">{pill}</span>;
          return (
            <Link
              to={albHref(published.proxyName)}
              className="inline-flex max-w-full"
              title={label}
              data-e2e="workload-list-alb">
              {pill}
            </Link>
          );
        },
      },
      {
        id: 'image',
        header: 'Image',
        enableSorting: false,
        cell: ({ row }) => {
          const image = imageShortName(row.original.image);
          if (!image) return <span className="text-muted-foreground">—</span>;
          return (
            <span className="text-muted-foreground block max-w-48 truncate font-mono text-xs" title={row.original.image}>
              {image}
            </span>
          );
        },
      },
      {
        id: 'createdAt',
        accessorFn: (workload) => workload.createdAt.getTime(),
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Created" />,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs whitespace-nowrap" title={row.original.createdAt.toLocaleString()}>
            {formatDistanceToNowStrict(row.original.createdAt, { addSuffix: true })}
          </span>
        ),
      },
    ],
    [projectId, publishedByWorkload, instanceKeysByWorkload, identityLabel, identityLoading, identityDenied, locationIndex, workloadHref, albHref, timeRange]
  );

  const searchFn = useCallback(
    (workload: Workload, search: string) => {
      const needle = search.trim().toLowerCase();
      if (!needle) return true;
      return [
        workload.name,
        workload.image,
        workload.runtimeType,
        ...workload.tags,
        ...workload.locations,
        publishedByWorkload[workload.name]?.hostname,
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
        .includes(needle);
    },
    [publishedByWorkload]
  );

  const open = useCallback((workload: Workload) => onOpen(workload.name), [onOpen]);

  return (
    <DataTable.Client
      data={workloads}
      columns={columns}
      getRowId={(workload) => workload.uid || workload.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={searchFn}
      className="space-y-6">
      <div className="flex w-full flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <DataTable.Search placeholder="Search" className="w-full sm:max-w-xs" />
      </div>
      <div className="overflow-hidden rounded-lg border" data-testid="compute-plugin-workload-table">
        <RowsContent onOpen={open} />
      </div>
      <ConditionalPagination />
    </DataTable.Client>
  );
}
