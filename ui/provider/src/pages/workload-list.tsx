/**
 * `portal.page/project` extension at `""` (the plugin mount's index),
 * exposed as `WorkloadList` — every Workload in this project, linking into
 * the existing `WorkloadDetail` support view (`:workloadName`, a sibling
 * route under the same mount).
 *
 * `projectName` comes from `useParams()` the same way `WorkloadDetail` gets
 * it — see `../lib/api.ts`'s header comment.
 */
import { StatusBadge } from '../components/detail-list';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useWorkloads } from '../lib/api';
import { healthToBadgeType, type Workload } from '../schema';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@datum-cloud/datum-ui/table';
import { formatDistanceToNowStrict } from 'date-fns';
import { Link, useParams } from 'react-router';

function WorkloadRow({ workload }: { workload: Workload }) {
  return (
    <TableRow>
      <TableCell>
        <Link to={workload.name} className="hover:underline">
          <span className="font-mono text-sm">{workload.name}</span>
        </Link>
      </TableCell>
      <TableCell>
        <StatusBadge type={healthToBadgeType(workload.health)}>{workload.health}</StatusBadge>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {workload.readyReplicas}/{workload.desiredReplicas}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {workload.regions.join(', ') || '—'}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {formatDistanceToNowStrict(workload.createdAt, { addSuffix: true })}
      </TableCell>
    </TableRow>
  );
}

export default function WorkloadList() {
  const { projectName } = useParams<{ projectName: string }>();
  const { data: workloads, isLoading, error, refetch } = useWorkloads(projectName);

  return (
    <div className="flex min-w-0 flex-col gap-6 p-4 sm:p-6" data-testid="provider-plugin-workload-list">
      <PageTitle title="Workloads" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view workloads in this project."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && workloads && workloads.length === 0 && (
        <EmptyContent title="there are no workloads in this project" size="sm" variant="dashed" />
      )}

      {!isLoading && !error && workloads && workloads.length > 0 && (
        <div className="border-border overflow-hidden rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Health</TableHead>
                <TableHead>Ready</TableHead>
                <TableHead>Regions</TableHead>
                <TableHead>Created</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {workloads.map((workload) => (
                <WorkloadRow key={workload.uid || workload.name} workload={workload} />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
