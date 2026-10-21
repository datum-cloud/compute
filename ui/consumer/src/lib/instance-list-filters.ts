/**
 * Facet filters for an instance list (the workload Instances tab today; a
 * project-wide list later, which can add a workload facet without changing
 * what is here). State lives in URL search params so a filtered view can be
 * shared, under `status`, `location`, `placement` and `problems` so it does
 * not collide with the Metrics tab's `regions` / `instances` params.
 *
 * Facet values are the labels the viewer sees (a location is the catalog's
 * region code), so a link keeps working across instances in the same place.
 */
import { formatLocationName, type LocationIndex } from './locations';
import { INSTANCE_STATUS_ORDER } from './instance-presenters';
import { parseListParam } from './instance-filters';
import { sortNatural } from './series-view';
import type { Instance, InstanceStatusValue } from '../schema';
import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router';

export const STATUS_PARAM = 'status';
export const LOCATION_PARAM = 'location';
export const PLACEMENT_PARAM = 'placement';
export const PROBLEMS_PARAM = 'problems';

/** Suspended and Gated match on their own flags, regardless of derived status. */
export const PSEUDO_STATUSES = ['Suspended', 'Gated'] as const;
export type PseudoStatus = (typeof PSEUDO_STATUSES)[number];
export type StatusFacetValue = InstanceStatusValue | PseudoStatus;

const STATUS_FACET_ORDER: StatusFacetValue[] = [
  ...(Object.keys(INSTANCE_STATUS_ORDER) as InstanceStatusValue[]).sort(
    (a, b) => INSTANCE_STATUS_ORDER[a] - INSTANCE_STATUS_ORDER[b],
  ),
  ...PSEUDO_STATUSES,
];

export type FacetOption = { value: string; label: string; count: number };

export type InstanceFilterPicks = {
  statuses: readonly string[];
  locations: readonly string[];
  placements: readonly string[];
  problemsOnly: boolean;
};

export function instanceIsSuspended(instance: Instance): boolean {
  return instance.suspended;
}

export function instanceIsGated(instance: Instance): boolean {
  return instance.schedulingGates.length > 0;
}

/** Anything an operator would want to look at: not Available, or held back. */
export function instanceProblem(instance: Instance): boolean {
  return (
    instance.status !== 'Available' || instanceIsSuspended(instance) || instanceIsGated(instance)
  );
}

function matchesStatusFacet(instance: Instance, value: string): boolean {
  if (value === 'Suspended') return instanceIsSuspended(instance);
  if (value === 'Gated') return instanceIsGated(instance);
  return instance.status === value;
}

export function locationLabel(
  instance: Instance,
  locationIndex: LocationIndex,
): string | undefined {
  return instance.location ? formatLocationName(instance.location, locationIndex) : undefined;
}

/** Status options in severity order, with zero-count entries omitted. */
export function statusFacetCounts(instances: readonly Instance[]): FacetOption[] {
  return STATUS_FACET_ORDER.map((value) => ({
    value,
    label: value,
    count: instances.filter((instance) => matchesStatusFacet(instance, value)).length,
  })).filter((option) => option.count > 0);
}

function countBy(
  instances: readonly Instance[],
  keyOf: (instance: Instance) => string | undefined,
): FacetOption[] {
  const counts = new Map<string, number>();
  for (const instance of instances) {
    const key = keyOf(instance);
    if (!key) continue;
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return sortNatural([...counts.keys()]).map((value) => ({
    value,
    label: value,
    count: counts.get(value) ?? 0,
  }));
}

export function locationFacetCounts(
  instances: readonly Instance[],
  locationIndex: LocationIndex,
): FacetOption[] {
  return countBy(instances, (instance) => locationLabel(instance, locationIndex));
}

export function placementFacetCounts(instances: readonly Instance[]): FacetOption[] {
  return countBy(instances, (instance) => instance.placement);
}

/**
 * Facets AND together; values within a facet OR. "Problems only" is
 * shorthand for every non-Available status plus Suspended and Gated.
 */
export function applyInstanceFilters(
  instances: readonly Instance[],
  picks: InstanceFilterPicks,
  locationIndex: LocationIndex,
): Instance[] {
  const statuses = new Set(picks.statuses);
  const locations = new Set(picks.locations);
  const placements = new Set(picks.placements);
  return instances.filter((instance) => {
    if (picks.problemsOnly && !instanceProblem(instance)) return false;
    if (statuses.size > 0 && ![...statuses].some((value) => matchesStatusFacet(instance, value))) {
      return false;
    }
    if (locations.size > 0) {
      const label = locationLabel(instance, locationIndex);
      if (!label || !locations.has(label)) return false;
    }
    if (placements.size > 0 && (!instance.placement || !placements.has(instance.placement))) {
      return false;
    }
    return true;
  });
}

/** Keeps only picks some instance matches, so a stale link does not hide everything. */
export function prunePicks(
  picks: InstanceFilterPicks,
  facets: { status: FacetOption[]; location: FacetOption[]; placement: FacetOption[] },
): InstanceFilterPicks {
  const keep = (values: readonly string[], options: FacetOption[]) => {
    const known = new Set(options.map((option) => option.value));
    return values.filter((value) => known.has(value));
  };
  return {
    statuses: keep(picks.statuses, facets.status),
    locations: keep(picks.locations, facets.location),
    placements: keep(picks.placements, facets.placement),
    problemsOnly: picks.problemsOnly,
  };
}

export function parseProblemsParam(raw: string | null): boolean {
  return raw === '1' || raw === 'true';
}

export function useInstanceListFilters(
  instances: readonly Instance[],
  locationIndex: LocationIndex,
) {
  const [searchParams, setSearchParams] = useSearchParams();
  const statusRaw = searchParams.get(STATUS_PARAM);
  const locationRaw = searchParams.get(LOCATION_PARAM);
  const placementRaw = searchParams.get(PLACEMENT_PARAM);
  const problemsOnly = parseProblemsParam(searchParams.get(PROBLEMS_PARAM));

  const facets = useMemo(
    () => ({
      status: statusFacetCounts(instances),
      location: locationFacetCounts(instances, locationIndex),
      placement: placementFacetCounts(instances),
      problems: instances.filter(instanceProblem).length,
    }),
    [instances, locationIndex],
  );

  const picks = useMemo(
    () =>
      prunePicks(
        {
          statuses: parseListParam(statusRaw),
          locations: parseListParam(locationRaw),
          placements: parseListParam(placementRaw),
          problemsOnly,
        },
        facets,
      ),
    [statusRaw, locationRaw, placementRaw, problemsOnly, facets],
  );

  const filtered = useMemo(
    () => applyInstanceFilters(instances, picks, locationIndex),
    [instances, picks, locationIndex],
  );

  const update = useCallback(
    (mutate: (next: URLSearchParams) => void) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          mutate(next);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const setList = useCallback(
    (key: string, values: string[]) =>
      update((next) => {
        if (values.length > 0) next.set(key, values.join(','));
        else next.delete(key);
        // Explicit statuses replace the "problems" shorthand.
        if (key === STATUS_PARAM) next.delete(PROBLEMS_PARAM);
      }),
    [update],
  );

  const setStatuses = useCallback((values: string[]) => setList(STATUS_PARAM, values), [setList]);
  const setLocations = useCallback(
    (values: string[]) => setList(LOCATION_PARAM, values),
    [setList],
  );
  const setPlacements = useCallback(
    (values: string[]) => setList(PLACEMENT_PARAM, values),
    [setList],
  );
  const setProblemsOnly = useCallback(
    (on: boolean) =>
      update((next) => {
        if (on) {
          next.set(PROBLEMS_PARAM, '1');
          next.delete(STATUS_PARAM);
        } else {
          next.delete(PROBLEMS_PARAM);
        }
      }),
    [update],
  );
  const clearAll = useCallback(
    () =>
      update((next) => {
        for (const key of [STATUS_PARAM, LOCATION_PARAM, PLACEMENT_PARAM, PROBLEMS_PARAM]) {
          next.delete(key);
        }
      }),
    [update],
  );

  const isFiltered =
    picks.problemsOnly ||
    picks.statuses.length > 0 ||
    picks.locations.length > 0 ||
    picks.placements.length > 0;

  return {
    filtered,
    facets,
    picks,
    setStatuses,
    setLocations,
    setPlacements,
    setProblemsOnly,
    clearAll,
    isFiltered,
  };
}

export type InstanceListFilterState = ReturnType<typeof useInstanceListFilters>;
