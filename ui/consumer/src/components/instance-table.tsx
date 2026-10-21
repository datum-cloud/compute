/**
 * Instance table shared by the workload Instances tab and, with
 * `showWorkloadColumn`, a project-wide list. Built on the same datum-ui
 * `DataTable.Client` / `DataTable.Content` as `workload-table.tsx`: search
 * toolbar, sortable headers, conditional pagination and whole-row
 * click-through to the instance page.
 *
 * Every row is a single line: placement, deployment and gate names are
 * search terms rather than cells, sizing is on the instance Overview, and
 * every cell has a bounded inline width so the table fits the portal's
 * content area without a horizontal scroll.
 */
import { formatLocationName, formatLocationTooltip, type LocationIndex } from '../lib/locations';
import {
  INSTANCE_STATUS_ORDER,
  instanceMatchesSearch,
  instanceStatusBadge,
  instanceTypeShortName,
} from '../lib/instance-presenters';
import type { Instance } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  DataTable,
  useDataTablePagination,
  type DataTableFeatures,
} from '@datum-cloud/datum-ui/data-table';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import type { ColumnDef } from '@tanstack/react-table';
import { formatDistanceToNowStrict } from 'date-fns';
import { useCallback, useMemo, type ReactNode } from 'react';
import { Link } from 'react-router';

type InstanceColumn = ColumnDef<DataTableFeatures, Instance, unknown>;

// Inline widths: the table must fit the portal's ~1100px content area, and
// IPv6 addresses or long names would otherwise stretch their columns.
const NAME_STYLE = { minWidth: 200, maxWidth: 360 } as const;
const REASON_STYLE = { maxWidth: 130 } as const;
const IP_STYLE = { maxWidth: 170 } as const;
const TYPE_STYLE = { maxWidth: 120 } as const;

/** Mono value cells (IPs, types) that truncate at a fixed width, full value in the tooltip. */
function MonoCell({
  value,
  title,
  style,
}: {
  value?: string;
  title?: string;
  style: { maxWidth: number };
}) {
  if (!value) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="block truncate font-mono text-xs" style={style} title={title ?? value}>
      {value}
    </span>
  );
}

/**
 * One line: badge, then the reason. Suspended and gated instances show as
 * such here rather than as extra badges on the name, since both explain why
 * the instance is not serving.
 */
function StatusCell({ instance }: { instance: Instance }) {
  let badge: ReactNode;
  let reason: string | undefined;
  let detail: string | undefined;
  if (instance.suspended) {
    badge = (
      <Badge type="secondary" theme="outline" className="w-fit shrink-0">
        Suspended
      </Badge>
    );
  } else if (instance.schedulingGates.length > 0) {
    badge = (
      <Badge type="warning" theme="light" className="w-fit shrink-0">
        Pending
      </Badge>
    );
    // Gate names are fully qualified and long; keep them for the tooltip.
    reason = 'Gated';
    detail = `Gated by ${instance.schedulingGates.join(', ')}`;
  } else {
    badge = (
      <Badge type={instanceStatusBadge(instance.status)} theme="light" className="w-fit shrink-0">
        {instance.status}
      </Badge>
    );
    reason = instance.status !== 'Available' ? instance.statusReason : undefined;
    detail = instance.statusMessage ?? reason;
  }
  return (
    <div className="flex min-w-0 items-center gap-2">
      {badge}
      {reason ? (
        <span
          className="text-muted-foreground truncate text-xs"
          style={REASON_STYLE}
          title={detail}
        >
          {reason}
        </span>
      ) : null}
    </div>
  );
}

/** Same rule as the portal: no pagination bar when everything fits on one page. */
function ConditionalPagination() {
  const { pageCount } = useDataTablePagination();
  if (pageCount <= 1) return null;
  return <DataTable.Pagination className="datum-ui-data-table__pagination" />;
}

export function InstanceTable({
  instances,
  locationIndex,
  instanceHref,
  workloadHref,
  showWorkloadColumn = false,
  showTypeColumn = false,
  onOpen,
  toolbar,
  emptyTitle = 'No instances for this workload',
  emptyAction,
}: {
  instances: Instance[];
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  /** Required when `showWorkloadColumn` is set; links the Workload column. */
  workloadHref?: (name: string) => string;
  showWorkloadColumn?: boolean;
  /** Off by default: within one workload every instance shares the template's type. */
  showTypeColumn?: boolean;
  onOpen: (name: string) => void;
  /** Facet controls on the search row. Keeps the table mounted when filters empty it. */
  toolbar?: ReactNode;
  emptyTitle?: string;
  emptyAction?: { label: string; onClick: () => void };
}) {
  const columns = useMemo<InstanceColumn[]>(() => {
    const typeColumn: InstanceColumn[] = showTypeColumn
      ? [
          {
            id: 'type',
            accessorFn: (instance) => instance.instanceType ?? '',
            header: ({ column }) => <DataTable.ColumnHeader column={column} title="Type" />,
            cell: ({ row }) => (
              <MonoCell
                value={instanceTypeShortName(row.original.instanceType)}
                title={row.original.instanceType}
                style={TYPE_STYLE}
              />
            ),
          },
        ]
      : [];
    const workloadColumn: InstanceColumn[] = showWorkloadColumn
      ? [
          {
            id: 'workload',
            accessorFn: (instance) => instance.workloadName ?? '',
            header: ({ column }) => <DataTable.ColumnHeader column={column} title="Workload" />,
            cell: ({ row }) => {
              const name = row.original.workloadName;
              if (!name) return <span className="text-muted-foreground">—</span>;
              if (!workloadHref) return <span className="truncate text-sm">{name}</span>;
              return (
                <Link
                  to={workloadHref(name)}
                  className="truncate text-sm hover:underline"
                  title={name}
                >
                  {name}
                </Link>
              );
            },
          },
        ]
      : [];

    return [
      {
        id: 'name',
        accessorKey: 'name',
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Instance" />,
        cell: ({ row }) => (
          <Link
            to={instanceHref(row.original.name)}
            className="block truncate font-mono text-sm font-medium hover:underline"
            style={NAME_STYLE}
            title={row.original.name}
            data-e2e="instance-name"
          >
            {row.original.name}
          </Link>
        ),
      },
      ...workloadColumn,
      {
        id: 'status',
        accessorFn: (instance) => INSTANCE_STATUS_ORDER[instance.status],
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Status" />,
        cell: ({ row }) => <StatusCell instance={row.original} />,
      },
      {
        id: 'location',
        accessorFn: (instance) =>
          instance.location ? formatLocationName(instance.location, locationIndex) : '',
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Location" />,
        cell: ({ row }) => {
          const { location } = row.original;
          if (!location) return <span className="text-muted-foreground">—</span>;
          return (
            <span
              className="text-sm whitespace-nowrap"
              title={formatLocationTooltip(location, locationIndex)}
            >
              {formatLocationName(location, locationIndex)}
            </span>
          );
        },
      },
      {
        id: 'internalIP',
        header: 'Internal IP',
        enableSorting: false,
        cell: ({ row }) => (
          <MonoCell
            value={row.original.internalIP}
            title={row.original.internalIPs.join(', ')}
            style={IP_STYLE}
          />
        ),
      },
      {
        id: 'externalIP',
        header: 'External IP',
        enableSorting: false,
        cell: ({ row }) => <MonoCell value={row.original.externalIP} style={IP_STYLE} />,
      },
      ...typeColumn,
      {
        id: 'createdAt',
        accessorFn: (instance) => instance.createdAt.getTime(),
        header: ({ column }) => <DataTable.ColumnHeader column={column} title="Age" />,
        cell: ({ row }) => (
          <span
            className="text-muted-foreground text-xs whitespace-nowrap"
            title={row.original.createdAt.toLocaleString()}
          >
            {formatDistanceToNowStrict(row.original.createdAt)}
          </span>
        ),
      },
    ];
  }, [instanceHref, locationIndex, showTypeColumn, showWorkloadColumn, workloadHref]);

  const searchFn = useCallback(
    (instance: Instance, search: string) => instanceMatchesSearch(instance, search, locationIndex),
    [locationIndex],
  );

  const open = useCallback((instance: Instance) => onOpen(instance.name), [onOpen]);

  const emptyActions = emptyAction
    ? [{ as: 'button' as const, label: emptyAction.label, onClick: emptyAction.onClick }]
    : undefined;

  // Without a toolbar there is nothing to interact with, so skip the table chrome.
  if (instances.length === 0 && !toolbar) {
    return <EmptyContent title={emptyTitle} size="sm" variant="dashed" actions={emptyActions} />;
  }

  return (
    <DataTable.Client
      data={instances}
      columns={columns}
      getRowId={(instance) => instance.uid || instance.name}
      // Status ascending puts Failed/Pending first (see INSTANCE_STATUS_ORDER).
      defaultSort={[{ id: 'status', desc: false }]}
      searchFn={searchFn}
      className="space-y-6"
    >
      <div className="flex w-full flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">
        <DataTable.Search placeholder="Search instances" className="w-full sm:max-w-xs" />
        {toolbar}
      </div>
      <div
        className="overflow-hidden rounded-lg border"
        data-testid="compute-plugin-instance-table"
      >
        <DataTable.Content
          onRowClick={open}
          emptyMessage={
            <EmptyContent
              title={instances.length === 0 ? emptyTitle : 'Try adjusting your search'}
              className="w-full rounded-none border-0"
              actions={instances.length === 0 ? emptyActions : undefined}
            />
          }
        />
      </div>
      <ConditionalPagination />
    </DataTable.Client>
  );
}
