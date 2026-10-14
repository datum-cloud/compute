/**
 * Workload Activity tab: the workload's own changes (who created or updated
 * it) and what happened to its instances in each region.
 */
import { ResourceActivity } from '../components/resource-activity';
import { computeResourceLinkResolver, workloadActivityFilter } from '../lib/activity';
import { useWorkloadOutlet } from './workload-outlet-context';
import { useMemo } from 'react';

export default function WorkloadActivity() {
  const { workload, projectId, overviewHref } = useWorkloadOutlet();
  const resolver = useMemo(
    () => computeResourceLinkResolver(workload.name, overviewHref),
    [workload.name, overviewHref]
  );
  return (
    <ResourceActivity
      projectId={projectId}
      filter={workloadActivityFilter(workload.name)}
      liveNameMatch={workload.name}
      resourceLinkResolver={resolver}
    />
  );
}
