/**
 * Workload Topology tab at `:workloadName/topology`: the topology diagram
 * that used to open the Overview, unchanged. Lazily loaded, so the canvas
 * only downloads when someone opens the tab.
 */
import { TopologyCard } from '../components/topology-card';
import { useWorkloadOverviewState } from '../lib/use-workload-overview-state';
import { useWorkloadOutlet } from './workload-outlet-context';

export default function WorkloadTopology() {
  const context = useWorkloadOutlet();
  const { workload, instances, projectId, published, locationIndex } = context;
  const state = useWorkloadOverviewState(context);

  return (
    <TopologyCard
      projectId={projectId}
      workload={workload}
      status={state.status}
      albStatuses={state.albStatuses}
      instances={instances}
      albs={published?.proxies ?? []}
      locationIndex={locationIndex}
      instanceHref={state.instanceHref}
      instanceMetricsHref={state.instanceMetricsHref}
      albHref={state.albHrefFor}
      albMetricsHref={state.albMetricsHrefFor}
    />
  );
}
