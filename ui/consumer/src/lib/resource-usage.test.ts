import { describe, expect, test } from "bun:test";
import {
  allocatedCores,
  allocatedMemory,
  allocatedTotal,
  cpuUsageHint,
  formatCores,
  formatCpuUsage,
  formatMemoryUsage,
  formatPercent,
  memoryUsageHint,
  parseCpuCores,
  parseMemoryBytes,
} from "./resource-usage";

describe("formatCores", () => {
  test("plain decimals, never SI prefixes", () => {
    expect(formatCores(0.0021, false)).toBe("0.0021");
    expect(formatCores(0.0007, true)).toBe("0.0007");
    expect(formatCores(0.02, false)).toBe("0.02");
    expect(formatCores(0.12345, false)).toBe("0.12");
  });

  test("keeps three decimals above one core", () => {
    expect(formatCores(1.002, false)).toBe("1.002");
    expect(formatCores(1.002, true)).toBe("1");
    expect(formatCores(2, false)).toBe("2");
  });

  test("zero and vanishing values", () => {
    expect(formatCores(0, true)).toBe("0");
    expect(formatCores(2e-7, false)).toBe("<0.000001");
  });
});

describe("formatPercent", () => {
  test("idle CPU does not round to 0%", () => {
    expect(formatPercent(0.0021, false)).toBe("0.21%");
    expect(formatPercent(0.0007, false)).toBe("0.07%");
    expect(formatPercent(0.0015, true)).toBe("0.15%");
  });

  test("larger values lose decimals", () => {
    expect(formatPercent(0.045, false)).toBe("4.5%");
    expect(formatPercent(0.5, false)).toBe("50%");
    expect(formatPercent(1, true)).toBe("100%");
    expect(formatPercent(0, true)).toBe("0%");
  });
});

describe("parseCpuCores", () => {
  test("cores and millicores", () => {
    expect(parseCpuCores("2")).toBe(2);
    expect(parseCpuCores("500m")).toBe(0.5);
    expect(parseCpuCores("0")).toBeUndefined();
    expect(parseCpuCores("lots")).toBeUndefined();
    expect(parseCpuCores(undefined)).toBeUndefined();
  });
});

describe("allocatedCores", () => {
  test("maps every instance", () => {
    const cores = allocatedCores([
      { name: "a", cpu: "1" },
      { name: "b", cpu: "250m" },
    ]);
    expect(cores?.get("a")).toBe(1);
    expect(cores?.get("b")).toBe(0.25);
  });

  test("undefined when any size is unknown", () => {
    expect(allocatedCores([{ name: "a", cpu: "1" }, { name: "b" }])).toBeUndefined();
    expect(allocatedCores([])).toBeUndefined();
  });
});

describe("formatCpuUsage", () => {
  test("percent of allocation, or cores without one", () => {
    expect(formatCpuUsage(0.014, 7)).toBe("0.2%");
    expect(formatCpuUsage(0.014, undefined)).toBe("0.014");
    expect(formatCpuUsage(undefined, 7)).toBe("—");
    expect(cpuUsageHint(0.0081, 5)).toBe("0.0081 of 5 vCPU");
    expect(cpuUsageHint(0.0081, undefined)).toBe("cores in use");
  });
});

describe("memory", () => {
  test("parses Kubernetes quantities", () => {
    expect(parseMemoryBytes("2Gi")).toBe(2 * 1024 ** 3);
    expect(parseMemoryBytes("512Mi")).toBe(512 * 1024 ** 2);
    expect(parseMemoryBytes("1G")).toBe(1e9);
    expect(parseMemoryBytes("0")).toBeUndefined();
    expect(parseMemoryBytes("lots")).toBeUndefined();
  });

  test("allocation per instance, undefined when any is unknown", () => {
    expect(allocatedMemory([{ name: "a", memory: "2Gi" }])?.get("a")).toBe(2 * 1024 ** 3);
    expect(allocatedMemory([{ name: "a", memory: "2Gi" }, { name: "b" }])).toBeUndefined();
  });

  test("totals only the given instances", () => {
    const allocated = new Map([["a", 1], ["b", 2], ["c", 4]]);
    expect(allocatedTotal(allocated, [{ name: "a" }, { name: "c" }])).toBe(5);
    expect(allocatedTotal(undefined, [{ name: "a" }])).toBeUndefined();
  });

  test("percent of allocation, or bytes without one", () => {
    expect(formatMemoryUsage(190 * 1024 ** 2, 2 * 1024 ** 3)).toBe("9.3%");
    expect(formatMemoryUsage(190 * 1024 ** 2, undefined)).toBe("190 MB");
    expect(memoryUsageHint(190 * 1024 ** 2, 2 * 1024 ** 3)).toBe("190 MB of 2.0 GB");
    expect(memoryUsageHint(undefined, 2 * 1024 ** 3)).toBe("of 2.0 GB");
    expect(memoryUsageHint(190 * 1024 ** 2, undefined)).toBe("in use");
  });
});
