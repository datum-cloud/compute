/**
 * Content-only loading placeholders. Pages keep breadcrumbs, titles, and tabs
 * mounted and swap just the data region for one of these.
 *
 * Panel heights that the host stylesheet does not compile (`27rem`, `32rem`,
 * chart `224px`) are inline styles, matching the live pages.
 */
import { Card, CardContent, CardHeader } from '@datum-cloud/datum-ui/card';
import { STRIP_GAP } from './stat-strip';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { cn } from '@datum-cloud/datum-ui/utils';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { ChevronDown, ChevronUp } from 'lucide-react';
import type { CSSProperties } from 'react';

const PANEL_STYLE = { height: '27rem' } as const;
const EXPLORER_STYLE = { height: '32rem' } as const;
const CHART_STYLE = { height: 224 } as const;
const TOPOLOGY_STYLE = { height: '28rem' } as const;

/**
 * The list table's columns before usage data arrives, each with a bone the
 * width of what it usually holds. CPU and Memory only appear once a row has
 * data, so the skeleton leaves them out.
 */
const TABLE_COLUMNS = [
  { title: 'Name', bones: ['h-4 w-32'] },
  { title: 'Status', bones: ['size-2 rounded-full', 'h-4 w-20'] },
  { title: 'Network', bones: ['h-4 w-16'] },
  { title: 'Locations', bones: ['size-1.5 rounded-full', 'h-4 w-40'] },
] as const;

function Bone({ className, style }: { className?: string; style?: CSSProperties }) {
  return <Skeleton className={className} style={style} />;
}

function Panel({ children, style }: { children: React.ReactNode; style?: CSSProperties }) {
  return (
    <Card size="sm" sectioned className="flex h-full flex-col overflow-hidden" style={style}>
      <CardHeader size="sm" bordered>
        <Bone className="h-4 w-28" />
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col gap-3">
        {children}
      </CardContent>
    </Card>
  );
}

function ChartBlock() {
  return (
    <Card size="sm" sectioned className="overflow-hidden">
      <CardHeader size="sm" bordered>
        <Bone className="h-4 w-24" />
      </CardHeader>
      <CardContent>
        <Bone className="w-full rounded-md" style={CHART_STYLE} />
      </CardContent>
    </Card>
  );
}

function MetricsSkeleton({ testId }: { testId: string }) {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid={testId}>
      <div className="flex justify-end">
        <Bone className="h-9 w-44" />
      </div>
      <Card size="sm" sectioned>
        <CardContent>
          <div className="border-border flex divide-x overflow-hidden rounded-lg border">
            {Array.from({ length: 5 }).map((_, index) => (
              <div key={index} className="flex min-w-24 flex-1 flex-col gap-2 px-3 py-3">
                <Bone className="h-3 w-14" />
                <Bone className="h-5 w-16" />
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <ChartBlock />
        <ChartBlock />
      </div>
      <ChartBlock />
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <ChartBlock />
        <ChartBlock />
        <ChartBlock />
      </div>
    </div>
  );
}

/** Matches `StatStrip`: four label/value pairs in one open row. */
function FleetSummarySkeleton() {
  return (
    <div className="flex flex-wrap items-start px-1" style={STRIP_GAP}>
      {Array.from({ length: 4 }).map((_, index) => (
        <div key={index} className="flex flex-col gap-0.5">
          <Bone className="h-3 w-16" />
          <Bone className="mt-1 h-3 w-10 rounded" />
        </div>
      ))}
    </div>
  );
}

/** Search on the left and the Deploy button on the right, as on the page. */
function ToolbarSkeleton() {
  return (
    <div className="flex w-full flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <Bone className="h-9 w-full sm:max-w-xs" />
      <Bone className="h-8 w-28" />
    </div>
  );
}

export function WorkloadListCardsSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-cards">
      <FleetSummarySkeleton />
      <ToolbarSkeleton />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {Array.from({ length: 4 }).map((_, index) => (
          <Card key={index} size="sm" className="overflow-hidden">
            <div className="flex items-start justify-between gap-3 px-4 pt-3">
              <Bone className="h-4 w-40" />
              <Bone className="h-4 w-20" />
            </div>
            <CardContent className="flex flex-col gap-4">
              <Bone className="h-16 w-full" />
              <div className="grid grid-cols-2 gap-3">
                <Bone className="h-10 w-full" />
                <Bone className="h-10 w-full" />
              </div>
              <div className="flex flex-col gap-2">
                <Bone className="h-3 w-20" />
                <Bone className="h-4 w-full" />
                <Bone className="h-4 w-3/4" />
              </div>
            </CardContent>
            <div className="flex items-center justify-between gap-2 px-4 pb-3">
              <Bone className="h-3 w-28" />
              <Bone className="h-3 w-24" />
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}

/** The sort chevrons `SortableHeader` draws, at rest. */
function SortChevrons() {
  return (
    <div className="flex flex-col" aria-hidden>
      <Icon icon={ChevronUp} size={10} className="text-foreground -mb-0.5 stroke-2 opacity-25" />
      <Icon icon={ChevronDown} size={10} className="text-foreground -mt-0.5 stroke-2 opacity-25" />
    </div>
  );
}

/**
 * The live table's shell (`WorkloadTable`: datum-ui's DataTable markup in a
 * rounded, bordered panel, with sortable headers) and placeholder rows in it, so the
 * swap to real rows doesn't move anything. `summary={false}` is the in-page
 * fallback, where the strip and toolbar are already on screen.
 */
export function WorkloadListTableSkeleton({ summary = true }: { summary?: boolean }) {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-table">
      {summary ? (
        <>
          <FleetSummarySkeleton />
          <ToolbarSkeleton />
        </>
      ) : null}
      <div className="overflow-hidden rounded-lg border">
        {/* DataTable's class and slots: the host styles its tables by these. */}
        <div className="datum-ui-data-table" data-slot="dt" style={{ overflowX: 'auto' }}>
          <Table data-slot="dt-table">
            <TableHeader data-slot="dt-header">
              <TableRow data-slot="dt-header-row">
                {TABLE_COLUMNS.map((column) => (
                  <TableHead key={column.title} data-slot="dt-header-cell">
                    <div className="flex h-full items-center justify-between gap-1">
                      <span>{column.title}</span>
                      <SortChevrons />
                    </div>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody data-slot="dt-body">
              {Array.from({ length: 6 }).map((_, row) => (
                <TableRow key={row} data-slot="dt-row">
                  {TABLE_COLUMNS.map((column) => (
                    <TableCell key={column.title} data-slot="dt-cell">
                      <span className="flex h-5 items-center gap-2">
                        {column.bones.map((bone) => (
                          <Bone key={bone} className={bone} />
                        ))}
                      </span>
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </div>
    </div>
  );
}

// Column heads as the Instances and Load balancers cards set them.
const CARD_HEAD = 'text-muted-foreground text-3xs h-9 font-semibold uppercase';

interface CardColumn {
  title: string;
  /** Bone classes, side by side in the cell. */
  bones: readonly string[];
  /** Hidden below `md`, as the live column is. */
  wide?: boolean;
}

/** The Instances card's columns, before network data adds its sparkline column. */
const INSTANCE_COLUMNS: readonly CardColumn[] = [
  { title: 'Instance', bones: ['h-4 w-48'] },
  { title: 'Location', bones: ['h-4 w-20'], wide: true },
  { title: 'State', bones: ['h-4 w-14 rounded-sm'] },
  { title: 'Address', bones: ['h-4 w-32'], wide: true },
  { title: '', bones: ['size-6 rounded-md', 'size-6 rounded-md', 'size-6 rounded-md'] },
];

const LOAD_BALANCER_COLUMNS: readonly CardColumn[] = [
  { title: 'Load balancer', bones: ['h-4 w-28'] },
  { title: 'State', bones: ['h-4 w-14 rounded-sm'] },
  { title: 'Hostname', bones: ['h-4 w-56'], wide: true },
  { title: 'Routes', bones: ['h-4 w-32'] },
  { title: '', bones: ['size-6 rounded-md'] },
];

/**
 * A card's table while its rows load: the live table's header and cell
 * padding, with bones where the values go, so the rows land without a jump.
 */
function CardTableSkeleton({ columns, rows }: { columns: readonly CardColumn[]; rows: number }) {
  const edge = (index: number) =>
    index === 0 ? 'pl-(--card-px)' : index === columns.length - 1 ? 'pr-(--card-px)' : undefined;
  return (
    <Table aria-hidden>
      <TableHeader className="bg-muted/40">
        <TableRow className="hover:bg-transparent">
          {columns.map((column, index) => (
            <TableHead
              key={index}
              className={cn(CARD_HEAD, edge(index), column.wide && 'hidden md:table-cell')}>
              {column.title}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {Array.from({ length: rows }, (_, row) => (
          <TableRow key={row} className="hover:bg-transparent">
            {columns.map((column, index) => (
              <TableCell key={index} className={cn(edge(index), column.wide && 'hidden md:table-cell')}>
                <span className={cn('flex h-6 items-center gap-1', index === columns.length - 1 && 'justify-end')}>
                  {column.bones.map((bone, i) => (
                    <Bone key={i} className={bone} />
                  ))}
                </span>
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** The Instances card's table while instances load. */
export function InstanceTableSkeleton({ rows = 2 }: { rows?: number }) {
  return <CardTableSkeleton columns={INSTANCE_COLUMNS} rows={rows} />;
}

/** The Load balancers card's table while the lookup runs. */
export function LoadBalancerTableSkeleton() {
  return <CardTableSkeleton columns={LOAD_BALANCER_COLUMNS} rows={1} />;
}

/** A sectioned card's bordered header: icon and title. */
function CardHeaderBone() {
  return (
    <CardHeader size="sm" bordered>
      <span className="flex items-center gap-2">
        <Bone className="size-4 rounded" />
        <Bone className="h-4 w-28" />
      </span>
    </CardHeader>
  );
}

/** An open section's header: heading, plus room for a right-side action. */
function SectionHeaderBone({ action = false }: { action?: boolean }) {
  return (
    <div className="flex h-7 items-center justify-between gap-2">
      <Bone className="h-4 w-24" />
      {action ? <Bone className="h-4 w-16" /> : null}
    </div>
  );
}

/**
 * Matches `WorkloadOverview`: status line (`h-9`), Instances rows (`h-10`),
 * Recent logs (its loading block is `min-h-48`), and the two-column Details.
 */
export function WorkloadOverviewSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-workload-overview">
      <Card size="sm" sectioned className="overflow-hidden">
        <div className="flex items-start gap-3 px-4 py-4">
          <Bone className="size-9 shrink-0 rounded-full" />
          <div className="flex flex-1 flex-col gap-2">
            <Bone className="h-4 w-40" />
            <Bone className="h-3 w-64" />
          </div>
        </div>
        {/* The stats strip's cells (`StatCell` while loading): label and hint, value, sparkline. */}
        <div className="bg-border grid grid-cols-2 gap-px border-t sm:flex">
          {Array.from({ length: 4 }, (_, index) => (
            <div key={index} className="bg-card flex min-w-0 flex-1 flex-col gap-1.5 px-4 py-3">
              <span className="flex h-4 items-center justify-between gap-2">
                <Bone className="h-3 w-14" />
                <Bone className="h-3 w-16" />
              </span>
              <span className="flex h-7 items-center">
                <Bone className="h-5 w-14" />
              </span>
              <Bone className="h-8 w-full" />
            </div>
          ))}
        </div>
      </Card>
      <Card sectioned size="sm" className="overflow-hidden">
        <CardHeaderBone />
        <InstanceTableSkeleton />
      </Card>
      <Card sectioned size="sm" className="overflow-hidden">
        <CardHeaderBone />
        <LoadBalancerTableSkeleton />
      </Card>
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-2">
          <SectionHeaderBone action />
          <Bone className="min-h-48 w-full rounded-md" />
        </div>
        <Panel>
          {Array.from({ length: 6 }, (_, index) => (
            <div key={index} className="flex gap-4">
              <Bone className="h-3 w-24" />
              <Bone className="h-3 w-32" />
            </div>
          ))}
        </Panel>
      </div>
    </div>
  );
}

/** Matches the Topology tab: one diagram panel. */
export function WorkloadTopologySkeleton() {
  return (
    <div aria-busy="true" data-testid="compute-plugin-loading-workload-topology">
      <Panel style={TOPOLOGY_STYLE}>
        <Bone className="min-h-0 w-full flex-1" />
      </Panel>
    </div>
  );
}

export function WorkloadMetricsSkeleton() {
  return <MetricsSkeleton testId="compute-plugin-loading-workload-metrics" />;
}

export function InstanceOverviewSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-instance-overview">
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="flex flex-col gap-6">
          <Panel>
            {Array.from({ length: 8 }).map((_, index) => (
              <Bone key={index} className="h-4 w-full" />
            ))}
          </Panel>
          <Panel>
            <Bone className="w-full" style={CHART_STYLE} />
          </Panel>
        </div>
        <div style={PANEL_STYLE}>
          <Panel style={PANEL_STYLE}>
            <Bone className="min-h-0 w-full flex-1" />
          </Panel>
        </div>
      </div>
      <Panel>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {Array.from({ length: 4 }).map((_, index) => (
            <Bone key={index} className="h-12 w-full" />
          ))}
        </div>
      </Panel>
    </div>
  );
}

export function InstanceLogsSkeleton() {
  return (
    <Card
      size="sm"
      sectioned
      className="flex flex-col overflow-hidden"
      style={EXPLORER_STYLE}
      aria-busy="true"
      data-testid="compute-plugin-loading-instance-logs">
      <CardContent padding="none" className="flex min-h-0 flex-1">
        <div className="border-border hidden w-56 shrink-0 flex-col gap-3 border-r p-4 sm:flex">
          <Bone className="h-4 w-20" />
          <Bone className="h-8 w-full" />
          <Bone className="h-8 w-full" />
          <Bone className="h-8 w-full" />
        </div>
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="border-border flex items-center justify-between gap-3 border-b px-4 py-3">
            <Bone className="h-8 w-40" />
            <Bone className="h-8 w-28" />
          </div>
          <div className="flex flex-col">
            {Array.from({ length: 8 }).map((_, index) => (
              <div key={index} className="border-border flex items-center gap-4 border-b px-4 py-3">
                <Bone className="h-4 w-24" />
                <Bone className="h-4 w-16" />
                <Bone className="h-4 min-w-0 flex-1" />
              </div>
            ))}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

export function InstanceMetricsSkeleton() {
  return <MetricsSkeleton testId="compute-plugin-loading-instance-metrics" />;
}

export function InstanceShellWindowSkeleton() {
  return (
    <div
      aria-busy="true"
      className="flex flex-col gap-4"
      data-testid="compute-plugin-loading-instance-shell">
      <Bone className="h-4 w-72" />
      <Bone className="w-full" style={{ height: '60vh' }} />
    </div>
  );
}
