// The MCP status copy ends where the server's is, whatever order the status frames, the status read and a
// reconcile arrive in.
import { describe, expect, it, vi, beforeAll } from "vitest";

// Each status read stays open until the test answers it, so a frame can land while it is in flight.
const wire = vi.hoisted(() => ({
  pending: [] as ((body: unknown) => void)[],
  reads: 0,
}));
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: async <T>(path: string, decode: Decoder<T>): Promise<T | null> => {
      if (path === "/api/mcp/status") {
        wire.reads++;
        const body = await new Promise<unknown>((resolve) => wire.pending.push(resolve));
        return decode(body);
      }
      if (path === "/api/mcp") {
        return decode({ servers: [] });
      }
      if (path === "/api/config/standing") {
        return decode({ standing: [], load_errors: {} });
      }
      return null;
    },
  };
});
vi.mock(import("./actions/mcp.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    signInServer: {
      dispatch: vi.fn(() => Promise.resolve(null)),
    } as unknown as typeof actual.signInServer,
  };
});
vi.mock(import("./actions/index.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    registerCleanup:
      (_fn: () => void): (() => void) =>
      () => {
        /* noop: the singleton cleanup registry is process-wide */
      },
  };
});

import type { Decoder } from "./api-client.js";
import { BUS_RECONCILE, dispatch, emitBus } from "./bus.js";
import type { ServerEvent } from "./types.js";
import { initMCP } from "./mcp-ui.js";
import { mcpState, servers, statusSignalFor } from "./mcp-state.js";
import { installMirrors } from "./mirror.js";
import type { Server } from "./mcp-state.js";

// The server's `mcp_status` counter, increasing across the file: the copy is module state that outlives a case.
let statusVersion = 0;
function nextVersion(): string {
  statusVersion++;
  return String(statusVersion);
}

async function readOpened(count: number): Promise<void> {
  await vi.waitFor(() => {
    expect(wire.pending.length).toBe(count);
  });
}

/** Answer the oldest open status read with these rows, read at `version`. */
function answerStatus(rows: readonly Record<string, unknown>[], version: string): void {
  const resolve = wire.pending.shift();
  if (resolve === undefined) {
    throw new Error("no status read is open");
  }
  resolve({ servers: rows, subject: { kind: "mcp_status", ref: "", version } });
}

async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

function configured(name: string): Server {
  return {
    id: `id-${name}`,
    name,
    transport: "stdio",
    enabled: true,
    created_at: 0,
    updated_at: 0,
  };
}

beforeAll(async () => {
  const section = document.createElement("div");
  section.id = "mcp-section";
  const modal = document.createElement("div");
  modal.id = "mcp-modal";
  document.body.replaceChildren(section, modal);
  // As app.ts boots: the mirrors follow the transport reconcile.
  installMirrors();
  initMCP();
  // initMCP's own first read: the server has no MCP rows yet.
  await readOpened(1);
  answerStatus([], nextVersion());
  await settle();
});

describe("a frame and a status read in flight", () => {
  it("a status read answered before a failure does not paint the server connected", async () => {
    servers.setAll([configured("s")]);
    mcpState.refetchStatus();
    await readOpened(1);
    // The server answered the read while the server was connected, then it failed.
    const readAt = nextVersion();
    dispatch({
      type: "mcp_status",
      payload: {
        server: "s",
        status: { name: "s", state: "failed", origin: "user", error: "boom" },
      },
      subject: { kind: "mcp_status", ref: "", version: nextVersion() },
    } as ServerEvent);
    answerStatus([{ name: "s", state: "connected", origin: "user" }], readAt);
    await settle();

    expect(statusSignalFor("s").value.state).toBe("failed");
  });
});

describe("a reconcile", () => {
  it("a reconcile re-reads MCP status", async () => {
    servers.setAll([configured("r")]);
    mcpState.refetchStatus();
    await readOpened(1);
    answerStatus([{ name: "r", state: "connected", origin: "user" }], nextVersion());
    await settle();
    expect(statusSignalFor("r").value.state).toBe("connected");
    const before = wire.reads;

    emitBus(BUS_RECONCILE, { cause: "full:test", signal: new AbortController().signal });
    await settle();

    expect(wire.reads, "a reconcile reads the status inventory again").toBe(before + 1);
    answerStatus([], nextVersion());
    await settle();
    expect(statusSignalFor("r").value.state).toBe("idle");
  });
});
