import { describe, expect, test } from "bun:test";
import { computeResourceLinkResolver, instanceActivityFilter, workloadActivityFilter } from "./activity";

describe("workloadActivityFilter", () => {
  test("matches the workload by name and its deployments and instances by prefix", () => {
    expect(workloadActivityFilter("api")).toBe(
      "spec.resource.apiGroup == 'compute.datumapis.com' && (" +
        "(spec.resource.kind == 'Workload' && spec.resource.name == 'api') || " +
        "(spec.resource.kind in ['WorkloadDeployment', 'Instance'] && spec.resource.name.startsWith('api-')))"
    );
  });

  test("escapes quotes and backslashes", () => {
    expect(workloadActivityFilter("a'b\\c")).toContain("spec.resource.name == 'a\\'b\\\\c'");
  });
});

describe("instanceActivityFilter", () => {
  test("matches one instance exactly", () => {
    expect(instanceActivityFilter("api-default-us-east-1-0")).toBe(
      "spec.resource.apiGroup == 'compute.datumapis.com' && spec.resource.kind == 'Instance' && " +
        "spec.resource.name == 'api-default-us-east-1-0'"
    );
  });
});

describe("computeResourceLinkResolver", () => {
  const resolve = computeResourceLinkResolver("api", "/project/p/services/compute/api");
  const ref = (kind: string, name: string, apiGroup = "compute.datumapis.com") => ({ apiGroup, kind, name });

  test("links the workload and its instances to plugin pages", () => {
    expect(resolve(ref("Workload", "api"))).toBe("/project/p/services/compute/api");
    expect(resolve(ref("Instance", "api-default-us-east-1-0"))).toBe(
      "/project/p/services/compute/api/instances/api-default-us-east-1-0"
    );
  });

  test("leaves other resources as plain text", () => {
    expect(resolve(ref("Workload", "other"))).toBeUndefined();
    expect(resolve(ref("HTTPProxy", "api", "networking.datumapis.com"))).toBeUndefined();
  });
});
