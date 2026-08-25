/**
 * Instance log surfaces built on `@datum-cloud/datum-ui/logs`.
 *
 * - {@link RecentInstanceLogs} — compact last-30-minutes table for Overview
 * - {@link InstanceLogsExplorer} — full explorer for the Logs tab
 *
 * No log backend yet — entries stay empty until a Loki query is wired.
 */
import { Card, CardContent } from "@datum-cloud/datum-ui/card";
import { Icon } from "@datum-cloud/datum-ui/icons";
import {
  lastThirtyMinutes,
  Logs,
  type LogFilters,
  type LogTimeRange,
} from "@datum-cloud/datum-ui/logs";
import { cn } from "@datum-cloud/datum-ui/utils";
import { ScrollTextIcon } from "lucide-react";
import { useCallback, useState } from "react";
import { Link } from "react-router";

const OVERVIEW_COLUMNS = ["time", "severity", "message"] as const;
const FULL_COLUMNS = ["time", "severity", "status", "path", "message"] as const;

/** Compact last-30-minutes log table for the instance Overview card. */
export function RecentInstanceLogs({
  logsHref,
  className,
}: {
  /** Link target for “View all” (Logs tab). */
  logsHref: string;
  className?: string;
}) {
  const [timeRange] = useState<LogTimeRange>(() => lastThirtyMinutes());

  return (
    <Card
      className={cn(
        "flex h-full w-full flex-col gap-0 overflow-hidden rounded-xl px-3 py-4 shadow sm:pt-6 sm:pb-4",
        className,
      )}
      data-testid="compute-plugin-instance-logs"
    >
      <CardContent className="flex min-h-0 flex-1 flex-col gap-3 p-0 sm:px-6 sm:pb-4">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <Icon
              icon={ScrollTextIcon}
              size={20}
              className="text-muted-foreground"
            />
            <span className="text-base font-semibold">Recent Logs</span>
            <span className="text-muted-foreground text-xs">Last 30 min</span>
          </div>
          <Link
            to={logsHref}
            className="text-muted-foreground hover:text-foreground text-xs transition-colors"
          >
            View all →
          </Link>
        </div>

        <Logs.Root
          entries={[]}
          facets={[{ name: "severity", label: "Severity", options: [] }]}
          timeRange={timeRange}
          columns={[...OVERVIEW_COLUMNS]}
          className="border-border flex min-h-48 flex-1 flex-col overflow-hidden rounded-lg border lg:min-h-72"
        >
          <Logs.Table />
        </Logs.Root>
      </CardContent>
    </Card>
  );
}

/** Full log explorer for the instance Logs tab. */
export function InstanceLogsExplorer({ className }: { className?: string }) {
  const [filters, setFilters] = useState<LogFilters>({});
  const [search, setSearch] = useState("");
  const [live, setLive] = useState(false);
  const [timeRange, setTimeRange] = useState<LogTimeRange>(() =>
    lastThirtyMinutes(),
  );

  const handleRefresh = useCallback(() => {
    setTimeRange(lastThirtyMinutes());
  }, []);

  return (
    <div
      className={cn(
        "border-border bg-card flex min-h-[32rem] flex-col overflow-hidden rounded-xl border",
        className,
      )}
      data-testid="compute-plugin-instance-logs-explorer"
    >
      <Logs.Root
        entries={[]}
        facets={[{ name: "severity", label: "Severity", options: [] }]}
        timeRange={timeRange}
        filters={filters}
        search={search}
        live={live}
        columns={[...FULL_COLUMNS]}
        onTimeRangeChange={setTimeRange}
        onFiltersChange={setFilters}
        onSearchChange={setSearch}
        onLiveChange={setLive}
        onRefresh={handleRefresh}
        className="flex min-h-0 flex-1 bg-card flex-col"
      >
        <Logs.Explorer />
      </Logs.Root>
    </div>
  );
}
