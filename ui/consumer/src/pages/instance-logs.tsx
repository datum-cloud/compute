/**
 * Instance Logs tab body. Mounted under the instance layout shell via
 * `<Outlet />` — does not own breadcrumbs / title / tabs.
 */
import { InstanceLogsExplorer } from '../components/instance-logs';

export default function InstanceLogs() {
  return <InstanceLogsExplorer />;
}
