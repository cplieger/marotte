// The delegate card's last lines, from its LANE: which lines show, that the OPEN entry is the real
// tail, and that ONE delta repaints ONE card. The binding half uses the real store.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { subagentTail, bindSubagentTail, TAIL_LINES } from "./subagent-tail.js";
import { makeToolCall } from "./__test-helpers__/model.js";
import {
  appendEntry,
  applyDelta,
  defaultUsage,
  openEntry,
  openTurn,
  sealEntry,
  setActive,
  setSessions,
} from "./store.js";
import { clearAllEntrySigs } from "./store-signals.js";
import type { Session } from "./types.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

const CHAT = "c-tail";
const SUB = "u-1";
const OTHER = "u-2";
const TURN = "t1";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "tail",
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

/** The `turn_open` of `turnID`, which is always `seq` 0. */
function turnOpen(turnID: string): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: `m-${turnID}`, text: "delegate this" } },
  };
}

/** A sealed entry at `seq`. `lane` absent is the chat's own lane. */
function sealed(seq: number, kind: Entry["kind"], payload: unknown, lane?: string): Entry {
  const base: Entry = {
    id: `${TURN}-e${String(seq)}`,
    turn: TURN,
    kind,
    seq,
    ts: seq + 1,
    payload,
  };
  return lane === undefined ? base : Object.assign(base, { lane });
}

function text(seq: number, body: string, lane?: string): Entry {
  return sealed(seq, "text", { text: body }, lane);
}

function thinking(seq: number, body: string, lane?: string): Entry {
  return sealed(seq, "thinking", { text: body }, lane);
}

function call(title: string, opts: { readonly subtask?: string } = {}): EntryToolCall {
  return makeToolCall({
    id: `tc-${title}`,
    title,
    kind: "other",
    status: "completed",
    ...(opts.subtask === undefined ? {} : { agent_subtask_id: opts.subtask }),
  });
}

function toolCall(seq: number, c: EntryToolCall, lane?: string): Entry {
  const base: Entry = { id: c.id, turn: TURN, kind: "tool_call", seq, ts: seq + 1, payload: c };
  return lane === undefined ? base : Object.assign(base, { lane });
}

/** Seed ONE turn holding `entries` after its `turn_open`, through the store's own
 *  operations: a fixture assembled by hand could hold a `seq` the store refuses. */
function seed(entries: readonly Entry[]): void {
  setSessions([makeSession(CHAT)]);
  setActive(CHAT);
  openTurn(CHAT, turnOpen(TURN));
  for (const e of entries) {
    appendEntry(CHAT, e);
  }
}

/** The lane's open entry, seated with `n` deltas already folded in. */
function open(lane: string, body: string, id = `${TURN}-live-${lane}`): void {
  openEntry(CHAT, { turn: TURN, id, lane, kind: "text", text: body, n: 1 });
}

describe("subagentTail reads the lane", () => {
  beforeEach(() => {
    clearAllEntrySigs();
  });

  it("takes the LAST lines of an entry, not its first", () => {
    seed([text(1, "one\ntwo\nthree\nfour\nfive", SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["three", "four", "five"]);
  });

  it("caps at TAIL_LINES by default and honours an explicit want", () => {
    seed([text(1, "a\nb\nc\nd", SUB)]);
    expect(subagentTail(CHAT, SUB)).toHaveLength(TAIL_LINES);
    expect(subagentTail(CHAT, SUB, 1)).toEqual(["d"]);
    expect(subagentTail(CHAT, SUB, 0)).toEqual([]);
  });

  // A body's `textContent` carries no separator between rendered blocks, so a DOM walk
  // would glue the last three lines into one, clipped at the card's width.
  it("keeps one line per ENTRY, so two entries never glue into one", () => {
    seed([text(1, "first", SUB), text(2, "second", SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["first", "second"]);
  });

  it("reads a thinking entry's trace as well as a text entry's prose", () => {
    seed([thinking(1, "I need to check the build first.", SUB), text(2, "Checking.", SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["I need to check the build first.", "Checking."]);
  });

  it("collapses whitespace runs and drops blank lines", () => {
    seed([text(1, "  spaced   out  \n\n\n   \nlast\n", SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["spaced out", "last"]);
  });

  // Every lane predicate is relative to the lane asked about: the chat's own entries are
  // lane "" and a sibling's are the sibling's, and either leaking puts one agent's work
  // under another's name.
  it("ignores the chat's own lane and every OTHER delegate's", () => {
    seed([text(1, "parent prose"), text(2, "mine", SUB), text(3, "theirs", OTHER)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["mine"]);
    expect(subagentTail(CHAT, OTHER)).toEqual(["theirs"]);
  });

  // "" is the chat's own lane, so a tail for it would be the conversation's last lines
  // under a delegate's name.
  it("answers nothing for the empty subtask id, and for a chat it does not hold", () => {
    seed([text(1, "parent prose")]);
    expect(subagentTail(CHAT, "")).toEqual([]);
    expect(subagentTail("c-nobody", SUB)).toEqual([]);
  });

  // Tool titles keep a command-running delegate's tail moving; its own invocation is excluded (the
  // header names it).
  it("takes a tool call's TITLE as its line, and never an invocation's", () => {
    seed([
      text(1, "looking", SUB),
      toolCall(2, call("Grep Search"), SUB),
      toolCall(3, call("Sub-agent: introspect", { subtask: "u-3" }), SUB),
    ]);
    expect(subagentTail(CHAT, SUB)).toEqual(["looking", "Grep Search"]);
  });

  // Engine bookkeeping the transcript renders nowhere may not surface here either, which
  // is the one exclusion a card cannot make for itself.
  it("contributes no line for an internal title", () => {
    seed([text(1, "mine", SUB), toolCall(2, call("Fetching your cloud config"), SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["mine"]);
  });

  // KAS titles a shell call with no description "Run Command", so a delegate running
  // three commands would tail three identical lines. The command is what it ran.
  it("takes the command as a description-less shell call's line", () => {
    const shell = makeToolCall({
      id: "tc-shell",
      title: "Run Command",
      kind: "execute",
      status: "completed",
      input: { command: "make lint" },
    });
    seed([text(1, "checking", SUB), toolCall(2, shell, SUB)]);
    expect(subagentTail(CHAT, SUB)).toEqual(["checking", "make lint"]);
  });

  // The open entry IS the lane's tail: it carries no `seq` and no position, so it is read
  // separately and last. A tail that ignored it would freeze at the last sealed line for
  // as long as the delegate keeps writing, which is every live card.
  it("reads the open entry as the lane's tail, after the sealed ones", () => {
    seed([text(1, "sealed one", SUB), text(2, "sealed two", SUB)]);
    open(SUB, "arriving now");
    expect(subagentTail(CHAT, SUB)).toEqual(["sealed one", "sealed two", "arriving now"]);
  });

  // The open entry is read with the whole budget, so its own lines are what `want` has to
  // bound: a lane whose live tail is longer than the card must still show its last lines.
  it("bounds the open entry's own lines by `want`", () => {
    seed([text(1, "sealed", SUB)]);
    open(SUB, "p\nq\nr\ns");
    expect(subagentTail(CHAT, SUB)).toEqual(["q", "r", "s"]);
  });

  it("reads an open entry that is the lane's only content", () => {
    seed([text(1, "parent prose")]);
    open(SUB, "first words");
    expect(subagentTail(CHAT, SUB)).toEqual(["first words"]);
  });

  it("reads the open entry of the asked-about lane alone", () => {
    seed([text(1, "parent prose")]);
    open(SUB, "mine");
    open(OTHER, "theirs");
    expect(subagentTail(CHAT, SUB)).toEqual(["mine"]);
  });
});

describe("bindSubagentTail repaints one card", () => {
  beforeEach(() => {
    clearAllEntrySigs();
  });

  it("paints the delegate's current tail on install", () => {
    seed([text(1, "first line", SUB)]);
    const paint = vi.fn();
    const stop = bindSubagentTail(CHAT, SUB, paint);
    expect(paint).toHaveBeenCalledTimes(1);
    expect(paint.mock.calls[0]?.[0]).toEqual(["first line"]);
    stop();
  });

  // The narrow channel is `laneSig(turn, lane)`, so a sibling's delta must not reach this
  // card. The sibling's own walk re-runs, and its lines are unchanged.
  it("repaints for its own delegate's delta and not for a sibling's", () => {
    seed([text(1, "parent prose")]);
    open(SUB, "mine");
    open(OTHER, "theirs");
    const mine = vi.fn();
    const theirs = vi.fn();
    const stopMine = bindSubagentTail(CHAT, SUB, mine);
    const stopTheirs = bindSubagentTail(CHAT, OTHER, theirs);
    mine.mockClear();
    theirs.mockClear();

    applyDelta(CHAT, TURN, `${TURN}-live-${SUB}`, SUB, 2, " and more");

    expect(mine).toHaveBeenCalledTimes(1);
    expect(mine.mock.calls[0]?.[0]).toEqual(["mine and more"]);
    expect(theirs).not.toHaveBeenCalled();
    stopMine();
    stopTheirs();
  });

  // At creation the lane does not exist yet, so the chat's transcript version (coalesced per
  // microtask) carries the first entry.
  it("picks up the lane's FIRST entry, which no lane signal can announce", async () => {
    seed([text(1, "parent prose")]);
    const paint = vi.fn();
    const stop = bindSubagentTail(CHAT, SUB, paint);
    expect(paint.mock.calls[0]?.[0]).toEqual([]);
    paint.mockClear();

    appendEntry(CHAT, text(2, "first words", SUB));
    await Promise.resolve();

    expect(paint).toHaveBeenCalledTimes(1);
    expect(paint.mock.calls[0]?.[0]).toEqual(["first words"]);
    stop();
  });

  // A seal moves the same words from the open entry into a sealed one, so the tail is
  // unchanged and the card must not be repainted for it.
  it("does not repaint when the lines are unchanged", async () => {
    seed([text(1, "parent prose")]);
    open(SUB, "settled words");
    const paint = vi.fn();
    const stop = bindSubagentTail(CHAT, SUB, paint);
    paint.mockClear();

    sealEntry(CHAT, TURN, `${TURN}-live-${SUB}`, SUB, 2, 9, 1);
    await Promise.resolve();

    expect(subagentTail(CHAT, SUB)).toEqual(["settled words"]);
    expect(paint).not.toHaveBeenCalled();
    stop();
  });

  it("stops painting once disposed", async () => {
    seed([text(1, "parent prose")]);
    open(SUB, "mine");
    const paint = vi.fn();
    bindSubagentTail(CHAT, SUB, paint)();
    paint.mockClear();

    applyDelta(CHAT, TURN, `${TURN}-live-${SUB}`, SUB, 2, " more");
    appendEntry(CHAT, text(2, "later", SUB));
    await Promise.resolve();

    expect(paint).not.toHaveBeenCalled();
  });
});
