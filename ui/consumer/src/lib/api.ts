/**
 * Data-fetching for the compute plugin.
 *
 * Every API call goes through the portal's existing Milo control-plane proxy
 * at `/api/proxy/…` — exactly like `examples/sample-plugin/src/lib/api.ts`'s
 * `fetchDnsZones`. There is no plugin-declared backend proxy, and no
 * generated SDK (PR #1315's `@/modules/control-plane/compute` is
 * portal-internal and unavailable here) — these are plain `fetch()` calls
 * against the compute aggregated apiserver, reached the same way as any other
 * Milo resource.
 *
 * `@tanstack/react-query` is a host-shared singleton (see vite.config.ts), so
 * plugin queries live in the host's cache alongside built-in pages. This
 * plugin must NOT create its own QueryClient.
 *
 * v1 simplifications (accepted, documented in the plugin's README):
 *  - Polling via `refetchInterval` instead of PR #1315's watch-stream hook
 *    (`useResourceWatch`, portal-internal).
 *  - Client-side 403 handling instead of PR #1315's server-loader RBAC gate
 *    (`runDetailLoader`, portal-internal) — see `ApiError` below.
 */
import { toInstance, toInstanceList, toWorkload, toWorkloadList, INSTANCE_LABELS } from '../adapter';
import type { RawInstance, RawInstanceList, RawWorkload, RawWorkloadList } from '../adapter';
import type { Instance, Workload } from '../schema';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

/**
 * Query keys are NAMESPACED under the canonical plugin id. Plugin queries
 * share the host's single QueryCache (flat global key namespace), so
 * prefixing with the plugin id prevents collisions with host keys or other
 * plugins' keys.
 */
export const PLUGIN_ID = 'workload.compute.datumapis.com';

/** Live-ish polling interval — the v1 substitute for watch-stream updates. */
const REFETCH_INTERVAL_MS = 10_000;

/** Thrown for non-ok proxy responses; carries the HTTP status for 403 handling. */
export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function getProjectScopedBase(projectId: string): string {
  // Project-scoped control-plane path, forwarded server-side by /api/proxy
  // with the user's token. Mirrors app/resources/base/utils.ts.
  return `/api/proxy/apis/resourcemanager.miloapis.com/v1alpha1/projects/${projectId}/control-plane`;
}

// NOTE: v1alpha, NOT v1alpha1 — verified against api/v1alpha/groupversion_info.go
// in the compute repo.
const WORKLOADS_PATH = '/apis/compute.datumapis.com/v1alpha/namespaces/default/workloads';
const INSTANCES_PATH = '/apis/compute.datumapis.com/v1alpha/namespaces/default/instances';

async function proxyFetch<T>(projectId: string, path: string): Promise<T> {
  const url = `${getProjectScopedBase(projectId)}${path}`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
  return res.json() as Promise<T>;
}

// ── Workloads ────────────────────────────────────────────────────────────

async function fetchWorkloads(projectId: string): Promise<Workload[]> {
  const body = await proxyFetch<RawWorkloadList>(projectId, `${WORKLOADS_PATH}?limit=100`);
  return toWorkloadList(body.items ?? []);
}

async function fetchWorkload(projectId: string, name: string): Promise<Workload> {
  const raw = await proxyFetch<RawWorkload>(projectId, `${WORKLOADS_PATH}/${name}`);
  return toWorkload(raw);
}

export function useWorkloads(projectId: string | undefined): UseQueryResult<Workload[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workloads', projectId],
    enabled: !!projectId,
    queryFn: () => fetchWorkloads(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false, // RBAC/entitlement failures shouldn't retry-storm
  });
}

export function useWorkload(
  projectId: string | undefined,
  name: string | undefined
): UseQueryResult<Workload, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workload', projectId, name],
    enabled: !!projectId && !!name,
    queryFn: () => fetchWorkload(projectId as string, name as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

// ── Instances ────────────────────────────────────────────────────────────

/** Builds the labelSelector that scopes instances to a single workload. */
function workloadInstancesSelector(workloadName: string): string {
  return `${INSTANCE_LABELS.workloadName}=${workloadName}`;
}

async function fetchWorkloadInstances(projectId: string, workloadName: string): Promise<Instance[]> {
  const query = new URLSearchParams({ labelSelector: workloadInstancesSelector(workloadName) });
  const body = await proxyFetch<RawInstanceList>(projectId, `${INSTANCES_PATH}?${query.toString()}`);
  return toInstanceList(body.items ?? []);
}

async function fetchInstance(projectId: string, instanceName: string): Promise<Instance> {
  const raw = await proxyFetch<RawInstance>(projectId, `${INSTANCES_PATH}/${instanceName}`);
  return toInstance(raw);
}

export function useWorkloadInstances(
  projectId: string | undefined,
  workloadName: string | undefined
): UseQueryResult<Instance[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workload-instances', projectId, workloadName],
    enabled: !!projectId && !!workloadName,
    queryFn: () => fetchWorkloadInstances(projectId as string, workloadName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export function useInstance(
  projectId: string | undefined,
  instanceName: string | undefined
): UseQueryResult<Instance, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'instance', projectId, instanceName],
    enabled: !!projectId && !!instanceName,
    queryFn: () => fetchInstance(projectId as string, instanceName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
