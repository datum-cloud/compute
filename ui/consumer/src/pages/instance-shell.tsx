import { Notice } from '../components/states';
import { TerminalIcon } from 'lucide-react';
import { InstanceShell } from '../components/instance-shell';
import { useInstanceOutlet } from './instance-outlet-context';
import { useLocation } from 'react-router';

export default function InstanceShellPage() {
  const { instance, projectId, shellAvailable } = useInstanceOutlet();
  const { pathname } = useLocation();
  if (shellAvailable === undefined) return null;
  if (!projectId || !shellAvailable) {
    return (
      <Notice
        icon={TerminalIcon}
        title="Shell isn't available for this instance"
        detail="Opening a shell needs permission to create shell sessions and an instance whose runtime supports them."
        className="min-h-48 rounded-lg border"
      />
    );
  }
  return (
    <InstanceShell variant="tab" projectId={projectId} instance={instance} shellHref={pathname} />
  );
}
