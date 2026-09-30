/**
 * `portal.column/project-home` contribution — the body of the Workloads
 * column on the project home page. The host draws the column heading (which
 * links to the Workloads page), the loading fallback and the error boundary;
 * this renders only what goes under the heading:
 *
 * - Compute not enabled, pending or declined: the compact enablement banner,
 *   the same states and "Enable Compute" request the Workloads page shows.
 * - Enabled with no workloads: the deploy command, plus a link to the
 *   one-click demo on the Workloads page.
 * - Enabled with workloads: up to five rows, unhealthy first, each linking to
 *   that workload's page.
 *
 * Rows mirror the host's own home columns (cloud-portal
 * `features/project/home/resource-column.tsx`) so the row of columns reads as
 * one piece.
 */
import { ComputeEnablementBanner } from '../components/compute-enablement-banner';
import { useComputeEntitlement, useWorkloads } from '../lib/api';
import { ownPluginHref, useOwnPluginSlug } from '../lib/plugin-slug';
import {
  HEALTH_DOT_CLASS,
  homeColumnWorkloads,
  statusLabel,
} from '../lib/workload-presenters';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { cn } from '@datum-cloud/datum-ui/utils';
import { ChevronRightIcon, RocketIcon } from 'lucide-react';
import { Link, useParams } from 'react-router';

const HOME_COLUMN_LIMIT = 5;

function ColumnSkeleton() {
  return (
    <div className="flex flex-col gap-1" role="status">
      <span className="sr-only">Loading workloads</span>
      {Array.from({ length: 3 }, (_, i) => (
        <Skeleton key={i} className="h-10 w-full rounded-md" />
      ))}
    </div>
  );
}

function ColumnMessage({ children }: { children: React.ReactNode }) {
  return (
    <div className="bg-muted/40 border-input flex min-h-24 flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-4 py-5 text-center">
      {children}
    </div>
  );
}

export default function WorkloadsHomeColumn() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: entitlement, isLoading: entitlementLoading } = useComputeEntitlement(projectId);
  const enabled = entitlement?.phase === 'Active';
  const { data: workloads, isLoading: workloadsLoading, error } = useWorkloads(projectId, enabled);
  const { data: slug } = useOwnPluginSlug(projectId);

  if (!projectId || entitlementLoading) return <ColumnSkeleton />;

  if (!enabled) {
    return (
      <ComputeEnablementBanner projectId={projectId} phase={entitlement?.phase ?? null} compact />
    );
  }

  if (workloadsLoading) return <ColumnSkeleton />;

  if (error) {
    return (
      <ColumnMessage>
        <p className="text-muted-foreground text-xs">
          {error.status === 403
            ? "You don't have access to workloads in this project."
            : "Couldn't load workloads. Try again in a moment."}
        </p>
      </ColumnMessage>
    );
  }

  const root = slug ? ownPluginHref(projectId, slug) : undefined;
  const all = workloads ?? [];

  if (all.length === 0) {
    return (
      <ColumnMessage>
        <p className="text-muted-foreground text-xs">No workloads in this project yet.</p>
        <code className="bg-background border-input rounded border px-2 py-1 font-mono text-xs">
          datumctl compute deploy -f workload.yaml
        </code>
        {root && (
          <Link
            to={`${root}?tryDemo=1`}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-xs transition-colors"
          >
            <Icon icon={RocketIcon} size={12} aria-hidden />
            Or try the demo
          </Link>
        )}
      </ColumnMessage>
    );
  }

  const rows = homeColumnWorkloads(all, HOME_COLUMN_LIMIT);

  return (
    <>
      <ul className="flex flex-col">
        {rows.map((workload) => {
          const content = (
            <>
              <span
                className={cn(
                  'size-2 shrink-0 rounded-full',
                  workload.deleting ? 'bg-muted-foreground' : HEALTH_DOT_CLASS[workload.health]
                )}
                aria-hidden
              />
              <span className="min-w-0 flex-1 truncate text-sm">{workload.name}</span>
              <span className="text-muted-foreground shrink-0 text-xs">
                {statusLabel(workload)}
              </span>
              <Icon
                icon={ChevronRightIcon}
                size={14}
                className="text-icon-quaternary group-hover:text-foreground shrink-0"
                aria-hidden
              />
            </>
          );
          const rowClass =
            'group flex min-h-10 items-center gap-2 rounded-md px-2 py-1.5 transition-colors';
          return (
            <li key={workload.uid}>
              {root ? (
                <Link
                  to={`${root}/${encodeURIComponent(workload.name)}`}
                  className={cn(rowClass, 'hover:bg-accent')}
                >
                  {content}
                </Link>
              ) : (
                <div className={rowClass}>{content}</div>
              )}
            </li>
          );
        })}
      </ul>
      {root && all.length > rows.length && (
        <Link
          to={root}
          className="text-muted-foreground hover:text-foreground px-2 text-xs transition-colors"
        >
          View all workloads
        </Link>
      )}
    </>
  );
}
