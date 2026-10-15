/**
 * Instance Logs tab body. Mounted under the instance layout shell via
 * `<Outlet />` — does not own breadcrumbs / title / tabs.
 */
import { InstanceLogsExplorer } from "../components/instance-logs";
import { useInstanceOutlet } from "./instance-outlet-context";

export default function InstanceLogs() {
  const { instance, projectId, proxyId } = useInstanceOutlet();
  return (
    <InstanceLogsExplorer
      projectId={projectId}
      proxyId={proxyId}
      instanceName={instance.name}
      upstreamIPs={instance.internalIPs}
      className="bg-card"
    />
  );
}
