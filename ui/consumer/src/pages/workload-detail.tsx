/**
 * `portal.page/project` extension at `:workloadName/*`, exposed as
 * `WorkloadDetail`. Overview is the index; Topology lives at `topology`,
 * Metrics at `metrics`, Logs at `logs`, Activity at `activity`.
 *
 * Overview layout, in the project home page's style: a one-line status,
 * the instances and how to reach them, recent logs, then details.
 *
 * Breadcrumbs are left to the host `ContentWrapper` — do not re-render them
 * inside the plugin (that double-stacks chrome vs native pages).
 */
import { ResourceActivity } from "../components/resource-activity";
import { WorkloadOverviewCard } from "../components/workload-overview-card";
import { WorkloadPageChrome } from "../components/workload-page-chrome";
import { DeleteWorkloadDialog, useDeleteWorkloadDialog } from "../components/delete-workload-dialog";
import {
  InstanceLogsSkeleton,
  WorkloadMetricsSkeleton,
  WorkloadOverviewSkeleton,
  WorkloadTopologySkeleton,
} from "../components/skeletons";
import { ErrorOrRestrictedState } from "../components/states";
import { SectionFrame } from "../components/surface";
import { WorkloadInstances } from "../components/workload-instances";
import { WorkloadLoadBalancers } from "../components/workload-load-balancers";
import {
  useDeletePermissions,
  usePublishedUrl,
  useWorkload,
  useWorkloadDeployments,
  useWorkloadInstances,
} from "../lib/api";
import { splitSlashValue } from "../lib/format";
import { useWorkloadOverviewState } from "../lib/use-workload-overview-state";
import {
  compactDetailRows,
  firstCrash,
  instancesStarting,
  overviewStatusLine,
} from "../lib/workload-presenters";
import { deployHeadline, deployState, deployTimeline } from "../lib/deploy-presenters";
import { formatLocationName, formatLocationNames, useLocationIndex } from "../lib/locations";
import { computeResourceLinkResolver, workloadActivityFilter } from "../lib/activity";
import { workloadSettling } from "../lib/workload-settling";
import { allocationOf, formatBytes, parseCpuCores, parseMemoryBytes } from "../lib/resource-usage";
import { Card, CardHeader, CardTitle } from "@datum-cloud/datum-ui/card";
import { identityValues, useProjectResourceIdentity } from "../lib/metrics-queries";
import type { WorkloadOutletContext } from "./workload-outlet-context";
import { useWorkloadOutlet } from "./workload-outlet-context";
import WorkloadLogs from "./workload-logs";
import WorkloadMetrics from "./workload-metrics";
import type { Instance, Workload } from "../schema";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { SettingsIcon } from "lucide-react";
import { lazy, Suspense, useMemo, type ReactNode } from "react";
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
// The topology canvas is the plugin's largest component; load it with its tab.
const WorkloadTopology = lazy(() => import("./workload-topology"));

const DETAIL_DATE_FORMAT: Intl.DateTimeFormatOptions = {
  day: "2-digit",
  month: "short",
  year: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
};

interface DetailRow {
  label: string;
  /** Raw value; rows without one are dropped (`compactDetailRows`). */
  value: unknown;
  content: ReactNode;
}

interface WorkloadDetailsProps {
  workload: Workload;
  instances: readonly Instance[];
  locationLabel: string;
}

function mono(text: string) {
  return (
    <span className="block max-w-full truncate font-mono text-xs" title={text}>
      {text}
    </span>
  );
}

/** "2 vCPU · 4 GB" from the first instance's allocation, when it is known. */
function instanceSize(instances: readonly Instance[]): string | undefined {
  const first = instances[0];
  const cores = parseCpuCores(first?.cpu);
  const memory = parseMemoryBytes(first?.memory);
  const parts = [
    cores !== undefined ? `${cores} vCPU` : undefined,
    memory !== undefined ? formatBytes(memory) : undefined,
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

/** Configuration rows, in display order, before empty ones are dropped. */
function workloadDetailRows({
  workload,
  instances,
  locationLabel,
}: WorkloadDetailsProps): DetailRow[] {
  const { main: resourceShort } = splitSlashValue(workload.resources ?? "");
  const size = instanceSize(instances);
  const type = resourceShort || workload.resources;
  return [
    { label: "Image", value: workload.image, content: workload.image ? mono(workload.image) : null },
    {
      label: "Instance type",
      value: type ?? size,
      content: [type, size].filter(Boolean).join(" · "),
    },
    {
      label: "Replicas",
      value: workload.desiredReplicas,
      content:
        workload.replicasPerRegion !== undefined
          ? `${workload.replicasPerRegion} per location · ${workload.desiredReplicas} total`
          : `${workload.desiredReplicas} total`,
    },
    {
      label: "Locations",
      value: workload.locations,
      content: <span title={workload.locations.join(", ")}>{locationLabel}</span>,
    },
    { label: "Ports", value: workload.ports, content: mono(workload.ports.join(", ")) },
    { label: "Networks", value: workload.networks, content: mono(workload.networks.join(", ")) },
    { label: "Runtime", value: workload.runtimeType, content: workload.runtimeType },
    {
      label: "Created",
      value: workload.createdAt,
      content: workload.createdAt.toLocaleDateString("en-GB", DETAIL_DATE_FORMAT),
    },
  ];
}

// Inline: the host compiles no fixed label width.
const DETAIL_LABEL_STYLE = { width: "8rem" } as const;

/**
 * What the workload is, in its own card. Rows with no value are left out
 * rather than shown as a dash; live state (instances, health) stays in the
 * cards above.
 */
function WorkloadDetails(props: WorkloadDetailsProps) {
  const rows = compactDetailRows(workloadDetailRows(props));

  return (
    <Card sectioned size="sm" className="overflow-hidden" data-testid="compute-plugin-workload-details">
      <CardHeader size="sm" bordered>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Icon icon={SettingsIcon} size={16} className="text-secondary" aria-hidden />
          Configuration
        </CardTitle>
      </CardHeader>
      <dl className="flex flex-col">
        {rows.map((row, index) => (
          <div
            key={row.label}
            className={index === 0 ? "flex gap-4 px-4 py-2.5" : "flex gap-4 border-t px-4 py-2.5"}>
            <dt className="text-muted-foreground shrink-0 text-sm" style={DETAIL_LABEL_STYLE}>
              {row.label}
            </dt>
            <dd className="min-w-0 flex-1 text-sm">{row.content}</dd>
          </div>
        ))}
      </dl>
    </Card>
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
  const onTab = (href: string) => pathname === href || pathname.startsWith(`${href}/`);
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
  const {
    data: instances = [],
    isLoading: instancesLoading,
    error: instancesError,
  } = useWorkloadInstances(
    projectId,
    workloadName,
    // Fast while instances are still coming up, not only while deploying.
    workload ? workloadSettling(workload) : false,
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
        (onTab(logsHref) ? (
          <InstanceLogsSkeleton />
        ) : onTab(metricsHref) ? (
          <WorkloadMetricsSkeleton />
        ) : onTab(`${overviewHref}/topology`) ? (
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
              instancesLoading,
              instancesError,
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
 * Overview: a summary card (status, crash or deploy progress, the
 * last hour's stats), the instances table, the load balancers that reach it,
 * then what changed recently beside the workload's configuration.
 */
/** When an instance's failure was reported, for the end of the "so far" line. */
function failedAt(instance: Instance | undefined): Date | undefined {
  const condition = instance?.conditions.find(
    (c) => (c.type === "Ready" || c.type === "Programmed") && c.status === "False" && c.lastTransitionTime,
  );
  return condition?.lastTransitionTime ? new Date(condition.lastTransitionTime) : undefined;
}

export function WorkloadOverview() {
  const context = useWorkloadOutlet();
  const {
    workload,
    instances,
    instancesLoading,
    instancesError,
    projectId,
    proxyId,
    published,
    publishedLoading,
    locationIndex,
    identityLabel,
    identityLoading,
    identityDenied,
    metricKeys,
    overviewHref,
  } = context;
  const { health, failure, deploy, albStatuses, now, healthyCount, totalCount, instanceHref } =
    useWorkloadOverviewState(context);

  const primaryAlb = published?.proxies[0];
  const primaryAlbStatus = primaryAlb ? albStatuses[primaryAlb.proxyName] : undefined;
  // While a deploy is under way, stuck, failed or just finished, the card
  // says so instead of showing (empty) stats.
  const coming = !!deploy || (!workload.deleting && instancesStarting(instances));
  const { data: deployments = [] } = useWorkloadDeployments(projectId, workload.uid, coming);
  const state = deployState({ workload, instances, deploy, deployments, healthyCount, now });
  const status = state
    ? deployHeadline(state, { healthy: healthyCount, total: totalCount, locations: workload.locations.length }, now)
    : overviewStatusLine({
        health,
        deleting: workload.deleting,
        deploy,
        failure,
        albError: primaryAlbStatus?.phase === "error" ? primaryAlbStatus.message : undefined,
        healthyCount,
        totalCount,
        locationCount: workload.locations.length,
        now,
      });
  const timeline = state
    ? deployTimeline(
        workload,
        instances,
        state.phase === "failed" && state.problem
          ? { label: state.problem.title, at: failedAt(state.failedInstance) ?? new Date(now), state: "failed" }
          : state.phase === "stalled" && state.stuckOn
            ? { label: state.stuckOn, at: state.since, state: "waiting" }
            : undefined,
      )
    : undefined;
  const problemActions =
    state?.phase === "failed" && state.failedInstance
      ? [{ label: "View instance", href: instanceHref(state.failedInstance.name), primary: true }]
      : state?.phase === "stalled" && state.problem?.title === "Waiting for quota" && projectId
        ? [{ label: "View usage", href: `/project/${projectId}/usage` }]
        : [];
  const crash = useMemo(() => firstCrash(instances), [instances]);
  const allocation = useMemo(() => allocationOf(instances), [instances]);
  const resolver = useMemo(
    () => computeResourceLinkResolver(workload.name, overviewHref),
    [workload.name, overviewHref],
  );

  return (
    <>
      <WorkloadOverviewCard
        status={status}
        phase={state?.phase}
        timeline={timeline}
        problem={state?.problem}
        problemActions={problemActions}
        image={workload.image}
        updatedAt={workload.updatedAt ?? workload.createdAt}
        now={now}
        crash={
          crash
            ? {
                ...crash,
                locationLabel: crash.instance.location
                  ? formatLocationName(crash.instance.location, locationIndex)
                  : "an unknown location",
                instanceHref: instanceHref(crash.instance.name),
                logsHref: `${instanceHref(crash.instance.name)}/logs`,
              }
            : undefined
        }
        projectId={projectId}
        instances={instances}
        totalCount={totalCount}
        identityLabel={identityLabel}
        metricKeys={metricKeys}
        metricsDenied={identityDenied}
        proxyId={proxyId}
        allocation={allocation}
        instancesLoading={instancesLoading}
        identityLoading={identityLoading}
        publishedLoading={publishedLoading}
      />

      <WorkloadInstances
        workloadName={workload.name}
        instances={instances}
        loading={instancesLoading}
        error={instancesError}
        locationIndex={locationIndex}
        instanceHref={instanceHref}
        deploying={!!deploy}
        deleting={workload.deleting}
        projectId={projectId}
        metricsDenied={identityDenied}
      />

      <WorkloadLoadBalancers
        projectId={projectId}
        published={published}
        loading={publishedLoading}
        albStatuses={albStatuses}
        network={workload.networks[0]}
        workload={workload}
      />

      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <SectionFrame
          title="What changed"
          action={
            <Link to={`${overviewHref}/activity`} className="text-primary text-sm hover:underline">
              All activity
            </Link>
          }
          testId="compute-plugin-workload-recent-activity">
          <ResourceActivity
            projectId={projectId}
            filter={workloadActivityFilter(workload.name)}
            liveNameMatch={workload.name}
            resourceLinkResolver={resolver}
            bare
            pageSize={6}
          />
        </SectionFrame>
        <WorkloadDetails
          workload={workload}
          instances={instances}
          locationLabel={formatLocationNames(workload.locations, locationIndex)}
        />
      </div>
    </>
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
        <Route
          path="topology"
          element={
            <Suspense fallback={<WorkloadTopologySkeleton />}>
              <WorkloadTopology />
            </Suspense>
          }
        />
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
