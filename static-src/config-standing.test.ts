import { describe, expect, it, vi } from "vitest";

// The network edge answers each read when the case says so, so read order is the case's.
const net = vi.hoisted(() => ({
  pending: [] as ((body: unknown) => void)[],
}));
vi.mock(import("./api-client.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    apiGetTyped: <T>(_path: string, decode: (v: unknown) => T): Promise<T | null> =>
      new Promise((resolve) => {
        net.pending.push((body) => {
          resolve(body === null ? null : decode(body));
        });
      }),
  } as unknown as typeof actual;
});
const bus = vi.hoisted(() => ({
  sse: new Map<string, () => void>(),
  busHandlers: new Map<string, () => void>(),
}));
vi.mock(import("./bus.js"), async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    onSSE: ((type: string, fn: () => void) => {
      bus.sse.set(type, fn);
      return () => undefined;
    }) as unknown as typeof actual.onSSE,
    onBus: ((type: string, fn: () => void) => {
      bus.busHandlers.set(type, fn);
      return () => undefined;
    }) as unknown as typeof actual.onBus,
  };
});

import { BUS_RECONCILE } from "./bus.js";
import { configStanding, refreshConfigStanding } from "./config-standing.js";

function answer(i: number, body: unknown): void {
  const reply = net.pending[i];
  if (reply === undefined) {
    throw new Error(`no read ${String(i)} is out`);
  }
  reply(body);
}

const disabled = (key: string, layer: string): Record<string, unknown> => ({
  type: "mcp-server",
  key,
  kind: "disabled",
  decided_by: layer,
  decided_by_name: layer,
});

describe("the configuration standing store", () => {
  it("keeps the newest read's answer when an older read lands last", async () => {
    refreshConfigStanding();
    refreshConfigStanding();
    answer(1, { standing: [disabled("srv", "Workspace")], load_errors: {} });
    await vi.waitFor(() => {
      expect(configStanding.peek().servers.get("srv")?.decided_by).toBe("Workspace");
    });
    answer(0, { standing: [], load_errors: {} });
    await Promise.resolve();
    await Promise.resolve();
    expect(configStanding.peek().servers.get("srv")?.decided_by).toBe("Workspace");
  });

  it("keeps the last answer when a read fails", async () => {
    net.pending = [];
    refreshConfigStanding();
    answer(0, null);
    await Promise.resolve();
    await Promise.resolve();
    expect(configStanding.peek().servers.get("srv")?.decided_by).toBe("Workspace");
  });

  it("keeps the first standing KAS lists for a key", async () => {
    net.pending = [];
    refreshConfigStanding();
    answer(0, {
      standing: [disabled("dup", "Workspace"), disabled("dup", "Machine Administrator")],
      load_errors: {},
    });
    await vi.waitFor(() => {
      expect(configStanding.peek().servers.get("dup")?.decided_by).toBe("Workspace");
    });
  });

  it("re-reads on config_standing_changed and on every stream gap", () => {
    net.pending = [];
    bus.sse.get("config_standing_changed")?.();
    expect(net.pending).toHaveLength(1);
    bus.busHandlers.get(BUS_RECONCILE)?.();
    expect(net.pending).toHaveLength(2);
  });
});
