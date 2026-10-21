/**
 * `portal.column/project-home` contribution — the body of the Workloads
 * column on the project home page. The host draws the column heading (which
 * links to the Workloads page), the loading fallback and the error boundary;
 * this renders only what goes under the heading:
 *
 * - Compute not enabled yet: enables it (self-service) behind the skeleton,
 *   as the Workloads page does.
 * - No workloads: the deploy command, plus a link to the
 *   one-click demo on the Workloads page.
 * - Workloads: up to five rows, unhealthy first, each linking to
 *   that workload's page.
 *
 * Rows mirror the host's own home columns (cloud-portal
 * `features/project/home/resource-column.tsx`) so the row of columns reads as
 * one piece.
 */
import { ColumnEmpty, ColumnSkeleton, COLUMN_ROW_CLASS } from './home-column-parts';
import { useComputeEnablement, useWorkloads } from '../lib/api';
import { ownPluginHref, useOwnPluginSlug } from '../lib/plugin-slug';
import { HealthDot } from '../components/workload-status-badge';
import { useNow } from '../lib/use-now';
import { deployStatus, homeColumnWorkloads, statusLabel } from '../lib/workload-presenters';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { RocketIcon, TriangleAlertIcon } from 'lucide-react';
import { Link, useParams } from 'react-router';

const HOME_COLUMN_LIMIT = 5;

export default function WorkloadsHomeColumn() {
  const { projectId } = useParams<{ projectId: string }>();
  const enablement = useComputeEnablement(projectId);
  const { enabled } = enablement;
  const { data: workloads, isLoading: workloadsLoading, error } = useWorkloads(projectId, enabled);
  const { data: slug } = useOwnPluginSlug(projectId);
  const now = useNow(!!workloads?.some((w) => w.health === 'Deploying' && !w.deleting), 5_000);

  if (!projectId || enablement.isLoading || (enabled && workloadsLoading)) {
    return <ColumnSkeleton label="Workloads" />;
  }

  if (enablement.error) {
    return (
      <ColumnEmpty icon={<Icon icon={TriangleAlertIcon} size={14} aria-hidden />}>
        {enablement.error.status === 403
          ? "You don't have permission to enable Compute for this project."
          : "Compute isn't available right now."}
      </ColumnEmpty>
    );
  }

  if (error) {
    return (
      <ColumnEmpty icon={<Icon icon={TriangleAlertIcon} size={14} aria-hidden />}>
        {error.status === 403
          ? "You don't have access to workloads in this project."
          : "Workloads aren't available right now."}
      </ColumnEmpty>
    );
  }

  const root = slug ? ownPluginHref(projectId, slug) : undefined;
  const all = workloads ?? [];

  if (all.length === 0) {
    return (
      <ColumnEmpty
        action={
          root && (
            <LinkButton
              as={Link}
              href={root}
              type="secondary"
              theme="outline"
              size="xs"
              icon={<Icon icon={RocketIcon} size={14} aria-hidden />}
            >
              Try the demo
            </LinkButton>
          )
        }
      >
        No workloads yet
      </ColumnEmpty>
    );
  }

  const rows = homeColumnWorkloads(all, HOME_COLUMN_LIMIT);

  return (
    <ul className="flex flex-col">
      {rows.map((workload) => {
        const content = (
          <>
            <span className="flex size-3.5 shrink-0 items-center justify-center" aria-hidden>
              {workload.deleting ? (
                <span className="bg-muted-foreground size-2 rounded-full" />
              ) : (
                <HealthDot health={workload.health} deploy={deployStatus(workload, now)} />
              )}
            </span>
            <span className="min-w-0 flex-1 truncate text-sm">{workload.name}</span>
            <span className="text-muted-foreground shrink-0 text-xs">
              {statusLabel(workload)}
            </span>
          </>
        );
        return (
          <li key={workload.uid}>
            {root ? (
              <Link
                to={`${root}/${encodeURIComponent(workload.name)}`}
                className={cn(COLUMN_ROW_CLASS, 'hover:bg-accent transition-colors')}
              >
                {content}
              </Link>
            ) : (
              <div className={COLUMN_ROW_CLASS}>{content}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}
