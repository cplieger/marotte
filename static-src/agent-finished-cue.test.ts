// The agent-finished cue fires when the server's notice arrives and the chat holds no open turn
// and no ask: the server already withheld it while a run the chat launched was live. It fires
// when the last of those holds ends, whatever its outcome. Only `notify.ts` is mocked (it reads
// permission and visibility); the release is an effect over `chat-settled.ts`'s tracked reads,
// so the store is real.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { signal, touch } from "@cplieger/reactive";
import type { Session } from "./types.js";
import type { NotificationPayload } from "./wire/types.gen.js";

/** The server's notification for a finished turn on `chatID`. */
function finished(body: string, chatID = "c1"): NotificationPayload {
  return { id: `n-${chatID}`, chat_id: chatID, kind: "agent_finished", title: "test", body };
}

// The live-runs read answers the EMPTY inventory, at the file's next `runs` version: a run that
// ended during an outage.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async (path: string) => {
    if (path !== "/api/runs/live") {
      return null;
    }
    const { runsStamp } = await import("./__test-helpers__/live-runs.js");
    return { runs: [], subject: runsStamp() };
  }),
  // Status 0 is a read that never left, which is what a null answered before the store started
  // spending a failed read's status.
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
  isAgentFinishedEnabled: () => notify.enabled,
  notifyOffScreen: (n: NotificationPayload) => {
    notify.raised.push(n.body);
    return true;
  },
}));

const cue = await import("./agent-finished-cue.js");
const store = await import("./store.js");
const runStore = await import("./run-store.js");
const { frameRunGone, frameRunLive } = await import("./__test-helpers__/live-runs.js");

function setPending(chatID: string, on: boolean): void {
  if (on) {
    pendingChats.add(chatID);
  } else {
    pendingChats.delete(chatID);
  }
  pendingVersion.value = pendingVersion.peek() + 1;
}

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
    frameRunGone(id);
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
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // The immediate and deferred raises share ONE dedup map, which a reconnect's replayed
  // `turn_closed` burst needs.
  it("keeps the dedup window across a replayed burst", () => {
    cue.noteAgentFinished("c1", finished("Agent finished"));
    cue.noteAgentFinished("c1", finished("Agent finished"));
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("says nothing for an empty chat id", () => {
    cue.noteAgentFinished("", finished("Agent finished", ""));
    expect(notify.raised).toEqual([]);
  });

  it("refuses when the per-kind switch is off", () => {
    notify.enabled = false;
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });
});

describe("a chat with an open turn or an ask defers, then fires on settle", () => {
  it("raises nothing while the turn is open", () => {
    store.setTurnOpen("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    expect(notify.raised).toEqual([]);
    expect(cue.hasDeferredCue("c1")).toBe(true);
  });

  it("fires exactly once with the TURN's own body when the turn closes", () => {
    store.setTurnOpen("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(notify.raised).toEqual([]);

    store.setTurnOpen("c1", false);

    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // The runs are the server's to wait for, so a run ending under a parked cue is not a release.
  it("is not released by a run's end while the turn is still open", () => {
    frameRunLive("wf-a", "c1", true);
    store.setTurnOpen("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    frameRunGone("wf-a");
    expect(notify.raised).toEqual([]);

    store.setTurnOpen("c1", false);
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // A paused run is still a live lease, and the server withheld for it; a notice that arrives
  // was judged after it.
  it("does not park on a PAUSED run either", () => {
    frameRunLive("wf-a", "c1", false);
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  it("waits for the LAST of the turn and the ask", () => {
    store.setTurnOpen("c1", true);
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    store.setTurnOpen("c1", false);
    expect(notify.raised).toEqual([]);

    setPending("c1", false);
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // An inventory read that drops a run moves nothing the cue reads.
  it("stays parked on its turn when a read drops the chat's run", async () => {
    frameRunLive("wf-a", "c1", true);
    store.setTurnOpen("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    await runStore.refetchLiveRuns();
    expect(runStore.hasLiveRunForChat("c1")).toBe(false);
    expect(notify.raised).toEqual([]);

    store.setTurnOpen("c1", false);
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("still fires when an ASK is what was outstanding", () => {
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));
    expect(cue.hasDeferredCue("c1")).toBe(true);

    setPending("c1", false);
    expect(notify.raised).toEqual(["Agent finished"]);
  });

  it("does not release one chat's cue for another chat settling", () => {
    store.setTurnOpen("c1", true);
    store.setTurnOpen("c2", true);
    cue.noteAgentFinished("c1", finished("Agent finished on c1"));
    cue.noteAgentFinished("c2", finished("Agent finished on c2", "c2"));

    store.setTurnOpen("c2", false);

    expect(notify.raised).toEqual(["Agent finished on c2"]);
    expect(cue.hasDeferredCue("c1")).toBe(true);
  });
});

describe("the deferral cannot double-fire or fire wrongly", () => {
  // A replayed `turn_closed` parks by KEY, so a burst arriving while the chat is held
  // cannot produce two cues for one turn.
  it("a repeated park is one cue", () => {
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));
    cue.noteAgentFinished("c1", finished("Agent finished"));
    cue.noteAgentFinished("c1", finished("Agent finished"));

    setPending("c1", false);

    expect(notify.raised).toEqual(["Agent finished"]);
  });

  // The switch is re-read at RAISE time rather than trusted from the park, because a
  // deferral can outlive the reader's decision to be told.
  it("refuses at release when the switch was turned off meanwhile", () => {
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    notify.enabled = false;
    setPending("c1", false);

    expect(notify.raised).toEqual([]);
    // Removed BEFORE the raise: a cue that stayed parked through a refused raise
    // would be re-offered on every later pass for the rest of the page's life.
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });

  // A cue for a conversation that no longer exists can never be acted on.
  it("forgetDeferredCue means it never fires", () => {
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));

    cue.forgetDeferredCue("c1");
    expect(cue.hasDeferredCue("c1")).toBe(false);

    setPending("c1", false);
    expect(notify.raised).toEqual([]);
  });

  it("forgetDeferredCue on a chat with no parked cue is a no-op", () => {
    expect(() => {
      cue.forgetDeferredCue("c1");
    }).not.toThrow();
  });
});

// The server withholds `agent_finished` while the chat holds a live run and sends the run's own notice
// when it ends, so a notice that does arrive is not parked behind the client's copy of the runs.
describe("a live run does not park a notice the server sent", () => {
  it("an agent_finished notice is raised while the chat has a live run", () => {
    frameRunLive("wf-a", "c1", true);

    cue.deliverNotification("c1", finished("Agent finished"));

    expect(notify.raised).toEqual(["Agent finished"]);
    expect(cue.hasDeferredCue("c1")).toBe(false);
  });
});

// The release effect must read a signal BEFORE any chat is parked, or it read nothing and
// never runs again; the first park has to wake it.
describe("the subscriber wakes on the FIRST park", () => {
  it("releases a cue parked after the effect first ran empty", () => {
    // beforeEach installed the subscriber over an empty set, so its first pass read
    // no chat's signals at all.
    setPending("c1", true);
    cue.noteAgentFinished("c1", finished("Agent finished"));
    setPending("c1", false);

    expect(notify.raised).toEqual(["Agent finished"]);
  });
});
