// The per-call SILENCE VALUE: `Date.now()`-derived and reading no entry `ts`, so it is never
// ordering state. The CARD is `tool-card-silence.test.ts`'s.
import { describe, it, expect, beforeEach, afterEach } from "vitest";

import {
  SILENCE_THRESHOLD_MS,
  clearToolSilence,
  noteToolActivity,
  silenceLabel,
  silenceMsFor,
} from "./tool-silence.js";
import { setSessions, setActive, openTurn, appendEntry, applyToolProgress } from "./store.js";
import { toolCallSigs } from "./store-signals.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

const CHAT = "c-silence";
const TURN = "t-1";
const CALL = "tc-1";

/** A `ts` no reader may touch. The store's own fold is the subject: an entry's
 *  timestamp is METADATA, so a marker derived from one would be ordering state
 *  wearing a display value's name. */
function poison(e: Entry): void {
  Object.defineProperty(e, "ts", {
    configurable: true,
    get(): never {
      throw new Error("entry ts was read");
    },
  });
}

function turnOpen(): Entry {
  return {
    id: `${TURN}-open`,
    turn: TURN,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: "m-1", text: "hi" } },
  };
}

function call(over: Partial<EntryToolCall> = {}): EntryToolCall {
  return Object.assign(
    { id: CALL, title: "Run Command", status: "in_progress", kind: "execute", ts: 1 },
    over,
  ) as EntryToolCall;
}

function callEntry(tc: EntryToolCall = call()): Entry {
  return { id: CALL, turn: TURN, kind: "tool_call", seq: 1, ts: 2, payload: tc };
}

/** A chat holding one open turn with one in-flight tool call, returning the two
 *  entries so a case can poison their `ts` after the store has them. */
function seedCall(tc: EntryToolCall = call()): { open: Entry; entry: Entry } {
  setSessions([makeSession({ id: CHAT })]);
  setActive(CHAT);
  const open = turnOpen();
  openTurn(CHAT, open);
  const entry = callEntry(tc);
  appendEntry(CHAT, entry);
  return { open, entry };
}

beforeEach(() => {
  clearToolSilence();
  toolCallSigs.clearAll();
});

afterEach(() => {
  clearToolSilence();
});

describe("what the marker says", () => {
  it("states the fact in whole minutes and never a verdict", () => {
    expect(silenceLabel(SILENCE_THRESHOLD_MS)).toBe("no output for 2 minutes");
    expect(silenceLabel(65_000)).toBe("no output for 1 minute");
    expect(silenceLabel(11 * 60_000 + 59_000)).toBe("no output for 11 minutes");
    for (const verdict of [/stuck/i, /hung/i, /stall/i, /dead/i, /frozen/i, /fail/i, /never/i]) {
      expect(silenceLabel(9 * 60_000)).not.toMatch(verdict);
    }
  });
});

describe("what the marker is derived from", () => {
  it("answers undefined for a call this client has applied no frame for", () => {
    expect(silenceMsFor(CHAT, CALL, 10_000)).toBeUndefined();
  });

  it("keys on (chat, call), so one call id in two chats does not report for the other", () => {
    noteToolActivity(CHAT, CALL, 1_000);
    expect(silenceMsFor(CHAT, CALL, 5_000)).toBe(4_000);
    expect(silenceMsFor("c-other", CALL, 5_000)).toBeUndefined();
  });

  it("takes its stamp from the APPLIED tool_progress, Date.now()-derived", () => {
    seedCall();
    expect(silenceMsFor(CHAT, CALL)).toBeUndefined();

    const fold = applyToolProgress(CHAT, TURN, {
      turn: TURN,
      tool_call_id: CALL,
      output_delta: "one line",
    });
    expect(fold?.output).toBe("one line");

    // A stamp read off the entry (`ts: 2`) would put the silence at the whole of
    // the epoch; a `Date.now()`-derived one puts it at the test's own elapsed time.
    const ms = silenceMsFor(CHAT, CALL);
    expect(ms).toBeDefined();
    expect(ms).toBeLessThan(5_000);
    expect(silenceMsFor(CHAT, CALL, Date.now() + 5 * 60_000)).toBeGreaterThan(SILENCE_THRESHOLD_MS);
  });

  it("reads no entry ts: a turn whose entries throw on ts is folded and stamped", () => {
    const { open, entry } = seedCall();
    poison(open);
    poison(entry);

    expect(() =>
      applyToolProgress(CHAT, TURN, { turn: TURN, tool_call_id: CALL, output_delta: "x" }),
    ).not.toThrow();
    expect(silenceMsFor(CHAT, CALL)).toBeLessThan(5_000);
  });
});
