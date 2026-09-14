/**
 * Instance Overview tab body. Mounted under {@link ./instance-detail.tsx}'s
 * layout shell via `<Outlet />` — chrome stays mounted across tab changes.
 */
import { CommandBlock } from '../components/cli-section';
import { DetailList, StatusBadge } from '../components/detail-list';
import { RecentInstanceLogs } from '../components/instance-logs';
import { MetricAreaChart, formatKpiValue } from '../components/metric-area-chart';
import { useInstanceOutlet } from './instance-outlet-context';
import { formatLocationName, useLocationIndex } from '../lib/locations';
import {
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  cpuUsageQuery,
  memoryUsageQuery,
  networkIoQuery,
  useInstanceMetricIdentity,
} from '../lib/metrics-queries';
import { lastThirtyMinutesRange, usePrometheusCard } from '../lib/prometheus';
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
import { toast } from '@datum-cloud/datum-ui/toast';
import { cn } from '@datum-cloud/datum-ui/utils';
import {
  ChartColumnIncreasingIcon,
  CheckIcon,
  CopyIcon,
  SquareLibraryIcon,
  SquareTerminalIcon,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link } from 'react-router';

const COMING_SOON = 'Coming soon';

/** Minimal local stand-in for the portal's internal `TextCopy` / `BadgeCopy`. */
function CopyableText({
  value,
  text,
  className,
  textClassName,
}: {
  value: string;
  text?: string;
  className?: string;
  textClassName?: string;
}) {
  const [, copy] = useCopyToClipboard();
  const [copied, setCopied] = useState(false);
  const display = text ?? value;

  return (
    <button
      type="button"
      title={display}
      onClick={() =>
        copy(value).then(() => {
          toast.success('Copied to clipboard');
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        })
      }
      className={cn('inline-flex max-w-full min-w-0 items-center gap-1.5 text-left', className)}>
      <span className={cn('min-w-0 truncate', textClassName)}>{display}</span>
      {copied ? (
        <Icon icon={CheckIcon} size={14} className="shrink-0" />
      ) : (
        <Icon icon={CopyIcon} size={14} className="shrink-0 opacity-60" />
      )}
    </button>
  );
}

/**
 * Canonical ALB hostname with the same hover-copy treatment as portal
 * `ValueRow` (copy button fades in on the CardField row).
 */
function HostnameCopyValue({ value }: { value: string }) {
  const [copied, copy] = useCopyToClipboard();

  return (
    <span className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 truncate font-mono text-sm" title={value}>
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

function ComingSoonValue() {
  return <span className="text-muted-foreground">{COMING_SOON}</span>;
}

function formatCpu(cpu?: string): string | undefined {
  if (!cpu) return undefined;
  if (/^\d+(\.\d+)?$/.test(cpu)) {
    const n = Number(cpu);
    return `${cpu} ${n === 1 ? 'core' : 'cores'}`;
  }
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

function GeneralCard({
  instance,
  projectId,
  proxyId,
  albHostname,
  albDisplayName,
  albLoading,
  locationLabel,
}: {
  instance: Instance;
  projectId?: string;
  proxyId?: string;
  albHostname?: string;
  albDisplayName?: string;
  albLoading?: boolean;
  locationLabel: string;
}) {
  const cpu = formatCpu(instance.cpu);
  const memory = formatMemory(instance.memory);

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
              content: (
                <StatusBadge type={instanceStatusToBadgeType(instance.status)}>
                  {instance.status}
                </StatusBadge>
              ),
            },
            {
              label: 'Resource Name',
              content: <CopyableText value={instance.name} className="font-mono" />,
            },
            {
              label: 'Location',
              content: instance.location ? (
                <span title={instance.location}>{locationLabel}</span>
              ) : (
                <span className="text-muted-foreground">—</span>
              ),
            },
            {
              label: 'Default Hostname',
              className: 'group/row',
              content: albLoading ? (
                <span className="text-muted-foreground">—</span>
              ) : albHostname ? (
                <HostnameCopyValue value={albHostname} />
              ) : (
                <ComingSoonValue />
              ),
            },
            {
              label: 'Load balancer',
              content: albLoading ? (
                <span className="text-muted-foreground">—</span>
              ) : proxyId && projectId ? (
                <Link
                  to={`/project/${projectId}/alb/${proxyId}/overview`}
                  className="text-primary text-sm hover:underline">
                  {albDisplayName || proxyId}
                </Link>
              ) : (
                <span className="text-muted-foreground">Not connected</span>
              ),
            },
            {
              label: 'Created At',
              content: formatCreatedAt(instance.createdAt),
            },
            {
              label: 'vCPU',
              content: cpu ?? <ComingSoonValue />,
            },
            {
              label: 'Memory',
              content: memory ?? <ComingSoonValue />,
            },
          ]}
        />
      </CardContent>
    </Card>
  );
}

function MetricsCard({
  projectId,
  instanceName,
  proxyId,
  metricsHref,
}: {
  projectId?: string;
  instanceName: string;
  proxyId?: string;
  metricsHref: string;
}) {
  const { identity, isLoading: identityLoading } = useInstanceMetricIdentity(projectId, instanceName);
  const enabled = !identityLoading && !!identity && !!projectId;
  const cpuQuery = enabled && identity && projectId ? cpuUsageQuery(projectId, identity) : undefined;
  const memoryQuery =
    enabled && identity && projectId ? memoryUsageQuery(projectId, identity) : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const p99Query = projectId && proxyId ? albP99Query(projectId, proxyId) : undefined;
  const errorQuery = projectId && proxyId ? albErrorRateQuery(projectId, proxyId) : undefined;

  const timeRange = useMemo(() => lastThirtyMinutesRange(), []);
  const networkQuery = enabled && identity && projectId ? networkIoQuery(projectId, identity) : undefined;

  const cpu = usePrometheusCard(cpuQuery, 'number', { enabled });
  const memory = usePrometheusCard(memoryQuery, 'bytes', { enabled });
  const rps = usePrometheusCard(rpsQuery, 'requestsPerSecond', { enabled: !!proxyId });
  const p99 = usePrometheusCard(p99Query, 'milliseconds-auto', { enabled: !!proxyId });
  const errors = usePrometheusCard(errorQuery, 'percent', { enabled: !!proxyId });

  const kpis: Array<{ label: string; value: string }> = [
    { label: 'CPU', value: cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, 'number') },
    { label: 'Memory', value: memory.data?.formattedValue ?? formatKpiValue(memory.data?.value, 'bytes') },
    {
      label: 'Requests',
      value: proxyId
        ? (rps.data?.formattedValue ?? formatKpiValue(rps.data?.value, 'requestsPerSecond'))
        : COMING_SOON,
    },
    {
      label: 'p99',
      value: proxyId
        ? (p99.data?.formattedValue ?? formatKpiValue(p99.data?.value, 'milliseconds-auto'))
        : COMING_SOON,
    },
    {
      label: 'Errors',
      value: proxyId
        ? (errors.data?.formattedValue ?? formatKpiValue(errors.data?.value, 'percent'))
        : COMING_SOON,
    },
  ];

  return (
    <Card
      size="sm"
      sectioned
      className="w-full flex-1 overflow-hidden"
      data-testid="compute-plugin-instance-metrics">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={ChartColumnIncreasingIcon} size={16} className="text-secondary" />
          Metrics
        </CardTitle>
        <CardAction>
          <Link to={metricsHref} className="text-primary text-xs font-medium hover:underline">
            View all
          </Link>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="divide-border border-border flex divide-x overflow-x-auto overscroll-x-contain rounded-lg border [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {kpis.map((kpi) => (
            <div key={kpi.label} className="flex min-w-24 flex-1 flex-col gap-1 px-3 py-3">
              <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
                {kpi.label}
              </p>
              <p
                className={cn(
                  'text-xs whitespace-nowrap sm:text-sm',
                  kpi.value === COMING_SOON && 'text-muted-foreground'
                )}>
                {kpi.value}
              </p>
            </div>
          ))}
        </div>
        <div>
          <p className="text-muted-foreground mb-2 text-xs font-medium tracking-wide uppercase">
            Network I/O
          </p>
          <MetricAreaChart
            title="Network I/O"
            query={networkQuery}
            timeRange={timeRange}
            format="bytesPerSecond"
            enabled={enabled}
            embedded
            height={144}
          />
        </div>
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
        <div className="flex h-full flex-col gap-6">
          <GeneralCard
            instance={instance}
            projectId={projectId}
            proxyId={proxyId}
            albHostname={albHostname}
            albDisplayName={albDisplayName}
            albLoading={albLoading}
            locationLabel={formatLocationName(instance.location, locationIndex)}
          />
          <MetricsCard
            projectId={projectId}
            instanceName={instance.name}
            proxyId={proxyId}
            metricsHref={metricsHref}
          />
        </div>
        <RecentInstanceLogs
          logsHref={logsHref}
          projectId={projectId}
          proxyId={proxyId}
          instanceName={instance.name}
          albHostname={albHostname}
        />
      </div>

      <Card size="sm" sectioned className="w-full overflow-hidden" data-testid="compute-plugin-instance-cli">
        <CardHeader size="sm" bordered>
          <CardTitle className="flex items-center gap-2 text-sm">
            <Icon icon={SquareTerminalIcon} size={16} className="text-secondary" />
            datumctl Commands
          </CardTitle>
          <CardDescription>CLI</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">Get instance</span>
              <CommandBlock value={`datumctl compute instances get ${instance.name}`} />
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">List instances</span>
              <CommandBlock
                value={
                  workloadName
                    ? `datumctl compute instances list --workload=${workloadName}`
                    : 'datumctl compute instances list'
                }
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">View logs</span>
              <CommandBlock value={`datumctl compute instances logs ${instance.name} --follow`} />
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-muted-foreground text-xs">Describe</span>
              <CommandBlock
                value={`datumctl compute instances describe ${instance.name} --output yaml`}
              />
            </div>
          </div>
        </CardContent>
      </Card>
    </>
  );
}
