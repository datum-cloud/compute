/**
 * Whether a workload's pages should poll fast. Kept free of runtime imports
 * so `api.ts` can use it without an import cycle.
 */
import type { Workload } from '../schema';

/** How long after its last change a workload short of ready replicas is still polled fast. */
export const SETTLING_WINDOW_MS = 10 * 60_000;

/**
 * Whether a workload is still settling: deploying, or recently changed with
 * fewer ready replicas than desired. Pages poll faster while it is, so short
 * starting states are seen; a long-broken workload falls back to the normal rate.
 */
export function workloadSettling(workload: Workload, now: number = Date.now()): boolean {
  if (workload.deleting) return false;
  if (workload.health === 'Deploying') return true;
  const changed = (workload.updatedAt ?? workload.createdAt).getTime();
  return workload.readyReplicas < workload.desiredReplicas && now - changed < SETTLING_WINDOW_MS;
}
