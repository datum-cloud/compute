/** Instance Activity tab: when the instance started, stopped, or failed. */
import { ResourceActivity } from '../components/resource-activity';
import { computeResourceLinkResolver, instanceActivityFilter } from '../lib/activity';
import { useInstanceOutlet } from './instance-outlet-context';
import { useMemo } from 'react';

export default function InstanceActivity() {
  const { instance, workloadName, workloadHref, projectId } = useInstanceOutlet();
  const owner = workloadName ?? instance.workloadName ?? '';
  const resolver = useMemo(
    () => computeResourceLinkResolver(owner, workloadHref),
    [owner, workloadHref]
  );
  return (
    <ResourceActivity
      projectId={projectId}
      filter={instanceActivityFilter(instance.name)}
      liveNameMatch={instance.name}
      resourceLinkResolver={resolver}
    />
  );
}
