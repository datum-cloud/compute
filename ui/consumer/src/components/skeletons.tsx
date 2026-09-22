/**
 * Content-only loading placeholders. Pages keep breadcrumbs, titles, and tabs
 * mounted and swap just the data region for one of these.
 *
 * Panel heights that the host stylesheet does not compile (`27rem`, `32rem`,
 * chart `224px`) are inline styles, matching the live pages.
 */
import { Card, CardContent, CardFooter, CardHeader } from '@datum-cloud/datum-ui/card';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import type { CSSProperties } from 'react';

const PANEL_STYLE = { height: '27rem' } as const;
const EXPLORER_STYLE = { height: '32rem' } as const;
const CHART_STYLE = { height: 224 } as const;
const TOPOLOGY_STYLE = { height: '28rem' } as const;

const TABLE_COLUMNS = [
  'Name',
  'Status',
  'Activity',
  'CPU / Memory',
  'Instances',
  'Locations',
  'Load balancer',
  'Image',
  'Created',
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

function FleetSummarySkeleton() {
  return (
    <div className="grid grid-cols-2 gap-6 lg:grid-cols-4">
      {Array.from({ length: 4 }).map((_, index) => (
        <Card key={index} size="sm">
          <CardContent className="flex flex-col gap-2">
            <Bone className="h-3 w-16" />
            <Bone className="h-8 w-20" />
            <Bone className="h-10 w-full" />
          </CardContent>
        </Card>
      ))}
    </div>
  );
}

export function WorkloadListCardsSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-cards">
      <FleetSummarySkeleton />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {Array.from({ length: 4 }).map((_, index) => (
          <Card key={index} size="sm" sectioned className="overflow-hidden">
            <CardHeader size="sm" bordered>
              <Bone className="h-4 w-40" />
            </CardHeader>
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
            <CardFooter bordered>
              <Bone className="h-3 w-28" />
            </CardFooter>
          </Card>
        ))}
      </div>
    </div>
  );
}

export function WorkloadListTableSkeleton({ summary = true }: { summary?: boolean }) {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-table">
      {summary ? <FleetSummarySkeleton /> : null}
      <Bone className="h-9 w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border">
        <div className="bg-muted/40 flex gap-4 border-b px-4 py-3">
          {TABLE_COLUMNS.map((column) => (
            <span key={column} className="text-muted-foreground min-w-0 flex-1 truncate text-xs font-medium">
              {column}
            </span>
          ))}
        </div>
        {Array.from({ length: 6 }).map((_, row) => (
          <div key={row} className="flex items-center gap-4 border-b px-4 py-3 last:border-b-0">
            {TABLE_COLUMNS.map((column) => (
              <Bone key={column} className="h-4 min-w-0 flex-1" />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

export function WorkloadOverviewSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true" data-testid="compute-plugin-loading-workload-overview">
      <Card size="sm">
        <CardContent className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Bone className="h-9 w-9 rounded-full" />
            <div className="flex flex-col gap-2">
              <Bone className="h-4 w-36" />
              <Bone className="h-3 w-48" />
            </div>
          </div>
          <div className="flex gap-2">
            <Bone className="h-6 w-20" />
            <Bone className="h-6 w-28" />
          </div>
        </CardContent>
      </Card>
      <Panel style={TOPOLOGY_STYLE}>
        <Bone className="min-h-0 w-full flex-1" />
      </Panel>
      <div style={PANEL_STYLE}>
        <Panel style={PANEL_STYLE}>
          <Bone className="min-h-0 w-full flex-1" />
        </Panel>
      </div>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div style={PANEL_STYLE}>
          <Panel style={PANEL_STYLE}>
            <Bone className="min-h-0 w-full flex-1" />
          </Panel>
        </div>
        <div style={PANEL_STYLE}>
          <Panel style={PANEL_STYLE}>
            <Bone className="h-4 w-full" />
            <Bone className="h-4 w-full" />
            <Bone className="h-4 w-3/4" />
            <Bone className="h-4 w-2/3" />
          </Panel>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel>
          {Array.from({ length: 5 }).map((_, index) => (
            <Bone key={index} className="h-4 w-full" />
          ))}
        </Panel>
        <Panel>
          {Array.from({ length: 5 }).map((_, index) => (
            <Bone key={index} className="h-4 w-full" />
          ))}
        </Panel>
      </div>
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
