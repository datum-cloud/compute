/**
 * Region / instance pickers for the workload Metrics tab. Same controls and
 * sizing as the portal's ALB metrics filters (datum-ui MultiSelect, 32px tall).
 */
import type { useInstanceFilters } from '../lib/instance-filters';
import { sortNatural } from '../lib/series-view';
import { MultiSelect } from '@datum-cloud/datum-ui/multi-select';
import { useMemo } from 'react';

type InstanceFilterState = ReturnType<typeof useInstanceFilters>;

export function InstanceFilters({ filters }: { filters: InstanceFilterState }) {
  const regionOptions = useMemo(
    () => filters.regions.map((region) => ({ value: region, label: region })),
    [filters.regions]
  );
  const instanceOptions = useMemo(
    () =>
      sortNatural(filters.instanceOptions.map((instance) => instance.name)).map((name) => ({
        value: name,
        label: name,
      })),
    [filters.instanceOptions]
  );

  return (
    <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center">
      <MultiSelect
        className="bg-card h-8 min-h-8 w-full sm:w-auto sm:min-w-44"
        options={regionOptions}
        value={filters.pickedRegions}
        onValueChange={filters.setRegions}
        placeholder="All regions"
        maxCount={1}
        emptyContent="No regions found."
      />
      <MultiSelect
        className="bg-card h-8 min-h-8 w-full sm:w-auto sm:min-w-64"
        options={instanceOptions}
        value={filters.pickedInstances}
        onValueChange={filters.setInstances}
        placeholder="All instances"
        maxCount={1}
        emptyContent="No instances found."
      />
    </div>
  );
}
