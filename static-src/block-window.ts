// ---------------------------------------------------------------------------
// Residency: which ENTRIES a paint may mount, as one contiguous window of a turn's `seq`
// space grown around the reader's own position. Pure and DOM-free; WHICH turns it is grown
// over is the caller's policy.
// ---------------------------------------------------------------------------

import { toolResultID } from "./entry-ids.js";
import { isInternalToolTitle } from "./tool-schema.js";
import type { Entry } from "./types.js";
import { payloadOf, type Turn } from "./turns.js";

/** What one paint's WINDOW may mount. TWO budgets, because a tool card is a whole
 *  disclosure where a text entry is part of a row, so whichever runs out first ends the
 *  side that asked. */
export const RESIDENT_ENTRIES = 320;
export const RESIDENT_TOOL_CALLS = 96;

/** The depth the window guarantees each side of its anchor, asserted as a FLOOR
 *  on it rather than fed in as an input. Two more consumers, both on the DEMAND
 *  side: `demandRange`'s half-width, shared by the pin and the walk, and
 *  `demandPin`'s arrival tolerance. One name, because all three are the same
 *  distance and separate constants are what would let them drift apart. */
export const OVERSCAN_ENTRIES = 24;

/** A paint budget, and the shape `turnCost` reports one turn's price in. BOTH fields count
 *  only what a view RENDERS, so either is comparable with the same field of a budget; the
 *  `seq` SPAN, which includes the ordinals nothing renders at, is `turnSpan`. */
export interface TurnCost {
  readonly entries: number;
  readonly toolCalls: number;
}

/** A turn's `tool_result` entries that carry a workflow id, keyed by their OWN entry id. */
export type RunResults = ReadonlyMap<string, string>;

const DEFAULT_BUDGET: TurnCost = {
  entries: RESIDENT_ENTRIES,
  toolCalls: RESIDENT_TOOL_CALLS,
};

/** A half-open range of a turn's entry `seq`: the plan's unit and the renderer's.
 *  One space serves both, because a turn's entries are one flat list. */
export interface EntryRange {
  readonly from: number;
  readonly to: number;
}

/** Where the reader is, in one turn's `seq` space. */
export interface ResidencyAnchor {
  readonly turnID: string;
  readonly at: number;
}

/** turn id → the entries that turn's body may hold. Only turns the window
 *  TOUCHES are present. */
export type ResidencyPlan = ReadonlyMap<string, EntryRange>;

/** The entry at `seq`, or undefined for the `turn_open` at 0 and for a `seq` past the
 *  turn. `TurnState`'s invariant is `entries[i].seq === i` and `Turn.body` is that array
 *  past the open, so the index is the seq less one. Exported for `turnOrdinalOf`'s reason:
 *  one decoder of this space, or a second is free to disagree with the one defining it. */
export function entryAt(t: Turn, seq: number): Entry | undefined {
  return seq <= 0 ? undefined : t.body[seq - 1];
}

/** How many ordinals `t` occupies: its entries, the `turn_open` at 0 included, because
 *  `seq` is the space and the header's own entry holds a place in it. */
export function turnSpan(t: Turn): number {
  return t.body.length + 1;
}

/** Whether this view renders anything AT THIS ENTRY'S OWN POSITION. Exported because the
 *  BUDGET and the PRICE must answer it identically: a spacer pricing entries the window
 *  charges nothing for is the same defect as the reverse.
 *
 *  `lane` is the VIEW's root, so an entry of any other lane is delegate content this view
 *  does not draw. `firstPlan` is that lane's `firstPlanSeq`, hoisted by the caller. The
 *  excluded kinds render elsewhere: `turn_open` as the card header, `turn_close` as the
 *  footer, a `tool_result` on its call's card, a `turn_bind` and a `reconciled` nowhere —
 *  the second is a fact about the RECORD, and `turnCost` prices only what this accepts, so
 *  this is also what keeps it out of the residency budget. */
export function entryRenders(e: Entry, lane: string, firstPlan: number): boolean {
  if (e.kind === "steer") {
    // The one kind whose lane does not decide where it draws: a steer is the READER's
    // words, so it renders in the parent's flow at its own `seq` whichever lane consumed
    // it, and never inside the delegate box, where the reader would not find it. The lane
    // reaches the note as a marker instead (`buildSteerNote`'s `lane`).
    return lane === "";
  }
  if ((e.lane ?? "") !== lane) {
    return false;
  }
  switch (e.kind) {
    case "turn_open":
    case "turn_close":
    case "turn_bind":
    case "tool_result":
    case "reconciled":
      return false;
    case "plan":
      return firstPlan === e.seq;
    default:
      return true;
  }
}

/** The `seq` of the turn's first `plan` entry in `lane`, or -1: the one `plan` that renders,
 *  every later one folding into its card. Hoisted ONCE per turn per pass and handed to
 *  `entryRenders`, for `runResults`' reason — a scan per plan entry is the turn's length
 *  times its plan count, and a long turn legitimately holds several. */
export function firstPlanSeq(t: Turn, lane: string): number {
  for (const e of t.body) {
    if (e.kind === "plan" && (e.lane ?? "") === lane) {
      return e.seq;
    }
  }
  return -1;
}

/** The turn's workflow-bearing `tool_result` entries, keyed by their own id so nothing has
 *  to parse a call id back out of one. Built ONCE per turn per pass and handed to
 *  `effectiveRunID`: both the budget and the price ask that question per tool call inside
 *  their own walk over the body, so a scan per call is quadratic in the turn's length. */
export function runResults(t: Turn): RunResults {
  const out = new Map<string, string>();
  for (const e of t.body) {
    if (e.kind !== "tool_result") {
      continue;
    }
    const runID = payloadOf(e, "tool_result")?.workflow_id ?? "";
    if (runID !== "") {
      out.set(e.id, runID);
    }
  }
  return out;
}

/** A tool call's EFFECTIVE workflow id: its own when the wire carries one, else its
 *  `tool_result`'s, through the index above. The live third source — the newest
 *  `tool_progress` for that call — is a store cell rather than an entry, so a run whose
 *  result has not landed has no owner here and every mention of it draws its own card,
 *  which is `ownsRunCard`'s own no-owner reading. */
export function effectiveRunID(e: Entry, results: RunResults): string {
  const own = payloadOf(e, "tool_call")?.workflow_id ?? "";
  return own !== "" ? own : (results.get(toolResultID(e.id)) ?? "");
}

/** run id → the id of the `tool_call` ENTRY that hosts that run's card: the first one in
 *  turn and `seq` order naming each run, every later mention rendering as a tool card.
 *
 *  Derived per pass rather than kept as a live registry, because the dispatcher's gates run
 *  in more than one pass over one turn: a registry written by the paint answers "no host
 *  yet, I host" to two calls of one turn, which is the double bind this exists to prevent.
 *  Scope is the RESIDENT window, so a run whose every mention is paged out has no card at
 *  all — `run-bar.ts` carries a live run and `/history` a finished one. */
export function runCardOwners(turns: readonly Turn[], lane = ""): ReadonlyMap<string, string> {
  const out = new Map<string, string>();
  for (const t of turns) {
    const results = runResults(t);
    for (const e of t.body) {
      const call = (e.lane ?? "") === lane ? payloadOf(e, "tool_call") : undefined;
      if (call === undefined || isInternalToolTitle(call.title)) {
        continue;
      }
      const runID = effectiveRunID(e, results);
      if (runID !== "" && !out.has(runID)) {
        out.set(runID, e.id);
      }
    }
  }
  return out;
}

/** What mounting `t`'s body costs this view: the entries it draws, and how many of those
 *  are tool calls. */
export function turnCost(t: Turn, lane = ""): TurnCost {
  const firstPlan = firstPlanSeq(t, lane);
  let entries = 0;
  let toolCalls = 0;
  for (const e of t.body) {
    if (!entryRenders(e, lane, firstPlan)) {
      continue;
    }
    entries++;
    if (e.kind === "tool_call") {
      toolCalls++;
    }
  }
  return { entries, toolCalls };
}

/** Whether this view draws NOTHING at any of `t`'s ordinals. The bodyless test, and a
 *  predicate rather than `turnCost(t).entries === 0` because the question is existence:
 *  a count answers it by pricing every entry, and the answer is known at the first one.
 *
 *  `firstPlanSeq` reads the WHOLE body, which would defeat that, so the lane's first
 *  `plan` is resolved on arrival instead — in `seq` order it IS that answer. Every other
 *  kind is `entryRenders`' to decide, so a kind the predicate does not draw stays one
 *  statement rather than a second list here. */
export function rendersNothing(t: Turn, lane = ""): boolean {
  let firstPlan = -1;
  for (const e of t.body) {
    if (firstPlan < 0 && e.kind === "plan" && (e.lane ?? "") === lane) {
      firstPlan = e.seq;
    }
    if (entryRenders(e, lane, firstPlan)) {
      return false;
    }
  }
  return true;
}

/** The `seq` of the entry `entryID`, or the turn's first ordinal when the id is absent.
 *  Undefined when the id names no entry of this turn.
 *
 *  In this module because the space is this module's: a second decoder elsewhere would be
 *  free to disagree with the one that defines it. */
export function turnOrdinalOf(t: Turn, entryID?: string): number | undefined {
  if (entryID === undefined || entryID === "") {
    return 0;
  }
  for (const e of t.body) {
    if (e.id === entryID) {
      return e.seq;
    }
  }
  return undefined;
}

/** `range` clamped into `t`'s span and SNAPPED to whole prose runs.
 *
 *  A run is one `.msg-row` holding one markdown stream (section 8.5), so an edge inside one
 *  would mount it as two rows with two parsers and a visible seam. `from` moves down to the
 *  run's first entry and `to` up past its last; the budget still charges per entry, so the
 *  snap can overspend it by at most one run on each side, which `RESIDENT_ENTRIES` already
 *  tolerates. */
export function sliceTurn(
  t: Turn,
  range: EntryRange,
  lane = "",
  // The lane's `firstPlanSeq`, resolved below only for a caller that holds none: a caller
  // inside a per-item loop already has it (`firstPlanSeq` states why one is hoisted).
  firstPlan?: number,
): EntryRange {
  const span = turnSpan(t);
  const from = Math.min(Math.max(range.from, 0), span);
  const to = Math.min(Math.max(range.to, from), span);
  if (from >= to) {
    return { from, to };
  }
  const plan = firstPlan ?? firstPlanSeq(t, lane);
  return { from: runStart(t, from, lane, plan), to: runEnd(t, to - 1, lane, plan) + 1 };
}

/** The prose run `seq` belongs to, as the `[from, to)` its members span, or null when `seq`
 *  is not one. The MOUNT's door onto the rule this file's window snap already applies, so an
 *  edge and a bubble cannot disagree about where a run begins. */
export function proseRunAt(
  t: Turn,
  seq: number,
  lane: string,
  firstPlan: number,
): EntryRange | null {
  if (!inProseRun(t, seq, lane, firstPlan)) {
    return null;
  }
  return { from: runStart(t, seq, lane, firstPlan), to: runEnd(t, seq, lane, firstPlan) + 1 };
}

/** Whether `seq` is a `text` entry this view draws — a prose run's member. */
function inProseRun(t: Turn, seq: number, lane: string, firstPlan: number): boolean {
  const e = entryAt(t, seq);
  return e?.kind === "text" && entryRenders(e, lane, firstPlan);
}

/** The first `seq` of the prose run `seq` sits in, or `seq` when it is not in one. An entry
 *  that renders nothing does not break a run, so the walk steps over it. */
function runStart(t: Turn, seq: number, lane: string, firstPlan: number): number {
  if (!inProseRun(t, seq, lane, firstPlan)) {
    return seq;
  }
  let first = seq;
  for (let i = seq - 1; i > 0; i--) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (inProseRun(t, i, lane, firstPlan)) {
      first = i;
    } else if (entryRenders(e, lane, firstPlan)) {
      break;
    }
  }
  return first;
}

/** The last `seq` of the prose run `seq` sits in, or `seq` when it is not in one. */
function runEnd(t: Turn, seq: number, lane: string, firstPlan: number): number {
  if (!inProseRun(t, seq, lane, firstPlan)) {
    return seq;
  }
  let last = seq;
  const span = turnSpan(t);
  for (let i = seq + 1; i < span; i++) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (inProseRun(t, i, lane, firstPlan)) {
      last = i;
    } else if (entryRenders(e, lane, firstPlan)) {
      break;
    }
  }
  return last;
}

/** The entries each turn's body may hold this paint.
 *
 *  `turns` is the sequence the window is grown over, newest LAST, ALREADY FILTERED to the
 *  turns that would render open if bodied. `anchor` says where the reader is; absent, or
 *  naming a turn the sequence does not hold, is the live edge, and one naming a turn that
 *  holds NO entry seeds at the nearest ordinal the sequence does. A turn no entry reaches is
 *  absent from the answer. */
export function planResidency(
  turns: readonly Turn[],
  anchor: ResidencyAnchor | undefined,
  // The VIEW's root, and NO default, for `spacerHeight`'s reason from the other side: this
  // is the function a view calls, so a silent `""` plans the window over lane `""` while
  // the spacers price a delegate's. It leads the budget, which only a test states.
  lane: string,
  budget: TurnCost = DEFAULT_BUDGET,
): ResidencyPlan {
  const plan = new Map<string, EntryRange>();
  const bases: number[] = [];
  const spans: number[] = [];
  // Both charges are properties of the ORDINAL, so neither can come from `turnCost`:
  // one flat pass mints the sequence and the flags together.
  const isTool: boolean[] = [];
  const isFree: boolean[] = [];
  // Kept per turn so the final loop hands it to `sliceTurn` instead of re-scanning the body.
  const firstPlans: number[] = [];
  for (const t of turns) {
    const base = isTool.length;
    const span = turnSpan(t);
    const firstPlan = firstPlanSeq(t, lane);
    firstPlans.push(firstPlan);
    for (let seq = 0; seq < span; seq++) {
      const e = entryAt(t, seq);
      const free = e === undefined || !entryRenders(e, lane, firstPlan);
      isFree.push(free);
      isTool.push(!free && e.kind === "tool_call");
    }
    bases.push(base);
    spans.push(span);
  }
  const total = isTool.length;
  if (total === 0) {
    return plan;
  }

  let at = total - 1;
  if (anchor !== undefined) {
    const i = turns.findIndex((t) => t.id === anchor.turnID);
    const base = bases[i] ?? -1;
    const span = spans[i] ?? 0;
    if (base >= 0) {
      // A zero-span turn's own base IS the next turn's first ordinal, and the
      // clamp is what answers a trailing one, whose base is past the end.
      at =
        span === 0 ? Math.min(base, total - 1) : base + Math.min(Math.max(anchor.at, 0), span - 1);
    }
  }

  // ONE seed, one ordinal per side per step: what makes an island unrepresentable.
  // The budget is SHARED, so a side latched at the sequence end reserves nothing.
  let lo = at;
  let hi = at + 1;
  let entries = isFree[at] === true ? 0 : 1;
  let toolCalls = isTool[at] === true ? 1 : 0;
  let headLatched = lo === 0;
  let tailLatched = hi === total;
  let head = true;
  while (!headLatched || !tailLatched) {
    if (head ? !headLatched : !tailLatched) {
      const next = head ? lo - 1 : hi;
      const tool = isTool[next] === true ? 1 : 0;
      // A FREE ordinal is taken unconditionally and latches nothing: this view renders
      // nothing at it, so a budget spent on one buys the reader nothing. It stays an
      // ORDINAL — the span is the renderer's own coordinate system.
      if (
        isFree[next] !== true &&
        (entries + 1 > budget.entries || toolCalls + tool > budget.toolCalls)
      ) {
        if (head) {
          headLatched = true;
        } else {
          tailLatched = true;
        }
      } else {
        if (isFree[next] !== true) {
          entries++;
        }
        toolCalls += tool;
        if (head) {
          lo = next;
          headLatched = lo === 0;
        } else {
          hi = next + 1;
          tailLatched = hi === total;
        }
      }
    }
    head = !head;
  }

  for (const [i, t] of turns.entries()) {
    const base = bases[i] ?? 0;
    const from = Math.max(lo, base);
    const to = Math.min(hi, base + (spans[i] ?? 0));
    if (from < to) {
      plan.set(t.id, sliceTurn(t, { from: from - base, to: to - base }, lane, firstPlans[i]));
    }
  }
  return plan;
}
