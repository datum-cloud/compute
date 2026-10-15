/**
 * Per-resource activity for workloads and instances, read from the platform
 * activity API through the portal's control-plane proxy.
 *
 * Compute records instance events against the Instance, with only a link back
 * to its Workload, and the activity API does not let a query filter on links
 * (`spec.links`) or on where an event came from (`spec.source`). A workload's
 * feed therefore matches its own records by name and its instances' and
 * deployments' records by name prefix: compute names deployments
 * `<workload>-<placement>-<location>` and instances `<deployment>-<n>`.
 * A workload named `api` also matches `api-v2`'s instances; filtering on the
 * link back to the workload would remove that, once the API allows it
 * (milo-os/activity#261).
 */
import { getProjectScopedBase } from './api';
import { ActivityApiClient, type ResourceLinkResolver } from '@datum-cloud/activity-ui';
import { useMemo } from 'react';

export const COMPUTE_API_GROUP = 'compute.datumapis.com';

function celString(value: string): string {
  return `'${value.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
}

/** The workload's own records plus those of its deployments and instances. */
export function workloadActivityFilter(workloadName: string): string {
  const name = celString(workloadName);
  const prefix = celString(`${workloadName}-`);
  return (
    `spec.resource.apiGroup == '${COMPUTE_API_GROUP}' && (` +
    `(spec.resource.kind == 'Workload' && spec.resource.name == ${name}) || ` +
    `(spec.resource.kind in ['WorkloadDeployment', 'Instance'] && spec.resource.name.startsWith(${prefix})))`
  );
}

export function instanceActivityFilter(instanceName: string): string {
  return (
    `spec.resource.apiGroup == '${COMPUTE_API_GROUP}' && ` +
    `spec.resource.kind == 'Instance' && spec.resource.name == ${celString(instanceName)}`
  );
}

export function useActivityClient(projectId: string | undefined): ActivityApiClient | undefined {
  return useMemo(
    () => (projectId ? new ActivityApiClient({ baseUrl: getProjectScopedBase(projectId) }) : undefined),
    [projectId]
  );
}

/**
 * Links compute resources in activity summaries to the plugin's own pages,
 * relative to the workload they belong to. Other kinds render as plain text.
 */
export function computeResourceLinkResolver(workloadName: string, workloadHref: string): ResourceLinkResolver {
  return (resource) => {
    if (resource.apiGroup && resource.apiGroup !== COMPUTE_API_GROUP) return undefined;
    if (resource.kind === 'Workload' && resource.name === workloadName) return workloadHref;
    if (resource.kind === 'Instance' && resource.name.startsWith(`${workloadName}-`)) {
      return `${workloadHref}/instances/${encodeURIComponent(resource.name)}`;
    }
    return undefined;
  };
}
