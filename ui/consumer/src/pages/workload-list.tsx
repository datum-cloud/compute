/**
 * `portal.page/project` extension at the plugin mount root, exposed as `WorkloadList`.
 *
 * Styled like the project home page: a compact stat strip, then the table
 * (or 2-column cards with regions), status as a dot and muted text. Real compute API fields fill operational slots;
 * Requests and CPU / memory totals come from the same Prometheus series (and
 * the same sum across instances) as the workload Metrics tab.
 */
import { CliBanner, SectionCard } from "../components/cli-section";
import { formatKpiValue } from "../components/metric-area-chart";
import { CpuMemorySparks } from "../components/metric-sparkline";
import { QuietNotice } from "../components/surface/quiet-notice";
import { StatStrip } from "../components/stat-strip";
import { TryDemoDialog, TryDemoWorkloadCard, useDemoWorkloadDialog } from "../components/try-demo";
import { WorkloadSearch } from "../components/workload-search";
import { HealthDot, WorkloadStatusDot } from "../components/workload-status-badge";
import { WorkloadUrl } from "../components/workload-url";
import { albStatus, workloadUrlState } from "../lib/alb-status";
import { WorkloadListCardsSkeleton, WorkloadListTableSkeleton } from "../components/skeletons";
import { ErrorOrRestrictedState } from "../components/states";
import {
  useComputeEnablement,
  useInstances,
  usePublishedUrls,
  useWorkloads,
  type PublishedUrl,
} from "../lib/api";
import {
  albRpsQuery,
  albRpsQueryMany,
  identityValues,
  type InstanceIdentityLabel,
  useProjectResourceIdentity,
  workloadCpuSumQuery,
  workloadMemorySumQuery,
} from "../lib/metrics-queries";
import { usePrometheusCard } from "../lib/prometheus";
import { allocationOf, type Allocation } from "../lib/resource-usage";
import { useOverviewRange } from "../components/overview-range";
import { useLocationIndex, type LocationIndex } from "../lib/locations";
import { matchesWorkloadSearch, newestFirst, regionLabel } from "../lib/workload-presenters";
import type { Workload } from "../schema";
import { Button, LinkButton } from "@datum-cloud/datum-ui/button";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@datum-cloud/datum-ui/breadcrumb";
import { Card, CardContent } from "@datum-cloud/datum-ui/card";
import { PageTitle } from "@datum-cloud/datum-ui/page-title";
import { useCopyToClipboard } from "@datum-cloud/datum-ui/hooks";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { cn } from "@datum-cloud/datum-ui/utils";
import { formatDistanceToNowStrict } from "date-fns";
import {
  ArrowRightIcon,
  CheckIcon,
  CopyIcon,
  HomeIcon,
  LayoutGridIcon,
  RocketIcon,
  Rows3Icon,
  SearchIcon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";

// The table bundles datum-ui's DataTable plus @tanstack/react-table and nuqs
// (the host shares none of them), which is most of this route's weight. Load
// it lazily so users who switch to cards don't pay.
const WorkloadTable = lazy(() =>
  import("../components/workload-table").then((m) => ({
    default: m.WorkloadTable,
  })),
);

type WorkloadView = "cards" | "table";
// v2: the default flipped from cards to table. The old key holds "cards" for
// anyone who ever loaded the page (the view is written back on mount), so
// reading it would keep almost everyone on cards.
const VIEW_STORAGE_KEY = "compute-plugin:workload-view:v2";

function readStoredView(): WorkloadView {
  try {
    return window.localStorage.getItem(VIEW_STORAGE_KEY) === "cards" ? "cards" : "table";
  } catch {
    return "table";
  }
}

const VIEW_OPTIONS = [
  { value: "table", label: "Table view", icon: Rows3Icon },
  { value: "cards", label: "Card view", icon: LayoutGridIcon },
] as const;

/**
 * Table / cards switch: a borderless icon pair in the page title actions.
 * Each button names its view and reports whether it is the current one.
 */
function ViewToggle({
  view,
  onChange,
}: {
  view: WorkloadView;
  onChange: (view: WorkloadView) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Workload view"
      className="flex items-center gap-0.5"
      data-testid="compute-plugin-workload-view-toggle"
    >
      {VIEW_OPTIONS.map((option) => {
        const active = view === option.value;
        return (
          <Button
            key={option.value}
            htmlType="button"
            type="quaternary"
            theme="borderless"
            size="icon"
            aria-label={option.label}
            aria-pressed={active}
            title={option.label}
            onClick={() => onChange(option.value)}
            className={cn(active ? "bg-accent text-foreground" : "text-muted-foreground")}
          >
            <Icon icon={option.icon} size={16} aria-hidden />
          </Button>
        );
      })}
    </div>
  );
}

/**
 * The fleet at a glance, one compact row like the home page's: counts from
 * the loaded list, and requests per second across every connected load
 * balancer. A value that is loading, errored or not applicable shows "—".
 */
function FleetSummary({
  workloads,
  requests,
}: {
  workloads: Workload[];
  /** Undefined while loading, on error, or with no load balancer attached. */
  requests?: string;
}) {
  const readyInstances = workloads.reduce((sum, w) => sum + w.readyReplicas, 0);
  const desiredInstances = workloads.reduce(
    (sum, w) => sum + w.desiredReplicas,
    0,
  );
  const healthy = workloads.filter((w) => w.health === "Available").length;

  return (
    <StatStrip
      testId="compute-plugin-fleet-summary"
      items={[
        { label: "Workloads", value: String(workloads.length) },
        {
          label: "Instances",
          value:
            desiredInstances > 0
              ? `${readyInstances} / ${desiredInstances}`
              : String(readyInstances),
        },
        { label: "Healthy", value: String(healthy) },
        { label: "Requests", value: requests },
      ]}
    />
  );
}

function MetricCell({
  label,
  value,
  placeholder,
}: {
  label: string;
  value?: string;
  placeholder?: boolean;
}) {
  return (
    <div>
      <p className="text-muted-foreground text-xs font-medium">{label}</p>
      <p
        className={cn(
          "mt-0.5 text-sm font-semibold tabular-nums",
          placeholder && "text-muted-foreground font-normal",
        )}
      >
        {value ?? "—"}
      </p>
    </div>
  );
}

/** The datumctl banner plus the "Deploy a workload" / "List & inspect workloads"
 * cards. datumctl enables Compute on first use, so these commands work even
 * when the page couldn't enable it. Render inside a `flex flex-col gap-6`. */
function WorkloadCliSections({ projectId }: { projectId: string | undefined }) {
  return (
    <>
      <CliBanner
        title="Deploy workloads with datumctl"
        description="Workloads are created and managed using the Datum CLI. Install datumctl, write a manifest, and deploy — workloads you create will appear here automatically."
      />
      <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
        <SectionCard
          icon={<Icon icon={RocketIcon} size={16} />}
          title="Deploy a workload"
          description="Create a workload manifest and deploy it to your project. The dashboard will reflect the new workload within seconds."
          commands={[
            "datumctl compute deploy -f workload.yaml",
            `datumctl compute deploy --project=${projectId ?? ""} -f workload.yaml`,
          ]}
        />
        <SectionCard
          icon={<Icon icon={SearchIcon} size={16} />}
          title="List & inspect workloads"
          description="Confirm your workload deployed successfully and inspect its current health and placement status."
          commands={[
            "datumctl compute workloads",
            "datumctl compute workloads describe <name>",
          ]}
        />
      </div>
    </>
  );
}

function WorkloadCard({
  workload,
  projectId,
  instanceKeys,
  allocation,
  published,
  identityLabel,
  identityLoading,
  identityDenied,
  timeRange,
  locationIndex,
  href,
}: {
  workload: Workload;
  projectId?: string;
  instanceKeys: string[];
  /** Allocated vCPU / memory, so the sparks read as a percentage. */
  allocation?: Allocation;
  /** The workload's connected load balancers, when it has any. */
  published?: PublishedUrl;
  identityLabel?: InstanceIdentityLabel;
  identityLoading?: boolean;
  identityDenied?: boolean;
  timeRange: ReturnType<typeof useOverviewRange>["timeRange"];
  locationIndex: LocationIndex;
  href: string;
}) {
  const updatedAt = workload.updatedAt ?? workload.createdAt;
  const tags =
    workload.tags.length > 0
      ? workload.tags
      : workload.runtimeType
        ? [workload.runtimeType]
        : [];
  const proxyId = published?.proxyName;
  const primaryAlb = published?.proxies[0];
  const serving = !workload.deleting && (workload.health === "Available" || workload.health === "Degraded");
  const url = workloadUrlState(
    serving,
    primaryAlb ? albStatus(primaryAlb, { workloadServing: workload.health === "Available" }) : undefined,
  );
  const enabled = !!projectId && !!identityLabel && instanceKeys.length > 0;
  const cpuQuery =
    enabled && identityLabel && projectId
      ? workloadCpuSumQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const memoryQuery =
    enabled && identityLabel && projectId
      ? workloadMemorySumQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const rps = usePrometheusCard(projectId, rpsQuery, "requestsPerSecond", { enabled: !!proxyId });
  const [copied, copy] = useCopyToClipboard();

  return (
    <Card
      size="sm"
      className="overflow-hidden"
      data-testid="compute-plugin-workload-card"
    >
      <div className="flex min-w-0 items-start justify-between gap-3 px-4 pt-3">
        <h3 className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
          <span className="flex min-w-0 items-center gap-1.5">
            <Link
              to={href}
              // A utility only renders if the host already compiled it, and cloud-portal
              // never generates focus-visible:underline — hence ring utilities for focus.
              className="truncate font-medium hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
              data-e2e="workload-card-name-link"
            >
              {workload.name}
            </Link>
            <button
              type="button"
              className="text-muted-foreground inline-flex shrink-0 items-center focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
              aria-label={copied ? "Copied" : `Copy ${workload.name}`}
              title="Copy name"
              onClick={() => void copy(workload.name, { withToast: true })}
              data-e2e="workload-card-copy-name"
            >
              <Icon
                icon={copied ? CheckIcon : CopyIcon}
                size={12}
                className={cn("shrink-0", !copied && "opacity-60")}
              />
            </button>
          </span>
          {tags.length > 0 && (
            <span className="text-muted-foreground shrink-0 text-xs font-normal">
              {tags.join(" · ")}
            </span>
          )}
        </h3>
        <WorkloadStatusDot workload={workload} className="shrink-0" />
      </div>
      {/* Dimmed while deleting: the numbers are the last ones before teardown. */}
      <CardContent className={cn("flex flex-col gap-4", workload.deleting && "opacity-50")}>
        {primaryAlb?.hostname ? (
          <WorkloadUrl
            hostname={primaryAlb.hostname}
            customHostnames={primaryAlb.customHostnames}
            live={url.live}
            status={url.status}
            size="sm"
          />
        ) : null}
        <CpuMemorySparks
          projectId={projectId}
          cpuQuery={cpuQuery}
          memoryQuery={memoryQuery}
          timeRange={timeRange}
          allocation={allocation}
          wide
          pending={identityLoading}
          denied={identityDenied}
        />

        <div className="grid grid-cols-2 gap-2 sm:gap-3">
          <MetricCell
            label="Instances"
            value={`${workload.readyReplicas} / ${workload.desiredReplicas}`}
          />
          <MetricCell
            label="Requests"
            value={
              proxyId
                ? (rps.data?.formattedValue ??
                  formatKpiValue(rps.data?.value, "requestsPerSecond"))
                : "—"
            }
            placeholder={!proxyId}
          />
        </div>

        {workload.placementRegions.length > 0 && (
          <div className="flex flex-col gap-1.5">
            <p className="text-muted-foreground text-xs font-medium">
              Locations
            </p>
            {workload.placementRegions.map((region) => (
              <div
                key={region.name}
                className="flex items-center justify-between gap-2 text-sm"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <HealthDot health={region.health} label={region.health} />
                  <span className="truncate font-mono text-xs" title={region.locations.join(", ") || region.locationSelector}>
                    {regionLabel(region, locationIndex)}
                  </span>
                </div>
                <span className="text-muted-foreground shrink-0 text-xs">
                  {region.readyReplicas} / {region.desiredReplicas} healthy
                </span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
      <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 px-4 pb-3 text-xs">
        <span>
          {workload.deleting
            ? "Deleting — instances are being stopped"
            : `Updated ${formatDistanceToNowStrict(updatedAt, { addSuffix: true })}`}
        </span>
        <LinkButton
          as={Link}
          href={href}
          type="quaternary"
          theme="borderless"
          size="xs"
          icon={<Icon icon={ArrowRightIcon} size={12} />}
          iconPosition="right"
          data-e2e="workload-card-link"
        >
          View workload
        </LinkButton>
      </div>
    </Card>
  );
}

export default function WorkloadList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const [view, setView] = useState<WorkloadView>(readStoredView);
  // Page-level, so a query survives switching between cards and table — the
  // table used to own its search and unmounts on every toggle.
  const [search, setSearch] = useState("");
  useEffect(() => {
    try {
      window.localStorage.setItem(VIEW_STORAGE_KEY, view);
    } catch {
      // Storage unavailable (private mode / quota) — the toggle still works for the session.
    }
  }, [view]);

  const {
    enabled: computeEnabled,
    isLoading: isEnabling,
    error: enablementError,
    retry: retryEnablement,
  } = useComputeEnablement(projectId);

  const {
    data: workloads,
    isLoading: isWorkloadsLoading,
    error,
    refetch,
  } = useWorkloads(projectId, computeEnabled);
  // Both views list newest first; table header sorts start from this order.
  const sortedWorkloads = useMemo(() => newestFirst(workloads ?? []), [workloads]);
  const { data: instances = [] } = useInstances(projectId, computeEnabled);
  const { data: publishedByWorkload = {} } = usePublishedUrls(projectId, computeEnabled);
  const {
    identityLabel,
    isLoading: identityLoading,
    isDenied: identityDenied,
  } = useProjectResourceIdentity(projectId, { enabled: computeEnabled });
  const listRange = useOverviewRange("1h");
  const locationIndex = useLocationIndex(computeEnabled ? projectId : undefined);
  const instancesByWorkload = useMemo(() => {
    const grouped = new Map<string, typeof instances>();
    for (const instance of instances) {
      const key = instance.workloadName;
      if (!key) continue;
      const group = grouped.get(key) ?? [];
      group.push(instance);
      grouped.set(key, group);
    }
    return grouped;
  }, [instances]);
  const keysByWorkload = useMemo(
    () => new Map([...instancesByWorkload].map(([name, group]) => [name, identityValues(group)])),
    [instancesByWorkload]
  );
  const instanceKeysByWorkload = useMemo(
    () => Object.fromEntries(keysByWorkload),
    [keysByWorkload]
  );
  const allocationByWorkload = useMemo(
    () =>
      Object.fromEntries(
        [...instancesByWorkload].map(([name, group]) => [name, allocationOf(group)])
      ),
    [instancesByWorkload]
  );
  const fleetProxyIds = useMemo(
    () => Object.values(publishedByWorkload).map((published) => published.proxyName),
    [publishedByWorkload],
  );
  const fleetRpsQuery =
    projectId && fleetProxyIds.length > 0
      ? albRpsQueryMany(projectId, fleetProxyIds)
      : undefined;
  const fleetRps = usePrometheusCard(projectId, fleetRpsQuery, "requestsPerSecond", {
    enabled: computeEnabled && fleetProxyIds.length > 0,
  });

  // Filtered once here and handed to both views, so cards and table agree on
  // what a query matches (see `matchesWorkloadSearch`). Filters the
  // newest-first list so a query keeps that order.
  const visibleWorkloads = useMemo(
    () =>
      sortedWorkloads.filter((workload) =>
        matchesWorkloadSearch(workload, search, publishedByWorkload[workload.name]?.hostname)
      ),
    [sortedWorkloads, search, publishedByWorkload]
  );

  const isLoading = isEnabling || (computeEnabled && isWorkloadsLoading);

  // Build the child route path from the current URL rather than the portal's
  // internal `paths.config.ts` (unavailable to plugins) — the host mounts this
  // page at `/project/:projectId/services/:serviceSlug`.
  const basePath = location.pathname.replace(/\/$/, "");
  // Stable builders: WorkloadTable memoises its column defs on these.
  const workloadHref = useCallback(
    (name: string) => `${basePath}/${name}`,
    [basePath],
  );
  const projectHref = projectId ? `/project/${projectId}` : "/";
  const demoDialog = useDemoWorkloadDialog(projectId);
  const goToDemoWorkload = () =>
    demoDialog.goToWorkload((name) => navigate(workloadHref(name)));

  // `?tryDemo=1` arrives from the top-header hint, which links here from
  // elsewhere in the project rather than opening the dialog itself (it lives
  // on this page). Pop it once, then strip the param so
  // a refresh or the browser back button doesn't reopen it.
  const [searchParams, setSearchParams] = useSearchParams();
  useEffect(() => {
    if (searchParams.get("tryDemo") !== "1") return;
    demoDialog.openDialog();
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("tryDemo");
        return next;
      },
      { replace: true },
    );
    // Only ever meant to fire once, off the param that brought us here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams]);

  return (
    <div
      data-testid="compute-plugin-workload-list"
      className="flex min-w-0 flex-col gap-6"
    >
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link to={projectHref}>
                <Icon icon={HomeIcon} size={16} />
              </Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>Workloads</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <PageTitle
        title="Workloads"
        description="Groups of compute instances deployed across locations"
        actions={
          computeEnabled && !error && (isWorkloadsLoading || (workloads?.length ?? 0) > 0) ? (
            <ViewToggle view={view} onChange={setView} />
          ) : undefined
        }
      />

      {isLoading &&
        (view === "table" ? <WorkloadListTableSkeleton /> : <WorkloadListCardsSkeleton />)}

      {enablementError && (
        <div
          className="flex flex-col gap-6"
          data-testid="compute-plugin-workload-not-enabled"
        >
          <ErrorOrRestrictedState
            error={enablementError}
            restrictedMessage="You don't have permission to enable Compute for this project."
            onRetry={retryEnablement}
          />
          <WorkloadCliSections projectId={projectId} />
        </div>
      )}

      {!isLoading && computeEnabled && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view workloads."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && computeEnabled && !error && (workloads?.length ?? 0) === 0 && (
        <div
          className="flex flex-col gap-6"
          data-testid="compute-plugin-workload-empty"
        >
          <WorkloadCliSections projectId={projectId} />
          <div
            className="grid grid-cols-1 gap-4 lg:grid-cols-2"
            data-testid="compute-plugin-workload-grid"
          >
            <TryDemoWorkloadCard projectId={projectId} onOpen={demoDialog.openDialog} />
          </div>
        </div>
      )}

      {!isLoading && computeEnabled && !error && workloads && workloads.length > 0 && (
        <>
          <FleetSummary
            workloads={workloads}
            requests={
              fleetProxyIds.length > 0 && fleetRps.data && !fleetRps.error
                ? (fleetRps.data.formattedValue ||
                  formatKpiValue(fleetRps.data.value, "requestsPerSecond"))
                : undefined
            }
          />
          {/* Shared by both views: search on the left, page actions on the
              right — the shape the portal's own table toolbar uses. */}
          <div className="flex w-full flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
            <WorkloadSearch value={search} onChange={setSearch} />
            <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:shrink-0 sm:justify-end">
              <Button
                type="primary"
                theme="solid"
                size="small"
                disabled={!projectId}
                onClick={demoDialog.openDialog}
                icon={<Icon icon={RocketIcon} size={14} aria-hidden />}
                data-testid="compute-plugin-try-demo-button"
              >
                Deploy now
              </Button>
            </div>
          </div>
          {view === "table" ? (
            <Suspense fallback={<WorkloadListTableSkeleton summary={false} />}>
              <WorkloadTable
                workloads={visibleWorkloads}
                projectId={projectId}
                instanceKeysByWorkload={instanceKeysByWorkload}
                allocationByWorkload={allocationByWorkload}
                identityLabel={identityLabel}
                identityLoading={identityLoading}
                identityDenied={identityDenied}
                timeRange={listRange.timeRange}
                locationIndex={locationIndex}
                workloadHref={workloadHref}
                onOpen={(name) => navigate(workloadHref(name))}
              />
            </Suspense>
          ) : visibleWorkloads.length === 0 ? (
            <QuietNotice
              title="No workloads match your search"
              description="Try a different name, image or location."
              testId="compute-plugin-workload-grid-empty"
            />
          ) : (
            <div
              className="grid grid-cols-1 gap-4 lg:grid-cols-2"
              data-testid="compute-plugin-workload-grid"
            >
              {visibleWorkloads.map((workload) => (
                <WorkloadCard
                  key={workload.uid || workload.name}
                  workload={workload}
                  projectId={projectId}
                  instanceKeys={keysByWorkload.get(workload.name) ?? []}
                  allocation={allocationByWorkload[workload.name]}
                  published={publishedByWorkload[workload.name]}
                  identityLabel={identityLabel}
                  identityLoading={identityLoading}
                  identityDenied={identityDenied}
                  timeRange={listRange.timeRange}
                  locationIndex={locationIndex}
                  href={workloadHref(workload.name)}
                />
              ))}
            </div>
          )}
        </>
      )}

      <TryDemoDialog
        open={demoDialog.open}
        phase={demoDialog.phase}
        canClose={demoDialog.canClose}
        onClose={demoDialog.close}
        onConfirm={demoDialog.confirm}
        onGoToWorkload={goToDemoWorkload}
      />
    </div>
  );
}
