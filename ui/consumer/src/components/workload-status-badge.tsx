/**
 * The workload status pill (dot + badge) shared by the card grid, the table
 * and the detail header. A workload being torn down gets its own treatment —
 * a spinner and "Deleting" in a neutral tone — instead of whatever health its
 * last status reported, so a disappearing workload doesn't look healthy.
 */
import { HEALTH_DOT_CLASS, statusLabel } from '../lib/workload-presenters';
import { workloadHealthToBadgeType, type Workload } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';

export function WorkloadStatusBadge({
  workload,
  label = statusLabel(workload),
  dot = true,
}: {
  workload: Workload;
  /** Overrides the healthy/degraded wording; ignored while deleting. */
  label?: string;
  dot?: boolean;
}) {
  if (workload.deleting) {
    return (
      <span className="flex shrink-0 items-center gap-2" data-e2e="workload-status-deleting">
        {dot && <span className="bg-muted-foreground size-2 shrink-0 rounded-full" aria-hidden />}
        <Badge type="muted" theme="light" className="gap-1.5">
          <SpinnerIcon size="xs" aria-hidden />
          Deleting
        </Badge>
      </span>
    );
  }
  return (
    <span className="flex shrink-0 items-center gap-2">
      {dot && (
        <span
          className={cn('size-2 shrink-0 rounded-full', HEALTH_DOT_CLASS[workload.health])}
          aria-hidden
        />
      )}
      <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
        {label}
      </Badge>
    </span>
  );
}
