// Eviction and residency: the idle sweep that reclaims a background chat's turn window, the
// exemptions that each alone prevent it, the residency tri-state the next activation keys its
// refetch on, and what leaves with the window: the per-turn signals and the freshness record.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  setSessions,
  setActive,
  get,
  openTurn,
  appendEntry,
  openEntry,
  setThinking,
  removeChat,
  recordSteerQueued,
  steerCount,
  evictChatMessages,
  registerEvictionExemption,
  startEvictionSweep,
  stopEvictionSweep,
  defaultUsage,
  EVICT_SWEEP_MS,
  EVICT_IDLE_MS,
} from "./store.js";
import {
  entryTextSig,
  laneSig,
  laneSigs,
  laneKey,
  ensureToolCallSig,
  peekToolCallSig,
  clearAllEntrySigs,
} from "./store-signals.js";
import { _resetForTest as resetFreshness, observeStamp } from "./subject-versions.js";
import { viewStale } from "./view-freshness.js";
import type { Session, ToolCall } from "./types.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

function session(id: string, over: Partial<Session> = {}): Session {
  const base: Session = {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
  return Object.assign(base, over);
}

function turnOpen(turnID: string, n = 1): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n, prompt: { id: `p-${turnID}`, text: "go" } },
  };
}

function textEntry(turnID: string, seq: number): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind: "text",
    seq,
    ts: 2,
    payload: { text: "hi" },
  };
}

function toolCallEntry(turnID: string, seq: number, callID: string): Entry {
  const call: EntryToolCall = {
    id: callID,
    title: "Run Command",
    status: "in_progress",
    kind: "execute",
    ts: 1,
  };
  return { id: callID, turn: turnID, kind: "tool_call", seq, ts: 2, payload: call };
}

/** Seed a chat holding one turn with one sealed text entry. */
function resident(id: string): Session {
  const s = session(id);
  s.turns.set(`t-${id}`, {
    entries: [turnOpen(`t-${id}`), textEntry(`t-${id}`, 1)],
    openEntries: new Map(),
  });
  s.turn_order.push(`t-${id}`);
  s.turn_count = 1;
  return s;
}

const unregisters: (() => void)[] = [];
function exempt(fn: (chatID: string) => boolean): void {
  unregisters.push(registerEvictionExemption(fn));
}

beforeEach(() => {
  vi.useFakeTimers();
  clearAllEntrySigs();
});

afterEach(() => {
  stopEvictionSweep();
  for (const un of unregisters.splice(0)) {
    un();
  }
  setSessions([]);
  setActive("");
  vi.useRealTimers();
});

/** Seed two chats, make `active` the active one, open one turn on each (which stamps their
 *  activity), then age everything past the idle bound. */
function seedIdlePair(active: string, background: string): void {
  setSessions([session(active), session(background)]);
  setActive(active);
  openTurn(active, turnOpen(`t-${active}`));
  openTurn(background, turnOpen(`t-${background}`));
  vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
}

function tick(): void {
  vi.advanceTimersByTime(EVICT_SWEEP_MS);
}

describe("the idle sweep", () => {
  it("evicts an idle background chat's window and leaves the session row", () => {
    seedIdlePair("c-act", "c-bg");
    startEvictionSweep();
    tick();

    const s = get("c-bg");
    expect(s, "the session ROW must survive eviction").toBeDefined();
    expect(s?.turns.size).toBe(0);
    expect(s?.turn_order).toEqual([]);
    expect(s?.residency).toBe("evicted");
    // has_more re-derives from the record's turn count so the Load-older door stays honest.
    expect(s?.has_more).toBe(true);
    expect(s?.turn_count).toBe(1);
    expect(s?.name).toBe("c-bg");
  });

  it("does not run before the idle bound", () => {
    setSessions([session("c-act"), session("c-bg")]);
    setActive("c-act");
    openTurn("c-bg", turnOpen("t1"));
    startEvictionSweep();
    vi.advanceTimersByTime(EVICT_IDLE_MS - EVICT_SWEEP_MS);
    expect(get("c-bg")?.residency).toBeUndefined();
    expect(get("c-bg")?.turn_order).toEqual(["t1"]);
  });

  it("errs toward keeping a chat whose activity it never observed", () => {
    setSessions([session("c-act"), resident("c-unknown")]);
    setActive("c-act");
    startEvictionSweep();
    vi.advanceTimersByTime(EVICT_IDLE_MS * 3);
    expect(get("c-unknown")?.turn_order).toEqual(["t-c-unknown"]);
    expect(get("c-unknown")?.residency).toBeUndefined();
  });

  it("pauses while the document is hidden and resumes when visible", () => {
    seedIdlePair("c-act", "c-bg");
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    startEvictionSweep();
    tick();
    tick();
    expect(get("c-bg")?.residency, "a hidden tab must reclaim nothing").toBeUndefined();
    expect(get("c-bg")?.turn_order).toEqual(["t-c-bg"]);

    hidden.mockReturnValue(false);
    tick();
    expect(get("c-bg")?.residency).toBe("evicted");
  });
});

describe("the exemptions, each alone", () => {
  it("never evicts the ACTIVE chat", () => {
    seedIdlePair("c-act", "c-bg");
    startEvictionSweep();
    tick();
    expect(get("c-bg")?.residency).toBe("evicted");
    expect(get("c-act")?.residency).toBeUndefined();
    expect(get("c-act")?.turn_order).toEqual(["t-c-act"]);
  });

  it("never evicts a BUSY chat, however idle its clock reads", () => {
    seedIdlePair("c-act", "c-busy");
    setThinking("c-busy", true);
    // setThinking stamps activity, so age it past the bound again: the exemption itself must hold,
    // not the recency it implies.
    vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
    startEvictionSweep();
    tick();
    expect(get("c-busy")?.residency).toBeUndefined();
    expect(get("c-busy")?.turn_order).toEqual(["t-c-busy"]);
  });

  it("never evicts a chat a registered predicate names, and evicts its sibling", () => {
    setSessions([session("c-act"), session("c-kept"), session("c-bg")]);
    setActive("c-act");
    openTurn("c-kept", turnOpen("t-kept"));
    openTurn("c-bg", turnOpen("t-bg"));
    vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
    exempt((chatID) => chatID === "c-kept");
    startEvictionSweep();
    tick();
    expect(get("c-kept")?.residency).toBeUndefined();
    expect(get("c-bg")?.residency).toBe("evicted");
  });

  it("never evicts a chat holding a waiting steer, because such a chat is busy", () => {
    seedIdlePair("c-act", "c-steer");
    setThinking("c-steer", true);
    recordSteerQueued("c-steer", { id: "steer-1", text: "waiting", origin: "user" });
    vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
    startEvictionSweep();
    tick();
    expect(get("c-steer")?.residency).toBeUndefined();
    expect(steerCount("c-steer")).toBe(1);
  });
});

describe("residency", () => {
  it("a turn opened on an evicted chat marks it PARTIAL, never loaded", () => {
    setSessions([resident("c1")]);
    evictChatMessages("c1");
    expect(get("c1")?.residency).toBe("evicted");

    openTurn("c1", turnOpen("t-late", 2));
    expect(get("c1")?.residency).toBe("partial");
    expect(get("c1")?.turn_order).toEqual(["t-late"]);
  });

  it("an entry appended on an evicted chat marks it PARTIAL too", () => {
    setSessions([resident("c1")]);
    evictChatMessages("c1");
    openTurn("c1", turnOpen("t-late", 2));
    appendEntry("c1", textEntry("t-late", 1));
    expect(get("c1")?.residency).toBe("partial");
  });

  it("ingest on a chat that is not evicted claims nothing", () => {
    setSessions([session("c1")]);
    openTurn("c1", turnOpen("t1"));
    appendEntry("c1", textEntry("t1", 1));
    expect(get("c1")?.residency).toBeUndefined();
  });

  it("leaves the steer dock intact when it evicts a window", () => {
    setSessions([resident("c1")]);
    setActive("c1");
    recordSteerQueued("c1", { id: "steer-waiting", text: "still waiting", origin: "user" });

    evictChatMessages("c1");

    expect(get("c1")?.turns.size, "the window goes").toBe(0);
    expect(get("c1")?.residency).toBe("evicted");
    expect(steerCount("c1"), "the dock stays").toBe(1);
  });
});

describe("the signals the window minted", () => {
  function seedSignals(chatID: string): void {
    setSessions([session(chatID)]);
    openTurn(chatID, turnOpen("t1"));
    appendEntry(chatID, toolCallEntry("t1", 1, "call-1"));
    openEntry(chatID, { turn: "t1", id: "open-1", lane: "", kind: "text", text: "x", n: 1 });
    ensureToolCallSig(chatID, "call-1", { id: "call-1" } as ToolCall);
    expect(entryTextSig("t1", "open-1")).toBeDefined();
    expect(laneSigs.get(laneKey("t1", ""))).toBeDefined();
  }

  it("eviction clears the streaming, lane and tool-call signals", () => {
    seedSignals("c1");
    evictChatMessages("c1");
    expect(entryTextSig("t1", "open-1")).toBeUndefined();
    expect(laneSigs.get(laneKey("t1", ""))).toBeUndefined();
    expect(peekToolCallSig("c1", "call-1")).toBeUndefined();
  });

  it("removeChat clears them too, without waiting for a render pass", () => {
    seedSignals("c1");
    removeChat("c1");
    expect(entryTextSig("t1", "open-1")).toBeUndefined();
    expect(laneSigs.get(laneKey("t1", ""))).toBeUndefined();
    expect(peekToolCallSig("c1", "call-1")).toBeUndefined();
  });

  it("clearing one chat's signals leaves a sibling chat's alone", () => {
    setSessions([session("c1"), session("c2")]);
    openTurn("c1", turnOpen("t1"));
    openTurn("c2", turnOpen("t2"));
    laneSig("t2", "");
    ensureToolCallSig("c2", "call-2", { id: "call-2" } as ToolCall);

    evictChatMessages("c1");

    expect(laneSigs.get(laneKey("t1", ""))).toBeUndefined();
    expect(laneSigs.get(laneKey("t2", ""))).toBeDefined();
    expect(peekToolCallSig("c2", "call-2")).toBeDefined();
  });
});

// A ledger record describes the window, so the window's death drops it; that is what makes the
// dispatcher's `viewStale`-only gate equivalent to `transcriptStale`. Without the drop an evicted
// chat whose record still matched would be skipped on its next activation.
describe("the ledger record eviction drops", () => {
  beforeEach(() => {
    resetFreshness();
  });

  it("evictChatMessages drops the record, so a re-activation refetches", () => {
    setSessions([resident("c1")]);
    observeStamp({ kind: "chat", ref: "c1", version: "1" });
    expect(viewStale("chat", "c1")).toBe(false);

    evictChatMessages("c1");

    expect(viewStale("chat", "c1")).toBe(true);
  });

  it("removeChat drops it too: the subject is gone, not just its window", () => {
    setSessions([resident("c1")]);
    observeStamp({ kind: "chat", ref: "c1", version: "1" });

    removeChat("c1");

    expect(viewStale("chat", "c1")).toBe(true);
  });
});
