/**
 * Instance Overview tab body. Mounted under {@link ./instance-detail.tsx}'s
 * layout shell via `<Outlet />` — chrome stays mounted across tab changes.
 */
import { instanceFailure } from '../adapter';
import { CommandBlock } from '../components/cli-section';
import { DetailList, StatusBadge } from '../components/detail-list';
import { RecentInstanceLogs } from '../components/instance-logs';
import { MetricAreaChart } from '../components/metric-area-chart';
import { useInstanceOutlet } from './instance-outlet-context';
import { splitSlashValue } from '../lib/format';
import {
  cpuUsageHint,
  formatCpuUsage,
  formatMemoryUsage,
  memoryUsageHint,
  parseCpuCores,
  parseMemoryBytes,
} from '../lib/resource-usage';
import { formatLocationName, formatLocationTooltip, useLocationIndex } from '../lib/locations';
import { cpuUsageQuery, memoryUsageQuery, useInstanceMetricIdentity } from '../lib/metrics-queries';
import { lastThirtyMinutesRange, usePrometheusCard, type PrometheusTimeRange } from '../lib/prometheus';
import { instanceStatusLabel } from '../lib/workload-presenters';
import { instanceStatusToBadgeType, type Instance } from '../schema';
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { Button } from '@datum-cloud/datum-ui/button';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { cn } from '@datum-cloud/datum-ui/utils';
import { formatDistanceToNowStrict } from 'date-fns';
import {
  ChartColumnIncreasingIcon,
  CheckIcon,
  CopyIcon,
  GlobeIcon,
  NetworkIcon,
  SquareLibraryIcon,
  SquareTerminalIcon,
} from 'lucide-react';
import { useMemo, type ReactNode } from 'react';
import { Link } from 'react-router';

/** Rows that hold a `CopyValue` reveal its button on hover. */
const COPY_ROW = 'group/row';

/**
 * A value with the same hover-copy treatment as portal `ValueRow` (copy
 * button fades in on the CardField row).
 */
function CopyValue({ value, mono = true }: { value: string; mono?: boolean }) {
  const [copied, copy] = useCopyToClipboard();

  return (
    <span className="flex min-w-0 items-center gap-1">
      <span className={cn('min-w-0 truncate text-sm', mono && 'font-mono')} title={value}>
        {value}
      </span>
      <Button
        type="quaternary"
        theme="borderless"
        size="xs"
        className={cn(
          'text-muted-foreground hidden size-7 shrink-0 p-0 opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100 sm:inline-flex',
          copied && 'opacity-100'
        )}
        aria-label={copied ? 'Copied' : `Copy ${value}`}
        onClick={() => void copy(value, { withToast: true })}>
        <Icon icon={copied ? CheckIcon : CopyIcon} size={14} />
      </Button>
    </span>
  );
}

function CopyValues({ values }: { values: readonly string[] }) {
  return (
    <span className="flex min-w-0 flex-col">
      {values.map((value) => (
        <CopyValue key={value} value={value} />
      ))}
    </span>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}

function formatCpu(cpu?: string): string | undefined {
  if (!cpu) return undefined;
  if (/^\d+(\.\d+)?$/.test(cpu)) return `${cpu} vCPU`;
  return cpu;
}

function formatMemory(memory?: string): string | undefined {
  if (!memory) return undefined;
  const gi = memory.match(/^(\d+(?:\.\d+)?)Gi$/i);
  if (gi) return `${gi[1]} GiB`;
  const mi = memory.match(/^(\d+(?:\.\d+)?)Mi$/i);
  if (mi) return `${mi[1]} MiB`;
  return memory;
}

function formatCreatedAt(date: Date): string {
  return date.toLocaleDateString('en-GB', {
    day: '2-digit',
    month: 'short',
    year: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

/** "1 vCPU · 2 GiB", then the instance type it came from. */
function SizeValue({ instance }: { instance: Instance }) {
  const resources = [formatCpu(instance.cpu), formatMemory(instance.memory)].filter(Boolean);
  const type = instance.instanceType ? splitSlashValue(instance.instanceType).main : undefined;
  if (resources.length === 0 && !type) return <Muted>—</Muted>;

  return (
    <span className="flex min-w-0 flex-col">
      {resources.length > 0 ? <span>{resources.join(' · ')}</span> : null}
      {type ? (
        <span className="text-muted-foreground truncate text-xs" title={instance.instanceType}>
          {type}
        </span>
      ) : null}
    </span>
  );
}

/** Badge, plus what's wrong when the instance is failing. */
function StatusValue({ instance }: { instance: Instance }) {
  const failure = instanceFailure(instance.conditions);

  return (
    <span className="flex min-w-0 flex-col gap-1">
      <StatusBadge type={instanceStatusToBadgeType(instance.status)}>
        {instanceStatusLabel(instance)}
      </StatusBadge>
      {failure?.message ? (
        <span className="text-muted-foreground text-xs">{failure.message}</span>
      ) : null}
    </span>
  );
}

function GeneralCard({
  instance,
  locationLabel,
  locationTooltip,
}: {
  instance: Instance;
  locationLabel: string;
  locationTooltip?: string;
}) {
  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="compute-plugin-instance-general">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={SquareLibraryIcon} size={16} className="text-secondary" />
          General
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            {
              label: 'Status',
              content: <StatusValue instance={instance} />,
            },
            {
              label: 'Resource Name',
              className: COPY_ROW,
              content: <CopyValue value={instance.name} />,
            },
            {
              label: 'Location',
              content: instance.location ? (
                <span title={locationTooltip}>{locationLabel}</span>
              ) : (
                <Muted>—</Muted>
              ),
            },
            {
              label: 'Size',
              content: <SizeValue instance={instance} />,
            },
            {
              label: 'Image',
              hidden: !instance.image,
              className: COPY_ROW,
              content: instance.image ? <CopyValue value={instance.image} /> : null,
            },
            {
              label: 'Created',
              content: (
                <span title={formatCreatedAt(instance.createdAt)}>
                  {formatDistanceToNowStrict(instance.createdAt, { addSuffix: true })}
                </span>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function NetworkingCard({
  instance,
  projectId,
  proxyId,
  albHostname,
  albDisplayName,
  albLoading,
}: {
  instance: Instance;
  projectId?: string;
  proxyId?: string;
  albHostname?: string;
  albDisplayName?: string;
  albLoading?: boolean;
}) {
  // Addresses arrive once the instance is programmed; until then say so.
  const unassigned = instance.status === 'Pending' ? <Muted>Assigning…</Muted> : <Muted>—</Muted>;

  return (
    <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="compute-plugin-instance-networking">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={NetworkIcon} size={16} className="text-secondary" />
          Networking
        </CardTitle>
      </CardHeader>
      <CardContent padding="none">
        <DetailList
          items={[
            {
              label: instance.addresses.length > 1 ? 'Internal IPs' : 'Internal IP',
              className: COPY_ROW,
              content:
                instance.addresses.length > 0 ? <CopyValues values={instance.addresses} /> : unassigned,
            },
            {
              label: instance.externalIPs.length > 1 ? 'External IPs' : 'External IP',
              className: COPY_ROW,
              content:
                instance.externalIPs.length > 0 ? <CopyValues values={instance.externalIPs} /> : <Muted>None</Muted>,
            },
            {
              label: 'Private DNS',
              hidden: instance.hostnames.length === 0,
              className: COPY_ROW,
              content: <CopyValues values={instance.hostnames} />,
            },
            {
              label: 'Ports',
              hidden: instance.ports.length === 0,
              content: <span className="font-mono text-sm">{instance.ports.join(', ')}</span>,
            },
            {
              label: 'Default Hostname',
              hidden: !proxyId,
              className: COPY_ROW,
              content: albLoading ? (
                <Muted>—</Muted>
              ) : albHostname ? (
                <CopyValue value={albHostname} />
              ) : (
                <Muted>Coming soon</Muted>
              ),
            },
            {
              label: 'Load balancer',
              content: albLoading ? (
                <Muted>—</Muted>
              ) : proxyId && projectId ? (
                <Link
                  to={`/project/${projectId}/alb/${proxyId}/overview`}
                  className="text-primary inline-flex items-center gap-1.5 text-sm hover:underline">
                  <Icon icon={GlobeIcon} size={14} className="shrink-0" />
                  {albDisplayName || proxyId}
                </Link>
              ) : (
                <Muted>Not connected</Muted>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

/** Headline figure over a small chart of the same series. */
function UsageChart({
  projectId,
  title,
  value,
  hint,
  query,
  timeRange,
  format,
  rawFormat,
  allocated,
  color,
  enabled,
  pending,
  denied,
}: {
  projectId: string | undefined;
  title: string;
  value: string;
  hint?: string;
  query?: string;
  timeRange: PrometheusTimeRange;
  format: 'cores' | 'bytes';
  rawFormat: 'cores' | 'bytes';
  allocated?: number;
  color?: string;
  enabled: boolean;
  pending: boolean;
  denied: boolean;
}) {
  const divisor = useMemo(() => (allocated ? () => allocated : undefined), [allocated]);
  const muted = value === '—' || value === 'Loading…';

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-muted-foreground text-xs font-medium">{title}</span>
        <span className="flex items-baseline gap-2">
          {hint && !muted ? <span className="text-muted-foreground text-xs">{hint}</span> : null}
          <span
            className={cn(
              'font-semibold tabular-nums',
              muted ? 'text-muted-foreground text-sm' : 'text-foreground text-base'
            )}>
            {value}
          </span>
        </span>
      </div>
      <MetricAreaChart
        projectId={projectId}
        title={title}
        query={query}
        timeRange={timeRange}
        format={allocated ? 'percent' : format}
        seriesDivisor={divisor}
        rawFormat={rawFormat}
        color={color}
        enabled={enabled}
        pending={pending}
        denied={denied}
        embedded
        height={120}
      />
    </div>
  );
}

function MetricsCard({
  projectId,
  instance,
  proxyId,
  metricsHref,
}: {
  projectId?: string;
  instance: Instance;
  proxyId?: string;
  metricsHref: string;
}) {
  const { identity, isLoading: identityLoading, isDenied: identityDenied } = useInstanceMetricIdentity(projectId, instance);
  const enabled = !identityLoading && !identityDenied && !!identity && !!projectId;
  const cpuQuery = enabled && identity && projectId ? cpuUsageQuery(projectId, identity) : undefined;
  const memoryQuery =
    enabled && identity && projectId ? memoryUsageQuery(projectId, identity) : undefined;

  const timeRange = useMemo(() => lastThirtyMinutesRange(), []);

  const allocatedCores = parseCpuCores(instance.cpu);
  const allocatedMemory = parseMemoryBytes(instance.memory);
  const cpu = usePrometheusCard(projectId, cpuQuery, 'number', { enabled });
  const memory = usePrometheusCard(projectId, memoryQuery, 'bytes', { enabled });

  return (
    <Card
      size="sm"
      sectioned
      className="w-full overflow-hidden"
      data-testid="compute-plugin-instance-metrics">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={ChartColumnIncreasingIcon} size={16} className="text-secondary" />
          Metrics
        </CardTitle>
        <CardDescription className="text-xs">Last 30 min</CardDescription>
        <CardAction>
          <Link to={metricsHref} className="text-primary text-xs font-medium hover:underline">
            View all
          </Link>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <UsageChart
          projectId={projectId}
          title="CPU"
          value={identityLoading ? 'Loading…' : formatCpuUsage(cpu.data?.value, allocatedCores)}
          hint={allocatedCores ? cpuUsageHint(cpu.data?.value, allocatedCores) : undefined}
          query={cpuQuery}
          timeRange={timeRange}
          format="cores"
          rawFormat="cores"
          allocated={allocatedCores}
          enabled={enabled}
          pending={identityLoading}
          denied={identityDenied}
        />
        <UsageChart
          projectId={projectId}
          title="Memory"
          value={identityLoading ? 'Loading…' : formatMemoryUsage(memory.data?.value, allocatedMemory)}
          hint={allocatedMemory ? memoryUsageHint(memory.data?.value, allocatedMemory) : undefined}
          query={memoryQuery}
          timeRange={timeRange}
          format="bytes"
          rawFormat="bytes"
          allocated={allocatedMemory}
          color="var(--color-chart-1)"
          enabled={enabled}
          pending={identityLoading}
          denied={identityDenied}
        />
        {proxyId ? (
          <p className="text-muted-foreground text-xs">
            Requests, latency and errors are measured at the load balancer for the whole workload.{' '}
            <Link to={metricsHref} className="text-primary font-medium hover:underline">
              View traffic
            </Link>
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

export default function InstanceOverview() {
  const {
    instance,
    workloadName,
    logsHref,
    metricsHref,
    projectId,
    proxyId,
    albHostname,
    albDisplayName,
    albLoading,
  } = useInstanceOutlet();
  const locationIndex = useLocationIndex(projectId);

  return (
    <>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <GeneralCard
          instance={instance}
          locationLabel={formatLocationName(instance.location, locationIndex)}
          locationTooltip={formatLocationTooltip(instance.location, locationIndex)}
        />
        <NetworkingCard
          instance={instance}
          projectId={projectId}
          proxyId={proxyId}
          albHostname={albHostname}
          albDisplayName={albDisplayName}
          albLoading={albLoading}
        />
        <MetricsCard
          projectId={projectId}
          instance={instance}
          proxyId={proxyId}
          metricsHref={metricsHref}
        />
        {/*
          The metrics card sets this row's height and the logs scroll inside
          it; a long log list would otherwise stretch the row. The floor keeps
          the card usable when it stacks on narrow screens.
        */}
        <div style={{ display: 'flex', flexDirection: 'column', height: 0, minHeight: 'max(100%, 20rem)' }}>
          <RecentInstanceLogs
            className="flex-1"
            logsHref={logsHref}
            projectId={projectId}
            proxyId={proxyId}
            albHostname={albHostname}
            instanceName={instance.name}
            upstreamIPs={instance.internalIPs}
          />
        </div>
      </div>

      <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="compute-plugin-instance-cli">
        <CardHeader size="sm" bordered>
          <CardTitle className="flex items-center gap-2 text-sm">
            <Icon icon={SquareTerminalIcon} size={16} className="text-secondary" />
            datumctl Commands
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">Describe instance</span>
              <CommandBlock value={`datumctl compute instances describe ${instance.name}`} />
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">List instances</span>
              <CommandBlock
                value={
                  workloadName
                    ? `datumctl compute instances --workload=${workloadName}`
                    : 'datumctl compute instances'
                }
              />
            </div>
          </div>
        </CardContent>
      </Card>
    </>
  );
}
