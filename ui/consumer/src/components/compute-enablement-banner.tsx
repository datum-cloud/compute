/**
 * Shown on the Workloads page in place of the CLI empty-state when the
 * project doesn't (yet) have an Active `compute` ServiceEntitlement — see
 * `lib/api.ts`'s `useComputeEntitlement`/`useEnableCompute`. The
 * "Enable Compute" button creates the same ServiceEntitlement object that
 * `datumctl compute deploy` creates on first use.
 *
 * The `compact` variant is the same states and wording, sized for the
 * project home page's Workloads column.
 */
import { Banner } from './cli-section';
import { ColumnEmpty } from '../cards/home-column-parts';
import { useEnableCompute, type EntitlementPhase } from '../lib/api';
import { Button } from '@datum-cloud/datum-ui/button';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { toast } from '@datum-cloud/datum-ui/toast';
import { ClockIcon, ShieldAlertIcon, XCircleIcon } from 'lucide-react';

type BannerState = 'NotRequested' | 'PendingApproval' | 'Rejected';

const COPY: Record<BannerState, { title: string; description: string; icon: React.ReactNode; cta?: string }> = {
  NotRequested: {
    title: 'This project does not have Compute enabled',
    description: 'You need to enable Compute to create Workloads in this project.',
    icon: <Icon icon={ShieldAlertIcon} size={32} className="text-primary shrink-0" />,
    cta: 'Enable Compute',
  },
  PendingApproval: {
    title: 'Enabling Compute',
    description: 'Compute is being enabled for this project. This usually takes a moment.',
    icon: <Icon icon={ClockIcon} size={32} className="text-primary shrink-0" />,
  },
  Rejected: {
    title: 'Compute could not be enabled',
    description: 'Run datumctl compute access request --renew to retry, or contact support.',
    icon: <Icon icon={XCircleIcon} size={32} className="text-destructive shrink-0" />,
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
  const { mutate, isPending } = useEnableCompute(projectId);
  const state = bannerState(phase);
  const copy = COPY[state];

  const handleClick = () => {
    mutate(undefined, {
      onSuccess: () => toast.success('Enabling Compute for this project'),
      onError: () => toast.error('Failed to enable Compute'),
    });
  };

  const action = state === 'NotRequested' && (
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
