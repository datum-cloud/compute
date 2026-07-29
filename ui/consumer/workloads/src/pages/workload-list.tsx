/**
 * `portal.page/project` extension at `workloads`, exposed as `WorkloadList`.
 *
 * Ported from cloud-portal PR #1315's
 * `app/routes/project/detail/compute/workloads/index.tsx`. Rewritten for the
 * plugin runtime:
 *  - `useWorkloads` now polls `/api/proxy/…` directly (see `lib/api.ts`)
 *    instead of PR #1315's generated-SDK service + `useResourceWatch`.
 *  - No server loader — table rows come straight from the query; loading /
 *    error / restricted / empty states are handled inline instead of via
 *    `defineResourceRoute`/`runListLoader`.
 *  - Row navigation uses a plain relative `Link`/`useNavigate` instead of the
 *    portal's `paths.config.ts` + `getPathWithParams`.
 *  - The portal's internal `Table` component isn't available to plugins, so
 *    this renders a plain semantic `<table>` (matching the shape of PR
 *    #1315's own instances table).
 */
import { CliBanner, SectionCard } from '../components/cli-section';
import { StatStrip } from '../components/stat-strip';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useWorkloads } from '../lib/api';
import { workloadHealthToBadgeType, type Workload, type WorkloadHealth } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { cn } from '@datum-cloud/datum-ui/utils';
import { formatDistanceToNowStrict } from 'date-fns';
import { ArrowRightIcon, HomeIcon, RocketIcon, SearchIcon } from 'lucide-react';
import { useLocation, useNavigate, useParams } from 'react-router';

const HEALTH_DOT_CLASS: Record<WorkloadHealth, string> = {
  Available: 'bg-green-500',
  Degraded: 'bg-yellow-500',
  Unavailable: 'bg-red-500',
  Unknown: 'bg-muted-foreground',
};

function FleetSummary({ workloads }: { workloads: Workload[] }) {
  const readyInstances = workloads.reduce((sum, w) => sum + w.currentReplicas, 0);
  const desiredInstances = workloads.reduce((sum, w) => sum + w.desiredReplicas, 0);
  const healthy = workloads.filter((w) => w.health === 'Available').length;
  const degraded = workloads.filter((w) => w.health === 'Degraded').length;
  const unavailable = workloads.filter(
    (w) => w.health === 'Unavailable' || w.health === 'Unknown'
  ).length;

  const stats: { label: string; value: string; className?: string }[] = [
    { label: 'Workloads', value: String(workloads.length) },
    { label: 'Instances', value: `${readyInstances}/${desiredInstances}` },
    {
      label: 'Healthy',
      value: String(healthy),
      className: healthy > 0 ? 'text-green-600 dark:text-green-500' : undefined,
    },
    {
      label: 'Degraded',
      value: String(degraded),
      className: degraded > 0 ? 'text-yellow-600 dark:text-yellow-500' : undefined,
    },
    {
      label: 'Unavailable',
      value: String(unavailable),
      className: unavailable > 0 ? 'text-red-600 dark:text-red-500' : undefined,
    },
  ];

  return <StatStrip stats={stats} testId="compute-plugin-fleet-summary" />;
}

function WorkloadCard({ workload, onClick }: { workload: Workload; onClick: () => void }) {
  return (
    <div
      className="border-card-border bg-card hover:border-foreground/20 flex cursor-pointer flex-col gap-4 rounded-xl border p-5 shadow transition-colors"
      onClick={onClick}
      data-testid="compute-plugin-workload-card">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h3 className="truncate font-semibold">{workload.name}</h3>
            {workload.runtimeType && (
              <span className="bg-muted text-muted-foreground shrink-0 rounded-full px-2 py-0.5 text-xs">
                {workload.runtimeType}
              </span>
            )}
          </div>
          {workload.image && (
            <p className="text-muted-foreground mt-0.5 truncate font-mono text-xs">
              {workload.image}
            </p>
          )}
        </div>
        <Badge type={workloadHealthToBadgeType(workload.health)} theme="light" className="shrink-0">
          {workload.health}
        </Badge>
      </div>

      <div className="border-border grid grid-cols-2 gap-3 border-t pt-4">
        <div>
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            Ready
          </p>
          <p className="mt-0.5 text-sm font-medium">
            {workload.currentReplicas}/{workload.desiredReplicas}
          </p>
        </div>
        <div>
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">Age</p>
          <p className="mt-0.5 text-sm font-medium">
            {formatDistanceToNowStrict(workload.createdAt, { addSuffix: true })}
          </p>
        </div>
      </div>

      {workload.placements.length > 0 && (
        <div className="border-border flex flex-col gap-1.5 border-t pt-4">
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            Placements
          </p>
          {workload.placements.map((placement) => (
            <div key={placement} className="flex items-center gap-2 text-sm">
              <span
                className={cn('size-1.5 shrink-0 rounded-full', HEALTH_DOT_CLASS[workload.health])}
              />
              <span>{placement}</span>
            </div>
          ))}
        </div>
      )}

      <div className="border-border text-muted-foreground flex items-center justify-between border-t pt-3 text-xs">
        <span>Created {workload.createdAt.toLocaleDateString()}</span>
        <span className="flex items-center gap-1">
          View workload
          <ArrowRightIcon className="size-3" />
        </span>
      </div>
    </div>
  );
}

export default function WorkloadList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const { data: workloads, isLoading, error, refetch } = useWorkloads(projectId);

  // Build the child route path from the current URL rather than the portal's
  // internal `paths.config.ts` (unavailable to plugins) — the host mounts this
  // page at `/project/:projectId/services/:serviceSlug/workloads`.
  const basePath = location.pathname.replace(/\/$/, '');
  const workloadHref = (name: string) => `${basePath}/${name}`;
  const projectHref = projectId ? `/project/${projectId}` : '/';

  return (
    <div data-testid="compute-plugin-workload-list" className="flex flex-col gap-4 p-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <HomeIcon className="size-4" />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>Workloads</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title="Workloads"
        description={
          <>
            Read-only operational view of your project&apos;s compute workloads. Workloads are
            created and managed with <code>datumctl</code>.
          </>
        }
      />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view workloads."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && (workloads?.length ?? 0) === 0 && (
        <div className="flex flex-col gap-6" data-testid="compute-plugin-workload-empty">
          <CliBanner
            title="Deploy workloads with datumctl"
            description="Workloads are created and managed using the Datum CLI. Install datumctl, write a manifest, and deploy — workloads you create will appear here automatically."
          />
          <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
            <SectionCard
              icon={<RocketIcon className="size-4" />}
              title="Deploy a workload"
              description="Create a workload manifest and deploy it to your project. The dashboard will reflect the new workload within seconds."
              commands={[
                'datumctl compute deploy -f workload.yaml',
                `datumctl compute deploy --project=${projectId ?? ''} -f workload.yaml`,
              ]}
            />
            <SectionCard
              icon={<SearchIcon className="size-4" />}
              title="List & inspect workloads"
              description="Confirm your workload deployed successfully and inspect its current health and placement status."
              commands={[
                'datumctl compute workloads list',
                'datumctl compute workloads describe <name>',
              ]}
            />
          </div>
        </div>
      )}

      {!isLoading && !error && workloads && workloads.length > 0 && (
        <>
          <FleetSummary workloads={workloads} />
          <div
            className="grid grid-cols-1 gap-4 lg:grid-cols-2"
            data-testid="compute-plugin-workload-grid">
            {workloads.map((workload) => (
              <WorkloadCard
                key={workload.uid || workload.name}
                workload={workload}
                onClick={() => navigate(workloadHref(workload.name))}
              />
            ))}
          </div>
        </>
      )}
    </div>
  );
}
