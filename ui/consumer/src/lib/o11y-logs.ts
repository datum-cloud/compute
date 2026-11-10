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
import { useCallback, useMemo } from "react";
import {
  keepPreviousData,
  useQueries,
  useQuery,
  type Query,
  type UseQueryResult,
} from "@tanstack/react-query";
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

export const O11Y_LOGS_LABEL_VALUES_PATH =
  "/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1/label/{name}/values";

/** Instant queries; queryapi answers `sum by (...) (count_over_time(...))` here. */
export const O11Y_LOGS_QUERY_PATH = "/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1/query";

export const O11Y_LOGS_QUERY_RANGE_PATH =
  "/apis/o11y.miloapis.com/v1alpha1/logs/loki/api/v1/query_range";

export const ALB_LOGS_PAGE_LIMIT = 500;
export const ALB_LOGS_PREVIEW_LIMIT = 20;
export const ALB_LOGS_LIVE_POLL_MS = 5_000;

/**
 * How far back the recent-logs preview looks for its latest rows. A day
 * covers a workload that has been quiet for hours. Queries go backward with
 * a small limit, so a busy stream stops at its newest rows; only a quiet one
 * scans the whole day, and that's cheap for a pinned stream selector. A week
 * would multiply that scan on every live poll for little gain.
 */
export const RECENT_LOGS_LOOKBACK_MS = 24 * 60 * 60 * 1000;

/** datum-ui preset with the same span, so live polling slides the window forward. */
const RECENT_LOGS_PRESET = "last-24h";

/** The recent-logs window: the last `RECENT_LOGS_LOOKBACK_MS`, ending at `now`. */
export function recentLogsRange(now: number = Date.now()): LogTimeRange {
  return {
    from: new Date(now - RECENT_LOGS_LOOKBACK_MS).toISOString(),
    to: new Date(now).toISOString(),
    preset: RECENT_LOGS_PRESET,
  };
}

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

/** Labels the Host column reads, in the order it prefers them. */
const ALB_HOST_SOURCE_LABELS = ["host", "requested_server_name", "x_forwarded_host", "authority"] as const;

/**
 * Host labels whose values list Host's options. A row's `x_forwarded_host` or
 * `authority` is rarely its host, and Host's counts add any that are.
 */
const ALB_HOST_OPTION_LABELS = ["host", "requested_server_name"] as const;

const CLIENT_HOST_FILTERS = [
  "host",
  "authority",
  "requested_server_name",
  "x_forwarded_host",
  "resource_name",
] as const;

export const ALB_LOG_FACET_NAMES = ["method", "response_code", "host"] as const;

/** ALB facets that are stream labels, so LogQL can filter on them. */
const ALB_SERVER_FILTER_LABELS = ["method", "response_code"] as const;

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
  // Only real ALB stream labels go to the server. Host is resolved from
  // several labels in the browser, and Source and Severity don't exist on
  // access logs, so sending them would match no ALB rows at all.
  const matchers: LogFilters = {};
  for (const label of ALB_SERVER_FILTER_LABELS) {
    const values = extraFilters[label];
    if (values?.length) matchers[label] = values;
  }
  return matchers;
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

/**
 * The row's host, as the Host column shows it: the first host label set, so
 * the filter and facet match what each row displays. `authority` comes last
 * because the proxy rewrites it to the backend's name (e.g. httpbin.org).
 * An array so callers can treat "no host" as empty.
 */
export function albHostValues(labels: Record<string, string>): string[] {
  for (const label of ALB_HOST_SOURCE_LABELS) {
    const host = labels[label]?.trim();
    // Envoy writes "-" for a header it didn't get; that's not a host.
    if (host && host !== "-") return [host];
  }
  return [];
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

/** True when any facet that only ALB rows carry is ticked. */
function hasHttpFilter(filters: LogFilters): boolean {
  return (
    (filters.method?.length ?? 0) > 0 ||
    (filters.response_code?.length ?? 0) > 0 ||
    CLIENT_HOST_FILTERS.some((key) => (filters[key]?.length ?? 0) > 0)
  );
}

/**
 * Which sources can still show rows under `filters`, so a hook can skip the
 * other query. Source picks them directly; HTTP facets only match ALB rows,
 * and Severity only matches instance stdout.
 */
export function logSourcesWanted(filters: LogFilters = {}): {
  alb: boolean;
  instance: boolean;
} {
  const sources = filters[LOG_SOURCE_LABEL] ?? [];
  const picked = (source: string) => sources.length === 0 || sources.includes(source);
  return {
    alb: picked(LOG_SOURCE_ALB) && (filters.severity?.length ?? 0) === 0,
    instance: picked(LOG_SOURCE_INSTANCE) && !hasHttpFilter(filters),
  };
}

/**
 * Host / HTTP facets apply to ALB rows only, and Severity to instance stdout
 * only, so ticking one hides the other source. Source filters by the label
 * each row is tagged with when it's fetched.
 */
export function filterCombinedLogs(
  entries: readonly LogEntry[],
  filters: LogFilters = {},
): LogEntry[] {
  let result = [...entries];
  if (hasHttpFilter(filters)) {
    result = result.filter(
      (entry) => entry.labels[LOG_SOURCE_LABEL] === LOG_SOURCE_ALB,
    );
  }
  result = filterAlbLogsByHost(result, filters);
  const severities = filters.severity;
  if (severities?.length) {
    result = result.filter((entry) =>
      severities.includes(entry.labels.severity ?? ""),
    );
  }
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

/** Every logs key is `[PLUGIN_ID, "o11y-logs", kind, projectId, scope, ...]`. */
const KEY_PROJECT_INDEX = 3;

/**
 * `keepPreviousData`, but only within one project and scope (load balancer,
 * instance set or scope query). A filter, search or range change keeps the
 * old rows and options on screen until the new ones arrive, while switching
 * workload or instance never shows another one's logs.
 */
export function keepPreviousInScope<T>(projectId: string | undefined, scope: string | undefined) {
  return (previous: T | undefined, previousQuery?: Query<T, ApiError, T, readonly unknown[]>) => {
    const key = previousQuery?.queryKey;
    return key?.[KEY_PROJECT_INDEX] === projectId && key?.[KEY_PROJECT_INDEX + 1] === scope
      ? keepPreviousData(previous)
      : undefined;
  };
}

/**
 * Facet options don't depend on exact timestamps, so a preset window keys by
 * its name: a filter click re-resolves "last 30m" to now, and keying by the
 * timestamps would refetch every facet on every click.
 */
export function facetWindowKey(timeRange: LogTimeRange, live: boolean): string {
  if (live) return `live:${timeRange.preset ?? "last-30m"}`;
  return timeRange.preset ? `preset:${timeRange.preset}` : `${timeRange.from}/${timeRange.to}`;
}

function windowFor(timeRange: LogTimeRange, live: boolean): LogTimeRange {
  if (live) return resolveLogTimeRange(timeRange.preset ? timeRange : lastThirtyMinutes());
  return timeRange.preset ? resolveLogTimeRange(timeRange) : timeRange;
}

async function labelValues(params: {
  projectId: string;
  name: string;
  query: string;
  start: string;
  end: string;
}): Promise<string[]> {
  const search = new URLSearchParams({ query: params.query, start: params.start, end: params.end });
  const path = O11Y_LOGS_LABEL_VALUES_PATH.replace("{name}", encodeURIComponent(params.name));
  const res = await fetch(`${getProjectScopedBase(params.projectId)}${path}?${search.toString()}`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw new ApiError(res.status, `Label values failed (${res.status})`);
  const body = (await res.json()) as { status?: string; data?: string[]; error?: string };
  if (body.status === "error") throw new ApiError(400, body.error || "Label values failed");
  return body.data ?? [];
}

async function countByLabels(params: {
  projectId: string;
  query: string;
  time: string;
}): Promise<LogLabelCount[]> {
  const search = new URLSearchParams({ query: params.query, time: params.time });
  const res = await fetch(`${getProjectScopedBase(params.projectId)}${O11Y_LOGS_QUERY_PATH}?${search.toString()}`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw new ApiError(res.status, `Log counts failed (${res.status})`);
  const body = (await res.json()) as {
    status?: string;
    data?: { result?: Array<{ metric?: Record<string, string>; value?: [number, string] }> };
    error?: string;
  };
  if (body.status === "error") throw new ApiError(400, body.error || "Log counts failed");
  return (body.data?.result ?? []).flatMap((sample) => {
    const count = Number(sample.value?.[1]);
    if (!Number.isFinite(count) || count <= 0) return [];
    return [{ labels: { ...sample.metric }, count }];
  });
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
    placeholderData: keepPreviousInScope<LogEntry[]>(projectId, proxyId),
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
    placeholderData: keepPreviousInScope<LogEntry[]>(projectId, query),
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
  /** First load only: earlier rows stay on screen for later requests. */
  isLoading: boolean;
  /** A request is in flight, including while earlier rows are still showing. */
  isFetching: boolean;
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
  const wanted = logSourcesWanted(rest.filters);
  const albEnabled = enabled && wanted.alb && !!proxyId;
  const computeEnabled = enabled && wanted.instance && !!computeQuery;

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
    isFetching:
      (albEnabled && alb.isFetching) || (computeEnabled && compute.isFetching),
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
  const wanted = logSourcesWanted(rest.filters);
  const albEnabled = enabled && wanted.alb && !!proxyId && ips.length > 0;
  const computeEnabled = enabled && wanted.instance && !!instanceName;

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
    isFetching:
      (albEnabled && alb.isFetching) || (computeEnabled && compute.isFetching),
    // Prefer stdout when ALB fails; only surface an error when nothing loaded.
    error,
  };
}

/** Labels the Host facet merges, in the order `albHostValues` reads them. */

const SEVERITY_ORDER = ["ERROR", "FATAL", "WARN", "WARNING", "INFO", "DEBUG", "TRACE"];

function severityRank(value: string): number {
  const index = SEVERITY_ORDER.indexOf(value.toUpperCase());
  return index === -1 ? SEVERITY_ORDER.length : index;
}

function uniqueSorted(values: Iterable<string>, compare?: (a: string, b: string) => number): string[] {
  const seen = new Set<string>();
  for (const raw of values) {
    const value = raw.trim();
    // Envoy writes "-" for a header it didn't get; that's not a value.
    if (value && value !== "-") seen.add(value);
  }
  return [...seen].sort(compare ?? ((a, b) => a.localeCompare(b, undefined, { numeric: true })));
}

interface FacetRequest {
  name: string;
  query: string;
}

/** One group from a `sum by (...) (count_over_time(...))` vector. */
export interface LogLabelCount {
  labels: Record<string, string>;
  count: number;
}

export interface WindowFacetsConfig {
  /** ALB access logs are in scope (proxy attached, and for an instance, its upstream IPs known). */
  alb: boolean;
  /** Instance stdout is in scope. */
  instances: boolean;
  /** Offered as the Instance facet on the workload page; omit on an instance page. */
  instanceNames?: readonly string[];
}

/** Lines per option value, keyed by facet name. A facet left out shows no counts. */
export type WindowFacetCounts = Partial<Record<string, ReadonlyMap<string, number>>>;

/**
 * Builds the explorer's facets from `/label/{name}/values` for the whole
 * window: Source, Severity, Method, Status code, Host and (workload) Instance.
 * A facet in `counts` gives every option a count (0 when nothing matches the
 * other filters). Exported for tests.
 */
export function computeFacetsFromLabelValues(
  config: WindowFacetsConfig,
  values: Readonly<Record<string, readonly string[] | undefined>>,
  counts: WindowFacetCounts = {},
): LogFacet[] {
  const facets: LogFacet[] = [];
  const push = (name: string, label: string, options: readonly string[]) => {
    const counted = counts[name];
    facets.push({
      name,
      label,
      options: options.map((value) => (counted ? { value, count: counted.get(value) ?? 0 } : { value })),
    });
  };
  // A counted value can land between the two requests; list it rather than drop it.
  const withCounted = (name: string, listed: Iterable<string>) => [...listed, ...(counts[name]?.keys() ?? [])];

  const sources = [
    ...(config.alb ? [LOG_SOURCE_ALB] : []),
    ...(config.instances ? [LOG_SOURCE_INSTANCE] : []),
  ];
  if (sources.length > 0) push(LOG_SOURCE_LABEL, "Source", sources);
  const severity = uniqueSorted(
    withCounted("severity", values.severity ?? []),
    (a, b) => severityRank(a) - severityRank(b) || a.localeCompare(b),
  );
  if (severity.length > 0) push("severity", "Severity", severity);
  for (const name of ["method", "response_code"] as const) {
    const options = uniqueSorted(withCounted(name, values[name] ?? []));
    if (options.length > 0) push(name, ALB_LOG_FACET_LABELS[name], options);
  }
  // With counts, Host's resolved hosts are exact, so list just those and
  // `host` itself; without, SNI fills in for rows with no Host header.
  const hostLabels = counts.host ? ["host"] : ALB_HOST_OPTION_LABELS;
  const hosts = uniqueSorted(withCounted("host", hostLabels.flatMap((label) => values[label] ?? [])));
  if (hosts.length > 0) push("host", ALB_LOG_FACET_LABELS.host, hosts);
  const instances = uniqueSorted(config.instanceNames ?? []);
  if (instances.length > 0) push(COMPUTE_LOG_INSTANCE_LABEL, "Instance", instances);
  return facets;
}

/**
 * The count queries behind the facet counts. Each counts lines in the window
 * per value of its facet, applying every other ticked facet but not its own,
 * so ticking GET still shows how many POSTs there are. Fixed slots so each
 * can be its own useQuery.
 */
export const WINDOW_COUNT_SLOTS = [
  "albMethod",
  "albStatus",
  "albHost",
  "stdoutSeverity",
  "stdoutInstance",
  "stdoutTotal",
] as const;
export type WindowCountSlot = (typeof WINDOW_COUNT_SLOTS)[number];

export interface WindowCountScope {
  /** Set when ALB access logs are in scope. */
  proxyId?: string;
  upstreamIPs?: readonly string[];
  /** Instances whose stdout is in scope. */
  instanceNames: readonly string[];
  /** The Instance facet is shown (workload page). */
  instanceFacet?: boolean;
}

const active = (filters: LogFilters, name: string) => (filters[name]?.length ?? 0) > 0;

/**
 * Which rows can show under `filters`, mirroring `filterCombinedLogs` and the
 * workload Instance filter: HTTP facets match ALB rows only, Severity and
 * Instance match stdout only, and Source picks one.
 */
function windowRowState(scope: WindowCountScope, filters: LogFilters) {
  const sources = filters[LOG_SOURCE_LABEL] ?? [];
  const sourceAllows = (source: string) => sources.length === 0 || sources.includes(source);
  const hostFiltered = CLIENT_HOST_FILTERS.some((key) => active(filters, key));
  const httpFiltered = hostFiltered || active(filters, "method") || active(filters, "response_code");
  const stdoutFiltered = active(filters, "severity") || active(filters, COMPUTE_LOG_INSTANCE_LABEL);
  const alb = !!scope.proxyId;
  const stdout = scope.instanceNames.length > 0;
  // `…Rows`: rows could show, Source aside. `…Shown`: they do.
  const albRows = alb && !stdoutFiltered;
  const stdoutRows = stdout && !httpFiltered;
  return {
    hostFiltered,
    alb,
    stdout,
    albRows,
    stdoutRows,
    albShown: albRows && sourceAllows(LOG_SOURCE_ALB),
    stdoutShown: stdoutRows && sourceAllows(LOG_SOURCE_INSTANCE),
  };
}

function withLabelMatcher(selector: string, label: string, values: readonly string[] | undefined): string {
  if (!values?.length) return selector;
  const matcher =
    values.length === 1
      ? `${label}="${escapeLogQLQuoted(values[0])}"`
      : `${label}=~"${values.map(escapeLogQLRegexp).join("|")}"`;
  return `${selector.slice(0, -1)}, ${matcher}}`;
}

function countQuery(by: readonly string[], selector: string, seconds: number): string {
  const range = `[${Math.max(1, Math.ceil(seconds))}s]`;
  const inner = `count_over_time(${selector} ${range})`;
  return by.length > 0 ? `sum by (${by.join(", ")}) (${inner})` : `sum(${inner})`;
}

/**
 * The query for each count slot, or null when it isn't needed: the server
 * can't count it like the table (search is applied in the browser, and so is
 * Host), or no row it counts could show. Exported for tests.
 */
export function windowCountQueries(
  scope: WindowCountScope,
  filters: LogFilters,
  search: string,
  seconds: number,
): Record<WindowCountSlot, string | null> {
  const none: Record<WindowCountSlot, string | null> = {
    albMethod: null,
    albStatus: null,
    albHost: null,
    stdoutSeverity: null,
    stdoutInstance: null,
    stdoutTotal: null,
  };
  if (search.trim()) return none;
  const state = windowRowState(scope, filters);
  const albSelector = (without?: string) => {
    const rest = { ...filters };
    if (without) delete rest[without];
    return buildAlbLogQL(scope.proxyId as string, rest, scope.upstreamIPs);
  };
  const picked = filters[COMPUTE_LOG_INSTANCE_LABEL] ?? [];
  const stdoutSelector = (instances: readonly string[]) =>
    buildComputeLogQLMany(instances.length > 0 ? instances : scope.instanceNames);
  const severities = filters.severity;

  return {
    albMethod:
      state.albShown && !state.hostFiltered ? countQuery(["method"], albSelector("method"), seconds) : null,
    albStatus:
      state.albShown && !state.hostFiltered
        ? countQuery(["response_code"], albSelector("response_code"), seconds)
        : null,
    // Also gives the Source facet its ALB total, so it runs whatever Source says.
    albHost: state.albRows ? countQuery(ALB_HOST_SOURCE_LABELS, albSelector(), seconds) : null,
    stdoutSeverity: state.stdoutShown ? countQuery(["severity"], stdoutSelector(picked), seconds) : null,
    stdoutInstance:
      state.stdoutShown && scope.instanceFacet
        ? countQuery(
            [COMPUTE_LOG_INSTANCE_LABEL],
            withLabelMatcher(stdoutSelector([]), "severity", severities),
            seconds,
          )
        : null,
    stdoutTotal:
      state.stdoutRows && !state.hostFiltered
        ? countQuery([], withLabelMatcher(stdoutSelector(picked), "severity", severities), seconds)
        : null,
  };
}

function groupCounts(groups: readonly LogLabelCount[], valuesOf: (labels: Record<string, string>) => string[]) {
  const counts = new Map<string, number>();
  for (const { labels, count } of groups) {
    for (const raw of valuesOf(labels)) {
      const value = raw.trim();
      if (!value || value === "-") continue;
      counts.set(value, (counts.get(value) ?? 0) + count);
    }
  }
  return counts;
}

const total = (groups: readonly LogLabelCount[]) => groups.reduce((sum, group) => sum + group.count, 0);

/**
 * Facet counts from the count slots' results. A slot missing from `results`
 * failed, and the facets it feeds show no counts; a facet whose rows can't
 * show under the other filters gets zeros. Exported for tests.
 */
export function windowFacetCounts(
  scope: WindowCountScope,
  filters: LogFilters,
  search: string,
  results: Partial<Record<WindowCountSlot, readonly LogLabelCount[]>>,
): WindowFacetCounts {
  if (search.trim()) return {};
  const state = windowRowState(scope, filters);
  const counts: Record<string, Map<string, number>> = {};
  const set = (name: string, shown: boolean, groups: readonly LogLabelCount[] | undefined, valuesOf: (labels: Record<string, string>) => string[]) => {
    if (!shown) counts[name] = new Map();
    else if (groups) counts[name] = groupCounts(groups, valuesOf);
  };

  if (state.alb) {
    if (!state.hostFiltered) {
      set("method", state.albShown, results.albMethod, (labels) => [labels.method ?? ""]);
      set("response_code", state.albShown, results.albStatus, (labels) => [labels.response_code ?? ""]);
    }
    // Each group counts under the host its rows show, as the Host filter matches.
    set("host", state.albShown, results.albHost, albHostValues);
  }
  if (state.stdout) {
    set("severity", state.stdoutShown, results.stdoutSeverity, (labels) => [labels.severity ?? ""]);
    if (scope.instanceFacet) {
      set(COMPUTE_LOG_INSTANCE_LABEL, state.stdoutShown, results.stdoutInstance, (labels) => [
        labels[COMPUTE_LOG_INSTANCE_LABEL] ?? "",
      ]);
    }
  }
  if (!state.hostFiltered) {
    const source = new Map<string, number>();
    let complete = true;
    if (state.alb) {
      if (!state.albRows) source.set(LOG_SOURCE_ALB, 0);
      else if (results.albHost) source.set(LOG_SOURCE_ALB, total(results.albHost));
      else complete = false;
    }
    if (state.stdout) {
      if (!state.stdoutRows) source.set(LOG_SOURCE_INSTANCE, 0);
      else if (results.stdoutTotal) source.set(LOG_SOURCE_INSTANCE, total(results.stdoutTotal));
      else complete = false;
    }
    if (complete && source.size > 0) counts[LOG_SOURCE_LABEL] = source;
  }
  return counts;
}

/**
 * Facets for the whole window, independent of what is ticked: label values
 * scoped to the load balancer (and instance upstreams) for the HTTP facets,
 * and to the instances' stdout for severity, so ticking a value never removes
 * the other options. Counts come from `windowCountQueries`.
 *
 * Returns `null` until the options and counts have loaded, or if an options
 * request failed, so the caller can fall back to facets built from the
 * fetched rows. A failed count only leaves its facets without counts.
 */
export function useWindowLogFacets(
  projectId: string | undefined,
  options: {
    proxyId?: string;
    upstreamIPs?: readonly string[];
    instanceNames: readonly string[];
    /** Offer an Instance facet listing `instanceNames` (workload page). */
    instanceFacet?: boolean;
    filters?: LogFilters;
    search?: string;
    timeRange: LogTimeRange;
    live?: boolean;
    enabled?: boolean;
  },
): LogFacet[] | null {
  const {
    proxyId,
    upstreamIPs,
    instanceNames,
    instanceFacet = false,
    filters = EMPTY_FILTERS,
    search = "",
    timeRange,
    live = false,
    enabled = true,
  } = options;
  const albQuery = proxyId ? buildAlbLogQL(proxyId, undefined, upstreamIPs) : "";
  const computeQuery = buildComputeLogQLMany(instanceNames);
  const windowKey = facetWindowKey(timeRange, live);
  const isEnabled = enabled && !!projectId;

  const requests = useMemo<FacetRequest[]>(
    () => [
      ...(albQuery
        ? ["method", "response_code", ...ALB_HOST_OPTION_LABELS].map((name) => ({ name, query: albQuery }))
        : []),
      ...(computeQuery ? [{ name: "severity", query: computeQuery }] : []),
    ],
    [albQuery, computeQuery],
  );

  const combine = useCallback(
    (results: ReadonlyArray<{ isSuccess: boolean; data?: string[] }>): Record<string, string[]> | null => {
      if (!results.every((result) => result.isSuccess)) return null;
      const values: Record<string, string[]> = {};
      requests.forEach((request, index) => {
        values[request.name] = [...(values[request.name] ?? []), ...(results[index]?.data ?? [])];
      });
      return values;
    },
    [requests],
  );

  const values = useQueries({
    queries: requests.map((request) => ({
      queryKey: [PLUGIN_ID, "o11y-logs", "label-values", projectId, request.query, request.name, windowKey],
      queryFn: () => {
        const range = windowFor(timeRange, live);
        return labelValues({
          projectId: projectId as string,
          name: request.name,
          query: request.query,
          start: range.from,
          end: range.to,
        });
      },
      enabled: isEnabled,
      // Options change slowly; live mode refreshes them less often than rows.
      refetchInterval: live ? ALB_LOGS_LIVE_POLL_MS * 6 : (false as const),
      staleTime: 60_000,
      retry: logsRetry,
    })),
    combine,
  });

  const scope = useMemo<WindowCountScope>(
    () => ({ proxyId: albQuery ? proxyId : undefined, upstreamIPs, instanceNames, instanceFacet }),
    // Arrays may be rebuilt each render; their contents are what matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [albQuery, computeQuery, instanceFacet],
  );
  const window = windowFor(timeRange, live);
  const seconds = (Date.parse(window.to) - Date.parse(window.from)) / 1000;
  const queries = windowCountQueries(scope, filters, search, seconds);
  const countScope = `${albQuery}|${computeQuery}`;
  const countOptions = { projectId, countScope, windowKey, timeRange, live, enabled: isEnabled };

  // One useQuery per slot rather than more useQueries entries: a filter click
  // changes these keys, and only a useQuery keeps its previous counts on
  // screen while the new ones load (useQueries starts a fresh observer).
  const albMethod = useCountSlot(queries.albMethod, countOptions);
  const albStatus = useCountSlot(queries.albStatus, countOptions);
  const albHost = useCountSlot(queries.albHost, countOptions);
  const stdoutSeverity = useCountSlot(queries.stdoutSeverity, countOptions);
  const stdoutInstance = useCountSlot(queries.stdoutInstance, countOptions);
  const stdoutTotal = useCountSlot(queries.stdoutTotal, countOptions);

  return useMemo(() => {
    if (!values) return null;
    const slots = { albMethod, albStatus, albHost, stdoutSeverity, stdoutInstance, stdoutTotal };
    const results: Partial<Record<WindowCountSlot, readonly LogLabelCount[]>> = {};
    for (const slot of WINDOW_COUNT_SLOTS) {
      const count = slots[slot];
      if (!count.needed) continue;
      if (count.data) results[slot] = count.data;
      // First load: wait for counts rather than show options that gain
      // numbers a moment later. A failed count just leaves them off.
      else if (!count.isError) return null;
    }
    return computeFacetsFromLabelValues(
      {
        alb: !!scope.proxyId,
        instances: scope.instanceNames.length > 0,
        instanceNames: instanceFacet ? scope.instanceNames : undefined,
      },
      values,
      windowFacetCounts(scope, filters, search, results),
    );
  }, [values, albMethod, albStatus, albHost, stdoutSeverity, stdoutInstance, stdoutTotal, scope, instanceFacet, filters, search]);
}

const EMPTY_FILTERS: LogFilters = {};

function useCountSlot(
  query: string | null,
  options: {
    projectId: string | undefined;
    countScope: string;
    windowKey: string;
    timeRange: LogTimeRange;
    live: boolean;
    enabled: boolean;
  },
) {
  const { projectId, countScope, windowKey, timeRange, live, enabled } = options;
  const result = useQuery({
    queryKey: [PLUGIN_ID, "o11y-logs", "label-counts", projectId, countScope, query, windowKey],
    queryFn: () =>
      countByLabels({
        projectId: projectId as string,
        query: query as string,
        time: windowFor(timeRange, live).to,
      }),
    placeholderData: keepPreviousInScope<LogLabelCount[]>(projectId, countScope),
    enabled: enabled && !!query,
    refetchInterval: live ? ALB_LOGS_LIVE_POLL_MS * 6 : false,
    staleTime: 30_000,
    retry: logsRetry,
  });
  const needed = !!query;
  return useMemo(
    () => ({ needed, data: needed ? result.data : undefined, isError: result.isError }),
    [needed, result.data, result.isError],
  );
}
