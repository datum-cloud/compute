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
  scaleSettings?: { minReplicas?: number; maxReplicas?: number };
}

interface RawWorkloadPlacementStatus {
  name?: string;
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
    template?: { spec?: { runtime?: RawRuntime } };
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

function deriveWorkloadHealth(conditions: RawCondition[]): WorkloadHealth {
  if (!conditions || conditions.length === 0) return 'Unknown';

  const available = conditions.find((c) => c.type === 'Available');
  const progressing = conditions.find((c) => c.type === 'Progressing');

  if (!available) return 'Unknown';

  if (available.status === 'True') return 'Available';
  if (available.status === 'False' && progressing?.status === 'True') return 'Degraded';
  if (available.status === 'False') return 'Unavailable';
  return 'Unknown';
}

/** Builds a human-readable resource summary, e.g. "datumcloud/d1-standard-2 · 1 vCPU · 512Mi". */
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
    const fromConditions = deriveWorkloadHealth(status?.conditions ?? []);
    // When placement conditions are absent, infer from ready/desired so the
    // region status dot still reflects health (green / yellow / red).
    const health =
      fromConditions !== 'Unknown'
        ? fromConditions
        : desired > 0 && ready >= desired
          ? 'Available'
          : ready > 0
            ? 'Degraded'
            : desired > 0
              ? 'Unavailable'
              : 'Unknown';

    return {
      name: p.name,
      locations: (p.locations ?? []).map((location) => location.name),
      readyReplicas: ready,
      desiredReplicas: desired,
      health,
    };
  });
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
    locations: Array.from(new Set(placements.flatMap((p) => (p.locations ?? []).map((location) => location.name)))),
    resources: deriveResources(runtime),
    replicasPerRegion: deriveReplicasPerRegion(placements),
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
    }[];
  };
}

export interface RawInstanceList {
  items?: RawInstance[];
}

// The compute API has no explicit "Failed" status field, so we infer it from
// the Available condition's reason/message text — best-effort, matching PR
// #1315's heuristic.
function deriveInstanceStatus(conditions: RawCondition[]): InstanceStatusValue {
  if (!conditions || conditions.length === 0) return 'Unknown';

  const available = conditions.find((c) => c.type === 'Available');
  if (!available) return 'Unknown';
  if (available.status === 'True') return 'Available';

  const text = `${available.reason ?? ''} ${available.message ?? ''}`;
  if (/fail|error/i.test(text)) return 'Failed';
  return 'Pending';
}

/**
 * Platform instance-type catalog — mirrors `instanceTypeCatalog` in
 * `internal/controller/instance_controller.go`. Most instances only set
 * `instanceType` (no explicit requests); the controller resolves size from
 * this catalog for quota. We do the same for display.
 */
const INSTANCE_TYPE_CATALOG: Record<string, { cpu: string; memory: string }> = {
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

export function toInstance(raw: RawInstance): Instance {
  const labels = raw.metadata?.labels ?? {};
  const assignments = raw.status?.networkInterfaces?.[0]?.assignments;
  const container = raw.spec?.runtime?.sandbox?.containers?.[0];
  const conditions = raw.status?.conditions ?? [];
  const { cpu, memory } = resolveInstanceResources(raw.spec?.runtime);

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
    status: deriveInstanceStatus(conditions),
    externalIP: assignments?.externalIP,
    internalIP: assignments?.networkIP,
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
