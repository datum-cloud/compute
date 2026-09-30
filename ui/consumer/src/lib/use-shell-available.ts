import { canPerform, getProjectScopedBase, PERMISSION_STALE_MS, PLUGIN_ID } from './api';
import {
  canShowShell,
  resolveRuntimeClass,
  toRuntimeClasses,
  type RawRuntimeClassList,
  type RuntimeClassSummary,
} from './instance-shell';
import { fetchSessionAllowance } from './console-sessions';
import type { Instance } from '../schema';
import { useQuery } from '@tanstack/react-query';
import { consoleClientAssets } from 'virtual:console-client-assets';

const RUNTIME_CLASSES_PATH = '/apis/compute.datumapis.com/v1alpha/runtimeclasses';

async function fetchShellAccess(projectId: string): Promise<{
  canCreateSessions: boolean;
  sessionAllowance: number | undefined;
  runtimeClasses: RuntimeClassSummary[];
}> {
  const [canCreateSessions, sessionAllowance, runtimeClasses] = await Promise.all([
    canPerform(projectId, 'compute.datumapis.com', 'instanceconsolesessions', 'create'),
    fetchSessionAllowance(projectId),
    fetch(`${getProjectScopedBase(projectId)}${RUNTIME_CLASSES_PATH}`, {
      headers: { Accept: 'application/json' },
    })
      .then((res) => (res.ok ? (res.json() as Promise<RawRuntimeClassList>) : {}))
      .then(toRuntimeClasses)
      .catch(() => []),
  ]);
  return { canCreateSessions, sessionAllowance, runtimeClasses };
}

export function useShellAvailable(
  projectId: string | undefined,
  instance: Instance | undefined
): boolean | undefined {
  const query = useQuery({
    queryKey: [PLUGIN_ID, 'shell-access', projectId],
    enabled: !!projectId && consoleClientAssets !== null,
    queryFn: () => fetchShellAccess(projectId as string),
    staleTime: PERMISSION_STALE_MS,
    retry: false,
  });
  if (!projectId || consoleClientAssets === null) return false;
  if (query.isPending || !instance) return undefined;
  if (!query.data) return false;
  return canShowShell({
    clientBundled: true,
    canCreateSessions: query.data.canCreateSessions,
    sessionAllowance: query.data.sessionAllowance,
    runtimeClass: resolveRuntimeClass(instance.runtimeClass, query.data.runtimeClasses),
    containers: instance.containers,
  });
}
