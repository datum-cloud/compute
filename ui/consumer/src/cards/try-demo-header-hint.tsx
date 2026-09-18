/**
 * `portal.header/project` contribution — a small "Try the demo" link in the
 * persistent top header, just left of the project search entry (see
 * cloud-portal's `ProjectHeaderPluginContent`). Renders nothing unless
 * Compute is confirmed Active for the project. Deliberately tiny: this slot
 * sits in the global nav next to search, so it has to read as a quiet hint,
 * not another CTA — same spirit as the "Try the demo" link already on the
 * Workloads page header (see `pages/workload-list.tsx`), just visible from
 * anywhere in the project rather than only on that one page.
 */
import { useComputeEntitlement } from '../lib/api';
import { ownPluginHref, useOwnPluginSlug } from '../lib/plugin-slug';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { SparklesIcon } from 'lucide-react';
import { useNavigate, useParams } from 'react-router';

export default function TryDemoHeaderHint() {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const { data: entitlement } = useComputeEntitlement(projectId);
  const { data: slug } = useOwnPluginSlug(projectId);

  if (entitlement?.phase !== 'Active') return null;

  // `?tryDemo=1` tells the Workloads page to pop the deploy dialog itself on
  // arrival, rather than just landing on the page and making the user click
  // "Try the demo" a second time — see workload-list.tsx's read of this param.
  const href = projectId && slug ? `${ownPluginHref(projectId, slug)}?tryDemo=1` : undefined;
  if (!href) return null;

  return (
    <Button type="secondary" theme="link" size="xs" onClick={() => navigate(href)}>
      <Icon icon={SparklesIcon} size={12} />
      One-Click Deploy
    </Button>
  );
}
