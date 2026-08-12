/**
 * `portal.page/project` extension at `workloads`, exposed as `WorkloadList`.
 *
 * Home layout matches the workloads wireframe: fleet summary strip + 2-column
 * cards with regions. Real compute API fields fill operational slots;
 * telemetry (Requests, Avg CPU) shows muted "Coming soon".
 */
import { CliBanner, SectionCard } from "../components/cli-section";
import { StatStrip } from "../components/stat-strip";
import { ErrorOrRestrictedState, LoadingSkeleton } from "../components/states";
import { useWorkloads } from "../lib/api";
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
import { PageTitle } from "@datum-cloud/datum-ui/page-title";
import { cn } from "@datum-cloud/datum-ui/utils";
import { formatDistanceToNowStrict } from "date-fns";
import { ArrowRightIcon, HomeIcon, RocketIcon, SearchIcon } from "lucide-react";
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

function regionLabel(region: WorkloadPlacementRegion): string {
  if (region.cityCodes.length > 0) return region.cityCodes.join(", ");
  return region.name;
}

function FleetSummary({ workloads }: { workloads: Workload[] }) {
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
      value: COMING_SOON,
      className: "text-muted-foreground text-sm font-medium",
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

function WorkloadCard({
  workload,
  onClick,
}: {
  workload: Workload;
  onClick: () => void;
}) {
  const updatedAt = workload.updatedAt ?? workload.createdAt;
  const tags =
    workload.tags.length > 0
      ? workload.tags
      : workload.runtimeType
        ? [workload.runtimeType]
        : [];

  return (
    <div
      className="border-card-border bg-card hover:border-foreground/20 flex cursor-pointer flex-col gap-4 rounded-xl border p-4 shadow transition-colors sm:p-5"
      onClick={onClick}
      data-testid="compute-plugin-workload-card"
    >
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between sm:gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h3 className="truncate font-semibold">{workload.name}</h3>
          {tags.length > 0 && (
            <span className="text-muted-foreground shrink-0 text-xs">
              {tags.join(" · ")}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span
            className={cn(
              "size-2 rounded-full",
              HEALTH_DOT_CLASS[workload.health],
            )}
          />
          <Badge
            type={workloadHealthToBadgeType(workload.health)}
            theme="light"
          >
            {statusLabel(workload)}
          </Badge>
        </div>
      </div>

      <div
        className="border-border bg-muted/40 text-muted-foreground flex h-12 items-center justify-center rounded-md border border-dashed text-xs"
        data-testid="compute-plugin-workload-metrics-placeholder"
      >
        {COMING_SOON}
      </div>

      <div className="border-border grid grid-cols-3 gap-2 border-t pt-4 sm:gap-3">
        <MetricCell
          label="Instances"
          value={`${workload.readyReplicas} / ${workload.desiredReplicas}`}
        />
        <MetricCell label="Requests" placeholder />
        <MetricCell label="Avg CPU" placeholder />
      </div>

      {workload.placementRegions.length > 0 && (
        <div className="border-border flex flex-col gap-1.5 border-t pt-4">
          <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
            Regions
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
                <span className="truncate">{regionLabel(region)}</span>
              </div>
              <span className="text-muted-foreground shrink-0 text-xs">
                {region.readyReplicas} / {region.desiredReplicas} healthy
              </span>
            </div>
          ))}
        </div>
      )}

      <div className="border-border text-muted-foreground flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-xs">
        <span>
          Updated {formatDistanceToNowStrict(updatedAt, { addSuffix: true })}
        </span>
        <span className="flex items-center gap-1">
          View workload
          <ArrowRightIcon className="size-3" />
        </span>
      </div>
    </div>
  );
}

export default function WorkloadList() {
  const { projectId } = useParams<{ projectId: string; serviceSlug: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const {
    data: workloads,
    isLoading,
    error,
    refetch,
  } = useWorkloads(projectId);

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
              <HomeIcon className="size-4" />
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
        description="Groups of compute instances deployed across regions"
      />

      {isLoading && <LoadingSkeleton />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage="You don't have permission to view workloads."
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && (workloads?.length ?? 0) === 0 && (
        <div
          className="flex flex-col gap-6"
          data-testid="compute-plugin-workload-empty"
        >
          <CliBanner
            title="Deploy workloads with datumctl"
            description="Workloads are created and managed using the Datum CLI. Install datumctl, write a manifest, and deploy — workloads you create will appear here automatically."
          />
          <div className="grid grid-cols-1 items-start gap-6 lg:grid-cols-2">
            <SectionCard
              icon={<RocketIcon className="size-4" />}
              title="Deploy a workload"
              description="Create a workload manifest and deploy it to your project. The dashboard will reflect the new workload within seconds."
              commands={[
                "datumctl compute deploy -f workload.yaml",
                `datumctl compute deploy --project=${projectId ?? ""} -f workload.yaml`,
              ]}
            />
            <SectionCard
              icon={<SearchIcon className="size-4" />}
              title="List & inspect workloads"
              description="Confirm your workload deployed successfully and inspect its current health and placement status."
              commands={[
                "datumctl compute workloads list",
                "datumctl compute workloads describe <name>",
              ]}
            />
          </div>
        </div>
      )}

      {!isLoading && !error && workloads && workloads.length > 0 && (
        <>
          <FleetSummary workloads={workloads} />
          <div
            className="grid grid-cols-1 gap-4 lg:grid-cols-2"
            data-testid="compute-plugin-workload-grid"
          >
            {workloads.map((workload) => (
              <WorkloadCard
                key={workload.uid || workload.name}
                workload={workload}
                onClick={() => navigate(workloadHref(workload.name))}
              />
            ))}
          </div>
        </>
      )}
    </div>
  );
}
