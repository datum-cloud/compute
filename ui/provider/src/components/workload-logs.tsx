/**
 * Workload log explorer built on `@datum-cloud/datum-ui/logs`.
 *
 * Full explorer for the Workload Logs tab. No log backend yet — entries stay
 * empty until a Loki query is wired.
 */
import {
  lastThirtyMinutes,
  Logs,
  type LogFilters,
  type LogTimeRange,
} from '@datum-cloud/datum-ui/logs';
import { cn } from '@datum-cloud/datum-ui/utils';
import { useCallback, useState } from 'react';

const COLUMNS = ['time', 'severity', 'message'] as const;

/** Full log explorer for the Workload Logs tab. */
export function WorkloadLogsExplorer({ className }: { className?: string }) {
  const [filters, setFilters] = useState<LogFilters>({});
  const [search, setSearch] = useState('');
  const [live, setLive] = useState(false);
  const [timeRange, setTimeRange] = useState<LogTimeRange>(() => lastThirtyMinutes());

  const handleRefresh = useCallback(() => {
    setTimeRange(lastThirtyMinutes());
  }, []);

  return (
    <div
      className={cn(
        'border-border bg-card flex flex-col overflow-hidden rounded-xl border',
        className
      )}
      style={{ minHeight: '32rem' }}
      data-testid="provider-plugin-workload-logs-explorer">
      <Logs.Root
        entries={[]}
        facets={[{ name: 'severity', label: 'Severity', options: [] }]}
        timeRange={timeRange}
        filters={filters}
        search={search}
        live={live}
        columns={[...COLUMNS]}
        onTimeRangeChange={setTimeRange}
        onFiltersChange={setFilters}
        onSearchChange={setSearch}
        onLiveChange={setLive}
        onRefresh={handleRefresh}
        className="bg-card flex min-h-0 flex-1 flex-col">
        <Logs.Explorer />
      </Logs.Root>
    </div>
  );
}
