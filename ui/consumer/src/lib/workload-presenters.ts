/**
 * Display helpers shared by the workloads card grid and table views.
 */
import { instanceFailure, QUOTA_REASONS } from '../adapter';
import { formatLocationNames, formatLocationSelector, type LocationIndex } from './locations';
import type { Instance, Workload, WorkloadHealth, WorkloadPlacementRegion } from '../schema';

/** `Deploying` has no class: the host doesn't compile a blue one, so its dot is styled inline (`HealthDot`). */
export const HEALTH_DOT_CLASS: Record<WorkloadHealth, string> = {
  Available: 'bg-green-500',
  Degraded: 'bg-yellow-500',
  Deploying: '',
  Unavailable: 'bg-red-500',
  Unknown: 'bg-muted-foreground',
};

/** Sort weight: unhealthy first so problems surface at the top of a status sort. */
export const HEALTH_ORDER: Record<WorkloadHealth, number> = {
  Unavailable: 0,
  Degraded: 1,
  Deploying: 2,
  Unknown: 3,
  Available: 4,
};

/** Tone + wording for a status pill, where the raw health isn't the whole story. */
export interface StatusDisplay {
  tone: 'success' | 'warning' | 'danger' | 'info' | 'muted';
  label: string;
}

/** A workload still deploying this long after it started is flagged as slow. */
export const DEPLOY_STALLED_AFTER_MS = 2 * 60_000;

/** What the controller is doing, keyed by its `Available=False` reason. */
const DEPLOY_STEP: Record<string, string> = {
  NoAvailableDeployments: 'Scheduling instances',
  NoAvailablePlacements: 'Scheduling instances',
  NoMatchingLocation: 'Assigning a location',
  NetworkProvisioning: 'Provisioning network',
  InstancesProvisioning: 'Starting instances',
  ReferencedDataNotReady: 'Distributing configuration',
  AwaitingPropagation: 'Distributing configuration',
  Resolving: 'Distributing configuration',
};

export interface DeployStatus {
  /** Badge text. */
  label: string;
  /** Health strip headline. */
  title: string;
  /** The current step, e.g. "Provisioning network". */
  step: string;
  /** The controller's message when the deploy needs attention. */
  message?: string;
  tone: 'info' | 'warning';
  /** Still moving on its own — show a spinner. */
  inProgress: boolean;
  /** When the workload started deploying. */
  since: Date;
}

/**
 * How a `Deploying` workload is getting on: still progressing, blocked on
 * quota, or past `DEPLOY_STALLED_AFTER_MS` — the last two in a warning tone,
 * since the user probably has to look at them. Undefined for any other health.
 *
 * Timed from the `Available` condition's last transition (the reason changes
 * step by step without moving it), falling back to creation.
 */
export function deployStatus(workload: Workload, now: number = Date.now()): DeployStatus | undefined {
  if (workload.deleting || workload.health !== 'Deploying') return undefined;

  const available = workload.conditions.find((c) => c.type === 'Available');
  const transitioned = available?.lastTransitionTime ? new Date(available.lastTransitionTime) : undefined;
  // Never before creation: a defaulted condition can carry a far older transition.
  const since =
    transitioned && !Number.isNaN(transitioned.getTime()) && transitioned > workload.createdAt
      ? transitioned
      : workload.createdAt;
  const reason = available?.reason ?? '';
  const step = DEPLOY_STEP[reason] ?? 'Waiting for the controller';

  if (QUOTA_REASONS.has(reason)) {
    return {
      label: 'Waiting for quota',
      title: 'Waiting for quota',
      step: 'Waiting for quota',
      message: available?.message,
      tone: 'warning',
      inProgress: false,
      since,
    };
  }
  if (now - since.getTime() > DEPLOY_STALLED_AFTER_MS) {
    return {
      label: 'Deploying',
      title: 'Taking longer than expected',
      step,
      message: available?.message,
      tone: 'warning',
      inProgress: true,
      since,
    };
  }
  return { label: 'Deploying', title: 'Deploying', step, tone: 'info', inProgress: true, since };
}

/** "45s", "3m", "1h 5m" — how long ago `since` was. */
export function formatElapsed(since: Date, now: number = Date.now()): string {
  const secs = Math.max(0, Math.floor((now - since.getTime()) / 1000));
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m`;
  return `${Math.floor(mins / 60)}h ${mins % 60}m`;
}

/**
 * Why a workload's instances won't come up, e.g. "1 instance failing:
 * Back-off pulling image". Undefined when none is failing. The first failure's
 * message stands for the rest — they usually share a cause.
 */
export function instanceFailureSummary(instances: readonly Instance[]): string | undefined {
  const failures = instances.flatMap((instance) => {
    const condition = instanceFailure(instance.conditions);
    return condition ? [condition] : [];
  });
  if (failures.length === 0) return undefined;
  const [first] = failures;
  const count = failures.length === 1 ? '1 instance' : `${failures.length} instances`;
  return `${count} failing: ${first.message || INSTANCE_REASON_LABEL[first.reason ?? ''] || first.reason}`;
}

/** Badge wording for the reasons a not-yet-available instance reports. */
const INSTANCE_REASON_LABEL: Record<string, string> = {
  SchedulingGatesPresent: 'Scheduling',
  PendingQuota: 'Waiting for quota',
  PendingProgramming: 'Provisioning',
  ProgrammingInProgress: 'Provisioning',
  Provisioning: 'Provisioning',
  Starting: 'Starting',
  Stopping: 'Stopping',
  Stopped: 'Stopped',
  Suspended: 'Suspended',
  ImageUnavailable: 'Image unavailable',
  InstanceCrashing: 'Crashing',
  ConfigurationError: 'Configuration error',
};

/**
 * Instance badge text: the status, or for a pending/failed instance the step
 * or failure it reports ("Starting", "Image unavailable").
 */
export function instanceStatusLabel(instance: Instance): string {
  if (instance.status === 'Available' || instance.status === 'Unknown') return instance.status;
  const failure = instanceFailure(instance.conditions);
  if (failure) return INSTANCE_REASON_LABEL[failure.reason ?? ''] ?? 'Failed';
  for (const type of ['Ready', 'Available', 'Programmed']) {
    const reason = instance.conditions.find((c) => c.type === type && c.status !== 'True')?.reason;
    if (reason && INSTANCE_REASON_LABEL[reason]) return INSTANCE_REASON_LABEL[reason];
  }
  return instance.status;
}

export function statusLabel(workload: Workload): string {
  if (workload.deleting) return 'Deleting';
  if (workload.health === 'Deploying') return deployStatus(workload)?.label ?? 'Deploying';
  if (workload.health === 'Available') {
    const ready = workload.readyReplicas;
    const desired = workload.desiredReplicas;
    if (desired > 0 && ready === desired) return 'All healthy';
    return 'Healthy';
  }
  if (workload.health === 'Degraded') {
    const notReady = Math.max(0, workload.desiredReplicas - workload.readyReplicas);
    if (notReady === 1) return '1 degraded';
    if (notReady > 1) return `${notReady} degraded`;
    return 'Degraded';
  }
  if (workload.health === 'Unavailable') return 'Unavailable';
  return 'Unknown';
}

export function regionLabel(region: WorkloadPlacementRegion, index: LocationIndex): string {
  if (region.locations.length > 0) return formatLocationNames(region.locations, index);
  if (region.locationSelector) {
    return formatLocationSelector(region.locationSelector, index) ?? region.locationSelector;
  }
  return region.name;
}

/** `registry/org/app:tag@sha256:…` → `app:tag`. */
export function imageShortName(image?: string): string | undefined {
  if (!image) return undefined;
  const noDigest = image.split('@')[0];
  return noDigest.split('/').pop() || noDigest;
}

/**
 * Workloads for the project home page column: unhealthy first so problems
 * surface, then newest, capped to `limit`. Returns a new array.
 */
export function homeColumnWorkloads(workloads: readonly Workload[], limit = 5): Workload[] {
  return [...workloads]
    .sort(
      (a, b) =>
        HEALTH_ORDER[a.health] - HEALTH_ORDER[b.health] ||
        b.createdAt.getTime() - a.createdAt.getTime()
    )
    .slice(0, limit);
}
