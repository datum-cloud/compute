/**
 * Raw condition table (Type/Status/Reason/Message/Last Transition) — the
 * "why is this broken" surface this support view is built around. Shared by
 * the Workload conditions card, per-placement conditions, and the Instances
 * tab's per-row condition detail.
 */
import { StatusBadge } from './detail-list';
import type { Condition } from '../schema';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@datum-cloud/datum-ui/table';
import { formatDistanceToNowStrict } from 'date-fns';

function conditionBadgeType(status: Condition['status']): 'success' | 'danger' | 'muted' {
  if (status === 'True') return 'success';
  if (status === 'False') return 'danger';
  return 'muted';
}

export function ConditionsTable({ conditions }: { conditions: Condition[] }) {
  if (conditions.length === 0) {
    return <p className="text-muted-foreground text-sm">No conditions reported.</p>;
  }

  return (
    <div className="border-border overflow-hidden rounded-lg border" data-testid="conditions-table">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Type</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>Message</TableHead>
            <TableHead>Last Transition</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {conditions.map((c, index) => (
            <TableRow key={`${c.type}-${index}`}>
              <TableCell className="font-mono text-xs">{c.type}</TableCell>
              <TableCell>
                <StatusBadge type={conditionBadgeType(c.status)}>{c.status}</StatusBadge>
              </TableCell>
              <TableCell className="font-mono text-xs">{c.reason ?? '—'}</TableCell>
              <TableCell className="max-w-md whitespace-normal">{c.message ?? '—'}</TableCell>
              <TableCell className="text-muted-foreground">
                {c.lastTransitionTime
                  ? formatDistanceToNowStrict(new Date(c.lastTransitionTime), { addSuffix: true })
                  : '—'}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
