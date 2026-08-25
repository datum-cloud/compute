/**
 * `portal.page/project` extension at
 * `workloads/:workloadName/instances/:instanceName`, exposed as
 * `InstanceDetail`.
 *
 * Layout: General + Metrics (left), Recent Logs (right, last 30 min via
 * datum-ui Logs), datumctl Commands (bottom).
 */
import { CommandBlock } from '../components/cli-section';
import { DetailList, StatusBadge } from '../components/detail-list';
import { RecentInstanceLogs } from '../components/instance-logs';
import { InstancePageChrome } from '../components/instance-page-chrome';
import { ErrorOrRestrictedState, LoadingSkeleton } from '../components/states';
import { useInstance } from '../lib/api';
import { instanceStatusToBadgeType, type Instance } from '../schema';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
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
import { useState } from 'react';
import { useLocation, useParams } from 'react-router';

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
      className="w-full gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-instance-general">
      <CardContent className="p-0 sm:px-6 sm:pb-4">
        <div className="mb-4 flex items-center gap-2.5">
          <Icon icon={SquareLibraryIcon} size={20} className="text-muted-foreground" />
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
      className="w-full flex-1 gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
      data-testid="compute-plugin-instance-metrics">
      <CardContent className="flex flex-col gap-4 p-0 sm:px-6 sm:pb-4">
        <div className="flex items-center gap-2.5">
          <Icon icon={ChartColumnIncreasingIcon} size={20} className="text-muted-foreground" />
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

export default function InstanceDetail() {
  const { projectId, workloadName, instanceName } = useParams<{
    projectId: string;
    workloadName: string;
    instanceName: string;
  }>();
  const location = useLocation();

  const { data: instance, isLoading, error, refetch } = useInstance(projectId, instanceName);

  const overviewHref = location.pathname.replace(/\/$/, '');
  const logsHref = `${overviewHref}/logs`;
  const instancesHref = overviewHref.replace(/\/instances\/[^/]+$/, '');
  const workloadsHref = instancesHref.replace(/\/[^/]+$/, '');
  const projectHref = projectId ? `/project/${projectId}` : '/';
  const titleName = instance?.name ?? instanceName ?? 'Instance';

  return (
    <InstancePageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      instancesHref={instancesHref}
      overviewHref={overviewHref}
      logsHref={logsHref}
      titleName={titleName}
      workloadName={workloadName}
      instance={instance}
      onRefresh={() => void refetch()}>
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
            <div className="flex h-full flex-col gap-6">
              <GeneralCard instance={instance} />
              <MetricsCard />
            </div>
            <RecentInstanceLogs logsHref={logsHref} />
          </div>

          <Card
            className="w-full overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4"
            data-testid="compute-plugin-instance-cli">
            <CardContent className="p-0 sm:px-6 sm:pb-4">
              <div className="mb-4 flex flex-wrap items-center gap-2">
                <Icon icon={SquareTerminalIcon} size={20} className="text-muted-foreground" />
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
    </InstancePageChrome>
  );
}
