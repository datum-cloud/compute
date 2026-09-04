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
import type { Workload } from '../schema';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

const FLEET_QUERY_KEY = [PLUGIN_ID, 'fleet-health'];

/** Live-ish polling interval — matches the per-project views (`api.ts`). */
const REFETCH_INTERVAL_MS = 30_000;

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
  serviceName: string | null;
  phase: string | null;
  consumerProject: { name: string; displayName: string };
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

async function fetchServiceConsumers(producerProject: string): Promise<RawServiceConsumer[]> {
  const data = await postGraphQL<{ serviceConsumers?: RawServiceConsumer[] }>(
    `
      query FleetHealthConsumers($producerProject: ID!) {
        serviceConsumers(producerProject: $producerProject) {
          name
          serviceName
          phase
          consumerProject { name displayName }
        }
      }
    `,
    { producerProject }
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
  /** The `Available` condition's reason/message, or the health label as a fallback — the closest thing to "why," without opening the workload. */
  reason: string;
  /** When the workload last transitioned to its current `Available` status — falls back to `createdAt` if never observed. */
  statusSince: Date;
}

/** A consumer project whose workload list couldn't be fetched — rendered, not dropped. */
export interface FailedProject {
  project: FleetConsumerProject;
  error: string;
}

interface RawGraphQLProject {
  organizationName: string;
  organizationDisplayName: string;
}

/**
 * Batches at most this many projects into one aliased GraphQL request (see
 * {@link fetchProjectOrganizationsChunk}) — an unbounded single query risks
 * tripping the gateway's query-size/complexity limits once the fleet grows
 * past a few hundred consumers.
 */
const ORG_LOOKUP_CHUNK_SIZE = 50;

/**
 * Resolves every project's owning organization via one aliased GraphQL
 * request per {@link ORG_LOOKUP_CHUNK_SIZE}-project chunk — the gateway's
 * `Project` type already carries `organizationName` / `organizationDisplayName`
 * resolved server-side (unlike the thin `ConsumerProject` the consumers query
 * returns), so this doesn't need a per-project `Project` GET plus a per-org
 * `Organization` GET: one query with a `p<i>: project(name: $name<i>) { ... }`
 * alias per project batches a whole chunk into a single request. (Aliases
 * are index-based within each chunk since GraphQL identifiers can't contain
 * the hyphens Kubernetes resource names commonly do — the real name travels
 * as the arg value, not the alias.) A chunk that fails outright just yields
 * no organizations for its projects — best-effort, doesn't fail the page.
 */
async function fetchProjectOrganizationsChunk(
  projectNames: string[]
): Promise<Map<string, FleetConsumerOrganization>> {
  const result = new Map<string, FleetConsumerOrganization>();
  if (projectNames.length === 0) return result;

  const variableDefs = projectNames.map((_, i) => `$name${i}: String!`).join(', ');
  const fields = projectNames
    .map((_, i) => `p${i}: project(name: $name${i}) { organizationName organizationDisplayName }`)
    .join('\n');
  const variables = Object.fromEntries(projectNames.map((name, i) => [`name${i}`, name]));

  const res = await fetch('/api/graphql', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({
      query: `query FleetHealthProjectOrgs(${variableDefs}) {\n${fields}\n}`,
      variables,
    }),
  });
  if (!res.ok) return result;

  const body = (await res.json()) as { data?: Record<string, RawGraphQLProject | null> };
  const data = body.data ?? {};
  projectNames.forEach((name, i) => {
    const p = data[`p${i}`];
    if (p?.organizationName) {
      result.set(name, {
        name: p.organizationName,
        displayName: p.organizationDisplayName || p.organizationName,
      });
    }
  });
  return result;
}

function chunk<T>(items: T[], size: number): T[][] {
  const chunks: T[][] = [];
  for (let i = 0; i < items.length; i += size) chunks.push(items.slice(i, i + size));
  return chunks;
}

async function fetchProjectOrganizations(
  projectNames: string[]
): Promise<Map<string, FleetConsumerOrganization>> {
  const chunks = await Promise.all(
    chunk(projectNames, ORG_LOOKUP_CHUNK_SIZE).map(fetchProjectOrganizationsChunk)
  );
  return new Map(chunks.flatMap((m) => [...m]));
}

/**
 * Attaches each project's owning organization. Best-effort: a project whose
 * org can't be resolved (or a wholesale request failure) just renders
 * without one, it doesn't fail the whole page.
 */
async function attachOrganizations(
  projects: FleetConsumerProject[]
): Promise<FleetConsumerProject[]> {
  const orgByProject = await fetchProjectOrganizations(projects.map((p) => p.name));
  return projects.map((project) => {
    const organization = orgByProject.get(project.name);
    return organization ? { ...project, organization } : project;
  });
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
}

/**
 * The `Available` condition's reason/message, or the workload's own health
 * label as a last resort — every row gets *some* reason text, even one with
 * no conditions reported at all (and even a healthy one, if its `Available`
 * condition carries no reason of its own).
 */
function reasonFor(workload: Workload): string {
  const available = workload.conditions.find((c) => c.type === 'Available');
  return available?.reason ?? available?.message ?? workload.health;
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

async function fetchFleetHealth(serviceResourceName: string): Promise<FleetHealth> {
  const service = await fetchService(serviceResourceName);
  const consumers = await fetchServiceConsumers(service.producerProject);

  const activeConsumerProjectsRaw: FleetConsumerProject[] = consumers
    .filter(
      (c) =>
        c.phase === 'Active' &&
        (c.serviceName === service.resourceName || c.serviceName === service.canonicalName)
    )
    .map((c) => c.consumerProject);

  const activeConsumerProjects = await attachOrganizations(activeConsumerProjectsRaw);

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
      reason: reasonFor(w.workload),
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
  };
}

export function useFleetHealth(
  serviceResourceName: string | undefined
): UseQueryResult<FleetHealth, Error> {
  return useQuery({
    queryKey: [FLEET_QUERY_KEY, serviceResourceName],
    enabled: !!serviceResourceName,
    queryFn: () => fetchFleetHealth(serviceResourceName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
