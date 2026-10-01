import { InstanceShell } from '../components/instance-shell';
import { useInstanceOutlet } from './instance-outlet-context';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { useLocation } from 'react-router';

export default function InstanceShellPage() {
  const { instance, projectId, shellAvailable } = useInstanceOutlet();
  const { pathname } = useLocation();
  if (shellAvailable === undefined) return null;
  if (!projectId || !shellAvailable) {
    return (
      <EmptyContent
        title="Shell isn't available for this instance"
        subtitle="Opening a shell needs permission to create shell sessions and an instance whose runtime supports them."
        size="sm"
        variant="dashed"
        className="min-h-48"
      />
    );
  }
  return (
    <InstanceShell variant="tab" projectId={projectId} instance={instance} shellHref={pathname} />
  );
}
