/**
 * The workload Overview's deploy states. A deploy takes seconds, faster than
 * the page polls, so these say what can be said truthfully: it's happening,
 * it worked (with a timeline rebuilt from condition timestamps), it's stuck
 * and why, or it failed.
 */
import type { WorkloadDeploymentSummary } from './api';
import {
  DEPLOY_STALLED_AFTER_MS,
  formatElapsed,
  instanceProblem,
  instancesStarting,
  type DeployStatus,
  type InstanceProblem,
  type OverviewStatus,
} from './workload-presenters';
import type { Instance, Workload } from '../schema';

export type DeployPhase = 'deploying' | 'deployed' | 'stalled' | 'failed';

/** How long "Deployed in …" stays up after the last instance is ready. */
export const DEPLOYED_FOR_MS = 60_000;

/** Only a workload this young gets "Deployed in …": older ones are rollouts, not first deploys. */
export const FIRST_DEPLOY_WINDOW_MS = 15 * 60_000;

type Condition = Instance['conditions'][number];

function transitionTime(condition: Condition | undefined): Date | undefined {
  if (!condition?.lastTransitionTime) return undefined;
  const at = new Date(condition.lastTransitionTime);
  return Number.isNaN(at.getTime()) ? undefined : at;
}

/** The latest time `type` turned True, once it is True on every instance. */
function allTrueAt(instances: readonly Instance[], type: string): Date | undefined {
  if (instances.length === 0) return undefined;
  let latest: Date | undefined;
  for (const instance of instances) {
    const condition = instance.conditions.find((c) => c.type === type);
    if (condition?.status !== 'True') return undefined;
    const at = transitionTime(condition) ?? instance.createdAt;
    if (!latest || at > latest) latest = at;
  }
  return latest;
}

/** When the last instance became Ready, once all are. */
export function instancesReadyAt(instances: readonly Instance[]): Date | undefined {
  return allTrueAt(instances, 'Ready');
}

export interface TimelineStep {
  label: string;
  at: Date;
  state: 'done' | 'waiting' | 'failed';
}

/**
 * Created → Placed → Started → Ready, from timestamps the API keeps, so it is
 * complete even when no poll landed mid-deploy. A step only appears once
 * every instance has reached it. `end` adds the step a stalled or failed
 * deploy is stuck on.
 */
export function deployTimeline(
  workload: Workload,
  instances: readonly Instance[],
  end?: { label: string; at: Date; state: 'waiting' | 'failed' }
): TimelineStep[] {
  const steps: TimelineStep[] = [{ label: 'Created', at: workload.createdAt, state: 'done' }];
  if (instances.length > 0) {
    const placedAt = instances.reduce((min, i) => (i.createdAt < min ? i.createdAt : min), instances[0].createdAt);
    const locations = [...new Set(instances.map((i) => i.location).filter(Boolean))];
    steps.push({ label: locations.length ? `Placed in ${locations.join(', ')}` : 'Placed', at: placedAt, state: 'done' });
  }
  const startedAt = allTrueAt(instances, 'Programmed');
  if (startedAt) steps.push({ label: 'Started', at: startedAt, state: 'done' });
  const readyAt = allTrueAt(instances, 'Ready');
  if (readyAt) steps.push({ label: 'Ready', at: readyAt, state: 'done' });
  if (end && !readyAt) steps.push(end);
  return steps;
}

/** Reasons a referenced Secret or ConfigMap blocks a deploy, in plain words. */
const REFERENCED_DATA_PROBLEM: Record<string, { title: string; hint: string }> = {
  SourceNotFound: {
    title: "A Secret or ConfigMap it uses doesn't exist",
    hint: 'Create it in this project, or remove the reference from the workload. The deploy carries on by itself once it exists.',
  },
  SourceUnauthorized: {
    title: "Datum can't read a Secret or ConfigMap it uses",
    hint: 'Check the permissions on the referenced Secret or ConfigMap.',
  },
  SourceTooLarge: {
    title: 'A Secret or ConfigMap it uses is too large',
    hint: 'Split it up or trim it, then redeploy.',
  },
  SourceNameTooLong: {
    title: 'A referenced Secret or ConfigMap name is too long',
    hint: 'Rename it to something shorter and update the reference.',
  },
};

const QUOTA_PROBLEM_REASONS = new Set(['QuotaExceeded', 'QuotaNoBudget']);

/**
 * Why a deploy is stuck, when the API says: a referenced Secret or ConfigMap
 * (reported only on the deployments), quota (on the instances), or the
 * workload's own waiting message. Undefined when nothing names a cause.
 */
export function stallCause(
  deployments: readonly WorkloadDeploymentSummary[],
  instances: readonly Instance[],
  deploy?: DeployStatus
): InstanceProblem | undefined {
  for (const deployment of deployments) {
    const data = deployment.conditions.find((c) => c.type === 'ReferencedDataReady' && c.status === 'False');
    const known = data?.reason && Object.hasOwn(REFERENCED_DATA_PROBLEM, data.reason) ? REFERENCED_DATA_PROBLEM[data.reason] : undefined;
    if (known) return { tone: 'warn', title: known.title, message: data?.message, hints: [known.hint], showLogs: false };
  }
  const quota = instances
    .flatMap((i) => i.conditions)
    .find((c) => c.type === 'QuotaGranted' && c.status === 'False' && QUOTA_PROBLEM_REASONS.has(c.reason ?? ''));
  if (quota || deploy?.label === 'Waiting for quota') {
    return {
      tone: 'warn',
      title: 'Waiting for quota',
      message: quota?.message || deploy?.message,
      hints: [
        'Scale down or delete another workload in this project to free resources.',
        'Or ask Datum for a higher limit. The deploy carries on by itself once quota is granted.',
      ],
      showLogs: false,
    };
  }
  return undefined;
}

export interface DeployState {
  phase: DeployPhase;
  /** When this deploy started. */
  since: Date;
  /** Set once every instance is ready. */
  readyAt?: Date;
  /** What is wrong, for stalled and failed. */
  problem?: InstanceProblem;
  /** The instance a failure comes from. */
  failedInstance?: Instance;
  /** Stalled: the step it is stuck on, short, for the end of the "so far" line. */
  stuckOn?: string;
}

export interface DeployStateInput {
  workload: Workload;
  instances: readonly Instance[];
  deploy?: DeployStatus;
  deployments: readonly WorkloadDeploymentSummary[];
  healthyCount: number;
  now: number;
}

/**
 * Which deploy state the Overview shows, if any:
 * - failed: nothing serves and an instance hit a failure that won't clear on its own;
 * - stalled: still coming up past `DEPLOY_STALLED_AFTER_MS`, or blocked on a named cause;
 * - deploying: still coming up;
 * - deployed: a first deploy whose last instance became ready in the last minute.
 * Undefined once the workload is just running (or being deleted).
 */
export function deployState(input: DeployStateInput): DeployState | undefined {
  const { workload, instances, deploy, deployments, healthyCount, now } = input;
  if (workload.deleting) return undefined;

  if (healthyCount === 0) {
    for (const instance of instances) {
      const problem = instanceProblem(instance);
      if (problem?.tone === 'error') {
        return { phase: 'failed', since: workload.createdAt, problem, failedInstance: instance };
      }
    }
  }

  const coming = !!deploy || instancesStarting(instances);
  if (coming) {
    const since = deploy?.since ?? workload.updatedAt ?? workload.createdAt;
    const cause = stallCause(deployments, instances, deploy);
    const slow = deploy?.tone === 'warning' || now - since.getTime() > DEPLOY_STALLED_AFTER_MS;
    if (cause || slow) {
      return {
        phase: 'stalled',
        since,
        stuckOn: cause?.title ?? deploy?.step ?? 'Starting instances',
        problem: cause ?? {
          tone: 'warn',
          title: `${deploy?.step ?? 'Starting instances'} is taking longer than usual`,
          message: deploy?.message,
          hints: ['Most deploys finish within a minute.', 'The instances below show how far each one has got.'],
          showLogs: false,
        },
      };
    }
    return { phase: 'deploying', since };
  }

  const readyAt = instancesReadyAt(instances);
  if (
    readyAt &&
    now - readyAt.getTime() < DEPLOYED_FOR_MS &&
    now - workload.createdAt.getTime() < FIRST_DEPLOY_WINDOW_MS
  ) {
    return { phase: 'deployed', since: workload.createdAt, readyAt };
  }
  return undefined;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** The card's headline for a deploy state. */
export function deployHeadline(
  state: DeployState,
  counts: { healthy: number; total: number; locations: number },
  now: number
): OverviewStatus {
  const ready = `${counts.healthy}/${plural(counts.total, 'instance', 'instances')} ready`;
  switch (state.phase) {
    case 'deploying':
      return { tone: 'muted', title: 'Deploying', detail: `Started ${formatElapsed(state.since, now)} ago · ${ready}` };
    case 'deployed':
      return {
        tone: 'ok',
        title: `Deployed in ${formatElapsed(state.since, (state.readyAt ?? new Date(now)).getTime())}`,
        detail: `${ready} · ${plural(counts.locations, 'location', 'locations')}`,
      };
    case 'stalled':
      return { tone: 'warn', title: 'Taking longer than expected', detail: `Deploying for ${formatElapsed(state.since, now)} · ${ready}` };
    case 'failed': {
      const what = state.problem?.title ?? 'an instance failed';
      return { tone: 'error', title: 'Deploy failed', detail: `${ready} · ${what.charAt(0).toLowerCase()}${what.slice(1)}` };
    }
  }
}
