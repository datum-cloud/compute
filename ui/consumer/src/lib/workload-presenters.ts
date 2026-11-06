/**
 * Display helpers shared by the workloads card grid and table views.
 */
import { instanceFailure, QUOTA_REASONS } from '../adapter';
import { formatLocationNames, formatLocationSelector, type LocationIndex } from './locations';
import {
  workloadHealthToBadgeType,
  type Instance,
  type Workload,
  type WorkloadHealth,
  type WorkloadPlacementRegion,
} from '../schema';

/**
 * Dot colors come from datum-ui's badge tokens, the palette its status badges
 * use. They're set inline because the host compiles only the utility classes it
 * uses itself, and these tokens have no utility class there.
 */
export const STATUS_TONE_COLOR: Record<StatusTone, string> = {
  ok: 'var(--color-badge-success)',
  warn: 'var(--color-badge-warning)',
  error: 'var(--color-badge-danger)',
  muted: 'var(--color-badge-muted)',
};

/**
 * Dot color per raw health, for places that show a health directly (region
 * rows). Shares `STATUS_TONE_COLOR`, plus datum-ui's info token while deploying.
 */
export const HEALTH_DOT_COLOR: Record<WorkloadHealth, string> = {
  Available: STATUS_TONE_COLOR.ok,
  Degraded: STATUS_TONE_COLOR.warn,
  Deploying: 'var(--color-badge-info)',
  Unavailable: STATUS_TONE_COLOR.error,
  Unknown: STATUS_TONE_COLOR.muted,
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
  return `${count} failing: ${first.message || instanceReasonLabel(first.reason) || first.reason}`;
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

/** Badge wording for a reason, read from the table's own keys only. */
function instanceReasonLabel(reason: string | undefined): string | undefined {
  return reason && Object.hasOwn(INSTANCE_REASON_LABEL, reason) ? INSTANCE_REASON_LABEL[reason] : undefined;
}

/**
 * Instance badge text: the status, or for a pending/failed instance the step
 * or failure it reports ("Starting", "Image unavailable").
 */
export function instanceStatusLabel(instance: Instance): string {
  if (instance.status === 'Available' || instance.status === 'Unknown') return instance.status;
  const failure = instanceFailure(instance.conditions);
  if (failure) return instanceReasonLabel(failure.reason) ?? 'Failed';
  for (const type of ['Ready', 'Available', 'Programmed']) {
    const reason = instance.conditions.find((c) => c.type === type && c.status !== 'True')?.reason;
    const label = instanceReasonLabel(reason);
    if (label) return label;
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

/** The four states a dot can show; the project home page uses the same set. */
export type StatusTone = 'ok' | 'warn' | 'error' | 'muted';

const BADGE_TONE: Record<ReturnType<typeof workloadHealthToBadgeType>, StatusTone> = {
  success: 'ok',
  warning: 'warn',
  danger: 'error',
  // `StatusTone` has no blue, matching the home page's four states: a
  // deploy in progress needs no attention, so it reads as neutral.
  info: 'muted',
  muted: 'muted',
};

/**
 * Dot tone and sentence-case wording for a workload, as the list shows it.
 * Tones come from the same `workloadHealthToBadgeType` mapping the status
 * badge uses, so the list and the badge agree. A deploy turns `warn` once
 * it is blocked on quota or past `DEPLOY_STALLED_AFTER_MS`; a stalled one
 * also carries a `reason`, so the state isn't shown by color alone.
 */
export function workloadStatusTone(
  workload: Workload,
  now: number = Date.now()
): { tone: StatusTone; label: string; reason?: string } {
  if (workload.deleting) return { tone: 'muted', label: 'Deleting' };
  const deploy = deployStatus(workload, now);
  if (deploy?.tone === 'warning') {
    // A stalled deploy keeps the label "Deploying", so say why it's amber
    // rather than leaving the color to carry it.
    return deploy.title === deploy.label
      ? { tone: 'warn', label: deploy.label }
      : { tone: 'warn', label: deploy.label, reason: deploy.title };
  }
  if (deploy) return { tone: 'muted', label: deploy.label };
  return { tone: BADGE_TONE[workloadHealthToBadgeType(workload.health)], label: statusLabel(workload) };
}

/** Sort weight per tone: problems first, then neutral states, healthy last. */
const STATUS_TONE_ORDER: Record<StatusTone, number> = { error: 0, warn: 1, muted: 2, ok: 3 };

/**
 * Status column sort weight, from the tone the row shows (`workloadStatusTone`)
 * rather than the raw health: a deleting workload sorts with the neutral
 * states, not by its last health, and a stalled deploy sorts with warnings.
 */
export function workloadStatusSortWeight(workload: Workload, now: number = Date.now()): number {
  return STATUS_TONE_ORDER[workloadStatusTone(workload, now).tone];
}

/** Plain words for the reasons an instance's `Ready` condition reports. */
export const READY_REASON_COPY: Readonly<Record<string, string>> = {
  Provisioning: 'Starting up',
  InstanceCrashing: 'Crashing on start',
  ImageUnavailable: "Image can't be pulled",
  ConfigurationError: 'Configuration rejected',
  SchedulingGatesPresent: 'Waiting to be scheduled',
  NetworkFailedToCreate: "Network couldn't be set up",
};

/** `Ready` reasons that need a new image or spec to clear, so they read as failed. */
const UNRECOVERABLE_READY_REASONS: ReadonlySet<string> = new Set([
  'ImageUnavailable',
  'ConfigurationError',
  'NetworkFailedToCreate',
]);

/** An instance row's dot: tone, short label and, when known, why it isn't serving. */
export interface InstanceReadiness {
  tone: StatusTone;
  label: string;
  reason?: string;
}

type Condition = Instance['conditions'][number];

/** The plain-words reason for a condition: mapped copy, then its message, then the raw reason. */
function conditionReason(condition: Condition | undefined): string | undefined {
  if (!condition) return undefined;
  // Own keys only: a raw reason like `constructor` must not hit Object.prototype.
  const copy = condition.reason && Object.hasOwn(READY_REASON_COPY, condition.reason)
    ? READY_REASON_COPY[condition.reason]
    : undefined;
  return copy || condition.message || condition.reason || undefined;
}

/**
 * Dot tone, label and reason for one instance on the workload Overview.
 *
 * Programmed but not Ready is amber ("running but not Ready") even when the
 * adapter has already called the instance Failed: it is placed and running,
 * and the reason says why it isn't serving. The exceptions are reasons that
 * won't clear without a new image or spec, which are red. Failed before it
 * was programmed is red too. Anything still on its way up is muted "Starting".
 */
export function instanceReadiness(instance: Instance): InstanceReadiness {
  const find = (type: string) => instance.conditions.find((c) => c.type === type);
  const ready = find('Ready');
  const programmed = find('Programmed');

  if (instance.status === 'Available' && ready?.status !== 'False') {
    return { tone: 'ok', label: 'Running' };
  }
  // Placed and booting: still on its way up, not a problem yet.
  if (programmed?.status === 'True' && ready?.status === 'False' && ready.reason === 'Provisioning') {
    return { tone: 'muted', label: 'Starting', reason: 'Booting' };
  }
  if (programmed?.status === 'True' && ready?.status !== 'True') {
    const unrecoverable = UNRECOVERABLE_READY_REASONS.has(ready?.reason ?? '');
    const base: InstanceReadiness = unrecoverable
      ? { tone: 'error', label: 'Failed' }
      : { tone: 'warn', label: 'Not ready' };
    return withReason(base, conditionReason(ready));
  }
  // A reason that won't clear on its own is a failure even before the
  // instance is programmed, e.g. its network could not be created.
  if (ready?.status === 'False' && UNRECOVERABLE_READY_REASONS.has(ready.reason ?? '')) {
    return withReason({ tone: 'error', label: 'Failed' }, conditionReason(ready));
  }
  if (instance.status === 'Failed') {
    const failure = instanceFailure(instance.conditions) ?? ready ?? programmed;
    return withReason({ tone: 'error', label: 'Failed' }, conditionReason(failure));
  }
  // "Starting · Starting up" says the same thing twice.
  const pending = [ready, programmed].find((c) => c && c.status !== 'True' && c.reason !== 'Provisioning');
  return withReason({ tone: 'muted', label: 'Starting' }, conditionReason(pending));
}

function withReason(base: InstanceReadiness, reason: string | undefined): InstanceReadiness {
  return reason ? { ...base, reason } : base;
}

/** An instance's published private names, or why it has none. */
export type PrivateNameState ={ kind: 'names'; names: string[] } | { kind: 'missing'; reason: string };

/**
 * The private DNS names the platform published for an instance, or why there
 * are none. There's no DNS-specific condition, so availability is the only
 * signal: names are assigned once the instance runs.
 */
export function privateNameState(instance: Instance): PrivateNameState {
  if (instance.hostnames.length > 0) return { kind: 'names', names: [...instance.hostnames] };
  return {
    kind: 'missing',
    reason:
      instance.status === 'Available'
        ? 'No private name published on this network'
        : 'Assigned once the instance is running',
  };
}

function isEmptyValue(value: unknown): boolean {
  return value === undefined || value === null || value === '' || (Array.isArray(value) && value.length === 0);
}

/** Detail rows with a value worth showing. `0` and `false` count as values. Returns a new array. */
export function compactDetailRows<T extends { value: unknown }>(rows: readonly T[]): T[] {
  return rows.filter((row) => !isEmptyValue(row.value));
}

/** What `instancesSectionState` needs to know about the instances query. */
export interface InstancesSectionInput {
  count: number;
  /** The instances query hasn't answered yet. */
  loading: boolean;
  /** The instances query failed; `status` is the HTTP status when known. */
  error?: { status?: number } | null;
  deploying: boolean;
  deleting: boolean;
}

/** Rows, placeholder rows, or a notice with its copy. */
export type InstancesSectionState =
  | { kind: 'loading' }
  | { kind: 'rows' }
  | { kind: 'notice'; title: string; description?: string };

/**
 * What the Overview's Instances section shows. An unanswered or failed query
 * must not read as "no instances": rows hold their place while loading, a
 * failure says so, and rows already on screen outlive a failed refresh.
 */
export function instancesSectionState({
  count,
  loading,
  error,
  deploying,
  deleting,
}: InstancesSectionInput): InstancesSectionState {
  if (count > 0) return { kind: 'rows' };
  if (loading) return { kind: 'loading' };
  if (error) {
    return error.status === 403
      ? {
          kind: 'notice',
          title: 'Access restricted',
          description: "You don't have permission to view this workload's instances.",
        }
      : {
          kind: 'notice',
          title: "Couldn't load instances",
          description: 'They will appear here once the next refresh succeeds.',
        };
  }
  if (deleting) return { kind: 'notice', title: 'Instances are shutting down' };
  if (deploying) return { kind: 'notice', title: 'Instances are being created' };
  return {
    kind: 'notice',
    title: 'No running instances',
    description: 'This workload has no instances right now, for example after scaling to zero.',
  };
}

export interface OverviewHealth {
  health: WorkloadHealth;
  /** Why instances are failing, or the workload's own unavailable message. */
  failure?: string;
  /** Instance failures overrode the workload's health; no deploy progress applies. */
  instancesFailing: boolean;
}

/**
 * Effective health for the workload Overview and Topology. A crash loop or
 * bad image still reads as provisioning at the workload level; only the
 * instances say otherwise, so their failures win unless the workload is
 * available or deleting. Otherwise an unavailable workload explains itself
 * through its `Available` condition.
 */
export function overviewHealth(workload: Workload, instances: readonly Instance[]): OverviewHealth {
  const instanceFailure =
    workload.health !== 'Available' && !workload.deleting ? instanceFailureSummary(instances) : undefined;
  if (instanceFailure) return { health: 'Unavailable', failure: instanceFailure, instancesFailing: true };
  const message =
    workload.health === 'Unavailable'
      ? workload.conditions.find((c) => c.type === 'Available')?.message
      : undefined;
  return message
    ? { health: workload.health, failure: message, instancesFailing: false }
    : { health: workload.health, instancesFailing: false };
}

/** The health, deploy and load balancer inputs behind the Overview's status line. */
export interface OverviewStatusInput {
  health: WorkloadHealth;
  deleting: boolean;
  healthyCount: number;
  totalCount: number;
  locationCount: number;
  /** Set while the workload is coming up, see `deployStatus`. */
  deploy?: DeployStatus;
  /** Why instances are failing, e.g. from `instanceFailureSummary`. */
  failure?: string;
  /** The attached load balancer's error, when it has one. */
  albError?: string;
  now?: number;
}

/** What `StatusLine` renders: tone, bold title and muted detail. */
export interface OverviewStatus {
  tone: StatusTone;
  title: string;
  detail: string;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/**
 * The workload Overview's one-line status, from the same inputs the old
 * health strip used. Red for failures, amber for a stalled or quota-blocked
 * deploy, degraded health or a load balancer error; plain otherwise.
 */
export function overviewStatusLine(input: OverviewStatusInput): OverviewStatus {
  const { health, deploy, failure, healthyCount, totalCount, locationCount } = input;
  const available = `${healthyCount}/${totalCount} instances available`;

  if (input.deleting) return { tone: 'muted', title: 'Deleting', detail: 'Instances are shutting down' };
  if (deploy) {
    const detail = [
      deploy.inProgress ? deploy.step : (deploy.message ?? deploy.step),
      `${healthyCount}/${plural(totalCount, 'instance', 'instances')} ready`,
      `started ${formatElapsed(deploy.since, input.now)} ago`,
    ].join(' · ');
    return { tone: deploy.tone === 'warning' ? 'warn' : 'muted', title: deploy.title, detail };
  }
  // Some instances still serve: a partial failure is degraded, not down.
  if (failure && healthyCount > 0) return { tone: 'warn', title: 'Degraded', detail: `${available} · ${failure}` };
  if (failure) return { tone: 'error', title: 'Unavailable', detail: `${failure} · ${available}` };
  if (health === 'Unavailable') {
    return { tone: 'error', title: 'Unavailable', detail: totalCount === 0 ? 'No running instances' : available };
  }
  if (input.albError) return { tone: 'warn', title: health === 'Degraded' ? 'Degraded' : 'Serving normally', detail: `Load balancer: ${input.albError}` };
  if (health === 'Degraded') return { tone: 'warn', title: 'Degraded', detail: available };
  if (health === 'Available') {
    return {
      tone: 'ok',
      title: 'Serving normally',
      detail: `${healthyCount}/${plural(totalCount, 'instance', 'instances')} · ${plural(locationCount, 'location', 'locations')}`,
    };
  }
  return { tone: 'muted', title: 'Waiting for status', detail: 'Workload health has not been reported yet' };
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

/** Workloads list order (cards and table): newest first. Returns a new array. */
export function newestFirst(workloads: readonly Workload[]): Workload[] {
  return [...workloads].sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime());
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

/**
 * Does `workload` match a free-text search? Shared by both list views so the
 * cards grid and the table agree on what a query means: name, image, runtime
 * type, tags, placement locations, attached networks, and the published
 * hostname.
 *
 * `publishedHostname` is passed in rather than read off the workload because
 * the published URL comes from a separate HTTPProxy lookup in the page.
 */
export function matchesWorkloadSearch(
  workload: Workload,
  search: string,
  publishedHostname?: string
): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [
    workload.name,
    workload.image,
    workload.runtimeType,
    ...workload.tags,
    ...workload.locations,
    ...workload.networks,
    publishedHostname,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

/** "just now", "45s ago", "12m ago", "3h ago", "4d ago". */
export function formatAgo(since: Date, now: number = Date.now()): string {
  const secs = Math.max(0, Math.floor((now - since.getTime()) / 1000));
  if (secs < 5) return 'just now';
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

/** An instance name split so the part after the workload name can be emphasised. */
export interface InstanceNameParts {
  /** The workload name and its trailing hyphen, or empty when it doesn't lead. */
  prefix: string;
  rest: string;
}

export function instanceNameParts(instanceName: string, workloadName: string): InstanceNameParts {
  const prefix = `${workloadName}-`;
  return workloadName && instanceName.startsWith(prefix) && instanceName.length > prefix.length
    ? { prefix, rest: instanceName.slice(prefix.length) }
    : { prefix: '', rest: instanceName };
}

/** What a crash message says about the last exit, until the API reports it as fields. */
export interface CrashDetail {
  exitCode?: number;
  restarts?: number;
}

/**
 * Reads the exit code and restart count out of a `RuntimeReady` crash message
 * ("Container "app" exited with code 137 and has restarted 3 times"). The
 * instance status has no fields for them yet (datum-cloud/compute#626).
 */
export function crashDetail(message: string | undefined): CrashDetail | undefined {
  if (!message) return undefined;
  const exit = /exited with code (\d+)/.exec(message);
  const restarts = /restarted (\d+) times?/.exec(message);
  if (!exit && !restarts) return undefined;
  const detail: CrashDetail = {};
  if (exit) detail.exitCode = Number(exit[1]);
  if (restarts) detail.restarts = Number(restarts[1]);
  return detail;
}

/** "Killed, usually out of memory (exit 137) · 3 restarts". */
export function crashDetailText(detail: CrashDetail): string {
  const parts: string[] = [];
  if (detail.exitCode === 137) parts.push('Killed, usually out of memory (exit 137)');
  else if (detail.exitCode !== undefined) parts.push(`Exited with code ${detail.exitCode}`);
  if (detail.restarts !== undefined) parts.push(plural(detail.restarts, 'restart', 'restarts'));
  return parts.join(' · ');
}

/** The crash detail of the first crashing instance, with the instance it came from. */
export function firstCrash(
  instances: readonly Instance[]
): { instance: Instance; detail: CrashDetail } | undefined {
  for (const instance of instances) {
    const failing = instance.conditions.find(
      (c) => (c.type === 'RuntimeReady' || c.type === 'Ready') && c.status === 'False' && c.reason === 'InstanceCrashing'
    );
    const detail = crashDetail(failing?.message);
    if (detail) return { instance, detail };
  }
  return undefined;
}

/**
 * Instances still on their way up: none has failed, and at least one is
 * starting. The workload reports Available as soon as a placement exists,
 * seconds before its instances serve, so the Overview keeps showing deploy
 * progress until this is false.
 */
export function instancesStarting(instances: readonly Instance[]): boolean {
  if (instances.length === 0) return false;
  const readiness = instances.map((instance) => instanceReadiness(instance));
  if (readiness.some((r) => r.tone === 'error' || r.tone === 'warn')) return false;
  return readiness.some((r) => r.tone === 'muted');
}

/** What is wrong with an instance, in plain words, and what to try. */
export interface InstanceProblem {
  tone: 'error' | 'warn';
  title: string;
  /** The platform's own message, shown as reported. */
  message?: string;
  hints: string[];
  /** The logs are likely to say more (the app ran at least once). */
  showLogs: boolean;
}

/**
 * The instance's failure, explained: from its `Ready`/`Programmed` failure
 * reason, or a crash reported by `RuntimeReady`. Undefined while healthy or
 * still starting.
 */
export function instanceProblem(instance: Instance): InstanceProblem | undefined {
  const crash = instance.conditions.find(
    (c) => (c.type === 'RuntimeReady' || c.type === 'Ready') && c.status === 'False' && c.reason === 'InstanceCrashing'
  );
  const failure = crash ?? instanceFailure(instance.conditions);
  if (!failure) return undefined;
  const message = failure.message || undefined;

  switch (failure.reason) {
    case 'ImageUnavailable': {
      const image = instance.image ?? 'the image';
      const anonymous = /anonymous/i.test(message ?? '');
      const dockerHub = /^(docker\.io\/|index\.docker\.io\/|[^./]+(:|$)|[^./]+\/[^./]+(:|$))/.test(instance.image ?? '');
      const hints = [`Check that ${image} exists and its tag is spelled correctly.`];
      hints.push(
        anonymous
          ? 'It was pulled without credentials. A private image needs registry credentials (imagePullSecrets) on the workload.'
          : 'If the image is private, add registry credentials (imagePullSecrets) to the workload.'
      );
      if (dockerHub && anonymous) {
        hints.push('Docker Hub can refuse anonymous pulls when it rate-limits. Retry later, or use the same image from another registry.');
      }
      return { tone: 'error', title: "The image can't be pulled", message, hints, showLogs: false };
    }
    case 'InstanceCrashing': {
      const detail = crashDetail(message);
      const hints = ['Check the logs for the error just before each exit.'];
      if (detail?.exitCode === 137) {
        hints.unshift('Exit 137 usually means it ran out of memory. Try a larger instance type, or lower the app’s memory use.');
      }
      return {
        tone: 'warn',
        title: 'The app keeps crashing',
        message: detail ? crashDetailText(detail) : message,
        hints,
        showLogs: true,
      };
    }
    case 'ConfigurationError':
      return {
        tone: 'error',
        title: 'The configuration was rejected',
        message,
        hints: ['Fix the field the message names in the workload spec, then redeploy.'],
        showLogs: false,
      };
    default:
      return { tone: 'error', title: 'The instance failed', message, hints: [], showLogs: true };
  }
}
