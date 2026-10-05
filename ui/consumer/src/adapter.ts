/**
 * Raw K8s resource → schema mappers — pure functions, ported from cloud-portal
 * PR #1315's `app/resources/workloads/workload.adapter.ts` and
 * `app/resources/instances/instance.adapter.ts`.
 *
 * PR #1315 typed `raw` against the generated SDK
 * (`ComDatumapisComputeV1AlphaWorkload` / `...Instance` from
 * `@/modules/control-plane/compute`), which a plugin has no access to. The
 * `Raw*` interfaces below are hand-written against the actual Go JSON tags in
 * this repo (`api/v1alpha/workload_types.go`, `api/v1alpha/instance_types.go`,
 * `api/v1alpha/labels.go`) instead — same shape, no generated-SDK dependency.
 */
import type {
  Instance,
  InstanceStatusValue,
  Workload,
  WorkloadHealth,
  WorkloadPlacementRegion,
} from './schema';

// ── Shared ───────────────────────────────────────────────────────────────

interface RawCondition {
  type?: string;
  status?: 'True' | 'False' | 'Unknown';
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
  observedGeneration?: number;
}

interface RawObjectMeta {
  uid?: string;
  name?: string;
  namespace?: string;
  resourceVersion?: string;
  creationTimestamp?: string;
  deletionTimestamp?: string;
  labels?: Record<string, string>;
}

interface RawSandboxContainer {
  name?: string;
  image?: string;
  ports?: { name?: string; port?: number; protocol?: string }[];
}

interface RawRuntime {
  resources?: {
    instanceType?: string;
    // corev1.ResourceList — a map of resource name -> quantity string, e.g.
    // { cpu: "1", memory: "512Mi" }.
    requests?: Record<string, string>;
  };
  sandbox?: { containers?: RawSandboxContainer[] };
  virtualMachine?: unknown;
  class?: string;
}

// ── Workload ─────────────────────────────────────────────────────────────

/** Well-known labels stamped onto instances by the compute controllers. */
export const INSTANCE_LABELS = {
  workloadName: 'compute.datumapis.com/workload-name',
  workloadUid: 'compute.datumapis.com/workload-uid',
  location: 'compute.datumapis.com/location',
  placementName: 'compute.datumapis.com/placement-name',
} as const;

interface RawWorkloadPlacement {
  name: string;
  locations?: Array<{ name: string }>;
  locationSelector?: RawLabelSelector;
  /** Deprecated: stored before placement moved to locations and not yet rewritten. */
  cityCodes?: string[];
  scaleSettings?: { minReplicas?: number; maxReplicas?: number };
}

interface RawWorkloadPlacementStatus {
  name?: string;
  locations?: Array<{ name: string }>;
  conditions?: RawCondition[];
  replicas?: number;
  currentReplicas?: number;
  updatedReplicas?: number;
  desiredReplicas?: number;
  readyReplicas?: number;
}

export interface RawWorkload {
  metadata?: RawObjectMeta;
  spec?: {
    template?: {
      spec?: {
        runtime?: RawRuntime;
        networkInterfaces?: Array<{ network?: { name?: string } }>;
      };
    };
    placements?: RawWorkloadPlacement[];
  };
  status?: {
    conditions?: RawCondition[];
    deployments?: number;
    replicas?: number;
    currentReplicas?: number;
    updatedReplicas?: number;
    desiredReplicas?: number;
    readyReplicas?: number;
    placements?: RawWorkloadPlacementStatus[];
  };
}

export interface RawWorkloadList {
  items?: RawWorkload[];
}

/**
 * `Available=False` reasons the controller reports while a workload is still
 * coming up on its own — mirrors the constants in `api/v1alpha/instance_types.go`.
 * Nothing the user has to fix; given time the workload should serve.
 */
const DEPLOYING_REASONS = new Set([
  'NoAvailableDeployments',
  'NoAvailablePlacements',
  'NoMatchingLocation',
  'NetworkProvisioning',
  'InstancesProvisioning',
  'AwaitingPropagation',
  'Resolving',
]);

/**
 * Quota blocks the rollout until it is granted. Reported as deploying (it
 * proceeds once quota frees up), but `deployStatus` gives it a warning tone
 * because the user usually has to act.
 */
export const QUOTA_REASONS = new Set(['QuotaNotGranted', 'PendingQuota']);

/**
 * `ReferencedDataNotReady` covers both companions still propagating (transient)
 * and a source that will never resolve. The controller only says which in the
 * message, so propagation is told apart by its wording.
 */
function isReferencedDataPropagating(condition: RawCondition): boolean {
  return condition.reason === 'ReferencedDataNotReady' && /propagat/i.test(condition.message ?? '');
}

/**
 * The Workload API has a single `Available` condition, with no `Progressing`
 * one: a workload that hasn't served yet is `Available=False` whether it is
 * mid-rollout or stuck. The reason tells the two apart. A workload with no
 * `Available` condition hasn't been reconciled yet, so it is deploying too.
 */
function deriveWorkloadHealth(conditions: RawCondition[]): WorkloadHealth {
  const available = conditions?.find((c) => c.type === 'Available');
  if (!available) return 'Deploying';

  if (available.status === 'True') return 'Available';
  if (available.status === 'False') {
    const reason = available.reason ?? '';
    if (DEPLOYING_REASONS.has(reason) || QUOTA_REASONS.has(reason)) return 'Deploying';
    if (isReferencedDataPropagating(available)) return 'Deploying';
    return 'Unavailable';
  }
  return 'Unknown';
}

/** Builds a human-readable resource summary, e.g. "datumcloud-d1-standard-2 · 1 vCPU · 512Mi". */
function deriveResources(runtime?: RawRuntime): string | undefined {
  const res = runtime?.resources;
  if (!res) return undefined;

  const parts: string[] = [];
  if (res.instanceType) parts.push(res.instanceType);

  const requests = res.requests ?? {};
  if (requests.cpu !== undefined) parts.push(`${requests.cpu} vCPU`);
  if (requests.memory !== undefined) parts.push(String(requests.memory));

  return parts.length > 0 ? parts.join(' · ') : undefined;
}

/** Returns the per-region replica count only when every placement shares the same minReplicas. */
function deriveReplicasPerRegion(placements: RawWorkloadPlacement[]): number | undefined {
  if (!placements || placements.length === 0) return undefined;

  const mins = placements.map((p) => p.scaleSettings?.minReplicas);
  const first = mins[0];
  if (first === undefined) return undefined;

  return mins.every((m) => m === first) ? first : undefined;
}

function derivePorts(runtime?: RawRuntime): string[] {
  const containers = runtime?.sandbox?.containers ?? [];
  return containers.flatMap((c) =>
    (c.ports ?? []).map((p) => `${p.port}/${p.protocol ?? 'TCP'}`)
  );
}

/** Card chips: runtime kind + first port protocol when present. */
function deriveTags(runtime?: RawRuntime): string[] {
  const tags: string[] = [];
  if (runtime?.sandbox) tags.push('Container sandbox');
  else if (runtime?.virtualMachine) tags.push('Virtual machine');

  const firstPort = runtime?.sandbox?.containers?.[0]?.ports?.[0];
  if (firstPort?.protocol) tags.push(firstPort.protocol);
  else if (firstPort?.port !== undefined) tags.push('TCP');

  return tags;
}

function deriveUpdatedAt(
  conditions: RawCondition[],
  createdAt: Date
): Date | undefined {
  let latest: Date | undefined;
  for (const c of conditions) {
    if (!c.lastTransitionTime) continue;
    const t = new Date(c.lastTransitionTime);
    if (Number.isNaN(t.getTime())) continue;
    if (!latest || t > latest) latest = t;
  }
  // Prefer a condition transition when it differs from create time.
  if (latest && latest.getTime() !== createdAt.getTime()) return latest;
  return latest ?? undefined;
}

interface RawLabelSelector {
  matchLabels?: Record<string, string>;
  matchExpressions?: Array<{ key: string; operator: string; values?: string[] }>;
}

/** Renders a label selector the way kubectl prints it, e.g. "city-code=DFW, region in (a,b)". */
function formatLabelSelector(selector: RawLabelSelector): string {
  const parts: string[] = [];
  for (const [key, value] of Object.entries(selector.matchLabels ?? {})) {
    parts.push(`${key}=${value}`);
  }
  for (const expr of selector.matchExpressions ?? []) {
    const values = (expr.values ?? []).join(',');
    switch (expr.operator) {
      case 'In':
        parts.push(`${expr.key} in (${values})`);
        break;
      case 'NotIn':
        parts.push(`${expr.key} notin (${values})`);
        break;
      case 'Exists':
        parts.push(expr.key);
        break;
      case 'DoesNotExist':
        parts.push(`!${expr.key}`);
        break;
      default:
        parts.push(`${expr.key} ${expr.operator} (${values})`);
    }
  }
  return parts.join(', ');
}

/**
 * The locations a placement runs at: what the controller resolved when status
 * is present, else what the spec names. A selector-based placement has no
 * names in its spec, so status is the only place its locations appear.
 */
function placementLocations(p: RawWorkloadPlacement, status?: RawWorkloadPlacementStatus): string[] {
  const resolved = (status?.locations ?? []).map((location) => location.name);
  if (resolved.length > 0) return resolved;
  return (p.locations ?? []).map((location) => location.name);
}

function workloadLocations(placements: RawWorkloadPlacement[], statusPlacements: RawWorkloadPlacementStatus[]): string[] {
  const statusByName = new Map(statusPlacements.filter((s) => !!s.name).map((s) => [s.name, s]));
  return Array.from(new Set(placements.flatMap((p) => placementLocations(p, statusByName.get(p.name)))));
}

function toPlacementRegions(
  placements: RawWorkloadPlacement[],
  statusPlacements: RawWorkloadPlacementStatus[]
): WorkloadPlacementRegion[] {
  const statusByName = new Map(
    statusPlacements
      .filter((s): s is RawWorkloadPlacementStatus & { name: string } => !!s.name)
      .map((s) => [s.name, s])
  );

  return placements.map((p) => {
    const status = statusByName.get(p.name);
    const desired = status?.desiredReplicas ?? p.scaleSettings?.minReplicas ?? 0;
    const ready = status?.readyReplicas ?? 0;
    const conditions = status?.conditions ?? [];
    // When the placement's Available condition is absent, infer from
    // ready/desired so the region status dot still reflects health.
    const health: WorkloadHealth = conditions.some((c) => c.type === 'Available')
      ? deriveWorkloadHealth(conditions)
      : desired > 0 && ready >= desired
        ? 'Available'
        : ready > 0
          ? 'Degraded'
          : 'Deploying';

    return {
      name: p.name,
      locations: placementLocations(p, status),
      locationSelector: p.locationSelector
        ? formatLabelSelector(p.locationSelector)
        : p.cityCodes && p.cityCodes.length > 0
          ? `topology.datum.net/city-code in (${p.cityCodes.join(',')})`
          : undefined,
      readyReplicas: ready,
      desiredReplicas: desired,
      health,
    };
  });
}

/** Distinct network names across the template's interfaces, in declaration order. */
function deriveNetworks(interfaces?: Array<{ network?: { name?: string } }>): string[] {
  const names = (interfaces ?? []).map((iface) => iface.network?.name).filter(Boolean) as string[];
  return [...new Set(names)];
}

export function toWorkload(raw: RawWorkload): Workload {
  const conditions = raw.status?.conditions ?? [];
  const placements = raw.spec?.placements ?? [];
  const runtime = raw.spec?.template?.spec?.runtime;
  const createdAt = raw.metadata?.creationTimestamp
    ? new Date(raw.metadata.creationTimestamp)
    : new Date();
  const ports = derivePorts(runtime);

  return {
    uid: raw.metadata?.uid ?? '',
    name: raw.metadata?.name ?? '',
    namespace: raw.metadata?.namespace,
    resourceVersion: raw.metadata?.resourceVersion,
    createdAt,
    updatedAt: deriveUpdatedAt(conditions, createdAt),
    image: runtime?.sandbox?.containers?.[0]?.image,
    health: deriveWorkloadHealth(conditions),
    currentReplicas: raw.status?.currentReplicas ?? 0,
    readyReplicas: raw.status?.readyReplicas ?? 0,
    desiredReplicas: raw.status?.desiredReplicas ?? 0,
    placements: placements.map((p) => p.name),
    placementRegions: toPlacementRegions(placements, raw.status?.placements ?? []),
    runtimeType: runtime ? (runtime.sandbox ? 'Container sandbox' : 'Virtual machine') : undefined,
    tags: deriveTags(runtime),
    ports,
    locations: workloadLocations(placements, raw.status?.placements ?? []),
    resources: deriveResources(runtime),
    replicasPerRegion: deriveReplicasPerRegion(placements),
    networks: deriveNetworks(raw.spec?.template?.spec?.networkInterfaces),
    deleting: !!raw.metadata?.deletionTimestamp,
    conditions: conditions.map((c) => ({
      type: c.type ?? '',
      status: c.status ?? 'Unknown',
      reason: c.reason,
      message: c.message,
      lastTransitionTime: c.lastTransitionTime,
      observedGeneration: c.observedGeneration,
    })),
  };
}

export function toWorkloadList(items: RawWorkload[]): Workload[] {
  return items.map(toWorkload);
}

// ── Instance ─────────────────────────────────────────────────────────────

export interface RawInstance {
  metadata?: RawObjectMeta;
  spec?: { runtime?: RawRuntime };
  status?: {
    conditions?: RawCondition[];
    networkInterfaces?: {
      assignments?: { networkIP?: string; externalIP?: string };
      addresses?: { address?: string }[];
    }[];
  };
}

export interface RawInstanceList {
  metadata?: { continue?: string };
  items?: RawInstance[];
}

/**
 * Reasons an instance's `Ready` (or `Programmed`) condition carries when it
 * won't come up without the user changing something — see the
 * `InstanceReadyReason*` constants in `api/v1alpha/instance_types.go`. The
 * owning deployment still reports `InstancesProvisioning` for these, so this is
 * the only place a bad image or crash loop shows up.
 */
const INSTANCE_FAILURE_REASONS = new Set(['ImageUnavailable', 'InstanceCrashing', 'ConfigurationError']);

/** The condition explaining why an instance is failing, if it is. */
export function instanceFailure<T extends RawCondition>(conditions: readonly T[]): T | undefined {
  return conditions.find(
    (c) =>
      (c.type === 'Ready' || c.type === 'Programmed') &&
      c.status === 'False' &&
      INSTANCE_FAILURE_REASONS.has(c.reason ?? '')
  );
}

// The compute API has no explicit "Failed" status field, so we infer it from
// the failure reasons above, then from the Available condition's
// reason/message text — best-effort, matching PR #1315's heuristic.
function deriveInstanceStatus(conditions: RawCondition[]): InstanceStatusValue {
  if (!conditions || conditions.length === 0) return 'Unknown';

  const available = conditions.find((c) => c.type === 'Available');
  if (available?.status === 'True') return 'Available';
  if (instanceFailure(conditions)) return 'Failed';
  if (!available) return 'Unknown';

  const text = `${available.reason ?? ''} ${available.message ?? ''}`;
  if (/fail|error/i.test(text)) return 'Failed';
  return 'Pending';
}

/**
 * Platform instance-type catalog — mirrors `pkg/instancetype/catalog.go`. Most
 * instances only set `instanceType` (no explicit requests); the controller
 * resolves size from this catalog for quota. We do the same for display.
 * `datumcloud/d1-standard-2` is the name the baseline type had before the
 * rename, still carried by instances created before it.
 */
const INSTANCE_TYPE_CATALOG: Record<string, { cpu: string; memory: string }> = {
  'datumcloud-d1-standard-2': { cpu: '1', memory: '2Gi' },
  'datumcloud/d1-standard-2': { cpu: '1', memory: '2Gi' },
  'd1-standard-2': { cpu: '1', memory: '2Gi' },
};

/** Resolves allocated CPU/memory: explicit requests, else instance-type catalog. */
function resolveInstanceResources(runtime?: RawRuntime): {
  cpu?: string;
  memory?: string;
} {
  const requests = runtime?.resources?.requests ?? {};
  const cpu = requests.cpu;
  const memory = requests.memory;
  if (cpu !== undefined && memory !== undefined) {
    return { cpu, memory };
  }

  const instanceType = runtime?.resources?.instanceType;
  if (instanceType && INSTANCE_TYPE_CATALOG[instanceType]) {
    const catalog = INSTANCE_TYPE_CATALOG[instanceType];
    return {
      cpu: cpu ?? catalog.cpu,
      memory: memory ?? catalog.memory,
    };
  }

  return { cpu, memory };
}

/**
 * Host-route in-network address. `/32` and `/128` strip to the bare IP;
 * delegated prefixes (`/96`, …) are not a single host and are skipped.
 */
export function hostRouteIP(address?: string): string | undefined {
  const trimmed = address?.trim();
  if (!trimmed) return undefined;
  const slash = trimmed.lastIndexOf("/");
  if (slash === -1) return isBareIP(trimmed) ? trimmed : undefined;
  const ip = trimmed.slice(0, slash);
  const bits = Number(trimmed.slice(slash + 1));
  if (!isBareIP(ip) || !Number.isInteger(bits)) return undefined;
  const bitLen = ip.includes(":") ? 128 : 32;
  if (bits !== bitLen) return undefined;
  return ip;
}

function isBareIP(value: string): boolean {
  if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(value)) return true;
  return value.includes(":") && !value.includes("/");
}

/** In-network host IPs the ALB may dial. Never includes external addresses. */
export function collectInstanceInternalIPs(raw: RawInstance): string[] {
  const seen = new Set<string>();
  const ips: string[] = [];
  const add = (value?: string) => {
    const ip = hostRouteIP(value);
    if (!ip || seen.has(ip)) return;
    seen.add(ip);
    ips.push(ip);
  };
  for (const iface of raw.status?.networkInterfaces ?? []) {
    for (const address of iface.addresses ?? []) add(address.address);
    add(iface.assignments?.networkIP);
  }
  return ips;
}

export function toInstance(raw: RawInstance): Instance {
  const labels = raw.metadata?.labels ?? {};
  const assignments = raw.status?.networkInterfaces?.[0]?.assignments;
  const container = raw.spec?.runtime?.sandbox?.containers?.[0];
  const conditions = raw.status?.conditions ?? [];
  const { cpu, memory } = resolveInstanceResources(raw.spec?.runtime);
  const internalIPs = collectInstanceInternalIPs(raw);

  return {
    uid: raw.metadata?.uid ?? '',
    name: raw.metadata?.name ?? '',
    namespace: raw.metadata?.namespace,
    createdAt: raw.metadata?.creationTimestamp
      ? new Date(raw.metadata.creationTimestamp)
      : new Date(),
    workloadName: labels[INSTANCE_LABELS.workloadName],
    workloadUid: labels[INSTANCE_LABELS.workloadUid],
    location: labels[INSTANCE_LABELS.location],
    placement: labels[INSTANCE_LABELS.placementName],
    instanceType: raw.spec?.runtime?.resources?.instanceType,
    cpu,
    memory,
    image: container?.image,
    ports: (container?.ports ?? []).map((p) => `${p.port}/${p.protocol ?? 'TCP'}`),
    containers: (raw.spec?.runtime?.sandbox?.containers ?? []).flatMap((c) =>
      c.name ? [c.name] : []
    ),
    runtimeClass: raw.spec?.runtime?.class || undefined,
    status: deriveInstanceStatus(conditions),
    externalIP: assignments?.externalIP,
    internalIP: assignments?.networkIP,
    internalIPs,
    conditions: conditions.map((c) => ({
      type: c.type ?? '',
      status: c.status ?? 'Unknown',
      reason: c.reason,
      message: c.message,
      lastTransitionTime: c.lastTransitionTime,
    })),
  };
}

export function toInstanceList(items: RawInstance[]): Instance[] {
  return items.map(toInstance);
}
