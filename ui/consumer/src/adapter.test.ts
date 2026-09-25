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
