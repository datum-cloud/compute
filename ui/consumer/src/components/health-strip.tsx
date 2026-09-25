import { Badge } from "@datum-cloud/datum-ui/badge";
import { Card, CardContent } from "@datum-cloud/datum-ui/card";
import { Icon } from "@datum-cloud/datum-ui/icons";
import { Tooltip } from "@datum-cloud/datum-ui/tooltip";
import {
  CircleCheckIcon,
  GlobeIcon,
  RadioIcon,
  TriangleAlertIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { workloadHealthToBadgeType, type WorkloadHealth } from "../schema";

function Chip({
  tone,
  icon,
  children,
}: {
  tone: "success" | "warning" | "danger" | "muted";
  icon: typeof GlobeIcon;
  children: ReactNode;
}) {
  return (
    <Badge
      type={tone}
      theme={tone === "muted" ? "solid" : "light"}
      className="h-6 gap-1.5 rounded-md px-2 text-xs font-medium whitespace-nowrap"
    >
      <Icon icon={icon} size={12} className="shrink-0" />
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
}) {
  const tone = workloadHealthToBadgeType(health);
  const headline = (() => {
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

  const albChip =
    albHref && albLabel ? (
      <Tooltip message={albLabel}>
        <Link to={albHref} className="hover:opacity-80">
          <Chip tone="muted" icon={GlobeIcon}>
            View ALB
          </Chip>
        </Link>
      </Tooltip>
    ) : (
      <Chip tone="muted" icon={GlobeIcon}>
        No load balancer
      </Chip>
    );

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
            <span className="truncate text-sm font-semibold">
              {headline.title}
            </span>
            <span className="text-muted-foreground flex min-w-0 items-baseline gap-1.5 text-xs">
              {albHostname ? (
                <>
                  <PublishedHostname
                    prefix={
                      health === "Available" || health === "Degraded"
                        ? "Reachable at"
                        : "Published at"
                    }
                    hostname={albHostname}
                    customHostnames={customHostnames}
                  />
                  <span className="shrink-0">·</span>
                </>
              ) : null}
              <span className="shrink-0">{headline.detail}</span>
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:justify-end">
          <Chip tone={tone} icon={CircleCheckIcon}>
            {health}
          </Chip>
          {albChip}
        </div>
      </CardContent>
    </Card>
  );
}
