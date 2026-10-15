/**
 * Workload Logs tab. ALB access logs for the connected load balancer, plus
 * stdout from every instance, merged newest-first.
 */
import { WorkloadLogsExplorer } from '../components/instance-logs';
import { useWorkloadOutlet } from './workload-outlet-context';

export default function WorkloadLogs() {
  const { projectId, proxyId, instanceNames } = useWorkloadOutlet();
  return (
    <WorkloadLogsExplorer
      projectId={projectId}
      proxyId={proxyId}
      instanceNames={instanceNames}
      className="bg-card"
    />
  );
}
