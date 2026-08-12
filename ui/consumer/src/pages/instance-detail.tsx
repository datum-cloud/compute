/**
 * `portal.page/project` extension at
 * `workloads/:workloadName/instances/:instanceName`, exposed as
 * `InstanceDetail`.
 *
 * Layout matches the instance-detail mockup: General + Metrics (left),
 * Recent Logs (right), datumctl Commands (bottom). Telemetry/logs slots
 * show muted "Coming soon" until those backends exist.
 */
import { CommandBlock } from '../components/cli-section';
import { DetailList, StatusBadge } from '../components/detail-list';
import { PluginTabs } from '../components/plugin-tabs';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance } from '../lib/api';
import { instanceStatusToBadgeType, type Instance, type InstanceStatusValue } from '../schema';
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@datum-cloud/datum-ui/breadcrumb';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { toast } from '@datum-cloud/datum-ui/toast';
import { cn } from '@datum-cloud/datum-ui/utils';
import { CheckIcon, CopyIcon, HomeIcon, RefreshCwIcon, SquareTerminalIcon } from 'lucide-react';
import { useState } from 'react';
import { useLocation, useParams } from 'react-router';

const COMING_SOON = 'Coming soon';

const INSTANCE_TABS = ['Overview', 'Metrics', 'Logs', 'Manage', 'Activity'];

const STATUS_DOT: Record<InstanceStatusValue, string> = {
  Available: 'bg-green-500',
  Pending: 'bg-blue-500',
  Failed: 'bg-red-500',
  Unknown: 'bg-muted-foreground',
};

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
        <CheckIcon className="size-3.5 shrink-0" />
      ) : (
        <CopyIcon className="size-3.5 shrink-0 opacity-60" />
      )}
    </button>
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
  // Normalize catalog-style "2Gi" / k8s quantities for display.
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

function GeneralCard({ instance }: { instance: Instance }) {
  const hostname = instance.externalIP;
  const cpu = formatCpu(instance.cpu);
  const memory = formatMemory(instance.memory);

  return (
    <Card
      className="h-full w-full gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-instance-general">
      <CardContent className="p-0 sm:px-6 sm:pb-4">
        <div className="mb-4 flex items-center gap-2.5">
          <span className="text-base font-semibold">General</span>
        </div>
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
              label: 'Default Hostname',
              content: hostname ? (
                <CopyableText
                  value={hostname}
                  className="text-primary font-mono text-xs"
                  textClassName="max-w-[250px]"
                />
              ) : (
                <ComingSoonValue />
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

function MetricsCard() {
  return (
    <Card
      className="h-full w-full gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-instance-metrics">
      <CardContent className="flex flex-col gap-4 p-0 sm:px-6 sm:pb-4">
        <div className="flex items-center gap-2.5">
          <span className="text-base font-semibold">Metrics</span>
        </div>
        <div className="divide-border border-border flex divide-x overflow-x-auto overscroll-x-contain rounded-lg border [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {['CPU', 'Memory', 'Requests', 'p99', 'Errors'].map((label) => (
            <div key={label} className="flex min-w-24 flex-1 flex-col gap-1 px-3 py-3">
              <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
                {label}
              </p>
              <p className="text-muted-foreground text-xs whitespace-nowrap sm:text-sm">
                {COMING_SOON}
              </p>
            </div>
          ))}
        </div>
        <div className="border-border bg-muted/40 text-muted-foreground flex h-36 items-center justify-center rounded-md border border-dashed text-xs">
          Network I/O — {COMING_SOON}
        </div>
        <p className="text-muted-foreground text-xs">View full metrics →</p>
      </CardContent>
    </Card>
  );
}

const LOG_SKELETON_ROWS = [
  { level: 'INFO', width: 'w-4/5' },
  { level: 'INFO', width: 'w-3/5' },
  { level: 'WARN', width: 'w-2/3' },
  { level: 'INFO', width: 'w-3/4' },
  { level: 'ERROR', width: 'w-1/2' },
  { level: 'INFO', width: 'w-5/6' },
  { level: 'INFO', width: 'w-2/5' },
  { level: 'WARN', width: 'w-3/5' },
  { level: 'INFO', width: 'w-3/4' },
  { level: 'INFO', width: 'w-1/2' },
  { level: 'ERROR', width: 'w-2/3' },
  { level: 'INFO', width: 'w-4/5' },
  { level: 'WARN', width: 'w-3/5' },
  { level: 'INFO', width: 'w-5/6' },
] as const;

const LOG_LEVEL_CLASS = {
  INFO: 'text-green-600 dark:text-green-500',
  WARN: 'text-yellow-600 dark:text-yellow-500',
  ERROR: 'text-red-600 dark:text-red-500',
} as const;

function RecentLogsCard() {
  return (
    <Card
      className="flex h-full w-full flex-col gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-instance-logs">
      <CardContent className="relative flex min-h-0 flex-1 flex-col p-0 sm:px-6 sm:pb-4">
        <div className="mb-4 flex items-center justify-between gap-2">
          <span className="text-base font-semibold">Recent Logs</span>
          <span className="text-muted-foreground text-xs">View all →</span>
        </div>
        <div
          className="pointer-events-none flex min-h-48 flex-1 flex-col justify-evenly gap-2.5 overflow-hidden opacity-50 sm:min-h-64 lg:min-h-72"
          aria-hidden="true">
          {LOG_SKELETON_ROWS.map((row, i) => (
            <div key={i} className="flex items-center gap-2 font-mono text-[10px] sm:gap-3 sm:text-xs">
              <span className={cn('w-9 shrink-0 font-medium sm:w-10', LOG_LEVEL_CLASS[row.level])}>
                {row.level}
              </span>
              <span className="bg-muted h-2 w-8 shrink-0 rounded-sm sm:h-2.5 sm:w-12" />
              <span className={cn('bg-muted h-2 rounded-sm sm:h-2.5', row.width)} />
            </div>
          ))}
        </div>
        <div className="absolute inset-0 flex items-center justify-center">
          <span className="bg-background text-muted-foreground border-border rounded-md border border-dashed px-3 py-1.5 text-xs">
            {COMING_SOON}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

export default function InstanceDetail() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const location = useLocation();

  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);

  const basePath = location.pathname.replace(/\/$/, '');
  const instancesHref = basePath.replace(/\/instances\/[^/]+$/, '');
  const workloadsHref = instancesHref.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = instance?.name ?? instanceName ?? 'Instance';

  return (
    <div data-testid="compute-plugin-instance-detail" className="flex min-w-0 flex-col gap-6">
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <HomeIcon className="size-4" />
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbLink href={workloadsHref}>Workloads</BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbLink href={instancesHref} className="truncate">
              {workloadName}
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem className="min-w-0">
            <BreadcrumbPage className="truncate">{titleName}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title={titleName}
        titleClassName="text-primary break-all sm:break-normal"
        className="flex-col items-start gap-3 sm:flex-row sm:items-center"
        description={
          instance ? (
            <span className="mt-1 flex items-center gap-2 text-sm">
              <span
                className={cn('size-2 shrink-0 rounded-full', STATUS_DOT[instance.status])}
                aria-hidden
              />
              <span>{instance.status}</span>
              {instance.city ? (
                <>
                  <span className="text-muted-foreground" aria-hidden>
                    ·
                  </span>
                  <span className="text-muted-foreground">{instance.city}</span>
                </>
              ) : null}
            </span>
          ) : undefined
        }
        actions={
          <button
            type="button"
            onClick={() => void refetch()}
            className="border-border hover:bg-muted inline-flex items-center justify-center gap-1.5 rounded-md border px-3 py-1.5 text-sm font-medium transition-colors">
            <RefreshCwIcon className="size-3.5" />
            Refresh
          </button>
        }
      />

      <PluginTabs tabs={INSTANCE_TABS} testId="compute-plugin-instance-tabs" />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && (error || !instance) && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this instance."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && instance && (
        <>
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div className="flex flex-col gap-6">
              <GeneralCard instance={instance} />
              <MetricsCard />
            </div>
            <RecentLogsCard />
          </div>

          <Card
            className="w-full overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
            data-testid="compute-plugin-instance-cli">
            <CardContent className="p-0 sm:px-6 sm:pb-4">
              <div className="mb-4 flex flex-wrap items-center gap-2">
                <SquareTerminalIcon className="text-muted-foreground size-4" />
                <span className="text-base font-semibold">datumctl Commands</span>
                <span className="text-muted-foreground text-xs sm:ml-auto">CLI</span>
              </div>
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
                <CommandBlock
                  value={`datumctl compute instances logs ${instance.name} --follow`}
                />
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
      )}
    </div>
  );
}
