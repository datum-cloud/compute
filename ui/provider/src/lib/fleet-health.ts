/**
 * Data-fetching for the provider plugin's fleet-wide views — the Overview
 * override and the Workloads tab on staff-portal's
 * `/admin/service-catalog/compute` detail page (see
 * `../pages/service-overview.tsx` and `../pages/fleet-workloads.tsx`).
 *
 * Unlike the per-project Workload views (`api.ts`, proxying only compute's
 * own aggregated API), this page also needs two things staff-portal's host
 * already has and proxies same-origin, cookie-authed, under the viewing
 * staff member's own session — no plugin-owned backend, same architecture
 * as everything else in this plugin:
 *
 * - The `Service` resource (`services.miloapis.com`), to resolve compute's
 *   producer project and canonical service name — via the same
 *   `/api/internal/...` proxy `api.ts` uses, just not project-scoped.
 * - The service's consumer list, to know which projects to fan out to — via
 *   `/api/graphql` (staff-portal's session-cookie-authed GraphQL proxy; see
 *   `app/modules/graphql/service-consumers.ts` on the host for the
 *   equivalent query staff-portal's own Consumers tab runs).
 */
import { fetchWorkloads, proxyFetchAbsolute, ApiError, PLUGIN_ID } from './api';
import { fetchLocations, type Location } from './locations';
import type { Workload } from '../schema';
import { useQuery, useQueryClient, type QueryClient, type UseQueryResult } from '@tanstack/react-query';

const FLEET_QUERY_KEY = [PLUGIN_ID, 'fleet-health'];

/** Live-ish polling interval — matches the per-project views (`api.ts`). */
const REFETCH_INTERVAL_MS = 30_000;

/**
 * How long the resolved Service, consumer list and Locations catalog are
 * reused across polls. They change on the order of approvals and new regions,
 * not seconds, so only the per-project workload fan-out repeats every
 * {@link REFETCH_INTERVAL_MS}.
 */
const FLEET_SHAPE_STALE_TIME_MS = 5 * 60_000;

/** Bounded fan-out — see the "Fan-out cost" risk in the design doc. */
const MAX_CONCURRENT_PROJECT_FETCHES = 5;

interface RawServiceOwner {
  producerProjectRef?: { name?: string };
}

interface RawService {
  metadata?: { name?: string };
  spec?: {
    serviceName?: string;
    owner?: RawServiceOwner;
  };
}

interface ResolvedService {
  /** The Service's resource name, e.g. "compute". */
  resourceName: string;
  /** The canonical dotted name, e.g. "compute.datumapis.com". */
  canonicalName: string;
  producerProject: string;
}

async function fetchService(serviceResourceName: string): Promise<ResolvedService> {
  // `proxyFetchAbsolute` (not a bare `fetch`) so a 403 throws `ApiError`,
  // which `ErrorOrRestrictedState` needs to render the restricted-access
  // state instead of a generic failure card.
  const svc = await proxyFetchAbsolute<RawService>(
    `/apis/services.miloapis.com/v1alpha1/services/${encodeURIComponent(serviceResourceName)}`
  );
  const producerProject = svc.spec?.owner?.producerProjectRef?.name;
  if (!producerProject) {
    throw new Error(`Service "${serviceResourceName}" has no producer project recorded`);
  }
  return {
    resourceName: svc.metadata?.name ?? serviceResourceName,
    canonicalName: svc.spec?.serviceName ?? serviceResourceName,
    producerProject,
  };
}

interface RawServiceConsumer {
  name: string;
  phase: string | null;
  consumerProject: {
    name: string;
    displayName: string;
    /** Empty when the gateway couldn't read the project. */
    organizationName: string;
    organizationDisplayName: string;
  };
}

/**
 * Same `serviceConsumers` query staff-portal's own Consumers tab runs
 * (`app/modules/graphql/service-consumers.ts`), issued directly against the
 * host's cookie-authed `/api/graphql` proxy — no generated client available
 * to plugin code, so this is a plain, hand-written GraphQL request.
 */
async function postGraphQL<T>(query: string, variables: Record<string, unknown>): Promise<T> {
  const res = await fetch('/api/graphql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ query, variables }),
  });
  if (!res.ok) {
    // `/api/graphql` isn't the envelope-wrapped `/api/internal` proxy, so
    // this can't reuse `proxyFetchAbsolute` — but it throws the same
    // `ApiError` shape for the same reason: a 403 here (no access to
    // `serviceConsumers`/`project` for this producer project) needs to
    // render as "restricted", not a generic failure.
    throw new ApiError(res.status, `GraphQL request failed (${res.status})`);
  }
  const body = (await res.json()) as { data?: T; errors?: { message: string }[] };
  if (body.errors?.length) {
    throw new Error(body.errors[0]?.message ?? 'GraphQL request failed');
  }
  return body.data as T;
}

/**
 * `serviceNames` makes the gateway filter by service before it enriches each
 * row with project and organization lookups. Without it the gateway resolved
 * every consumer of every service the producer project owns (805 rows for
 * datum-cloud, 36 of them compute) and took ~2.5s doing it. The consumer's
 * `serviceRef` may hold either the Service's resource name or its canonical
 * name, so both are passed.
 */
async function fetchServiceConsumers(
  producerProject: string,
  serviceNames: string[]
): Promise<RawServiceConsumer[]> {
  const data = await postGraphQL<{ serviceConsumers?: RawServiceConsumer[] }>(
    `
      query FleetHealthConsumers($producerProject: ID!, $serviceNames: [String!]) {
        serviceConsumers(producerProject: $producerProject, serviceNames: $serviceNames) {
          name
          phase
          consumerProject { name displayName organizationName organizationDisplayName }
        }
      }
    `,
    { producerProject, serviceNames }
  );
  return data.serviceConsumers ?? [];
}

export interface FleetConsumerOrganization {
  name: string;
  displayName: string;
}

export interface FleetConsumerProject {
  name: string;
  displayName: string;
  /** Undefined when the owning organization couldn't be resolved — the row still renders, just without an org link. */
  organization?: FleetConsumerOrganization;
}

/** A workload across the fleet, with the consumer project it belongs to attached. */
export interface FleetWorkload {
  project: FleetConsumerProject;
  workload: Workload;
  /** The `Available` condition's message (human-readable), falling back to its reason code or the health label — the closest thing to "why," without opening the workload. */
  message: string;
  /** When the workload last transitioned to its current `Available` status — falls back to `createdAt` if never observed. */
  statusSince: Date;
}

/** A consumer project whose workload list couldn't be fetched — rendered, not dropped. */
export interface FailedProject {
  project: FleetConsumerProject;
  error: string;
}

export interface FleetHealth {
  /** Every active consumer project that was queried. */
  consumerCount: number;
  totalWorkloads: number;
  healthyCount: number;
  /** Unhealthy counts by severity, for the stat strip — `Available` excluded. */
  severityCounts: Record<Exclude<Workload['health'], 'Available'>, number>;
  /** Every workload across the fleet, sorted worst-first (severity, then most-recently-changed). */
  workloads: FleetWorkload[];
  failed: FailedProject[];
  /**
   * The producer project's Locations catalog. Every consumer project sees the
   * same platform catalog, so this is fetched once instead of once per
   * consumer. Empty when it can't be listed; rows then show raw names.
   */
  locations: Location[];
}

/**
 * The `Available` condition's human-readable message, falling back to its
 * terse reason code and then the workload's own health label as a last
 * resort — every row gets *some* text, even one with no conditions reported
 * at all (and even a healthy one, if its `Available` condition carries no
 * message of its own).
 */
function messageFor(workload: Workload): string {
  const available = workload.conditions.find((c) => c.type === 'Available');
  return available?.message ?? available?.reason ?? workload.health;
}

/**
 * When the workload last transitioned to its current `Available` status
 * (healthy or not). Falls back to `createdAt` when there's no `Available`
 * condition to read a transition time from.
 */
function statusSinceFor(workload: Workload): Date {
  const available = workload.conditions.find((c) => c.type === 'Available');
  if (available?.lastTransitionTime) {
    const parsed = new Date(available.lastTransitionTime);
    // A malformed timestamp would otherwise produce `Invalid Date`, which
    // makes the sort comparator's `.getTime()` NaN (inconsistent ordering)
    // and `formatDistanceToNowStrict` throw — falling back to `createdAt`
    // keeps the row rendering instead of taking out the whole page.
    if (!Number.isNaN(parsed.getTime())) return parsed;
  }
  return workload.createdAt;
}

/** Runs `fn` over `items` with at most `limit` in flight at once. */
async function mapWithConcurrency<T, R>(
  items: T[],
  limit: number,
  fn: (item: T) => Promise<R>
): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let next = 0;
  async function worker() {
    while (true) {
      const i = next++;
      if (i >= items.length) return;
      results[i] = await fn(items[i]);
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

const SEVERITY_RANK: Record<Workload['health'], number> = {
  Unavailable: 0,
  Degraded: 1,
  Unknown: 2,
  Available: 3,
};

interface FleetShape {
  activeConsumerProjects: FleetConsumerProject[];
  locations: Location[];
}

/**
 * Who to fan out to, plus the Locations catalog. Everything here depends only
 * on the Service, so it runs once per {@link FLEET_SHAPE_STALE_TIME_MS} rather
 * than on every poll, and the catalog fetch overlaps the consumer query.
 */
async function fetchFleetShape(serviceResourceName: string): Promise<FleetShape> {
  const service = await fetchService(serviceResourceName);
  const serviceNames = [...new Set([service.resourceName, service.canonicalName])];

  const [consumers, locations] = await Promise.all([
    fetchServiceConsumers(service.producerProject, serviceNames),
    fetchLocations(service.producerProject).catch(() => [] as Location[]),
  ]);

  const activeConsumerProjects: FleetConsumerProject[] = consumers
    .filter((c) => c.phase === 'Active')
    .map(({ consumerProject: p }) => ({
      name: p.name,
      displayName: p.displayName,
      ...(p.organizationName && {
        organization: {
          name: p.organizationName,
          displayName: p.organizationDisplayName || p.organizationName,
        },
      }),
    }));

  return { activeConsumerProjects, locations };
}

async function fetchFleetHealth(
  queryClient: QueryClient,
  serviceResourceName: string
): Promise<FleetHealth> {
  const { activeConsumerProjects, locations } = await queryClient.fetchQuery({
    queryKey: [FLEET_QUERY_KEY, 'shape', serviceResourceName],
    queryFn: () => fetchFleetShape(serviceResourceName),
    staleTime: FLEET_SHAPE_STALE_TIME_MS,
    retry: false,
  });

  const outcomes = await mapWithConcurrency(
    activeConsumerProjects,
    MAX_CONCURRENT_PROJECT_FETCHES,
    async (project) => {
      try {
        const workloads = await fetchWorkloads(project.name);
        return { project, workloads, error: null as string | null };
      } catch (err) {
        return {
          project,
          workloads: [] as Workload[],
          error: err instanceof Error ? err.message : 'Failed to load workloads',
        };
      }
    }
  );

  const workloads: FleetWorkload[] = outcomes
    .flatMap((o) => o.workloads.map((workload) => ({ project: o.project, workload })))
    .map((w) => ({
      ...w,
      message: messageFor(w.workload),
      statusSince: statusSinceFor(w.workload),
    }))
    .sort((a, b) => {
      const bySeverity = SEVERITY_RANK[a.workload.health] - SEVERITY_RANK[b.workload.health];
      if (bySeverity !== 0) return bySeverity;
      // Within the same severity, the most recently changed workload is the
      // one worth looking at first — a workload that's been down for months
      // is stale, not an incident. See the design discussion this came out
      // of: raw health alone can't distinguish "new problem" from "known,
      // ignored problem."
      return b.statusSince.getTime() - a.statusSince.getTime();
    });

  const failed: FailedProject[] = outcomes
    .filter((o): o is typeof o & { error: string } => o.error !== null)
    .map((o) => ({ project: o.project, error: o.error }));

  const severityCounts: FleetHealth['severityCounts'] = {
    Unavailable: 0,
    Degraded: 0,
    Unknown: 0,
  };
  let healthyCount = 0;
  for (const w of workloads) {
    if (w.workload.health === 'Available') healthyCount++;
    else severityCounts[w.workload.health]++;
  }

  return {
    consumerCount: activeConsumerProjects.length,
    totalWorkloads: workloads.length,
    healthyCount,
    severityCounts,
    workloads,
    failed,
    locations,
  };
}

export function useFleetHealth(
  serviceResourceName: string | undefined
): UseQueryResult<FleetHealth, Error> {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: [FLEET_QUERY_KEY, serviceResourceName],
    enabled: !!serviceResourceName,
    queryFn: () => fetchFleetHealth(queryClient, serviceResourceName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
