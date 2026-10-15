/**
 * Workload tab bodies share this context from the layout shell so Overview
 * and Metrics do not re-fetch or remount breadcrumbs / title / tabs.
 */
import type { InstanceIdentityLabel } from '../lib/metrics-queries';
import type { PublishedUrl } from '../lib/api';
import type { LocationIndex } from '../lib/locations';
import type { Instance, Workload } from '../schema';
import { useOutletContext } from 'react-router';

export type WorkloadOutletContext = {
  workload: Workload;
  instances: Instance[];
  projectId?: string;
  proxyId?: string;
  published?: PublishedUrl | null;
  publishedLoading: boolean;
  locationIndex: LocationIndex;
  identityLabel?: InstanceIdentityLabel;
  identityLoading: boolean;
  identityDenied: boolean;
  metricKeys: string[];
  instanceNames: string[];
  overviewHref: string;
  metricsHref: string;
};

export function useWorkloadOutlet(): WorkloadOutletContext {
  return useOutletContext<WorkloadOutletContext>();
}
