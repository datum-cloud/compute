/**
 * Instance log surfaces built on `@datum-cloud/datum-ui/logs`.
 *
 * - {@link RecentInstanceLogs} — live feed on Overview (stdout, plus ALB when published)
 * - {@link InstanceLogsExplorer} — full explorer for the Logs tab
 *
 * Instance stdout is always queried. ALB access logs are merged in when the
 * workload has a published HTTPProxy.
 */
import { ApiError } from '../lib/api';
import {
  ALB_LOGS_PREVIEW_LIMIT,
  combinedLogFacets,
  filterCombinedLogs,
  LOG_SOURCE_ALB,
  useInstanceLogs,
  useWorkloadLogs,
} from '../lib/o11y-logs';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { EmptyContent } from '@datum-cloud/datum-ui/empty-content';
import { Icon, SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import {
  httpStatusBadgeType,
  lastThirtyMinutes,
  logRequestHost,
  Logs,
  parseLogLine,
  resolveLogTimeRange,
  type LogColumn,
  type LogColumnSpec,
  type LogEntry,
  type LogFilters,
  type LogTimeRange,
} from '@datum-cloud/datum-ui/logs';
import { cn } from '@datum-cloud/datum-ui/utils';
import { formatDistanceToNowStrict } from 'date-fns';
import { CheckIcon, CopyIcon, LogsIcon, RadioIcon } from 'lucide-react';
import { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router';

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

const ROW_LIMIT = ALB_LOGS_PREVIEW_LIMIT;

const NO_LOGS_TITLE = 'No logs';
const DENIED_MESSAGE = "You don't have permission to view logs.";

/** Illustration empty state. `h-full` overrides EmptyContent's fixed `h-48` so it fills the card. */
function NoLogs({ className }: { className?: string }) {
  return (
    <div className={cn('flex h-full min-h-0 flex-1', className)} style={{ borderRadius: 0 }}>
      <EmptyContent title={NO_LOGS_TITLE} size="sm" variant="minimal" className="h-full w-full flex-1" />
    </div>
  );
}

/** Same first-request prompt as the cloud-portal ALB overview logs card. */
function AlbLogsEmpty({ hostname }: { hostname: string }) {
  const command = `curl -I https://${hostname}/`;
  const [copied, copy] = useCopyToClipboard();

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col items-center justify-center gap-3 px-(--card-px) py-8 text-center">
      <span className="bg-muted flex size-10 items-center justify-center rounded-full">
        <Icon icon={RadioIcon} size={18} className="text-muted-foreground" aria-hidden="true" />
      </span>
      <div className="flex flex-col gap-1">
        <p className="text-sm font-medium">Waiting for the first request…</p>
        <p className="text-muted-foreground max-w-xs text-xs">
          Send a test request and it will appear here live.
        </p>
      </div>
      <div className="bg-muted/60 border-border flex w-full max-w-sm items-center gap-2 rounded-md border py-1.5 pr-1.5 pl-3 text-left">
        <code className="min-w-0 flex-1 font-mono text-xs break-all">{command}</code>
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground inline-flex size-6 shrink-0 items-center justify-center rounded-md"
          aria-label="Copy test request command"
          onClick={() => void copy(command, { withToast: true })}>
          <Icon icon={copied ? CheckIcon : CopyIcon} size={12} />
        </button>
      </div>
    </div>
  );
}

function LogsEmpty({ hostname, className }: { hostname?: string; className?: string }) {
  if (hostname) return <AlbLogsEmpty hostname={hostname} />;
  return <NoLogs className={className} />;
}

function isLogsDenied(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 401 || error.status === 403);
}

function relativeAge(date: Date): string {
  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  return `${formatDistanceToNowStrict(date, { roundingMethod: 'floor' })
    .replace(/ minutes?/, 'm')
    .replace(/ hours?/, 'h')
    .replace(/ days?/, 'd')} ago`;
}

function LivePulse() {
  return (
    <span
      className="relative flex size-4 items-center justify-center"
      role="img"
      aria-label="Active">
      <span className="size-2.5 rounded-full shadow-[0_0_0_3px_rgba(34,197,94,0.4)]" />
      <span className="absolute size-2.5 animate-pulse rounded-full bg-green-500" />
    </span>
  );
}

function RequestRow({ entry, logsHref }: { entry: LogEntry; logsHref: string }) {
  const parsed = parseLogLine(entry.line, entry.labels);
  const host = logRequestHost(entry.labels);

  if (parsed.kind !== 'http') {
    return (
      <li>
        <Link
          to={logsHref}
          className="hover:bg-muted/40 flex items-center gap-3 px-(--card-px) py-2 transition-colors">
          <span className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs">
            {parsed.line}
          </span>
          <span className="text-muted-foreground shrink-0 text-xs tabular-nums">
            {relativeAge(entry.timestamp)}
          </span>
        </Link>
      </li>
    );
  }

  return (
    <li>
      <Link
        to={logsHref}
        className="hover:bg-muted/40 flex items-center gap-3 px-(--card-px) py-2 transition-colors">
        <Badge
          type={httpStatusBadgeType(parsed.status)}
          theme="light"
          className="h-5 w-11 shrink-0 justify-center rounded-md px-0 font-mono text-[11px] font-medium tabular-nums">
          {parsed.status}
        </Badge>
        <span className="text-muted-foreground w-12 shrink-0 font-mono text-[11px] font-medium">
          {parsed.method}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-xs" title={parsed.path}>
          {parsed.path}
        </span>
        {host ? (
          <span className="text-muted-foreground hidden max-w-40 shrink-0 truncate text-xs lg:inline">
            {host}
          </span>
        ) : null}
        <span
          className={cn(
            'w-14 shrink-0 text-right font-mono text-xs tabular-nums',
            parsed.durationMs >= 1000 ? 'text-(--color-badge-warning)' : 'text-muted-foreground'
          )}>
          {parsed.durationMs >= 1000
            ? `${(parsed.durationMs / 1000).toFixed(1)}s`
            : `${Math.round(parsed.durationMs)}ms`}
        </span>
        <span className="text-muted-foreground w-16 shrink-0 text-right text-xs tabular-nums">
          {relativeAge(entry.timestamp)}
        </span>
      </Link>
    </li>
  );
}

/** Live feed for instance and workload Overview cards. */
export function RecentInstanceLogs({
  logsHref,
  projectId,
  proxyId,
  albHostname,
  instanceName,
  instanceNames,
  className,
}: {
  logsHref: string;
  projectId?: string;
  proxyId?: string;
  /** Set when a load balancer is attached. Empty results then show the curl prompt. */
  albHostname?: string;
  instanceName?: string;
  instanceNames?: readonly string[];
  className?: string;
}) {
  const [timeRange] = useState<LogTimeRange>(() => lastThirtyMinutes());
  const names = useMemo(
    () => instanceNames ?? (instanceName ? [instanceName] : []),
    [instanceName, instanceNames]
  );
  const logsQuery = useWorkloadLogs(projectId, proxyId, names, {
    timeRange,
    limit: ROW_LIMIT,
    live: true,
    enabled: names.length > 0 || !!proxyId,
  });

  const denied = isLogsDenied(logsQuery.error);
  const errorMessage = logsQuery.error && !denied ? logsQuery.error.message : undefined;
  const entries = (logsQuery.data ?? []).slice(0, ROW_LIMIT);
  const waitingForRequest =
    !!albHostname && !logsQuery.isLoading && !denied && !errorMessage && entries.length === 0;

  return (
    <Card
      size="sm"
      sectioned
      className={cn('relative flex h-full w-full flex-col overflow-hidden', className)}
      data-testid="compute-plugin-instance-logs">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={LogsIcon} size={16} className="text-secondary" />
          Logs
          {entries.length > 0 ? <LivePulse /> : null}
          {waitingForRequest ? (
            <Badge
              type="muted"
              theme="solid"
              className="h-5 gap-1.5 rounded-md px-1.5 text-[11px] font-medium whitespace-nowrap">
              <span className="bg-muted-foreground/60 size-1.5 rounded-full" aria-hidden="true" />
              Idle
            </Badge>
          ) : null}
        </CardTitle>
        <CardDescription className="text-xs">Most recent · last 30 min</CardDescription>
        <CardAction>
          <Link to={logsHref} className="text-primary text-xs font-medium hover:underline">
            View all
          </Link>
        </CardAction>
      </CardHeader>
      <CardContent padding="none" className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        {denied ? (
          <EmptyContent
            title="Access restricted"
            subtitle={DENIED_MESSAGE}
            size="sm"
            variant="dashed"
            className="min-h-48 flex-1"
          />
        ) : logsQuery.isLoading ? (
          <div className="flex h-full min-h-48 items-center justify-center">
            <SpinnerIcon size="sm" />
          </div>
        ) : errorMessage ? (
          <div className="text-muted-foreground flex h-full min-h-48 items-center justify-center px-(--card-px) text-center text-sm">
            Unable to load logs.
          </div>
        ) : entries.length === 0 ? (
          <LogsEmpty hostname={albHostname} className="min-h-48" />
        ) : (
          <ul className="divide-border divide-y">
            {entries.map((entry) => (
              <RequestRow key={entry.id} entry={entry} logsHref={logsHref} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

/** Full log explorer for the instance Logs tab. */
export function InstanceLogsExplorer({
  projectId,
  proxyId,
  instanceName,
  className,
}: {
  projectId?: string;
  proxyId?: string;
  instanceName?: string;
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

  const logsQuery = useInstanceLogs(projectId, proxyId, instanceName, {
    timeRange,
    filters,
    search,
    live,
    enabled: !!proxyId || !!instanceName,
  });

  const visibleEntries = useMemo(
    () => filterCombinedLogs(logsQuery.data ?? [], filters),
    [logsQuery.data, filters]
  );
  const facets = useMemo(() => combinedLogFacets(logsQuery.data ?? []), [logsQuery.data]);

  const denied = isLogsDenied(logsQuery.error);
  const errorMessage = logsQuery.error && !denied ? logsQuery.error.message : undefined;

  if (!proxyId && !instanceName) {
    return (
      <div
        className={cn('flex min-h-96 flex-1 flex-col', className)}
        data-testid="compute-plugin-instance-logs-explorer">
        <NoLogs className="min-h-96" />
      </div>
    );
  }

  if (denied) {
    return (
      <div
        className={cn('flex min-h-96 flex-col', className)}
        data-testid="compute-plugin-instance-logs-explorer">
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
      data-testid="compute-plugin-instance-logs-explorer">
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
          {/* Same class string as the portal's AlbLogsExplorer so the filters bar matches. */}
          <Logs.Explorer className="bg-card **:data-[slot=logs-filters]:bg-card min-h-0 flex-1" />
        </Logs.Root>
      </CardContent>
    </Card>
  );
}
