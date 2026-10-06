// A transient toast while the gate is active, carrying the violated/active properties; nothing
// when idle.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockNotice = vi.fn();
vi.mock("../toast.js", async () => ({
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
  notice: mockNotice,
}));
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so the handler registers against the bus mock.
await import("./safety.js");

beforeEach(() => {
  vi.clearAllMocks();
});

describe("safety_status handler", () => {
  it("raises an error-level toast for a blocked gate, including the violated property", () => {
    fireSSE("safety_status", "c1", {
      status: "blocked",
      detail: "fs_write blocked",
      tool_id: "fs_write",
      blocked_properties: ["no public S3 buckets"],
    });
    expect(mockNotice).toHaveBeenCalledTimes(1);
    const call = mockNotice.mock.calls[0] ?? [];
    expect(String(call[0])).toContain("no public S3 buckets"); // message carries the constraint
    expect(call[1]).toBe("error"); // level
  });

  // A gate that has stopped nothing takes the info level rather than being promoted
  // to a failure.
  it("raises an info toast for evaluating (monitor-mode would-violate)", () => {
    fireSSE("safety_status", "c1", {
      status: "evaluating",
      detail: "Would violate: no public buckets",
    });
    const call = mockNotice.mock.calls[0] ?? [];
    expect(call[1]).toBe("info");
    expect(String(call[0])).toContain("Would violate");
  });

  it("raises nothing for idle, which is the all-clear", () => {
    fireSSE("safety_status", "c1", { status: "idle" });
    expect(mockNotice).not.toHaveBeenCalled();
  });

  it("folds the last formalized properties into a status that carries none of its own", () => {
    fireSSE("safety_properties", "c1", {
      properties: [{ description: "encrypt EBS volumes", enabled: true }],
      reason: "formalized",
    });
    fireSSE("safety_status", "c1", { status: "evaluating" });
    const call = mockNotice.mock.calls[0] ?? [];
    expect(String(call[0])).toContain("encrypt EBS volumes");
  });

  it("ignores an event with an empty chat id", () => {
    fireSSE("safety_status", "", { status: "blocked" });
    expect(mockNotice).not.toHaveBeenCalled();
  });
});
