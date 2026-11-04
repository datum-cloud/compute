/**
 * The workload Overview's Load balancers section: one row per load balancer
 * whose rules send traffic to this workload. The hostname belongs to the load
 * balancer, so it shows here, once per load balancer, and nowhere else on the
 * page: the workload is a backend, not something with a URL of its own.
 */
import type { AlbRoute, ConnectedAlb, PublishedUrl } from '../lib/api';
import type { AlbStatus } from '../lib/alb-status';
import { albOverviewHref } from '../lib/use-workload-overview-state';
import { albRowState, routePortText, STATUS_TONE_COLOR } from '../lib/workload-presenters';
import { QuietNotice, SectionFrame } from './surface';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Skeleton } from '@datum-cloud/datum-ui/skeleton';
import { ArrowRightIcon, ArrowUpRightIcon, PlusIcon, SquareArrowOutUpRightIcon } from 'lucide-react';
import { Link } from 'react-router';

// Inline: the host compiles none of these arbitrary sizes.
const IDENTITY_STYLE = { width: '14rem' } as const;
const PILL_STYLE = { fontSize: '11px' } as const;
const DASHED_STYLE = { borderStyle: 'dashed' } as const;

/** "routes [50% of] /api → this workload on :8080 (http) · shared with 1 other backend" */
function RouteLine({ route }: { route: AlbRoute }) {
  const port = routePortText(route.port);
  return (
    <span className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs">
      routes
      {route.percent !== undefined ? (
        <>
          <span className="bg-muted text-foreground rounded-full px-1.5 font-semibold" style={PILL_STYLE}>
            {route.percent}%
          </span>
          of
        </>
      ) : null}
      <span className="text-foreground font-mono" title={route.narrowed ? 'Also matches on headers, query or method' : undefined}>
        {route.paths.join(', ')}
        {route.narrowed ? '*' : null}
      </span>
      → this workload
      {port ? (
        <>
          {' '}on <span className="text-foreground font-mono">{port}</span>
        </>
      ) : null}
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
  const href = projectId ? albOverviewHref(projectId, alb.proxyName) : undefined;
  const name = alb.displayName || alb.proxyName;
  return (
    <li
      className="flex flex-wrap items-center gap-x-5 gap-y-2 border-t px-4 py-3 first:border-t-0"
      data-testid="compute-plugin-workload-alb-row">
      <span className="flex min-w-0 shrink-0 flex-col gap-1" style={IDENTITY_STYLE}>
        <span className="flex min-w-0 items-center gap-2">
          <span className="size-2 shrink-0 rounded-full" style={{ background: STATUS_TONE_COLOR[state.tone] }} aria-hidden />
          {href ? (
            <Link to={href} className="text-primary truncate text-sm font-medium hover:underline">
              {name}
            </Link>
          ) : (
            <span className="truncate text-sm font-medium">{name}</span>
          )}
        </span>
        <span className="text-muted-foreground truncate pl-4 text-xs" title={state.label}>
          {state.label}
        </span>
      </span>
      <Icon icon={ArrowRightIcon} size={16} className="text-muted-foreground hidden shrink-0 sm:block" aria-hidden />
      <span className="flex min-w-0 flex-1 flex-col gap-1.5">
        {alb.hostname ? (
          <span className="flex min-w-0 items-center gap-1.5">
            <a
              href={`https://${alb.hostname}`}
              target="_blank"
              rel="noopener noreferrer"
              className="text-foreground flex min-w-0 items-center gap-1.5 font-mono text-xs hover:underline">
              <span className="truncate">{alb.hostname}</span>
              <Icon icon={SquareArrowOutUpRightIcon} size={12} className="text-muted-foreground shrink-0" aria-hidden />
            </a>
            {alb.customHostnames.length > 0 ? (
              <span className="text-muted-foreground shrink-0 text-xs" title={alb.customHostnames.join('\n')}>
                +{alb.customHostnames.length} more
              </span>
            ) : null}
          </span>
        ) : null}
        {alb.routes.length > 0 ? (
          alb.routes.map((route, index) => <RouteLine key={index} route={route} />)
        ) : (
          // Labelled to the workload with no service backend to read routes from.
          <span className="text-muted-foreground text-xs">sends traffic to this workload</span>
        )}
      </span>
      {href ? (
        <LinkButton
          as={Link}
          href={href}
          type="quaternary"
          theme="outline"
          size="xs"
          icon={<Icon icon={ArrowUpRightIcon} size={12} aria-hidden />}
          iconPosition="right"
          aria-label={`Open load balancer ${name}`}>
          Open load balancer
        </LinkButton>
      ) : null}
    </li>
  );
}

export function WorkloadLoadBalancers({
  projectId,
  published,
  loading,
  albStatuses,
  network,
}: {
  projectId?: string;
  published?: PublishedUrl | null;
  /** The load balancer lookup hasn't answered yet. */
  loading: boolean;
  albStatuses: Record<string, AlbStatus>;
  /** The workload's network, for saying where it is reachable without one. */
  network?: string;
}) {
  const albs = published?.proxies ?? [];
  return (
    <SectionFrame
      title="Load balancers"
      count={albs.length}
      isLoading={loading}
      testId="compute-plugin-workload-load-balancers">
      {loading ? (
        <Skeleton className="h-16 w-full rounded-lg" />
      ) : albs.length === 0 ? (
        <div className="rounded-lg border" style={DASHED_STYLE}>
          <QuietNotice
            className="px-4"
            title={
              network
                ? `No load balancer sends traffic to this workload. It's reachable only on ${network}.`
                : 'No load balancer sends traffic to this workload.'
            }
            action={
              projectId ? (
                <LinkButton
                  as={Link}
                  href={`/project/${projectId}/alb?action=create`}
                  type="quaternary"
                  theme="outline"
                  size="xs"
                  icon={<Icon icon={PlusIcon} size={12} aria-hidden />}>
                  Add to a load balancer
                </LinkButton>
              ) : undefined
            }
          />
        </div>
      ) : (
        <>
          <p className="text-muted-foreground text-xs">Load balancers that send traffic to this workload.</p>
          <ul className="bg-card overflow-hidden rounded-lg border">
            {albs.map((alb) => (
              <AlbRow key={alb.proxyName} alb={alb} status={albStatuses[alb.proxyName]} projectId={projectId} />
            ))}
          </ul>
        </>
      )}
    </SectionFrame>
  );
}
