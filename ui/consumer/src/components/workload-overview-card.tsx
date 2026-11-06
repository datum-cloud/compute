/**
 * The workload Overview's summary card, in the shape of the ALB overview's
 * health strip: a tinted status ring, a headline and detail, the public
 * endpoint, and when the workload last changed. Below that, either a crash
 * band (only while an instance is crashing), the deploy steps (while the
 * workload first comes up), or the last hour's stats.
 *
 * The asleep state, wake-ups, revision and image digest from the design are
 * left out until the API reports them (datum-cloud/compute#626).
 */
import { MetricSparkline } from './metric-sparkline';
import { useOverviewRange } from './overview-range';
import { albStatusDisplay, type AlbStatus } from '../lib/alb-status';
import { formatKpiValue } from './metric-area-chart';
import {
  albRpsQuery,
  workloadCpuSumQuery,
  workloadMemorySumQuery,
  type InstanceIdentityLabel,
} from '../lib/metrics-queries';
import type { DeployPhase, TimelineStep } from '../lib/deploy-presenters';
import { useWorkloadNetworkMetrics } from '../lib/network-interface-metrics';
import { usePrometheusChart, type ChartDataPoint } from '../lib/prometheus';
import {
  cpuUsageHint,
  formatCpuUsage,
  formatMemoryUsage,
  memoryUsageHint,
  type Allocation,
} from '../lib/resource-usage';
import {
  crashDetailText,
  formatAgo,
  imageShortName,
  instanceReadiness,
  STATUS_TONE_COLOR,
  type CrashDetail,
  type InstanceProblem,
  type OverviewStatus,
  type StatusTone,
} from '../lib/workload-presenters';
import type { Instance } from '../schema';
import { Card } from '@datum-cloud/datum-ui/card';
import { useCopyToClipboard } from '@datum-cloud/datum-ui/hooks';
import { Icon, SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { cn } from '@datum-cloud/datum-ui/utils';
import {
  CheckIcon,
  CircleCheckIcon,
  HourglassIcon,
  InfoIcon,
  CircleIcon,
  CircleXIcon,
  CopyIcon,
  GlobeIcon,
  LogsIcon,
  TriangleAlertIcon,
} from 'lucide-react';
import { useMemo, type ReactNode } from 'react';
import { Link } from 'react-router';

const INFO_COLOR = 'var(--color-badge-info)';

/** Text blocks keep this much width and push their neighbours onto the next line instead. */
const WRAP_BASIS = { flexBasis: '18rem' } as const;

// Inline: cloud-portal compiles no `h-1`.
const BAR_STYLE = { height: 4 } as const;

/** A tone's color at a low opacity, for the ring and the attention band. */
function tint(color: string, percent: number): string {
  return `color-mix(in oklab, ${color} ${percent}%, transparent)`;
}

const TONE_ICON: Record<StatusTone, typeof CircleCheckIcon> = {
  ok: CircleCheckIcon,
  warn: TriangleAlertIcon,
  error: CircleXIcon,
  muted: CircleIcon,
};

function StatusRing({ tone, phase }: { tone: StatusTone; phase?: DeployPhase }) {
  const ring = phase ? PHASE_RING[phase] : undefined;
  const color = ring?.color ?? STATUS_TONE_COLOR[tone];
  return (
    <span
      className="flex size-9 shrink-0 items-center justify-center rounded-full"
      style={{ background: tint(color, 14), color }}
      aria-hidden>
      {phase === 'deploying' ? <SpinnerIcon size="sm" /> : <Icon icon={ring?.icon ?? TONE_ICON[tone]} size={18} />}
    </span>
  );
}

export interface OverviewEndpoint {
  hostname: string;
  /** The load balancer's page in the portal. */
  albHref?: string;
  albStatus?: AlbStatus;
}

function Endpoint({ endpoint }: { endpoint: OverviewEndpoint }) {
  const [copied, copy] = useCopyToClipboard();
  const display = endpoint.albStatus ? albStatusDisplay(endpoint.albStatus) : undefined;
  const ready = !display;
  return (
    <span className="flex min-w-0 items-center gap-2 pt-1" data-e2e="compute-workload-alb-chip">
      <Icon
        icon={GlobeIcon}
        size={14}
        className={cn('shrink-0', ready ? 'text-primary' : 'text-muted-foreground')}
        aria-hidden
      />
      <a
        href={`https://${endpoint.hostname}`}
        target="_blank"
        rel="noreferrer"
        className={cn(
          'min-w-0 truncate font-mono text-xs hover:underline',
          ready ? 'text-primary' : 'text-muted-foreground'
        )}>
        https://{endpoint.hostname}
      </a>
      <Tooltip message={copied ? 'Copied' : 'Copy URL'}>
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground inline-flex shrink-0 cursor-pointer"
          aria-label={copied ? 'Copied' : 'Copy URL'}
          onClick={() => void copy(`https://${endpoint.hostname}`, { withToast: true })}>
          <Icon icon={copied ? CheckIcon : CopyIcon} size={13} aria-hidden />
        </button>
      </Tooltip>
      {display ? (
        <span className="text-muted-foreground truncate text-xs" title={display.detail}>
          · {display.short === 'Error' ? 'load balancer error' : 'not serving yet'}
        </span>
      ) : endpoint.albHref ? (
        <Link to={endpoint.albHref} className="text-muted-foreground hidden shrink-0 text-xs hover:underline sm:inline-flex">
          · load balancer
        </Link>
      ) : null}
    </span>
  );
}

/** The instance that is crashing, and what its runtime last reported. */
export interface OverviewCrash {
  instance: Instance;
  detail: CrashDetail;
  /** Where the instance runs, in words. */
  locationLabel: string;
  logsHref: string;
  instanceHref: string;
}

function CrashBand({ crash }: { crash: OverviewCrash }) {
  const color = STATUS_TONE_COLOR.warn;
  return (
    <div
      className="flex flex-wrap items-center gap-3 border-t px-4 py-3"
      style={{ background: tint(color, 8), borderColor: tint(color, 30) }}
      data-testid="compute-plugin-workload-crash">
      <Icon icon={TriangleAlertIcon} size={16} className="shrink-0" style={{ color }} aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5" style={WRAP_BASIS}>
        <span className="text-sm font-medium">
          {crash.instance.name} in {crash.locationLabel} keeps crashing
        </span>
        <span className="text-muted-foreground text-xs">{crashDetailText(crash.detail)}</span>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Link
          to={crash.instanceHref}
          className="bg-card hover:bg-accent rounded-md border px-2.5 py-1 text-xs font-medium">
          Instance
        </Link>
        <Link
          to={crash.logsHref}
          className="bg-foreground text-background inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium hover:opacity-90">
          <Icon icon={LogsIcon} size={12} aria-hidden />
          View logs
        </Link>
      </div>
    </div>
  );
}

const PHASE_RING: Record<DeployPhase, { color: string; icon?: typeof CircleCheckIcon }> = {
  deploying: { color: INFO_COLOR },
  deployed: { color: STATUS_TONE_COLOR.ok, icon: CircleCheckIcon },
  stalled: { color: STATUS_TONE_COLOR.warn, icon: HourglassIcon },
  failed: { color: STATUS_TONE_COLOR.error, icon: CircleXIcon },
};

/** While deploying: a slow pulse instead of fake progress, and what to expect. */
function DeployingNote() {
  return (
    <>
      <span className="relative block overflow-hidden" style={{ ...BAR_STYLE, height: 2, background: tint(INFO_COLOR, 12) }} aria-hidden>
        <span className="absolute inset-y-0 left-0 w-1/3 animate-pulse" style={{ background: tint(INFO_COLOR, 70) }} />
      </span>
      <p className="text-muted-foreground flex items-center gap-2 px-4 py-3 text-xs" data-testid="compute-plugin-workload-deploying">
        <Icon icon={InfoIcon} size={14} className="shrink-0" aria-hidden />
        Most deploys finish in under a minute. CPU, memory and network appear once an instance is ready.
      </p>
    </>
  );
}

const clock = (at: Date) => at.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

function stepColor(state: TimelineStep['state']): string {
  return state === 'done' ? STATUS_TONE_COLOR.ok : state === 'failed' ? STATUS_TONE_COLOR.error : STATUS_TONE_COLOR.warn;
}

/** Deployed: the steps on a line, each with its time and the gap from the one before. */
function TimelineTrack({ steps }: { steps: TimelineStep[] }) {
  return (
    <ol
      className="flex flex-col gap-3 border-t px-4 py-4 sm:flex-row sm:justify-between"
      style={{ background: tint(STATUS_TONE_COLOR.ok, 5), borderColor: tint(STATUS_TONE_COLOR.ok, 25) }}
      aria-label="Deploy timeline"
      data-testid="compute-plugin-workload-timeline">
      {steps.map((step, index) => {
        const gap = index > 0 ? Math.round((step.at.getTime() - steps[index - 1].at.getTime()) / 1000) : undefined;
        return (
          <li key={step.label} className="flex min-w-0 items-start gap-2">
            <span className="mt-1 size-2 shrink-0 rounded-full" style={{ background: stepColor(step.state) }} aria-hidden />
            <span className="flex min-w-0 flex-col">
              <span className="text-sm font-medium">{step.label}</span>
              <span className="text-muted-foreground font-mono text-xs">
                {clock(step.at)}
                {gap !== undefined ? ` · +${gap}s` : ''}
              </span>
            </span>
          </li>
        );
      })}
    </ol>
  );
}

/** Stalled and failed: steps reached so far, ending in the one it's stuck on. */
function SoFar({ steps }: { steps: TimelineStep[] }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t px-4 py-2.5" data-testid="compute-plugin-workload-so-far">
      <span className="text-muted-foreground text-xs font-semibold">So far</span>
      {steps.map((step) => (
        <span key={step.label} className="flex items-center gap-1.5 text-xs">
          <span className="size-2 shrink-0 rounded-full" style={{ background: stepColor(step.state) }} aria-hidden />
          {step.label}
          <span className="text-muted-foreground font-mono">
            {step.state === 'waiting' ? 'since ' : ''}
            {clock(step.at)}
          </span>
        </span>
      ))}
    </div>
  );
}

export interface OverviewAction {
  label: string;
  href: string;
  primary?: boolean;
}

/** Stalled and failed: the cause in plain words, the platform's message, and what to try. */
function ProblemBand({ problem, actions }: { problem: InstanceProblem; actions: OverviewAction[] }) {
  const color = STATUS_TONE_COLOR[problem.tone];
  return (
    <div
      className="flex gap-3 border-t px-4 py-4"
      style={{ background: tint(color, 6), borderColor: tint(color, 30) }}
      data-testid="compute-plugin-workload-problem">
      <Icon icon={problem.tone === 'error' ? CircleXIcon : TriangleAlertIcon} size={16} className="mt-0.5 shrink-0" style={{ color }} aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="text-sm font-semibold">{problem.title}</span>
          {actions.length > 0 ? (
            <span className="flex shrink-0 items-center gap-2">
              {actions.map((action) => (
                <Link
                  key={action.label}
                  to={action.href}
                  className={
                    action.primary
                      ? 'bg-foreground text-background rounded-md px-2.5 py-1 text-xs font-medium hover:opacity-90'
                      : 'bg-card hover:bg-accent rounded-md border px-2.5 py-1 text-xs font-medium'
                  }>
                  {action.label}
                </Link>
              ))}
            </span>
          ) : null}
        </div>
        {problem.message ? (
          <p className="text-muted-foreground font-mono text-xs" style={{ overflowWrap: 'anywhere' }}>
            {problem.message}
          </p>
        ) : null}
        {problem.hints.length > 0 ? (
          <ul className="flex flex-col gap-1 text-sm">
            {problem.hints.map((hint) => (
              <li key={hint} className="flex gap-2">
                <span className="text-muted-foreground" aria-hidden>
                  •
                </span>
                <span>{hint}</span>
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </div>
  );
}

function lastValue(points: readonly ChartDataPoint[] | undefined): number | undefined {
  for (let i = (points?.length ?? 0) - 1; i >= 0; i--) {
    const value = points?.[i]?.value;
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

/** Two series added point by point; they share timestamps because one range query samples both. */
function addSeries(
  a: readonly ChartDataPoint[] | undefined,
  b: readonly ChartDataPoint[] | undefined
): ChartDataPoint[] | undefined {
  if (!a && !b) return undefined;
  const sums = new Map<number, ChartDataPoint>();
  for (const point of [...(a ?? []), ...(b ?? [])]) {
    const prev = sums.get(point.timestamp);
    sums.set(point.timestamp, prev ? { ...prev, value: prev.value + point.value } : { ...point });
  }
  return [...sums.values()].sort((x, y) => x.timestamp - y.timestamp);
}

function StatCell({
  label,
  hint,
  value,
  children,
}: {
  label: string;
  hint?: string;
  value: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="bg-card flex min-w-0 flex-1 flex-col gap-1.5 px-4 py-3">
      <span className="flex items-baseline justify-between gap-2">
        <span className="text-muted-foreground text-xs">{label}</span>
        {hint ? <span className="text-muted-foreground truncate text-xs opacity-80">{hint}</span> : null}
      </span>
      <span className="text-lg font-semibold tabular-nums">{value}</span>
      {children}
    </div>
  );
}

function Stats({
  projectId,
  instances,
  identityLabel,
  metricKeys,
  metricsDenied,
  proxyId,
  allocation,
  totalCount,
}: {
  projectId?: string;
  instances: readonly Instance[];
  identityLabel?: InstanceIdentityLabel;
  metricKeys: readonly string[];
  metricsDenied: boolean;
  proxyId?: string;
  allocation: Allocation;
  totalCount: number;
}) {
  const range = useOverviewRange('1h');
  const enabled = !!projectId && !!identityLabel && metricKeys.length > 0 && !metricsDenied;
  const cpuQuery =
    enabled && projectId && identityLabel ? workloadCpuSumQuery(projectId, identityLabel, metricKeys) : undefined;
  const memoryQuery =
    enabled && projectId && identityLabel ? workloadMemorySumQuery(projectId, identityLabel, metricKeys) : undefined;
  const rpsQuery = projectId && proxyId && !metricsDenied ? albRpsQuery(projectId, proxyId) : undefined;
  const cpu = usePrometheusChart(projectId, cpuQuery, range.timeRange, { enabled: !!cpuQuery, keepPrevious: true });
  const memory = usePrometheusChart(projectId, memoryQuery, range.timeRange, {
    enabled: !!memoryQuery,
    keepPrevious: true,
  });
  const rps = usePrometheusChart(projectId, rpsQuery, range.timeRange, { enabled: !!rpsQuery, keepPrevious: true });
  const network = useWorkloadNetworkMetrics(metricsDenied ? undefined : projectId, instances);
  const rx = usePrometheusChart(projectId, network.receiveTotalQuery, range.timeRange, {
    enabled: !!network.receiveTotalQuery,
    keepPrevious: true,
  });
  const tx = usePrometheusChart(projectId, network.transmitTotalQuery, range.timeRange, {
    enabled: !!network.transmitTotalQuery,
    keepPrevious: true,
  });

  const cpuPoints = cpu.error ? undefined : cpu.data?.series[0]?.data;
  const memoryPoints = memory.error ? undefined : memory.data?.series[0]?.data;
  const rpsPoints = rps.error ? undefined : rps.data?.series[0]?.data;
  const cpuNow = lastValue(cpuPoints);
  const memoryNow = lastValue(memoryPoints);
  const rpsNow = lastValue(rpsPoints);
  const rxPoints = rx.error ? undefined : rx.data?.series[0]?.data;
  const txPoints = tx.error ? undefined : tx.data?.series[0]?.data;
  const rxNow = lastValue(rxPoints);
  const txNow = lastValue(txPoints);
  const ioPoints = useMemo(() => addSeries(rxPoints, txPoints), [rxPoints, txPoints]);
  const showNetwork = !!network.receiveTotalQuery || network.isLoading;

  const readiness = useMemo(() => instances.map((instance) => instanceReadiness(instance)), [instances]);
  const ready = readiness.filter((r) => r.tone === 'ok').length;
  const notReady = readiness.length - ready;

  const spark = (points: readonly ChartDataPoint[] | undefined, loading: boolean, color?: string, format: 'cores' | 'bytes' | 'requestsPerSecond' | 'bitsPerSecond' = 'cores', allocated?: number, flat = false) => (
    <MetricSparkline
      projectId={projectId}
      timeRange={range.timeRange}
      format={format}
      series={points ?? []}
      pending={loading && !points}
      allocated={allocated}
      color={color}
      flatWhenZero={flat}
      wide
      hideValue
    />
  );

  return (
    <div
      className="bg-border grid grid-cols-2 gap-px border-t sm:flex"
      data-testid="compute-plugin-workload-stats">
      <StatCell
        label="Instances"
        hint={notReady > 0 ? `${notReady} not ready` : undefined}
        value={`${ready} / ${totalCount}`}>
        <span className="flex h-8 items-center gap-1.5" aria-hidden>
          {readiness.slice(0, 24).map((r, index) => (
            <span
              key={index}
              className="size-3 rounded-sm"
              style={{ background: r.tone === 'muted' ? INFO_COLOR : STATUS_TONE_COLOR[r.tone] }}
            />
          ))}
        </span>
      </StatCell>
      <StatCell label="CPU" hint={cpuUsageHint(cpuNow, allocation.cores)} value={formatCpuUsage(cpuNow, allocation.cores)}>
        {spark(cpuPoints, cpu.isLoading, undefined, 'cores', allocation.cores)}
      </StatCell>
      <StatCell
        label="Memory"
        hint={memoryUsageHint(memoryNow, allocation.memoryBytes)}
        value={formatMemoryUsage(memoryNow, allocation.memoryBytes)}>
        {spark(memoryPoints, memory.isLoading, 'var(--color-chart-1)', 'bytes', allocation.memoryBytes)}
      </StatCell>
      {showNetwork ? (
        <StatCell
          label="Network"
          hint={
            rxNow !== undefined || txNow !== undefined
              ? `${formatKpiValue(rxNow, 'bitsPerSecond')} in · ${formatKpiValue(txNow, 'bitsPerSecond')} out`
              : 'in + out'
          }
          value={formatKpiValue(rxNow === undefined && txNow === undefined ? undefined : (rxNow ?? 0) + (txNow ?? 0), 'bitsPerSecond')}>
          {spark(ioPoints, rx.isLoading || tx.isLoading || network.isLoading, 'var(--color-chart-3)', 'bitsPerSecond', undefined, true)}
        </StatCell>
      ) : null}
      {proxyId ? (
        <StatCell label="Requests" hint="per second" value={formatKpiValue(rpsNow, 'requestsPerSecond')}>
          {spark(rpsPoints, rps.isLoading, INFO_COLOR, 'requestsPerSecond', undefined, true)}
        </StatCell>
      ) : null}
    </div>
  );
}

export interface WorkloadOverviewCardProps {
  status: OverviewStatus;
  /** A deploy state replaces the stats while it lasts; see `deployState`. */
  phase?: DeployPhase;
  /** Deployed: the full timeline. Stalled and failed: the steps so far. */
  timeline?: TimelineStep[];
  /** Stalled and failed: what is wrong. */
  problem?: InstanceProblem;
  problemActions?: OverviewAction[];
  endpoint?: OverviewEndpoint;
  image?: string;
  updatedAt: Date;
  now: number;
  crash?: OverviewCrash;
  projectId?: string;
  instances: readonly Instance[];
  totalCount: number;
  identityLabel?: InstanceIdentityLabel;
  metricKeys: readonly string[];
  metricsDenied: boolean;
  proxyId?: string;
  allocation: Allocation;
}

export function WorkloadOverviewCard(props: WorkloadOverviewCardProps) {
  const { status, phase, endpoint, image, updatedAt, now, crash } = props;
  const attention = status.tone === 'warn' || status.tone === 'error';
  const short = imageShortName(image);
  return (
    <Card
      sectioned
      size="sm"
      className="overflow-hidden"
      style={attention ? { borderColor: tint(STATUS_TONE_COLOR[status.tone], 45) } : undefined}
      data-testid="compute-plugin-workload-health">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3 px-4 py-4">
        <div className="flex min-w-0 flex-1 items-start gap-3" style={WRAP_BASIS}>
          <StatusRing tone={status.tone} phase={phase} />
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="text-base font-semibold">{status.title}</span>
            <span className="text-muted-foreground text-sm" title={status.detail}>
              {status.detail}
            </span>
            {endpoint ? <Endpoint endpoint={endpoint} /> : null}
          </div>
        </div>
        <div className="flex shrink-0 flex-col gap-0.5">
          <span className="text-sm font-medium" title={updatedAt.toLocaleString()}>
            Updated {formatAgo(updatedAt, now)}
          </span>
          {short ? (
            <span className="text-muted-foreground max-w-xs truncate font-mono text-xs" title={image}>
              {short}
            </span>
          ) : null}
        </div>
      </div>
      {phase === 'deploying' ? <DeployingNote /> : null}
      {phase === 'deployed' && props.timeline ? <TimelineTrack steps={props.timeline} /> : null}
      {(phase === 'stalled' || phase === 'failed') && props.problem ? (
        <ProblemBand problem={props.problem} actions={props.problemActions ?? []} />
      ) : null}
      {(phase === 'stalled' || phase === 'failed') && props.timeline ? <SoFar steps={props.timeline} /> : null}
      {!phase && crash ? <CrashBand crash={crash} /> : null}
      {phase ? null : (
        <Stats
          projectId={props.projectId}
          instances={props.instances}
          identityLabel={props.identityLabel}
          metricKeys={props.metricKeys}
          metricsDenied={props.metricsDenied}
          proxyId={props.proxyId}
          allocation={props.allocation}
          totalCount={props.totalCount}
        />
      )}
    </Card>
  );
}
