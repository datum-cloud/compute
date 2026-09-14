/**
 * Instance tab bodies share this context from the layout shell so Overview,
 * Logs, and Metrics do not re-fetch or remount breadcrumbs / title / tabs.
 */
import type { Instance } from '../schema';
import { useOutletContext } from 'react-router';

export type InstanceOutletContext = {
  instance: Instance;
  workloadName?: string;
  projectId?: string;
  logsHref: string;
  metricsHref: string;
  /** HTTPProxy name when an ALB backs this workload's NetworkService. */
  proxyId?: string;
  /** Canonical/default hostname of the connected ALB, when known. */
  albHostname?: string;
  /** Portal display name of the connected ALB (`app.kubernetes.io/name` or `kubernetes.io/display-name`). */
  albDisplayName?: string;
  /** True while the NetworkService → HTTPProxy lookup is in flight. */
  albLoading: boolean;
};

export function useInstanceOutlet(): InstanceOutletContext {
  return useOutletContext<InstanceOutletContext>();
}
