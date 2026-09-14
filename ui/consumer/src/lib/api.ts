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
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query';

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

export function getProjectScopedBase(projectId: string): string {
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

export function useWorkloads(
  projectId: string | undefined,
  enabled = true
): UseQueryResult<Workload[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workloads', projectId],
    enabled: !!projectId && enabled,
    queryFn: () => fetchWorkloads(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false, // RBAC/entitlement failures shouldn't retry-storm
  });
}

// ── Compute service entitlement ─────────────────────────────────────────
//
// Mirrors datumctl's `serviceactivation` gate: a project must have an Active
// `ServiceEntitlement` named "compute" before the Compute API is usable. The
// entitlement is a cluster-scoped resource in the project's own control
// plane (services.miloapis.com/v1alpha1), fetched/created through the same
// proxy as everything else above.

const SERVICE_ENTITLEMENTS_PATH = '/apis/services.miloapis.com/v1alpha1/serviceentitlements';

/** metadata.name of the compute ServiceEntitlement — one per project, named after the service. */
const COMPUTE_SERVICE_NAME = 'compute';

export type EntitlementPhase = 'PendingApproval' | 'Active' | 'Rejected';

const ENTITLEMENT_PHASES: readonly EntitlementPhase[] = ['PendingApproval', 'Active', 'Rejected'];

function isEntitlementPhase(value: unknown): value is EntitlementPhase {
  return typeof value === 'string' && (ENTITLEMENT_PHASES as readonly string[]).includes(value);
}

interface RawServiceEntitlement {
  status?: {
    phase?: string;
  };
}

export interface ComputeEntitlement {
  /** `null` means no ServiceEntitlement has been requested for this project yet. */
  phase: EntitlementPhase | null;
}

async function fetchComputeEntitlement(projectId: string): Promise<ComputeEntitlement> {
  const url = `${getProjectScopedBase(projectId)}${SERVICE_ENTITLEMENTS_PATH}/${COMPUTE_SERVICE_NAME}`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (res.status === 404) {
    return { phase: null };
  }
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${SERVICE_ENTITLEMENTS_PATH}`);
  }
  const body = (await res.json()) as RawServiceEntitlement;
  const rawPhase = body.status?.phase;
  // The entitlement exists — a missing or unrecognized phase (a just-created
  // object has no status yet; an unrecognized one means a phase this UI
  // doesn't know about) is treated as PendingApproval rather than passed
  // through raw, so an unknown value can never be mistaken for "not
  // requested" and re-trigger a request.
  return { phase: isEntitlementPhase(rawPhase) ? rawPhase : 'PendingApproval' };
}

export function useComputeEntitlement(
  projectId: string | undefined
): UseQueryResult<ComputeEntitlement, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'compute-entitlement', projectId],
    enabled: !!projectId,
    queryFn: () => fetchComputeEntitlement(projectId as string),
    retry: false,
  });
}

async function requestComputeEntitlement(projectId: string): Promise<void> {
  const url = `${getProjectScopedBase(projectId)}${SERVICE_ENTITLEMENTS_PATH}`;
  const res = await fetch(url, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({
      apiVersion: 'services.miloapis.com/v1alpha1',
      kind: 'ServiceEntitlement',
      metadata: { name: COMPUTE_SERVICE_NAME },
      spec: { serviceRef: { name: COMPUTE_SERVICE_NAME } },
    }),
  });
  // 409 AlreadyExists is a benign race (e.g. a second click) — not a failure.
  if (!res.ok && res.status !== 409) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${SERVICE_ENTITLEMENTS_PATH}`);
  }
}

export function useRequestComputeAccess(
  projectId: string | undefined
): UseMutationResult<void, ApiError, void> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => requestComputeEntitlement(projectId as string),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'compute-entitlement', projectId] });
    },
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

async function fetchInstances(projectId: string): Promise<Instance[]> {
  const body = await proxyFetch<RawInstanceList>(projectId, `${INSTANCES_PATH}?limit=100`);
  return toInstanceList(body.items ?? []);
}

export function useInstances(
  projectId: string | undefined,
  enabled = true
): UseQueryResult<Instance[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'instances', projectId],
    enabled: !!projectId && enabled,
    queryFn: () => fetchInstances(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
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

// ── Connected ALB (HTTPProxy via NetworkService) ─────────────────────────
//
// A workload is on a public URL when an HTTPProxy (ALB) names a NetworkService
// that selects the workload's interfaces. That covers both `datumctl compute
// url` (labelled pair) and a portal ALB whose backend is the workload's
// NetworkService. ALB logs and Envoy metrics key off the HTTPProxy name.
// 403/404/empty are treated as "not connected" — the Logs tab stays empty
// rather than failing the instance page.

const HTTPPROXIES_PATH = '/apis/networking.datumapis.com/v1alpha/namespaces/default/httpproxies';
const NETWORKSERVICES_PATH =
  '/apis/networking.datumapis.com/v1alpha/namespaces/default/networkservices';

interface RawNetworkService {
  metadata?: { name?: string; labels?: Record<string, string> };
  spec?: {
    networkInterfaces?: {
      selector?: { matchLabels?: Record<string, string> };
    };
  };
}

interface RawHttpProxy {
  metadata?: {
    name?: string;
    labels?: Record<string, string>;
    annotations?: Record<string, string>;
  };
  spec?: {
    hostnames?: string[];
    rules?: Array<{
      backends?: Array<{
        networkService?: { name?: string };
      }>;
    }>;
  };
  status?: { canonicalHostname?: string };
}

export interface ConnectedAlb {
  /** HTTPProxy metadata.name — Envoy `gateway_name` and LogQL `route_name`. */
  proxyName: string;
  /** Canonical/default hostname (`status.canonicalHostname`, then spec.hostnames). */
  hostname?: string;
  /** Portal display name (`app.kubernetes.io/name`, then `kubernetes.io/display-name`). */
  displayName: string;
}

export interface PublishedUrl {
  /** First connected HTTPProxy — used by instance logs/metrics. */
  proxyName: string;
  hostname?: string;
  displayName: string;
  proxies: ConnectedAlb[];
}

async function listOrUnavailable<T>(projectId: string, path: string): Promise<T[] | null> {
  const url = `${getProjectScopedBase(projectId)}${path}?limit=100`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (res.status === 403 || res.status === 404) {
    return null;
  }
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
  const body = (await res.json()) as { items?: T[] };
  return body.items ?? [];
}

function proxyNetworkServiceNames(proxy: RawHttpProxy): string[] {
  const names: string[] = [];
  for (const rule of proxy.spec?.rules ?? []) {
    for (const backend of rule.backends ?? []) {
      if (backend.networkService?.name) names.push(backend.networkService.name);
    }
  }
  return names;
}

function proxyHostname(proxy: RawHttpProxy): string | undefined {
  return proxy.status?.canonicalHostname || proxy.spec?.hostnames?.[0];
}

function proxyDisplayName(proxy: RawHttpProxy): string {
  const annotations = proxy.metadata?.annotations;
  const chosen = annotations?.['app.kubernetes.io/name']?.trim();
  const display = annotations?.['kubernetes.io/display-name']?.trim();
  return chosen || display || proxy.metadata?.name || '';
}

function workloadNameForService(svc: RawNetworkService): string | undefined {
  return (
    svc.metadata?.labels?.[INSTANCE_LABELS.workloadName] ||
    svc.spec?.networkInterfaces?.selector?.matchLabels?.[INSTANCE_LABELS.workloadName]
  );
}

function toConnectedAlb(proxy: RawHttpProxy): ConnectedAlb | null {
  const proxyName = proxy.metadata?.name ?? '';
  if (!proxyName) return null;
  return {
    proxyName,
    hostname: proxyHostname(proxy),
    displayName: proxyDisplayName(proxy),
  };
}

function publishedFromAlbs(albs: ConnectedAlb[]): PublishedUrl | null {
  if (albs.length === 0) return null;
  return {
    proxyName: albs[0].proxyName,
    hostname: albs[0].hostname,
    displayName: albs[0].displayName,
    proxies: albs,
  };
}

async function fetchPublishedUrls(projectId: string): Promise<Record<string, PublishedUrl>> {
  const [services, proxies] = await Promise.all([
    listOrUnavailable<RawNetworkService>(projectId, NETWORKSERVICES_PATH),
    listOrUnavailable<RawHttpProxy>(projectId, HTTPPROXIES_PATH),
  ]);
  if (services === null || proxies === null) return {};

  const serviceNamesByWorkload = new Map<string, Set<string>>();
  for (const svc of services) {
    const workload = workloadNameForService(svc);
    const name = svc.metadata?.name;
    if (!workload || !name) continue;
    const set = serviceNamesByWorkload.get(workload) ?? new Set<string>();
    set.add(name);
    serviceNamesByWorkload.set(workload, set);
  }

  const albsByWorkload = new Map<string, ConnectedAlb[]>();
  const addAlb = (workload: string, alb: ConnectedAlb) => {
    const list = albsByWorkload.get(workload) ?? [];
    if (!list.some((item) => item.proxyName === alb.proxyName)) list.push(alb);
    albsByWorkload.set(workload, list);
  };

  for (const proxy of proxies) {
    const alb = toConnectedAlb(proxy);
    if (!alb) continue;
    const labelled = proxy.metadata?.labels?.[INSTANCE_LABELS.workloadName];
    if (labelled) addAlb(labelled, alb);
    const nsNames = proxyNetworkServiceNames(proxy);
    for (const [workload, names] of serviceNamesByWorkload) {
      if (nsNames.some((name) => names.has(name))) addAlb(workload, alb);
    }
  }

  const result: Record<string, PublishedUrl> = {};
  for (const [workload, albs] of albsByWorkload) {
    const published = publishedFromAlbs(albs);
    if (published) result[workload] = published;
  }
  return result;
}

export function usePublishedUrls(
  projectId: string | undefined,
  enabled = true
): UseQueryResult<Record<string, PublishedUrl>, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'published-urls', projectId],
    enabled: !!projectId && enabled,
    queryFn: () => fetchPublishedUrls(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export function usePublishedUrl(
  projectId: string | undefined,
  workloadName: string | undefined
): { data: PublishedUrl | null | undefined; isLoading: boolean } {
  const all = usePublishedUrls(projectId, !!workloadName);
  return {
    data:
      all.data === undefined
        ? undefined
        : workloadName
          ? (all.data[workloadName] ?? null)
          : null,
    isLoading: all.isLoading,
  };
}
