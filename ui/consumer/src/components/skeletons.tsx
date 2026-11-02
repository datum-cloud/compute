/**
 * Content-only loading placeholders. Pages keep breadcrumbs, titles, and tabs
 * mounted and swap just the data region for one of these.
 *
 * Panel heights that the host stylesheet does not compile (`27rem`, `32rem`,
 * chart `224px`) are inline styles, matching the live pages.
 */
import { DetailList, DETAILS_GRID_CLASS, OPEN_ROW_PADDING } from './detail-list';
import { Card, CardContent, CardHeader } from '@datum-cloud/datum-ui/card';
import { STRIP_GAP } from './stat-strip';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import type { CSSProperties } from 'react';

const PANEL_STYLE = { height: '27rem' } as const;
const EXPLORER_STYLE = { height: '32rem' } as const;
const CHART_STYLE = { height: 224 } as const;
const TOPOLOGY_STYLE = { height: '28rem' } as const;

/**
 * The list table's columns before usage data arrives. CPU and Memory only
 * appear once a row has data, so the skeleton leaves them out.
 */
const TABLE_COLUMNS = ['Name', 'Status', 'Network', 'Locations'] as const;

/** Same thin line the live table draws (`TABLE_STYLES` in workload-table). */
const ROW_LINE_STYLE = {
  borderBottom: '1px solid color-mix(in oklab, var(--border) 60%, transparent)',
} as const;

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

/**
 * Open table like the live one: plain muted header, `h-10` rows, thin lines.
 * `summary={false}` is the in-page fallback, where the strip and toolbar are
 * already on screen.
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
      <div>
        <div className="flex h-10 items-center gap-4 px-2" style={ROW_LINE_STYLE}>
          {TABLE_COLUMNS.map((column) => (
            <span key={column} className="text-muted-foreground min-w-0 flex-1 truncate text-xs font-medium">
              {column}
            </span>
          ))}
        </div>
        {Array.from({ length: 6 }).map((_, row) => (
          <div key={row} className="flex h-10 items-center gap-4 px-2" style={ROW_LINE_STYLE}>
            {TABLE_COLUMNS.map((column) => (
              <Bone key={column} className="h-4 min-w-0 flex-1" />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

/** Placeholder instance rows, the same `h-10` as `WorkloadInstances` rows. */
export function InstanceRowsSkeleton({ rows = 2 }: { rows?: number }) {
  return (
    <div className="flex flex-col" aria-hidden>
      {Array.from({ length: rows }).map((_, index) => (
        <div key={index} className="flex h-10 items-center gap-4 px-2">
          <Bone className="size-2 shrink-0 rounded-full" />
          <Bone className="h-3 w-40" />
          <Bone className="h-3 w-24" />
          <Bone className="h-3 flex-1" />
        </div>
      ))}
    </div>
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
      <div className="flex h-9 items-center gap-3 px-1">
        <Bone className="size-4 shrink-0 rounded-full" />
        <Bone className="h-3 w-48" />
      </div>
      <div className="flex flex-col gap-2">
        <SectionHeaderBone />
        <InstanceRowsSkeleton />
      </div>
      <div className="flex flex-col gap-2">
        <SectionHeaderBone action />
        <Bone className="min-h-48 w-full rounded-md" />
      </div>
      <div className="flex flex-col gap-2">
        <SectionHeaderBone />
        {/* The real rows, with bones for text, so heights and dividers match at every width. */}
        <div style={OPEN_ROW_PADDING}>
          <DetailList
            className={DETAILS_GRID_CLASS}
            itemClassName="py-2"
            items={Array.from({ length: 6 }, () => ({
              label: <Bone className="h-3 w-24" />,
              content: <Bone className="h-3 w-32" />,
            }))}
          />
        </div>
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
