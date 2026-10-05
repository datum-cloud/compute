import { Badge } from "@datum-cloud/datum-ui/badge";
import { Card, CardContent } from "@datum-cloud/datum-ui/card";
import { Icon, SpinnerIcon } from "@datum-cloud/datum-ui/icons";
import { Tooltip } from "@datum-cloud/datum-ui/tooltip";
import {
  CircleCheckIcon,
  ClockIcon,
  GlobeIcon,
  RadioIcon,
  TriangleAlertIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { albStatusDisplay, type AlbStatus } from "../lib/alb-status";
import { formatElapsed, type DeployStatus } from "../lib/workload-presenters";
import { workloadHealthToBadgeType, type WorkloadHealth } from "../schema";

function Chip({
  tone,
  icon,
  spinner = false,
  children,
}: {
  tone: "success" | "warning" | "danger" | "info" | "muted";
  icon: typeof GlobeIcon;
  /** Swaps the icon for a spinner while something is in flight. */
  spinner?: boolean;
  children: ReactNode;
}) {
  return (
    <Badge
      type={tone}
      theme={tone === "muted" ? "solid" : "light"}
      className="h-6 gap-1.5 rounded-md px-2 text-xs font-medium whitespace-nowrap"
    >
      {spinner ? (
        <SpinnerIcon size="xs" className="shrink-0" aria-hidden />
      ) : (
        <Icon icon={icon} size={12} className="shrink-0" />
      )}
      {children}
    </Badge>
  );
}

function PublishedHostname({
  prefix,
  hostname,
  customHostnames,
}: {
  prefix: string;
  hostname: string;
  customHostnames: string[];
}) {
  const extras = customHostnames.filter((host) => host && host !== hostname);
  const hostnameEl = (
    <span className="min-w-0 truncate" title={hostname}>
      {hostname}
    </span>
  );

  return (
    <span className="flex min-w-0 items-baseline gap-1">
      <span className="shrink-0">{prefix}</span>
      {extras.length === 0 ? (
        hostnameEl
      ) : (
        <Tooltip
          side="bottom"
          align="start"
          message={
            <span className="flex flex-col items-start gap-0.5 text-left">
              {extras.map((host) => (
                <span key={host} className="font-mono">
                  {host}
                </span>
              ))}
            </span>
          }
        >
          <span
            tabIndex={0}
            className="flex min-w-0 items-baseline gap-1"
            style={{
              cursor: "help",
              textDecoration: "underline",
              textDecorationStyle: "dotted",
              textUnderlineOffset: "2px",
            }}
            aria-label={`Also published at ${extras.join(", ")}`}
          >
            {hostnameEl}
            <span className="shrink-0">+{extras.length}</span>
          </span>
        </Tooltip>
      )}
    </span>
  );
}

export function WorkloadHealthStrip({
  health,
  healthyCount,
  totalCount,
  locationCount,
  albHref,
  albLabel,
  albHostname,
  customHostnames = [],
  deploy,
  failure,
  alb,
  now = Date.now(),
}: {
  health: WorkloadHealth;
  healthyCount: number;
  totalCount: number;
  locationCount: number;
  albHref?: string;
  albLabel?: string;
  /** Platform default hostname of the attached load balancer. */
  albHostname?: string;
  /** User-attached hostnames. Listed in the reachable-at tooltip. */
  customHostnames?: string[];
  /** Set while the workload is coming up — see `deployStatus`. */
  deploy?: DeployStatus;
  /** Why instances are failing, shown in place of the generic unavailable copy. */
  failure?: string;
  /** Whether the attached load balancer is serving yet. */
  alb?: AlbStatus;
  /** For the deploy's elapsed time. */
  now?: number;
}) {
  const tone = deploy?.tone ?? workloadHealthToBadgeType(health);
  const headline: {
    icon: ReactNode;
    title: string;
    detail: string;
    note?: string;
    /** Extra detail on hover, for transient states that shouldn't add a line. */
    tooltip?: string;
    ring: string;
  } = (() => {
    if (deploy) {
      const ring = `var(--color-badge-${deploy.tone})`;
      return {
        icon: deploy.inProgress ? (
          <SpinnerIcon size="md" style={{ color: ring }} aria-hidden />
        ) : (
          <Icon icon={ClockIcon} size={18} style={{ color: ring }} />
        ),
        title: deploy.title,
        detail: [
          deploy.step,
          `${healthyCount}/${totalCount} ${totalCount === 1 ? "instance" : "instances"} ready`,
          `started ${formatElapsed(deploy.since, now)} ago`,
        ].join(" · "),
        // Quota is a standing state, so it earns a line. A slow start keeps its
        // message in the title tooltip: the strip shouldn't grow mid-deploy.
        note: deploy.inProgress ? undefined : deploy.message,
        tooltip: deploy.inProgress ? deploy.message : undefined,
        ring,
      };
    }
    if (failure) {
      return {
        icon: (
          <Icon
            icon={TriangleAlertIcon}
            size={18}
            style={{ color: "var(--color-badge-danger)" }}
          />
        ),
        title: "Unavailable",
        detail: `${healthyCount}/${totalCount} instances available`,
        note: failure,
        ring: "var(--color-badge-danger)",
      };
    }
    if (health === "Available") {
      return {
        icon: (
          <Icon
            icon={CircleCheckIcon}
            size={18}
            style={{ color: "var(--color-badge-success)" }}
          />
        ),
        title: "Serving normally",
        detail: `${healthyCount}/${totalCount} instances · ${locationCount} ${locationCount === 1 ? "location" : "locations"}`,
        ring: "var(--color-badge-success)",
      };
    }
    if (health === "Degraded") {
      return {
        icon: (
          <Icon
            icon={TriangleAlertIcon}
            size={18}
            style={{ color: "var(--color-badge-warning)" }}
          />
        ),
        title: "Degraded",
        detail: `${healthyCount}/${totalCount} instances available`,
        ring: "var(--color-badge-warning)",
      };
    }
    if (health === "Unavailable") {
      return {
        icon: (
          <Icon
            icon={TriangleAlertIcon}
            size={18}
            style={{ color: "var(--color-badge-danger)" }}
          />
        ),
        title: "Unavailable",
        detail:
          totalCount === 0
            ? "No running instances"
            : `${healthyCount}/${totalCount} instances available`,
        ring: "var(--color-badge-danger)",
      };
    }
    return {
      icon: (
        <Icon
          icon={RadioIcon}
          size={18}
          style={{ color: "var(--color-badge-info)" }}
        />
      ),
      title: "Waiting for status",
      detail: "Workload health has not been reported yet.",
      ring: "var(--color-badge-info)",
    };
  })();

  const albDisplay = alb ? albStatusDisplay(alb) : undefined;
  const albTooltip = albDisplay?.detail ?? albLabel;
  const albChip =
    albHref && albLabel ? (
      <Tooltip message={albTooltip}>
        <Link to={albHref} className="hover:opacity-80" data-e2e="compute-workload-alb-chip">
          {albDisplay ? (
            <Chip
              tone={albDisplay.tone}
              icon={alb?.phase === "error" ? TriangleAlertIcon : GlobeIcon}
              spinner={alb?.phase === "provisioning"}
            >
              {albDisplay.label}
            </Chip>
          ) : (
            <Chip tone="muted" icon={GlobeIcon}>
              View ALB
            </Chip>
          )}
        </Link>
      </Tooltip>
    ) : (
      <Chip tone="muted" icon={GlobeIcon}>
        No load balancer
      </Chip>
    );

  // Only claim reachability once both the workload and its ALB are serving.
  const hostnamePrefix =
    alb?.phase === "provisioning"
      ? "Publishing at"
      : (health === "Available" || health === "Degraded") && alb?.phase !== "error"
        ? "Reachable at"
        : "Published at";
  // Only standing states get the extra line; a slow ALB says so in its chip
  // and tooltip, so the strip keeps its height while things come up.
  const note =
    headline.note ?? (alb?.phase === "error" ? `Load balancer: ${alb.message}` : undefined);

  return (
    <Card size="sm" data-testid="compute-plugin-workload-health">
      <CardContent className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-center gap-3">
          <span
            className="flex size-9 shrink-0 items-center justify-center rounded-full"
            // Inline: the host does not compile `bg-(--color-badge-*)/10` for every tone.
            style={{
              background: `color-mix(in oklab, ${headline.ring} 10%, transparent)`,
            }}
          >
            {headline.icon}
          </span>
          <div className="flex min-w-0 flex-col">
            <span className="truncate text-sm font-semibold" title={headline.tooltip}>
              {headline.title}
            </span>
            <span className="text-muted-foreground flex min-w-0 items-baseline gap-1.5 text-xs">
              {albHostname ? (
                <>
                  <PublishedHostname
                    prefix={hostnamePrefix}
                    hostname={albHostname}
                    customHostnames={customHostnames}
                  />
                  <span className="shrink-0">·</span>
                </>
              ) : null}
              <span className="shrink-0">{headline.detail}</span>
            </span>
            {note ? (
              <span
                className="text-muted-foreground text-xs"
                data-testid="compute-plugin-workload-health-note"
              >
                {note}
              </span>
            ) : null}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:justify-end">
          <Chip tone={tone} icon={deploy ? ClockIcon : CircleCheckIcon}>
            {deploy?.label ?? health}
          </Chip>
          {albChip}
        </div>
      </CardContent>
    </Card>
  );
}
