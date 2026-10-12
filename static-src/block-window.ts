// Residency: which ENTRIES a paint may mount, one contiguous window of a turn's `seq` space
// grown around the reader's position. Pure and DOM-free; which turns is the caller's policy.

import { toolResultID } from "./entry-ids.js";
import { isInternalToolTitle } from "./tool-schema.js";
import type { Entry } from "./types.js";
import { payloadOf, type Turn } from "./turns.js";

/**
 * What one paint's WINDOW may mount. Two budgets (a tool card is a whole disclosure, a text
 * entry part of a row); whichever runs out first ends that side.
 */
export const RESIDENT_ENTRIES = 320;
export const RESIDENT_TOOL_CALLS = 96;

/**
 * The depth the window guarantees each side of its anchor, asserted as a FLOOR. Also
 * `demandRange`'s half-width and `demandPin`'s arrival tolerance: one distance, one name.
 */
export const OVERSCAN_ENTRIES = 24;

/**
 * A paint budget, and the shape `turnCost` reports. Both fields count only what a view
 * RENDERS; the `seq` SPAN is `turnSpan`.
 */
interface TurnCost {
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

/**
 * The entry at `seq`, or undefined for the `turn_open` at 0 and past the turn
 * (`entries[i].seq === i`, so the index is seq less one). Exported so there is one decoder.
 */
export function entryAt(t: Turn, seq: number): Entry | undefined {
  return seq <= 0 ? undefined : t.body[seq - 1];
}

/** How many ordinals `t` occupies: its entries, the `turn_open` at 0 included, because
 *  `seq` is the space and the header's own entry holds a place in it. */
export function turnSpan(t: Turn): number {
  return t.body.length + 1;
}

/**
 * Whether this view renders anything AT THIS ENTRY'S OWN POSITION; the budget and the price
 * must answer identically. `lane` is the VIEW's root; `folds` is that lane's `turnFolds`.
 * `turn_open`, `turn_close`, `tool_result`, `turn_bind` and `reconciled` render elsewhere or
 * nowhere.
 */
export function entryRenders(e: Entry, lane: string, folds: TurnFolds): boolean {
  if (e.kind === "steer") {
    // A steer is the READER's words, so it renders in the parent's flow whichever lane consumed
    // it; the lane reaches the note as a marker (`buildSteerNote`).
    return lane === "" && !(folds.readDrop.has(e.id) && payloadOf(e, "steer")?.state === "dropped");
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
    case "steer_delivered":
      return false;
    case "plan":
      return folds.plan === e.seq;
    case "turn_revert":
      return folds.revert === e.seq;
    case "model_switched":
      return !folds.fellBack.has(e.seq);
    default:
      return true;
  }
}

/** The one entry of each folding kind that renders in a lane, by `seq`, -1 for none: the FIRST
 *  `plan` (a later one replaces its card's state) and the NEWEST `turn_revert` (rewinding to the
 *  same turn again replaces its "Rewound to here" row). `readDrop` holds the turn's dropped agent
 *  notes whose id the same turn read, as a read note or a take-up's `kas_id`: KAS's boundary clear
 *  names what the turn injected, and that is no loss to show. `fellBack` holds the `seq` of each
 *  `model_switched{reason: "unavailable"}` whose next entry in the lane is the chat's own (not a
 *  sub-agent's) `model_fallback` to the same model, per `fallsBackFrom`: KAS re-pins before it
 *  sends the notice, so the switch row's guessed cause is the notice's to state. */
export interface TurnFolds {
  readonly plan: number;
  readonly revert: number;
  readonly readDrop: ReadonlySet<string>;
  readonly fellBack: ReadonlySet<number>;
}

/** `entries`' folds per lane. Hoisted ONCE per turn per pass, since a long turn holds several
 *  plans; takes a `TurnState`'s entries or a `Turn`'s body alike. */
export function foldsByLane(entries: readonly Entry[]): Map<string, TurnFolds> {
  const readDrop = readDrops(entries);
  const out = new Map<
    string,
    { plan: number; revert: number; readDrop: ReadonlySet<string>; fellBack: Set<number> }
  >();
  const fold = (lane: string) => {
    let f = out.get(lane);
    if (f === undefined) {
      f = { plan: -1, revert: -1, readDrop, fellBack: new Set() };
      out.set(lane, f);
    }
    return f;
  };
  const prevInLane = new Map<string, Entry>();
  for (const e of entries) {
    const lane = e.lane ?? "";
    const prev = prevInLane.get(lane);
    prevInLane.set(lane, e);
    if (e.kind === "plan") {
      const f = fold(lane);
      if (f.plan < 0) {
        f.plan = e.seq;
      }
    } else if (e.kind === "turn_revert") {
      fold(lane).revert = e.seq;
    } else if (e.kind === "model_fallback" && prev !== undefined && fallsBackFrom(prev, e)) {
      fold(lane).fellBack.add(prev.seq);
    }
  }
  if (readDrop.size > 0 && !out.has("")) {
    out.set("", { plan: -1, revert: -1, readDrop, fellBack: new Set() });
  }
  return out;
}

/** Whether `fallback` names the move `prev` recorded as an unexplained re-pin. A sub-agent's
 *  fallback never moves the chat's model, so it explains no chat re-pin. */
function fallsBackFrom(prev: Entry, fallback: Entry): boolean {
  const switched = payloadOf(prev, "model_switched");
  const fb = payloadOf(fallback, "model_fallback");
  const to = fb?.to ?? "";
  return (
    switched?.reason === "unavailable" && fb?.subagent !== true && to !== "" && switched.to === to
  );
}

const NONE: ReadonlySet<string> = new Set();
const NO_SEQS: ReadonlySet<number> = new Set();

/** The ids of `entries`' dropped agent notes the same entries read (`TurnFolds.readDrop`). */
function readDrops(entries: readonly Entry[]): ReadonlySet<string> {
  const read = new Set<string>();
  const dropped: string[] = [];
  for (const e of entries) {
    const steer = payloadOf(e, "steer");
    if (steer?.origin === "agent") {
      if (steer.state === "dropped") {
        dropped.push(e.id);
      } else {
        read.add(e.id);
      }
    }
    const d = payloadOf(e, "steer_delivered");
    if (d !== undefined && d.dropped !== true && d.kas_id !== undefined && d.kas_id !== "") {
      read.add(d.kas_id);
    }
  }
  const out = dropped.filter((id) => read.has(id));
  return out.length === 0 ? NONE : new Set(out);
}

/** The folds of a lane that holds no folding kind. */
export const NO_FOLDS: TurnFolds = { plan: -1, revert: -1, readDrop: NONE, fellBack: NO_SEQS };

/** `t`'s folds in `lane`. */
export function turnFolds(t: Turn, lane: string): TurnFolds {
  return foldsByLane(t.body).get(lane) ?? NO_FOLDS;
}

/**
 * The turn's workflow-bearing `tool_result` entries, keyed by their own id, built ONCE per
 * pass for `effectiveRunID` (a per-call scan is quadratic).
 */
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

/**
 * A tool call's EFFECTIVE workflow id: its own, else its `tool_result`'s. A run whose
 * result has not landed has no owner here, so each mention draws its own card.
 */
export function effectiveRunID(e: Entry, results: RunResults): string {
  const own = payloadOf(e, "tool_call")?.workflow_id ?? "";
  return own !== "" ? own : (results.get(toolResultID(e.id)) ?? "");
}

/**
 * run id → the `tool_call` entry hosting that run's card: the first naming it in turn and
 * `seq` order. Derived per pass: the gates run in several passes, and a live registry
 * would let two calls each host. A fully paged-out run has no card.
 */
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

/**
 * Whether this view draws NOTHING at any of `t`'s ordinals; an existence test, answered at
 * the first drawn entry. The lane's first `plan` is resolved on arrival, in `seq` order.
 */
export function rendersNothing(t: Turn, lane = ""): boolean {
  // The whole body is read only for a dropped agent note, whose answer turns on entries anywhere in the body.
  let readDrop: ReadonlySet<string> | undefined;
  let plan = -1;
  for (const e of t.body) {
    if (plan < 0 && e.kind === "plan" && (e.lane ?? "") === lane) {
      plan = e.seq;
    }
    // Any revert in the lane answers: the newest one renders, whichever that is. A switch row a
    // fallback folds answers too, since the fallback after it renders.
    const renders =
      e.kind === "turn_revert"
        ? (e.lane ?? "") === lane
        : entryRenders(e, lane, {
            plan,
            revert: -1,
            readDrop:
              payloadOf(e, "steer")?.state === "dropped" ? (readDrop ??= readDrops(t.body)) : NONE,
            fellBack: NO_SEQS,
          });
    if (renders) {
      return false;
    }
  }
  return true;
}

/**
 * The `seq` of the entry `entryID`, or the turn's first ordinal when absent; undefined when
 * the id names no entry here. Here so this space has one decoder.
 */
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

/**
 * `range` clamped into `t`'s span and SNAPPED to whole prose runs, since a run is one
 * `.msg-row` with one markdown stream. The snap can overspend the budget by one run per
 * side, which `RESIDENT_ENTRIES` tolerates.
 */
export function sliceTurn(
  t: Turn,
  range: EntryRange,
  lane = "",
  // The lane's `turnFolds`, resolved below only for a caller that holds none: a caller
  // inside a per-item loop already has them (`foldsByLane` states why they are hoisted).
  folds?: TurnFolds,
): EntryRange {
  const span = turnSpan(t);
  const from = Math.min(Math.max(range.from, 0), span);
  const to = Math.min(Math.max(range.to, from), span);
  if (from >= to) {
    return { from, to };
  }
  const f = folds ?? turnFolds(t, lane);
  return { from: runStart(t, from, lane, f), to: runEnd(t, to - 1, lane, f) + 1 };
}

/**
 * The prose run `seq` belongs to, as `[from, to)`, or null: the mount's door onto the
 * window snap's rule.
 */
export function proseRunAt(
  t: Turn,
  seq: number,
  lane: string,
  folds: TurnFolds,
): EntryRange | null {
  if (!inProseRun(t, seq, lane, folds)) {
    return null;
  }
  return { from: runStart(t, seq, lane, folds), to: runEnd(t, seq, lane, folds) + 1 };
}

/** Whether `seq` is a `text` entry this view draws — a prose run's member. */
function inProseRun(t: Turn, seq: number, lane: string, folds: TurnFolds): boolean {
  const e = entryAt(t, seq);
  return e?.kind === "text" && entryRenders(e, lane, folds);
}

/** The first `seq` of the prose run `seq` sits in, or `seq` when it is not in one. An entry
 *  that renders nothing does not break a run, so the walk steps over it. */
function runStart(t: Turn, seq: number, lane: string, folds: TurnFolds): number {
  if (!inProseRun(t, seq, lane, folds)) {
    return seq;
  }
  let first = seq;
  for (let i = seq - 1; i > 0; i--) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (inProseRun(t, i, lane, folds)) {
      first = i;
    } else if (entryRenders(e, lane, folds)) {
      break;
    }
  }
  return first;
}

/** The last `seq` of the prose run `seq` sits in, or `seq` when it is not in one. */
function runEnd(t: Turn, seq: number, lane: string, folds: TurnFolds): number {
  if (!inProseRun(t, seq, lane, folds)) {
    return seq;
  }
  let last = seq;
  const span = turnSpan(t);
  for (let i = seq + 1; i < span; i++) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (inProseRun(t, i, lane, folds)) {
      last = i;
    } else if (entryRenders(e, lane, folds)) {
      break;
    }
  }
  return last;
}

/**
 * The entries each turn's body may hold this paint. `turns` is newest LAST, already
 * filtered to turns that render open. An absent or unknown `anchor` is the live edge; one
 * naming an empty turn seeds at the nearest ordinal. A turn no entry reaches is absent.
 */
export function planResidency(
  turns: readonly Turn[],
  anchor: ResidencyAnchor | undefined,
  // The VIEW's root, with NO default, for `spacerHeight`'s reason.
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
  // Kept per turn so the final loop hands them to `sliceTurn` instead of re-scanning the body.
  const turnFoldsAt: TurnFolds[] = [];
  for (const t of turns) {
    const base = isTool.length;
    const span = turnSpan(t);
    const folds = turnFolds(t, lane);
    turnFoldsAt.push(folds);
    for (let seq = 0; seq < span; seq++) {
      const e = entryAt(t, seq);
      const free = e === undefined || !entryRenders(e, lane, folds);
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
      // A FREE ordinal is taken unconditionally and latches nothing: this view renders nothing
      // there.
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
      plan.set(t.id, sliceTurn(t, { from: from - base, to: to - base }, lane, turnFoldsAt[i]));
    }
  }
  return plan;
}
