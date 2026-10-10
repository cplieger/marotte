// The residency planner and its `seq` space: which ENTRIES a paint may mount around the
// reader's position, under an entry and tool-card budget. In the NODE project: a DOM
// import in this graph throws at load. Pure, so every case states the whole input.

import { describe, it, expect } from "vitest";
import { toolResultID } from "./entry-ids.js";
import {
  OVERSCAN_ENTRIES,
  RESIDENT_ENTRIES,
  RESIDENT_TOOL_CALLS,
  effectiveRunID,
  entryAt,
  entryRenders,
  firstPlanSeq,
  planResidency,
  proseRunAt,
  runCardOwners,
  runResults,
  sliceTurn,
  turnOrdinalOf,
  turnSpan,
  type EntryRange,
  type ResidencyAnchor,
  type ResidencyPlan,
} from "./block-window.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

// A `Turn` is built directly (this module reads `id` and `body` alone); `body` is the
// entries past the `turn_open`, with `body[i].seq === i + 1`.

function entry(
  seq: number,
  kind: Entry["kind"],
  payload: unknown = {},
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  const base: Entry = {
    id: opts.id ?? `e${String(seq)}`,
    turn: "t1",
    kind,
    seq,
    ts: seq,
    payload,
  };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

function text(seq: number, lane?: string): Entry {
  return entry(seq, "text", { text: `p${String(seq)}` }, lane === undefined ? {} : { lane });
}

function call(
  seq: number,
  opts: {
    readonly id?: string;
    readonly title?: string;
    readonly run?: string;
    readonly lane?: string;
  } = {},
): Entry {
  const id = opts.id ?? `c${String(seq)}`;
  const payload = {
    id,
    title: opts.title ?? "Run Command",
    kind: "execute",
    status: "completed",
    ts: 1,
    ...(opts.run !== undefined && { workflow_id: opts.run }),
  };
  return entry(seq, "tool_call", payload, {
    id,
    ...(opts.lane !== undefined && { lane: opts.lane }),
  });
}

/** The settled value of `callID`, keyed `<call>:result` — which is the join
 *  `effectiveRunID` makes with no table behind it. */
function result(
  seq: number,
  callID: string,
  opts: { readonly run?: string; readonly lane?: string } = {},
): Entry {
  return entry(
    seq,
    "tool_result",
    { status: "completed", ...(opts.run !== undefined && { workflow_id: opts.run }) },
    { id: toolResultID(callID), ...(opts.lane !== undefined && { lane: opts.lane }) },
  );
}

/** A turn whose body is `body`, with the seqs re-stamped so the caller may list kinds
 *  without counting. Ids and lanes are preserved. */
function turn(id: string, body: readonly Entry[]): Turn {
  return {
    id,
    n: 1,
    trigger: undefined,
    body: body.map((e, i) => Object.assign({ ...e }, { seq: i + 1, turn: id })),
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

function texts(n: number): Entry[] {
  return Array.from({ length: n }, (_, i) => text(i + 1));
}

function calls(n: number): Entry[] {
  return Array.from({ length: n }, (_, i) => call(i + 1, { id: `c${String(i)}` }));
}

function spanOf(plan: ResidencyPlan, turnID: string): number {
  const r = plan.get(turnID);
  return r === undefined ? 0 : r.to - r.from;
}

// --- The coordinate space ---------------------------------------------------

describe("the entry space", () => {
  it("indexes the body from seq 1, leaving 0 for the turn_open", () => {
    const t = turn("t1", [text(1), call(2)]);
    expect(entryAt(t, 1)?.kind).toBe("text");
    expect(entryAt(t, 2)?.kind).toBe("tool_call");
  });

  it("answers nothing at 0 and past the turn", () => {
    // 0 is the `turn_open`, which is not in `body`; past the turn there is no entry.
    const t = turn("t1", [text(1)]);
    expect(entryAt(t, 0)).toBeUndefined();
    expect(entryAt(t, 2)).toBeUndefined();
  });

  it("counts the turn_open's own ordinal in the span", () => {
    // `seq` is the space and the header's entry holds a place in it, so a two-entry body
    // spans three ordinals. A span short by one would let a window's edge fall off the tail.
    expect(turnSpan(turn("t1", [text(1), text(2)]))).toBe(3);
    expect(turnSpan(turn("t1", []))).toBe(1);
  });

  it("turnOrdinalOf answers the entry's OWN seq", () => {
    const t = turn("t1", [text(1), call(2, { id: "the-call" }), text(3)]);
    expect(turnOrdinalOf(t, "the-call")).toBe(2);
  });

  it("turnOrdinalOf answers the turn's first ordinal for an absent id", () => {
    const t = turn("t1", [text(1)]);
    expect(turnOrdinalOf(t)).toBe(0);
    expect(turnOrdinalOf(t, "")).toBe(0);
  });

  it("turnOrdinalOf answers undefined for an id this turn does not hold", () => {
    // A caller branches on "not in this turn" rather than guessing at an ordinal.
    expect(turnOrdinalOf(turn("t1", [text(1)]), "nope")).toBeUndefined();
  });

  it("round-trips an id through its ordinal back to the entry", () => {
    const t = turn("t1", [text(1), call(2, { id: "the-call" }), text(3)]);
    const seq = turnOrdinalOf(t, "the-call");
    expect(seq).not.toBeUndefined();
    expect(entryAt(t, seq as number)?.id).toBe("the-call");
  });
});

// --- The ONE render predicate ----------------------------------------------

describe("entryRenders", () => {
  it.each(["turn_open", "turn_close", "turn_bind", "tool_result", "reconciled"] as const)(
    "refuses a %s, which renders elsewhere",
    (kind) => {
      // Header, footer, nothing, and its call's own card: exported so budget and spacer agree.
      expect(entryRenders(entry(1, kind), "", -1)).toBe(false);
    },
  );

  it.each([
    "text",
    "thinking",
    "tool_call",
    "steer",
    "steer_ack",
    "compaction",
    "model_switched",
    "model_routed",
    "mode_switched",
    "turn_revert",
  ] as const)("draws a %s at its own position", (kind) => {
    expect(entryRenders(entry(1, kind), "", -1)).toBe(true);
  });

  it("draws only the FIRST plan of the lane, every later one folding into its card", () => {
    expect(entryRenders(entry(3, "plan"), "", 3)).toBe(true);
    expect(entryRenders(entry(7, "plan"), "", 3)).toBe(false);
  });

  it("refuses an entry of any lane but the VIEW's root", () => {
    // `lane` is the view's root: `""` for the transcript, a delegate's uuid for its own page.
    expect(entryRenders(text(1, "sub-A"), "", -1)).toBe(false);
    expect(entryRenders(text(1, "sub-A"), "sub-A", -1)).toBe(true);
    expect(entryRenders(text(1), "sub-A", -1)).toBe(false);
  });
});

describe("firstPlanSeq", () => {
  it("names the first plan of the given lane", () => {
    const t = turn("t1", [text(1), entry(2, "plan"), entry(3, "plan")]);
    expect(firstPlanSeq(t, "")).toBe(2);
  });

  it("is per LANE, so a delegate's plan is not the transcript's first", () => {
    const t = turn("t1", [entry(1, "plan", {}, { lane: "sub-A" }), entry(2, "plan")]);
    expect(firstPlanSeq(t, "")).toBe(2);
    expect(firstPlanSeq(t, "sub-A")).toBe(1);
  });

  it("answers -1 for a turn holding no plan, which no seq can equal", () => {
    expect(firstPlanSeq(turn("t1", [text(1)]), "")).toBe(-1);
  });
});

// --- A run's card owner ----------------------------------------------------

describe("effectiveRunID", () => {
  it("prefers the call's OWN workflow id", () => {
    const t = turn("t1", [call(1, { id: "c1", run: "w-own" }), result(2, "c1", { run: "w-res" })]);
    expect(effectiveRunID(t.body[0] as Entry, runResults(t))).toBe("w-own");
  });

  it("falls back to the call's own tool_result, joined on the entry id", () => {
    const t = turn("t1", [call(1, { id: "c1" }), result(2, "c1", { run: "w-1" })]);
    expect(effectiveRunID(t.body[0] as Entry, runResults(t))).toBe("w-1");
  });

  it("answers empty when neither carries one", () => {
    const t = turn("t1", [call(1, { id: "c1" }), result(2, "c1")]);
    expect(effectiveRunID(t.body[0] as Entry, runResults(t))).toBe("");
  });

  it("does not take ANOTHER call's result", () => {
    // The join is the id, so a run-bearing result in the same turn cannot lend its id to a
    // call it does not settle.
    const t = turn("t1", [
      call(1, { id: "c1" }),
      call(2, { id: "c2" }),
      result(3, "c2", { run: "w-2" }),
    ]);
    expect(effectiveRunID(t.body[0] as Entry, runResults(t))).toBe("");
    expect(effectiveRunID(t.body[1] as Entry, runResults(t))).toBe("w-2");
  });
});

describe("runCardOwners", () => {
  it("names the FIRST call of a run and no other", () => {
    // A later mention of one run is indistinguishable on the wire, so POSITION decides.
    const t = turn("t1", [
      call(1, { id: "launch", run: "w-1" }),
      call(2, { id: "inspect", run: "w-1" }),
    ]);
    expect(runCardOwners([t])).toEqual(new Map([["w-1", "launch"]]));
  });

  it("resolves both calls of one turn, in seq order", () => {
    const t = turn("t1", [call(1, { id: "a", run: "w-1" }), call(2, { id: "b", run: "w-2" })]);
    expect(runCardOwners([t])).toEqual(
      new Map([
        ["w-1", "a"],
        ["w-2", "b"],
      ]),
    );
  });

  it("names one owner per run across several turns, oldest first", () => {
    const older = turn("t1", [call(1, { id: "first", run: "w-1" })]);
    const newer = turn("t2", [call(1, { id: "second", run: "w-1" })]);
    expect(runCardOwners([older, newer])).toEqual(new Map([["w-1", "first"]]));
  });

  it("skips an entry this view does not render, so no owner names an undrawn call", () => {
    const t = turn("t1", [
      call(1, { id: "delegate", run: "w-1", lane: "sub-A" }),
      call(2, { id: "own", run: "w-1" }),
    ]);
    expect(runCardOwners([t])).toEqual(new Map([["w-1", "own"]]));
  });

  it("skips an internal-titled call, matching the dispatcher's own early return", () => {
    const t = turn("t1", [
      call(1, { id: "internal", run: "w-1", title: "Fetching your cloud config" }),
      call(2, { id: "real", run: "w-1" }),
    ]);
    expect(runCardOwners([t])).toEqual(new Map([["w-1", "real"]]));
  });

  it("names nothing for a turn whose calls carry no run", () => {
    expect(runCardOwners([turn("t1", [call(1), text(2)])])).toEqual(new Map());
  });

  it("plans a DELEGATE's page against that lane's own calls", () => {
    const t = turn("t1", [
      call(1, { id: "own", run: "w-1" }),
      call(2, { id: "delegate", run: "w-2", lane: "sub-A" }),
    ]);
    expect(runCardOwners([t], "sub-A")).toEqual(new Map([["w-2", "delegate"]]));
  });
});

// --- What a turn costs -----------------------------------------------------

// --- The prose-run snap ----------------------------------------------------

describe("sliceTurn", () => {
  it("clamps a range into the turn's span", () => {
    const t = turn("t1", [text(1), text(2)]);
    expect(sliceTurn(t, { from: -5, to: 99 })).toEqual({ from: 0, to: 3 });
  });

  it("keeps an empty range empty rather than snapping it open", () => {
    const t = turn("t1", texts(4));
    expect(sliceTurn(t, { from: 2, to: 2 })).toEqual({ from: 2, to: 2 });
  });

  it("snaps an edge INSIDE a prose run out to the whole run", () => {
    // A run is one `.msg-row` holding one markdown stream, so an edge inside one would mount
    // it as two rows with two parsers and a visible seam.
    const t = turn("t1", texts(6));
    expect(sliceTurn(t, { from: 3, to: 5 })).toEqual({ from: 1, to: 7 });
  });

  it("stops the snap at an entry that RENDERS, which is where the run ends", () => {
    // 8.5's run: consecutive `text` entries of one lane with nothing that renders between
    // them. The tool call at seq 3 is a card between two paragraphs, so it bounds both runs.
    const t = turn("t1", [text(1), text(2), call(3), text(4), text(5)]);
    expect(sliceTurn(t, { from: 4, to: 5 })).toEqual({ from: 4, to: 6 });
  });

  it("steps OVER an entry that renders nothing, which does not break a run", () => {
    // A `tool_result` renders on its call's card, so it is not between two paragraphs in any
    // sense the reader can see and the run continues across it.
    const t = turn("t1", [text(1), entry(2, "tool_result"), text(3)]);
    expect(sliceTurn(t, { from: 3, to: 4 })).toEqual({ from: 1, to: 4 });
  });

  it("does not join two runs across a DELEGATE's entry for the delegate's own view", () => {
    // Symmetry with the transcript: whichever lane is the view's root, an entry of that lane
    // that renders is a run boundary and an entry of any other lane is not.
    const t = turn("t1", [text(1, "sub-A"), text(2), text(3, "sub-A")]);
    expect(sliceTurn(t, { from: 3, to: 4 }, "sub-A")).toEqual({ from: 1, to: 4 });
    expect(sliceTurn(t, { from: 2, to: 3 }, "sub-A")).toEqual({ from: 2, to: 3 });
  });

  it("leaves a range whose edges are already run boundaries alone", () => {
    const t = turn("t1", [text(1), text(2), call(3)]);
    expect(sliceTurn(t, { from: 1, to: 3 })).toEqual({ from: 1, to: 3 });
  });

  it("takes a hoisted firstPlan, so a caller in a loop needs no re-scan", () => {
    // The plan at seq 2 renders, so it bounds the run either way; what the parameter proves
    // is that the caller's own answer is the one used.
    const t = turn("t1", [text(1), entry(2, "plan"), text(3)]);
    expect(sliceTurn(t, { from: 3, to: 4 }, "", 2)).toEqual({ from: 3, to: 4 });
  });
});

describe("proseRunAt", () => {
  it("answers the run's whole range for one of its members", () => {
    const t = turn("t1", [text(1), text(2), call(3)]);
    expect(proseRunAt(t, 2, "", -1)).toEqual({ from: 1, to: 3 });
  });

  it("answers null for a seq that is not prose this view draws", () => {
    // The MOUNT's door onto the same rule the window snap applies, so an edge and a bubble
    // cannot disagree about where a run begins.
    const t = turn("t1", [text(1), call(2), text(3, "sub-A")]);
    expect(proseRunAt(t, 2, "", -1)).toBeNull();
    expect(proseRunAt(t, 3, "", -1)).toBeNull();
    expect(proseRunAt(t, 0, "", -1)).toBeNull();
  });

  it("agrees with sliceTurn on the run it names", () => {
    const t = turn("t1", texts(5));
    const run = proseRunAt(t, 3, "", -1) as EntryRange;
    expect(sliceTurn(t, { from: 3, to: 4 })).toEqual(run);
  });

  it("breaks a run at a turn_revert and steps over an entry that renders nothing", () => {
    // A turn_revert RENDERS (`entry_fold.json`), so it ends a prose run: text on either side of
    // the cut is two paragraphs. The log cannot produce this shape, so a synthetic turn pins it.
    const cut = turn("t1", [
      text(1),
      entry(2, "turn_revert", { from: "t-9", from_n: 9, through: "t-9", cause: "rewind" }),
      text(3),
    ]);
    expect(proseRunAt(cut, 1, "", -1)).toEqual({ from: 1, to: 2 });
    expect(proseRunAt(cut, 3, "", -1)).toEqual({ from: 3, to: 4 });

    // The control: a turn_bind renders nothing, so the walk steps over it and keeps ONE run.
    const bound = turn("t1", [
      text(1),
      entry(2, "turn_bind", { kas_message_id: "kas-1", session_id: "sess-1" }),
      text(3),
    ]);
    expect(proseRunAt(bound, 1, "", -1)).toEqual({ from: 1, to: 4 });
  });
});

// --- The plan --------------------------------------------------------------

describe("planResidency", () => {
  it("keeps every turn at its full span when the whole window fits", () => {
    const turns = [turn("t1", texts(3)), turn("t2", texts(3))];
    const plan = planResidency(turns, undefined, "");
    expect(plan.get("t1")).toEqual({ from: 0, to: 4 });
    expect(plan.get("t2")).toEqual({ from: 0, to: 4 });
  });

  it("windows a single over-budget turn to its TAIL at the live edge", () => {
    // One turn that must be cut; prose so the snap is exercised (the head is not exactly the
    // budget).
    const t = turn("t1", texts(RESIDENT_ENTRIES + 40));
    const plan = planResidency([t], undefined, "");
    const r = plan.get("t1") as EntryRange;
    expect(r.to).toBe(turnSpan(t));
    expect(r.from).toBeGreaterThan(0);
    expect(spanOf(plan, "t1")).toBeLessThanOrEqual(turnSpan(t));
  });

  it("names an INTERIOR range for a turn the reader is parked inside", () => {
    const t = turn("t1", calls(RESIDENT_TOOL_CALLS + 60));
    const anchor: ResidencyAnchor = { turnID: "t1", at: 30 };
    const r = planResidency([t], anchor, "").get("t1") as EntryRange;
    expect(r.from).toBeLessThanOrEqual(30);
    expect(r.to).toBeGreaterThan(30);
    expect(r.to).toBeLessThan(turnSpan(t));
  });

  it("grows both sides of an interior anchor by at least one overscan", () => {
    // The floor is asserted ON the window rather than fed in as an input, which is what makes
    // `OVERSCAN_ENTRIES` one name for one distance.
    const t = turn("t1", calls(RESIDENT_TOOL_CALLS + 200));
    const at = 150;
    const r = planResidency([t], { turnID: "t1", at }, "").get("t1") as EntryRange;
    expect(at - r.from).toBeGreaterThanOrEqual(OVERSCAN_ENTRIES);
    expect(r.to - at).toBeGreaterThanOrEqual(OVERSCAN_ENTRIES);
  });

  it("spends the budget from the live edge back", () => {
    // Oldest turn first: the newest is whole, the middle partial, the oldest absent.
    const turns = [
      turn("t1", texts(200)),
      turn("t2", texts(200)),
      turn("t3", texts(RESIDENT_ENTRIES - 20)),
    ];
    const plan = planResidency(turns, undefined, "");
    expect(plan.get("t3")).toEqual({ from: 0, to: turnSpan(turns[2] as Turn) });
    expect(spanOf(plan, "t2")).toBeGreaterThan(0);
    expect(plan.has("t1")).toBe(false);
  });

  it("cuts at the TOOL budget, short of what the entry budget would allow", () => {
    // Two budgets, because a tool card is a whole disclosure where a text entry is part of a
    // row: whichever runs out first ends the side that asked.
    const t = turn("t1", calls(RESIDENT_TOOL_CALLS + 50));
    const plan = planResidency([t], undefined, "");
    expect(spanOf(plan, "t1")).toBeLessThanOrEqual(RESIDENT_TOOL_CALLS + 1);
    expect(spanOf(plan, "t1")).toBeLessThan(RESIDENT_ENTRIES);
  });

  it("is CONTIGUOUS: a cheap turn behind an over-budget one gets no ordinal", () => {
    // One seed, one ordinal per side per step, which is what makes an island unrepresentable
    // — the alternative would mount a turn with a gap of unrendered history above it.
    const turns = [turn("t1", texts(2)), turn("t2", texts(RESIDENT_ENTRIES + 50))];
    const plan = planResidency(turns, undefined, "");
    expect(plan.has("t1")).toBe(false);
    expect(spanOf(plan, "t2")).toBeGreaterThan(0);
  });

  it("holds ONE contiguous run across three turns that each exceed the budget", () => {
    const turns = [
      turn("t1", texts(RESIDENT_ENTRIES)),
      turn("t2", texts(RESIDENT_ENTRIES)),
      turn("t3", texts(RESIDENT_ENTRIES)),
    ];
    const plan = planResidency(turns, { turnID: "t2", at: 160 }, "");
    // The middle turn is reached; the two edges are either partial or absent, and no turn
    // between two reached turns may be missing.
    expect(spanOf(plan, "t2")).toBeGreaterThan(0);
    const reached = ["t1", "t2", "t3"].map((id) => plan.has(id));
    expect(reached.slice(reached.indexOf(true), reached.lastIndexOf(true) + 1)).not.toContain(
      false,
    );
  });

  it("bounds a huge turn to the order of the budget when its prose runs are short", () => {
    // Prose broken by tool cards, which is what a long working turn looks like, so the two
    // boundary runs the snap may overspend are two entries each rather than the whole turn.
    const body: Entry[] = [];
    for (let i = 0; i < 350; i++) {
      body.push(text(0), call(0, { id: `c${String(i)}` }));
    }
    const plan = planResidency([turn("t1", body)], undefined, "");
    expect(spanOf(plan, "t1")).toBeLessThanOrEqual(RESIDENT_ENTRIES + OVERSCAN_ENTRIES * 2 + 4);
  });

  it("MOUNTS A TURN THAT IS ONE PROSE RUN WHOLE, over the budget", () => {
    // The bound is the budget plus the two BOUNDARY RUNS: uninterrupted prose is one run and
    // cannot be split into two markdown streams. `from` is 1 because ordinal 0 is the
    // `turn_open`, which renders anyway and costs nothing.
    const t = turn("t1", texts(RESIDENT_ENTRIES + 200));
    const plan = planResidency([t], undefined, "");
    expect(plan.get("t1")).toEqual({ from: 1, to: turnSpan(t) });
  });

  it("takes the budget as a parameter, so a caller can state a different one", () => {
    // Tool calls rather than prose: the snap only moves an edge inside a text run, so this
    // states the budget's own effect with nothing else in the way.
    const t = turn("t1", calls(50));
    const plan = planResidency([t], undefined, "", { entries: 10, toolCalls: 10 });
    expect(spanOf(plan, "t1")).toBeLessThan(20);
  });

  it("charges a FREE ordinal nothing and takes it unconditionally", () => {
    // A non-rendering ordinal buys nothing, so the span exceeds the entry budget when padded.
    const body: Entry[] = [];
    for (let i = 0; i < 30; i++) {
      body.push(call(0, { id: `c${String(i)}` }), result(0, `c${String(i)}`));
    }
    const t = turn("t1", body);
    const plan = planResidency([t], undefined, "", { entries: 10, toolCalls: 10 });
    const r = plan.get("t1") as EntryRange;
    // Ten calls plus the ten results interleaved with them: the results are free.
    expect(r.to - r.from).toBeGreaterThan(10);
  });

  it("clamps an anchor whose ordinal is outside its own turn", () => {
    const t = turn("t1", texts(10));
    const low = planResidency([t], { turnID: "t1", at: -50 }, "").get("t1");
    const high = planResidency([t], { turnID: "t1", at: 9999 }, "").get("t1");
    expect(low).toEqual({ from: 0, to: 11 });
    expect(high).toEqual({ from: 0, to: 11 });
  });

  it("falls back to the live edge for an anchor naming a turn the sequence does not hold", () => {
    const turns = [turn("t1", texts(RESIDENT_ENTRIES)), turn("t2", texts(20))];
    const plan = planResidency(turns, { turnID: "gone", at: 5 }, "");
    expect(plan.get("t2")).toEqual({ from: 0, to: 21 });
  });

  it("seeds at the NEXT turn's first ordinal for an anchor on a turn holding no ordinal", () => {
    // A zero-span turn's own base IS the next turn's first ordinal, so the seed lands
    // forward rather than off the sequence.
    const turns = [turn("t1", texts(RESIDENT_ENTRIES + 50)), turn("t2", []), turn("t3", texts(5))];
    const plan = planResidency(turns, { turnID: "t2", at: 0 }, "");
    expect(plan.has("t3")).toBe(true);
  });

  it("keeps the seed inside the sequence for an anchor on a TRAILING empty turn", () => {
    // Its base is past the end, so only the clamp answers it.
    const turns = [turn("t1", texts(5)), turn("t2", [])];
    const plan = planResidency(turns, { turnID: "t2", at: 0 }, "");
    expect(plan.get("t1")).toEqual({ from: 0, to: 6 });
  });

  it("returns nothing for a sequence holding no ordinal at all", () => {
    expect(planResidency([], undefined, "")).toEqual(new Map());
  });

  it("answers TURN-LOCAL ranges, so the renderer subtracts no base itself", () => {
    const turns = [turn("t1", texts(4)), turn("t2", texts(4))];
    const plan = planResidency(turns, undefined, "");
    for (const id of ["t1", "t2"]) {
      const r = plan.get(id) as EntryRange;
      expect(r.from).toBe(0);
      expect(r.to).toBe(5);
    }
  });

  it("plans a DELEGATE's page over that lane, not the transcript's", () => {
    // No default for `lane`, and this is why: a silent `""` would plan the window over the
    // transcript while the spacers priced a delegate's.
    const t = turn("t1", [text(1), text(2, "sub-A"), text(3, "sub-A")]);
    const own = planResidency([t], undefined, "", { entries: 1, toolCalls: 1 });
    const delegate = planResidency([t], undefined, "sub-A", { entries: 1, toolCalls: 1 });
    // The transcript's budget of one is spent on seq 1 and the two delegate ordinals are
    // free; the delegate's is spent on seq 3 and seq 1 is free to it.
    expect(own.get("t1")?.from).toBe(0);
    expect(delegate.get("t1")?.to).toBe(4);
  });

  it("returns a range every consumer can hand to sliceTurn unchanged", () => {
    // The plan already applied the snap, so no consumer can hold an unsnapped range.
    const t = turn("t1", texts(RESIDENT_ENTRIES + 30));
    const r = planResidency([t], { turnID: "t1", at: 100 }, "").get("t1") as EntryRange;
    expect(sliceTurn(t, r)).toEqual(r);
  });
});
