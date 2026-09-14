/**
 * `portal.page/project` extension at `workloads`, exposed as `WorkloadList`.
 *
 * Home layout matches the workloads wireframe: fleet summary strip + 2-column
 * cards with regions. Real compute API fields fill operational slots;
 * Requests and Avg CPU come from the same Prometheus series as instance pages.
 */
import { CliBanner, SectionCard } from "../components/cli-section";
import { ComputeEnablementBanner } from "../components/compute-enablement-banner";
import { MetricAreaChart, formatKpiValue } from "../components/metric-area-chart";
import { StatStrip } from "../components/stat-strip";
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
  useInstanceMetricIdentity,
  workloadCpuAvgQuery,
  workloadCpuSumQuery,
} from "../lib/metrics-queries";
import { lastThirtyMinutesRange, usePrometheusCard } from "../lib/prometheus";
import {
  formatLocationNames,
  formatLocationSelector,
  useLocationIndex,
  type LocationIndex,
} from "../lib/locations";
import {
  workloadHealthToBadgeType,
  type Workload,
  type WorkloadHealth,
  type WorkloadPlacementRegion,
} from "../schema";
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
import { Icon } from "@datum-cloud/datum-ui/icons";
import { cn } from "@datum-cloud/datum-ui/utils";
import { formatDistanceToNowStrict } from "date-fns";
import { ArrowRightIcon, HomeIcon, RocketIcon, SearchIcon } from "lucide-react";
import { useMemo } from "react";
import { useLocation, useNavigate, useParams } from "react-router";

const COMING_SOON = "Coming soon";

const HEALTH_DOT_CLASS: Record<WorkloadHealth, string> = {
  Available: "bg-green-500",
  Degraded: "bg-yellow-500",
  Unavailable: "bg-red-500",
  Unknown: "bg-muted-foreground",
};

function statusLabel(workload: Workload): string {
  if (workload.health === "Available") {
    const ready = workload.readyReplicas;
    const desired = workload.desiredReplicas;
    if (desired > 0 && ready === desired) return "All healthy";
    return "Healthy";
  }
  if (workload.health === "Degraded") {
    const notReady = Math.max(
      0,
      workload.desiredReplicas - workload.readyReplicas,
    );
    if (notReady === 1) return "1 degraded";
    if (notReady > 1) return `${notReady} degraded`;
    return "Degraded";
  }
  if (workload.health === "Unavailable") return "Unavailable";
  return "Unknown";
}

function regionLabel(region: WorkloadPlacementRegion, index: LocationIndex): string {
  if (region.locations.length > 0) return formatLocationNames(region.locations, index);
  if (region.locationSelector) return formatLocationSelector(region.locationSelector, index) ?? region.locationSelector;
  return region.name;
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
  const degraded = workloads.filter((w) => w.health === "Degraded").length;
  const errored = workloads.filter(
    (w) => w.health === "Unavailable" || w.health === "Unknown",
  ).length;

  const stats: { label: string; value: string; className?: string }[] = [
    { label: "Workloads", value: String(workloads.length) },
    {
      label: "Instances",
      value:
        desiredInstances > 0
          ? `${readyInstances} / ${desiredInstances}`
          : String(readyInstances),
    },
    {
      label: "Healthy",
      value: String(healthy),
      className: healthy > 0 ? "text-green-600 dark:text-green-500" : undefined,
    },
    {
      label: "Degraded",
      value: String(degraded),
      className:
        degraded > 0 ? "text-yellow-600 dark:text-yellow-500" : undefined,
    },
    {
      label: "Errored",
      value: String(errored),
      className: errored > 0 ? "text-red-600 dark:text-red-500" : undefined,
    },
    {
      label: "Requests",
      value: requests,
    },
  ];

  return <StatStrip stats={stats} testId="compute-plugin-fleet-summary" />;
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
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
        {label}
      </p>
      <p
        className={cn(
          "mt-0.5 text-xs font-medium sm:text-sm",
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
  instanceNames,
  proxyId,
  identityLabel,
  locationIndex,
  onClick,
}: {
  workload: Workload;
  projectId?: string;
  instanceNames: string[];
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
  const enabled = !!projectId && !!identityLabel && instanceNames.length > 0;
  const cpuQuery =
    enabled && identityLabel && projectId
      ? workloadCpuAvgQuery(projectId, identityLabel.label, instanceNames)
      : undefined;
  const sparkQuery =
    enabled && identityLabel && projectId
      ? workloadCpuSumQuery(projectId, identityLabel.label, instanceNames)
      : undefined;
  const rpsQuery = projectId && proxyId ? albRpsQuery(projectId, proxyId) : undefined;
  const cpu = usePrometheusCard(cpuQuery, "number", { enabled });
  const rps = usePrometheusCard(rpsQuery, "requestsPerSecond", { enabled: !!proxyId });

  return (
    <Card
      size="sm"
      sectioned
      className="hover:border-foreground/20 cursor-pointer overflow-hidden transition-colors"
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
                : "—"
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
  const { identity } = useInstanceMetricIdentity(projectId, instances[0]?.name);
  const locationIndex = useLocationIndex(computeEnabled ? projectId : undefined);
  const namesByWorkload = useMemo(() => {
    const map = new Map<string, string[]>();
    for (const instance of instances) {
      const key = instance.workloadName;
      if (!key) continue;
      const names = map.get(key) ?? [];
      names.push(instance.name);
      map.set(key, names);
    }
    return map;
  }, [instances]);
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
  // page at `/project/:projectId/services/:serviceSlug/workloads`.
  const basePath = location.pathname.replace(/\/$/, "");
  const workloadHref = (name: string) => `${basePath}/${name}`;
  const projectHref = projectId ? `/project/${projectId}` : "/";

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
          <div
            className="grid grid-cols-1 gap-4 lg:grid-cols-2"
            data-testid="compute-plugin-workload-grid"
          >
            {workloads.map((workload) => (
              <WorkloadCard
                key={workload.uid || workload.name}
                workload={workload}
                projectId={projectId}
                instanceNames={namesByWorkload.get(workload.name) ?? []}
                proxyId={publishedByWorkload[workload.name]?.proxyName}
                identityLabel={identity}
                locationIndex={locationIndex}
                onClick={() => navigate(workloadHref(workload.name))}
              />
            ))}
          </div>
        </>
      )}
    </div>
  );
}
