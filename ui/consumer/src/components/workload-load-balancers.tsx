/**
 * The workload Overview's Load balancers card: one row per load balancer
 * whose rules send traffic to this workload, with its state, hostname and
 * the routes that reach the workload. Same card and table styling as the
 * Instances card.
 */
import { ensureWorkloadService, type AlbRoute, type ConnectedAlb, type PublishedUrl } from '../lib/api';
import type { Workload } from '../schema';
import type { AlbStatus } from '../lib/alb-status';
import { albOverviewHref } from '../lib/use-workload-overview-state';
import { albRowState, routePortText, type StatusTone } from '../lib/workload-presenters';
import { StatusBadge } from './detail-list';
import { LoadBalancerTableSkeleton } from './skeletons';
import { QuietNotice } from './surface';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { Button, LinkButton } from '@datum-cloud/datum-ui/button';
import { Card, CardHeader, CardTitle } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import { ArrowUpRightIcon, NetworkIcon, PlusIcon, SquareArrowOutUpRightIcon } from 'lucide-react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router';

// Column headers as the ALB Backends card (and our Instances card) sets them.
const HEAD = 'text-muted-foreground text-3xs h-9 font-semibold uppercase';
// Inline: the host compiles no arbitrary font size.
const PILL_STYLE = { fontSize: '11px' } as const;

const BADGE_TYPE: Record<StatusTone, 'success' | 'warning' | 'danger' | 'info'> = {
  ok: 'success',
  warn: 'warning',
  error: 'danger',
  muted: 'info',
};

/** The badge's one word; the full state goes in its tooltip. */
function badgeLabel(status: AlbStatus | undefined): string {
  if (!status || status.phase === 'ready') return 'Ready';
  return status.phase === 'error' ? 'Error' : 'Provisioning';
}

/** "[50% of] /api → :8080 (http) · shared with 1 other backend" */
function RouteLine({ route }: { route: AlbRoute }) {
  const port = routePortText(route.port);
  return (
    <span className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs">
      {route.percent !== undefined ? (
        <>
          <span className="bg-muted text-foreground rounded-full px-1.5 font-semibold" style={PILL_STYLE}>
            {route.percent}%
          </span>
          of
        </>
      ) : null}
      <span
        className="text-foreground font-mono"
        title={route.narrowed ? 'Also matches on headers, query or method' : undefined}>
        {route.paths.join(', ')}
        {route.narrowed ? '*' : null}
      </span>
      →
      {port ? <span className="text-foreground font-mono">{port}</span> : <span>this workload</span>}
      {route.sharedWith > 0 ? (
        <span>
          · shared with {route.sharedWith} other {route.sharedWith === 1 ? 'backend' : 'backends'}
        </span>
      ) : null}
    </span>
  );
}

function AlbRow({ alb, status, projectId }: { alb: ConnectedAlb; status?: AlbStatus; projectId?: string }) {
  const state = albRowState(status);
  const label = badgeLabel(status);
  const href = projectId ? albOverviewHref(projectId, alb.proxyName) : undefined;
  const name = alb.displayName || alb.proxyName;
  const badge = <StatusBadge type={BADGE_TYPE[state.tone]}>{label}</StatusBadge>;
  return (
    <TableRow data-testid="compute-plugin-workload-alb-row">
      <TableCell className="pl-(--card-px)">
        {href ? (
          <Link to={href} className="block truncate text-sm font-medium hover:underline" title={alb.proxyName}>
            {name}
          </Link>
        ) : (
          <span className="block truncate text-sm font-medium">{name}</span>
        )}
      </TableCell>
      <TableCell>
        {state.label !== label ? (
          <Tooltip message={state.label}>
            <span tabIndex={0} className="inline-flex cursor-help" aria-label={`${label}: ${state.label}`}>
              {badge}
            </span>
          </Tooltip>
        ) : (
          badge
        )}
      </TableCell>
      <TableCell className="hidden md:table-cell">
        {alb.hostname ? (
          <span className="flex min-w-0 items-center gap-1.5">
            <a
              href={`https://${alb.hostname}`}
              target="_blank"
              rel="noopener noreferrer"
              className="flex min-w-0 items-center gap-1.5 font-mono text-xs hover:underline">
              <span className="truncate">{alb.hostname}</span>
              <Icon icon={SquareArrowOutUpRightIcon} size={12} className="text-muted-foreground shrink-0" aria-hidden />
            </a>
            {alb.customHostnames.length > 0 ? (
              <Tooltip message={alb.customHostnames.join(', ')}>
                <span tabIndex={0} className="text-muted-foreground shrink-0 cursor-help text-xs">
                  +{alb.customHostnames.length} more
                </span>
              </Tooltip>
            ) : null}
          </span>
        ) : (
          <span className="text-muted-foreground text-xs">No hostname yet</span>
        )}
      </TableCell>
      <TableCell>
        <span className="flex flex-col gap-1">
          {alb.routes.length > 0 ? (
            alb.routes.map((route, index) => <RouteLine key={index} route={route} />)
          ) : (
            // Labelled to the workload, with no service backend to read routes from.
            <span className="text-muted-foreground text-xs">All traffic</span>
          )}
        </span>
      </TableCell>
      <TableCell className="pr-(--card-px)">
        {href ? (
          <span className="flex justify-end">
            <Tooltip message="Open load balancer">
              <span className="inline-flex">
                <LinkButton
                  as={Link}
                  href={href}
                  type="quaternary"
                  theme="borderless"
                  size="xs"
                  aria-label={`Open load balancer ${name}`}
                  icon={<Icon icon={ArrowUpRightIcon} size={14} aria-hidden />}
                />
              </span>
            </Tooltip>
          </span>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

/**
 * Opens the portal's new load balancer dialog with this workload chosen. The
 * dialog can only choose a workload with a NetworkService, so one is made
 * first when it has none; if that fails the dialog still opens and says why.
 */
function AddToLoadBalancer({ projectId, workload }: { projectId: string; workload: Workload }) {
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const open = async () => {
    setBusy(true);
    try {
      await ensureWorkloadService(projectId, workload);
    } catch {
      // The dialog explains a workload it can't choose.
    }
    void navigate(`/project/${projectId}/alb?action=create&workload=${encodeURIComponent(workload.name)}`);
  };
  return (
    <Button
      type="quaternary"
      theme="outline"
      size="xs"
      loading={busy}
      onClick={() => void open()}
      icon={<Icon icon={PlusIcon} size={12} aria-hidden />}>
      Add to a load balancer
    </Button>
  );
}

export function WorkloadLoadBalancers({
  projectId,
  published,
  loading,
  albStatuses,
  network,
  workload,
}: {
  projectId?: string;
  /** Chosen in the portal's create dialog. */
  workload: Workload;
  published?: PublishedUrl | null;
  /** The load balancer lookup hasn't answered yet. */
  loading: boolean;
  albStatuses: Record<string, AlbStatus>;
  /** The workload's network, for saying where it is reachable without one. */
  network?: string;
}) {
  const albs = published?.proxies ?? [];
  return (
    <Card sectioned size="sm" className="overflow-hidden" data-testid="compute-plugin-workload-load-balancers">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={NetworkIcon} size={16} className="text-secondary" aria-hidden />
          Load balancers
          {!loading && albs.length > 0 ? (
            <Badge type="muted" theme="solid" className="text-3xs h-5 rounded-md px-1.5 tabular-nums">
              {albs.length}
            </Badge>
          ) : null}
        </CardTitle>
      </CardHeader>
      {loading ? (
        <LoadBalancerTableSkeleton />
      ) : albs.length === 0 ? (
        <div className="px-2 py-2">
          <QuietNotice
            testId="compute-plugin-workload-load-balancers-empty"
            title={
              network
                ? `No load balancer sends traffic to this workload. It's reachable only on ${network}.`
                : 'No load balancer sends traffic to this workload.'
            }
            action={
              projectId ? (
                <AddToLoadBalancer projectId={projectId} workload={workload} />
              ) : undefined
            }
          />
        </div>
      ) : (
        <Table>
          <TableHeader className="bg-muted/40">
            <TableRow className="hover:bg-transparent">
              <TableHead className={cn(HEAD, 'pl-(--card-px)')}>Load balancer</TableHead>
              <TableHead className={HEAD}>State</TableHead>
              <TableHead className={cn(HEAD, 'hidden md:table-cell')}>Hostname</TableHead>
              <TableHead className={HEAD}>Routes</TableHead>
              <TableHead className="pr-(--card-px)">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {albs.map((alb) => (
              <AlbRow key={alb.proxyName} alb={alb} status={albStatuses[alb.proxyName]} projectId={projectId} />
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}
