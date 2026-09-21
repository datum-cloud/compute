/**
 * `portal.page/project` extension at the plugin mount root, exposed as `WorkloadList`.
 *
 * Home layout matches the workloads wireframe: fleet summary strip + 2-column
 * cards with regions. Real compute API fields fill operational slots;
 * Requests and Avg CPU come from the same Prometheus series as instance pages.
 */
import { CliBanner, SectionCard } from "../components/cli-section";
import { ComputeEnablementBanner } from "../components/compute-enablement-banner";
import { formatKpiValue } from "../components/metric-area-chart";
import { CpuMemorySparks } from "../components/metric-sparkline";
import { SparklineStatCard } from "../components/sparkline-stat-card";
import { ErrorOrRestrictedState, LoadingSkeleton } from "../components/states";
import {
  useComputeEntitlement,
  useCreateDemoWorkload,
  useInstances,
  usePublishedUrls,
  useWorkload,
  useWorkloads,
} from "../lib/api";
import {
  albRpsQuery,
  albRpsQueryMany,
  identityValues,
  type InstanceIdentityLabel,
  useProjectResourceIdentity,
  workloadCpuAvgQuery,
  workloadMemoryAvgQuery,
} from "../lib/metrics-queries";
import { lastThirtyMinutesRange, usePrometheusCard } from "../lib/prometheus";
import { useOverviewRange } from "../components/overview-range";
import { useLocationIndex, type LocationIndex } from "../lib/locations";
import { HEALTH_DOT_CLASS, regionLabel, statusLabel } from "../lib/workload-presenters";
import { workloadHealthToBadgeType, type Workload } from "../schema";
import { Badge } from "@datum-cloud/datum-ui/badge";
import { Button } from "@datum-cloud/datum-ui/button";
import { Dialog } from "@datum-cloud/datum-ui/dialog";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@datum-cloud/datum-ui/breadcrumb";
import {
  Card,
  CardAction,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@datum-cloud/datum-ui/card";
import { PageTitle } from "@datum-cloud/datum-ui/page-title";
import { Skeleton } from "@datum-cloud/datum-ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@datum-cloud/datum-ui/tabs";
import { toast } from "@datum-cloud/datum-ui/toast";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { cn } from "@datum-cloud/datum-ui/utils";
import { formatDistanceToNowStrict } from "date-fns";
import {
  ArrowRightIcon,
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
// it only when the user picks the table view so card-view users don't pay.
const WorkloadTable = lazy(() =>
  import("../components/workload-table").then((m) => ({
    default: m.WorkloadTable,
  })),
);

function TableSkeleton() {
  return (
    <div className="space-y-6" aria-busy="true">
      <Skeleton className="h-9 w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="flex items-center gap-4 border-b px-4 py-3 last:border-b-0">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="h-4 w-20" />
            <Skeleton className="h-8 w-40" />
            <Skeleton className="h-4 w-16" />
          </div>
        ))}
      </div>
    </div>
  );
}

type WorkloadView = "cards" | "table";
const VIEW_STORAGE_KEY = "compute-plugin:workload-view";

function readStoredView(): WorkloadView {
  try {
    return window.localStorage.getItem(VIEW_STORAGE_KEY) === "table" ? "table" : "cards";
  } catch {
    return "cards";
  }
}

/** Cards / table switch — datum-ui's segmented Tabs, placed in the page title actions like ALB's toolbar. */
function ViewToggle({
  view,
  onChange,
}: {
  view: WorkloadView;
  onChange: (view: WorkloadView) => void;
}) {
  return (
    <Tabs
      value={view}
      onValueChange={(value) => onChange(value as WorkloadView)}
      data-testid="compute-plugin-workload-view-toggle"
    >
      <TabsList aria-label="Workload view" className="border-card-border border">
        <TabsTrigger value="cards" className="gap-1.5">
          <Icon icon={LayoutGridIcon} size={14} />
          Cards
        </TabsTrigger>
        <TabsTrigger value="table" className="gap-1.5">
          <Icon icon={Rows3Icon} size={14} />
          Table
        </TabsTrigger>
      </TabsList>
    </Tabs>
  );
}

function FleetSummary({
  workloads,
  requests,
}: {
  workloads: Workload[];
  requests: string;
}) {
  const readyInstances = workloads.reduce((sum, w) => sum + w.readyReplicas, 0);
  const desiredInstances = workloads.reduce(
    (sum, w) => sum + w.desiredReplicas,
    0,
  );
  const healthy = workloads.filter((w) => w.health === "Available").length;

  const timeRange = useMemo(() => lastThirtyMinutesRange(), []);

  return (
    <div
      className="grid grid-cols-2 gap-6 lg:grid-cols-4"
      data-testid="compute-plugin-fleet-summary"
    >
      <SparklineStatCard
        title="Workloads"
        value={String(workloads.length)}
        timeRange={timeRange}
      />
      <SparklineStatCard
        title="Instances"
        value={
          desiredInstances > 0
            ? `${readyInstances} / ${desiredInstances}`
            : String(readyInstances)
        }
        timeRange={timeRange}
      />
      <SparklineStatCard
        title="Healthy"
        value={String(healthy)}
        timeRange={timeRange}
      />
      <SparklineStatCard title="Requests" value={requests} timeRange={timeRange} />
    </div>
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

/** The "Deploy a workload" / "List & inspect workloads" cards — datumctl handles enabling
 * Compute itself (the same "Would you like to request access?" prompt the banner's button
 * triggers), so these commands work whether or not Compute is enabled yet. */
function WorkloadCliSections({ projectId }: { projectId: string | undefined }) {
  return (
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
          "datumctl compute workloads list",
          "datumctl compute workloads describe <name>",
        ]}
      />
    </div>
  );
}

/** Confirm → create → wait → ready, with the user deciding when to leave at
 * every step. Kept as a dialog (rather than showing progress inline in the
 * grid) because a demo Workload appearing mid-reconciliation among the
 * user's real workloads, then changing shape as it comes up, reads as a
 * glitch. Navigation is always a manual click, never automatic — the dialog
 * polls readiness only to update its own copy/buttons, and just says so
 * once the instance is up rather than whisking the user away on its own. */
type DemoPhase = "confirm" | "creating" | "waiting" | "ready" | "error";

const DEMO_PHASE_COPY: Record<DemoPhase, string> = {
  confirm: "This deploys a running workload into your project — one instance in DFW, usually live within a minute.",
  creating: "Deploying your workload…",
  waiting: "Your workload is coming up in the background. You can wait here or head to its page now — it'll keep coming up either way.",
  ready: "Your workload is live.",
  error: "Something went wrong deploying the workload.",
};

/** Owns the demo dialog's state. Deliberately hoisted out of the CTA card:
 * the workloads grid switches between an "empty" and a "populated" branch
 * as soon as the demo Workload shows up (via the query invalidation on
 * create), and each branch renders its own `<TryDemoWorkloadCard>` element —
 * if the dialog's open/phase state lived inside that card, the branch swap
 * would unmount the open dialog's instance and mount a fresh, closed one,
 * which looks exactly like the dialog auto-closing itself. Called once in
 * `WorkloadList` and threaded into both the card and a single, always-mounted
 * `TryDemoDialog` so the state survives that swap. */
function useDemoWorkloadDialog(projectId: string | undefined) {
  const [open, setOpen] = useState(false);
  const [phase, setPhase] = useState<DemoPhase>("confirm");
  const [deployedName, setDeployedName] = useState<string | undefined>();
  const { mutate } = useCreateDemoWorkload(projectId);
  // Only polls while actually waiting on readiness — stops once ready, on
  // error, or when the dialog isn't tracking a deploy.
  const { data: workload, error: workloadError } = useWorkload(
    projectId,
    phase === "waiting" ? deployedName : undefined,
  );

  useEffect(() => {
    if (phase !== "waiting") return;
    if (workload?.health === "Available" && workload.readyReplicas > 0) {
      setPhase("ready");
    } else if (workloadError && workloadError.status !== 404) {
      setPhase("error");
    }
  }, [phase, workload, workloadError]);

  // The create call itself (a handful of sequential POSTs) is quick and has
  // no object to show yet, so closing is blocked until it settles. Once
  // we're waiting on readiness, the Workload already exists and keeps
  // reconciling in the background regardless of the dialog — the user can
  // leave any time, either back to the list or straight to the workload.
  const canClose = phase !== "creating";

  const openDialog = () => {
    setPhase("confirm");
    setDeployedName(undefined);
    setOpen(true);
  };

  const close = () => {
    if (!canClose) return;
    setOpen(false);
  };

  const confirm = () => {
    setPhase("creating");
    mutate(undefined, {
      onSuccess: (workloadName) => {
        setDeployedName(workloadName);
        setPhase("waiting");
      },
      onError: () => {
        setPhase("error");
        toast.error("Failed to deploy the workload");
      },
    });
  };

  const goToWorkload = (onView: (workloadName: string) => void) => {
    setOpen(false);
    if (deployedName) onView(deployedName);
  };

  return { open, phase, canClose, openDialog, close, confirm, goToWorkload };
}

/** Static CTA card offering a one-click demo deploy — always the last card in
 * the grid (the only one when the project has no workloads yet). Purely
 * presentational: the dialog it opens is owned and rendered by the parent
 * (see `useDemoWorkloadDialog`), since this card gets unmounted/remounted
 * when the grid switches branches once the demo appears. */
function TryDemoWorkloadCard({
  projectId,
  onOpen,
}: {
  projectId?: string;
  onOpen: () => void;
}) {
  return (
    <Card
      className="border-secondary/20 bg-secondary/5 flex h-full flex-col items-center justify-center gap-4 p-6 text-center"
      data-testid="compute-plugin-try-demo-card"
    >
      <Icon icon={RocketIcon} size={28} className="text-secondary" />
      <div className="flex flex-col gap-1">
        <h3 className="text-sm font-semibold">Ready to deploy?</h3>
        <p className="text-muted-foreground text-sm">
          Launch a running workload in one click
        </p>
      </div>
      <Button type="secondary" theme="solid" size="small" disabled={!projectId} onClick={onOpen}>
        Deploy Now
      </Button>
    </Card>
  );
}

/** The demo confirm/progress dialog itself — rendered once, unconditionally,
 * by `WorkloadList` (see `useDemoWorkloadDialog` for why). */
function TryDemoDialog({
  open,
  phase,
  canClose,
  onClose,
  onConfirm,
  onGoToWorkload,
}: {
  open: boolean;
  phase: DemoPhase;
  canClose: boolean;
  onClose: () => void;
  onConfirm: () => void;
  onGoToWorkload: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Content>
        <Dialog.Header
          title="Deploy a workload"
          description={DEMO_PHASE_COPY[phase]}
          onClose={canClose ? onClose : undefined}
        />
        <Dialog.Footer>
          {phase === "confirm" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Cancel
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onConfirm}>
                Deploy
              </Button>
            </>
          )}
          {phase === "creating" && (
            <Button type="secondary" theme="solid" size="small" loading disabled>
              Creating…
            </Button>
          )}
          {phase === "waiting" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onGoToWorkload}>
                Go to Workload
              </Button>
            </>
          )}
          {phase === "ready" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onGoToWorkload}>
                View Workload
              </Button>
            </>
          )}
          {phase === "error" && (
            <>
              <Button type="secondary" theme="outline" size="small" onClick={onClose}>
                Close
              </Button>
              <Button type="secondary" theme="solid" size="small" onClick={onConfirm}>
                Try Again
              </Button>
            </>
          )}
        </Dialog.Footer>
      </Dialog.Content>
    </Dialog>
  );
}

function WorkloadCard({
  workload,
  projectId,
  instanceKeys,
  proxyId,
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
  proxyId?: string;
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
  const enabled = !!projectId && !!identityLabel && instanceKeys.length > 0;
  const cpuQuery =
    enabled && identityLabel && projectId
      ? workloadCpuAvgQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const memoryQuery =
    enabled && identityLabel && projectId
      ? workloadMemoryAvgQuery(projectId, identityLabel, instanceKeys)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const rps = usePrometheusCard(rpsQuery, "requestsPerSecond", { enabled: !!proxyId });

  return (
    <Card
      size="sm"
      sectioned
      className="overflow-hidden"
      data-testid="compute-plugin-workload-card"
    >
      <CardHeader size="sm" bordered>
        <CardTitle className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
          <span className="truncate font-semibold">{workload.name}</span>
          {tags.length > 0 && (
            <span className="text-muted-foreground shrink-0 text-xs font-normal">
              {tags.join(" · ")}
            </span>
          )}
        </CardTitle>
        <CardAction>
          <div className="flex shrink-0 items-center gap-2">
            <span
              className={cn("size-2 rounded-full", HEALTH_DOT_CLASS[workload.health])}
            />
            <Badge type={workloadHealthToBadgeType(workload.health)} theme="light">
              {statusLabel(workload)}
            </Badge>
          </div>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <CpuMemorySparks
          cpuQuery={cpuQuery}
          memoryQuery={memoryQuery}
          timeRange={timeRange}
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
            <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
              Locations
            </p>
            {workload.placementRegions.map((region) => (
              <div
                key={region.name}
                className="flex items-center justify-between gap-2 text-sm"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span
                    className={cn(
                      "size-2 shrink-0 rounded-full",
                      HEALTH_DOT_CLASS[region.health],
                    )}
                    aria-label={region.health}
                  />
                  <span className="truncate" title={region.locations.join(", ") || region.locationSelector}>
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
      <CardFooter bordered className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
        <span>
          Updated {formatDistanceToNowStrict(updatedAt, { addSuffix: true })}
        </span>
        <Link
          to={href}
          // A utility only renders if the host already compiled it, and cloud-portal
          // never generates focus-visible:underline — hence ring utilities for focus.
          className="flex items-center gap-1 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          data-e2e="workload-card-link"
        >
          View workload
          <Icon icon={ArrowRightIcon} size={12} />
        </Link>
      </CardFooter>
    </Card>
  );
}

export default function WorkloadList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const [view, setView] = useState<WorkloadView>(readStoredView);
  useEffect(() => {
    try {
      window.localStorage.setItem(VIEW_STORAGE_KEY, view);
    } catch {
      // Storage unavailable (private mode / quota) — the toggle still works for the session.
    }
  }, [view]);

  const {
    data: entitlement,
    isLoading: isEntitlementLoading,
    error: entitlementError,
    refetch: refetchEntitlement,
  } = useComputeEntitlement(projectId);
  const computeEnabled = entitlement?.phase === "Active";

  const {
    data: workloads,
    isLoading: isWorkloadsLoading,
    error,
    refetch,
  } = useWorkloads(projectId, computeEnabled);
  const { data: instances = [] } = useInstances(projectId, computeEnabled);
  const { data: publishedByWorkload = {} } = usePublishedUrls(projectId, computeEnabled);
  const {
    identityLabel,
    isLoading: identityLoading,
    isDenied: identityDenied,
  } = useProjectResourceIdentity(projectId, { enabled: computeEnabled });
  const listRange = useOverviewRange("1h");
  const locationIndex = useLocationIndex(computeEnabled ? projectId : undefined);
  const keysByWorkload = useMemo(() => {
    const map = new Map<string, string[]>();
    const grouped = new Map<string, typeof instances>();
    for (const instance of instances) {
      const key = instance.workloadName;
      if (!key) continue;
      const group = grouped.get(key) ?? [];
      group.push(instance);
      grouped.set(key, group);
    }
    for (const [name, group] of grouped) {
      map.set(name, identityValues(group));
    }
    return map;
  }, [instances]);
  const instanceKeysByWorkload = useMemo(
    () => Object.fromEntries(keysByWorkload),
    [keysByWorkload]
  );
  const fleetProxyIds = useMemo(
    () => Object.values(publishedByWorkload).map((published) => published.proxyName),
    [publishedByWorkload],
  );
  const fleetRpsQuery =
    projectId && fleetProxyIds.length > 0
      ? albRpsQueryMany(projectId, fleetProxyIds)
      : undefined;
  const fleetRps = usePrometheusCard(fleetRpsQuery, "requestsPerSecond", {
    enabled: computeEnabled && fleetProxyIds.length > 0,
  });

  const isLoading = isEntitlementLoading || (computeEnabled && isWorkloadsLoading);

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

  // `?tryDemo=1` arrives from the project-home card / top-header hint, which
  // link here from elsewhere in the project rather than opening the dialog
  // themselves (it lives on this page). Pop it once, then strip the param so
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
  const albHref = useMemo(
    () =>
      projectId
        ? (proxyName: string) =>
            `/project/${projectId}/alb/${proxyName}/overview`
        : undefined,
    [projectId],
  );

  return (
    <div
      data-testid="compute-plugin-workload-list"
      className="flex min-w-0 flex-col gap-6"
    >
      <Breadcrumb className="min-w-0 overflow-x-auto">
        <BreadcrumbList className="flex-nowrap">
          <BreadcrumbItem>
            <BreadcrumbLink href={projectHref}>
              <Icon icon={HomeIcon} size={16} />
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
          !isLoading && computeEnabled && !error && (workloads?.length ?? 0) > 0 ? (
            <ViewToggle view={view} onChange={setView} />
          ) : undefined
        }
      />

      {isLoading && <LoadingSkeleton />}

      {!isEntitlementLoading && entitlementError && (
        <ErrorOrRestrictedState
          error={entitlementError}
          restrictedMessage="You don't have permission to view this project's Compute access."
          onRetry={() => void refetchEntitlement()}
        />
      )}

      {!isEntitlementLoading && !entitlementError && !computeEnabled && projectId && (
        <div
          className="flex flex-col gap-6"
          data-testid="compute-plugin-workload-not-enabled"
        >
          <ComputeEnablementBanner projectId={projectId} phase={entitlement?.phase ?? null} />
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
          <CliBanner
            title="Deploy workloads with datumctl"
            description="Workloads are created and managed using the Datum CLI. Install datumctl, write a manifest, and deploy — workloads you create will appear here automatically."
          />
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
              fleetProxyIds.length > 0
                ? (fleetRps.data?.formattedValue ??
                  formatKpiValue(fleetRps.data?.value, "requestsPerSecond"))
                : "—"
            }
          />
          {view === "table" ? (
            <Suspense fallback={<TableSkeleton />}>
              <WorkloadTable
                workloads={workloads}
                projectId={projectId}
                publishedByWorkload={publishedByWorkload}
                instanceKeysByWorkload={instanceKeysByWorkload}
                identityLabel={identityLabel}
                identityLoading={identityLoading}
                identityDenied={identityDenied}
                timeRange={listRange.timeRange}
                locationIndex={locationIndex}
                workloadHref={workloadHref}
                albHref={albHref}
                onOpen={(name) => navigate(workloadHref(name))}
              />
            </Suspense>
          ) : (
            <div
              className="grid grid-cols-1 gap-4 lg:grid-cols-2"
              data-testid="compute-plugin-workload-grid"
            >
              {workloads.map((workload) => (
                <WorkloadCard
                  key={workload.uid || workload.name}
                  workload={workload}
                  projectId={projectId}
                  instanceKeys={keysByWorkload.get(workload.name) ?? []}
                  proxyId={publishedByWorkload[workload.name]?.proxyName}
                  identityLabel={identityLabel}
                  identityLoading={identityLoading}
                  identityDenied={identityDenied}
                  timeRange={listRange.timeRange}
                  locationIndex={locationIndex}
                  href={workloadHref(workload.name)}
                />
              ))}
              <TryDemoWorkloadCard projectId={projectId} onOpen={demoDialog.openDialog} />
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
