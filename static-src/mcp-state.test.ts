import { describe, it, expect, beforeEach, vi } from "vitest";
import { effect } from "@cplieger/reactive";
import {
  adaptStatus,
  servers,
  insertConfiguredEntry,
  removeConfiguredEntry,
  mcpState,
  updateConfiguredEntry,
} from "./mcp-state.js";
import type { Server } from "./mcp-state.js";

// Only the network edge is mocked: the decoder is real, so the fixture decodes as a GET /api/mcp body.
const wire = vi.hoisted(() => ({ body: {} as unknown }));
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: <T>(_path: string, decode: (v: unknown) => T) =>
      Promise.resolve(decode(wire.body)),
  } as unknown as typeof actual;
});

describe("the server list decoder", () => {
  it("keeps a record's wait_for_ready and timeout_ms", async () => {
    wire.body = {
      servers: [
        {
          id: "srv-w",
          name: "slow",
          transport: "stdio",
          enabled: true,
          created_at: 1,
          updated_at: 2,
          command: "uvx",
          wait_for_ready: true,
          timeout_ms: 120000,
        },
      ],
    };
    mcpState.refetchServers();
    await vi.waitFor(() => {
      expect(servers.get("srv-w")).toBeDefined();
    });
    expect(servers.get("srv-w")?.wait_for_ready).toBe(true);
    expect(servers.get("srv-w")?.timeout_ms).toBe(120000);
  });

  it("keeps a record's oauth metadata URL and redirect", async () => {
    // The raw-JSON box shows the decoded record, so a dropped field would also be cleared by a raw-edit save.
    wire.body = {
      servers: [
        {
          id: "srv-o",
          name: "cimd",
          transport: "http",
          enabled: true,
          created_at: 1,
          updated_at: 2,
          url: "https://mcp.example/mcp",
          oauth_client_metadata_url: "https://example.com/c.json",
          oauth_redirect_uri: ":7778",
        },
      ],
    };
    mcpState.refetchServers();
    await vi.waitFor(() => {
      expect(servers.get("srv-o")).toBeDefined();
    });
    expect(servers.get("srv-o")?.oauth_client_metadata_url).toBe("https://example.com/c.json");
    expect(servers.get("srv-o")?.oauth_redirect_uri).toBe(":7778");
  });
});

describe("adaptStatus", () => {
  const cases = [
    {
      name: "connected state preserves name",
      input: { name: "github", state: "connected" },
      expected: { name: "github", origin: "user", state: "connected" },
    },
    {
      name: "needs_auth with oauth_url preserves url",
      input: { name: "linear", state: "needs_auth", oauth_url: "https://auth.example.com" },
      expected: {
        name: "linear",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://auth.example.com",
        relayed: false,
      },
    },
    {
      name: "needs_auth without oauth_url defaults to empty string",
      input: { name: "sentry", state: "needs_auth" },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "",
        relayed: false,
      },
    },
    {
      // An absent `relayed` reads false: true would hide the only recovery path for an undelivered callback.
      name: "needs_auth without relayed defaults to not-yet-relayed",
      input: { name: "sentry", state: "needs_auth", oauth_url: "https://a.example" },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://a.example",
        relayed: false,
      },
    },
    {
      name: "needs_auth carries a delivered relay through",
      input: { name: "sentry", state: "needs_auth", oauth_url: "https://a.example", relayed: true },
      expected: {
        name: "sentry",
        origin: "user",
        state: "needs_auth",
        oauth_url: "https://a.example",
        relayed: true,
      },
    },
    {
      name: "failed with error preserves error",
      input: { name: "pg", state: "failed", error: "connection refused" },
      expected: { name: "pg", origin: "user", state: "failed", error: "connection refused" },
    },
    {
      name: "failed without error defaults to empty string",
      input: { name: "redis", state: "failed" },
      expected: { name: "redis", origin: "user", state: "failed", error: "" },
    },
    {
      name: "idle state",
      input: { name: "slack", state: "idle" },
      expected: { name: "slack", origin: "user", state: "idle" },
    },
    {
      name: "unknown state falls through to idle",
      input: { name: "custom", state: "reconnecting" },
      expected: { name: "custom", origin: "user", state: "idle" },
    },
    {
      name: "empty name preserved as-is",
      input: { name: "", state: "connected" },
      expected: { name: "", origin: "user", state: "connected" },
    },
    // Sent only for a server marotte did not configure; "off" and "no chat is running" are different rows.
    {
      name: "disabled state is a state of its own, not idle",
      input: { name: "off-server", state: "disabled", origin: "power" },
      expected: { name: "off-server", origin: "power", state: "disabled" },
    },
    // The origin drives edit affordances, so each value is pinned.
    {
      name: "a power's origin is carried through",
      input: { name: "from-power", state: "connected", origin: "power" },
      expected: { name: "from-power", origin: "power", state: "connected" },
    },
    {
      name: "an unattributable origin is carried through",
      input: { name: "mystery", state: "connected", origin: "unknown" },
      expected: { name: "mystery", origin: "unknown", state: "connected" },
    },
    // Both fall back to "user", keeping the status on the config row it names.
    {
      name: "an absent origin falls back to user",
      input: { name: "old-server", state: "connected" },
      expected: { name: "old-server", origin: "user", state: "connected" },
    },
    {
      name: "an unrecognised origin falls back to user",
      input: { name: "weird", state: "connected", origin: "sideloaded" },
      expected: { name: "weird", origin: "user", state: "connected" },
    },
    {
      name: "a workspace origin carries the root that defines it",
      input: {
        name: "ws",
        state: "connected",
        origin: "workspace",
        origin_root: "/workspace/app",
      },
      expected: {
        name: "ws",
        origin: "workspace",
        originRoot: "/workspace/app",
        state: "connected",
      },
    },
    {
      name: "a bundled origin is carried through",
      input: { name: "kiro-docs", state: "connected", origin: "bundled" },
      expected: { name: "kiro-docs", origin: "bundled", state: "connected" },
    },
    {
      name: "a power origin carries the Power's name",
      input: { name: "aws", state: "connected", origin: "power", origin_power: "aws-infra" },
      expected: { name: "aws", origin: "power", originPower: "aws-infra", state: "connected" },
    },
    {
      name: "a shadowing server says so",
      input: { name: "mine", state: "connected", origin: "workspace", shadows: true },
      expected: { name: "mine", origin: "workspace", shadows: true, state: "connected" },
    },
  ] as const;

  for (const { name, input, expected } of cases) {
    it(name, () => {
      expect(adaptStatus(input as Parameters<typeof adaptStatus>[0])).toEqual(expected);
    });
  }
});

// A module singleton, reset per test; these exercise the real splice/fallback/dedup logic.
describe("configured-server mutation helpers", () => {
  beforeEach(() => {
    servers.clear();
  });

  function makeServer(id: string, name = `srv-${id}`): Server {
    return {
      id,
      name,
      transport: "stdio",
      enabled: true,
      created_at: 1000,
      updated_at: 1000,
    };
  }

  const orderedIds = (): string[] => servers.items().map((s) => s.id);

  it("insertConfiguredEntry inserts at the given index", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, c]);
    insertConfiguredEntry(b, 1);
    expect(orderedIds()).toEqual(["a", "b", "c"]);
  });

  it("insertConfiguredEntry falls back to id ordering when atIndex is out of range / omitted", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");

    // Omitted: id position (b sorts between a and c).
    servers.setAll([a, c]);
    insertConfiguredEntry(b);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    // atIndex past the end falls back to id ordering.
    servers.clear();
    servers.setAll([a, c]);
    insertConfiguredEntry(b, 99);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    // Negative atIndex falls back to id ordering.
    servers.clear();
    servers.setAll([a, c]);
    insertConfiguredEntry(b, -1);
    expect(orderedIds()).toEqual(["a", "b", "c"]);
  });

  it("insertConfiguredEntry is idempotent when the id already exists", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, b, c]);

    // Same id, different name, and a hint that would otherwise move it.
    insertConfiguredEntry(makeServer("b", "CHANGED"), 0);

    // has(id) early-return: order and stored value unchanged.
    expect(orderedIds()).toEqual(["a", "b", "c"]);
    expect(servers.size).toBe(3);
    expect(servers.get("b")).toEqual(b);
  });

  it("insert preserves per-entity signal identity + does not re-fire unchanged rows", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    servers.setAll([a]);

    const sigA = servers.signalFor("a");
    if (sigA === undefined) {
      throw new Error("signalFor('a') missing after setAll");
    }

    let aRuns = 0;
    const dispose = effect(() => {
      void sigA.value;
      aRuns++;
    });
    expect(aRuns).toBe(1); // Initial run.

    // Only the order changes; setAll writes a's value back as the same reference, so its per-entity signal does not fire.
    insertConfiguredEntry(b, 0);

    expect(aRuns).toBe(1); // a's effect did not re-fire.
    expect(orderedIds()).toEqual(["b", "a"]); // Structure did change.
    expect(servers.signalFor("a")).toBe(sigA); // Same signal object, reused.

    dispose();
  });

  it("removeConfiguredEntry returns [entry, index]; reinserting at that index restores order", () => {
    const a = makeServer("a");
    const b = makeServer("b");
    const c = makeServer("c");
    servers.setAll([a, b, c]);

    const removed = removeConfiguredEntry("b");
    expect(removed).toEqual([b, 1]);
    expect(orderedIds()).toEqual(["a", "c"]);

    if (removed === undefined) {
      throw new Error("expected removed tuple");
    }
    insertConfiguredEntry(removed[0], removed[1]);
    expect(orderedIds()).toEqual(["a", "b", "c"]);

    expect(removeConfiguredEntry("missing")).toBeUndefined();
  });

  it("updateConfiguredEntry patches in place and returns the previous snapshot", () => {
    const a = makeServer("a");
    servers.setAll([a]);

    const prev = updateConfiguredEntry("a", { enabled: false });
    expect(prev).toEqual(a);
    expect(servers.get("a")).toEqual({ ...a, enabled: false });
    expect(updateConfiguredEntry("missing", { enabled: false })).toBeUndefined();
  });
});
