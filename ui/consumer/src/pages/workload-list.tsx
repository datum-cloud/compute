/**
 * `portal.page/project` extension at the plugin mount root, exposed as `WorkloadList`.
 *
 * Home layout matches the workloads wireframe: fleet summary strip + 2-column
 * cards with regions. Real compute API fields fill operational slots;
 * Requests and Avg CPU come from the same Prometheus series as instance pages.
 */
import { CliBanner, SectionCard } from "../components/cli-section";
import { ComputeEnablementBanner } from "../components/compute-enablement-banner";
import { MetricAreaChart, formatKpiValue } from "../components/metric-area-chart";
import { SparklineStatCard } from "../components/sparkline-stat-card";
import { ErrorOrRestrictedState, LoadingSkeleton } from "../components/states";
import {
  useComputeEntitlement,
  useInstances,
  usePublishedUrls,
  useWorkloads,
} from "../lib/api";
import {
  albRpsQuery,
  albRpsQueryMany,
  identityValues,
  useInstanceMetricIdentity,
  workloadCpuAvgQuery,
  workloadCpuSumQuery,
} from "../lib/metrics-queries";
import { lastThirtyMinutesRange, usePrometheusCard } from "../lib/prometheus";
import { useLocationIndex, type LocationIndex } from "../lib/locations";
import { HEALTH_DOT_CLASS, regionLabel, statusLabel } from "../lib/workload-presenters";
import { workloadHealthToBadgeType, type Workload } from "../schema";
import { Badge } from "@datum-cloud/datum-ui/badge";
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
import { useLocation, useNavigate, useParams } from "react-router";

const COMING_SOON = "Coming soon";

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
        {value ?? COMING_SOON}
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

function WorkloadCard({
  workload,
  projectId,
  instanceKeys,
  proxyId,
  identityLabel,
  locationIndex,
  onClick,
}: {
  workload: Workload;
  projectId?: string;
  instanceKeys: string[];
  proxyId?: string;
  identityLabel?: ReturnType<typeof useInstanceMetricIdentity>["identity"];
  locationIndex: LocationIndex;
  onClick: () => void;
}) {
  const updatedAt = workload.updatedAt ?? workload.createdAt;
  const tags =
    workload.tags.length > 0
      ? workload.tags
      : workload.runtimeType
        ? [workload.runtimeType]
        : [];
  const timeRange = useMemo(() => lastThirtyMinutesRange(), []);
  const enabled = !!projectId && !!identityLabel && instanceKeys.length > 0;
  const cpuQuery =
    enabled && identityLabel && projectId
      ? workloadCpuAvgQuery(projectId, identityLabel.label, instanceKeys)
      : undefined;
  const sparkQuery =
    enabled && identityLabel && projectId
      ? workloadCpuSumQuery(projectId, identityLabel.label, instanceKeys)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const cpu = usePrometheusCard(cpuQuery, "number", { enabled });
  const rps = usePrometheusCard(rpsQuery, "requestsPerSecond", { enabled: !!proxyId });

  return (
    <Card
      size="sm"
      sectioned
      className="cursor-pointer overflow-hidden"
      onClick={onClick}
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
        <MetricAreaChart
          title="CPU"
          query={sparkQuery}
          timeRange={timeRange}
          format="number"
          enabled={enabled}
          unavailable={!enabled}
          embedded
          height={48}
        />

        <div className="grid grid-cols-3 gap-2 sm:gap-3">
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
          <MetricCell
            label="Avg CPU"
            value={
              enabled
                ? (cpu.data?.formattedValue ?? formatKpiValue(cpu.data?.value, "number"))
                : COMING_SOON
            }
            placeholder={!enabled}
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
        <span className="flex items-center gap-1">
          View workload
          <Icon icon={ArrowRightIcon} size={12} />
        </span>
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
  const { identity } = useInstanceMetricIdentity(projectId, instances[0]);
  const locationIndex = useLocationIndex(computeEnabled ? projectId : undefined);
  const keysByWorkload = useMemo(() => {
    const map = new Map<string, string[]>();
    const label = identity?.label;
    if (!label) return map;
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
  }, [instances, identity?.label]);
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
                  identityLabel={identity}
                  locationIndex={locationIndex}
                  onClick={() => navigate(workloadHref(workload.name))}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
