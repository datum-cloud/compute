import { describe, expect, test } from "bun:test";
import {
  collectInstanceInternalIPs,
  toInstance,
  toWorkload,
  type RawInstance,
  type RawWorkload,
} from "./adapter";

function raw(status: RawInstance["status"]): RawInstance {
  return {
    metadata: { name: "inst-1", uid: "uid-1", creationTimestamp: "2026-01-01T00:00:00Z" },
    status,
  };
}

describe("collectInstanceInternalIPs", () => {
  test("only assignments.networkIP", () => {
    const instance = toInstance(
      raw({ networkInterfaces: [{ assignments: { networkIP: "10.0.1.4" } }] }),
    );
    expect(instance.internalIP).toBe("10.0.1.4");
    expect(instance.internalIPs).toEqual(["10.0.1.4"]);
  });

  test("v4 + v6 in addresses", () => {
    expect(
      collectInstanceInternalIPs(
        raw({
          networkInterfaces: [
            {
              assignments: { networkIP: "10.0.1.4" },
              addresses: [{ address: "10.0.1.4/32" }, { address: "2001:db8::1/128" }],
            },
          ],
        }),
      ),
    ).toEqual(["10.0.1.4", "2001:db8::1"]);
  });

  test("CIDR /32 strips to bare", () => {
    expect(
      collectInstanceInternalIPs(
        raw({
          networkInterfaces: [{ addresses: [{ address: "10.0.1.4/32" }] }],
        }),
      ),
    ).toEqual(["10.0.1.4"]);
  });

  test("delegated prefix is not a candidate", () => {
    expect(
      collectInstanceInternalIPs(
        raw({
          networkInterfaces: [{ addresses: [{ address: "2001:db8:a001::/96" }] }],
        }),
      ),
    ).toEqual([]);
  });

  test("ignores externalIP", () => {
    expect(
      collectInstanceInternalIPs(
        raw({
          networkInterfaces: [
            {
              assignments: { networkIP: "10.0.1.4", externalIP: "203.0.113.9" },
              addresses: [{ address: "10.0.1.4/32" }],
            },
          ],
        }),
      ),
    ).toEqual(["10.0.1.4"]);
  });

  test("empty status", () => {
    const instance = toInstance(raw({}));
    expect(instance.internalIP).toBeUndefined();
    expect(instance.internalIPs).toEqual([]);
  });
});

function rawWorkload(
  conditions?: NonNullable<RawWorkload["status"]>["conditions"],
): RawWorkload {
  return {
    metadata: { name: "web", uid: "uid-web", creationTimestamp: "2026-01-01T00:00:00Z" },
    spec: { placements: [{ name: "dfw", scaleSettings: { minReplicas: 1 } }] },
    status: conditions ? { conditions } : undefined,
  };
}

function unavailable(reason: string, message = "") {
  return [{ type: "Available", status: "False" as const, reason, message }];
}

describe("toWorkload health", () => {
  test("not reconciled yet is deploying", () => {
    expect(toWorkload(rawWorkload()).health).toBe("Deploying");
    expect(toWorkload(rawWorkload([])).health).toBe("Deploying");
  });

  test("Available=True is available", () => {
    expect(
      toWorkload(rawWorkload([{ type: "Available", status: "True", reason: "AvailablePlacementFound" }]))
        .health,
    ).toBe("Available");
  });

  test.each([
    "NoAvailableDeployments",
    "NetworkProvisioning",
    "InstancesProvisioning",
    "NoMatchingLocation",
    "QuotaNotGranted",
  ])("transient reason %s is deploying", (reason) => {
    expect(toWorkload(rawWorkload(unavailable(reason))).health).toBe("Deploying");
  });

  test.each([
    "NetworkNotFound",
    "NoMatchingLocations",
    "RuntimeClassNotServed",
    "SourceNotFound",
    "SomethingNew",
  ])("blocking reason %s is unavailable", (reason) => {
    expect(toWorkload(rawWorkload(unavailable(reason))).health).toBe("Unavailable");
  });

  test("referenced data is deploying only while it propagates", () => {
    expect(
      toWorkload(
        rawWorkload(unavailable("ReferencedDataNotReady", "1 of 1 desired instances waiting for companion propagation")),
      ).health,
    ).toBe("Deploying");
    expect(
      toWorkload(rawWorkload(unavailable("ReferencedDataNotReady", 'secret "db" not found'))).health,
    ).toBe("Unavailable");
  });

  test("a placement without status is deploying", () => {
    expect(toWorkload(rawWorkload()).placementRegions[0].health).toBe("Deploying");
  });
});

describe("toInstance status", () => {
  test("a crashing instance is failed", () => {
    const instance = toInstance(
      raw({
        conditions: [
          { type: "Available", status: "False", reason: "Starting" },
          { type: "Ready", status: "False", reason: "InstanceCrashing", message: "Back-off restarting" },
        ],
      }),
    );
    expect(instance.status).toBe("Failed");
  });

  test("a starting instance is pending", () => {
    const instance = toInstance(
      raw({
        conditions: [
          { type: "Available", status: "False", reason: "Starting" },
          { type: "Ready", status: "False", reason: "Provisioning" },
        ],
      }),
    );
    expect(instance.status).toBe("Pending");
  });
});
