import { describe, expect, test } from "bun:test";
import { collectInstanceInternalIPs, toInstance, type RawInstance } from "./adapter";

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

// Shaped like `kubectl get instance -o yaml` on a gated, suspended replica:
// labels the controllers stamp, a struct-valued schedulingGates list, and an
// Available=False condition carrying the reason the table surfaces.
function gatedInstance(): RawInstance {
  return {
    metadata: {
      name: "demo-workload-us-east-1-abc12-0",
      uid: "0f7b2c1e-5d1a-4e6b-9f3c-2a1b3c4d5e6f",
      namespace: "default",
      creationTimestamp: "2026-09-30T18:04:11Z",
      labels: {
        "compute.datumapis.com/instance-index": "3",
        "compute.datumapis.com/location": "us-east-1-a",
        "compute.datumapis.com/placement-name": "us",
        "compute.datumapis.com/workload-deployment-name": "demo-workload-us-east-1-abc12",
        "compute.datumapis.com/workload-name": "demo-workload",
        "compute.datumapis.com/workload-uid": "7a1c9d2e-0b3f-4c5d-8e6f-1a2b3c4d5e6f",
      },
    },
    spec: {
      controller: { schedulingGates: [{ name: "compute.datumapis.com/quota" }, { name: "custom/hold" }] },
      runtime: { resources: { instanceType: "datumcloud-d1-standard-2" } },
    },
    status: {
      suspended: true,
      conditions: [
        {
          type: "Available",
          status: "False",
          reason: "SchedulingGatesPresent",
          message: "Instance has scheduling gates present",
          lastTransitionTime: "2026-09-30T18:04:12Z",
        },
        { type: "Ready", status: "False", reason: "SchedulingGatesPresent" },
      ],
    },
  };
}

describe("toInstance deployment and scheduling fields", () => {
  test("surfaces the Available condition's reason and message", () => {
    const instance = toInstance(gatedInstance());
    expect(instance.status).toBe("Pending");
    expect(instance.statusReason).toBe("SchedulingGatesPresent");
    expect(instance.statusMessage).toBe("Instance has scheduling gates present");
  });

  test("flattens struct-valued scheduling gates to their names", () => {
    expect(toInstance(gatedInstance()).schedulingGates).toEqual([
      "compute.datumapis.com/quota",
      "custom/hold",
    ]);
  });

  test("reads suspended from status", () => {
    expect(toInstance(gatedInstance()).suspended).toBe(true);
  });

  test("reads deployment name and numeric index from labels", () => {
    const instance = toInstance(gatedInstance());
    expect(instance.workloadDeploymentName).toBe("demo-workload-us-east-1-abc12");
    expect(instance.instanceIndex).toBe(3);
  });

  test("defaults when the controller has not stamped anything yet", () => {
    const instance = toInstance(raw({}));
    expect(instance.statusReason).toBeUndefined();
    expect(instance.statusMessage).toBeUndefined();
    expect(instance.schedulingGates).toEqual([]);
    expect(instance.suspended).toBe(false);
    expect(instance.workloadDeploymentName).toBeUndefined();
    expect(instance.instanceIndex).toBeUndefined();
  });

  test("ignores a malformed instance-index label", () => {
    const fixture = gatedInstance();
    fixture.metadata!.labels!["compute.datumapis.com/instance-index"] = "three";
    expect(toInstance(fixture).instanceIndex).toBeUndefined();
  });
});
