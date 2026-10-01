// The invocation is the ISSUER's, client side: design 13's `node` half.
//
// A delegate has no record of its own: what identifies one is its LANE, and the `tool_call`
// that dispatched it sits in the ISSUER's lane carrying that uuid in `agent_subtask_id`. Two
// readers turn on it — the predicate deciding whether an agent-initiated turn renders, and
// the projection the delegate's page is built from.
//
// Folded through the store operations: the bus is one call into one of these per event.
import { describe, it, expect, beforeEach } from "vitest";

import {
  appendEntry,
  applyDelta,
  defaultUsage,
  get,
  openEntry,
  openTurn,
  registerTurnRepair,
  sealEntry,
  setSessions,
} from "./store.js";
import { findSubagentInvocation, sliceSubagentGroup } from "./subagent-slice.js";
import { payloadOf, turnIsDrawn, type TurnSource } from "./turns.js";
import type { Session, TurnState } from "./types.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

const CHAT = "c";
/** The turn the delegate was dispatched in: reader-opened, so its header renders whatever
 *  its body holds, which is what leaves the drawn predicate to the two turns below. */
const TURN = "t1";
/** Agent-initiated and holding the invocation alone — the drawn case. */
const SOLO = "t2";
/** Agent-initiated and holding a delegate's OUTPUT alone, its invocation paged out. */
const ORPHAN = "t3";

const LANE = "8f2a1c9e-4b77-4d1a-9c30-6e5b0d2f7a11";
const SOLO_LANE = "1c4d5e6f-2a3b-4c5d-8e9f-0a1b2c3d4e5f";
const ORPHAN_LANE = "9b8a7c6d-5e4f-4a3b-2c1d-0f9e8d7c6b5a";
const CALL = "toolu_bdrk_parent_invoke";
const DELEGATE_CALL = "toolu_bdrk_delegate_read";
const SOLO_CALL = "toolu_bdrk_solo_invoke";

/** One SSE frame as the operation the entry handler folds it into. Total over the six entry
 *  events, so a seventh fails the type check rather than being dropped. */
type Frame =
  | { readonly kind: "turn_opened"; readonly entry: Entry }
  | { readonly kind: "entry_appended"; readonly entry: Entry }
  | { readonly kind: "entry_opened"; readonly open: OpenEntry }
  | {
      readonly kind: "entry_delta";
      readonly turn: string;
      readonly id: string;
      readonly lane: string;
      readonly n: number;
      readonly delta: string;
    }
  | {
      readonly kind: "entry_sealed";
      readonly turn: string;
      readonly id: string;
      readonly lane: string;
      readonly seq: number;
      readonly n: number;
    };

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  lane = "",
): Entry {
  const e: Entry = { id: `${turn}-e${String(seq)}`, turn, kind, seq, ts: seq + 1, payload };
  return lane === "" ? e : { ...e, lane };
}

function invocation(id: string, subtask: string): unknown {
  return {
    id,
    title: "invoke_sub_agent",
    kind: "other",
    status: "in_progress",
    agent_subtask_id: subtask,
    ts: 1,
  };
}

/** The stream design 13 drives: a parent text delta, the invocation, deltas in the
 *  delegate's lane, then a parent delta. The seals bracket the invocation because the
 *  appender seals the lane it is interrupting before it writes the call. */
const STREAM: readonly Frame[] = [
  { kind: "turn_opened", entry: sealed(TURN, 0, "turn_open", { source: "prompt", n: 1 }) },
  { kind: "entry_opened", open: { turn: TURN, id: "p1", kind: "text", text: "on", n: 1 } },
  { kind: "entry_delta", turn: TURN, id: "p1", lane: "", n: 2, delta: "e" },
  { kind: "entry_sealed", turn: TURN, id: "p1", lane: "", seq: 1, n: 2 },
  { kind: "entry_appended", entry: sealed(TURN, 2, "tool_call", invocation(CALL, LANE)) },
  {
    kind: "entry_opened",
    open: { turn: TURN, id: "d1", lane: LANE, kind: "text", text: "sub", n: 1 },
  },
  { kind: "entry_delta", turn: TURN, id: "d1", lane: LANE, n: 2, delta: " work" },
  { kind: "entry_sealed", turn: TURN, id: "d1", lane: LANE, seq: 3, n: 2 },
  {
    kind: "entry_appended",
    entry: sealed(
      TURN,
      4,
      "tool_call",
      { id: DELEGATE_CALL, title: "Read File", kind: "read", status: "completed", ts: 5 },
      LANE,
    ),
  },
  { kind: "entry_opened", open: { turn: TURN, id: "p2", kind: "text", text: "tw", n: 1 } },
  { kind: "entry_delta", turn: TURN, id: "p2", lane: "", n: 2, delta: "o" },
  { kind: "entry_sealed", turn: TURN, id: "p2", lane: "", seq: 5, n: 2 },
  {
    kind: "turn_opened",
    entry: sealed(SOLO, 0, "turn_open", { source: "wire_turn_start", n: 2 }),
  },
  {
    kind: "entry_appended",
    entry: sealed(SOLO, 1, "tool_call", invocation(SOLO_CALL, SOLO_LANE)),
  },
  {
    kind: "turn_opened",
    entry: sealed(ORPHAN, 0, "turn_open", { source: "wire_turn_start", n: 3 }),
  },
  {
    kind: "entry_appended",
    entry: sealed(ORPHAN, 1, "text", { text: "orphaned output" }, ORPHAN_LANE),
  },
];

function apply(f: Frame): void {
  switch (f.kind) {
    case "turn_opened":
      openTurn(CHAT, f.entry);
      return;
    case "entry_appended":
      appendEntry(CHAT, f.entry);
      return;
    case "entry_opened":
      openEntry(CHAT, f.open);
      return;
    case "entry_delta":
      applyDelta(CHAT, f.turn, f.id, f.lane, f.n, f.delta);
      return;
    case "entry_sealed":
      sealEntry(CHAT, f.turn, f.id, f.lane, f.seq, f.seq + 1, f.n);
      return;
  }
}

function session(): Session {
  return {
    id: CHAT,
    name: CHAT,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 3,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    residency: "loaded",
  };
}

function source(): TurnSource {
  const s = get(CHAT);
  if (s === undefined) {
    throw new Error("the chat is not in the store");
  }
  return s;
}

function entriesOf(turn: string): readonly Entry[] {
  return source().turns.get(turn)?.entries ?? [];
}

function turnState(turn: string): TurnState {
  const state = source().turns.get(turn);
  if (state === undefined) {
    throw new Error(`the store holds no turn ${turn}`);
  }
  return state;
}

/** The `tool_call` entry carrying `id`, so a case reads the store rather than asserting
 *  through a cast the fixture would satisfy either way. */
function callEntry(turn: string, id: string): Entry {
  const e = entriesOf(turn).find((x) => payloadOf(x, "tool_call")?.id === id);
  if (e === undefined) {
    throw new Error(`turn ${turn} holds no call ${id}`);
  }
  return e;
}

function laneOf(e: Entry): string {
  return e.lane ?? "";
}

describe("the invocation is the issuer's", () => {
  let repairs: string[] = [];

  beforeEach(() => {
    repairs = [];
    registerTurnRepair((chatID, turnID, afterSeq) => {
      repairs.push(`${chatID}/${turnID}/${String(afterSeq)}`);
    });
    setSessions([session()]);
    for (const f of STREAM) {
      apply(f);
    }
  });

  it('seats the invocation in lane "" naming the delegate\'s uuid', () => {
    const inv = callEntry(TURN, CALL);
    expect(laneOf(inv)).toBe("");
    expect(payloadOf(inv, "tool_call")?.agent_subtask_id).toBe(LANE);
  });

  it("lands it between the parent's two text entries, with no hole", () => {
    const held = entriesOf(TURN);
    const prose = held.filter((e) => e.kind === "text" && laneOf(e) === "");
    const inv = callEntry(TURN, CALL);
    expect(prose).toHaveLength(2);
    expect(prose.map((e) => e.seq < inv.seq)).toEqual([true, false]);
    // The store's own invariant: the fold preserved that order, not the fixture's numbering.
    expect(held.map((e) => e.seq)).toEqual(held.map((_e, i) => i));
    expect(repairs).toEqual([]);
    expect(get(CHAT)?.residency).toBe("loaded");
  });

  it("gives the delegate's own entries the uuid as their lane", () => {
    const held = entriesOf(TURN);
    const laned = held.filter((e) => laneOf(e) === LANE);
    expect(laned.map((e) => e.kind)).toEqual(["text", "tool_call"]);
    expect(
      payloadOf(callEntry(TURN, DELEGATE_CALL), "tool_call")?.agent_subtask_id,
    ).toBeUndefined();
    expect(laneOf(callEntry(TURN, DELEGATE_CALL))).toBe(LANE);
    expect(held.filter((e) => laneOf(e) !== LANE).map((e) => laneOf(e))).toEqual(["", "", "", ""]);
  });

  it("draws a turn whose only body entry is the invocation, and not one holding a lane alone", () => {
    expect(turnIsDrawn(turnState(SOLO))).toBe(true);
    expect(turnIsDrawn(turnState(ORPHAN))).toBe(false);
  });

  it("yields the invocation exactly once, and never inside the lane it dispatched", () => {
    const p = sliceSubagentGroup(source(), LANE, false);
    expect([...p.slices.keys()]).toEqual([LANE]);
    const slice = p.slices.get(LANE);
    expect(slice?.invocation?.id).toBe(CALL);
    expect(slice?.entries.map((e) => e.id)).toEqual(
      entriesOf(TURN)
        .filter((e) => laneOf(e) === LANE)
        .map((e) => e.id),
    );
    const yielded = [...p.slices.values()].reduce(
      (n, s) =>
        n +
        (s.invocation?.id === CALL ? 1 : 0) +
        s.entries.filter((e) => payloadOf(e, "tool_call")?.id === CALL).length,
      0,
    );
    expect(yielded).toBe(1);
    expect(findSubagentInvocation(source(), LANE)?.id).toBe(CALL);
  });
});
