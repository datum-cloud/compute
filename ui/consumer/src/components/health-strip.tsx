import { Badge } from "@datum-cloud/datum-ui/badge";
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
import type { WorkloadHealth } from "../schema";

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

/**
 * The workload's state as one open line, like the project home's health
 * line: an icon, what is happening, and the counts behind it, with how to
 * reach the workload on the right. A published workload shows its load
 * balancer there; one without passes `reach` (its private address).
 */
export function WorkloadHealthStrip({
  health,
  healthyCount,
  totalCount,
  locationCount,
  albHref,
  albLabel,
  deploy,
  failure,
  alb,
  reach,
  now = Date.now(),
}: {
  health: WorkloadHealth;
  healthyCount: number;
  totalCount: number;
  locationCount: number;
  albHref?: string;
  albLabel?: string;
  /** Set while the workload is coming up — see `deployStatus`. */
  deploy?: DeployStatus;
  /** Why instances are failing, shown in place of the generic unavailable copy. */
  failure?: string;
  /** Whether the attached load balancer is serving yet. */
  alb?: AlbStatus;
  /** Shown in place of the load balancer when none publishes the workload. */
  reach?: ReactNode;
  /** For the deploy's elapsed time. */
  now?: number;
}) {
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
          <SpinnerIcon size="sm" style={{ color: ring }} aria-hidden />
        ) : (
          <Icon icon={ClockIcon} size={16} style={{ color: ring }} />
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
            size={16}
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
            size={16}
            style={{ color: "var(--color-badge-success)" }}
          />
        ),
        title: "Serving normally",
        detail: `${healthyCount} of ${totalCount} ${totalCount === 1 ? "instance" : "instances"} ready · ${locationCount} ${locationCount === 1 ? "location" : "locations"}`,
        ring: "var(--color-badge-success)",
      };
    }
    if (health === "Degraded") {
      return {
        icon: (
          <Icon
            icon={TriangleAlertIcon}
            size={16}
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
            size={16}
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
          size={16}
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
              Configure ALB
            </Chip>
          )}
        </Link>
      </Tooltip>
    ) : (
      reach
    );

  // Only standing states get the extra line; a slow ALB says so in its chip
  // and tooltip, so the line keeps its height while things come up.
  const note =
    headline.note ?? (alb?.phase === "error" ? `Load balancer: ${alb.message}` : undefined);

  return (
    <div className="flex min-w-0 flex-col gap-1" data-testid="compute-plugin-workload-health">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className="flex shrink-0 items-center">{headline.icon}</span>
          <span className="truncate text-sm font-semibold" title={headline.tooltip}>
            {headline.title}
          </span>
          <span className="text-muted-foreground truncate text-sm">{headline.detail}</span>
        </div>
        {albChip ? <div className="ml-auto flex min-w-0 items-center gap-2">{albChip}</div> : null}
      </div>
      {note ? (
        <span
          className="text-muted-foreground pl-6 text-xs"
          data-testid="compute-plugin-workload-health-note"
        >
          {note}
        </span>
      ) : null}
    </div>
  );
}
