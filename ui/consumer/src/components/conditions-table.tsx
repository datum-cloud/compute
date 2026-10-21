/**
 * Raw condition table (Type / Status / Reason / Message / Last transition),
 * the "why is this instance not Available" detail on the instance Overview.
 * Uses the host-shared datum-ui `table` primitives.
 */
import { StatusBadge } from './detail-list';
import type { InstanceCondition } from '../schema';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@datum-cloud/datum-ui/table';
import { formatDistanceToNowStrict } from 'date-fns';

/**
 * Some controllers stamp a zero-value time (the Unix epoch) on conditions
 * they never transitioned, which would otherwise read as "57 years ago".
 */
export function transitionDate(value?: string): Date | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) || date.getTime() <= 0 ? undefined : date;
}

function conditionBadgeType(status: InstanceCondition['status']): 'success' | 'danger' | 'muted' {
  if (status === 'True') return 'success';
  if (status === 'False') return 'danger';
  return 'muted';
}

export function ConditionsTable({ conditions }: { conditions: InstanceCondition[] }) {
  if (conditions.length === 0) {
    return <p className="text-muted-foreground px-4 py-6 text-sm">No conditions reported.</p>;
  }

  // Chrome (border, rounding) comes from the enclosing Card.
  return (
    <div className="overflow-x-auto" data-testid="compute-plugin-conditions-table">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Type</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Reason</TableHead>
            <TableHead>Message</TableHead>
            <TableHead>Last transition</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {conditions.map((condition, index) => {
            const transitioned = transitionDate(condition.lastTransitionTime);
            return (
              <TableRow key={`${condition.type}-${index}`}>
                <TableCell className="font-mono text-xs">{condition.type}</TableCell>
                <TableCell>
                  <StatusBadge type={conditionBadgeType(condition.status)}>
                    {condition.status}
                  </StatusBadge>
                </TableCell>
                <TableCell className="font-mono text-xs">{condition.reason ?? '—'}</TableCell>
                <TableCell className="whitespace-normal" style={{ maxWidth: 480 }}>
                  {condition.message ?? '—'}
                </TableCell>
                <TableCell
                  className="text-muted-foreground whitespace-nowrap"
                  title={transitioned?.toLocaleString()}
                >
                  {transitioned
                    ? formatDistanceToNowStrict(transitioned, {
                        addSuffix: true,
                      })
                    : '—'}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
