/**
 * Data-fetching for the provider plugin's Workload detail view.
 *
 * Every call goes through staff-portal's same-origin proxy at
 * `/api/internal/…` (see staff-portal's `app/server/routes/api.ts`), exactly
 * like `ui/consumer/src/lib/api.ts` does against cloud-portal's own proxy —
 * plain `fetch()` against the compute aggregated apiserver, no plugin-owned
 * backend, no new credential (runs under the viewing staff member's own
 * session).
 *
 * `projectName` is resolved via `useParams()` from the host's shared
 * react-router singleton — the mount route
 * (`/customers/projects/:projectName/plugins/workloads/:workloadName`) puts
 * it in scope as an ancestor route param even though this plugin's own
 * declared page path only adds `:workloadName`.
 *
 * `@tanstack/react-query` is a host-shared singleton (see vite.config.ts);
 * this plugin must NOT create its own QueryClient.
 */
import { toInstanceList, toWorkload, toWorkloadList, INSTANCE_LABELS } from '../adapter';
import type { RawInstanceList, RawWorkload, RawWorkloadList } from '../adapter';
import type { Instance, Workload } from '../schema';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

export const PLUGIN_ID = 'workloads.staff-portal.datumapis.com';

/** Live-ish polling interval — no watch stream in v1. */
const REFETCH_INTERVAL_MS = 10_000;

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function getProjectScopedBase(projectName: string): string {
  return `/api/internal/apis/resourcemanager.miloapis.com/v1alpha1/projects/${encodeURIComponent(projectName)}/control-plane`;
}

// v1alpha, NOT v1alpha1 — verified against api/v1alpha/groupversion_info.go.
const WORKLOADS_PATH = '/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads';
const INSTANCES_PATH = '/apis/compute.datumapis.com/v1alpha/namespaces/default/instances';

/**
 * Every `/api/internal/*` response is wrapped by staff-portal's own proxy
 * (`createSuccessResponse` in `app/server/response.ts`): `{ requestId, code,
 * data, path }` — the upstream K8s object lives at `.data`, not the body
 * root. Unlike `ui/consumer`'s `/api/proxy/...` (cloud-portal), which passes
 * the upstream response through unwrapped.
 */
interface ProxyEnvelope<T> {
  data: T;
}

async function proxyFetch<T>(projectName: string, path: string): Promise<T> {
  return proxyFetchAbsolute<T>(`${getProjectScopedBase(projectName)}${path}`);
}

/**
 * Same envelope-unwrapping `/api/internal/...` fetch as {@link proxyFetch},
 * for a path that isn't project-scoped (e.g. a cluster-scoped
 * `services.miloapis.com` resource) — see `../lib/fleet-health.ts` and
 * `../lib/service-catalog.ts`'s own fetch helpers, which use this directly
 * (not `proxyFetch`) so a 403 surfaces as {@link ApiError} instead of a
 * plain `Error` — required for `ErrorOrRestrictedState` to render the
 * restricted-access state rather than a generic failure card.
 */
export async function proxyFetchAbsolute<T>(path: string): Promise<T> {
  const res = await fetch(`/api/internal${path}`, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
  const envelope = (await res.json()) as ProxyEnvelope<T>;
  return envelope.data;
}

export async function fetchWorkloads(projectName: string): Promise<Workload[]> {
  const body = await proxyFetch<RawWorkloadList>(projectName, `${WORKLOADS_PATH}?limit=100`);
  return toWorkloadList(body.items ?? []);
}

export function useWorkloads(projectName: string | undefined): UseQueryResult<Workload[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workloads', projectName],
    enabled: !!projectName,
    queryFn: () => fetchWorkloads(projectName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

/**
 * `useWorkload` and `useWorkloadRaw` share this exact query key so they share
 * one underlying fetch/cache entry (react-query dedupes by key) — the YAML
 * tab needs the unadapted resource and Overview needs the adapted one, but
 * there's no reason to hit the same endpoint twice or for one to resolve
 * before the other.
 */
function workloadQueryKey(projectName: string | undefined, name: string | undefined) {
  return [PLUGIN_ID, 'workload-raw', projectName, name] as const;
}

async function fetchRawWorkload(projectName: string, name: string): Promise<RawWorkload> {
  return proxyFetch<RawWorkload>(projectName, `${WORKLOADS_PATH}/${encodeURIComponent(name)}`);
}

export function useWorkload(
  projectName: string | undefined,
  name: string | undefined
): UseQueryResult<Workload, ApiError> {
  return useQuery({
    queryKey: workloadQueryKey(projectName, name),
    enabled: !!projectName && !!name,
    queryFn: () => fetchRawWorkload(projectName as string, name as string),
    select: toWorkload,
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

/** Raw resource, for the YAML tab — same query as {@link useWorkload}, unadapted. */
export function useWorkloadRaw(
  projectName: string | undefined,
  name: string | undefined
): UseQueryResult<RawWorkload, ApiError> {
  return useQuery({
    queryKey: workloadQueryKey(projectName, name),
    enabled: !!projectName && !!name,
    queryFn: () => fetchRawWorkload(projectName as string, name as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

function workloadInstancesSelector(workloadName: string): string {
  return `${INSTANCE_LABELS.workloadName}=${workloadName}`;
}

async function fetchWorkloadInstances(
  projectName: string,
  workloadName: string
): Promise<Instance[]> {
  const query = new URLSearchParams({ labelSelector: workloadInstancesSelector(workloadName) });
  const body = await proxyFetch<RawInstanceList>(
    projectName,
    `${INSTANCES_PATH}?${query.toString()}`
  );
  return toInstanceList(body.items ?? []);
}

export function useWorkloadInstances(
  projectName: string | undefined,
  workloadName: string | undefined
): UseQueryResult<Instance[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workload-instances', projectName, workloadName],
    enabled: !!projectName && !!workloadName,
    queryFn: () => fetchWorkloadInstances(projectName as string, workloadName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
