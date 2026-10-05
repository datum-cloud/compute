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
import { ColumnEmpty, ColumnSkeleton, COLUMN_ROW_CLASS } from './home-column-parts';
import { ComputeEnablementBanner } from '../components/compute-enablement-banner';
import { useComputeEntitlement, useWorkloads } from '../lib/api';
import { ownPluginHref, useOwnPluginSlug } from '../lib/plugin-slug';
import {
  HEALTH_DOT_CLASS,
  homeColumnWorkloads,
  statusLabel,
} from '../lib/workload-presenters';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { RocketIcon, ServerIcon, TriangleAlertIcon } from 'lucide-react';
import { Link, useParams } from 'react-router';

const HOME_COLUMN_LIMIT = 5;

const ServerTile = <Icon icon={ServerIcon} size={18} aria-hidden />;

export default function WorkloadsHomeColumn() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: entitlement, isLoading: entitlementLoading } = useComputeEntitlement(projectId);
  const enabled = entitlement?.phase === 'Active';
  const { data: workloads, isLoading: workloadsLoading, error } = useWorkloads(projectId, enabled);
  const { data: slug } = useOwnPluginSlug(projectId);

  if (!projectId || entitlementLoading) return <ColumnSkeleton label="Workloads" />;

  if (!enabled) {
    return (
      <ComputeEnablementBanner projectId={projectId} phase={entitlement?.phase ?? null} compact />
    );
  }

  if (workloadsLoading) return <ColumnSkeleton label="Workloads" />;

  if (error) {
    return (
      <ColumnEmpty icon={<Icon icon={TriangleAlertIcon} size={18} aria-hidden />}>
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
        icon={ServerTile}
        title="Deploy your first workload"
        action={
          root && (
            <LinkButton
              as={Link}
              href={root}
              type="primary"
              theme="solid"
              size="xs"
              icon={<Icon icon={RocketIcon} size={14} aria-hidden />}
            >
              Try the demo
            </LinkButton>
          )
        }
      >
        Run <code className="font-mono">datumctl compute deploy</code>, or launch a demo workload
        in one click.
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
              <span
                className={cn(
                  'size-2 rounded-full',
                  workload.deleting ? 'bg-muted-foreground' : HEALTH_DOT_CLASS[workload.health]
                )}
              />
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
