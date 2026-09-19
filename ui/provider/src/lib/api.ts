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
 * (`/customers/projects/:projectName/plugins/compute/:workloadName`) puts
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

export function getProjectScopedBase(projectName: string): string {
  return `/apis/resourcemanager.miloapis.com/v1alpha1/projects/${encodeURIComponent(projectName)}/control-plane`;
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

async function fetchInstances(projectName: string): Promise<Instance[]> {
  const body = await proxyFetch<RawInstanceList>(projectName, `${INSTANCES_PATH}?limit=100`);
  return toInstanceList(body.items ?? []);
}

export function useInstances(projectName: string | undefined): UseQueryResult<Instance[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'instances', projectName],
    enabled: !!projectName,
    queryFn: () => fetchInstances(projectName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

// ── Connected ALB (HTTPProxy via NetworkService) ─────────────────────────

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
  proxyName: string;
  hostname?: string;
  displayName: string;
}

export interface PublishedUrl {
  proxyName: string;
  hostname?: string;
  displayName: string;
  proxies: ConnectedAlb[];
}

async function listOrUnavailable<T>(projectName: string, path: string): Promise<T[] | null> {
  try {
    const body = await proxyFetch<{ items?: T[] }>(projectName, `${path}?limit=100`);
    return body.items ?? [];
  } catch (error) {
    if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
      return null;
    }
    throw error;
  }
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

async function fetchPublishedUrls(projectName: string): Promise<Record<string, PublishedUrl>> {
  const [services, proxies] = await Promise.all([
    listOrUnavailable<RawNetworkService>(projectName, NETWORKSERVICES_PATH),
    listOrUnavailable<RawHttpProxy>(projectName, HTTPPROXIES_PATH),
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
  projectName: string | undefined
): UseQueryResult<Record<string, PublishedUrl>, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'published-urls', projectName],
    enabled: !!projectName,
    queryFn: () => fetchPublishedUrls(projectName as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export function usePublishedUrl(
  projectName: string | undefined,
  workloadName: string | undefined
): { data: PublishedUrl | null | undefined; isLoading: boolean } {
  const all = usePublishedUrls(projectName);
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
