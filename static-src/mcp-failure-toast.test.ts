// A toast when a broken MCP server is used. Dedupe keys on the state transition (KAS emits one status copy per
// resident session), and the notice carries kiro-cli's captured text, which tells a missing command from a timeout.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

import type { ServerEvent } from "./types.js";
import type { MCPServerStatus } from "./wire/types.gen.js";

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
  $: { mcpModal: document.createElement("div") },
  byId: () => document.createElement("div"),
  maybeEl: () => null,
  setBusy: () => undefined,
  setControlBusy: () => undefined,
  forceReflow: () => 0,
}));

// No status read answers: the rows here come only from the frames each case delivers.
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, apiGetTyped: async (): Promise<null> => null };
});

const { announceMCPFailure, initMCP, mcpFailureText } = await import("./mcp-ui.js");
const { dispatch } = await import("./bus.js");

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
    // The reconnect storm: three resident sessions report the same wedged server; only the first crosses idle -> failed.
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

describe("the toasts an mcp_status frame raises", () => {
  let version = 0;

  /** Deliver `row` for its server at `at`, or at the next version. */
  function frame(row: Omit<MCPServerStatus, "origin">, at?: number): void {
    if (at === undefined) {
      version++;
    }
    dispatch({
      type: "mcp_status",
      chat_id: "",
      payload: { server: row.name, status: { origin: "user", ...row } },
      subject: { kind: "mcp_status", ref: "", version: String(at ?? version) },
    } as ServerEvent);
  }

  beforeAll(() => {
    initMCP();
  });
  beforeEach(() => {
    toastCalls.length = 0;
  });

  it("fires once on the move into failed, and not for a frame older than the row", () => {
    frame({ name: "f1", state: "connected" });
    const stale = version;
    version += 10;
    frame({ name: "f1", state: "connected" });
    // Older than the connected row held, so applying it would paint a live server dead.
    frame({ name: "f1", state: "failed", error: "old" }, stale);
    expect(toastCalls).toHaveLength(0);

    frame({ name: "f1", state: "failed", error: "spawn ENOENT" });
    frame({ name: "f1", state: "failed", error: "spawn ENOENT" });
    expect(toastCalls).toHaveLength(1);
    expect(toastCalls[0]?.message).toContain("spawn ENOENT");
  });

  it("fires the sign-in failure toast each time the waiting server's reason moves", () => {
    frame({ name: "f2", state: "needs_auth" });
    frame({ name: "f2", state: "needs_auth", error: "listener busy" });
    frame({ name: "f2", state: "needs_auth", error: "listener busy" });
    frame({ name: "f2", state: "needs_auth", error: "port taken" });

    expect(toastCalls.map((t) => t.message)).toEqual([
      'Signing in to "f2" failed: listener busy',
      'Signing in to "f2" failed: port taken',
    ]);
  });
});
