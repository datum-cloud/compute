/**
 * Facet toolbar for an instance list: status, location, placement pickers
 * (same datum-ui MultiSelect, 32px tall, as the Metrics tab's filters) and a
 * "Problems only" chip. Location / placement pickers only appear when there
 * is more than one value to choose between.
 */
import type { FacetOption, InstanceListFilterState } from '../lib/instance-list-filters';
import { Button } from '@datum-cloud/datum-ui/button';
import { MultiSelect } from '@datum-cloud/datum-ui/multi-select';
import { useMemo } from 'react';

function toOptions(facet: FacetOption[]) {
  return facet.map((option) => ({
    value: option.value,
    label: `${option.label} (${option.count})`,
  }));
}

const PICKER_CLASS = 'bg-card h-8 min-h-8 w-full sm:w-auto sm:min-w-40';

export function InstanceListFilters({ filters }: { filters: InstanceListFilterState }) {
  const statusOptions = useMemo(() => toOptions(filters.facets.status), [filters.facets.status]);
  const locationOptions = useMemo(
    () => toOptions(filters.facets.location),
    [filters.facets.location],
  );
  const placementOptions = useMemo(
    () => toOptions(filters.facets.placement),
    [filters.facets.placement],
  );
  const problemsOn = filters.picks.problemsOnly;

  return (
    <div
      className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center"
      data-testid="compute-plugin-instance-filters"
    >
      <MultiSelect
        className={PICKER_CLASS}
        options={statusOptions}
        value={[...filters.picks.statuses]}
        onValueChange={filters.setStatuses}
        placeholder="All statuses"
        maxCount={1}
        emptyContent="No statuses found."
      />
      {filters.facets.location.length > 1 ? (
        <MultiSelect
          className={PICKER_CLASS}
          options={locationOptions}
          value={[...filters.picks.locations]}
          onValueChange={filters.setLocations}
          placeholder="All locations"
          maxCount={1}
          emptyContent="No locations found."
        />
      ) : null}
      {filters.facets.placement.length > 1 ? (
        <MultiSelect
          className={PICKER_CLASS}
          options={placementOptions}
          value={[...filters.picks.placements]}
          onValueChange={filters.setPlacements}
          placeholder="All placements"
          maxCount={1}
          emptyContent="No placements found."
        />
      ) : null}
      {/* datum-ui has no toggle; a Button with aria-pressed and a swapped theme is the chip. */}
      <Button
        type={problemsOn ? 'primary' : 'secondary'}
        theme={problemsOn ? 'light' : 'outline'}
        size="small"
        className="h-8 whitespace-nowrap"
        aria-pressed={problemsOn}
        onClick={() => filters.setProblemsOnly(!problemsOn)}
        data-e2e="instances-problems-only"
      >
        Problems only · {filters.facets.problems}
      </Button>
      {filters.isFiltered ? (
        <Button
          type="secondary"
          theme="link"
          size="small"
          className="h-8"
          onClick={filters.clearAll}
          data-e2e="instances-clear-filters"
        >
          Clear filters
        </Button>
      ) : null}
    </div>
  );
}
