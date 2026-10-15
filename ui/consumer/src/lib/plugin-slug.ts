/**
 * Resolves this plugin's own registry slug at runtime, so a plugin-contributed
 * card/header hint can link back to its own mounted page
 * (`/project/:projectId/services/<slug>`) without hardcoding a path — the
 * slug is assigned at plugin registration (not by this manifest), so it can
 * only be discovered by asking the host.
 *
 * Mirrors cloud-portal's own `useProjectPlugins`/`normalizePluginList`
 * (`GET /api/plugins?projectId=...` may return a bare array or a
 * `{data}`/`{plugins}` envelope) — see
 * `cloud-portal/app/modules/plugins/client/use-project-plugins.ts`.
 */
import { PLUGIN_ID } from './api';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

interface PublicPluginSummary {
  slug: string;
  manifest: { name: string };
}

function normalizePluginList(body: unknown): PublicPluginSummary[] {
  if (Array.isArray(body)) return body as PublicPluginSummary[];
  if (body && typeof body === 'object') {
    const record = body as { data?: unknown; plugins?: unknown };
    if (Array.isArray(record.data)) return record.data as PublicPluginSummary[];
    if (Array.isArray(record.plugins)) return record.plugins as PublicPluginSummary[];
  }
  return [];
}

async function fetchOwnPluginSlug(projectId: string): Promise<string | undefined> {
  const url = `/api/plugins?projectId=${encodeURIComponent(projectId)}`;
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) return undefined;
  const plugins = normalizePluginList(await res.json());
  return plugins.find((plugin) => plugin.manifest.name === PLUGIN_ID)?.slug;
}

export function useOwnPluginSlug(projectId: string | undefined): UseQueryResult<string | undefined> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'own-plugin-slug', projectId],
    queryFn: () => fetchOwnPluginSlug(projectId as string),
    enabled: !!projectId,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/** This plugin's own mounted page for a project, once its slug is known. */
export function ownPluginHref(projectId: string, slug: string): string {
  return `/project/${projectId}/services/${slug}`;
}
