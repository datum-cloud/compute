/**
 * Workload overview topology card: ALBs, the workload, and its instances in a
 * static PlanetScale-style diagram, with live metrics in each node.
 */
import { formatKpiValue } from './metric-area-chart';
import {
  TopologyCanvas,
  TopologyRow,
  type TopologyAlb,
  type TopologyInstance,
} from './topology-canvas';
import type { ConnectedAlb } from '../lib/api';
import { parseCpuCores, parseMemoryBytes } from '../lib/resource-usage';
import { formatUptime, splitSlashValue } from '../lib/format';
import { formatLocationName, type LocationIndex } from '../lib/locations';
import { albStatusDisplay, type AlbStatus } from '../lib/alb-status';
import { instanceStatusLabel, type StatusDisplay } from '../lib/workload-presenters';
import {
  ALB_INSTANT_WINDOW,
  albErrorRateQuery,
  albP99Query,
  albRpsQuery,
  cpuUsageQuery,
  memoryUsageQuery,
  useInstanceMetricIdentity,
} from '../lib/metrics-queries';
import { usePrometheusCard } from '../lib/prometheus';
import {
  instanceStatusToBadgeType,
  workloadHealthToBadgeType,
  type Instance,
  type Workload,
} from '../schema';
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { WaypointsIcon } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';

function percent(numerator?: number, denominator?: number): string | undefined {
  if (numerator === undefined || !denominator) return undefined;
  const pct = (numerator / denominator) * 100;
  if (!Number.isFinite(pct)) return undefined;
  return `${pct < 10 ? pct.toFixed(1) : pct.toFixed(0)}%`;
}

function Strong({ children, tone }: { children: React.ReactNode; tone?: string }) {
  return <span className={`cpt-strong ${tone ?? ''}`}>{children}</span>;
}

function AlbBody({
  projectId,
  proxyId,
  onTraffic,
}: {
  projectId: string;
  proxyId: string;
  onTraffic: (proxyId: string, rps: number | undefined) => void;
}) {
  // Instant queries: use a 5m window so low-volume bursts register (see metrics-queries).
  const rps = usePrometheusCard(albRpsQuery(projectId, proxyId, ALB_INSTANT_WINDOW), 'requestsPerSecond');
  const errors = usePrometheusCard(albErrorRateQuery(projectId, proxyId, ALB_INSTANT_WINDOW), 'percent');
  const p99 = usePrometheusCard(albP99Query(projectId, proxyId, ALB_INSTANT_WINDOW), 'milliseconds-auto');

  const rpsValue = rps.data?.value;
  useEffect(() => {
    onTraffic(proxyId, rpsValue);
  }, [onTraffic, proxyId, rpsValue]);

  // The portal returns 0 for an empty result; with no requests, 0/0 is "no data", not 0%.
  const hasTraffic = rpsValue !== undefined && rpsValue > 0;
  const errorValue = hasTraffic ? errors.data?.value : undefined;
  const p99Value = hasTraffic ? p99.data?.value : undefined;
  const errorTone =
    errorValue !== undefined && errorValue > 0.05
      ? 'cpt-status-danger'
      : errorValue !== undefined && errorValue > 0
        ? 'cpt-status-warning'
        : undefined;

  return (
    <>
      <TopologyRow
        label={`traffic · ${ALB_INSTANT_WINDOW}`}
        value={
          <>
            <Strong tone="cpt-status-success">
              {formatKpiValue(rpsValue, 'requestsPerSecond')}
            </Strong>
            <span className="cpt-muted"> req</span>
          </>
        }
      />
      <TopologyRow
        label="errors"
        value={<Strong tone={errorTone}>{formatKpiValue(errorValue, 'percent')}</Strong>}
      />
      <TopologyRow label="p99" value={formatKpiValue(p99Value, 'milliseconds-auto')} mono />
    </>
  );
}

function InstanceBody({
  projectId,
  instance,
}: {
  projectId?: string;
  instance: Instance;
}) {
  const { identity } = useInstanceMetricIdentity(projectId, instance);
  const cpu = usePrometheusCard(
    projectId && identity ? cpuUsageQuery(projectId, identity) : undefined,
    'number'
  );
  const memory = usePrometheusCard(
    projectId && identity ? memoryUsageQuery(projectId, identity) : undefined,
    'bytes'
  );

  const cpuPct = percent(cpu.data?.value, parseCpuCores(instance.cpu));
  const memPct = percent(memory.data?.value, parseMemoryBytes(instance.memory));
  const ip = instance.externalIP ?? instance.internalIP;

  return (
    <>
      <TopologyRow
        label="CPU · Mem"
        value={
          identity ? (
            <>
              <Strong>{cpuPct ?? '—'}</Strong>
              <span className="cpt-muted"> · </span>
              <Strong>{memPct ?? '—'}</Strong>
            </>
          ) : (
            <span className="cpt-muted">
              {[instance.cpu ? `${instance.cpu} vCPU` : null, instance.memory]
                .filter(Boolean)
                .join(' · ') || '—'}
            </span>
          )
        }
      />
      {/* Always rendered so the node doesn't grow when the address is assigned. */}
      <TopologyRow
        label={instance.externalIP ? 'public' : 'internal'}
        value={ip ?? <span className="cpt-muted">—</span>}
        mono={!!ip}
        title={ip}
      />
    </>
  );
}

export function TopologyCard({
  projectId,
  workload,
  instances,
  albs,
  locationIndex,
  instanceHref,
  instanceMetricsHref,
  albHref,
  albMetricsHref,
  status,
  albStatuses = {},
}: {
  projectId?: string;
  workload: Workload;
  instances: Instance[];
  albs: ConnectedAlb[];
  locationIndex: LocationIndex;
  instanceHref: (name: string) => string;
  instanceMetricsHref: (name: string) => string;
  albHref?: (proxyName: string) => string;
  albMetricsHref?: (proxyName: string) => string;
  /** Overrides the workload's raw health, e.g. while it is deploying. */
  status?: StatusDisplay;
  /** Per-proxy serving state, keyed by HTTPProxy name. */
  albStatuses?: Record<string, AlbStatus>;
}) {
  const [traffic, setTraffic] = useState<Record<string, number | undefined>>({});
  const handleTraffic = useCallback((proxyId: string, rps: number | undefined) => {
    setTraffic((prev) => (prev[proxyId] === rps ? prev : { ...prev, [proxyId]: rps }));
  }, []);
  // Drop entries for ALBs that have been detached so the map does not grow unbounded.
  useEffect(() => {
    setTraffic((prev) => {
      const live = new Set(albs.map((alb) => alb.proxyName));
      const stale = Object.keys(prev).filter((id) => !live.has(id));
      if (stale.length === 0) return prev;
      const next = { ...prev };
      for (const id of stale) delete next[id];
      return next;
    });
  }, [albs]);

  const albNodes: TopologyAlb[] = useMemo(
    () =>
      albs.map((alb) => {
        const albStatus = albStatuses[alb.proxyName];
        const display = albStatus ? albStatusDisplay(albStatus) : undefined;
        return {
          id: alb.proxyName,
          title: alb.displayName || alb.proxyName,
          hostname: alb.hostname,
          traffic: traffic[alb.proxyName],
          href: albHref?.(alb.proxyName),
          metricsHref: albMetricsHref?.(alb.proxyName),
          status: display ? { tone: display.tone, label: display.short } : undefined,
          // No traffic to show until it serves: say what it is waiting on
          // instead, in as many rows as the traffic body so the node keeps its size.
          body:
            albStatus && albStatus.phase !== 'ready' ? (
              <>
                <TopologyRow
                  label={albStatus.phase === 'error' ? 'error' : 'status'}
                  value={albStatus.phase === 'error' ? albStatus.message : albStatus.step}
                  title={albStatus.message}
                />
                <TopologyRow label={`traffic · ${ALB_INSTANT_WINDOW}`} value={<span className="cpt-muted">—</span>} />
                <TopologyRow label="errors" value={<span className="cpt-muted">—</span>} />
              </>
            ) : projectId ? (
              <AlbBody projectId={projectId} proxyId={alb.proxyName} onTraffic={handleTraffic} />
            ) : undefined,
        };
      }),
    [albs, albStatuses, projectId, traffic, handleTraffic, albHref, albMetricsHref]
  );

  const instanceNodes: TopologyInstance[] = useMemo(
    () =>
      instances.map((instance) => {
        const type = instance.instanceType ? splitSlashValue(instance.instanceType).main : undefined;
        const location = instance.location
          ? formatLocationName(instance.location, locationIndex)
          : undefined;
        return {
          id: instance.name,
          title: instance.name,
          location,
          group: location,
          status: instanceStatusToBadgeType(instance.status),
          statusLabel: instanceStatusLabel(instance),
          href: instanceHref(instance.name),
          metricsHref: instanceMetricsHref(instance.name),
          body: <InstanceBody projectId={projectId} instance={instance} />,
          footLeft:
            type ?? ([instance.cpu, instance.memory].filter(Boolean).join(' · ') || '—'),
          footLeftTitle: instance.instanceType,
          footRight: (
            <>
              <span className="cpt-muted">up </span>
              {formatUptime(instance.createdAt)}
            </>
          ),
        };
      }),
    [instances, locationIndex, projectId, instanceHref, instanceMetricsHref]
  );

  // While the workload deploys, stand in for the instances it hasn't created
  // yet. The graph then has its final shape from the start, rather than
  // re-fitting (and jumping) as each instance appears.
  const deploying = workload.health === 'Deploying' && !workload.deleting;
  const pendingNodes: TopologyInstance[] = useMemo(() => {
    if (!deploying) return [];
    const instanceClass = workload.resources ? splitSlashValue(workload.resources).main : undefined;
    return workload.placementRegions.flatMap((region) => {
      const created = instances.filter((instance) => instance.placement === region.name).length;
      const missing = Math.max(0, region.desiredReplicas - created);
      // Same grouping key a real instance gets, so swapping one in doesn't regroup.
      const location =
        region.locations.length === 1 ? formatLocationName(region.locations[0], locationIndex) : undefined;
      return Array.from({ length: missing }, (_, i) => ({
        id: `pending:${region.name}:${i}`,
        title: 'Creating instance…',
        location,
        group: location,
        status: 'info' as const,
        statusLabel: 'Creating',
        body: (
          <>
            <TopologyRow label="CPU · Mem" value={<span className="cpt-muted">—</span>} />
            <TopologyRow label="internal" value={<span className="cpt-muted">—</span>} />
          </>
        ),
        footLeft: instanceClass ?? '—',
        footRight: <span className="cpt-status-info">Creating…</span>,
      }));
    });
  }, [deploying, workload.placementRegions, workload.resources, instances, locationIndex]);
  const graphInstances = useMemo(
    () => (pendingNodes.length ? [...instanceNodes, ...pendingNodes] : instanceNodes),
    [instanceNodes, pendingNodes]
  );

  const summary = [
    albNodes.length === 1 ? '1 load balancer' : `${albNodes.length} load balancers`,
    instanceNodes.length === 1 ? '1 instance' : `${instanceNodes.length} instances`,
  ].join(' · ');

  const workloadLocation =
    workload.locations.length === 1
      ? formatLocationName(workload.locations[0], locationIndex)
      : workload.locations.length > 1
        ? `${workload.locations.length} locations`
        : undefined;

  const ready = instances.length
    ? instances.filter((instance) => instance.status === 'Available').length
    : workload.readyReplicas;
  const total = instances.length || workload.desiredReplicas;
  const workloadStatus = status?.tone ?? workloadHealthToBadgeType(workload.health);
  const workloadStatusLabel = status?.label ?? workload.health;
  const instanceClass = instances[0]?.instanceType
    ? splitSlashValue(instances[0].instanceType).main
    : workload.resources
      ? splitSlashValue(workload.resources).main
      : undefined;

  return (
    <Card
      size="sm"
      sectioned
      className="flex min-h-0 w-full flex-col overflow-hidden"
      data-testid="compute-plugin-topology">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={WaypointsIcon} size={16} className="text-secondary" />
          Topology
        </CardTitle>
        <CardAction>
          <span className="text-muted-foreground text-xs">{summary}</span>
        </CardAction>
      </CardHeader>
      {/* The canvas sets its own height: a fixed frame it pans inside, or on a
          narrow screen the height of its stacked tree. */}
      <CardContent padding="none" className="relative">
        <TopologyCanvas
          workload={{
            id: workload.name,
            title: workload.name,
            location: workloadLocation,
            image: workload.image,
            status: workloadStatus,
            statusLabel: workloadStatusLabel,
            body: (
              <>
                <TopologyRow
                  label="instances"
                  value={
                    <>
                      <Strong tone={`cpt-status-${workloadStatus}`}>{ready}</Strong>
                      <span className="cpt-muted">/{total} ready</span>
                    </>
                  }
                />
                <TopologyRow
                  label="locations"
                  value={workload.locations.length || '—'}
                />
                {workload.ports.length > 0 ? (
                  <TopologyRow
                    label="ports"
                    value={workload.ports.join(', ')}
                    mono
                    title={workload.ports.join(', ')}
                  />
                ) : null}
                <TopologyRow label="uptime" value={formatUptime(workload.createdAt)} mono />
              </>
            ),
          }}
          albs={albNodes}
          instances={graphInstances}
          chromeLeft={
            <span className="cpt-chip">
              <span className={`cpt-chip-dot cpt-bg-${workloadStatus}`} />
              {workloadStatusLabel}
            </span>
          }
          chromeRight={
            <span className="cpt-chip">
              {workload.runtimeType ? <span className="cpt-muted">{workload.runtimeType} · </span> : null}
              {instanceClass ?? `${total} × instance`}
            </span>
          }
        />
      </CardContent>
    </Card>
  );
}
