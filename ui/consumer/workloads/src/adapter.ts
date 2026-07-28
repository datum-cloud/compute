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
  cityCode: 'compute.datumapis.com/city-code',
  placementName: 'compute.datumapis.com/placement-name',
} as const;

interface RawWorkloadPlacement {
  name: string;
  cityCodes?: string[];
  scaleSettings?: { minReplicas?: number; maxReplicas?: number };
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

export function toWorkload(raw: RawWorkload): Workload {
  const conditions = raw.status?.conditions ?? [];
  const placements = raw.spec?.placements ?? [];
  const runtime = raw.spec?.template?.spec?.runtime;

  return {
    uid: raw.metadata?.uid ?? '',
    name: raw.metadata?.name ?? '',
    namespace: raw.metadata?.namespace,
    resourceVersion: raw.metadata?.resourceVersion,
    createdAt: raw.metadata?.creationTimestamp
      ? new Date(raw.metadata.creationTimestamp)
      : new Date(),
    image: runtime?.sandbox?.containers?.[0]?.image,
    health: deriveWorkloadHealth(conditions),
    currentReplicas: raw.status?.currentReplicas ?? 0,
    desiredReplicas: raw.status?.desiredReplicas ?? 0,
    placements: placements.map((p) => p.name),
    runtimeType: runtime ? (runtime.sandbox ? 'Container sandbox' : 'Virtual machine') : undefined,
    regions: Array.from(new Set(placements.flatMap((p) => p.cityCodes ?? []))),
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

export function toInstance(raw: RawInstance): Instance {
  const labels = raw.metadata?.labels ?? {};
  const assignments = raw.status?.networkInterfaces?.[0]?.assignments;
  const container = raw.spec?.runtime?.sandbox?.containers?.[0];
  const conditions = raw.status?.conditions ?? [];

  return {
    uid: raw.metadata?.uid ?? '',
    name: raw.metadata?.name ?? '',
    namespace: raw.metadata?.namespace,
    createdAt: raw.metadata?.creationTimestamp
      ? new Date(raw.metadata.creationTimestamp)
      : new Date(),
    workloadName: labels[INSTANCE_LABELS.workloadName],
    workloadUid: labels[INSTANCE_LABELS.workloadUid],
    city: labels[INSTANCE_LABELS.cityCode],
    placement: labels[INSTANCE_LABELS.placementName],
    instanceType: raw.spec?.runtime?.resources?.instanceType,
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
