// Unit tests for store.ts, the ENTRY-LOG store: the five operations over `session.turns`, the
// header re-sync, the dock projection, the dot vocabulary and the per-chat repaint causes.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fc from "fast-check";
import {
  parseContextSize,
  setSessions,
  getSessions,
  get,
  setActive,
  getActive,
  getActiveId,
  watchActiveId,
  activeSession,
  upsertHeader,
  removeChat,
  reinsertSession,
  indexOfSession,
  setThinking,
  setWorkingLabel,
  setName,
  setModel,
  setCurrentMode,
  setSupervisedMode,
  setEffort,
  setAgentStatus,
  setTurnOpen,
  turnLive,
  isThinking,
  isEmptyChat,
  derivedHasMore,
  chatHoldingTurn,
  hasMessage,
  transcriptStale,
  outcomeLatch,
  tabStatusFor,
  subagentStatusFor,
  runStatusFor,
  defaultUsage,
  evictChatMessages,
  registerEvictionExemption,
  startEvictionSweep,
  stopEvictionSweep,
  EVICT_IDLE_MS,
  EVICT_SWEEP_MS,
  markWindowStale,
  registerTurnRepair,
  openTurn,
  appendEntry,
  openEntry,
  applyDelta,
  sealEntry,
  bumpMessages,
  messagesVersionOf,
  renderCauseOf,
  steerIDFor,
  steerCount,
  recordSteerSent,
  forgetSteer,
  recordSteerQueued,
  dropConfirmedSteers,
  restoreSteers,
  forgetSteers,
  markSteersCompacted,
  setCodeReferences,
  codeReferencesFor,
  setLiveRefusal,
  liveRefusalFor,
  clearLiveTurnFacts,
  applyToolProgress,
  foldToolCallDelta,
  settledToolCall,
  republishWindowToolCalls,
} from "./store.js";
import {
  ensureToolCallSig,
  peekToolCallSig,
  toolCallSigs,
  toolCallSigKey,
  entryTextSig,
  laneSig,
  clearAllEntrySigs,
} from "./store-signals.js";
import { _resetForTest as resetFreshness, observeStamp } from "./subject-versions.js";
import type { ChatHeader, Session, ToolCall, ToolProgressPayload } from "./types.js";
import type { Entry, EntryToolCall, EntryToolResult, TurnOutcome } from "./wire/types.gen.js";
import { effect } from "@cplieger/reactive";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
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
}

function resetStore(chatID: string): void {
  setSessions([makeSession(chatID)]);
  setActive(chatID);
}

/** A minimal server header for `chatID`. `model` is deliberately absent, which is the shape the
 *  wire produces for a chat whose model the server has not been told yet (`Model` is
 *  `omitempty`). */
function headerFor(chatID: string): ChatHeader {
  return {
    id: chatID,
    name: chatID,
    usage: defaultUsage(),
    turn_count: 0,
    created_at: 0,
    updated_at: 0,
  };
}

/** The `turn_open` that opens turn `turnID`, at session-absolute ordinal `n`. A prompt id makes
 *  it a reader-opened turn, which is what `hasMessage` addresses. */
function turnOpenEntry(turnID: string, n: number, promptID?: string): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: {
      source: promptID === undefined ? "wire_turn_start" : "prompt",
      n,
      ...(promptID !== undefined && { prompt: { id: promptID, text: "hi" } }),
    },
  };
}

/** A sealed entry of any kind at `seq`. `lane` is `""` unless named, which is the transcript's
 *  own lane. */
function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  return {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    ...(opts.lane !== undefined && { lane: opts.lane }),
    payload,
  };
}

function textEntry(turnID: string, seq: number, text: string, lane?: string): Entry {
  return sealed(turnID, seq, "text", { text }, lane === undefined ? {} : { lane });
}

// `Object.assign` rather than a spread: under `exactOptionalPropertyTypes` a spread of a `Partial`
// widens every required field to include `undefined`, which the target type refuses.
function toolCall(id: string, over: Partial<EntryToolCall> = {}): EntryToolCall {
  const base: EntryToolCall = {
    id,
    title: "Run Command",
    status: "in_progress",
    kind: "execute",
    ts: 1,
  };
  return Object.assign(base, over);
}

function toolResult(over: Partial<EntryToolResult> = {}): EntryToolResult {
  const base: EntryToolResult = { status: "completed" };
  return Object.assign(base, over);
}

/** Open a turn and return its id, so a case reads as one line of arrange. */
function openTurnIn(chatID: string, turnID: string, n = 1, promptID?: string): string {
  openTurn(chatID, turnOpenEntry(turnID, n, promptID));
  return turnID;
}

/** The repair calls `store.ts` asked for, in order. Installed per test so a case can assert that
 *  a hole was DETECTED without the loader being present. */
let repairs: { chatID: string; turnID: string; afterSeq?: number }[] = [];

beforeEach(() => {
  repairs = [];
  registerTurnRepair((chatID, turnID, afterSeq) => {
    repairs.push(afterSeq === undefined ? { chatID, turnID } : { chatID, turnID, afterSeq });
  });
  resetFreshness();
  clearAllEntrySigs();
  toolCallSigs.clearAll();
});

afterEach(() => {
  registerTurnRepair(() => {
    // No repair recorded outside a case that installed its own collector.
  });
});

describe("parseContextSize reads a window size out of a model's own description", () => {
  const cases: [string, number | undefined][] = [
    ["200K context", 200_000],
    ["200k context", 200_000],
    ["1M context", 1_000_000],
    ["2M context", 2_000_000],
    ["Claude with 200K context window", 200_000],
    ["no numbers here", undefined],
    ["", undefined],
    ["200Kcontext", 200_000],
    ["200 K context", 200_000],
    ["200  K  context", 200_000],
    ["200K  Context", 200_000],
  ];
  for (const [input, expected] of cases) {
    it(`reads ${JSON.stringify(input)} as ${String(expected)}`, () => {
      expect(parseContextSize(input)).toBe(expected);
    });
  }
});

describe("the active session", () => {
  it("follows the active id", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    expect(getActive()?.id).toBe("a");
    setActive("b");
    expect(getActive()?.id).toBe("b");
  });

  it("answers undefined when nothing is active", () => {
    setSessions([makeSession("a")]);
    setActive("");
    expect(getActive()).toBeUndefined();
    expect(activeSession.value).toBeUndefined();
  });

  it("never resolves the no-active sentinel to a row whose id is empty", () => {
    const ghost = makeSession("");
    setSessions([ghost, makeSession("a")]);
    setActive("");
    expect(activeSession.value).toBeUndefined();
  });

  it("re-fires on the ACTIVE session's field change and not on another's", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    let runs = 0;
    const stop = effect(() => {
      void activeSession.value?.name;
      runs += 1;
    });
    const base = runs;
    setName("b", "other");
    expect(runs).toBe(base);
    setName("a", "mine");
    expect(runs).toBe(base + 1);
    stop();
  });

  it("recovers when the active id is set before the session exists", () => {
    setSessions([]);
    setActive("later");
    expect(activeSession.value).toBeUndefined();
    upsertHeader(headerFor("later"));
    expect(activeSession.value?.id).toBe("later");
  });

  it("reads the active id, tracked and untracked", () => {
    resetStore("a");
    expect(getActiveId()).toBe("a");
    expect(watchActiveId()).toBe("a");
  });
});

describe("the session collection stays consistent under arbitrary add and remove", () => {
  it("keeps every id's index in sync with the list", () => {
    fc.assert(
      fc.property(
        fc.array(
          fc.record({
            op: fc.constantFrom("add", "remove") as fc.Arbitrary<"add" | "remove">,
            id: fc.constantFrom("a", "b", "c", "d"),
          }),
          { maxLength: 24 },
        ),
        (ops) => {
          setSessions([]);
          for (const { op, id } of ops) {
            if (op === "add") {
              upsertHeader(headerFor(id));
            } else {
              removeChat(id);
            }
          }
          const ids = getSessions().map((s) => s.id);
          for (const [i, id] of ids.entries()) {
            expect(indexOfSession(id)).toBe(i);
          }
          expect(new Set(ids).size).toBe(ids.length);
        },
      ),
    );
  });
});

describe("removeChat", () => {
  it("activates the next remaining chat when the active one goes", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    removeChat("a");
    expect(getActiveId()).toBe("b");
  });

  it("leaves the active chat alone when a different one goes", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    removeChat("b");
    expect(getActiveId()).toBe("a");
  });

  it("does not switch chats for an id that has no row", () => {
    resetStore("a");
    removeChat("nope");
    expect(getActiveId()).toBe("a");
  });

  it("drops the removed chat's per-entry signals with it", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1")));
    ensureToolCallSig("a", "c1", toolCall("c1"));
    expect(peekToolCallSig("a", "c1")).toBeDefined();
    removeChat("a");
    expect(peekToolCallSig("a", "c1")).toBeUndefined();
  });

  it("re-derives the active session ONCE when the active chat goes", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    let runs = 0;
    const stop = effect(() => {
      void activeSession.value?.id;
      runs += 1;
    });
    const base = runs;
    removeChat("a");
    expect(runs).toBe(base + 1);
    stop();
  });
});

describe("reinsertSession", () => {
  it("restores a removed chat at the index it held", () => {
    setSessions([makeSession("a"), makeSession("b"), makeSession("c")]);
    const b = get("b");
    removeChat("b");
    reinsertSession(b as Session, 1);
    expect(getSessions().map((s) => s.id)).toEqual(["a", "b", "c"]);
  });

  it("puts it at the head when no index is named", () => {
    setSessions([makeSession("a")]);
    reinsertSession(makeSession("z"));
    expect(getSessions().map((s) => s.id)).toEqual(["z", "a"]);
  });

  it("clamps an index past the end to the end", () => {
    setSessions([makeSession("a")]);
    reinsertSession(makeSession("z"), 99);
    expect(getSessions().map((s) => s.id)).toEqual(["a", "z"]);
  });

  it("clamps a negative index to the head", () => {
    setSessions([makeSession("a")]);
    reinsertSession(makeSession("z"), -5);
    expect(getSessions().map((s) => s.id)).toEqual(["z", "a"]);
  });

  it("is idempotent: a chat already present is left where it is", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    reinsertSession(makeSession("b"), 0);
    expect(getSessions().map((s) => s.id)).toEqual(["a", "b"]);
  });
});

describe("openTurn", () => {
  it("creates the turn with its turn_open as entries[0] and appends the turn order", () => {
    resetStore("a");
    openTurn("a", turnOpenEntry("t1", 1));
    const state = get("a")?.turns.get("t1");
    expect(state?.entries.map((e) => e.kind)).toEqual(["turn_open"]);
    expect(state?.entries[0]?.seq).toBe(0);
    expect(get("a")?.turn_order).toEqual(["t1"]);
  });

  it("is idempotent by turn id, so a replayed frame changes nothing", () => {
    resetStore("a");
    openTurn("a", turnOpenEntry("t1", 1));
    appendEntry("a", textEntry("t1", 1, "hello"));
    openTurn("a", turnOpenEntry("t1", 1));
    expect(get("a")?.turns.get("t1")?.entries).toHaveLength(2);
    expect(get("a")?.turn_order).toEqual(["t1"]);
  });

  it("adopts turn_open.n as the session's turn count, never lowering it", () => {
    resetStore("a");
    openTurn("a", turnOpenEntry("t9", 9));
    expect(get("a")?.turn_count).toBe(9);
    openTurn("a", turnOpenEntry("t3", 3));
    expect(get("a")?.turn_count).toBe(9);
  });

  it("derives has_more from the count against what is resident", () => {
    resetStore("a");
    openTurn("a", turnOpenEntry("t5", 5));
    expect(get("a")?.has_more).toBe(true);
    openTurn("a", turnOpenEntry("t6", 1));
    openTurn("a", turnOpenEntry("t7", 1));
    openTurn("a", turnOpenEntry("t8", 1));
    openTurn("a", turnOpenEntry("t9", 1));
    expect(get("a")?.has_more).toBe(false);
  });

  it("is a no-op for a chat the store does not hold", () => {
    setSessions([]);
    openTurn("ghost", turnOpenEntry("t1", 1));
    expect(get("ghost")).toBeUndefined();
  });

  it("bumps the chat's version synchronously, because a new card must be in the frame", () => {
    resetStore("a");
    const before = messagesVersionOf("a").peek();
    openTurn("a", turnOpenEntry("t1", 1));
    expect(messagesVersionOf("a").peek()).toBe(before + 1);
    expect(renderCauseOf("a").cause).toBe("shape");
  });
});

describe("appendEntry", () => {
  it("appends an entry whose seq is exactly the next one", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", textEntry(turn, 1, "one"));
    appendEntry("a", textEntry(turn, 2, "two"));
    expect(
      get("a")
        ?.turns.get(turn)
        ?.entries.map((e) => e.seq),
    ).toEqual([0, 1, 2]);
    expect(repairs).toEqual([]);
  });

  it("reads an entry naming a turn it does not hold as a hole, and asks for the WHOLE turn", () => {
    resetStore("a");
    appendEntry("a", textEntry("unseen", 3, "x"));
    expect(repairs).toEqual([{ chatID: "a", turnID: "unseen" }]);
    expect(get("a")?.residency).toBeUndefined();
  });

  it("reads a seq that is not next as a hole, and asks for the range above what it holds", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", textEntry(turn, 1, "one"));
    appendEntry("a", textEntry(turn, 4, "four"));
    expect(repairs).toEqual([{ chatID: "a", turnID: turn, afterSeq: 1 }]);
    expect(get("a")?.turns.get(turn)?.entries).toHaveLength(2);
  });

  it("drops a REDELIVERY rather than asking for a range read", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    const e = textEntry(turn, 1, "one");
    appendEntry("a", e);
    appendEntry("a", e);
    expect(get("a")?.turns.get(turn)?.entries).toHaveLength(2);
    expect(repairs).toEqual([]);
  });

  it("marks the window stale on a hole, so the next activation refetches", () => {
    resetStore("a");
    const s = get("a") as Session;
    s.residency = "loaded";
    appendEntry("a", textEntry("unseen", 1, "x"));
    expect(get("a")?.residency).toBe("partial");
  });

  it("caches where the turn_close landed, which is what liveness reads", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    expect(get("a")?.turns.get(turn)?.closeAt).toBeUndefined();
    appendEntry("a", sealed(turn, 1, "turn_close", { outcome: "completed" }));
    expect(get("a")?.turns.get(turn)?.closeAt).toBe(1);
  });

  it("takes a dock row out on that steer's own entry, whatever its state", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    recordSteerSent("a", "m1", "wait");
    const steerID = steerIDFor("m1");
    expect(steerCount("a")).toBe(1);
    appendEntry(
      "a",
      sealed(turn, 1, "steer", { text: "wait", origin: "user", state: "dropped" }, { id: steerID }),
    );
    expect(steerCount("a")).toBe(0);
  });

  it("publishes a tool_result's settled value at the card its call mounted", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    ensureToolCallSig("a", "c1", toolCall("c1"));
    appendEntry(
      "a",
      sealed(turn, 2, "tool_result", toolResult({ output: "done" }), { id: "c1:result" }),
    );
    expect(peekToolCallSig("a", "c1")?.status).toBe("completed");
    expect(peekToolCallSig("a", "c1")?.output).toBe("done");
  });

  it("bumps the lane signal for the entry's own lane", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    const before = laneSig(turn, "sub-1").peek();
    appendEntry("a", textEntry(turn, 1, "delegate says", "sub-1"));
    expect(laneSig(turn, "sub-1").peek()).toBe(before + 1);
  });

  it("earns a shape pass for a lane-less entry and a chunk pass for a laned one", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", textEntry(turn, 1, "top"));
    expect(renderCauseOf("a").cause).toBe("shape");
    appendEntry("a", textEntry(turn, 2, "delegate", "sub-1"));
    // A laned entry coalesces, so its cause is parked on the microtask rather than flushed.
    expect(renderCauseOf("a").cause).toBe("shape");
  });
});

describe("the coalescing trio", () => {
  it("stores one open entry per lane with the count it arrived with", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    const open = get("a")?.turns.get(turn)?.openEntries.get("");
    expect(open?.text).toBe("He");
    expect(open?.n).toBe(1);
  });

  it("mints the streaming signal an unmounted surface follows", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    expect(entryTextSig(turn, "e1")?.peek().full).toBe("He");
  });

  it("extends the open entry when n is exactly the next one", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    applyDelta("a", turn, "e1", "", 2, "llo");
    expect(get("a")?.turns.get(turn)?.openEntries.get("")?.text).toBe("Hello");
    expect(entryTextSig(turn, "e1")?.peek()).toEqual({ full: "Hello", delta: "llo" });
    expect(repairs).toEqual([]);
  });

  it("reads a delta whose n is not the next one as a hole", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    applyDelta("a", turn, "e1", "", 4, "llo");
    expect(get("a")?.turns.get(turn)?.openEntries.get("")?.text).toBe("He");
    expect(repairs).toEqual([{ chatID: "a", turnID: turn, afterSeq: 0 }]);
  });

  it("reads a delta naming an entry the lane is not coalescing as the same hole", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    applyDelta("a", turn, "other", "", 2, "llo");
    expect(repairs).toEqual([{ chatID: "a", turnID: turn, afterSeq: 0 }]);
  });

  it("reads a frame naming a turn it does not hold as the whole-turn hole", () => {
    resetStore("a");
    openEntry("a", { turn: "unseen", id: "e1", kind: "text", text: "x", n: 1 });
    applyDelta("a", "unseen", "e1", "", 2, "y");
    sealEntry("a", "unseen", "e1", "", 1, 5, 2);
    expect(repairs).toEqual([
      { chatID: "a", turnID: "unseen" },
      { chatID: "a", turnID: "unseen" },
      { chatID: "a", turnID: "unseen" },
    ]);
  });

  it("seals the open entry into the log, so the seq check applies to the seal too", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    applyDelta("a", turn, "e1", "", 2, "llo");
    sealEntry("a", turn, "e1", "", 1, 42, 2);
    const state = get("a")?.turns.get(turn);
    expect(state?.openEntries.size).toBe(0);
    expect(state?.entries[1]).toMatchObject({
      id: "e1",
      turn,
      kind: "text",
      seq: 1,
      ts: 42,
      payload: { text: "Hello" },
    });
    expect(entryTextSig(turn, "e1")).toBeUndefined();
  });

  it("reads a seal whose n disagrees with the held count as a hole, keeping the open entry", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    sealEntry("a", turn, "e1", "", 1, 5, 7);
    expect(get("a")?.turns.get(turn)?.openEntries.get("")?.text).toBe("He");
    expect(get("a")?.turns.get(turn)?.entries).toHaveLength(1);
    expect(repairs).toEqual([{ chatID: "a", turnID: turn, afterSeq: 0 }]);
  });

  it("reads a seal whose seq is not next as the append's own hole", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "He", n: 1 });
    sealEntry("a", turn, "e1", "", 5, 5, 1);
    expect(get("a")?.turns.get(turn)?.entries).toHaveLength(1);
    expect(repairs).toEqual([{ chatID: "a", turnID: turn, afterSeq: 0 }]);
  });

  it("keeps two lanes' open entries apart", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "top", kind: "text", text: "A", n: 1 });
    openEntry("a", { turn, id: "sub", lane: "sub-1", kind: "text", text: "B", n: 1 });
    applyDelta("a", turn, "sub", "sub-1", 2, "C");
    const state = get("a")?.turns.get(turn);
    expect(state?.openEntries.get("")?.text).toBe("A");
    expect(state?.openEntries.get("sub-1")?.text).toBe("BC");
  });

  it("replaces a lane's open entry when a second one opens in it", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", kind: "text", text: "A", n: 1 });
    openEntry("a", { turn, id: "e2", kind: "thinking", text: "B", n: 1 });
    const state = get("a")?.turns.get(turn);
    expect(state?.openEntries.size).toBe(1);
    expect(state?.openEntries.get("")?.id).toBe("e2");
  });

  it("carries the kind through the seal, which is what tells text from thinking", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "r1", kind: "thinking", text: "hmm", n: 1 });
    sealEntry("a", turn, "r1", "", 1, 5, 1);
    expect(get("a")?.turns.get(turn)?.entries[1]?.kind).toBe("thinking");
  });
});

describe("markWindowStale", () => {
  it("flips a loaded window partial, and leaves any other residency alone", () => {
    resetStore("a");
    const s = get("a") as Session;
    s.residency = "loaded";
    markWindowStale("a");
    expect(get("a")?.residency).toBe("partial");
    s.residency = "evicted";
    markWindowStale("a");
    expect(get("a")?.residency).toBe("evicted");
  });

  it("is a no-op for a chat the store does not hold", () => {
    setSessions([]);
    expect(() => {
      markWindowStale("ghost");
    }).not.toThrow();
  });
});

describe("hasMessage answers off turn_open.prompt.id, the one place a prompt id is addressed", () => {
  it("finds a prompt this client sent", () => {
    resetStore("a");
    openTurnIn("a", "t1", 1, "m-abc");
    expect(hasMessage("a", "m-abc")).toBe(true);
  });

  it("answers false for a prompt id no resident turn opened", () => {
    resetStore("a");
    openTurnIn("a", "t1", 1, "m-abc");
    expect(hasMessage("a", "m-other")).toBe(false);
  });

  it("answers false for an agent-initiated turn, which carries no prompt", () => {
    resetStore("a");
    openTurnIn("a", "t1", 1);
    expect(hasMessage("a", "m-abc")).toBe(false);
  });

  it("answers false for a chat it does not hold", () => {
    setSessions([]);
    expect(hasMessage("ghost", "m-abc")).toBe(false);
  });
});

describe("turnLive: liveness is the log", () => {
  it("is true while a resident turn carries no turn_close", () => {
    resetStore("a");
    openTurnIn("a", "t1");
    expect(turnLive(get("a") as Session)).toBe(true);
  });

  it("is false once every resident turn is closed and the server says nothing", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "turn_close", { outcome: "completed" }));
    expect(turnLive(get("a") as Session)).toBe(false);
  });

  it("is true on the server's own statement for a window that is not resident", () => {
    resetStore("a");
    setTurnOpen("a", true);
    expect(turnLive(get("a") as Session)).toBe(true);
  });

  it("is true for a provisional row, which states neither input", () => {
    resetStore("a");
    const s = get("a") as Session;
    s.provisional = true;
    expect(turnLive(s)).toBe(true);
  });

  it("does NOT read thinking, so a latch can never outrank an open turn", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "turn_close", { outcome: "completed" }));
    setThinking("a", true);
    expect(turnLive(get("a") as Session)).toBe(false);
  });

  it("drops the server's statement when a NEW turn starts", () => {
    resetStore("a");
    setTurnOpen("a", false);
    expect(get("a")?.turn_open).toBe(false);
    setThinking("a", true);
    expect(get("a")?.turn_open).toBeUndefined();
  });

  it("does not churn the session on a repeated statement", () => {
    resetStore("a");
    setTurnOpen("a", true);
    const before = get("a");
    setTurnOpen("a", true);
    expect(get("a")).toBe(before);
  });
});

describe("chatHoldingTurn", () => {
  it("names the chat whose window holds the turn", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    openTurnIn("b", "t9");
    expect(chatHoldingTurn("t9")).toBe("b");
  });

  it("answers empty for a turn no resident window holds", () => {
    resetStore("a");
    expect(chatHoldingTurn("t9")).toBe("");
  });
});

describe("window edges", () => {
  it("derivedHasMore compares the record's count against what is resident", () => {
    expect(derivedHasMore(0, 0)).toBe(false);
    expect(derivedHasMore(3, 3)).toBe(false);
    expect(derivedHasMore(4, 3)).toBe(true);
  });

  it("isEmptyChat needs BOTH halves: nothing on the record and nothing resident", () => {
    resetStore("a");
    expect(isEmptyChat(get("a"))).toBe(true);
    openTurnIn("a", "t1", 1);
    expect(isEmptyChat(get("a"))).toBe(false);
  });

  it("isEmptyChat reads an absent chat as empty, so callers need no null check", () => {
    expect(isEmptyChat(undefined)).toBe(true);
  });
});

describe("outcomeLatch grades a persisted outcome for the dot", () => {
  const cases: [TurnOutcome | undefined, "done" | "failed" | ""][] = [
    ["completed", "done"],
    ["empty", "done"],
    ["cancelled", "done"],
    ["interrupted", "failed"],
    ["unknown", "done"],
    ["failed", "failed"],
    ["refused", "failed"],
    ["running", ""],
    [undefined, ""],
  ];
  for (const [outcome, want] of cases) {
    it(`maps ${outcome ?? "an absent outcome"} to ${want === "" ? "nothing" : want}`, () => {
      expect(outcomeLatch(outcome)).toBe(want);
    });
  }
});

describe("tabStatusFor", () => {
  function idleChat(outcome?: TurnOutcome): Session {
    const s = makeSession("a");
    if (outcome !== undefined) {
      s.last_turn_outcome = outcome;
    }
    s.turn_open = false;
    return s;
  }

  it("answers nothing at all for a chat it does not hold", () => {
    expect(tabStatusFor(undefined)).toBe("");
  });

  it("puts a question ahead of everything", () => {
    expect(tabStatusFor(idleChat("failed"), true)).toBe("input");
  });

  it("reads a failed newest turn as failed while nothing is live", () => {
    expect(tabStatusFor(idleChat("failed"))).toBe("failed");
  });

  it("gates failed on liveness, so a running turn is never painted red", () => {
    resetStore("a");
    const s = get("a") as Session;
    s.last_turn_outcome = "failed";
    openTurnIn("a", "t1");
    expect(tabStatusFor(get("a"))).toBe("working");
  });

  it("reads a declared waiting_on_user as waiting once the turn has ended", () => {
    const s = idleChat("completed");
    s.agent_status = "waiting_on_user";
    expect(tabStatusFor(s)).toBe("waiting");
  });

  it("reads a completed newest turn as done", () => {
    expect(tabStatusFor(idleChat("completed"))).toBe("done");
  });

  it("keeps a chat that has never initiated on the idle floor", () => {
    expect(tabStatusFor(idleChat())).toBe("idle");
  });
});

describe("runStatusFor", () => {
  it("answers nothing for a run it has not fetched", () => {
    expect(runStatusFor(undefined)).toBe("");
  });

  it("puts an unanswered ask ahead of every status", () => {
    expect(runStatusFor("completed", true)).toBe("input");
  });

  it("reads a park on a person as input and any other park as waiting", () => {
    expect(runStatusFor("paused", false, "need_input")).toBe("input");
    expect(runStatusFor("paused")).toBe("waiting");
  });

  it("reads a cancelled run as done, because the reader asked for the stop", () => {
    expect(runStatusFor("cancelled")).toBe("done");
    expect(runStatusFor("completed")).toBe("done");
  });

  it("reads failed and aborted as failed", () => {
    expect(runStatusFor("failed")).toBe("failed");
    expect(runStatusFor("aborted")).toBe("failed");
  });

  it("folds an unrecognised status toward saying nothing rather than idle", () => {
    expect(runStatusFor("unknown")).toBe("");
  });
});

describe("subagentStatusFor maps a delegate's invocation status to its dot", () => {
  const cases: [ToolCall["status"], string][] = [
    ["pending", "working"],
    ["in_progress", "working"],
    ["completed", "done"],
    ["aborted", "done"],
    ["failed", "failed"],
  ];
  for (const [status, want] of cases) {
    it(`maps ${status} to ${want}`, () => {
      expect(subagentStatusFor(status)).toBe(want);
    });
  }

  it("answers nothing at all when this client holds no invocation", () => {
    expect(subagentStatusFor(undefined)).toBe("");
  });
});

describe("upsertHeader re-syncs an existing row", () => {
  it("never lowers the turn count the client has already seen", () => {
    resetStore("a");
    const s = get("a") as Session;
    s.turn_count = 7;
    upsertHeader({ ...headerFor("a"), turn_count: 3 });
    expect(get("a")?.turn_count).toBe(7);
  });

  it("leaves a locally-chosen model alone when the header omits it", () => {
    resetStore("a");
    setModel("a", "opus");
    upsertHeader(headerFor("a"));
    expect(get("a")?.model).toBe("opus");
  });

  it("does not read an empty model string as a clear either", () => {
    resetStore("a");
    setModel("a", "opus");
    upsertHeader({ ...headerFor("a"), model: "" });
    expect(get("a")?.model).toBe("opus");
  });

  it("overwrites the local model when the header names one", () => {
    resetStore("a");
    setModel("a", "opus");
    upsertHeader({ ...headerFor("a"), model: "sonnet" });
    expect(get("a")?.model).toBe("sonnet");
  });

  it("reads an ABSENT last_turn_outcome as a CLEAR, unlike model", () => {
    resetStore("a");
    upsertHeader({ ...headerFor("a"), last_turn_outcome: "failed" });
    expect(get("a")?.last_turn_outcome).toBe("failed");
    upsertHeader(headerFor("a"));
    expect(get("a")?.last_turn_outcome).toBeUndefined();
  });

  it("takes pending_model in both directions, which is what clears the badge everywhere", () => {
    resetStore("a");
    upsertHeader({ ...headerFor("a"), pending_model: "opus" });
    expect(get("a")?.pending_model).toBe("opus");
    upsertHeader(headerFor("a"));
    expect(get("a")?.pending_model).toBe("");
  });

  it("keeps the tier list when the header has no session catalog to report", () => {
    resetStore("a");
    upsertHeader({ ...headerFor("a"), effort_levels: [{ id: "max", name: "Max" }] });
    upsertHeader(headerFor("a"));
    expect(get("a")?.effort_levels).toHaveLength(1);
  });

  it("adopts a compaction watermark and drops it when the header stops carrying one", () => {
    resetStore("a");
    upsertHeader({ ...headerFor("a"), compaction_watermark: "w1" });
    expect(get("a")?.compaction_watermark).toBe("w1");
    upsertHeader(headerFor("a"));
    expect(get("a")?.compaction_watermark).toBeUndefined();
  });

  it("leaves supervised mode off when the header does not mention it", () => {
    resetStore("a");
    upsertHeader(headerFor("a"));
    expect(get("a")?.supervised_mode).toBe(false);
  });

  // The follow-up list and the mode are header-authoritative: a header without them is steer and no
  // rows, which is how a drained or discarded row leaves every device.
  it("takes the interrupt mode and the queued rows in both directions", () => {
    resetStore("a");
    upsertHeader({
      ...headerFor("a"),
      interrupt_mode: "queue",
      queued_prompts: [{ id: "m-q1", text: "then add tests" }],
    });
    expect(get("a")?.interrupt_mode).toBe("queue");
    expect(get("a")?.queued?.map((q) => q.id)).toEqual(["m-q1"]);

    upsertHeader(headerFor("a"));
    expect(get("a")?.interrupt_mode).toBe("steer");
    expect(get("a")?.queued).toEqual([]);
  });
});

describe("upsertHeader seeds a brand-new row", () => {
  it("seeds it idle, unsupervised and with an empty window", () => {
    setSessions([]);
    upsertHeader(headerFor("a"));
    const s = get("a") as Session;
    expect(s.thinking).toBe(false);
    expect(s.supervised_mode).toBe(false);
    expect(s.turns.size).toBe(0);
    expect(s.turn_order).toEqual([]);
  });

  it("seeds the interrupt mode and the queued rows from the header", () => {
    setSessions([]);
    upsertHeader({
      ...headerFor("a"),
      interrupt_mode: "queue",
      queued_prompts: [{ id: "m-q1", text: "held", held: true }],
    });
    expect(get("a")?.interrupt_mode).toBe("queue");
    expect(get("a")?.queued?.[0]?.held).toBe(true);
  });

  it("derives has_more from the record's count, because a header carries no window", () => {
    setSessions([]);
    upsertHeader({ ...headerFor("a"), turn_count: 4 });
    expect(get("a")?.has_more).toBe(true);
    upsertHeader(headerFor("b"));
    expect(get("b")?.has_more).toBe(false);
  });

  it("carries the header's own outcome onto the new row", () => {
    setSessions([]);
    upsertHeader({ ...headerFor("a"), last_turn_outcome: "failed" });
    expect(get("a")?.last_turn_outcome).toBe("failed");
  });
});

describe("the per-chat switches", () => {
  it("setCurrentMode applies a new mode and does not churn on the one it has", () => {
    resetStore("a");
    setCurrentMode("a", "spec");
    expect(get("a")?.current_mode_id).toBe("spec");
    const before = get("a");
    setCurrentMode("a", "spec");
    expect(get("a")).toBe(before);
  });

  it("setSupervisedMode applies a new value and does not churn on the one it has", () => {
    resetStore("a");
    setSupervisedMode("a", true);
    expect(get("a")?.supervised_mode).toBe(true);
    const before = get("a");
    setSupervisedMode("a", true);
    expect(get("a")).toBe(before);
  });

  it("setEffort applies a level and reads an absent one as empty", () => {
    resetStore("a");
    setEffort("a", "max");
    expect(get("a")?.effort).toBe("max");
    const before = get("a");
    setEffort("a", "max");
    expect(get("a")).toBe(before);
    setSessions([makeSession("b")]);
    const fresh = get("b");
    setEffort("b", "");
    expect(get("b")).toBe(fresh);
  });

  it("setName updates the name and no-ops on an unknown chat", () => {
    resetStore("a");
    setName("a", "renamed");
    expect(get("a")?.name).toBe("renamed");
    expect(() => {
      setName("ghost", "x");
    }).not.toThrow();
  });

  it("setModel refreshes the derived context size in the same update", () => {
    resetStore("a");
    setModel("a", "unknown-model");
    expect(get("a")?.usage.context_size).toBe(0);
  });
});

describe("setThinking and setWorkingLabel", () => {
  it("resets the working label when a turn ends", () => {
    resetStore("a");
    setThinking("a", true);
    setWorkingLabel("a", "Reading files");
    setThinking("a", false);
    expect(get("a")?.working_label).toBe("Thinking");
  });

  it("keeps the label across a start, so the resume control has a fallback", () => {
    resetStore("a");
    setWorkingLabel("a", "Reading files");
    setThinking("a", true);
    expect(get("a")?.working_label).toBe("Reading files");
  });

  it("clears the agent's declared status when a new turn starts", () => {
    resetStore("a");
    setAgentStatus("a", "waiting_on_user");
    setThinking("a", true);
    expect(get("a")?.agent_status).toBeUndefined();
  });

  it("isThinking reads false for a chat it has never heard of", () => {
    setSessions([]);
    expect(isThinking("ghost")).toBe(false);
  });
});

describe("setAgentStatus", () => {
  it("records the status and takes a new one", () => {
    resetStore("a");
    setAgentStatus("a", "in_progress");
    expect(get("a")?.agent_status).toBe("in_progress");
    setAgentStatus("a", "waiting_on_user");
    expect(get("a")?.agent_status).toBe("waiting_on_user");
  });

  it("does not churn the session on a repeated frame", () => {
    resetStore("a");
    setAgentStatus("a", "in_progress");
    const before = get("a");
    setAgentStatus("a", "in_progress");
    expect(get("a")).toBe(before);
  });

  it("reads an empty status as a clear, and deletes the field", () => {
    resetStore("a");
    setAgentStatus("a", "in_progress");
    setAgentStatus("a", "");
    expect(get("a")?.agent_status).toBeUndefined();
  });

  it("does not churn a chat that never had one when an empty status arrives", () => {
    resetStore("a");
    const before = get("a");
    setAgentStatus("a", "");
    expect(get("a")).toBe(before);
  });
});

describe("the steer dock holds what the agent has NOT read", () => {
  beforeEach(() => {
    resetStore("a");
  });

  it("derives the id KAS will return, which is what lets a plain id match reconcile", () => {
    expect(steerIDFor("m1")).toBe("steer-m1");
  });

  it("records a submitted steer as pending, keyed by the derived id", () => {
    recordSteerSent("a", "m1", "wait");
    expect(get("a")?.steers).toEqual([
      { id: "steer-m1", text: "wait", origin: "user", pending: true },
    ]);
  });

  it("is idempotent by message id, so a retried submit adds no second row", () => {
    recordSteerSent("a", "m1", "wait");
    recordSteerSent("a", "m1", "wait harder");
    expect(get("a")?.steers).toHaveLength(1);
    expect(get("a")?.steers?.[0]?.text).toBe("wait harder");
  });

  it("ignores a submit with no message id rather than creating an unaddressable row", () => {
    recordSteerSent("a", "", "wait");
    expect(steerCount("a")).toBe(0);
  });

  it("forgets one pending row and leaves a sibling alone", () => {
    recordSteerSent("a", "m1", "one");
    recordSteerSent("a", "m2", "two");
    forgetSteer("a", "steer-m1");
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["steer-m2"]);
  });

  it("deletes the field when the forgotten row was the last one", () => {
    recordSteerSent("a", "m1", "one");
    forgetSteer("a", "steer-m1");
    expect(get("a")?.steers).toBeUndefined();
  });

  it("is a no-op when the id to forget is not held", () => {
    recordSteerSent("a", "m1", "one");
    const before = get("a");
    forgetSteer("a", "steer-other");
    expect(get("a")).toBe(before);
  });

  it("records a queued steer as waiting, with no pending flag", () => {
    recordSteerQueued("a", { id: "steer-x", text: "hi", origin: "user" });
    expect(get("a")?.steers).toEqual([{ id: "steer-x", text: "hi", origin: "user" }]);
  });

  it("confirms the pending row in place when the ids agree", () => {
    recordSteerSent("a", "m1", "wait");
    recordSteerQueued("a", { id: "steer-m1", text: "wait", origin: "user" });
    expect(get("a")?.steers).toEqual([{ id: "steer-m1", text: "wait", origin: "user" }]);
  });

  it("adopts a server id onto the pending row when only the text matches", () => {
    recordSteerSent("a", "m1", "wait");
    recordSteerQueued("a", { id: "kas-99", text: "wait", origin: "user" });
    expect(get("a")?.steers).toEqual([{ id: "kas-99", text: "wait", origin: "user" }]);
  });

  it("adopts onto the OLDEST pending row of equal text, leaving the newer pending", () => {
    recordSteerSent("a", "m1", "wait");
    recordSteerSent("a", "m2", "wait");
    recordSteerQueued("a", { id: "kas-99", text: "wait", origin: "user" });
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["kas-99", "steer-m2"]);
    expect(get("a")?.steers?.[1]?.pending).toBe(true);
  });

  it("is idempotent by id across a replayed frame", () => {
    recordSteerQueued("a", { id: "steer-x", text: "hi", origin: "user" });
    recordSteerQueued("a", { id: "steer-x", text: "hi", origin: "user" });
    expect(get("a")?.steers).toHaveLength(1);
  });

  it("ignores a queued frame with an empty id", () => {
    recordSteerQueued("a", { id: "", text: "hi", origin: "user" });
    expect(steerCount("a")).toBe(0);
  });

  it("takes the frame's origin over this device's claim", () => {
    recordSteerSent("a", "m1", "wait");
    recordSteerQueued("a", { id: "steer-m1", text: "wait", origin: "agent" });
    expect(get("a")?.steers?.[0]?.origin).toBe("agent");
  });

  it("does NOT put a steer back in the dock when the log already holds its entry", () => {
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(turn, 1, "steer", { text: "hi", origin: "user", state: "read" }, { id: "steer-x" }),
    );
    recordSteerQueued("a", { id: "steer-x", text: "hi", origin: "user" });
    expect(steerCount("a")).toBe(0);
  });

  it("takes a row out on its removed frame and leaves its neighbours", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user", state: "removed" });
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["s2"]);
  });

  it("marks a row the server could not deliver as unsent, in place", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user", state: "unsent" });
    expect(get("a")?.steers?.map((e) => [e.id, e.unsent])).toEqual([
      ["s1", true],
      ["s2", undefined],
    ]);
  });

  it("clears the unsent mark when the row is queued again", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user", state: "unsent" });
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user", state: "queued" });
    expect(get("a")?.steers?.[0]?.unsent).toBeUndefined();
  });

  it("records a batch's id on its members without adding a row", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    recordSteerQueued("a", { id: "s3", text: "three", origin: "user" });
    recordSteerQueued("a", {
      id: "steer-b1",
      text: "one\n\nthree",
      origin: "user",
      replaces: ["s1", "s3"],
    });
    expect(get("a")?.steers?.map((e) => [e.id, e.text, e.kas])).toEqual([
      ["s1", "one", "steer-b1"],
      ["s2", "two", undefined],
      ["s3", "three", "steer-b1"],
    ]);
  });

  it("retires every member of a batch on the batch's own steer entry", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    recordSteerQueued("a", {
      id: "steer-b1",
      text: "one\n\ntwo",
      origin: "user",
      replaces: ["s1", "s2"],
    });
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(
        turn,
        1,
        "steer",
        { text: "one\n\ntwo", origin: "user", state: "read" },
        { id: "steer-b1" },
      ),
    );
    expect(get("a")?.steers).toBeUndefined();
  });

  it("retires a batch's members at once when the log already holds the batch's entry", () => {
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(
        turn,
        1,
        "steer",
        { text: "one\n\ntwo", origin: "user", state: "read" },
        { id: "steer-b1" },
      ),
    );
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    recordSteerQueued("a", {
      id: "steer-b1",
      text: "one\n\ntwo",
      origin: "user",
      replaces: ["s1", "s2"],
    });
    expect(get("a")?.steers).toBeUndefined();
  });

  it("retires the rows a steer entry names as its resends", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(
        turn,
        1,
        "steer",
        { text: "two", origin: "user", state: "read", resends: ["s2"] },
        { id: "steer-other" },
      ),
    );
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["s1"]);
  });

  it("takes the confirmed rows out on an explicit discard and keeps the pending one", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerSent("a", "m2", "two");
    const snapshot = dropConfirmedSteers("a");
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["steer-m2"]);
    expect(snapshot.map((e) => e.id)).toEqual(["s1", "steer-m2"]);
  });

  it("reports nothing removed when every row is still pending", () => {
    recordSteerSent("a", "m1", "one");
    expect(dropConfirmedSteers("a")).toEqual([]);
    expect(steerCount("a")).toBe(1);
  });

  it("restores a discard snapshot in its original order", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    const snapshot = dropConfirmedSteers("a");
    restoreSteers("a", snapshot);
    expect(get("a")?.steers?.map((e) => e.id)).toEqual(["s1", "s2"]);
  });

  it("forgets the dock on a gap without asserting anything about what was read", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    forgetSteers("a");
    expect(get("a")?.steers).toBeUndefined();
  });

  it("marks every row then in the dock as sent across a compaction", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    markSteersCompacted("a");
    recordSteerQueued("a", { id: "s2", text: "two", origin: "user" });
    expect(get("a")?.steers?.map((e) => e.compacted)).toEqual([true, undefined]);
  });

  it("is idempotent: a second compaction over the same rows changes nothing", () => {
    recordSteerQueued("a", { id: "s1", text: "one", origin: "user" });
    markSteersCompacted("a");
    const before = get("a");
    markSteersCompacted("a");
    expect(get("a")).toBe(before);
  });
});

describe("the per-chat transcript version", () => {
  it("counts up by one per explicit bump, on the named chat only", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    const a = messagesVersionOf("a").peek();
    const b = messagesVersionOf("b").peek();
    bumpMessages("a", "shape");
    expect(messagesVersionOf("a").peek()).toBe(a + 1);
    expect(messagesVersionOf("b").peek()).toBe(b);
  });

  it("coalesces a tick's worth of laned growth into one repaint", async () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", lane: "sub-1", kind: "text", text: "A", n: 1 });
    const before = messagesVersionOf("a").peek();
    applyDelta("a", turn, "e1", "sub-1", 2, "B");
    applyDelta("a", turn, "e1", "sub-1", 3, "C");
    await Promise.resolve();
    expect(messagesVersionOf("a").peek()).toBe(before + 1);
  });

  it("schedules again once the deferred repaint has run", async () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", lane: "sub-1", kind: "text", text: "A", n: 1 });
    const before = messagesVersionOf("a").peek();
    applyDelta("a", turn, "e1", "sub-1", 2, "B");
    await Promise.resolve();
    applyDelta("a", turn, "e1", "sub-1", 3, "C");
    await Promise.resolve();
    expect(messagesVersionOf("a").peek()).toBe(before + 2);
  });

  it("a background chat's stream bumps its own version, never the active chat's", async () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    const turn = openTurnIn("b", "t1");
    const a = messagesVersionOf("a").peek();
    openEntry("b", { turn, id: "e1", kind: "text", text: "A", n: 1 });
    await Promise.resolve();
    expect(messagesVersionOf("a").peek()).toBe(a);
  });

  it("a chat removed before its deferred flush is skipped, not re-minted", async () => {
    setSessions([makeSession("a"), makeSession("b")]);
    setActive("a");
    const turn = openTurnIn("b", "t1");
    openEntry("b", { turn, id: "e1", lane: "sub-1", kind: "text", text: "A", n: 1 });
    removeChat("b");
    await Promise.resolve();
    expect(messagesVersionOf("b").peek()).toBe(0);
  });

  it("lets a load bump win a window a shape cause is still parked in", async () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    openEntry("a", { turn, id: "e1", lane: "sub-1", kind: "text", text: "A", n: 1 });
    bumpMessages("a", "load");
    expect(renderCauseOf("a").cause).toBe("load");
    await Promise.resolve();
  });

  it("reads a chat it has flushed nothing for as needing the full pass", () => {
    setSessions([makeSession("fresh")]);
    expect(renderCauseOf("fresh")).toEqual({ cause: "shape" });
  });
});

describe("transcriptStale: the activation refetch gate", () => {
  function loadedChat(): Session {
    resetStore("a");
    const s = get("a") as Session;
    s.residency = "loaded";
    observeStamp({ kind: "chat", ref: "a", version: "1" });
    return s;
  }

  it("a loaded window whose version is held is fresh", () => {
    expect(transcriptStale(loadedChat())).toBe(false);
  });

  it("a never-loaded chat is stale by construction", () => {
    resetStore("a");
    expect(transcriptStale(get("a") as Session)).toBe(true);
  });

  it("an evicted window is stale whatever its stamp says", () => {
    const s = loadedChat();
    s.residency = "evicted";
    expect(transcriptStale(s)).toBe(true);
  });

  it("a partial window is stale whatever its stamp says", () => {
    const s = loadedChat();
    s.residency = "partial";
    expect(transcriptStale(s)).toBe(true);
  });

  it("a load_failed window is stale whatever its stamp says", () => {
    const s = loadedChat();
    s.residency = "load_failed";
    expect(transcriptStale(s)).toBe(true);
  });

  it("forgetting the held version flips a fresh window stale", () => {
    const s = loadedChat();
    resetFreshness();
    expect(transcriptStale(s)).toBe(true);
  });
});

describe("evictChatMessages", () => {
  it("drops the window and keeps the session row, so header data survives", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t3", 3);
    appendEntry("a", textEntry(turn, 1, "x"));
    evictChatMessages("a");
    const s = get("a") as Session;
    expect(s.turns.size).toBe(0);
    expect(s.turn_order).toEqual([]);
    expect(s.turn_count).toBe(3);
    expect(s.residency).toBe("evicted");
    expect(s.has_more).toBe(true);
  });

  it("takes the window's per-entry signals with it", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    ensureToolCallSig("a", "c1", toolCall("c1"));
    evictChatMessages("a");
    expect(peekToolCallSig("a", "c1")).toBeUndefined();
  });

  it("is a no-op for a chat it does not hold", () => {
    setSessions([]);
    expect(() => {
      evictChatMessages("ghost");
    }).not.toThrow();
  });
});

describe("registerEvictionExemption", () => {
  // The sweep is the exemption's only reader, so the timer is what makes either half observable:
  // the predicate and its unregister move nothing else in the store.
  it("keeps an exempt chat resident, and its unregister hands the window back", () => {
    vi.useFakeTimers();
    try {
      setSessions([makeSession("a"), makeSession("b")]);
      setActive("a");
      openTurnIn("b", "t1");
      const stop = registerEvictionExemption((chatID) => chatID === "b");
      startEvictionSweep();
      vi.advanceTimersByTime(EVICT_IDLE_MS + EVICT_SWEEP_MS);
      expect(get("b")?.turn_order).toEqual(["t1"]);
      stop();
      vi.advanceTimersByTime(EVICT_SWEEP_MS);
      expect(get("b")?.turn_order).toEqual([]);
      expect(get("b")?.residency).toBe("evicted");
    } finally {
      stopEvictionSweep();
      vi.useRealTimers();
    }
  });
});

describe("the live turn facts", () => {
  it("keys code references by TURN, and only for a turn the window holds", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    setCodeReferences("a", turn, [{ license_name: "MIT", repository: "r", url: "u" }]);
    expect(codeReferencesFor("a", turn)).toHaveLength(1);
    setCodeReferences("a", "unseen", [{ license_name: "MIT", repository: "r", url: "u" }]);
    expect(codeReferencesFor("a", "unseen")).toBeUndefined();
  });

  it("replaces rather than appends, because the server sends the full deduped list", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    setCodeReferences("a", turn, [{ license_name: "MIT", repository: "r", url: "u" }]);
    setCodeReferences("a", turn, []);
    expect(codeReferencesFor("a", turn)).toEqual([]);
  });

  it("stamps a refusal ONCE per turn, so a second frame cannot restate it", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    setLiveRefusal("a", turn, { category: "policy" });
    setLiveRefusal("a", turn, { category: "other" });
    expect(liveRefusalFor("a", turn)?.category).toBe("policy");
  });

  it("ignores a refusal for a turn the window does not hold", () => {
    resetStore("a");
    setLiveRefusal("a", "unseen", { category: "policy" });
    expect(liveRefusalFor("a", "unseen")).toBeUndefined();
  });

  it("clears both, because the turn's own close carries them durably", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    setCodeReferences("a", turn, [{ license_name: "MIT", repository: "r", url: "u" }]);
    setLiveRefusal("a", turn, { category: "policy" });
    clearLiveTurnFacts("a");
    expect(codeReferencesFor("a", turn)).toBeUndefined();
    expect(liveRefusalFor("a", turn)).toBeUndefined();
  });
});

describe("settledToolCall joins a call with its result", () => {
  it("returns the call as created when no result has landed", () => {
    const call = toolCall("c1");
    expect(settledToolCall(call, undefined)).toBe(call);
  });

  it("lets the result's status win, because the call's is a starting state", () => {
    const merged = settledToolCall(toolCall("c1", { status: "in_progress" }), toolResult());
    expect(merged.status).toBe("completed");
  });

  it("takes each field the result carries and leaves the rest of the call alone", () => {
    const merged = settledToolCall(
      toolCall("c1", { title: "Run Command" }),
      toolResult({ output: "hi", duration_ms: 12 }),
    );
    expect(merged.title).toBe("Run Command");
    expect(merged.output).toBe("hi");
    expect(merged.duration_ms).toBe(12);
  });

  it("latches declined one way, so a later verdict-free result cannot clear it", () => {
    const merged = settledToolCall(toolCall("c1", { declined: true }), toolResult());
    expect(merged.declined).toBe(true);
  });

  it("carries the result's offloaded output file onto the card", () => {
    const offload = {
      path: "/k/sessions/cli/sess_1/tool-outputs/shell-0a1b2c3d.txt",
      total_chars: 9,
    };
    expect(settledToolCall(toolCall("c1"), toolResult({ offload })).offload).toEqual(offload);
  });

  it("carries the result's answered interaction onto the card", () => {
    const interaction = { type: "tool_approval", outcome: "selected", choice: "allow_always" };
    expect(settledToolCall(toolCall("c1"), toolResult({ interaction })).interaction).toEqual(
      interaction,
    );
  });
});

describe("foldToolCallDelta", () => {
  function delta(over: Partial<ToolProgressPayload> = {}): ToolProgressPayload {
    return { turn: "t1", tool_call_id: "c1", ...over };
  }

  it("accumulates output by default", () => {
    const next = foldToolCallDelta(toolCall("c1", { output: "ab" }), delta({ output_delta: "cd" }));
    expect(next.output).toBe("abcd");
  });

  it("reads output_replace as authoritative on its own, even with no delta at all", () => {
    const next = foldToolCallDelta(
      toolCall("c1", { output: "ab" }),
      delta({ output_replace: true }),
    );
    expect(next.output).toBe("");
  });

  it("leaves the output alone when the frame carries none", () => {
    const next = foldToolCallDelta(toolCall("c1", { output: "ab" }), delta({ title: "x" }));
    expect(next.output).toBe("ab");
  });

  it("appends diffs rather than replacing them", () => {
    const next = foldToolCallDelta(
      toolCall("c1", { diffs: [{ path: "a", old_text: "", new_text: "1" }] }),
      delta({ diffs_appended: [{ path: "b", old_text: "", new_text: "2" }] }),
    );
    expect(next.diffs?.map((d) => d.path)).toEqual(["a", "b"]);
  });

  it("latches declined one way", () => {
    const next = foldToolCallDelta(toolCall("c1", { declined: true }), delta({ status: "failed" }));
    expect(next.declined).toBe(true);
  });

  it("returns a fresh object, because the card's signal dedups by identity", () => {
    const prev = toolCall("c1");
    expect(foldToolCallDelta(prev, delta({ title: "x" }))).not.toBe(prev);
  });
});

describe("applyToolProgress", () => {
  it("folds onto the signal a card subscribes to, seeded from the call's own payload", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    ensureToolCallSig("a", "c1", toolCall("c1"));
    const next = applyToolProgress("a", turn, {
      turn,
      tool_call_id: "c1",
      output_delta: "hello",
    });
    expect(next?.output).toBe("hello");
    expect(peekToolCallSig("a", "c1")?.output).toBe("hello");
  });

  it("reads the call off the entry when no signal has been minted yet", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    expect(applyToolProgress("a", turn, { turn, tool_call_id: "c1", title: "x" })?.title).toBe("x");
  });

  it("answers undefined for a call this window does not hold, which is the handler's own gap signal", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    expect(applyToolProgress("a", turn, { turn, tool_call_id: "nope" })).toBeUndefined();
    expect(
      applyToolProgress("a", "unseen", { turn: "unseen", tool_call_id: "c1" }),
    ).toBeUndefined();
  });
});

describe("republishWindowToolCalls", () => {
  it("replaces a mounted card's value with the fetched call", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(turn, 1, "tool_call", toolCall("c1", { output: "full" }), { id: "c1" }),
    );
    ensureToolCallSig("a", "c1", toolCall("c1", { output: "trunc" }));
    republishWindowToolCalls("a", [turn]);
    expect(peekToolCallSig("a", "c1")?.output).toBe("full");
  });

  it("publishes a call's SETTLED value, which is its tool_result", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    appendEntry(
      "a",
      sealed(turn, 2, "tool_result", toolResult({ output: "done" }), { id: "c1:result" }),
    );
    ensureToolCallSig("a", "c1", toolCall("c1"));
    republishWindowToolCalls("a", [turn]);
    expect(peekToolCallSig("a", "c1")?.status).toBe("completed");
  });

  it("publishes nothing at a card already showing what the fetch carries", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry(
      "a",
      sealed(turn, 1, "tool_call", toolCall("c1", { output: "same" }), { id: "c1" }),
    );
    ensureToolCallSig("a", "c1", toolCall("c1", { output: "same" }));
    const shown = peekToolCallSig("a", "c1");
    republishWindowToolCalls("a", [turn]);
    expect(peekToolCallSig("a", "c1")).toBe(shown);
  });

  it("publishes at a card whose only difference is the answered interaction", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    const interaction = { type: "tool_approval", outcome: "selected", choice: "reject_once" };
    appendEntry(
      "a",
      sealed(turn, 2, "tool_result", toolResult({ interaction }), { id: "c1:result" }),
    );
    const shown = { type: "tool_approval", outcome: "selected", choice: "allow_once" };
    ensureToolCallSig(
      "a",
      "c1",
      settledToolCall(toolCall("c1"), toolResult({ interaction: shown })),
    );
    republishWindowToolCalls("a", [turn]);
    expect(peekToolCallSig("a", "c1")?.interaction).toEqual(interaction);
  });

  it("mints no signal for a call nothing has mounted", () => {
    resetStore("a");
    const turn = openTurnIn("a", "t1");
    appendEntry("a", sealed(turn, 1, "tool_call", toolCall("c1"), { id: "c1" }));
    republishWindowToolCalls("a", [turn]);
    expect(toolCallSigs.get(toolCallSigKey("a", "c1"))).toBeUndefined();
  });

  it("skips a turn the window does not hold", () => {
    resetStore("a");
    expect(() => {
      republishWindowToolCalls("a", ["unseen"]);
    }).not.toThrow();
  });
});

describe("defaultUsage", () => {
  it("seeds usage as not-yet-measured, so a ring does not claim a reading", () => {
    expect(defaultUsage()).toEqual({
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    });
  });
});
