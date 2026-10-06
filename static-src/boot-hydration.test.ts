// A manual refresh must not lose the server's connect hook (handshake plus the asks and
// waiting-status snapshots, sent once per connection) or reopen tabs the user closed. The
// stream opens before GET /api/chats resolves, and every chat-scoped consumer bails on an
// unknown chat, so the hook must be held until hydration. Opening the stream after
// hydration instead would lose frames: a fresh hello has no replay.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { init, markHydrated, _resetForTest } from "./sse-adapter.js";
import { createScriptedFetch, type ScriptedFetch, until } from "./__test-helpers__/sse-fetch.js";

vi.mock("./store-load.js", () => ({
  loadList: vi.fn(),
  loadMessages: vi.fn(),
  requestTurnRange: vi.fn(),
  scheduleListRetry: vi.fn(),
  confirmChatExists: vi.fn(() => Promise.resolve("unresolved")),
  serverMayAnswer: vi.fn(() => false),
  chatListLoaded: vi.fn(() => false),
}));
vi.mock("./tabs-sync.js", () => ({ listTabs: vi.fn() }));
// Browser Mode links ESM for real, so every imported name must exist. Each reader answers
// the EMPTY value, so no assertion passes on a live run production did not supply.
vi.mock("./run-store.js", () => ({
  rebuildLiveRuns: vi.fn(),
  // The adapter's `run_turn` arm asks past what this client holds, so the name has to exist
  // on the mock or the module graph does not link (a mock is linked, not read off an object).
  runTurnHeldSeq: vi.fn(() => undefined),
  adoptConnectRuns: vi.fn(),
  invalidateCachedRuns: vi.fn(),
  invalidateRun: vi.fn(),
  invalidateRunControls: vi.fn(),
  forgetRun: vi.fn(),
  noteRunLive: vi.fn(),
  noteRunSettled: vi.fn(),
  openRunTurn: vi.fn(),
  openRunEntry: vi.fn(),
  appendRunEntry: vi.fn(),
  // The open tails a whole-turn read REPLACES, reached by the range read in this graph.
  adoptRunOpenEntries: vi.fn(),
  clearRunHole: vi.fn(),
  registerLiveRunObserver: vi.fn(),
  registerRunStateDemand: vi.fn(),
  liveRunsForChat: vi.fn(() => []),
  liveRunIDsForChat: vi.fn(() => []),
  peekLiveRun: vi.fn(() => undefined),
  peekRunState: vi.fn(() => undefined),
  runState: vi.fn(() => undefined),
  runChatID: vi.fn(() => ""),
  runLabelOf: vi.fn(() => ""),
  isNeedInputPark: vi.fn(() => false),
  isNeedInputPause: vi.fn(() => false),
}));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: vi.fn() }));
vi.mock("./send-state.js", () => ({ setSSEStatus: vi.fn() }));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

/** Let the written bytes cross the stream reader: a frame that is still in flight
 *  cannot prove it was held. */
function settle(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 20);
  });
}

describe("the adapter holds frames until the chat store is hydrated", () => {
  let scripted: ScriptedFetch;
  let seen: string[];

  beforeEach(() => {
    scripted = createScriptedFetch();
    vi.stubGlobal("fetch", scripted.fetch);
    seen = [];
  });

  function boot(): void {
    init(
      (evt) => {
        seen.push(evt.type);
      },
      () => undefined,
    );
  }

  afterEach(() => {
    _resetForTest();
    vi.useRealTimers();
  });

  async function opened(): Promise<NonNullable<ScriptedFetch["connections"][number]>> {
    await until(() => scripted.connections.length === 1);
    const conn = scripted.connections[0];
    if (conn === undefined) {
      throw new Error("no connection");
    }
    conn.hello();
    return conn;
  }

  it("delivers nothing before markHydrated, then everything in arrival order", async () => {
    boot();
    const conn = await opened();
    conn.frame({ type: "connected", payload: { busy_stated: false, live_runs_stated: false } });
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    conn.frame({ type: "permission_needed", chat_id: "chat-1", payload: { request_id: 1 } });
    await settle();
    // Nothing has reached the store yet: this is the whole point.
    expect(seen).toEqual([]);

    markHydrated();
    // Order is preserved, and order is load-bearing: an entry_delta released before the
    // entry_opened it extends is a hole, which costs a range read to repair.
    expect(seen).toEqual(["connected", "pending_snapshot", "permission_needed"]);
  });

  it("passes frames straight through once hydrated", async () => {
    boot();
    const conn = await opened();
    markHydrated();
    conn.frame({
      type: "entry_delta",
      chat_id: "chat-1",
      payload: { turn: "t1", entry_id: "e1", lane: "", delta: "x", n: 1 },
    });
    await until(() => seen.length === 1);
    expect(seen).toEqual(["entry_delta"]);
  });

  it("markHydrated is idempotent and does not re-deliver", async () => {
    boot();
    const conn = await opened();
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    await settle();
    markHydrated();
    markHydrated();
    markHydrated();
    expect(seen).toEqual(["pending_snapshot"]);
  });

  it("releases what it held if hydration never reports in", async () => {
    // Fake timers that advance with the clock: the watchdog must be jumpable, the stream needs
    // real ticks.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    boot();
    const conn = await opened();
    conn.frame({ type: "pending_snapshot", payload: { items: [] } });
    await settle();
    expect(seen).toEqual([]);

    // The gate is an ordering aid: a hydration that never lands must not wedge the stream.
    vi.advanceTimersByTime(25_000);
    expect(seen).toEqual(["pending_snapshot"]);
  });
});
