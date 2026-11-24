/**
 * What the workload Overview and Topology tabs both derive from the layout
 * shell's outlet context: effective health (instance failures override the
 * workload's own), deploy progress, load balancer statuses and the hrefs the
 * topology and instance rows link to.
 */
import { albStatus, anyAlbProvisioning, type AlbStatus } from './alb-status';
import { DEPLOYED_FOR_MS, instancesReadyAt } from './deploy-presenters';
import { useNow } from './use-now';
import {
  deployStatus,
  instancesStarting,
  overviewHealth,
  type DeployStatus,
  type StatusDisplay,
} from './workload-presenters';
import type { WorkloadOutletContext } from '../pages/workload-outlet-context';
import { workloadHealthToBadgeType, type WorkloadHealth } from '../schema';
import { useCallback, useMemo } from 'react';

/** The load balancer's Overview page in the host portal. */
export function albOverviewHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/overview`;
}

/** The load balancer's Metrics page in the host portal. */
export function albMetricsHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/metrics`;
}

export interface WorkloadOverviewState {
  health: WorkloadHealth;
  /** Why instances are failing, or the workload's own unavailable message. */
  failure?: string;
  deploy?: DeployStatus;
  albStatuses: Record<string, AlbStatus>;
  /** Tone + label for the topology's workload node. */
  status: StatusDisplay;
  now: number;
  healthyCount: number;
  totalCount: number;
  instanceHref: (name: string) => string;
  instanceMetricsHref: (name: string) => string;
  albHrefFor?: (proxyName: string) => string;
  albMetricsHrefFor?: (proxyName: string) => string;
}

/**
 * Derives the Overview and Topology tabs' shared state from the outlet
 * context. Re-renders on a clock tick while a deploy or load balancer is
 * still provisioning, so elapsed times stay current.
 */
export function useWorkloadOverviewState({
  workload,
  instances,
  projectId,
  published,
  overviewHref,
}: Pick<WorkloadOutletContext, 'workload' | 'instances' | 'projectId' | 'published' | 'overviewHref'>): WorkloadOverviewState {
  const instanceHref = useCallback((name: string) => `${overviewHref}/instances/${name}`, [overviewHref]);
  const instanceMetricsHref = useCallback(
    (name: string) => `${overviewHref}/instances/${name}/metrics`,
    [overviewHref]
  );
  const albHrefFor = useMemo(
    () => (projectId ? (proxyName: string) => albOverviewHref(projectId, proxyName) : undefined),
    [projectId]
  );
  const albMetricsHrefFor = useMemo(
    () => (projectId ? (proxyName: string) => albMetricsHref(projectId, proxyName) : undefined),
    [projectId]
  );

  const healthyCount = instances.length
    ? instances.filter((i) => i.status === 'Available').length
    : workload.readyReplicas;
  const totalCount = instances.length || workload.desiredReplicas;

  const { health, failure, instancesFailing } = overviewHealth(workload, instances);
  const albs = published?.proxies;
  const readyAt = instancesReadyAt(instances);
  const justDeployed = !!readyAt && Date.now() - readyAt.getTime() < DEPLOYED_FOR_MS;
  const now = useNow(
    health === 'Deploying' || instancesStarting(instances) || justDeployed || (!!albs && anyAlbProvisioning(albs))
  );
  const deploy = instancesFailing ? undefined : deployStatus(workload, now);
  const albStatuses = useMemo(
    () =>
      Object.fromEntries(
        (albs ?? []).map((alb) => [
          alb.proxyName,
          albStatus(alb, { workloadServing: workload.health === 'Available', now }),
        ])
      ),
    [albs, workload.health, now]
  );
  const status: StatusDisplay = deploy
    ? { tone: deploy.tone, label: deploy.label }
    : { tone: workloadHealthToBadgeType(health), label: health };

  return {
    health,
    failure,
    deploy,
    albStatuses,
    status,
    now,
    healthyCount,
    totalCount,
    instanceHref,
    instanceMetricsHref,
    albHrefFor,
    albMetricsHrefFor,
  };
}
