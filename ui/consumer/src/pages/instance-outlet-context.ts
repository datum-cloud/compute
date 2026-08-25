/**
 * Instance tab bodies share this context from the layout shell so Overview and
 * Logs do not re-fetch or remount breadcrumbs / title / tabs.
 */
import type { Instance } from '../schema';
import { useOutletContext } from 'react-router';

export type InstanceOutletContext = {
  instance: Instance;
  workloadName?: string;
  logsHref: string;
};

export function useInstanceOutlet(): InstanceOutletContext {
  return useOutletContext<InstanceOutletContext>();
}
