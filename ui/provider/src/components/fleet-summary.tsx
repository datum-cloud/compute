/**
 * Shared between the Overview-override page (`../pages/service-overview.tsx`)
 * and the Workloads tab (`../pages/fleet-workloads.tsx`) — the "at a glance"
 * fleet numbers belong on Overview (see the design discussion this came out
 * of), the detailed sortable/paginated table stays on the Workloads tab.
 */
import { StatStrip, type Stat } from './stat-strip';
import type { FailedProject, FleetHealth } from '../lib/fleet-health';
import { Text } from '@datum-cloud/datum-ui/typography';
import { AlertTriangleIcon } from 'lucide-react';

export function FleetStats({ data }: { data: FleetHealth }) {
  const stats: Stat[] = [
    { label: 'Active Consumers', value: String(data.consumerCount) },
    { label: 'Workloads', value: String(data.totalWorkloads) },
    {
      label: 'Healthy',
      value: `${data.healthyCount}/${data.totalWorkloads}`,
      // `text-success` isn't a class the host's compiled CSS actually
      // contains (this plugin has no Tailwind build of its own — see
      // ui/CLAUDE.md — and nothing in the host renders that string), so
      // this uses the real `--btn-success` custom property directly instead.
      style: { color: 'var(--btn-success)' },
    },
    {
      label: 'Unavailable',
      value: String(data.severityCounts.Unavailable),
      className: data.severityCounts.Unavailable > 0 ? 'text-destructive' : undefined,
    },
    {
      label: 'Degraded',
      value: String(data.severityCounts.Degraded),
      className: data.severityCounts.Degraded > 0 ? 'text-warning' : undefined,
    },
  ];
  return <StatStrip stats={stats} testId="provider-plugin-fleet-stats" />;
}

export function FailedProjectsNotice({ failed }: { failed: FailedProject[] }) {
  if (failed.length === 0) return null;

  return (
    // `border-warning/40` isn't a class the host's compiled CSS contains
    // (see ui/CLAUDE.md) — `bg-warning/10`/`text-warning` below are kept as
    // classes since the host already uses those exact strings elsewhere.
    <div
      className="bg-warning/10 flex items-start gap-2 rounded-lg border p-3"
      style={{ borderColor: 'var(--btn-warning)' }}>
      <AlertTriangleIcon className="text-warning mt-0.5 size-4 shrink-0" />
      <div className="flex flex-col gap-1">
        <Text size="sm" className="font-medium">
          {failed.length === 1
            ? '1 consumer project could not be checked'
            : `${failed.length} consumer projects could not be checked`}
        </Text>
        <Text size="xs" textColor="muted">
          {failed.map((f) => f.project.displayName).join(', ')} — results below may be incomplete.
        </Text>
      </div>
    </div>
  );
}
