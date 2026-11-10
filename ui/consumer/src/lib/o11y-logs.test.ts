import { describe, expect, test } from "vitest";
import {
  albUpstreamHostMatches,
  buildAlbLogQL,
  buildAlbUpstreamHostRegexp,
  computeFacetsFromLabelValues,
  COMPUTE_LOG_INSTANCE_LABEL,
  facetWindowKey,
  windowCountQueries,
  windowFacetCounts,
  filterCombinedLogs,
  logSourcesWanted,
  keepPreviousInScope,
  filterAlbLogsByUpstreamHost,
  instanceUpstreamIPs,
  LOG_SOURCE_ALB,
  LOG_SOURCE_INSTANCE,
  LOG_SOURCE_LABEL,
  RECENT_LOGS_LOOKBACK_MS,
  recentLogsRange,
} from "./o11y-logs";
import { resolveLogTimeRange } from "@datum-cloud/datum-ui/logs";
import type { LogEntry } from "@datum-cloud/datum-ui/logs";

function entry(
  source: string,
  labels: Record<string, string> = {},
): LogEntry {
  return {
    id: `${source}-${labels.upstream_host ?? labels.line ?? "row"}`,
    timestamp: new Date(0),
    timestampNs: "0",
    line: "",
    labels: { [LOG_SOURCE_LABEL]: source, ...labels },
  };
}

describe("instanceUpstreamIPs", () => {
  test("unique-merges internalIPs and internalIP", () => {
    expect(
      instanceUpstreamIPs({
        internalIP: "10.0.1.4",
        internalIPs: ["10.0.1.4", "2001:db8::1"],
      }),
    ).toEqual(["10.0.1.4", "2001:db8::1"]);
  });

  test("strips host-route CIDR and skips delegated prefixes", () => {
    expect(
      instanceUpstreamIPs({
        internalIPs: ["10.0.1.4/32", "2001:db8:a001::/96", "2001:db8::1/128"],
      }),
    ).toEqual(["10.0.1.4", "2001:db8::1"]);
  });
});

describe("albUpstreamHostMatches", () => {
  const cases: Array<{
    name: string;
    host: string | undefined;
    ips: string[];
    want: boolean;
  }> = [
    { name: "IPv4 with port", host: "10.0.1.4:8080", ips: ["10.0.1.4"], want: true },
    { name: "IPv4 wrong host", host: "10.0.1.5:8080", ips: ["10.0.1.4"], want: false },
    {
      name: "IPv6 bracketed",
      host: "[2001:db8::1]:8080",
      ips: ["2001:db8::1"],
      want: true,
    },
    {
      name: "IPv6 unbracketed",
      host: "2001:db8::1:8080",
      ips: ["2001:db8::1"],
      want: true,
    },
    { name: "empty", host: "", ips: ["10.0.1.4"], want: false },
    { name: "dash", host: "-", ips: ["10.0.1.4"], want: false },
    { name: "missing", host: undefined, ips: ["10.0.1.4"], want: false },
    { name: "unix socket", host: "unix:///tmp/envoy.sock", ips: ["10.0.1.4"], want: false },
  ];

  for (const tc of cases) {
    test(tc.name, () => {
      expect(albUpstreamHostMatches(tc.host, tc.ips)).toBe(tc.want);
    });
  }
});

describe("buildAlbUpstreamHostRegexp", () => {
  test("escapes IPv4 dots", () => {
    expect(buildAlbUpstreamHostRegexp(["10.0.1.4"])).toBe("10\\.0\\.1\\.4:[0-9]+");
  });

  test("IPv6 has bracketed and unbracketed arms", () => {
    expect(buildAlbUpstreamHostRegexp(["2001:db8::1"])).toBe(
      "\\[2001:db8::1\\]:[0-9]+|2001:db8::1:[0-9]+",
    );
  });
});

describe("buildAlbLogQL", () => {
  test("pins upstream_host when IPs are provided", () => {
    expect(buildAlbLogQL("my-proxy", undefined, ["10.0.1.4"])).toBe(
      '{route_name=~"httproute/[^/]+/my-proxy/.*", upstream_host=~"10\\\\.0\\\\.1\\\\.4:[0-9]+"}',
    );
  });

  test("omits upstream_host when no IPs (workload path)", () => {
    expect(buildAlbLogQL("my-proxy")).toBe(
      '{route_name=~"httproute/[^/]+/my-proxy/.*"}',
    );
  });

  test("sends only ALB stream labels, never Source, Severity or Host", () => {
    expect(
      buildAlbLogQL("my-proxy", {
        [LOG_SOURCE_LABEL]: [LOG_SOURCE_ALB],
        severity: ["ERROR"],
        host: ["a.example.com"],
        method: ["GET"],
      }),
    ).toBe('{route_name=~"httproute/[^/]+/my-proxy/.*", method="GET"}');
  });
});

describe("logSourcesWanted", () => {
  test("queries both sources with no filters", () => {
    expect(logSourcesWanted({})).toEqual({ alb: true, instance: true });
  });

  test("follows the Source facet", () => {
    expect(logSourcesWanted({ [LOG_SOURCE_LABEL]: [LOG_SOURCE_ALB] })).toEqual({ alb: true, instance: false });
    expect(logSourcesWanted({ [LOG_SOURCE_LABEL]: [LOG_SOURCE_INSTANCE] })).toEqual({ alb: false, instance: true });
  });

  test("HTTP facets need ALB rows and Severity needs stdout", () => {
    expect(logSourcesWanted({ method: ["GET"] })).toEqual({ alb: true, instance: false });
    expect(logSourcesWanted({ host: ["a.example.com"] })).toEqual({ alb: true, instance: false });
    expect(logSourcesWanted({ severity: ["ERROR"] })).toEqual({ alb: false, instance: true });
  });
});

describe("filterCombinedLogs", () => {
  const rows = [
    entry(LOG_SOURCE_ALB, { line: "alb", method: "GET" }),
    entry(LOG_SOURCE_INSTANCE, { line: "err", severity: "ERROR" }),
    entry(LOG_SOURCE_INSTANCE, { line: "info", severity: "INFO" }),
  ];
  const lines = (filtered: LogEntry[]) => filtered.map((row) => row.labels.line);

  test("Source keeps only that source", () => {
    expect(lines(filterCombinedLogs(rows, { [LOG_SOURCE_LABEL]: [LOG_SOURCE_ALB] }))).toEqual(["alb"]);
  });

  test("Severity filters stdout and hides ALB rows", () => {
    expect(lines(filterCombinedLogs(rows, { severity: ["ERROR"] }))).toEqual(["err"]);
  });
});

describe("filterAlbLogsByUpstreamHost", () => {
  test("filters ALB rows and keeps stdout", () => {
    const rows = [
      entry(LOG_SOURCE_ALB, { upstream_host: "10.0.1.4:8080" }),
      entry(LOG_SOURCE_ALB, { upstream_host: "10.0.1.5:8080" }),
      entry(LOG_SOURCE_ALB, { upstream_host: "-" }),
      entry(LOG_SOURCE_INSTANCE, { line: "ready" }),
    ];
    const kept = filterAlbLogsByUpstreamHost(rows, ["10.0.1.4"]);
    expect(kept.map((row) => row.labels.upstream_host ?? row.labels.line)).toEqual([
      "10.0.1.4:8080",
      "ready",
    ]);
  });
});

describe("recentLogsRange", () => {
  const NOW = Date.parse("2026-11-02T12:00:00.000Z");

  test("recentLogsRange ends at now", () => {
    expect(recentLogsRange(NOW).to).toBe("2026-11-02T12:00:00.000Z");
  });

  test("recentLogsRange looks back RECENT_LOGS_LOOKBACK_MS", () => {
    const range = recentLogsRange(NOW);
    expect(Date.parse(range.to) - Date.parse(range.from)).toBe(RECENT_LOGS_LOOKBACK_MS);
  });

  test("RECENT_LOGS_LOOKBACK_MS is at least 24h", () => {
    expect(RECENT_LOGS_LOOKBACK_MS).toBeGreaterThanOrEqual(24 * 60 * 60 * 1000);
  });

  test("live tailing keeps the same window when it slides forward", () => {
    const later = new Date(NOW + 60_000);
    const slid = resolveLogTimeRange(recentLogsRange(NOW), later);
    expect(slid.to).toBe(later.toISOString());
    expect(Date.parse(slid.to) - Date.parse(slid.from)).toBe(RECENT_LOGS_LOOKBACK_MS);
  });
});

describe("computeFacetsFromLabelValues", () => {
  test("lists every option for the window in the explorer's facet order", () => {
    const facets = computeFacetsFromLabelValues(
      { alb: true, instances: true, instanceNames: ["web-b", "web-a"] },
      {
        severity: ["INFO", "ERROR", "debug"],
        method: ["POST", "GET", "GET"],
        response_code: ["503", "200", "404"],
        host: ["b.example.com", "-"],
        authority: ["a.example.com", " b.example.com "],
        requested_server_name: [""],
      },
    );
    expect(facets.map((facet) => [facet.name, facet.options.map((option) => option.value)])).toEqual([
      [LOG_SOURCE_LABEL, [LOG_SOURCE_ALB, LOG_SOURCE_INSTANCE]],
      ["severity", ["ERROR", "INFO", "debug"]],
      ["method", ["GET", "POST"]],
      ["response_code", ["200", "404", "503"]],
      ["host", ["b.example.com"]],
      [COMPUTE_LOG_INSTANCE_LABEL, ["web-a", "web-b"]],
    ]);
  });

  test("leaves out sources and facets that are not in scope", () => {
    const facets = computeFacetsFromLabelValues({ alb: false, instances: true }, { severity: ["WARN"] });
    expect(facets.map((facet) => facet.name)).toEqual([LOG_SOURCE_LABEL, "severity"]);
    expect(facets[0].options).toEqual([{ value: LOG_SOURCE_INSTANCE }]);
  });
});

describe("keepPreviousInScope", () => {
  const previous = ["row"];
  const withKey = (queryKey: unknown[]) =>
    ({ queryKey }) as unknown as Parameters<ReturnType<typeof keepPreviousInScope<string[]>>>[1];

  test("keeps rows for the same project and scope, drops them otherwise", () => {
    const keep = keepPreviousInScope<string[]>("p1", "alb-a");
    expect(keep(previous, withKey(["compute", "o11y-logs", "alb", "p1", "alb-a", "q", "w"]))).toBe(previous);
    expect(keep(previous, withKey(["compute", "o11y-logs", "alb", "p1", "alb-b", "q", "w"]))).toBe(undefined);
    expect(keep(previous, withKey(["compute", "o11y-logs", "alb", "p2", "alb-a", "q", "w"]))).toBe(undefined);
  });
});

describe("facetWindowKey", () => {
  test("keys a preset by name, so re-anchoring to now keeps it", () => {
    expect(facetWindowKey({ from: "a", to: "b", preset: "last-30m" }, false)).toBe(
      facetWindowKey({ from: "c", to: "d", preset: "last-30m" }, false),
    );
    expect(facetWindowKey({ from: "a", to: "b" }, false)).toBe("a/b");
  });
});

describe("windowCountQueries", () => {
  const scope = { proxyId: "my-proxy", instanceNames: ["web-0", "web-1"], instanceFacet: true };
  const alb = 'route_name=~"httproute/[^/]+/my-proxy/.*"';

  test("counts each facet with the others applied, not its own", () => {
    const queries = windowCountQueries(scope, { method: ["GET"], response_code: ["503"] }, "", 1800);
    expect(queries.albMethod).toBe(
      `sum by (method) (count_over_time({${alb}, response_code="503"} [1800s]))`,
    );
    expect(queries.albStatus).toBe(`sum by (response_code) (count_over_time({${alb}, method="GET"} [1800s]))`);
    expect(queries.albHost).toBe(
      `sum by (host, requested_server_name, x_forwarded_host, authority) (count_over_time({${alb}, method="GET", response_code="503"} [1800s]))`,
    );
    // HTTP facets hide stdout, so nothing counts it.
    expect(queries.stdoutSeverity).toBeNull();
    expect(queries.stdoutTotal).toBeNull();
  });

  test("applies Severity and the picked instances to stdout", () => {
    const queries = windowCountQueries(
      scope,
      { severity: ["ERROR", "WARN"], [COMPUTE_LOG_INSTANCE_LABEL]: ["web-1"] },
      "",
      300,
    );
    expect(queries.stdoutSeverity).toBe(
      `sum by (severity) (count_over_time({${COMPUTE_LOG_INSTANCE_LABEL}="web-1"} [300s]))`,
    );
    expect(queries.stdoutInstance).toBe(
      `sum by (${COMPUTE_LOG_INSTANCE_LABEL}) (count_over_time({${COMPUTE_LOG_INSTANCE_LABEL}=~"web-0|web-1", severity=~"ERROR|WARN"} [300s]))`,
    );
    expect(queries.stdoutTotal).toBe(
      `sum(count_over_time({${COMPUTE_LOG_INSTANCE_LABEL}="web-1", severity=~"ERROR|WARN"} [300s]))`,
    );
    // Severity hides ALB rows.
    expect(queries.albHost).toBeNull();
  });

  test("runs nothing while searching, and no HTTP counts with a host ticked", () => {
    expect(Object.values(windowCountQueries(scope, {}, "boom", 60)).every((query) => query === null)).toBe(true);
    const queries = windowCountQueries(scope, { host: ["a.example.com"] }, "", 60);
    expect(queries.albMethod).toBeNull();
    expect(queries.albHost).not.toBeNull();
  });

  test("keeps the ALB total for Source when Source hides ALB rows", () => {
    const queries = windowCountQueries(scope, { [LOG_SOURCE_LABEL]: [LOG_SOURCE_INSTANCE] }, "", 60);
    expect(queries.albMethod).toBeNull();
    expect(queries.albHost).not.toBeNull();
    expect(queries.stdoutTotal).not.toBeNull();
  });
});

describe("windowFacetCounts", () => {
  const scope = { proxyId: "my-proxy", instanceNames: ["web-0"], instanceFacet: false };
  const get = (counts: ReturnType<typeof windowFacetCounts>, name: string) =>
    counts[name] ? Object.fromEntries(counts[name]) : undefined;

  test("counts every facet, Source from the totals", () => {
    const counts = windowFacetCounts(scope, {}, "", {
      albMethod: [{ labels: { method: "GET" }, count: 4 }, { labels: { method: "-" }, count: 1 }],
      albStatus: [{ labels: { response_code: "200" }, count: 5 }],
      albHost: [
        { labels: { host: "a.example.com", authority: "httpbin.org" }, count: 3 },
        { labels: { host: "-", x_forwarded_host: "b.example.com" }, count: 2 },
      ],
      stdoutSeverity: [{ labels: { severity: "ERROR" }, count: 7 }],
      stdoutTotal: [{ labels: {}, count: 9 }],
    });
    expect(get(counts, "method")).toEqual({ GET: 4 });
    expect(get(counts, "response_code")).toEqual({ "200": 5 });
    expect(get(counts, "host")).toEqual({ "a.example.com": 3, "b.example.com": 2 });
    expect(get(counts, "severity")).toEqual({ ERROR: 7 });
    expect(get(counts, LOG_SOURCE_LABEL)).toEqual({ [LOG_SOURCE_ALB]: 5, [LOG_SOURCE_INSTANCE]: 9 });
  });

  test("zeros a source the other filters hide", () => {
    const counts = windowFacetCounts(scope, { severity: ["ERROR"] }, "", {
      stdoutSeverity: [{ labels: { severity: "ERROR" }, count: 2 }],
      stdoutTotal: [{ labels: {}, count: 2 }],
    });
    expect(get(counts, "method")).toEqual({});
    expect(get(counts, LOG_SOURCE_LABEL)).toEqual({ [LOG_SOURCE_ALB]: 0, [LOG_SOURCE_INSTANCE]: 2 });
  });

  test("leaves a failed slot's facets uncounted, and everything while searching", () => {
    const counts = windowFacetCounts(scope, {}, "", { albMethod: [] });
    expect(get(counts, "method")).toEqual({});
    expect(counts.response_code).toBeUndefined();
    expect(counts[LOG_SOURCE_LABEL]).toBeUndefined();
    expect(windowFacetCounts(scope, {}, "x", { albMethod: [] })).toEqual({});
  });
});

describe("computeFacetsFromLabelValues with counts", () => {
  test("gives counted facets a count per option, 0 when nothing matched", () => {
    const facets = computeFacetsFromLabelValues(
      { alb: true, instances: false },
      { method: ["GET", "POST"], response_code: ["200"] },
      { method: new Map([["GET", 3]]) },
    );
    expect(facets.find((facet) => facet.name === "method")?.options).toEqual([
      { value: "GET", count: 3 },
      { value: "POST", count: 0 },
    ]);
    expect(facets.find((facet) => facet.name === "response_code")?.options).toEqual([{ value: "200" }]);
  });
});
