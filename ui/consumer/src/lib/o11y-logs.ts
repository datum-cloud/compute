/**
 * Project-scoped logs via telemetry queryapi's Loki-shaped route.
 *
 * Mirrors cloud-portal `app/resources/o11y-logs` without importing portal
 * internals. ALB identity is the HTTPProxy name (`route_name` regexp). Instance
 * stdout is pinned on `datum_instance_name`. LogQL log queries take one stream
 * selector — `{a="x"} or {b="x"}` is a metric-query form and returns 400.
 * Search and host filters stay client-side because Envoy OTEL access logs keep
 * an empty Body.
 */
import { hostRouteIP } from "../adapter";
import { ApiError, PLUGIN_ID, getProjectScopedBase } from "./api";
import { useMemo } from "react";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  buildLogQL,
  facetsFromEntries,
  filterEntries,
  flattenLokiStreams,
  lastThirtyMinutes,
  logRequestHost,
  resolveLogTimeRange,
  type LogEntry,
  type LogFacet,
  type LogFilters,
  type LogTimeRange,
  type LokiQueryRangeResponse,
} from "@datum-cloud/datum-ui/logs";

export const O11Y_LOGS_QUERY_RANGE_PATH =
  "/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1/query_range";

export const ALB_LOGS_PAGE_LIMIT = 500;
export const ALB_LOGS_PREVIEW_LIMIT = 20;
export const ALB_LOGS_LIVE_POLL_MS = 5_000;

const ALB_LOG_LABELS = [
  "method",
  "path",
  "response_code",
  "duration",
  "authority",
  "requested_server_name",
  "x_forwarded_host",
  "referer",
  "request_id",
  "protocol",
  "user_agent",
  "upstream_host",
  "response_flags",
] as const;

const ALB_LOG_LABEL_SET = new Set<string>(ALB_LOG_LABELS);

const CLIENT_HOST_FILTERS = [
  "host",
  "authority",
  "requested_server_name",
  "x_forwarded_host",
  "resource_name",
] as const;

export const ALB_LOG_FACET_NAMES = ["method", "response_code", "host"] as const;

export const ALB_LOG_FACET_LABELS: Record<
  (typeof ALB_LOG_FACET_NAMES)[number],
  string
> = {
  method: "Method",
  response_code: "Status code",
  host: "Host",
};

export const LOG_SOURCE_ALB = "ALB";
export const LOG_SOURCE_INSTANCE = "Instance";
export const LOG_SOURCE_LABEL = "source";

/** Stream label that uniquely identifies an instance's stdout in queryapi. */
export const COMPUTE_LOG_INSTANCE_LABEL = "datum_instance_name";

const COMPUTE_LOG_LABELS = [
  "severity",
  "service_name",
  "resource_name",
  "k8s_container_name",
  "container",
] as const;
const COMPUTE_LOG_LABEL_SET = new Set<string>(COMPUTE_LOG_LABELS);

const LOKI_LABEL_ESCAPE = /[.*+?^${}()|[\]\\]/g;

function escapeLogQLQuoted(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

function escapeLogQLRegexp(value: string): string {
  return value.replace(LOKI_LABEL_ESCAPE, "\\$&");
}

/** Envoy `%ROUTE_NAME%` is `httproute/<namespace>/<httpProxyName>/rule/...`. */
export function albRouteNameRegexp(proxyId: string): string {
  return `httproute/[^/]+/${escapeLogQLRegexp(proxyId)}/.*`;
}

export function albLogMatchers(
  _proxyId: string,
  extraFilters: LogFilters = {},
): LogFilters {
  const {
    resource_name: _ignoredResource,
    route_name: _ignoredRoute,
    host: _ignoredHost,
    authority: _ignoredAuthority,
    requested_server_name: _ignoredSni,
    x_forwarded_host: _ignoredXfh,
    ...rest
  } = extraFilters;
  return rest;
}

/** Host-route IPs an ALB may dial for this instance. */
export function instanceUpstreamIPs(input: {
  internalIP?: string;
  internalIPs?: readonly string[];
}): string[] {
  const seen = new Set<string>();
  const ips: string[] = [];
  for (const raw of [...(input.internalIPs ?? []), input.internalIP]) {
    const ip = hostRouteIP(raw);
    if (!ip || seen.has(ip)) continue;
    seen.add(ip);
    ips.push(ip);
  }
  return ips;
}

/** Envoy `%UPSTREAM_HOST%` host (no port). Empty, `-`, and unix sockets are unset. */
export function parseUpstreamHostIP(
  upstreamHost: string | undefined,
): string | undefined {
  const raw = upstreamHost?.trim();
  if (!raw || raw === "-" || raw.startsWith("unix:")) return undefined;
  if (raw.startsWith("[")) {
    const end = raw.indexOf("]");
    if (end <= 1) return undefined;
    return raw.slice(1, end);
  }
  const lastColon = raw.lastIndexOf(":");
  if (lastColon <= 0) return undefined;
  return raw.slice(0, lastColon);
}

/** True when `labels.upstream_host` names one of `ips`. */
export function albUpstreamHostMatches(
  upstreamHost: string | undefined,
  ips: readonly string[],
): boolean {
  const host = parseUpstreamHostIP(upstreamHost);
  if (!host || ips.length === 0) return false;
  return ips.includes(host);
}

export function buildAlbUpstreamHostRegexp(ips: readonly string[]): string {
  return ips
    .flatMap((ip) => {
      const escaped = escapeLogQLRegexp(ip);
      if (ip.includes(":")) {
        return [`\\[${escaped}\\]:[0-9]+`, `${escaped}:[0-9]+`];
      }
      return [`${escaped}:[0-9]+`];
    })
    .join("|");
}

export function filterAlbLogsByUpstreamHost(
  entries: readonly LogEntry[],
  ips: readonly string[],
): LogEntry[] {
  return entries.filter((entry) => {
    if (entry.labels[LOG_SOURCE_LABEL] === LOG_SOURCE_INSTANCE) return true;
    return albUpstreamHostMatches(entry.labels.upstream_host, ips);
  });
}

export function buildAlbLogQL(
  proxyId: string,
  extraFilters?: LogFilters,
  upstreamIPs?: readonly string[],
): string {
  const extras = albLogMatchers(proxyId, extraFilters);
  const pin = `route_name=~"${escapeLogQLQuoted(albRouteNameRegexp(proxyId))}"`;
  const parts = [pin];
  if (upstreamIPs && upstreamIPs.length > 0) {
    parts.push(
      `upstream_host=~"${escapeLogQLQuoted(buildAlbUpstreamHostRegexp(upstreamIPs))}"`,
    );
  }
  const prefix = parts.join(", ");
  const hasExtras = Object.values(extras).some((values) => values.length > 0);
  if (!hasExtras) return `{${prefix}}`;
  return `{${prefix}, ${buildLogQL({ matchers: extras }).slice(1)}`;
}

/** LogQL for one instance's stdout. One stream selector — not `or`. */
export function buildComputeLogQL(instanceName: string): string {
  return `{${COMPUTE_LOG_INSTANCE_LABEL}="${escapeLogQLQuoted(instanceName)}"}`;
}

/** Pin stdout for every replica with a single regexp matcher. */
export function buildComputeLogQLMany(instanceNames: readonly string[]): string {
  const unique = [...new Set(instanceNames.filter(Boolean))];
  if (unique.length === 0) return "";
  if (unique.length === 1) return buildComputeLogQL(unique[0]);
  const re = unique.map(escapeLogQLRegexp).join("|");
  return `{${COMPUTE_LOG_INSTANCE_LABEL}=~"${re}"}`;
}

function formatDurationLabel(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || /[a-z]/i.test(trimmed)) return trimmed;
  return `${trimmed}ms`;
}

export function pickAlbLogLabels(
  labels: Record<string, string>,
): Record<string, string> {
  const picked: Record<string, string> = {};
  for (const [key, value] of Object.entries(labels)) {
    if (!value || !ALB_LOG_LABEL_SET.has(key)) continue;
    picked[key] = key === "duration" ? formatDurationLabel(value) : value;
  }
  const host = logRequestHost(picked);
  if (!host) return picked;
  return { host, ...picked };
}

export function pickComputeLogLabels(
  labels: Record<string, string>,
): Record<string, string> {
  const picked: Record<string, string> = {};
  for (const [key, value] of Object.entries(labels)) {
    if (!value || !COMPUTE_LOG_LABEL_SET.has(key)) continue;
    picked[key] = value;
  }
  return picked;
}

function toAlbLogEntries(response: LokiQueryRangeResponse): LogEntry[] {
  return flattenLokiStreams(response).map((entry) => ({
    ...entry,
    labels: {
      ...pickAlbLogLabels(entry.labels),
      [LOG_SOURCE_LABEL]: LOG_SOURCE_ALB,
    },
  }));
}

function toComputeLogEntries(response: LokiQueryRangeResponse): LogEntry[] {
  return flattenLokiStreams(response)
    .filter((entry) => !entry.labels.route_name)
    .map((entry) => ({
      ...entry,
      labels: {
        ...pickComputeLogLabels(entry.labels),
        ...(entry.labels[COMPUTE_LOG_INSTANCE_LABEL]
          ? { [COMPUTE_LOG_INSTANCE_LABEL]: entry.labels[COMPUTE_LOG_INSTANCE_LABEL] }
          : {}),
        [LOG_SOURCE_LABEL]: LOG_SOURCE_INSTANCE,
      },
    }));
}

/** Newest-first merge of ALB access logs and instance stdout. */
export function mergeLogEntries(
  ...groups: Array<readonly LogEntry[] | undefined>
): LogEntry[] {
  return groups
    .flatMap((group) => group ?? [])
    .sort((a, b) => {
      if (a.timestampNs === b.timestampNs) return a.id.localeCompare(b.id);
      return a.timestampNs < b.timestampNs ? 1 : -1;
    });
}

export function albHostValues(labels: Record<string, string>): string[] {
  const seen = new Set<string>();
  const values: string[] = [];
  for (const raw of [
    labels.host,
    labels.requested_server_name,
    labels.x_forwarded_host,
    labels.authority,
  ]) {
    const host = raw?.trim();
    if (!host || seen.has(host)) continue;
    seen.add(host);
    values.push(host);
  }
  return values;
}

export function filterAlbLogsByHost(
  entries: readonly LogEntry[],
  filters: LogFilters = {},
): LogEntry[] {
  const selected = new Set<string>();
  for (const key of CLIENT_HOST_FILTERS) {
    for (const value of filters[key] ?? []) {
      if (value) selected.add(value);
    }
  }
  if (selected.size === 0) return [...entries];
  return entries.filter((entry) =>
    albHostValues(entry.labels).some((host) => selected.has(host)),
  );
}

/**
 * Host / HTTP facets apply to ALB rows only. Instance stdout stays visible
 * unless those facets (or an explicit Source=ALB filter) hide it.
 */
export function filterCombinedLogs(
  entries: readonly LogEntry[],
  filters: LogFilters = {},
): LogEntry[] {
  const httpPinned =
    (filters.method?.length ?? 0) > 0 ||
    (filters.response_code?.length ?? 0) > 0 ||
    CLIENT_HOST_FILTERS.some((key) => (filters[key]?.length ?? 0) > 0);

  let result = [...entries];
  if (httpPinned) {
    result = result.filter(
      (entry) => entry.labels[LOG_SOURCE_LABEL] === LOG_SOURCE_ALB,
    );
  }
  result = filterAlbLogsByHost(result, filters);
  const sources = filters[LOG_SOURCE_LABEL];
  if (sources?.length) {
    result = result.filter((entry) =>
      sources.includes(entry.labels[LOG_SOURCE_LABEL] ?? ""),
    );
  }
  return result;
}

function withAlbHostLabel(entry: LogEntry): LogEntry {
  const host = logRequestHost(entry.labels);
  if (!host || entry.labels.host === host) return entry;
  return { ...entry, labels: { ...entry.labels, host } };
}

export function albLogFacets(entries: readonly LogEntry[]): LogFacet[] {
  const hosted = entries.map(withAlbHostLabel);
  const byName = new Map(
    facetsFromEntries(hosted, ["method", "response_code"]).map((facet) => [
      facet.name,
      facet,
    ]),
  );
  const hostFacet = hostFacetFromEntries(hosted);
  return ALB_LOG_FACET_NAMES.flatMap((name) => {
    if (name === "host") return hostFacet ? [hostFacet] : [];
    const facet = byName.get(name);
    if (!facet) return [];
    return [{ ...facet, label: ALB_LOG_FACET_LABELS[name] }];
  });
}

export function combinedLogFacets(entries: readonly LogEntry[]): LogFacet[] {
  const source = facetsFromEntries(entries, [LOG_SOURCE_LABEL]).map(
    (facet) => ({
      ...facet,
      label: "Source",
    }),
  );
  const severity = facetsFromEntries(entries, ["severity"]);
  return [...source, ...severity, ...albLogFacets(entries)];
}

function hostFacetFromEntries(entries: readonly LogEntry[]): LogFacet | null {
  const counts = new Map<string, number>();
  for (const entry of entries) {
    for (const host of albHostValues(entry.labels)) {
      counts.set(host, (counts.get(host) ?? 0) + 1);
    }
  }
  if (counts.size === 0) return null;
  const options = [...counts.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([value, count]) => ({ value, count }));
  return { name: "host", label: ALB_LOG_FACET_LABELS.host, options };
}

async function queryRange(
  params: {
    projectId: string;
    query: string;
    start: string;
    end: string;
    limit: number;
  },
  toEntries: (response: LokiQueryRangeResponse) => LogEntry[],
): Promise<LogEntry[]> {
  const search = new URLSearchParams({
    query: params.query,
    start: params.start,
    end: params.end,
    limit: String(params.limit),
    direction: "backward",
  });
  const url = `${getProjectScopedBase(params.projectId)}${O11Y_LOGS_QUERY_RANGE_PATH}?${search.toString()}`;
  const res = await fetch(url, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    throw new ApiError(res.status, `Log query failed (${res.status})`);
  }
  const body = (await res.json()) as LokiQueryRangeResponse;
  if (body.status === "error") {
    throw new ApiError(400, body.error || "Log query failed");
  }
  return toEntries(body);
}

async function queryComputeRange(params: {
  projectId: string;
  query: string;
  start: string;
  end: string;
  limit: number;
}): Promise<LogEntry[]> {
  return queryRange(params, toComputeLogEntries);
}

export interface UseAlbLogsOptions {
  timeRange: LogTimeRange;
  filters?: LogFilters;
  search?: string;
  live?: boolean;
  limit?: number;
  enabled?: boolean;
  /** When set, pin ALB LogQL to these in-network IPs (`upstream_host`). */
  upstreamIPs?: readonly string[];
}

function logsRetry(failureCount: number, error: ApiError) {
  if (
    error instanceof ApiError &&
    (error.status === 401 || error.status === 403 || error.status === 400)
  ) {
    return false;
  }
  return failureCount < 2;
}

export function useAlbLogs(
  projectId: string | undefined,
  proxyId: string | undefined,
  options: UseAlbLogsOptions,
): UseQueryResult<LogEntry[], ApiError> {
  const {
    timeRange,
    filters,
    search,
    live = false,
    limit = ALB_LOGS_PAGE_LIMIT,
    enabled = true,
    upstreamIPs,
  } = options;

  const query = proxyId ? buildAlbLogQL(proxyId, filters, upstreamIPs) : "";
  const windowKey = live ? "live" : `${timeRange.from}/${timeRange.to}`;

  return useQuery({
    queryKey: [PLUGIN_ID, "o11y-logs", "alb", projectId, proxyId, query, windowKey, limit],
    enabled: enabled && !!projectId && !!proxyId,
    queryFn: () => {
      const range = live
        ? resolveLogTimeRange(timeRange.preset ? timeRange : lastThirtyMinutes())
        : timeRange;
      return queryRange(
        {
          projectId: projectId as string,
          query,
          start: range.from,
          end: range.to,
          limit,
        },
        toAlbLogEntries,
      );
    },
    select: (entries) => filterEntries(entries, {}, search),
    refetchInterval: live ? ALB_LOGS_LIVE_POLL_MS : false,
    staleTime: live ? 0 : 15_000,
    retry: logsRetry,
  });
}

function useComputeLogsQuery(
  projectId: string | undefined,
  query: string,
  options: UseAlbLogsOptions,
): UseQueryResult<LogEntry[], ApiError> {
  const { timeRange, live = false, limit = ALB_LOGS_PAGE_LIMIT, enabled = true } = options;
  const windowKey = live ? "live" : `${timeRange.from}/${timeRange.to}`;

  return useQuery({
    queryKey: [
      PLUGIN_ID,
      "o11y-logs",
      "compute",
      projectId,
      query,
      windowKey,
      limit,
    ],
    enabled: enabled && !!projectId && !!query,
    queryFn: () => {
      const range = live
        ? resolveLogTimeRange(timeRange.preset ? timeRange : lastThirtyMinutes())
        : timeRange;
      return queryComputeRange({
        projectId: projectId as string,
        query,
        start: range.from,
        end: range.to,
        limit,
      });
    },
    refetchInterval: live ? ALB_LOGS_LIVE_POLL_MS : false,
    staleTime: live ? 0 : 15_000,
    retry: logsRetry,
  });
}

function useComputeLogs(
  projectId: string | undefined,
  instanceName: string | undefined,
  options: UseAlbLogsOptions,
): UseQueryResult<LogEntry[], ApiError> {
  return useComputeLogsQuery(
    projectId,
    instanceName ? buildComputeLogQL(instanceName) : "",
    options,
  );
}

export interface UseInstanceLogsResult {
  data: LogEntry[];
  isLoading: boolean;
  error: ApiError | null;
}

/**
 * ALB access logs plus stdout from every named instance, merged newest-first.
 * Stdout is queried even when no HTTPProxy is attached.
 */
export function useWorkloadLogs(
  projectId: string | undefined,
  proxyId: string | undefined,
  instanceNames: readonly string[],
  options: UseAlbLogsOptions,
): UseInstanceLogsResult {
  const { search, enabled = true, ...rest } = options;
  const computeQuery = buildComputeLogQLMany(instanceNames);
  const albEnabled = enabled && !!proxyId;
  const computeEnabled = enabled && !!computeQuery;

  const alb = useAlbLogs(projectId, proxyId, { ...rest, search: undefined, enabled: albEnabled });
  const compute = useComputeLogsQuery(projectId, computeQuery, {
    ...rest,
    search: undefined,
    enabled: computeEnabled,
  });

  const merged = useMemo(
    () => mergeLogEntries(alb.data, compute.data),
    [alb.data, compute.data],
  );
  const data = useMemo(() => filterEntries(merged, {}, search), [merged, search]);
  const error =
    merged.length > 0
      ? null
      : ((compute.error as ApiError | null) ??
        (albEnabled ? (alb.error as ApiError | null) : null) ??
        null);

  return {
    data,
    isLoading:
      (albEnabled && alb.isLoading) || (computeEnabled && compute.isLoading),
    error,
  };
}

/**
 * ALB access logs plus instance stdout, merged newest-first. Stdout is queried
 * whenever the instance name is known — crash loops still write before a URL
 * is published.
 */
export function useInstanceLogs(
  projectId: string | undefined,
  proxyId: string | undefined,
  instanceName: string | undefined,
  options: UseAlbLogsOptions,
): UseInstanceLogsResult {
  const { search, enabled = true, upstreamIPs, ...rest } = options;
  const ips = useMemo(
    () => instanceUpstreamIPs({ internalIPs: upstreamIPs }),
    [upstreamIPs],
  );
  const albEnabled = enabled && !!proxyId && ips.length > 0;
  const computeEnabled = enabled && !!instanceName;

  const alb = useAlbLogs(projectId, proxyId, {
    ...rest,
    search: undefined,
    enabled: albEnabled,
    upstreamIPs: ips,
  });
  const compute = useComputeLogs(projectId, instanceName, {
    ...rest,
    search: undefined,
    enabled: computeEnabled,
  });

  const albRows = useMemo(
    () => filterAlbLogsByUpstreamHost(alb.data ?? [], ips),
    [alb.data, ips],
  );
  const merged = useMemo(
    () => mergeLogEntries(albRows, compute.data),
    [albRows, compute.data],
  );
  const data = useMemo(() => filterEntries(merged, {}, search), [merged, search]);
  const error =
    merged.length > 0
      ? null
      : ((compute.error as ApiError | null) ??
        (albEnabled ? (alb.error as ApiError | null) : null) ??
        null);

  return {
    data,
    isLoading:
      (albEnabled && alb.isLoading) || (computeEnabled && compute.isLoading),
    // Prefer stdout when ALB fails; only surface an error when nothing loaded.
    error,
  };
}
