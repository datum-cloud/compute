/**
 * The workload status pill (dot + badge) shared by the card grid, the table
 * and the detail header. A workload being torn down gets its own treatment —
 * a spinner and "Deleting" in a neutral tone — instead of whatever health its
 * last status reported, so a disappearing workload doesn't look healthy. One
 * still coming up gets a spinner and "Deploying" rather than reading as
 * unavailable, turning to a warning once it is blocked or slow.
 */
import { deployStatus, HEALTH_DOT_CLASS, statusLabel, type DeployStatus } from '../lib/workload-presenters';
import { useNow } from '../lib/use-now';
import { workloadHealthToBadgeType, type Workload, type WorkloadHealth } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';

/**
 * Status dot. Deploying tones are inline: the host compiles no `bg-blue-*`
 * class to rely on.
 */
export function HealthDot({
  health,
  deploy,
  className = 'size-2',
  label,
}: {
  health: WorkloadHealth;
  deploy?: DeployStatus;
  className?: string;
  /** Announced in place of hiding the dot, where nothing else names the health. */
  label?: string;
}) {
  const tone = deploy?.tone ?? (health === 'Deploying' ? 'info' : undefined);
  return (
    <span
      className={cn('shrink-0 rounded-full', className, !tone && HEALTH_DOT_CLASS[health])}
      style={tone ? { background: `var(--color-badge-${tone})` } : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
  );
}

/**
 * Matches the portal's own status badge (`BadgeStatus`, e.g. the ALB list's
 * "ACTIVE"): small bold caps. The host compiles every one of these classes.
 */
const BADGE_CLASS = 'text-5xs px-1 py-0.5 font-bold tracking-[0.03em] uppercase';

export function WorkloadStatusBadge({
  workload,
  label = statusLabel(workload),
  dot = true,
}: {
  workload: Workload;
  /** Overrides the healthy/degraded wording; ignored while deleting or deploying. */
  label?: string;
  dot?: boolean;
}) {
  const now = useNow(workload.health === 'Deploying' && !workload.deleting, 5_000);
  const deploy = deployStatus(workload, now);

  if (workload.deleting) {
    return (
      <span className="flex shrink-0 items-center gap-2" data-e2e="workload-status-deleting">
        {dot && <span className="bg-muted-foreground size-2 shrink-0 rounded-full" aria-hidden />}
        <Badge type="muted" theme="light" className={cn('gap-1.5', BADGE_CLASS)}>
          <SpinnerIcon size="xs" aria-hidden />
          Deleting
        </Badge>
      </span>
    );
  }
  if (deploy) {
    return (
      <span
        className="flex shrink-0 items-center gap-2"
        data-e2e="workload-status-deploying"
        title={deploy.message}>
        {dot && <HealthDot health={workload.health} deploy={deploy} />}
        <Badge type={deploy.tone} theme="light" className={cn('gap-1.5', BADGE_CLASS)}>
          {deploy.inProgress && <SpinnerIcon size="xs" aria-hidden />}
          {deploy.label}
        </Badge>
      </span>
    );
  }
  return (
    <span className="flex shrink-0 items-center gap-2">
      {dot && <HealthDot health={workload.health} />}
      <Badge type={workloadHealthToBadgeType(workload.health)} theme="light" className={BADGE_CLASS}>
        {label}
      </Badge>
    </span>
  );
}
