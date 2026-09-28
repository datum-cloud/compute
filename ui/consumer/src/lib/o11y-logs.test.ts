import { describe, expect, test } from "bun:test";
import {
  albUpstreamHostMatches,
  buildAlbLogQL,
  buildAlbUpstreamHostRegexp,
  filterAlbLogsByUpstreamHost,
  instanceUpstreamIPs,
  LOG_SOURCE_ALB,
  LOG_SOURCE_INSTANCE,
  LOG_SOURCE_LABEL,
} from "./o11y-logs";
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
