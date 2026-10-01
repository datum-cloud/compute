/**
 * Shown on the Workloads page in place of the CLI empty-state when the
 * project doesn't (yet) have an Active `compute` ServiceEntitlement — see
 * `lib/api.ts`'s `useComputeEntitlement`/`useRequestComputeAccess`. The
 * "Enable Compute" button creates the same ServiceEntitlement object that
 * `datumctl compute deploy`'s "Would you like to request access?" prompt
 * does, so a request made here shows up identically to one made via the CLI.
 *
 * The `compact` variant is the same states and wording, sized for the
 * project home page's Workloads column.
 */
import { Banner } from './cli-section';
import { ColumnEmpty } from '../cards/home-column-parts';
import { useRequestComputeAccess, type EntitlementPhase } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { ClockIcon, ShieldAlertIcon, XCircleIcon } from 'lucide-react';

type BannerState = 'NotRequested' | 'PendingApproval' | 'Rejected';

const COPY: Record<BannerState, { title: string; description: string; icon: React.ReactNode; cta: string }> = {
  NotRequested: {
    title: 'This project does not have Compute enabled',
    description: 'You need to enable Compute to create Workloads in this project.',
    icon: <Icon icon={ShieldAlertIcon} size={32} className="text-primary shrink-0" />,
    cta: 'Enable Compute',
  },
  PendingApproval: {
    title: 'Your request is pending',
    description:
      'Thanks for requesting access to Compute. We will be in touch shortly with a response.',
    icon: <Icon icon={ClockIcon} size={32} className="text-primary shrink-0" />,
    cta: 'Enable Compute',
  },
  Rejected: {
    title: 'Compute access was declined',
    description: 'The request to enable Compute for this project was declined. You can request access again.',
    icon: <Icon icon={XCircleIcon} size={32} className="text-destructive shrink-0" />,
    cta: 'Request access again',
  },
};

/** Smaller icons for the compact (home column) variant, which sits in a tile. */
const COMPACT_ICON: Record<BannerState, typeof ShieldAlertIcon> = {
  NotRequested: ShieldAlertIcon,
  PendingApproval: ClockIcon,
  Rejected: XCircleIcon,
};

function bannerState(phase: EntitlementPhase | null): BannerState {
  if (phase === 'PendingApproval') return 'PendingApproval';
  if (phase === 'Rejected') return 'Rejected';
  return 'NotRequested';
}

export function ComputeEnablementBanner({
  projectId,
  phase,
  compact = false,
}: {
  projectId: string;
  phase: EntitlementPhase | null;
  compact?: boolean;
}) {
  const { mutate, isPending } = useRequestComputeAccess(projectId);
  const state = bannerState(phase);
  const copy = COPY[state];

  const handleClick = () => {
    mutate(undefined, {
      onSuccess: () => toast.success('Requested Compute access for this project'),
      onError: () => toast.error('Failed to request Compute access'),
    });
  };

  const action = state !== 'PendingApproval' && (
    <Button
      loading={isPending}
      disabled={isPending}
      size={compact ? 'xs' : undefined}
      onClick={handleClick}
    >
      {copy.cta}
    </Button>
  );

  if (compact) {
    return (
      <ColumnEmpty
        icon={<Icon icon={COMPACT_ICON[state]} size={18} aria-hidden />}
        title={copy.title}
        action={action}
        testId="compute-plugin-enablement-compact"
      >
        {copy.description}
      </ColumnEmpty>
    );
  }

  return (
    <Banner
      testId="compute-plugin-enablement-banner"
      icon={copy.icon}
      title={copy.title}
      description={copy.description}
      actions={action}
    />
  );
}
