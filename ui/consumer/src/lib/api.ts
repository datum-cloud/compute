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
// Mirrors the service catalog's activation gate (pkg/activation): a project
// must have an Active `ServiceEntitlement` named "compute" before the Compute API is usable. The
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

// ── Demo workload: "Global Mesh" ────────────────────────────────────────
//
// The Workloads page's "Try the Demo!" card creates the same four objects
// this manifest does — one unikernel instance in DFW, simulating a
// six-region mesh (DEMO_MODE=simulate), reachable over a public HTTPProxy URL
// so it shows up with live "Requests" metrics like any other published
// workload. No credentials Secret is needed: the unikernel image pulls
// anonymously. Names below are per-deploy
// (`datum-demo-<suffix>` / `datum-demo-net-<suffix>`) rather than fixed, and
// every object carries `app.kubernetes.io/managed-by: compute-portal-demo` —
// see `randomDemoSuffix`/`DEMO_LABELS` below. Verified on staging 2026-09-17:
//
//   apiVersion: networking.datumapis.com/v1alpha
//   kind: Network
//   metadata: {name: datum-demo-net-<suffix>, namespace: default}
//   spec: {ipFamilies: [IPv6], ipam: {mode: Auto}, mtu: 1440}
//   ---
//   apiVersion: compute.datumapis.com/v1alpha
//   kind: Workload
//   metadata: {name: datum-demo-<suffix>, namespace: default}
//   spec:
//     placements:
//     - name: dfw
//       locationSelector: {matchLabels: {topology.datum.net/city-code: DFW}}
//       scaleSettings: {minReplicas: 1}
//     template:
//       spec:
//         networkInterfaces:
//         - {name: eth0, ipFamilies: [IPv6], network: {name: datum-demo-net-<suffix>}}
//         runtime:
//           class: unikernel
//           resources: {instanceType: datumcloud/d1-standard-2}
//           sandbox:
//             containers:
//             - name: mesh
//               image: index.docker.io/scotwells/global-mesh-uk@sha256:6bfeb06ad16e395a6642145024427e37589c4fae33c4ca9932e10704cf7e46d9
//               ports: [{name: http, port: 8080, protocol: TCP}]
//               env: [{name: DEMO_MODE, value: simulate}]
//               securityContext:
//                 capabilities: {drop: [ALL]}
//   ---
//   apiVersion: networking.datumapis.com/v1alpha
//   kind: NetworkService
//   metadata: {name: datum-demo-<suffix>, namespace: default}
//   spec:
//     networkInterfaces:
//       selector: {matchLabels: {compute.datumapis.com/workload-name: datum-demo-<suffix>}}
//     ports: [{name: http, port: 8080, protocol: TCP}]
//     trafficDistribution: {strategy: Nearest}
//   ---
//   apiVersion: networking.datumapis.com/v1alpha
//   kind: HTTPProxy
//   metadata: {name: datum-demo-<suffix>, namespace: default}
//   spec:
//     rules:
//     - backends: [{networkService: {name: datum-demo-<suffix>, port: http}, weight: 1}]
//       matches: [{path: {type: PathPrefix, value: /}}]

const NETWORKS_PATH = '/apis/networking.datumapis.com/v1alpha/namespaces/default/networks';

/** Marks every object this flow creates as demo-origin — e.g. `kubectl get workloads -l app.kubernetes.io/managed-by=compute-portal-demo` to find/clean them up. */
const DEMO_LABELS = { 'app.kubernetes.io/managed-by': 'compute-portal-demo' };

/** Short random suffix so each deploy gets its own Network/Workload/NetworkService/HTTPProxy
 * names — `datumctl compute destroy workload` only removes the Workload, not the networking
 * objects alongside it, so a fixed name would 409 on the next deploy against whatever it left behind. */
function randomDemoSuffix(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return crypto.randomUUID().replace(/-/g, '').slice(0, 8);
  }
  return Math.random().toString(36).slice(2, 10);
}

function demoNetworkPayload(networkName: string) {
  return {
    apiVersion: 'networking.datumapis.com/v1alpha',
    kind: 'Network',
    metadata: { name: networkName, namespace: 'default', labels: DEMO_LABELS },
    spec: {
      ipFamilies: ['IPv6'],
      ipam: { mode: 'Auto' },
      mtu: 1440,
    },
  };
}

function demoWorkloadPayload(workloadName: string, networkName: string) {
  return {
    apiVersion: 'compute.datumapis.com/v1alpha',
    kind: 'Workload',
    metadata: { name: workloadName, namespace: 'default', labels: DEMO_LABELS },
    spec: {
      placements: [
        {
          name: 'dfw',
          locationSelector: { matchLabels: { 'topology.datum.net/city-code': 'DFW' } },
          scaleSettings: { minReplicas: 1 },
        },
      ],
      template: {
        spec: {
          networkInterfaces: [
            { name: 'eth0', ipFamilies: ['IPv6'], network: { name: networkName } },
          ],
          runtime: {
            class: 'unikernel',
            resources: { instanceType: 'datumcloud/d1-standard-2' },
            sandbox: {
              containers: [
                {
                  name: 'mesh',
                  image:
                    'index.docker.io/scotwells/global-mesh-uk@sha256:6bfeb06ad16e395a6642145024427e37589c4fae33c4ca9932e10704cf7e46d9',
                  ports: [{ name: 'http', port: 8080, protocol: 'TCP' }],
                  env: [{ name: 'DEMO_MODE', value: 'simulate' }],
                  securityContext: {
                    capabilities: { drop: ['ALL'] },
                  },
                },
              ],
            },
          },
        },
      },
    },
  };
}

function demoNetworkServicePayload(name: string, workloadName: string) {
  return {
    apiVersion: 'networking.datumapis.com/v1alpha',
    kind: 'NetworkService',
    metadata: { name, namespace: 'default', labels: DEMO_LABELS },
    spec: {
      networkInterfaces: {
        selector: { matchLabels: { [INSTANCE_LABELS.workloadName]: workloadName } },
      },
      ports: [{ name: 'http', port: 8080, protocol: 'TCP' }],
      trafficDistribution: { strategy: 'Nearest' },
    },
  };
}

function demoHttpProxyPayload(name: string, networkServiceName: string) {
  return {
    apiVersion: 'networking.datumapis.com/v1alpha',
    kind: 'HTTPProxy',
    metadata: { name, namespace: 'default', labels: DEMO_LABELS },
    spec: {
      rules: [
        {
          backends: [{ networkService: { name: networkServiceName, port: 'http' }, weight: 1 }],
          matches: [{ path: { type: 'PathPrefix', value: '/' } }],
        },
      ],
    },
  };
}

/** POSTs one object of the demo manifest, treating 409 AlreadyExists as success (a repeat click racing itself). */
async function applyDemoResource(projectId: string, path: string, body: unknown): Promise<void> {
  const url = `${getProjectScopedBase(projectId)}${path}`;
  const res = await fetch(url, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok && res.status !== 409) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
}

/** Deploys the Global Mesh demo under a fresh, randomly-suffixed name: Network, then
 * Workload, then the NetworkService/HTTPProxy that publish it. Returns the Workload's name. */
async function createDemoWorkload(projectId: string): Promise<string> {
  const suffix = randomDemoSuffix();
  const networkName = `datum-demo-net-${suffix}`;
  const workloadName = `datum-demo-${suffix}`;

  await applyDemoResource(projectId, NETWORKS_PATH, demoNetworkPayload(networkName));
  await applyDemoResource(projectId, WORKLOADS_PATH, demoWorkloadPayload(workloadName, networkName));
  await applyDemoResource(
    projectId,
    NETWORKSERVICES_PATH,
    demoNetworkServicePayload(workloadName, workloadName)
  );
  await applyDemoResource(
    projectId,
    HTTPPROXIES_PATH,
    demoHttpProxyPayload(workloadName, workloadName)
  );

  return workloadName;
}

export function useCreateDemoWorkload(
  projectId: string | undefined
): UseMutationResult<string, ApiError, void> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => createDemoWorkload(projectId as string),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'workloads', projectId] });
      void queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'instances', projectId] });
      void queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'published-urls', projectId] });
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

/**
 * Which HTTPProxies reach which workload. Shared by the published-URL lookup
 * and the delete dialog so both agree on what "connected" means: a proxy
 * labelled with the workload's name, or one whose backend names a
 * NetworkService that selects the workload's interfaces.
 */
function indexWorkloadProxies(services: RawNetworkService[], proxies: RawHttpProxy[]) {
  const serviceNamesByWorkload = new Map<string, Set<string>>();
  for (const svc of services) {
    const workload = workloadNameForService(svc);
    const name = svc.metadata?.name;
    if (!workload || !name) continue;
    const set = serviceNamesByWorkload.get(workload) ?? new Set<string>();
    set.add(name);
    serviceNamesByWorkload.set(workload, set);
  }

  const proxiesByWorkload = new Map<string, RawHttpProxy[]>();
  const addProxy = (workload: string, proxy: RawHttpProxy) => {
    const list = proxiesByWorkload.get(workload) ?? [];
    if (!list.some((item) => item.metadata?.name === proxy.metadata?.name)) list.push(proxy);
    proxiesByWorkload.set(workload, list);
  };

  for (const proxy of proxies) {
    if (!proxy.metadata?.name) continue;
    const labelled = proxy.metadata.labels?.[INSTANCE_LABELS.workloadName];
    if (labelled) addProxy(labelled, proxy);
    const nsNames = proxyNetworkServiceNames(proxy);
    for (const [workload, names] of serviceNamesByWorkload) {
      if (nsNames.some((name) => names.has(name))) addProxy(workload, proxy);
    }
  }

  return { serviceNamesByWorkload, proxiesByWorkload };
}

async function fetchPublishedUrls(projectId: string): Promise<Record<string, PublishedUrl>> {
  const [services, proxies] = await Promise.all([
    listOrUnavailable<RawNetworkService>(projectId, NETWORKSERVICES_PATH),
    listOrUnavailable<RawHttpProxy>(projectId, HTTPPROXIES_PATH),
  ]);
  if (services === null || proxies === null) return {};

  const { proxiesByWorkload } = indexWorkloadProxies(services, proxies);
  const result: Record<string, PublishedUrl> = {};
  for (const [workload, workloadProxies] of proxiesByWorkload) {
    const albs = workloadProxies.map(toConnectedAlb).filter((alb): alb is ConnectedAlb => !!alb);
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

// ── Delete workload ──────────────────────────────────────────────────────
//
// Deleting the Workload tears down everything the compute controllers made
// for it (deployments, instances, interface claims, bindings). The ALB
// (HTTPProxy + the NetworkService behind it) and the Network are separate,
// user-created objects that nothing cleans up, so the delete dialog offers
// them as opt-in extras.

export interface RelatedAlb extends ConnectedAlb {
  /** Backend NetworkServices that select this workload — removed with the ALB. */
  serviceNames: string[];
  /** A backend routes to something other than this workload's services. */
  sharedWithOtherWorkloads: boolean;
}

export interface RelatedNetwork {
  name: string;
  /** Other workloads whose template attaches to this network. */
  sharedWith: string[];
}

export interface WorkloadRelatedResources {
  albs: RelatedAlb[];
  networks: RelatedNetwork[];
  /** NetworkService name → every HTTPProxy that backends to it, so a service
   * still used by an ALB the user keeps is not deleted out from under it. */
  serviceProxies: Record<string, string[]>;
}

async function fetchWorkloadRelatedResources(
  projectId: string,
  workload: Workload
): Promise<WorkloadRelatedResources> {
  const [services, proxies, workloads] = await Promise.all([
    listOrUnavailable<RawNetworkService>(projectId, NETWORKSERVICES_PATH),
    listOrUnavailable<RawHttpProxy>(projectId, HTTPPROXIES_PATH),
    fetchWorkloads(projectId),
  ]);

  const networks = workload.networks.map((name) => ({
    name,
    sharedWith: workloads
      .filter((other) => other.name !== workload.name && other.networks.includes(name))
      .map((other) => other.name),
  }));

  if (services === null || proxies === null) {
    return { albs: [], networks, serviceProxies: {} };
  }

  const { serviceNamesByWorkload, proxiesByWorkload } = indexWorkloadProxies(services, proxies);
  const ownServices = serviceNamesByWorkload.get(workload.name) ?? new Set<string>();

  const serviceProxies: Record<string, string[]> = {};
  for (const proxy of proxies) {
    const proxyName = proxy.metadata?.name;
    if (!proxyName) continue;
    for (const svc of proxyNetworkServiceNames(proxy)) {
      (serviceProxies[svc] ??= []).push(proxyName);
    }
  }

  const albs: RelatedAlb[] = [];
  for (const proxy of proxiesByWorkload.get(workload.name) ?? []) {
    const alb = toConnectedAlb(proxy);
    if (!alb) continue;
    const backends = proxyNetworkServiceNames(proxy);
    albs.push({
      ...alb,
      serviceNames: backends.filter((name) => ownServices.has(name)),
      sharedWithOtherWorkloads: backends.some((name) => !ownServices.has(name)),
    });
  }

  return { albs, networks, serviceProxies };
}

export function useWorkloadRelatedResources(
  projectId: string | undefined,
  workload: Workload | undefined
): UseQueryResult<WorkloadRelatedResources, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'workload-related', projectId, workload?.name],
    enabled: !!projectId && !!workload,
    queryFn: () => fetchWorkloadRelatedResources(projectId as string, workload as Workload),
    retry: false,
  });
}

// ── Delete permissions ───────────────────────────────────────────────────
//
// Same answers as the portal's `useResourcePermissions({ scope: 'project' })`
// (app/modules/rbac): a resource-level SelfSubjectAccessReview in the
// project's `default` namespace, failing closed. The host's RBAC hooks aren't
// exposed to plugins, so the SSARs go through the control-plane proxy like
// every other call here. One query per page, not per row.

const SSAR_PATH = '/apis/authorization.k8s.io/v1/selfsubjectaccessreviews';
const PERMISSION_STALE_MS = 5 * 60_000;

async function canDelete(projectId: string, group: string, resource: string): Promise<boolean> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${SSAR_PATH}`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({
      apiVersion: 'authorization.k8s.io/v1',
      kind: 'SelfSubjectAccessReview',
      spec: { resourceAttributes: { group, resource, verb: 'delete', namespace: 'default' } },
    }),
  });
  if (!res.ok) return false;
  const body = (await res.json()) as { status?: { allowed?: boolean; denied?: boolean } };
  return !!body.status?.allowed && !body.status?.denied;
}

export interface DeletePermissions {
  canDeleteWorkload: boolean;
  canDeleteAlb: boolean;
  canDeleteNetworkService: boolean;
  canDeleteNetwork: boolean;
}

const NO_DELETE_PERMISSIONS: DeletePermissions = {
  canDeleteWorkload: false,
  canDeleteAlb: false,
  canDeleteNetworkService: false,
  canDeleteNetwork: false,
};

async function fetchDeletePermissions(projectId: string): Promise<DeletePermissions> {
  const [canDeleteWorkload, canDeleteAlb, canDeleteNetworkService, canDeleteNetwork] =
    await Promise.all([
      canDelete(projectId, 'compute.datumapis.com', 'workloads'),
      canDelete(projectId, 'networking.datumapis.com', 'httpproxies'),
      canDelete(projectId, 'networking.datumapis.com', 'networkservices'),
      canDelete(projectId, 'networking.datumapis.com', 'networks'),
    ]);
  return { canDeleteWorkload, canDeleteAlb, canDeleteNetworkService, canDeleteNetwork };
}

export function useDeletePermissions(
  projectId: string | undefined
): DeletePermissions & { isLoading: boolean } {
  const query = useQuery({
    queryKey: [PLUGIN_ID, 'permissions', projectId],
    enabled: !!projectId,
    queryFn: () => fetchDeletePermissions(projectId as string),
    staleTime: PERMISSION_STALE_MS,
    retry: false,
  });
  // Pending (including disabled) reads as loading so nothing flashes in and out.
  return { ...(query.data ?? NO_DELETE_PERMISSIONS), isLoading: query.isPending };
}

// ── Delete mutation ──────────────────────────────────────────────────────

/** DELETEs one object; a 404 means it's already gone, which is what we wanted. */
async function proxyDelete(projectId: string, path: string): Promise<void> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${path}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  });
  if (!res.ok && res.status !== 404) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
}

export type RelatedKind = 'alb' | 'network';

export interface DeleteWorkloadInput {
  workloadName: string;
  /** HTTPProxy names the user ticked. */
  albs: string[];
  /** Network names the user ticked. */
  networks: string[];
  related: WorkloadRelatedResources;
  canDeleteNetworkService: boolean;
}

export interface DeleteWorkloadResult {
  failed: { kind: RelatedKind; name: string; error: ApiError }[];
}

async function deleteWorkload(
  projectId: string,
  input: DeleteWorkloadInput
): Promise<DeleteWorkloadResult> {
  // The Workload goes first: if that is refused, nothing else is touched.
  await proxyDelete(projectId, `${WORKLOADS_PATH}/${input.workloadName}`);

  const failed: DeleteWorkloadResult['failed'] = [];
  const attempt = async (kind: RelatedKind, name: string, run: () => Promise<void>) => {
    try {
      await run();
    } catch (error) {
      failed.push({
        kind,
        name,
        error: error instanceof ApiError ? error : new ApiError(0, String(error)),
      });
    }
  };

  const selected = new Set(input.albs);
  for (const alb of input.related.albs.filter((item) => selected.has(item.proxyName))) {
    await attempt('alb', alb.proxyName, async () => {
      await proxyDelete(projectId, `${HTTPPROXIES_PATH}/${alb.proxyName}`);
      if (!input.canDeleteNetworkService) return;
      for (const svc of alb.serviceNames) {
        // Keep a service another, un-ticked ALB still routes to.
        const users = input.related.serviceProxies[svc] ?? [];
        if (users.some((proxy) => !selected.has(proxy))) continue;
        await proxyDelete(projectId, `${NETWORKSERVICES_PATH}/${svc}`);
      }
    });
  }

  // The Network's in-use finalizer holds it in Terminating until the workload's
  // bindings are released, so it's safe to ask right away.
  for (const network of input.networks) {
    await attempt('network', network, () => proxyDelete(projectId, `${NETWORKS_PATH}/${network}`));
  }

  return { failed };
}

/**
 * cloud-portal's own query-key roots for the objects a workload delete can
 * remove or orphan. The host's cache is shared with this plugin (see the top
 * of this file), so invalidating these makes the ALB pages refetch on their
 * next mount instead of showing a deleted ALB, or a workload link that now
 * 404s. Mirrors `httpProxyKeys.all`, `networkServiceKeys.all` and
 * `computeWorkloadKeys.all` in cloud-portal's `app/resources/*`.
 */
const HOST_QUERY_ROOTS = [['http-proxies'], ['network-services'], ['compute-workloads']] as const;

type DeleteWorkloadContext = { previous?: Workload[] };

export function useDeleteWorkload(
  projectId: string | undefined
): UseMutationResult<DeleteWorkloadResult, ApiError, DeleteWorkloadInput, DeleteWorkloadContext> {
  const queryClient = useQueryClient();
  const listKey = [PLUGIN_ID, 'workloads', projectId];
  return useMutation({
    mutationFn: (input) => deleteWorkload(projectId as string, input),
    // Show "Deleting" straight away: the detail page navigates to the list on
    // confirm, before the DELETE has returned for the next poll to pick up.
    onMutate: async (input) => {
      await queryClient.cancelQueries({ queryKey: listKey });
      const previous = queryClient.getQueryData<Workload[]>(listKey);
      queryClient.setQueryData<Workload[]>(listKey, (list) =>
        list?.map((item) => (item.name === input.workloadName ? { ...item, deleting: true } : item))
      );
      return { previous };
    },
    onError: (_error, _input, context) => {
      if (context?.previous) queryClient.setQueryData(listKey, context.previous);
    },
    onSettled: (_data, _error, input) => {
      for (const key of ['workloads', 'instances', 'published-urls', 'workload-related']) {
        void queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, key, projectId] });
      }
      for (const queryKey of HOST_QUERY_ROOTS) {
        void queryClient.invalidateQueries({ queryKey: [...queryKey] });
      }
      const detailKeys = [
        [PLUGIN_ID, 'workload', projectId, input.workloadName],
        [PLUGIN_ID, 'workload-instances', projectId, input.workloadName],
      ];
      for (const queryKey of detailKeys) void queryClient.cancelQueries({ queryKey });
      // Drop the detail cache once the page has navigated away, so a later
      // visit doesn't render the deleted workload from cache first. Doing it
      // now would make the still-mounted detail page refetch into a 404.
      setTimeout(() => {
        for (const queryKey of detailKeys) queryClient.removeQueries({ queryKey, type: 'inactive' });
      }, 0);
    },
  });
}
