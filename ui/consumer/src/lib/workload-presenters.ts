/**
 * Display helpers shared by the workloads card grid and table views.
 */
import { formatLocationNames, formatLocationSelector, type LocationIndex } from './locations';
import type { Workload, WorkloadHealth, WorkloadPlacementRegion } from '../schema';

export const HEALTH_DOT_CLASS: Record<WorkloadHealth, string> = {
  Available: 'bg-green-500',
  Degraded: 'bg-yellow-500',
  Unavailable: 'bg-red-500',
  Unknown: 'bg-muted-foreground',
};

/** Sort weight: unhealthy first so problems surface at the top of a status sort. */
export const HEALTH_ORDER: Record<WorkloadHealth, number> = {
  Unavailable: 0,
  Degraded: 1,
  Unknown: 2,
  Available: 3,
};

export function statusLabel(workload: Workload): string {
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
