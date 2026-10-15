import { describe, expect, test } from "bun:test";
import type { Instance } from "../schema";
import { filterInstances, parseListParam } from "./instance-filters";
import type { ChartSeries } from "./prometheus";
import { busiestSeries, nextHiddenSeries, shortInstanceLabels } from "./series-view";

function series(name: string, values: number[]): ChartSeries {
  return {
    name,
    labels: {},
    data: values.map((value, i) => ({ timestamp: i, value, formattedTime: String(i) })),
  };
}

describe("busiestSeries", () => {
  test("keeps the highest peaks, in natural name order", () => {
    const input = [
      series("app-10", [1, 9]),
      series("app-2", [5]),
      series("app-1", [0, 0]),
      series("app-3", [7]),
    ];
    expect(busiestSeries(input, 3).map((s) => s.name)).toEqual(["app-2", "app-3", "app-10"]);
  });

  test("returns everything name-sorted under the limit", () => {
    const input = [series("app-10", [1]), series("app-2", [1])];
    expect(busiestSeries(input, 8).map((s) => s.name)).toEqual(["app-2", "app-10"]);
    expect(busiestSeries(input).map((s) => s.name)).toEqual(["app-2", "app-10"]);
  });
});

describe("nextHiddenSeries", () => {
  const names = ["a", "b", "c"];

  test("click isolates, click again restores", () => {
    const isolated = nextHiddenSeries(names, new Set(), "b");
    expect([...isolated].sort()).toEqual(["a", "c"]);
    expect(nextHiddenSeries(names, isolated, "b").size).toBe(0);
  });

  test("shift-click toggles but never hides the last series", () => {
    const hidden = nextHiddenSeries(names, new Set(["a", "b"]), "c", { shiftKey: true });
    expect([...hidden].sort()).toEqual(["a", "b"]);
    expect([...nextHiddenSeries(names, new Set(), "a", { shiftKey: true })]).toEqual(["a"]);
  });
});

describe("shortInstanceLabels", () => {
  test("single location leaves the ordinal", () => {
    expect(
      shortInstanceLabels(["counter-default-us-central-1-0", "counter-default-us-central-1-15"]),
    ).toEqual({
      "counter-default-us-central-1-0": "#0",
      "counter-default-us-central-1-15": "#15",
    });
  });

  test("several locations keep the location", () => {
    expect(shortInstanceLabels(["web-default-us-central-1-0", "web-default-eu-west-1-0"])).toEqual({
      "web-default-us-central-1-0": "us-central-1-0",
      "web-default-eu-west-1-0": "eu-west-1-0",
    });
  });

  test("nothing in common, or one name, is left alone", () => {
    expect(shortInstanceLabels(["alpha-0", "beta-0"])).toEqual({ "alpha-0": "alpha-0", "beta-0": "beta-0" });
    expect(shortInstanceLabels(["only-0"])).toEqual({ "only-0": "only-0" });
  });
});

function instance(name: string, location?: string): Instance {
  return { name, location } as Instance;
}

describe("filterInstances", () => {
  const all = [instance("a-0", "us"), instance("a-1", "us"), instance("a-2", "eu"), instance("a-3")];

  test("no filters covers every instance", () => {
    const result = filterInstances(all, [], []);
    expect(result.regions).toEqual(["eu", "us"]);
    expect(result.matching).toHaveLength(4);
  });

  test("region narrows the instance options", () => {
    const result = filterInstances(all, ["us"], []);
    expect(result.instanceOptions.map((i) => i.name)).toEqual(["a-0", "a-1"]);
    expect(result.matching.map((i) => i.name)).toEqual(["a-0", "a-1"]);
  });

  test("instance picks outside the chosen regions are ignored", () => {
    expect(filterInstances(all, ["us"], ["a-1", "a-2"]).matching.map((i) => i.name)).toEqual(["a-1"]);
    const stale = filterInstances(all, ["us"], ["a-2"]);
    expect(stale.pickedInstances).toEqual([]);
    expect(stale.matching.map((i) => i.name)).toEqual(["a-0", "a-1"]);
  });
});

test("a region no instance is in is ignored", () => {
  const result = filterInstances([instance("a-0", "us"), instance("a-1", "eu")], ["ap"], []);
  expect(result.pickedRegions).toEqual([]);
  expect(result.matching.map((i) => i.name)).toEqual(["a-0", "a-1"]);
});

test("locations in the same region share one option", () => {
  const regionOf = (i: Instance) => (i.location ? i.location.replace(/-[a-z]$/, "") : undefined);
  const result = filterInstances(
    [instance("a-0", "us-east-1-a"), instance("a-1", "us-east-1-b"), instance("a-2", "eu-west-1-a")],
    ["us-east-1"],
    [],
    regionOf,
  );
  expect(result.regions).toEqual(["eu-west-1", "us-east-1"]);
  expect(result.matching.map((i) => i.name)).toEqual(["a-0", "a-1"]);
});

describe("parseListParam", () => {
  test("splits, trims and dedupes", () => {
    expect(parseListParam(" a,b,,a ")).toEqual(["a", "b"]);
    expect(parseListParam(null)).toEqual([]);
  });
});
