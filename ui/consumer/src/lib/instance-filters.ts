/**
 * Region / instance filters for the workload Metrics tab, kept on the
 * `regions` and `instances` search params (comma-separated, like the
 * portal's ALB metrics filters) so a filtered view can be shared.
 *
 * A region is the Location catalog's region code ("us-east-1"), the same
 * label the ALB pages filter on. Instances whose location is missing from the
 * catalog fall back to their raw location name.
 */
import type { Instance } from '../schema';
import { formatLocationName, type LocationIndex } from './locations';
import { sortNatural } from './series-view';
import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router';

export const REGIONS_PARAM = 'regions';
export const INSTANCES_PARAM = 'instances';

export function parseListParam(raw: string | null): string[] {
  if (!raw) return [];
  return [...new Set(raw.split(',').map((value) => value.trim()).filter(Boolean))];
}

export type FilteredInstances = {
  /** Regions present on at least one instance, name-sorted. */
  regions: string[];
  /** Picked regions that at least one instance is in. */
  pickedRegions: string[];
  /** Instances the instance picker offers: every instance in the selected regions. */
  instanceOptions: Instance[];
  /** Picked instances that are still offered under the region filter. */
  pickedInstances: string[];
  /** Instances the charts should cover. */
  matching: Instance[];
};

/**
 * Picks that match nothing are ignored, and dropped from the URL the next time
 * that picker changes: a region no instance is in (a link from another
 * workload, or before the Location catalog has loaded), or an instance outside
 * the chosen regions or no longer running.
 */
export function filterInstances(
  instances: readonly Instance[],
  selectedRegions: readonly string[],
  selectedInstances: readonly string[],
  regionOf: (instance: Instance) => string | undefined = (instance) => instance.location
): FilteredInstances {
  const regions = sortNatural([
    ...new Set(instances.map(regionOf).filter((region): region is string => !!region)),
  ]);
  const known = new Set(regions);
  const pickedRegions = selectedRegions.filter((region) => known.has(region));
  const regionSet = new Set(pickedRegions);
  const instanceOptions =
    regionSet.size > 0
      ? instances.filter((instance) => {
          const region = regionOf(instance);
          return !!region && regionSet.has(region);
        })
      : [...instances];
  const picked = new Set(selectedInstances);
  const pickedInstances = instanceOptions
    .filter((instance) => picked.has(instance.name))
    .map((instance) => instance.name);
  const matching =
    pickedInstances.length > 0
      ? instanceOptions.filter((instance) => picked.has(instance.name))
      : instanceOptions;
  return { regions, pickedRegions, instanceOptions, pickedInstances, matching };
}

export function useInstanceFilters(instances: readonly Instance[], locationIndex: LocationIndex) {
  const [searchParams, setSearchParams] = useSearchParams();
  const regionsRaw = searchParams.get(REGIONS_PARAM);
  const instancesRaw = searchParams.get(INSTANCES_PARAM);
  const selectedRegions = useMemo(() => parseListParam(regionsRaw), [regionsRaw]);
  const selectedInstances = useMemo(() => parseListParam(instancesRaw), [instancesRaw]);

  const filtered = useMemo(
    () =>
      filterInstances(instances, selectedRegions, selectedInstances, (instance) =>
        instance.location ? formatLocationName(instance.location, locationIndex) : undefined
      ),
    [instances, selectedRegions, selectedInstances, locationIndex]
  );

  const setParam = useCallback(
    (key: string, values: string[]) => {
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          if (values.length > 0) next.set(key, values.join(','));
          else next.delete(key);
          return next;
        },
        { replace: true }
      );
    },
    [setSearchParams]
  );

  const setRegions = useCallback((values: string[]) => setParam(REGIONS_PARAM, values), [setParam]);
  const setInstances = useCallback((values: string[]) => setParam(INSTANCES_PARAM, values), [setParam]);

  return {
    ...filtered,
    setRegions,
    setInstances,
    /** The viewer picked specific instances, so charts should draw all of them. */
    instancesPicked: filtered.pickedInstances.length > 0,
    isFiltered: filtered.pickedRegions.length > 0 || filtered.pickedInstances.length > 0,
  };
}
