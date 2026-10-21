/**
 * Display helpers shared by the workload Instances tab and the Overview's
 * instances panel: a stable "problems first" ordering, the badge type for
 * each status, and the search matcher the instance table filters through.
 */
import { formatLocationName, type LocationIndex } from './locations';
import { instanceStatusToBadgeType, type Instance, type InstanceStatusValue } from '../schema';

/** Sort weight: unhealthy first so problems surface at the top of a status sort. */
export const INSTANCE_STATUS_ORDER: Record<InstanceStatusValue, number> = {
  Failed: 0,
  Pending: 1,
  Unknown: 2,
  Available: 3,
};

export function instanceStatusBadge(status: InstanceStatusValue) {
  return instanceStatusToBadgeType(status);
}

function compareOptional(a: string | undefined, b: string | undefined): number {
  // Instances still missing a label sort after labelled ones.
  if (a === b) return 0;
  if (a === undefined) return 1;
  if (b === undefined) return -1;
  return a.localeCompare(b);
}

function compareIndex(a: number | undefined, b: number | undefined): number {
  if (a === b) return 0;
  if (a === undefined) return 1;
  if (b === undefined) return -1;
  return a - b;
}

/**
 * Status (unhealthy first), then location, then the deployment ordinal, then
 * name — so replicas read in the order the controller numbered them within
 * each location rather than by hash-suffixed names. Returns a new array.
 */
export function sortInstances(instances: readonly Instance[]): Instance[] {
  return [...instances].sort(
    (a, b) =>
      INSTANCE_STATUS_ORDER[a.status] - INSTANCE_STATUS_ORDER[b.status] ||
      compareOptional(a.location, b.location) ||
      compareIndex(a.instanceIndex, b.instanceIndex) ||
      a.name.localeCompare(b.name)
  );
}

/**
 * `datumcloud-d1-standard-2` (and the pre-rename `datumcloud/…`) -> `d1-standard-2`;
 * the vendor prefix is noise in a column.
 */
export function instanceTypeShortName(instanceType?: string): string | undefined {
  if (!instanceType) return undefined;
  return instanceType.replace(/^datumcloud[/-]/, '');
}

/**
 * Case-insensitive substring match over everything an operator might paste
 * into the search box: the name, the location as displayed and as labelled,
 * deployment and placement names, any IP, and the status plus its reason.
 */
export function instanceMatchesSearch(
  instance: Instance,
  search: string,
  locationIndex: LocationIndex
): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [
    instance.name,
    instance.location,
    instance.location ? formatLocationName(instance.location, locationIndex) : undefined,
    instance.workloadDeploymentName,
    instance.placement,
    instance.internalIP,
    instance.externalIP,
    ...instance.internalIPs,
    instance.status,
    instance.statusReason,
    ...instance.schedulingGates,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}
