/**
 * View-model types for the compute plugin's two resources — pure data shapes,
 * ported from cloud-portal PR #1315's `app/resources/workloads/workload.schema.ts`
 * and `app/resources/instances/instance.schema.ts`. Combined into one file here
 * since the plugin only exposes list/detail read views for both resources.
 *
 * PR #1315 declared these as Zod schemas, but nothing here ever parses with
 * them — `adapter.ts` builds every value — so they are plain types. That keeps
 * zod out of the bundle every page loads.
 */

// ── Workload ─────────────────────────────────────────────────────────────

/**
 * `Deploying` covers a workload that has not served yet and is still being
 * brought up — the controller reports `Available=False` with a transient
 * reason. See `adapter.ts`'s `deriveWorkloadHealth`.
 */
export type WorkloadHealth = 'Available' | 'Degraded' | 'Deploying' | 'Unavailable' | 'Unknown';

type ConditionStatus = 'True' | 'False' | 'Unknown';

interface WorkloadCondition {
  type: string;
  status: ConditionStatus;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
  observedGeneration?: number;
}

export interface WorkloadPlacementRegion {
  name: string;
  locations: string[];
  /** Topology selector the placement resolves through, when it does not name locations. */
  locationSelector?: string;
  readyReplicas: number;
  desiredReplicas: number;
  health: WorkloadHealth;
}

export interface Workload {
  uid: string;
  name: string;
  namespace?: string;
  resourceVersion?: string;
  createdAt: Date;
  /** Latest status condition transition, when present — used for “Updated … ago”. */
  updatedAt?: Date;
  image?: string;
  health: WorkloadHealth;
  /** Programmed replicas (latest template applied) — not the same as ready. */
  currentReplicas: number;
  /** Ready-to-serve replicas — prefer this for health counts. */
  readyReplicas: number;
  desiredReplicas: number;
  /** Placement names (legacy list consumers). */
  placements: string[];
  /** Spec + status joined for region rows on the home cards. */
  placementRegions: WorkloadPlacementRegion[];
  conditions: WorkloadCondition[];
  // Detail/overview fields
  runtimeType?: string;
  tags: string[];
  ports: string[];
  /** The containers' named ports, which a NetworkService can expose to a load balancer. */
  namedPorts: Array<{ name: string; port: number; protocol: string }>;
  locations: string[];
  resources?: string;
  replicasPerRegion?: number;
  /** Networks the instance template attaches to (`networkInterfaces[].network.name`). */
  networks: string[];
  /** Set once `metadata.deletionTimestamp` is — the finalizer is tearing the workload down. */
  deleting: boolean;
}

export interface WorkloadList {
  items: Workload[];
}

/**
 * Maps a workload health value to a `Badge` `type` prop understood by
 * `@datum-cloud/datum-ui/badge`. Shared by the list and detail pages.
 */
export function workloadHealthToBadgeType(
  health: WorkloadHealth
): 'success' | 'warning' | 'danger' | 'info' | 'muted' {
  switch (health) {
    case 'Available':
      return 'success';
    case 'Deploying':
      return 'info';
    case 'Degraded':
      return 'warning';
    case 'Unavailable':
      return 'danger';
    default:
      return 'muted';
  }
}

// ── Instance ─────────────────────────────────────────────────────────────

export type InstanceStatusValue = 'Available' | 'Pending' | 'Failed' | 'Unknown';

export interface InstanceCondition {
  type: string;
  status: ConditionStatus;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export interface InstanceNetworkInterface {
  /** Spec interface name, for example "eth0". */
  name: string;
  /** VPC network the interface attaches to (spec `network.name`). */
  network?: string;
  /** `status.networkInterfaces[].networkInterfaceRef.name`; the `network` label on traffic series. */
  networkInterfaceRef?: string;
}

export interface Instance {
  uid: string;
  name: string;
  namespace?: string;
  createdAt: Date;
  workloadName?: string;
  workloadUid?: string;
  location?: string;
  placement?: string;
  instanceType?: string;
  /** Allocated CPU — from requests, or resolved from instanceType catalog (e.g. "1"). */
  cpu?: string;
  /** Allocated memory — from requests, or resolved from instanceType catalog (e.g. "2Gi"). */
  memory?: string;
  image?: string;
  ports: string[];
  /** Sandbox container names, in spec order. Empty for virtual machines. */
  containers: string[];
  /** RuntimeClass the instance names; unset selects the catalog default. */
  runtimeClass?: string;
  status: InstanceStatusValue;
  externalIP?: string;
  internalIP?: string;
  /** Host-route in-network IPs Envoy may dial (`upstream_host`), v4 and v6. */
  internalIPs: string[];
  /**
   * Every in-network address the guest answers on, v4 and v6, for display.
   * Unlike `internalIPs` it keeps a delegated IPv6 block, by its base address.
   */
  addresses: string[];
  /** Every externally reachable address, v4 and v6. */
  externalIPs: string[];
  /** Private DNS names allocated in the instance's networks (`status.dns[].hostnames`). */
  hostnames: string[];
  /** Interfaces in spec order, each joined to its status entry by name. */
  networkInterfaces: InstanceNetworkInterface[];
  conditions: InstanceCondition[];
}

export interface InstanceList {
  items: Instance[];
}

/**
 * Maps an instance status value to a `Badge` `type` prop understood by
 * `@datum-cloud/datum-ui/badge`.
 */
export function instanceStatusToBadgeType(
  status: InstanceStatusValue
): 'success' | 'warning' | 'danger' | 'muted' {
  switch (status) {
    case 'Available':
      return 'success';
    case 'Pending':
      return 'warning';
    case 'Failed':
      return 'danger';
    default:
      return 'muted';
  }
}
