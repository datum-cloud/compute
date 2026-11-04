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
import {
  DetailList,
  DETAILS_GRID_CLASS,
  OPEN_ROW_PADDING,
  StatusBadge,
} from "../components/detail-list";
import { RecentInstanceLogs } from "../components/instance-logs";
import { WorkloadPageChrome } from "../components/workload-page-chrome";
import { DeleteWorkloadDialog, useDeleteWorkloadDialog } from "../components/delete-workload-dialog";
import {
  InstanceLogsSkeleton,
  WorkloadMetricsSkeleton,
  WorkloadOverviewSkeleton,
  WorkloadTopologySkeleton,
} from "../components/skeletons";
import { ErrorOrRestrictedState } from "../components/states";
import { SectionFrame, StatusLine } from "../components/surface";
import { CopyValue } from "../components/copy-value";
import { WorkloadInstances } from "../components/workload-instances";
import {
  useDeletePermissions,
  usePublishedUrl,
  useWorkload,
  useWorkloadInstances,
  type PublishedUrl,
} from "../lib/api";
import { splitSlashValue } from "../lib/format";
import {
  albStatus,
  albStatusDisplay,
  workloadUrlState,
  type AlbStatus,
} from "../lib/alb-status";
import { WorkloadUrl } from "../components/workload-url";
import { albOverviewHref, useWorkloadOverviewState } from "../lib/use-workload-overview-state";
import {
  compactDetailRows,
  imageShortName,
  overviewStatusLine,
  privateReach,
  type PrivateReach,
} from "../lib/workload-presenters";
import { formatLocationNames, useLocationIndex } from "../lib/locations";
import { identityValues, useProjectResourceIdentity } from "../lib/metrics-queries";
import type { WorkloadOutletContext } from "./workload-outlet-context";
import { useWorkloadOutlet } from "./workload-outlet-context";
import WorkloadLogs from "./workload-logs";
import WorkloadMetrics from "./workload-metrics";
import type { Workload } from "../schema";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { GlobeIcon, LockIcon, MapPinIcon, NetworkIcon, PackageIcon } from "lucide-react";
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
  albStatuses: Record<string, AlbStatus>;
  healthyCount: number;
  totalCount: number;
  projectId?: string;
  published?: PublishedUrl | null;
  publishedLoading: boolean;
  locationLabel: string;
}

function mono(text: string) {
  return (
    <span className="block max-w-full truncate font-mono text-xs" title={text}>
      {text}
    </span>
  );
}

/** General and Configuration rows, in display order, before empty ones are dropped. */
function workloadDetailRows({
  workload,
  albStatuses,
  healthyCount,
  totalCount,
  projectId,
  published,
  publishedLoading,
  locationLabel,
}: WorkloadDetailsProps): DetailRow[] {
  const { main: resourceShort } = splitSlashValue(workload.resources ?? "");
  return [
    { label: "Resource name", value: workload.name, content: mono(workload.name) },
    {
      label: "Load balancer",
      // Held while loading so the row doesn't pop in after the rest.
      value: publishedLoading ? "loading" : published?.proxies,
      content: (
        <LoadBalancerValue
          projectId={projectId}
          published={published}
          isLoading={publishedLoading}
          albStatuses={albStatuses}
        />
      ),
    },
    { label: "Instances", value: totalCount, content: `${healthyCount}/${totalCount}` },
    {
      label: "Created",
      value: workload.createdAt,
      content: workload.createdAt.toLocaleDateString("en-GB", DETAIL_DATE_FORMAT),
    },
    { label: "Runtime", value: workload.runtimeType, content: workload.runtimeType },
    { label: "Image", value: workload.image, content: workload.image ? mono(workload.image) : null },
    {
      label: "Resources",
      value: resourceShort || workload.resources,
      content: resourceShort || workload.resources,
    },
    {
      label: "Replicas",
      value: workload.desiredReplicas,
      content:
        workload.replicasPerRegion !== undefined
          ? `${workload.replicasPerRegion}/location · ${workload.desiredReplicas} total`
          : `${workload.desiredReplicas} total`,
    },
    {
      label: "Locations",
      value: workload.locations,
      content: <span title={workload.locations.join(", ")}>{locationLabel}</span>,
    },
    { label: "Networks", value: workload.networks, content: mono(workload.networks.join(", ")) },
    { label: "Ports", value: workload.ports, content: mono(workload.ports.join(", ")) },
  ];
}

/**
 * General and Configuration merged into one two-column list. Rows with no
 * value are left out rather than shown as "Coming soon" or a dash, and the
 * grid fills row by row so dropped rows leave no holes.
 */
function WorkloadDetails(props: WorkloadDetailsProps) {
  const rows = compactDetailRows(workloadDetailRows(props));

  return (
    <SectionFrame title="Details" testId="compute-plugin-workload-details">
      <div style={OPEN_ROW_PADDING}>
        <DetailList
          className={DETAILS_GRID_CLASS}
          itemClassName="py-2"
          labelClassName="text-muted-foreground font-normal"
          items={rows.map((row) => ({ label: row.label, content: row.content }))}
        />
      </div>
    </SectionFrame>
  );
}

/** Inline: the host compiles no `text-[11px]`. */
const CHIP_TEXT_STYLE = { fontSize: "11px" } as const;

/** A small inline value: an icon and a mono label. */
function SummaryChip({
  icon,
  children,
  title,
}: {
  icon: typeof GlobeIcon;
  children: ReactNode;
  title?: string;
}) {
  return (
    <span
      className="bg-muted inline-flex min-w-0 items-center gap-1 rounded px-1.5 py-0.5"
      title={title}
    >
      <Icon icon={icon} size={11} className="text-muted-foreground shrink-0" aria-hidden />
      <span className="truncate font-mono font-medium" style={CHIP_TEXT_STYLE}>
        {children}
      </span>
    </span>
  );
}

/** "Workload · redis:8.10 on app-net in us-central-1", under the workload's name. */
function WorkloadSummary({ workload }: { workload: Workload }) {
  const image = imageShortName(workload.image);
  const [network] = workload.networks;
  const [location, ...more] = workload.locations;
  return (
    <span
      className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-1.5 text-sm"
      data-testid="compute-plugin-workload-summary"
    >
      Workload
      {image ? (
        <>
          <span aria-hidden>·</span>
          <SummaryChip icon={PackageIcon} title={workload.image}>
            {image}
          </SummaryChip>
        </>
      ) : null}
      {network ? (
        <>
          on
          <SummaryChip icon={NetworkIcon} title={workload.networks.join(", ")}>
            {workload.networks.length > 1 ? `${network} +${workload.networks.length - 1}` : network}
          </SummaryChip>
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

function WorkloadOverview() {
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
    instanceNames,
    overviewHref,
  } = context;
  const {
    health,
    failure,
    deploy,
    albStatuses,
    now,
    healthyCount,
    totalCount,
    instanceHref,
  } = useWorkloadOverviewState(context);

  const primaryAlb = published?.proxies[0];
  const primaryAlbStatus = primaryAlb ? albStatuses[primaryAlb.proxyName] : undefined;
  const logsHref = `${overviewHref}/logs`;
  const status = overviewStatusLine({
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

  return (
    <>
      <div data-testid="compute-plugin-workload-health">
        <StatusLine
          tone={status.tone}
          title={status.title}
          detail={<span title={status.detail}>{status.detail}</span>}
          action={
            primaryAlb?.hostname ? (
              <AlbHostnameLink
                hostname={primaryAlb.hostname}
                href={projectId ? albOverviewHref(projectId, primaryAlb.proxyName) : undefined}
                status={primaryAlbStatus}
              />
            ) : !publishedLoading && !published?.proxies.length ? (
              <PrivateReachLine reach={privateReach(workload, instances)} />
            ) : undefined
          }
        />
      </div>

      <WorkloadInstances
        instances={instances}
        loading={instancesLoading}
        error={instancesError}
        projectId={projectId}
        locationIndex={locationIndex}
        instanceHref={instanceHref}
        deploying={!!deploy}
        deleting={workload.deleting}
        now={now}
      />

      <RecentInstanceLogs
        bare
        logsHref={logsHref}
        projectId={projectId}
        proxyId={proxyId}
        albHostname={published?.hostname}
        instanceNames={instanceNames}
        // Without a load balancer the query waits on instance names; until
        // they arrive it would otherwise read as "No logs".
        waiting={instancesLoading && !proxyId}
      />

      <WorkloadDetails
        workload={workload}
        albStatuses={albStatuses}
        healthyCount={healthyCount}
        totalCount={totalCount}
        projectId={projectId}
        published={published}
        publishedLoading={publishedLoading}
        locationLabel={formatLocationNames(workload.locations, locationIndex)}
      />
    </>
  );
}

/**
 * The status line's action slot doesn't shrink, so the hostname needs its
 * own cap to truncate instead of pushing the detail out on narrow screens.
 */
const ALB_HOSTNAME_STYLE = { maxWidth: 'min(20rem, 45vw)' } as const;

/** The load balancer's hostname on the status line, with its state while it isn't ready. */
function AlbHostnameLink({
  hostname,
  href,
  status,
}: {
  hostname: string;
  href?: string;
  status?: AlbStatus;
}) {
  const display = status ? albStatusDisplay(status) : undefined;
  const label = (
    <>
      <Icon icon={GlobeIcon} size={14} className="shrink-0" aria-hidden />
      <span className="truncate font-mono text-xs">{hostname}</span>
    </>
  );
  return (
    <span
      className="flex min-w-0 items-center gap-2"
      style={ALB_HOSTNAME_STYLE}
      data-e2e="compute-workload-alb-chip">
      {display ? (
        <span className="text-muted-foreground hidden text-xs sm:inline" title={display.detail}>
          {display.short}
        </span>
      ) : null}
      {href ? (
        <Link
          to={href}
          className="text-primary flex min-w-0 items-center gap-1.5 hover:underline"
          title="Open load balancer"
        >
          {label}
        </Link>
      ) : (
        <span className="flex min-w-0 items-center gap-1.5">{label}</span>
      )}
    </span>
  );
}

/** Room for a full private name and port, which is longer than a hostname. */
const PRIVATE_REACH_STYLE = { maxWidth: "min(36rem, 60vw)" } as const;

/**
 * How to reach a workload no load balancer publishes, for the status line:
 * its one instance's address, or the network and port when it has several.
 */
function PrivateReachLine({ reach }: { reach: PrivateReach }) {
  return (
    <span
      className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs"
      style={PRIVATE_REACH_STYLE}
      data-testid="compute-plugin-workload-private-reach"
    >
      <Icon icon={LockIcon} size={12} className="shrink-0" aria-hidden />
      <span className="shrink-0">Private</span>
      {reach.kind === "address" ? (
        <CopyValue value={reach.address} className="text-foreground" />
      ) : (
        <span className="truncate">
          {reach.network ? (
            <>
              on <span className="text-foreground font-mono">{reach.network}</span>
            </>
          ) : null}
          {reach.port !== undefined ? (
            <>
              {reach.network ? ", " : ""}port{" "}
              <span className="text-foreground font-mono">{reach.port}</span>
            </>
          ) : null}
        </span>
      )}
    </span>
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
