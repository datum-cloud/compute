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
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useWorkloads } from '../lib/api';
import { workloadHealthToBadgeType, type Workload } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { RocketIcon, SearchIcon } from 'lucide-react';
import { useLocation, useNavigate, useParams } from 'react-router';

function WorkloadRow({ workload, onClick }: { workload: Workload; onClick: () => void }) {
  return (
    <tr
      className="border-border hover:bg-muted/50 cursor-pointer border-t transition-colors"
      onClick={onClick}
      data-testid="compute-plugin-workload-row">
      <td className="px-5 py-3">
        <span className="font-medium">{workload.name}</span>
      </td>
      <td className="px-5 py-3">
        {workload.image ? (
          <span className="text-muted-foreground max-w-xs truncate font-mono text-sm">
            {workload.image}
          </span>
        ) : (
          <span className="text-muted-foreground text-sm">—</span>
        )}
      </td>
      <td className="px-5 py-3">
        <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
          {workload.health}
        </Badge>
      </td>
      <td className="px-5 py-3 text-sm">
        {workload.currentReplicas}/{workload.desiredReplicas}
      </td>
      <td className="px-5 py-3 text-sm">
        {workload.placements.length ? (
          workload.placements.join(', ')
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className="px-5 py-3 text-sm">{workload.createdAt.toLocaleDateString()}</td>
    </tr>
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

  return (
    <div data-testid="compute-plugin-workload-list" className="flex flex-col gap-6 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Workloads</h1>
        <p className="text-muted-foreground mt-1 text-sm">
          Read-only operational view of your project&apos;s compute workloads. Workloads are
          created and managed with <code>datumctl</code>.
        </p>
      </div>

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
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full text-sm" data-testid="compute-plugin-workload-table">
            <thead>
              <tr className="border-border border-b">
                {['Name', 'Image', 'Health', 'Ready', 'Placements', 'Age'].map((h) => (
                  <th
                    key={h}
                    className="text-muted-foreground px-5 py-2.5 text-left text-xs font-medium tracking-wide uppercase">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {workloads.map((workload) => (
                <WorkloadRow
                  key={workload.uid || workload.name}
                  workload={workload}
                  onClick={() => navigate(workloadHref(workload.name))}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
