/**
 * Workload log explorer built on `@datum-cloud/datum-ui/logs`.
 *
 * Merges ALB access logs (when an HTTPProxy is attached) with instance stdout
 * from every replica. Unlike the consumer instance page, stdout is queried even
 * without a published URL — that's the signal staff need for crash loops.
 */
import { ApiError } from '../lib/api';
import {
  combinedLogFacets,
  filterCombinedLogs,
  LOG_SOURCE_ALB,
  useWorkloadLogs,
} from '../lib/o11y-logs';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import {
  lastThirtyMinutes,
  Logs,
  resolveLogTimeRange,
  type LogColumn,
  type LogColumnSpec,
  type LogFilters,
  type LogTimeRange,
} from '@datum-cloud/datum-ui/logs';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useCallback, useMemo, useState } from 'react';

const SOURCE_COLUMN: LogColumn = {
  id: 'source',
  header: 'Source',
  size: 'hug',
  className: 'text-muted-foreground font-mono text-xs',
  cell: ({ entry }) => entry.labels.source ?? '—',
};

const DETAIL_COLUMN: LogColumn = {
  id: 'detail',
  header: 'Detail',
  size: 'fill',
  className: 'truncate font-mono text-xs',
  cell: ({ entry, path, message }) => {
    const text = entry.labels.source === LOG_SOURCE_ALB ? path : message || path;
    return (
      <span className={text ? undefined : 'text-muted-foreground'} title={text ?? undefined}>
        {text || '—'}
      </span>
    );
  },
};

const EXPLORER_COLUMNS: readonly LogColumnSpec[] = [
  'time',
  SOURCE_COLUMN,
  'status',
  'host',
  DETAIL_COLUMN,
];

const EMPTY_TITLE = 'No logs for this workload';
const EMPTY_SUBTITLE =
  'Instance stdout appears here once replicas are running. ALB access logs appear when the workload is published on a public URL.';
const DENIED_MESSAGE = "You don't have permission to view workload logs.";

export function WorkloadLogsExplorer({
  projectName,
  proxyId,
  instanceNames,
  className,
}: {
  projectName?: string;
  proxyId?: string;
  instanceNames: readonly string[];
  className?: string;
}) {
  const [filters, setFilters] = useState<LogFilters>({});
  const [search, setSearch] = useState('');
  const [live, setLive] = useState(false);
  const [timeRange, setTimeRange] = useState<LogTimeRange>(() => lastThirtyMinutes());

  const handleRefresh = useCallback(() => {
    setTimeRange((current) =>
      current.preset ? resolveLogTimeRange(current) : lastThirtyMinutes()
    );
  }, []);

  const hasSources = !!proxyId || instanceNames.length > 0;
  const logsQuery = useWorkloadLogs(projectName, proxyId, instanceNames, {
    timeRange,
    filters,
    search,
    live,
    enabled: hasSources,
  });

  const visibleEntries = useMemo(
    () => filterCombinedLogs(logsQuery.data ?? [], filters),
    [logsQuery.data, filters]
  );
  const facets = useMemo(() => combinedLogFacets(logsQuery.data ?? []), [logsQuery.data]);

  const denied = logsQuery.error instanceof ApiError && logsQuery.error.status === 403;
  const errorMessage = logsQuery.error && !denied ? logsQuery.error.message : undefined;

  if (!hasSources) {
    return (
      <div
        className={cn('flex min-h-96 flex-col', className)}
        data-testid="provider-plugin-workload-logs-explorer">
        <EmptyContent
          title={EMPTY_TITLE}
          subtitle={EMPTY_SUBTITLE}
          size="sm"
          variant="dashed"
          className="min-h-96 flex-1"
        />
      </div>
    );
  }

  if (denied) {
    return (
      <div
        className={cn('flex min-h-96 flex-col', className)}
        data-testid="provider-plugin-workload-logs-explorer">
        <EmptyContent
          title="Access restricted"
          subtitle={DENIED_MESSAGE}
          size="sm"
          variant="dashed"
          className="min-h-96 flex-1"
        />
      </div>
    );
  }

  return (
    <Card
      size="sm"
      sectioned
      className={cn('flex min-h-96 flex-col overflow-hidden', className)}
      style={{ minHeight: '32rem' }}
      data-testid="provider-plugin-workload-logs-explorer">
      <CardContent padding="none" className="flex min-h-0 flex-1 flex-col">
        <Logs.Root
          entries={visibleEntries}
          facets={facets}
          timeRange={timeRange}
          filters={filters}
          search={search}
          live={live}
          isLoading={logsQuery.isLoading}
          error={errorMessage}
          columns={[...EXPLORER_COLUMNS]}
          onTimeRangeChange={setTimeRange}
          onFiltersChange={setFilters}
          onSearchChange={setSearch}
          onLiveChange={setLive}
          onRefresh={handleRefresh}
          className="bg-card flex min-h-0 flex-1 flex-col">
          <Logs.Explorer className="bg-card min-h-0 flex-1" />
        </Logs.Root>
      </CardContent>
    </Card>
  );
}
