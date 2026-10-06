// A toast when a broken MCP server is used. Dedupe keys on the state transition (every bridge emits its own status on
// connect), and the notice carries kiro-cli's captured text, which tells a missing command from a timeout.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const toastCalls: { message: string; level: string }[] = [];

vi.mock("./toast.js", () => ({
  showToast: (message: string, level: string) => {
    toastCalls.push({ message, level });
    return () => {
      /* dismiss */
    };
  },
  // ESM is linked for real here, so every name the graph reaches must exist.
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
  errorWithAction: vi.fn(),
  _resetForTest: vi.fn(),
}));

// mcp-ui.ts's heavy siblings are stubbed; every name its graph reaches must exist, since Browser Mode links ESM for real.
vi.mock("./dom.js", () => ({
  $: {},
  byId: () => document.createElement("div"),
  maybeEl: () => null,
  setBusy: () => undefined,
  setControlBusy: () => undefined,
  forceReflow: () => 0,
}));

const { announceMCPFailure, mcpFailureText } = await import("./mcp-ui.js");

describe("mcpFailureText", () => {
  it("carries the captured reason", () => {
    expect(mcpFailureText("github", "spawn ENOENT")).toContain("spawn ENOENT");
    expect(mcpFailureText("github", "spawn ENOENT")).toContain("github");
  });

  it("names the server when the reason is empty", () => {
    // adaptStatus defaults an absent error to "", so this is a real arrival; the notice must name the server.
    const text = mcpFailureText("linear", "");
    expect(text).toContain("linear");
    expect(text.endsWith("failed to start.")).toBe(true);
  });

  it("ignores whitespace-only reasons", () => {
    expect(mcpFailureText("sentry", "   \n ")).toBe(mcpFailureText("sentry", ""));
  });
});

describe("announceMCPFailure", () => {
  beforeEach(() => {
    toastCalls.length = 0;
  });
  afterEach(() => {
    toastCalls.length = 0;
  });

  it("fires once per transition into failed, not once per frame", () => {
    // The reconnect storm: three bridges report the same wedged server; only the first crosses idle -> failed.
    announceMCPFailure("github", "spawn ENOENT", "idle");
    announceMCPFailure("github", "spawn ENOENT", "failed");
    announceMCPFailure("github", "spawn ENOENT", "failed");
    expect(toastCalls).toHaveLength(1);
    expect(toastCalls[0]?.level).toBe("error");
    expect(toastCalls[0]?.message).toContain("spawn ENOENT");
  });

  it("re-arms after the server leaves failed", () => {
    announceMCPFailure("github", "spawn ENOENT", "idle");
    // A reconnect succeeded, so the next failure must be audible again.
    announceMCPFailure("github", "handshake timeout", "connected");
    expect(toastCalls).toHaveLength(2);
    expect(toastCalls[1]?.message).toContain("handshake timeout");
  });

  it("does not suppress a second server's first failure", () => {
    // Per server per transition: one wedged server must not silence another.
    announceMCPFailure("github", "spawn ENOENT", "idle");
    announceMCPFailure("linear", "handshake timeout", "idle");
    expect(toastCalls).toHaveLength(2);
    expect(toastCalls[1]?.message).toContain("linear");
  });

  it("announces a failure arriving from every non-failed state", () => {
    for (const prev of ["idle", "connected", "needs_auth", "disabled"] as const) {
      toastCalls.length = 0;
      announceMCPFailure("github", "boom", prev);
      expect(toastCalls, `prev=${prev}`).toHaveLength(1);
    }
  });
});
