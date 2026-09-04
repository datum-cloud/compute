/**
 * `portal.page/service` extension at `workloads`, exposed as
 * `FleetWorkloads` — every Workload across compute's active consumer fleet
 * (healthy and unhealthy alike), on staff-portal's
 * `/admin/service-catalog/compute` detail page (see `../lib/fleet-health.ts`
 * for how the fan-out works and why).
 *
 * This is a triage index into the existing per-consumer troubleshooting page
 * (`WorkloadDetail`, `../pages/workload-detail.tsx`) — it links straight
 * there, it doesn't duplicate it. The "at a glance" fleet numbers
 * (`FleetStats`/`FailedProjectsNotice`) live on the Overview page instead
 * (`../pages/service-overview.tsx`) — this page is the detailed,
 * sortable/paginated drill-down across every workload, not a second
 * dashboard.
 *
 * Built directly on datum-ui's headless `@datum-cloud/datum-ui/data-table`
 * (sort/paginate) rather than the raw `Table` primitives the project-scoped
 * pages use — staff-portal's own list pages (Organizations,
 * the Consumers tab on this same service page) are all built on this same
 * primitive via a host-only `ListTable` wrapper (`app/features/milo`) this
 * plugin can't import (not federated), so this hand-wires the pieces
 * `ListTable` composes, styled to match.
 *
 * `serviceName` (the Service's resource name, e.g. "compute") comes from
 * `useParams()` resolving the ancestor route param from staff-portal's
 * service-scoped plugin mount, the same trick `WorkloadDetail` uses for
 * `projectName` — see `../lib/api.ts`'s header comment for why this works
 * with no extra prop/context plumbing.
 */
import { StatusBadge } from '../components/detail-list';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useFleetHealth, type FleetWorkload } from '../lib/fleet-health';
import { healthToBadgeType } from '../schema';
import { createColumnHelper, type ColumnDef } from '../lib/table';
import { DataTable } from '@datum-cloud/datum-ui/data-table';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { formatDistanceToNowStrict } from 'date-fns';
import { ChevronRightIcon } from 'lucide-react';
import { useMemo } from 'react';
import { Link, useParams } from 'react-router';

const columnHelper = createColumnHelper<FleetWorkload>();

/**
 * `DataTable.ColumnHeader` is itself correctly generic
 * (`Column<DataTableFeatures, TData, TValue>`), but once several
 * differently-typed columns are collected into one array and widened to
 * `ColumnDef<TData, unknown>[]` for `DataTable.Client` (see the cast below),
 * TanStack's `accessorFn`/`getUniqueValues` positions become invariant and
 * no longer narrow back per-column — same friction staff-portal's own
 * `ListColumnHeader` avoids by living outside that widened array. `column`
 * is `any` here for that reason; every other column-def field stays typed.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function SortableHeader({ column, title }: { column: any; title: string }) {
  return <DataTable.ColumnHeader column={column} title={title} />;
}

const cellClassName = 'px-3 py-2 text-sm';
const headerCellClassName = 'px-3 py-2 text-xs font-medium text-muted-foreground uppercase tracking-wide';
const rowClassName = 'border-border border-b last:border-b-0 hover:bg-muted/40';

function WorkloadsTable({ workloads }: { workloads: FleetWorkload[] }) {
  const columns = useMemo(
    () => [
      columnHelper.accessor((row) => row.workload.health, {
        id: 'health',
        header: ({ column }) => <SortableHeader column={column} title="Health" />,
        cell: ({ getValue }) => (
          <StatusBadge type={healthToBadgeType(getValue())}>{getValue()}</StatusBadge>
        ),
      }),
      columnHelper.accessor((row) => row.workload.name, {
        id: 'workload',
        header: ({ column }) => <SortableHeader column={column} title="Workload" />,
        cell: ({ row }) => (
          <Link
            to={`/customers/projects/${row.original.project.name}/plugins/workloads/${row.original.workload.name}`}
            className="hover:underline">
            <span className="font-mono text-sm">{row.original.workload.name}</span>
          </Link>
        ),
      }),
      columnHelper.accessor((row) => row.reason, {
        id: 'reason',
        header: 'Reason',
        // `max-w-56` isn't a class the host's compiled CSS contains (see ui/CLAUDE.md) — inline style instead.
        cell: ({ getValue }) => (
          <span className="block truncate" style={{ maxWidth: '14rem' }} title={getValue()}>
            {getValue()}
          </span>
        ),
      }),
      columnHelper.accessor((row) => row.project.displayName, {
        id: 'project',
        header: ({ column }) => <SortableHeader column={column} title="Consumer" />,
        cell: ({ row }) => {
          const { organization, name, displayName } = row.original.project;
          return (
            <div className="flex min-w-0 items-center gap-1 text-sm">
              {organization && (
                <>
                  <Link
                    to={`/customers/organizations/${organization.name}`}
                    className="truncate hover:underline">
                    {organization.displayName}
                  </Link>
                  <ChevronRightIcon className="text-muted-foreground size-3.5 shrink-0" />
                </>
              )}
              <Link to={`/customers/projects/${name}`} className="truncate hover:underline">
                {displayName}
              </Link>
            </div>
          );
        },
      }),
      columnHelper.display({
        id: 'ready',
        header: 'Ready',
        cell: ({ row }) => (
          <span className="text-muted-foreground">
            {row.original.workload.readyReplicas}/{row.original.workload.desiredReplicas}
          </span>
        ),
      }),
      columnHelper.accessor((row) => row.workload.regions.join(', '), {
        id: 'regions',
        header: 'Regions',
        cell: ({ getValue }) => <span className="text-muted-foreground">{getValue() || '—'}</span>,
      }),
      columnHelper.accessor((row) => row.statusSince.getTime(), {
        id: 'statusSince',
        header: ({ column }) => <SortableHeader column={column} title="Since" />,
        cell: ({ row }) => (
          <span className="text-muted-foreground">
            {formatDistanceToNowStrict(row.original.statusSince, { addSuffix: true })}
          </span>
        ),
      }),
    ],
    []
  );

  return (
    <DataTable.Client<FleetWorkload>
      data={workloads}
      // Each column's own TValue (string, health enum, number) is narrower
      // than the array element type DataTable.Client wants — same variance
      // wart staff-portal's own ListTable casts around.
      columns={columns as ColumnDef<FleetWorkload, unknown>[]}
      getRowId={(row) => `${row.project.name}/${row.workload.uid || row.workload.name}`}
      pageSize={25}
      className="border-border flex flex-col gap-3 overflow-hidden rounded-lg border">
      <DataTable.Content
        headerCellClassName={headerCellClassName}
        rowClassName={rowClassName}
        cellClassName={cellClassName}
        emptyMessage="No workloads."
      />
      <div className="px-2 pb-2">
        <DataTable.Pagination pageSizes={[10, 25, 50, 100]} />
      </div>
    </DataTable.Client>
  );
}

export default function FleetWorkloads() {
  const { name: serviceName } = useParams<{ name: string }>();
  const { data, isLoading, error, refetch } = useFleetHealth(serviceName);

  return (
    <div className="flex min-w-0 flex-col gap-6 p-4 sm:p-6" data-testid="provider-plugin-fleet-workloads">
      <PageTitle title="Workloads" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view compute's consumer fleet."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && data && (
        <>
          {data.workloads.length === 0 ? (
            <EmptyContent
              title="there are no workloads across the fleet"
              size="sm"
              variant="dashed"
            />
          ) : (
            <WorkloadsTable workloads={data.workloads} />
          )}
        </>
      )}
    </div>
  );
}
