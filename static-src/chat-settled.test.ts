// "Is everything this chat started actually over?": the three terms of `chatSettled`, the two of
// `noticeSettled`, and the property both release on, that every read is TRACKED. The dock is
// mocked with a real signal behind it, so a predicate that stopped subscribing fails.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal, touch, effect } from "@cplieger/reactive";
import type { Session } from "./types.js";

// run-store fetches through the API client on invalidation; nothing here invalidates,
// so the mock exists only to keep a real `fetch` off the wire.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async () => null),
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

function setPending(chatID: string, on: boolean): void {
  if (on) {
    pendingChats.add(chatID);
  } else {
    pendingChats.delete(chatID);
  }
  pendingVersion.value = pendingVersion.peek() + 1;
}

const { chatOutstanding, chatSettled, noticeSettled } = await import("./chat-settled.js");
const store = await import("./store.js");
const { frameRunGone, frameRunLive } = await import("./__test-helpers__/live-runs.js");

// `provisional` unset and `turn_open` absent: a provisional row reads as LIVE.
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

const runIDs = ["wf-a", "wf-b", "wf-parentless"];

beforeEach(() => {
  for (const id of runIDs) {
    frameRunGone(id);
  }
  pendingChats.clear();
  store.setSessions([makeSession("c1"), makeSession("c2")]);
});

describe("chatSettled", () => {
  it("is true for an idle chat with nothing outstanding", () => {
    expect(chatSettled("c1")).toBe(true);
  });

  it("is false while a run this chat launched is EXECUTING", () => {
    frameRunLive("wf-a", "c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  // A parked run's `executing` is false, so reading that would call a chat waiting on a person
  // finished.
  it("is false while a run this chat launched is PARKED", () => {
    frameRunLive("wf-a", "c1", false);
    expect(chatSettled("c1")).toBe(false);
  });

  it("is false while a decision sits in this chat's dock queue", () => {
    setPending("c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  it("is false while the chat's own turn is live", () => {
    store.setTurnOpen("c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  // `turnLive` reads a row that states NO liveness as running, so a provisional row is
  // outstanding too — a boot-snapshot hint must not be read as "the turn is over".
  it("is false for a row that states no liveness at all", () => {
    const provisional: Session = { ...makeSession("c3"), provisional: true };
    store.setSessions([provisional]);
    expect(chatSettled("c3")).toBe(false);
  });

  it("becomes true once the run terminates", () => {
    frameRunLive("wf-a", "c1", true);
    expect(chatSettled("c1")).toBe(false);
    frameRunGone("wf-a");
    expect(chatSettled("c1")).toBe(true);
  });

  it("is unmoved by another chat's live run", () => {
    frameRunLive("wf-b", "c2", true);
    expect(chatSettled("c1")).toBe(true);
    expect(chatSettled("c2")).toBe(false);
  });

  // A parentless run names no chat and holds nobody's cue.
  it("is unmoved by a PARENTLESS run", () => {
    frameRunLive("wf-parentless", "", true);
    expect(chatSettled("c1")).toBe(true);
  });

  // An unknown chat has no session, no live run and no queue, so a caller needs no
  // second existence check before asking.
  it("is true for a chat that does not exist", () => {
    expect(chatSettled("nope")).toBe(true);
  });

  it("is true for the empty id, which is not a chat", () => {
    expect(chatSettled("")).toBe(true);
  });
});

describe("chatOutstanding names the reason", () => {
  it("reports each term independently", () => {
    frameRunLive("wf-a", "c1", true);
    frameRunLive("wf-b", "c1", false);
    setPending("c1", true);
    store.setTurnOpen("c1", true);

    expect(chatOutstanding("c1")).toEqual({ turn: true, runs: 2, asks: true });
  });

  it("reports a run-only hold as a run-only hold", () => {
    frameRunLive("wf-a", "c1", true);
    expect(chatOutstanding("c1")).toEqual({ turn: false, runs: 1, asks: false });
  });

  it("reports nothing outstanding for an idle chat", () => {
    expect(chatOutstanding("c1")).toEqual({ turn: false, runs: 0, asks: false });
  });
});

// The release depends on every term being a tracked read: an untracked one leaves a cue
// parked forever, invisibly.
describe("every term is a tracked read", () => {
  function watch(chatID: string): { settled: boolean[]; dispose: () => void } {
    const settled: boolean[] = [];
    const dispose = effect(() => {
      settled.push(chatSettled(chatID));
    });
    return { settled, dispose };
  }

  it("re-runs when a run starts and when it settles", () => {
    const w = watch("c1");
    expect(w.settled).toEqual([true]);

    frameRunLive("wf-a", "c1", true);
    expect(w.settled.at(-1)).toBe(false);

    frameRunGone("wf-a");
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  it("re-runs when an ask arrives and when it is answered", () => {
    const w = watch("c1");
    setPending("c1", true);
    expect(w.settled.at(-1)).toBe(false);
    setPending("c1", false);
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  it("re-runs when the chat's own turn state moves", () => {
    const w = watch("c1");
    store.setTurnOpen("c1", true);
    expect(w.settled.at(-1)).toBe(false);
    store.setTurnOpen("c1", false);
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  // The empty-id path still touches the inventory's version, so a later change can wake it.
  it("subscribes even for a chat with nothing to read", () => {
    const w = watch("");
    const before = w.settled.length;
    frameRunLive("wf-a", "c1", true);
    expect(w.settled.length).toBeGreaterThan(before);
    w.dispose();
  });
});

// The server withholds `agent_finished` for a live run, so the notice's own predicate leaves the
// runs out and keeps the two holds it does not wait for.
describe("noticeSettled", () => {
  it("is true while a run this chat launched is live, executing or parked", () => {
    frameRunLive("wf-a", "c1", true);
    frameRunLive("wf-b", "c1", false);
    expect(noticeSettled("c1")).toBe(true);
    expect(chatSettled("c1")).toBe(false);
  });

  it("is false while the chat's own turn is live", () => {
    store.setTurnOpen("c1", true);
    expect(noticeSettled("c1")).toBe(false);
  });

  it("is false while a decision sits in this chat's dock queue", () => {
    setPending("c1", true);
    expect(noticeSettled("c1")).toBe(false);
  });

  it("re-runs when the turn or the ask moves", () => {
    const seen: boolean[] = [];
    const dispose = effect(() => {
      seen.push(noticeSettled("c1"));
    });
    store.setTurnOpen("c1", true);
    expect(seen.at(-1)).toBe(false);
    store.setTurnOpen("c1", false);
    setPending("c1", true);
    expect(seen.at(-1)).toBe(false);
    setPending("c1", false);
    expect(seen.at(-1)).toBe(true);
    dispose();
  });
});
