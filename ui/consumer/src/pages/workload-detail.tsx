/**
 * `portal.page/project` extension at `:workloadName/*`, exposed as
 * `WorkloadDetail`. Overview is the index; Topology lives at `topology`,
 * Metrics at `metrics`, Logs at `logs`.
 *
 * Overview layout, laid out like the portal's project home (open sections,
 * hairline rows): the status line, the running instances, recent logs, live
 * traffic when a load balancer serves the workload, then its details.
 *
 * Breadcrumbs are left to the host `ContentWrapper` — do not re-render them
 * inside the plugin (that double-stacks chrome vs native pages).
 */
import { StatusBadge } from "../components/detail-list";
import { WorkloadHealthStrip } from "../components/health-strip";
import {
  DetailsSection,
  InstancesSection,
  PrivateReachLine,
  type DetailItem,
} from "../components/workload-overview-sections";
import { ingressText, instanceNotReady, portText, privateReach, scaleText } from "../lib/workload-overview";
import { RecentInstanceLogs } from "../components/instance-logs";
import { MetricAreaChart } from "../components/metric-area-chart";
import { TopologyCard } from "../components/topology-card";
import { WorkloadPageChrome } from "../components/workload-page-chrome";
import { DeleteWorkloadDialog, useDeleteWorkloadDialog } from "../components/delete-workload-dialog";
import {
  DEFAULT_OVERVIEW_RANGE,
  OVERVIEW_RANGE_OPTIONS,
  useOverviewRange,
  type OverviewRange,
  type OverviewRangeValue,
} from "../components/overview-range";
import {
  InstanceLogsSkeleton,
  WorkloadMetricsSkeleton,
  WorkloadOverviewSkeleton,
  WorkloadTopologySkeleton,
} from "../components/skeletons";
import { ErrorOrRestrictedState } from "../components/states";
import {
  useDeletePermissions,
  usePublishedUrl,
  useWorkload,
  useWorkloadInstances,
  type PublishedUrl,
} from "../lib/api";
import {
  albStatus,
  anyAlbProvisioning,
  albStatusDisplay,
  workloadUrlState,
  type AlbStatus,
} from "../lib/alb-status";
import { WorkloadUrl } from "../components/workload-url";
import { useNow } from "../lib/use-now";
import {
  deployStatus,
  instanceFailureSummary,
  type StatusDisplay,
} from "../lib/workload-presenters";
import { formatLocationNames, useLocationIndex } from "../lib/locations";
import {
  albRpsQuery,
  identityValues,
  useProjectResourceIdentity,
} from "../lib/metrics-queries";
import type { WorkloadOutletContext } from "./workload-outlet-context";
import { useWorkloadOutlet } from "./workload-outlet-context";
import WorkloadLogs from "./workload-logs";
import WorkloadMetrics from "./workload-metrics";
import { workloadHealthToBadgeType, type Workload } from "../schema";
import { Icon } from "@datum-cloud/datum-ui/icons";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@datum-cloud/datum-ui/select";
import {
  GlobeIcon,
  HistoryIcon,
  MapPinIcon,
  NetworkIcon,
  PackageIcon,
} from "lucide-react";
import { lazy, Suspense, useCallback, useMemo, useState } from "react";
import {
  Link,
  Outlet,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
} from "react-router";

// activity-ui is sizeable; load it only when the tab is opened.
const WorkloadActivity = lazy(() => import("./workload-activity"));

// Inline rather than `h-[27rem]`: the host only compiles that class because its
// own ALB overview happens to use it today.
const TRAFFIC_STYLE = { height: "16rem" } as const;

function albOverviewHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/overview`;
}

function albMetricsHref(projectId: string, proxyName: string): string {
  return `/project/${projectId}/alb/${proxyName}/metrics`;
}

function LoadBalancerValue({
  projectId,
  published,
  isLoading,
  albStatuses,
}: {
  projectId?: string;
  published?: PublishedUrl | null;
  isLoading: boolean;
  albStatuses: Record<string, AlbStatus>;
}) {
  if (isLoading) {
    return <span className="text-muted-foreground">—</span>;
  }
  if (!published?.proxies.length) {
    return <span className="text-muted-foreground">Not connected</span>;
  }

  return (
    <div className="flex flex-col gap-1">
      {published.proxies.map((alb) => {
        const label = alb.displayName || alb.proxyName;
        const albStatus = albStatuses[alb.proxyName];
        const display = albStatus ? albStatusDisplay(albStatus) : undefined;
        const badge =
          albStatus && albStatus.phase !== "ready" && display ? (
            <span title={albStatus.message}>
              <StatusBadge type={display.tone}>
                {albStatus.phase === "provisioning" ? albStatus.step : "Error"}
              </StatusBadge>
            </span>
          ) : null;
        if (!projectId) {
          return (
            <span
              key={alb.proxyName}
              className="inline-flex items-center gap-1.5 text-sm"
            >
              <Icon
                icon={GlobeIcon}
                size={14}
                className="text-muted-foreground shrink-0"
              />
              {label}
              {badge}
            </span>
          );
        }
        return (
          <span key={alb.proxyName} className="inline-flex items-center gap-2">
            <Link
              to={albOverviewHref(projectId, alb.proxyName)}
              className="text-primary inline-flex items-center gap-1.5 text-sm hover:underline"
            >
              <Icon icon={GlobeIcon} size={14} className="shrink-0" />
              {label}
            </Link>
            {badge}
          </span>
        );
      })}
    </div>
  );
}

/** Requests through the load balancer, as an open section like the rest of the Overview. */
function LiveTraffic({
  projectId,
  proxyId,
  range,
  onRangeChange,
}: {
  projectId?: string;
  proxyId?: string;
  range: OverviewRange;
  onRangeChange: (value: OverviewRangeValue) => void;
}) {
  const rpsQuery =
    projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;

  return (
    <section
      aria-labelledby="compute-live-traffic-heading"
      className="flex min-w-0 flex-col gap-2"
    >
      <div className="flex items-center gap-2">
        <h2 id="compute-live-traffic-heading" className="text-sm font-medium">
          Live traffic
        </h2>
        <span className="text-muted-foreground text-xs">requests per second</span>
        <span className="ml-auto">
          <Select
            value={range.value}
            onValueChange={(value) =>
              onRangeChange(value as OverviewRangeValue)
            }
          >
            <SelectTrigger
              className="h-7 w-auto gap-1.5 text-xs"
              aria-label="Live traffic time range"
              data-e2e="compute-overview-range"
              data-testid="compute-plugin-overview-range"
            >
              <Icon
                icon={HistoryIcon}
                size={12}
                className="text-muted-foreground"
              />
              <SelectValue />
            </SelectTrigger>
            <SelectContent align="end">
              {OVERVIEW_RANGE_OPTIONS.map((option) => (
                <SelectItem
                  key={option.value}
                  value={option.value}
                  className="text-xs"
                >
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </span>
      </div>
      <div
        className="relative flex flex-col rounded-lg border p-4"
        style={TRAFFIC_STYLE}
      >
        {rpsQuery ? (
          <MetricAreaChart
            title="Requests"
            query={rpsQuery}
            timeRange={range.timeRange}
            format="requestsPerSecond"
            enabled
            embedded
            fill
          />
        ) : (
          <p className="text-muted-foreground flex flex-1 items-center justify-center text-sm">
            Not connected
          </p>
        )}
      </div>
    </section>
  );
}

/** A small inline value: an icon and a mono label. */
function SummaryChip({ icon, children, title }: { icon: typeof GlobeIcon; children: React.ReactNode; title?: string }) {
  return (
    <span
      className="bg-muted inline-flex min-w-0 items-center gap-1 rounded px-1.5 py-0.5"
      title={title}
    >
      <Icon icon={icon} size={11} className="text-muted-foreground shrink-0" />
      <span className="truncate font-mono text-[11px] font-medium">{children}</span>
    </span>
  );
}

/** "Workload · redis:8.10 on app-net in us-central-1", under the workload's name. */
function WorkloadSummary({ workload }: { workload: Workload }) {
  const image = workload.image?.slice(workload.image.lastIndexOf("/") + 1);
  const [location, ...more] = workload.locations;
  return (
    <span className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-1.5 text-sm">
      Workload
      {image ? (
        <>
          <span aria-hidden>·</span>
          <SummaryChip icon={PackageIcon} title={workload.image}>
            {image}
          </SummaryChip>
        </>
      ) : null}
      {workload.networks[0] ? (
        <>
          on
          <SummaryChip icon={NetworkIcon}>{workload.networks[0]}</SummaryChip>
        </>
      ) : null}
      {location ? (
        <>
          in
          <SummaryChip icon={MapPinIcon} title={workload.locations.join(", ")}>
            {more.length ? `${location} +${more.length}` : location}
          </SummaryChip>
        </>
      ) : null}
    </span>
  );
}

function WorkloadLayoutShell({
  projectHref,
  workloadsHref,
  overviewHref,
  metricsHref,
  logsHref,
  titleName,
}: {
  projectHref: string;
  workloadsHref: string;
  overviewHref: string;
  metricsHref: string;
  logsHref: string;
  titleName: string;
}) {
  const { pathname } = useLocation();
  const { projectId, workloadName } = useParams<{
    projectId: string;
    workloadName: string;
  }>();
  const {
    data: workload,
    isLoading,
    error,
    refetch,
  } = useWorkload(projectId, workloadName);
  const { data: instances = [] } = useWorkloadInstances(
    projectId,
    workloadName,
    workload?.health === "Deploying",
  );
  const published = usePublishedUrl(projectId, workloadName);
  const locationIndex = useLocationIndex(projectId);
  const {
    identityLabel,
    isLoading: identityLoading,
    isDenied: identityDenied,
  } = useProjectResourceIdentity(projectId);
  const metricKeys = useMemo(() => identityValues(instances), [instances]);
  const instanceNames = useMemo(
    () => instances.map((instance) => instance.name),
    [instances],
  );
  const proxyId = published.data?.proxyName;
  const headerAlb = published.data?.proxies[0];
  const headerUrl =
    workload && headerAlb?.hostname
      ? workloadUrlState(
          !workload.deleting && workload.health === "Available",
          albStatus(headerAlb, { workloadServing: workload.health === "Available" }),
        )
      : undefined;
  const navigate = useNavigate();
  const permissions = useDeletePermissions(projectId);
  const deleteDialog = useDeleteWorkloadDialog();
  // Hidden (not disabled) without permission, per the portal's RBAC conventions.
  const canDelete =
    !isLoading && !error && !!workload && !workload.deleting &&
    !permissions.isLoading && permissions.canDeleteWorkload;

  return (
    <WorkloadPageChrome
      projectHref={projectHref}
      workloadsHref={workloadsHref}
      overviewHref={overviewHref}
      metricsHref={metricsHref}
      logsHref={logsHref}
      titleName={workload?.name ?? titleName}
      summary={workload ? <WorkloadSummary workload={workload} /> : undefined}
      // Held until the workload is known too, since that decides whether it links.
      urlLoading={published.isLoading || (isLoading && !!headerAlb?.hostname)}
      deleteLoading={(isLoading || permissions.isLoading) && !workload?.deleting}
      visit={
        headerAlb?.hostname && headerUrl
          ? { href: `https://${headerAlb.hostname}`, live: headerUrl.live, status: headerUrl.status }
          : undefined
      }
      url={
        headerAlb?.hostname && headerUrl ? (
          <WorkloadUrl
            hostname={headerAlb.hostname}
            customHostnames={headerAlb.customHostnames}
            live={headerUrl.live}
            status={headerUrl.status}
            plain
          />
        ) : undefined
      }
      onDelete={canDelete && workload ? () => deleteDialog.show(workload) : undefined}>
      <DeleteWorkloadDialog
        projectId={projectId}
        workload={deleteDialog.workload}
        open={deleteDialog.open}
        permissions={permissions}
        onClose={deleteDialog.close}
        onDeleted={() => navigate(workloadsHref)}
      />

      {isLoading &&
        (pathname === logsHref || pathname.startsWith(`${logsHref}/`) ? (
          <InstanceLogsSkeleton />
        ) : pathname === metricsHref || pathname.startsWith(`${metricsHref}/`) ? (
          <WorkloadMetricsSkeleton />
        ) : pathname.endsWith("/topology") ? (
          <WorkloadTopologySkeleton />
        ) : (
          <WorkloadOverviewSkeleton />
        ))}

      {!isLoading && (error || !workload) && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view this workload."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && workload && (
        <Outlet
          context={
            {
              workload,
              instances,
              projectId,
              proxyId,
              published: published.data,
              publishedLoading: published.isLoading,
              locationIndex,
              identityLabel,
              identityLoading,
              identityDenied,
              metricKeys,
              instanceNames,
              overviewHref,
              metricsHref,
            } satisfies WorkloadOutletContext
          }
        />
      )}
    </WorkloadPageChrome>
  );
}

/**
 * What the Overview and Topology tabs both need to say about the workload's
 * state: its health as the instances report it, whether it is still coming
 * up, and how each attached load balancer is doing.
 */
function useWorkloadState() {
  const outlet = useWorkloadOutlet();
  const { workload, instances, published } = outlet;
  const healthyCount = instances.length
    ? instances.filter((i) => i.status === "Available" && !instanceNotReady(i)).length
    : workload.readyReplicas;
  const totalCount = instances.length || workload.desiredReplicas;

  // A crash loop or bad image still reads as provisioning at the workload
  // level; only the instances say otherwise.
  const instanceFailure =
    workload.health !== "Available" && !workload.deleting
      ? instanceFailureSummary(instances)
      : undefined;
  const health = instanceFailure ? "Unavailable" : workload.health;
  // Otherwise an unavailable workload explains itself through its condition.
  const failure =
    instanceFailure ??
    (health === "Unavailable"
      ? workload.conditions.find((c) => c.type === "Available")?.message
      : undefined);
  const albs = published?.proxies;
  const now = useNow(
    health === "Deploying" || (!!albs && anyAlbProvisioning(albs)),
  );
  const deploy = instanceFailure ? undefined : deployStatus(workload, now);
  const albStatuses = useMemo(
    () =>
      Object.fromEntries(
        (albs ?? []).map((alb) => [
          alb.proxyName,
          albStatus(alb, { workloadServing: workload.health === "Available", now }),
        ]),
      ),
    [albs, workload.health, now],
  );
  const status: StatusDisplay = deploy
    ? { tone: deploy.tone, label: deploy.label }
    : { tone: workloadHealthToBadgeType(health), label: health };
  const instanceHref = useCallback(
    (name: string) => `${outlet.overviewHref}/instances/${name}`,
    [outlet.overviewHref],
  );

  return {
    ...outlet,
    healthyCount,
    totalCount,
    health,
    failure,
    deploy,
    now,
    albStatuses,
    status,
    instanceHref,
  };
}

/**
 * The Overview: the workload's state, then its running instances, which is
 * what someone opening a workload usually came to check. Its topology has a
 * tab of its own.
 */
function WorkloadOverview() {
  const state = useWorkloadState();
  const {
    workload,
    instances,
    projectId,
    proxyId,
    published,
    publishedLoading,
    locationIndex,
    instanceNames,
    overviewHref,
    albStatuses,
    instanceHref,
  } = state;
  const [rangeValue, setRangeValue] = useState<OverviewRangeValue>(
    DEFAULT_OVERVIEW_RANGE,
  );
  const range = useOverviewRange(rangeValue);
  const primaryAlb = published?.proxies[0];
  const isPublished = !!published?.proxies.length;

  const details: DetailItem[] = [
    { label: "Runtime", value: workload.runtimeClass ?? workload.runtimeType, mono: !!workload.runtimeClass },
    { label: "Image", value: workload.image && <span title={workload.image}>{workload.image}</span>, mono: true },
    {
      label: "Ports",
      value: workload.namedPorts.length
        ? `${workload.namedPorts.map(portText).join(", ")} · ${isPublished ? "public" : "private"}`
        : undefined,
    },
    {
      label: "Accepts",
      value: workload.ingress.length ? workload.ingress.map(ingressText).join("; ") : undefined,
    },
    { label: "Network", value: workload.networks.join(", "), mono: true },
    { label: "Instance type", value: workload.resources },
    {
      label: "Replicas",
      value:
        scaleText(workload.scale) ??
        (workload.replicasPerRegion !== undefined
          ? `${workload.replicasPerRegion} per location`
          : `${workload.desiredReplicas} total`),
    },
    {
      label: "Locations",
      value: workload.locations.length > 0 && (
        <span title={formatLocationNames(workload.locations, locationIndex)}>
          {workload.locations.join(", ")}
        </span>
      ),
      mono: true,
    },
    ...(isPublished || publishedLoading
      ? [
          {
            label: "Load balancer",
            value: (
              <LoadBalancerValue
                projectId={projectId}
                published={published}
                isLoading={publishedLoading}
                albStatuses={albStatuses}
              />
            ),
          },
        ]
      : []),
    {
      label: "Created",
      value: workload.createdAt.toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }),
    },
  ];

  return (
    <div className="flex flex-col gap-10">
      <WorkloadHealthStrip
        health={state.health}
        deploy={state.deploy}
        failure={state.failure}
        alb={primaryAlb ? albStatuses[primaryAlb.proxyName] : undefined}
        now={state.now}
        healthyCount={state.healthyCount}
        totalCount={state.totalCount}
        locationCount={workload.locations.length}
        albHref={
          projectId && proxyId ? albOverviewHref(projectId, proxyId) : undefined
        }
        albLabel={published?.displayName || published?.proxyName}
        reach={
          publishedLoading || isPublished ? undefined : (
            <PrivateReachLine reach={privateReach(workload, instances)} />
          )
        }
      />

      <InstancesSection
        instances={instances}
        projectId={projectId}
        locationIndex={locationIndex}
        deploying={!!state.deploy}
        instanceHref={instanceHref}
        now={state.now}
      />

      <RecentInstanceLogs
        bare
        logsHref={`${overviewHref}/logs`}
        projectId={projectId}
        proxyId={proxyId}
        albHostname={published?.hostname}
        instanceNames={instanceNames}
      />

      {/* Traffic only means something once a load balancer serves the workload. */}
      {proxyId ? (
        <LiveTraffic
          projectId={projectId}
          proxyId={proxyId}
          range={range}
          onRangeChange={setRangeValue}
        />
      ) : null}

      <DetailsSection items={details} />
    </div>
  );
}

/** The workload's topology on its own tab: load balancers, then instances. */
function WorkloadTopology() {
  const state = useWorkloadState();
  const { projectId, workload, instances, published, locationIndex, overviewHref } = state;
  const instanceMetricsHref = useCallback(
    (name: string) => `${overviewHref}/instances/${name}/metrics`,
    [overviewHref],
  );
  const albHrefFor = useMemo(
    () =>
      projectId
        ? (proxyName: string) => albOverviewHref(projectId, proxyName)
        : undefined,
    [projectId],
  );
  const albMetricsHrefFor = useMemo(
    () =>
      projectId
        ? (proxyName: string) => albMetricsHref(projectId, proxyName)
        : undefined,
    [projectId],
  );
  return (
    <TopologyCard
      projectId={projectId}
      workload={workload}
      status={state.status}
      albStatuses={state.albStatuses}
      instances={instances}
      albs={published?.proxies ?? []}
      locationIndex={locationIndex}
      instanceHref={state.instanceHref}
      instanceMetricsHref={instanceMetricsHref}
      albHref={albHrefFor}
      albMetricsHref={albMetricsHrefFor}
    />
  );
}

export default function WorkloadDetail() {
  const { projectId, workloadName } = useParams<{
    projectId: string;
    workloadName: string;
  }>();
  const location = useLocation();

  const path = location.pathname.replace(/\/$/, "");
  const overviewHref = path.replace(/\/(logs|metrics|activity|topology)$/, "");
  const logsHref = `${overviewHref}/logs`;
  const metricsHref = `${overviewHref}/metrics`;
  const workloadsHref = overviewHref.replace(/\/[^/]+$/, "");
  const projectHref = projectId ? `/project/${projectId}` : "/";
  const titleName = workloadName ?? "Workload";

  return (
    <Routes>
      <Route
        element={
          <WorkloadLayoutShell
            projectHref={projectHref}
            workloadsHref={workloadsHref}
            overviewHref={overviewHref}
            metricsHref={metricsHref}
            logsHref={logsHref}
            titleName={titleName}
          />
        }
      >
        <Route index element={<WorkloadOverview />} />
        <Route path="topology" element={<WorkloadTopology />} />
        <Route path="metrics" element={<WorkloadMetrics />} />
        <Route path="logs" element={<WorkloadLogs />} />
        <Route
          path="activity"
          element={
            <Suspense fallback={null}>
              <WorkloadActivity />
            </Suspense>
          }
        />
      </Route>
    </Routes>
  );
}
