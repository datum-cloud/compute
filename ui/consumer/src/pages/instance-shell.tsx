import { InstanceShell } from '../components/instance-shell';
import { ShellUnavailableNotice } from '../components/states';
import { useInstanceOutlet } from './instance-outlet-context';
import { useLocation } from 'react-router';

export default function InstanceShellPage() {
  const { instance, projectId, shellAvailable } = useInstanceOutlet();
  const { pathname } = useLocation();
  if (shellAvailable === undefined) return null;
  if (!projectId || !shellAvailable) {
    return (
      <ShellUnavailableNotice />
    );
  }
  return (
    <InstanceShell variant="tab" projectId={projectId} instance={instance} shellHref={pathname} />
  );
}
