// What a range of a turn's ENTRIES is worth in pixels, on its own.
//
// In the NODE project because the arithmetic is pure: the measurement lives at the drop, so
// this module answers with no DOM at all — and that placement is its own assertion, since a
// DOM import anywhere in this graph would throw at module load rather than drift. Every case
// states the whole cache and the whole turn, so no number here is inherited from a fixture.
import { describe, it, expect } from "vitest";

import {
  ENTRY_ESTIMATE_PX,
  ROW_GAP_PX,
  forgetHeights,
  recordEntryHeight,
  recordRowHeight,
  spacerHeight,
} from "./block-heights.js";
import type { EntryRange } from "./block-window.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./types.js";

/** THE PREMISE for every number below: the table is TIER-KEYED, and this project has no
 *  `document` and no `matchMedia`, so `tierNow`'s fallback arm resolves to FINE. Read off the
 *  table so a retune moves the assertions with it. */
const FINE = ENTRY_ESTIMATE_PX.fine;

/** The flex `gap` (`--sp-3`) `.turn-body` puts between the entries it holds, which no child's
 *  own height carries. A literal, because that CSS rule is what the token shadows. */
const GAP_PX = 12;

// --- Fixtures ---------------------------------------------------------------
//
// A `Turn` is built directly rather than projected: this module reads `id` and `body` alone,
// and a projection would put `turns.ts`'s rules between a case and its subject. `body` is the
// entries PAST the `turn_open`, and the space rests on `body[i].seq === i + 1`.

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

function text(seq: number, opts: { readonly body?: string; readonly lane?: string } = {}): Entry {
  return entry(
    seq,
    "text",
    { text: opts.body ?? `p${String(seq)}` },
    opts.lane === undefined ? {} : { lane: opts.lane },
  );
}

function call(
  seq: number,
  opts: {
    readonly id?: string;
    readonly title?: string;
    readonly run?: string;
    readonly subtask?: string;
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
    ...(opts.subtask !== undefined && { agent_subtask_id: opts.subtask }),
  };
  return entry(seq, "tool_call", payload, {
    id,
    ...(opts.lane !== undefined && { lane: opts.lane }),
  });
}

/** The settled value of `callID`, keyed `<call>:result` — the join `effectiveRunID` makes
 *  with no table behind it. */
function result(seq: number, callID: string, opts: { readonly run?: string } = {}): Entry {
  return entry(
    seq,
    "tool_result",
    { status: "completed", ...(opts.run !== undefined && { workflow_id: opts.run }) },
    { id: `${callID}:result` },
  );
}

/** A turn whose body is `body`, seqs re-stamped so a case may list kinds without counting. */
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

/** Everything the turn holds, priced as a TAIL spacer: nothing is mounted, so the spacer
 *  stands in for every ordinal it has. Its answer carries the boundary gap on top of the
 *  boxes' own heights whenever it prices at least one box. */
function stood(t: Turn, lane = ""): number {
  return spacerHeight(t, { from: 0, to: 0 }, "tail", lane);
}

/** A pipeline's DRIVER id, and one STAGE id per stage in the shape KAS mints —
 *  `invoke_subagent_<driverId>_stage_<name>`, which is how a stage names its parent. */
const DRIVER_ID = "orc-1";
const STAGE_NAMES = ["plan", "build", "review"] as const;

function stageID(name: string): string {
  return `invoke_subagent_${DRIVER_ID}_stage_${name}`;
}

/** The driver's entry followed by one stage INVOCATION per stage. A stage's own output
 *  entries are the delegate's lane and are priced by that view, not by this one. */
function pipeline(stages: number): Entry[] {
  const out: Entry[] = [call(1, { id: DRIVER_ID, title: "Orchestrate Sub-agent" })];
  for (const name of STAGE_NAMES.slice(0, stages)) {
    out.push(call(2, { id: stageID(name), title: `Sub-agent: ${name}`, subtask: `sub-${name}` }));
  }
  return out;
}

describe("the premise this file's numbers rest on", () => {
  it("resolves the FINE tier, with no document and no matchMedia to read", () => {
    // The ABSENCE is the condition `tierNow`'s fallback arm keys on, asserted rather than
    // assumed because a runtime that grew either global would move four of the nine prices
    // silently — Node has already grown a `navigator` this way. The PRICE then proves the arm
    // landed on fine, through `thinking`, one of the entries the two tiers disagree about.
    const g = globalThis as { readonly document?: unknown; readonly matchMedia?: unknown };
    expect({ document: g.document, matchMedia: g.matchMedia }).toEqual({
      document: undefined,
      matchMedia: undefined,
    });
    expect(FINE.thinking, "the two tiers disagree, so this price is a tier witness").not.toBe(
      ENTRY_ESTIMATE_PX.coarse.thinking,
    );
    expect(stood(turn("t", [entry(1, "thinking", { thinking: "considering" })]))).toBe(
      FINE.thinking + GAP_PX,
    );
  });

  it("shadows the gap `.turn-body` declares", () => {
    expect(ROW_GAP_PX).toBe(GAP_PX);
  });
});

describe("what one entry is worth", () => {
  it("prices an unmeasured text entry at the row height CSS declares", () => {
    expect(stood(turn("t", [text(1)]))).toBe(48 + GAP_PX);
  });

  it("prices an EMPTY text entry at nothing, its gap included", () => {
    // A released steer's carry mounts an `is-empty` row, which is `display: none`. It costs no
    // gap either: `gap` counts an ITEM rather than a height, so a spacer standing for no box
    // replaces no gap and answers zero outright.
    expect(stood(turn("t", [text(1, { body: "" })]))).toBe(0);
  });

  it("prices a sealed thinking trace at its measured collapsed height", () => {
    // NOT a reserve: a sealed trace declares no `content-visibility`, so there is nothing to
    // shadow, and 25 is the real height of its own `<summary>` row on this tier.
    expect(stood(turn("t", [entry(1, "thinking", { thinking: "considering" })]))).toBe(25 + GAP_PX);
  });

  it("prices a tool_call at the tool card's claim line PLUS its border", () => {
    // `.tool-call` reserves `auto var(--btn-h)` — 36px of CONTENT — and the box model adds the
    // card's 2px border to a skipped card and a rendered one alike.
    expect(stood(turn("t", [call(1)]))).toBe(38 + GAP_PX);
  });

  it("prices a steer at its note, and a steer_ack as agent prose", () => {
    // The two halves of one exchange, priced differently on purpose: the note is a dock-shaped
    // box (head plus one text line plus padding) while the acknowledgement is agent content at
    // its own position, so it mounts a prose row like any other.
    expect(stood(turn("t", [entry(1, "steer", { text: "wait", origin: "user" })]))).toBe(
      62 + GAP_PX,
    );
    expect(stood(turn("t", [entry(1, "steer_ack", {})]))).toBe(48 + GAP_PX);
  });

  it.each([
    "compaction",
    "compaction_failed",
    "safety_blocked",
    "model_switched",
    "mode_switched",
  ] as const)("prices the %s event at one row", (kind) => {
    expect(stood(turn("t", [entry(1, kind, {})]))).toBe(48 + GAP_PX);
  });

  it("prices the turn's FIRST plan at its card and every later one at nothing", () => {
    // Only the first `plan` entry renders; a later one replaces that card's rows, so charging
    // it again would reserve a box the body never mounts.
    expect(stood(turn("t", [entry(1, "plan", { entries: [] })]))).toBe(70 + GAP_PX);
    expect(
      stood(turn("t", [entry(1, "plan", { entries: [] }), entry(2, "plan", { entries: [] })])),
    ).toBe(70 + GAP_PX);
  });

  it("prices the kinds that render ELSEWHERE at nothing", () => {
    // A `tool_result` was charged at its call, a `turn_bind` renders nowhere and a `turn_close`
    // is the footer, a card-level sibling of the body this spacer stands inside.
    const t = turn("t", [
      call(1, { id: "c1" }),
      result(2, "c1"),
      entry(3, "turn_bind", {}),
      entry(4, "turn_close", { outcome: "completed" }),
    ]);
    expect(stood(t)).toBe(38 + GAP_PX);
  });
});

describe("what a tool call MOUNTS AS", () => {
  it("prices a WORKFLOW LAUNCH at the run card its own payload names", () => {
    expect(stood(turn("t", [call(1, { run: "wf-1" })]))).toBe(71 + GAP_PX);
  });

  it("prices a call whose RESULT carries the run id at the run card too", () => {
    // The wire carries no `workflow_id` on the call today, so the result's is the id every
    // resident run card is derived from — and the price has to follow the same join or a run
    // is reserved a tool row and mounts a card.
    const t = turn("t", [call(1, { id: "c1" }), result(2, "c1", { run: "wf-1" })]);
    expect(stood(t)).toBe(71 + GAP_PX);
  });

  it("prices a DELEGATE INVOCATION at the subagent card, not at the tool row", () => {
    // Its element is a `.subagent-block`, and the transcript draws exactly one thing per
    // delegate — this entry — so the card's height is reserved here or nowhere.
    expect(stood(turn("t", [call(1, { subtask: "sub-1" })]))).toBe(71 + GAP_PX);
  });

  it("prices a PIPELINE DRIVER at the box it mounts, not at a tool row", () => {
    // Its entry mounts a `.subagent-container`, which RESTS at the same height a card does:
    // the collapsed body holding its stages is out of layout. `Orchestrate Sub-agent` is
    // deliberately absent from the delegate predicate — one title with two owners makes a
    // classification unpredictable — so this is what stops a driver falling through to 38.
    expect(stood(turn("t", [call(1, { id: DRIVER_ID, title: "Orchestrate Sub-agent" })]))).toBe(
      71 + GAP_PX,
    );
  });

  it("prices a pipeline STAGE at nothing, its driver holding the price", () => {
    // Inside a rendered container a stage's card sits at height 0. The control that stops this
    // collapsing into "any delegate invocation is free" is the plain-delegate case above.
    const t = turn("t", [
      call(1, { id: stageID("plan"), title: "Sub-agent: plan", subtask: "sub-plan" }),
    ]);
    expect(stood(t)).toBe(0);
  });

  it("prices a `Sub-agent:`-TITLED call with no stage id at a full card", () => {
    // The control that keeps this keyed on the ID rather than the title. Measured over the
    // chat files on one live volume, 392 of 393 delegate invocations carry the `Sub-agent:`
    // prefix while only 369 carry the `_stage_` id shape; the rest look like this and are
    // seated at the TOP LEVEL, where they cost a whole card.
    const id = `invoke_subagent_${DRIVER_ID}-sub-agent-start`;
    const t = turn("t", [call(1, { id, title: "Sub-agent: wf-coder", subtask: "sub-flat" })]);
    expect(stood(t)).toBe(71 + GAP_PX);
  });

  it("prices a whole PIPELINE at ONE card, its three stages included", () => {
    // The container rests at one card's height whatever it holds, so the pipeline's price is
    // its DRIVER's alone. A stage names its driver in its own tool-call id, which is the join
    // this reads — and being worth nothing, a stage is not a box, so it adds no gap either.
    expect(stood(turn("t", pipeline(3)))).toBe(71 + GAP_PX);
  });

  it("prices a PROMOTED single-stage pipeline at one card too", () => {
    // One stage, so the renderer promotes its card to where the container would have gone and
    // the two entries' prices are swapped against that — 71 at the driver, 0 at the stage. What
    // is exact is the pipeline's TOTAL, which is one card either way.
    expect(stood(turn("t", pipeline(1)))).toBe(71 + GAP_PX);
  });

  it("charges no GAP for a pipeline's zero-priced stages", () => {
    const t = turn("t", [text(1), ...pipeline(2), text(2)]);
    expect(stood(t)).toBe(48 + GAP_PX + 71 + GAP_PX + 48 + GAP_PX);
  });
});

describe("a PROSE RUN is one row, priced once", () => {
  it("charges consecutive text entries of one lane as a single row", () => {
    // They mount ONE `.msg-row` holding one markdown stream, so charging each would reserve
    // rows the body never builds. The run's first entry carries the price.
    expect(stood(turn("t", [text(1), text(2), text(3)]))).toBe(48 + GAP_PX);
  });

  it("charges TWO rows once an entry that renders breaks the run", () => {
    const t = turn("t", [text(1), call(2), text(3)]);
    expect(stood(t)).toBe(48 + 38 + 48 + 2 * GAP_PX + GAP_PX);
  });

  it("does not let an entry that renders NOTHING break a run", () => {
    // A `tool_result` mounts nothing at its own position, so the two text entries around it are
    // still one row — the same rule the window's own snap walks over.
    const t = turn("t", [call(1, { id: "c1" }), text(2), result(3, "c1"), text(4)]);
    expect(stood(t)).toBe(38 + GAP_PX + 48 + GAP_PX);
  });
});

describe("the LANE the view is rooted in", () => {
  it("prices nothing for an entry of another lane", () => {
    // Delegate content this view does not draw. The lane parameter carries no default for
    // exactly this reason: the price and the budget have to answer `entryRenders` identically.
    expect(stood(turn("t", [text(1, { lane: "sub-1" })]))).toBe(0);
  });

  it("prices the delegate's own lane when that lane IS the view's root", () => {
    const t = turn("t", [text(1), text(2, { lane: "sub-1" })]);
    expect(stood(t, "sub-1")).toBe(48 + GAP_PX);
  });
});

describe("which ordinals a spacer stands in for", () => {
  const four = (): Turn => turn("t", [call(1), call(2), call(3), call(4)]);

  it("prices the ordinals BEFORE the mounted range for the head spacer", () => {
    expect(spacerHeight(four(), { from: 3, to: 5 }, "head", "")).toBe(2 * 38 + GAP_PX + GAP_PX);
  });

  it("prices the ordinals AFTER the mounted range for the tail spacer", () => {
    expect(spacerHeight(four(), { from: 0, to: 3 }, "tail", "")).toBe(2 * 38 + GAP_PX + GAP_PX);
  });

  it("stands in for nothing once the mounted range reaches the turn's own edge", () => {
    expect(spacerHeight(four(), { from: 0, to: 5 }, "head", "")).toBe(0);
    expect(spacerHeight(four(), { from: 0, to: 5 }, "tail", "")).toBe(0);
  });

  it("clamps a range that overruns the turn instead of walking past it", () => {
    // The head spacer of a range starting past the span stands for the whole turn; the tail
    // spacer of the same range stands for nothing, because there is nothing above the span.
    expect(spacerHeight(four(), { from: 99, to: 99 }, "head", "")).toBe(
      4 * 38 + 3 * GAP_PX + GAP_PX,
    );
    expect(spacerHeight(four(), { from: 99, to: 99 }, "tail", "")).toBe(0);
  });

  it("charges K−1 gaps between the boxes it replaces PLUS the boundary one", () => {
    // Two gap terms, and the boundary one is what the parent no longer supplies: the spacer
    // sits under `.turn`, which declares no `gap`, where the boxes it replaces sat inside
    // `.turn-body`'s gapped column.
    expect(spacerHeight(four(), { from: 0, to: 1 }, "tail", "")).toBe(4 * 38 + 3 * GAP_PX + GAP_PX);
  });
});

describe("what a drop measured", () => {
  it("prefers a measured entry height over that entry's estimate", () => {
    recordEntryHeight("m-entry", 1, 300);
    expect(stood(turn("m-entry", [call(1), call(2)]))).toBe(300 + GAP_PX + 38 + GAP_PX);
  });

  it("prefers the ROW measurement for a prose run the window held whole", () => {
    // A row's own height already carries whatever sat between its entries, so nothing is added
    // on top of a measurement covering the range being priced. The key is the run's FIRST seq;
    // ordinal 0 is the `turn_open`, so a body-leading run starts at 1.
    const run: EntryRange = { from: 1, to: 3 };
    recordRowHeight("m-row", run, 500);
    expect(stood(turn("m-row", [text(1), text(2)]))).toBe(500 + GAP_PX);
  });

  it("prices prose from its ROW rather than from an entry measurement", () => {
    // A run is one box, so its height is the row's; an entry height recorded against a member
    // answers for an element the body does not mount on its own.
    recordEntryHeight("m-prose", 1, 300);
    expect(stood(turn("m-prose", [text(1), text(2)]))).toBe(48 + GAP_PX);
  });

  it("refuses a row measurement taken over a DIFFERENT range than the run", () => {
    // The row was dropped under a PARTIAL window, so it measured that slice alone — one entry of
    // a run that now spans two — and answering the whole run with it would price the rest at
    // zero. Its `from` matches, so the RANGE is what refuses it rather than the key.
    recordRowHeight("m-part", { from: 1, to: 2 }, 500);
    expect(stood(turn("m-part", [text(1), text(2)]))).toBe(48 + GAP_PX);
  });

  it("consults no row measurement recorded against another ordinal", () => {
    // Keyed by the run's own first `seq`, so a measurement filed elsewhere in the turn cannot
    // answer for this run at all.
    recordRowHeight("m-elsewhere", { from: 2, to: 3 }, 500);
    expect(stood(turn("m-elsewhere", [text(1), text(2)]))).toBe(48 + GAP_PX);
  });

  it("forgets a turn's measurements, so the estimates answer again", () => {
    recordRowHeight("m-forget", { from: 1, to: 2 }, 500);
    recordEntryHeight("m-forget", 2, 300);
    const t = turn("m-forget", [text(1), call(2)]);
    expect(stood(t)).toBe(500 + GAP_PX + 300 + GAP_PX);
    forgetHeights(["m-forget"]);
    expect(stood(t)).toBe(48 + GAP_PX + 38 + GAP_PX);
  });
});
