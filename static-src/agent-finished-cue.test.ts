// The agent-finished cue fires when a turn ends and EVERYTHING it started is over: not at
// the turn's end while a run is live, and when the last outstanding thing ends, whatever
// its outcome. Only `notify.ts` is mocked (it reads permission and visibility); the
// release is an effect over `chat-settled.ts`'s tracked reads, so the store is real.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { signal, touch } from "@cplieger/reactive";
import type { Session } from "./types.js";

// The live-runs rebuild answers the EMPTY inventory: a run that ended during an outage.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async (path: string) => (path === "/api/runs/live" ? { runs: [] } : null)),
  // The run store's own read. Status 0 is a read that never left, which is what a null
  // answered before the store started spending a failed read's status.
  apiGetOrError: vi.fn(async () => ({ ok: false, status: 0, data: null, error: "" })),
}));

const pendingChats = new Set<string>();
const pendingVersion = signal(0);
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: (chatID: string) => {
    touch(pendingVersion);
    return pendingChats.has(chatID);
  },
}));

// Mutable, because production re-reads the gate at RAISE time: a cue parked while on and
// released after switching off must not fire.
const notify = vi.hoisted(() => ({
  raised: [] as string[],
  enabled: true,
}));
vi.mock("./notify.js", () => ({
  NOTIFY_TITLE: "Marotte",
  isAgentFinishedEnabled: () => notify.enabled,
  notifyIfHidden: (_title: string, body: string, _target: unknown) => {
    notify.raised.push(body);
    return true;
  },
}));

const cue = await import("./agent-finished-cue.js");
const store = await import("./store.js");
const runStore = await import("./run-store.js");

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    turn_open: false,
    working_label: "Thinking",
  };
}

const runIDs = ["wf-a", "wf-b"];
let stop: (() => void) | undefined;

beforeEach(() => {
  for (const id of runIDs) {
    runStore.noteRunSettled(id);
  }
  pendingChats.clear();
  notify.raised.length = 0;
  notify.enabled = true;
  store.setSessions([makeSession("c1"), makeSession("c2")]);
  cue._resetAgentFinishedCueForTest();
  stop = cue.installDeferredCueSubscriber();
});

afterEach(() => {
  stop?.();
  stop = undefined;
});

describe("a settled chat raises immediately", () => {
  it("raises the turn's own body", () => {
    cue.noteAgentFinished("c1", "Agent finished");
    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // The immediate and deferred raises share ONE dedup map, which a reconnect's replayed
  // `turn_closed` burst needs.
  it("keeps the dedup window across a replayed burst", () => {
    cue.noteAgentFinished("c1", "Agent finished");
    cue.noteAgentFinished("c1", "Agent finished");
    cue.noteAgentFinished("c1", "Agent finished");
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("says nothing for a turn with no body of its own", () => {
    cue.noteAgentFinished("c1", "");
    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  it("says nothing for an empty chat id", () => {
    cue.noteAgentFinished("", "Agent finished");
    expect(notify.raised).toEqual([]);
  });

  it("refuses when the per-kind switch is off", () => {
    notify.enabled = false;
    cue.noteAgentFinished("c1", "Agent finished");
    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });
});

describe("a chat with a live run defers, then fires on settle", () => {
  // `run_workflow` returns at the turn's end, long before the run is done.
  it("raises nothing while the run is live", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");

    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(true);
  });

  it("fires exactly once with the TURN's own body when the run terminates", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    expect(notify.raised).toEqual([]);

    runStore.noteRunSettled("wf-a");

    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // Every outcome arrives as the same terminal `run_finished` frame, so none may leave the
  // cue parked forever.
  it("releases on a run that FAILED just as on one that completed", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    // handlers/run.ts calls noteRunSettled for every non-paused terminal status, so
    // failed, aborted and cancelled reach the release through this one call.
    runStore.noteRunSettled("wf-a");
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // A PARKED run holds the cue: a run stopped on a person is precisely what a
  // "finished" notification must not claim is over.
  it("stays parked while the run is merely PAUSED", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    runStore.noteRunLive("wf-a", "c1", false); // paused, still live
    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(true);
  });

  it("waits for the LAST of several runs", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    runStore.noteRunLive("wf-b", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");

    runStore.noteRunSettled("wf-a");
    expect(notify.raised).toEqual([]);

    runStore.noteRunSettled("wf-b");
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // A `BUS_RECONCILE` rebuild that drops the run releases the cue with no lifecycle frame.
  it("releases when the run disappears from the inventory", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    runStore.rebuildLiveRuns();
    expect(notify.raised).toEqual([]); // the rebuild is async

    return vi.waitFor(() => {
      expect(notify.raised).toEqual(["Agent finished"]);
    });
  });

  it("still fires when the chat's own turn state is what was outstanding", () => {
    store.setTurnOpen("c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    expect(cue.hasDeferredCue("c1")).toBe(true);

    store.setTurnOpen("c1", false);
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("still fires when an ASK is what was outstanding", () => {
    pendingChats.add("c1");
    pendingVersion.value = pendingVersion.peek() + 1;
    cue.noteAgentFinished("c1", "Agent finished");
    expect(cue.hasDeferredCue("c1")).toBe(true);

    pendingChats.delete("c1");
    pendingVersion.value = pendingVersion.peek() + 1;
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("does not release one chat's cue for another chat settling", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    runStore.noteRunLive("wf-b", "c2", true);
    cue.noteAgentFinished("c1", "Agent finished on c1");
    cue.noteAgentFinished("c2", "Agent finished on c2");

    runStore.noteRunSettled("wf-b");

    expect(notify.raised).toEqual(["Agent finished on c2"]);
    expect(cue.hasDeferredCue("c1")).toBe(true);
  });
});

describe("the deferral cannot double-fire or fire wrongly", () => {
  // A replayed `turn_closed` parks by KEY, so a burst arriving while a run is live
  // cannot produce two cues for one turn.
  it("a repeated park is one cue", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    cue.noteAgentFinished("c1", "Agent finished");
    cue.noteAgentFinished("c1", "Agent finished");

    runStore.noteRunSettled("wf-a");

    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // The switch is re-read at RAISE time rather than trusted from the park, because a
  // deferral can outlive the reader's decision to be told.
  it("refuses at release when the switch was turned off meanwhile", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");

    notify.enabled = false;
    runStore.noteRunSettled("wf-a");

    expect(notify.raised).toEqual([]);
    // Removed BEFORE the raise: a cue that stayed parked through a refused raise
    // would be re-offered on every later pass for the rest of the page's life.
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // A cue for a conversation that no longer exists can never be acted on.
  it("forgetDeferredCue means it never fires", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");

    cue.forgetDeferredCue("c1");
    expect(cue.hasDeferredCue("c1")).toBe(false);

    runStore.noteRunSettled("wf-a");
    expect(notify.raised).toEqual([]);
  });

  it("forgetDeferredCue on a chat with no parked cue is a no-op", () => {
    expect(() => {
      cue.forgetDeferredCue("c1");
    }).not.toThrow();
  });
});

// The release effect must read a signal BEFORE any chat is parked, or it read nothing and
// never runs again; the first park has to wake it.
describe("the subscriber wakes on the FIRST park", () => {
  it("releases a cue parked after the effect first ran empty", () => {
    // beforeEach installed the subscriber over an empty set, so its first pass read
    // no chat's signals at all.
    runStore.noteRunLive("wf-a", "c1", true);
    cue.noteAgentFinished("c1", "Agent finished");
    runStore.noteRunSettled("wf-a");

    expect(notify.raised).toEqual(["Agent finished"]);
  });
});
