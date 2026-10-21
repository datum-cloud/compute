/**
 * Workload Instances tab: every instance the layout shell already fetched,
 * unhealthy first, narrowed by the URL-backed facet filters and the table's
 * own search. Deployment is a subtitle and search term here rather than its
 * own tab.
 */
import { InstanceListFilters } from '../components/instance-list-filters';
import { InstanceTable } from '../components/instance-table';
import { useInstanceListFilters } from '../lib/instance-list-filters';
import { sortInstances } from '../lib/instance-presenters';
import { useWorkloadOutlet } from './workload-outlet-context';
import { useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router';

export default function WorkloadInstances() {
  const { instances, locationIndex, overviewHref } = useWorkloadOutlet();
  const navigate = useNavigate();
  const filters = useInstanceListFilters(instances, locationIndex);
  const sorted = useMemo(() => sortInstances(filters.filtered), [filters.filtered]);
  const instanceHref = useCallback(
    (name: string) => `${overviewHref}/instances/${name}`,
    [overviewHref],
  );

  return (
    <InstanceTable
      instances={sorted}
      locationIndex={locationIndex}
      instanceHref={instanceHref}
      onOpen={(name) => navigate(instanceHref(name))}
      toolbar={instances.length > 0 ? <InstanceListFilters filters={filters} /> : null}
      emptyTitle={
        filters.isFiltered
          ? 'No instances match the current filters'
          : 'No instances for this workload'
      }
      emptyAction={
        filters.isFiltered ? { label: 'Clear filters', onClick: filters.clearAll } : undefined
      }
    />
  );
}
