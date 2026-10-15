/**
 * Activity timeline for one workload or instance — `ActivityFeed` from
 * `@datum-cloud/activity-ui`, the component cloud-portal's own activity pages
 * use. Pinned to the version cloud-portal ships: the host compiles that
 * package's Tailwind classes, so a different version could use classes the
 * host never built.
 */
import { COMPUTE_API_GROUP, useActivityClient } from '../lib/activity';
import { ActivityFeed, type ResourceLinkResolver } from '@datum-cloud/activity-ui';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { useMemo } from 'react';

export function ResourceActivity({
  projectId,
  filter,
  liveNameMatch,
  resourceLinkResolver,
}: {
  projectId?: string;
  /** CEL that scopes the feed to this resource (see `lib/activity.ts`). */
  filter: string;
  /**
   * Substring the resource name must contain. Live updates arrive over a
   * watch that ignores `filter`; this and the API group are the only filters
   * it applies, so without them every compute event in the project would
   * stream in (milo-os/activity#262). Queries still apply `filter`, which is
   * exact.
   */
  liveNameMatch: string;
  resourceLinkResolver: ResourceLinkResolver;
}) {
  const client = useActivityClient(projectId);
  // A new object resets the feed's filter state, so keep it stable.
  const initialFilters = useMemo(
    () => ({
      changeSource: 'all' as const,
      apiGroups: [COMPUTE_API_GROUP],
      resourceName: liveNameMatch,
      customFilter: filter,
    }),
    [filter, liveNameMatch]
  );

  if (!client) return null;
  return (
    <Card size="sm" className="w-full overflow-hidden" data-testid="compute-plugin-activity">
      <CardContent>
        <ActivityFeed
          client={client}
          initialFilters={initialFilters}
          resourceLinkResolver={resourceLinkResolver}
          // Every row is in this project; the project badge would only repeat it.
          tenantRenderer={() => null}
          hiddenFilters={['resourceKinds', 'apiGroups', 'resourceName', 'resourceNamespaces']}
          variant="timeline"
          compact
          enableStreaming
          infiniteScroll={false}
          pageSize={30}
        />
      </CardContent>
    </Card>
  );
}
