/**
 * Raw K8s resource → schema mappers — adapted from `ui/consumer/src/adapter.ts`
 * (same hand-written `Raw*` shapes against `api/v1alpha/workload_types.go` /
 * `instance_types.go`, no generated-SDK dependency), extended with
 * `spec.controller.schedulingGates` and `status.suspended` for Instance —
 * both real "why isn't this starting" signals the consumer dashboard doesn't
 * surface, but this support view is built around.
 */
import type { Condition, Instance, Workload, WorkloadHealth, WorkloadPlacement } from './schema';

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
  creationTimestamp?: string;
  labels?: Record<string, string>;
}

interface RawSandboxContainer {
  name?: string;
  image?: string;
}

interface RawRuntime {
  resources?: {
    instanceType?: string;
    requests?: Record<string, string>;
  };
  sandbox?: { containers?: RawSandboxContainer[] };
  virtualMachine?: unknown;
}

function toConditions(conditions: RawCondition[]): Condition[] {
  return conditions.map((c) => ({
    type: c.type ?? '',
    status: c.status ?? 'Unknown',
    reason: c.reason,
    message: c.message,
    lastTransitionTime: c.lastTransitionTime,
    observedGeneration: c.observedGeneration,
  }));
}

function deriveHealth(conditions: RawCondition[]): WorkloadHealth {
  if (!conditions || conditions.length === 0) return 'Unknown';

  const available = conditions.find((c) => c.type === 'Available');
  const progressing = conditions.find((c) => c.type === 'Progressing');

  if (!available) return 'Unknown';
  if (available.status === 'True') return 'Available';
  if (available.status === 'False' && progressing?.status === 'True') return 'Degraded';
  if (available.status === 'False') return 'Unavailable';
  return 'Unknown';
}

// ── Workload ─────────────────────────────────────────────────────────────

/** Well-known labels stamped onto instances by the compute controllers. */
export const INSTANCE_LABELS = {
  workloadName: 'compute.datumapis.com/workload-name',
} as const;

interface RawWorkloadPlacement {
  name: string;
  cityCodes?: string[];
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

function deriveReplicasPerRegion(placements: RawWorkloadPlacement[]): number | undefined {
  if (!placements || placements.length === 0) return undefined;

  const mins = placements.map((p) => p.scaleSettings?.minReplicas);
  const first = mins[0];
  if (first === undefined) return undefined;

  return mins.every((m) => m === first) ? first : undefined;
}

function toPlacements(
  placements: RawWorkloadPlacement[],
  statusPlacements: RawWorkloadPlacementStatus[]
): WorkloadPlacement[] {
  const statusByName = new Map(
    statusPlacements
      .filter((s): s is RawWorkloadPlacementStatus & { name: string } => !!s.name)
      .map((s) => [s.name, s])
  );

  return placements.map((p) => {
    const status = statusByName.get(p.name);
    const conditions = status?.conditions ?? [];
    const desired = status?.desiredReplicas ?? p.scaleSettings?.minReplicas ?? 0;
    const ready = status?.readyReplicas ?? 0;
    const current = status?.currentReplicas ?? 0;
    const fromConditions = deriveHealth(conditions);
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
      cityCodes: p.cityCodes ?? [],
      readyReplicas: ready,
      desiredReplicas: desired,
      currentReplicas: current,
      health,
      conditions: toConditions(conditions),
    };
  });
}

export function toWorkload(raw: RawWorkload): Workload {
  const conditions = raw.status?.conditions ?? [];
  const placements = raw.spec?.placements ?? [];
  const runtime = raw.spec?.template?.spec?.runtime;

  return {
    uid: raw.metadata?.uid ?? '',
    name: raw.metadata?.name ?? '',
    namespace: raw.metadata?.namespace,
    createdAt: raw.metadata?.creationTimestamp
      ? new Date(raw.metadata.creationTimestamp)
      : new Date(),
    image: runtime?.sandbox?.containers?.[0]?.image,
    health: deriveHealth(conditions),
    currentReplicas: raw.status?.currentReplicas ?? 0,
    updatedReplicas: raw.status?.updatedReplicas ?? 0,
    readyReplicas: raw.status?.readyReplicas ?? 0,
    desiredReplicas: raw.status?.desiredReplicas ?? 0,
    placements: toPlacements(placements, raw.status?.placements ?? []),
    conditions: toConditions(conditions),
    runtimeType: runtime ? (runtime.sandbox ? 'Container sandbox' : 'Virtual machine') : undefined,
    regions: Array.from(new Set(placements.flatMap((p) => p.cityCodes ?? []))),
    resources: deriveResources(runtime),
    replicasPerRegion: deriveReplicasPerRegion(placements),
  };
}

export function toWorkloadList(items: RawWorkload[]): Workload[] {
  return items.map(toWorkload);
}

// ── Instance ─────────────────────────────────────────────────────────────

export interface RawInstance {
  metadata?: RawObjectMeta;
  spec?: {
    runtime?: RawRuntime;
    controller?: { schedulingGates?: string[] };
  };
  status?: {
    conditions?: RawCondition[];
    networkInterfaces?: {
      assignments?: { networkIP?: string; externalIP?: string };
    }[];
    suspended?: boolean;
  };
}

export interface RawInstanceList {
  items?: RawInstance[];
}

// No explicit "Failed" status field on the API — inferred from the Available
// condition's reason/message text, same heuristic as ui/consumer's adapter.
function deriveInstanceStatus(conditions: RawCondition[]): Instance['status'] {
  if (!conditions || conditions.length === 0) return 'Unknown';

  const available = conditions.find((c) => c.type === 'Available');
  if (!available) return 'Unknown';
  if (available.status === 'True') return 'Available';

  const text = `${available.reason ?? ''} ${available.message ?? ''}`;
  if (/fail|error/i.test(text)) return 'Failed';
  return 'Pending';
}

/** Mirrors `instanceTypeCatalog` in `internal/controller/instance_controller.go`. */
const INSTANCE_TYPE_CATALOG: Record<string, { cpu: string; memory: string }> = {
  'datumcloud/d1-standard-2': { cpu: '1', memory: '2Gi' },
  'd1-standard-2': { cpu: '1', memory: '2Gi' },
};

function resolveInstanceResources(runtime?: RawRuntime): { cpu?: string; memory?: string } {
  const requests = runtime?.resources?.requests ?? {};
  const cpu = requests.cpu;
  const memory = requests.memory;
  if (cpu !== undefined && memory !== undefined) return { cpu, memory };

  const instanceType = runtime?.resources?.instanceType;
  if (instanceType && INSTANCE_TYPE_CATALOG[instanceType]) {
    const catalog = INSTANCE_TYPE_CATALOG[instanceType];
    return { cpu: cpu ?? catalog.cpu, memory: memory ?? catalog.memory };
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
    city: labels['compute.datumapis.com/city-code'],
    placement: labels['compute.datumapis.com/placement-name'],
    instanceType: raw.spec?.runtime?.resources?.instanceType,
    cpu,
    memory,
    image: container?.image,
    status: deriveInstanceStatus(conditions),
    externalIP: assignments?.externalIP,
    internalIP: assignments?.networkIP,
    conditions: toConditions(conditions),
    schedulingGates: raw.spec?.controller?.schedulingGates ?? [],
    suspended: raw.status?.suspended ?? false,
  };
}

export function toInstanceList(items: RawInstance[]): Instance[] {
  return items.map(toInstance);
}
