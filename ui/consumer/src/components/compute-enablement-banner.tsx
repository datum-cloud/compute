/**
 * Shown on the Workloads page in place of the CLI empty-state when the
 * project doesn't (yet) have an Active `compute` ServiceEntitlement — see
 * `lib/api.ts`'s `useComputeEntitlement`/`useRequestComputeAccess`. The
 * "Enable Compute" button creates the same ServiceEntitlement object that
 * `datumctl compute deploy`'s "Would you like to request access?" prompt
 * does, so a request made here shows up identically to one made via the CLI.
 */
import { Banner } from './cli-section';
import { useRequestComputeAccess, type EntitlementPhase } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { toast } from '@datum-cloud/datum-ui/toast';
import { ClockIcon, ShieldAlertIcon, XCircleIcon } from 'lucide-react';

type BannerState = 'NotRequested' | 'PendingApproval' | 'Rejected';

const COPY: Record<BannerState, { title: string; description: string; icon: React.ReactNode; cta: string }> = {
  NotRequested: {
    title: 'This project does not have Compute enabled',
    description: 'You need to enable Compute to create Workloads in this project.',
    icon: <ShieldAlertIcon className="text-primary size-8 shrink-0" />,
    cta: 'Enable Compute',
  },
  PendingApproval: {
    title: 'Compute access is pending approval',
    description:
      'A request to enable Compute for this project has been sent to the service provider and is awaiting approval.',
    icon: <ClockIcon className="text-primary size-8 shrink-0" />,
    cta: 'Enable Compute',
  },
  Rejected: {
    title: 'Compute access was declined',
    description: 'The request to enable Compute for this project was declined. You can request access again.',
    icon: <XCircleIcon className="text-destructive size-8 shrink-0" />,
    cta: 'Request access again',
  },
};

function bannerState(phase: EntitlementPhase | null): BannerState {
  if (phase === 'PendingApproval') return 'PendingApproval';
  if (phase === 'Rejected') return 'Rejected';
  return 'NotRequested';
}

export function ComputeEnablementBanner({ projectId, phase }: { projectId: string; phase: EntitlementPhase | null }) {
  const { mutate, isPending } = useRequestComputeAccess(projectId);
  const state = bannerState(phase);
  const copy = COPY[state];

  const handleClick = () => {
    mutate(undefined, {
      onSuccess: () => toast.success('Requested Compute access for this project'),
      onError: () => toast.error('Failed to request Compute access'),
    });
  };

  return (
    <Banner
      testId="compute-plugin-enablement-banner"
      icon={copy.icon}
      title={copy.title}
      description={copy.description}
      actions={
        state !== 'PendingApproval' && (
          <Button loading={isPending} disabled={isPending} onClick={handleClick}>
            {copy.cta}
          </Button>
        )
      }
    />
  );
}
