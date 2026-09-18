/**
 * `portal.card/project-home` contribution — a lightweight nudge on the
 * project home page pointing entitled projects at the one-click demo on the
 * Workloads page (see `pages/workload-list.tsx`'s `TryDemoWorkloadCard`).
 * Renders nothing until Compute is confirmed Active for the project — the
 * demo only makes sense once workloads can actually be created, and the
 * project home page always wraps this in a "Compute" titled card regardless
 * (see cloud-portal's `ProjectHomePluginCards`), so an inactive project just
 * sees an empty one rather than a confusing partial pitch.
 *
 * The plugin mount path (`/project/:projectId/services/<slug>`) isn't known
 * to this manifest — the slug is assigned at plugin registration, not here —
 * so the link resolves it at runtime via `useOwnPluginSlug` instead of
 * hardcoding a path.
 */
import { useComputeEntitlement } from '../lib/api';
import { ownPluginHref, useOwnPluginSlug } from '../lib/plugin-slug';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { RocketIcon } from 'lucide-react';
import { useNavigate, useParams } from 'react-router';

export default function TryDemoHomeCard() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const { data: entitlement } = useComputeEntitlement(projectId);
  const { data: slug } = useOwnPluginSlug(projectId);

  if (entitlement?.phase !== 'Active') return null;

  // `?tryDemo=1` tells the Workloads page to pop the deploy dialog itself on
  // arrival — see workload-list.tsx's read of this param.
  const href = projectId && slug ? `${ownPluginHref(projectId, slug)}?tryDemo=1` : undefined;

  return (
    <div className="flex items-center gap-4">
      <Icon icon={RocketIcon} size={28} className="text-secondary shrink-0" />
      <div className="flex flex-1 flex-col gap-1">
        <h3 className="text-sm font-semibold">Deploy your first workload</h3>
        <p className="text-muted-foreground text-sm">
          Launch a running workload in one click — no manifest required.
        </p>
      </div>
      <Button
        type="secondary"
        theme="solid"
        size="small"
        disabled={!href}
        onClick={() => {
          if (href) navigate(href);
        }}
      >
        Deploy Now
      </Button>
    </div>
  );
}
