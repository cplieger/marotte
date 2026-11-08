// Residency: which ENTRIES a paint may mount, one contiguous window of a turn's `seq` space
// grown around the reader's position. Pure and DOM-free; which turns is the caller's policy.

import { MessageRun, callIDOfToolResult, toolResultID } from "./entry-ids.js";
import { bodyMemo } from "./body-memo.js";
import { hookRenders, hookStatusShown } from "./hook-fold.js";
import { isInternalToolTitle } from "./tool-schema.js";
import { isWakingRow } from "./waking-rows.js";
import type { Entry, EntryToolResult } from "./types.js";
import { isSubagentFallback, payloadOf, type Turn } from "./turns.js";

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
 * `turn_open`, `turn_close`, `tool_result`, `turn_bind`, `turn_wake`, `run_named`, `reconciled` and a
 * sub-agent's `model_fallback` render elsewhere or nowhere.
 */
export function entryRenders(e: Entry, lane: string, folds: TurnFolds): boolean {
  if (e.kind === "steer") {
    // A steer is the READER's words, so it renders in the parent's flow whichever lane consumed
    // it; the lane reaches the note as a marker (`buildSteerNote`). A row a wake draws as its
    // turn's trigger renders there instead.
    return (
      lane === "" &&
      !isWakingRow(e) &&
      !(folds.readDrop.has(e.id) && payloadOf(e, "steer")?.state === "dropped")
    );
  }
  if ((e.lane ?? "") !== lane) {
    return false;
  }
  switch (e.kind) {
    case "turn_open":
    case "turn_close":
    case "step_graded":
    case "turn_bind":
    case "turn_wake":
    case "run_named":
    case "tool_result":
    case "reconciled":
    case "steer_delivered":
    case "steer_cleared":
      return false;
    case "plan":
      return folds.plan === e.seq;
    case "turn_revert":
      return folds.revert === e.seq;
    case "model_switched":
      return !folds.fellBack.has(e.seq);
    case "model_fallback":
      return !isSubagentFallback(e);
    case "tool_call":
      return hookCallRenders(e, folds);
    default:
      return true;
  }
}

/** A hook call's card renders per `hookRenders` over its settled value; any other call renders. */
function hookCallRenders(e: Entry, folds: TurnFolds): boolean {
  const call = payloadOf(e, "tool_call");
  if (call?.kind !== "hook") {
    return true;
  }
  const result = folds.hookResults.get(e.id);
  return hookRenders(
    {
      status: result?.status ?? call.status,
      output: result?.output ?? call.output,
      hook_trigger: call.hook_trigger,
    },
    hookStatusShown(),
  );
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
  /** The settling `tool_result` of each hook call, keyed by the call's entry id; turn-wide like `readDrop`. */
  readonly hookResults: ReadonlyMap<string, EntryToolResult>;
}

/** A turn body's folds per lane, derived once per body array (`bodyMemo`). */
export function foldsByLane(body: readonly Entry[]): ReadonlyMap<string, TurnFolds> {
  return lanes(body, "", () => deriveLanes(body));
}

const lanes = bodyMemo<ReadonlyMap<string, TurnFolds>>();

function deriveLanes(entries: readonly Entry[]): ReadonlyMap<string, TurnFolds> {
  const readDrop = readDrops(entries);
  const hookResults = hookResultsOf(entries);
  const out = new Map<
    string,
    {
      plan: number;
      revert: number;
      readDrop: ReadonlySet<string>;
      fellBack: Set<number>;
      hookResults: ReadonlyMap<string, EntryToolResult>;
    }
  >();
  const fold = (lane: string) => {
    let f = out.get(lane);
    if (f === undefined) {
      f = { plan: -1, revert: -1, readDrop, fellBack: new Set(), hookResults };
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
    } else if (e.kind === "tool_result" && hookResults.size > 0) {
      // The result sits in its call's lane, which must then carry the map.
      fold(lane);
    }
  }
  if (readDrop.size > 0 && !out.has("")) {
    out.set("", { plan: -1, revert: -1, readDrop, fellBack: new Set(), hookResults });
  }
  return out;
}

const NO_RESULTS: ReadonlyMap<string, EntryToolResult> = new Map();

/** Each hook call's settling result in `entries`, keyed by the call's entry id. */
function hookResultsOf(entries: readonly Entry[]): ReadonlyMap<string, EntryToolResult> {
  const hooks = new Set<string>();
  let out: Map<string, EntryToolResult> | undefined;
  for (const e of entries) {
    if (payloadOf(e, "tool_call")?.kind === "hook") {
      hooks.add(e.id);
      continue;
    }
    const result = payloadOf(e, "tool_result");
    const callID = result === undefined ? null : callIDOfToolResult(e.id);
    if (result !== undefined && callID !== null && hooks.has(callID)) {
      (out ??= new Map()).set(callID, result);
    }
  }
  return out ?? NO_RESULTS;
}

/** Whether `fallback` names the move `prev` recorded as an unexplained re-pin. A sub-agent's
 *  fallback never moves the chat's model, so it explains no chat re-pin. */
function fallsBackFrom(prev: Entry, fallback: Entry): boolean {
  const switched = payloadOf(prev, "model_switched");
  const to = payloadOf(fallback, "model_fallback")?.to ?? "";
  return (
    switched?.reason === "unavailable" &&
    !isSubagentFallback(fallback) &&
    to !== "" &&
    switched.to === to
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
    if (d?.kas_id !== undefined && d.kas_id !== "") {
      read.add(d.kas_id);
    }
  }
  const out = dropped.filter((id) => read.has(id));
  return out.length === 0 ? NONE : new Set(out);
}

/** The folds of a lane that holds no folding kind. */
export const NO_FOLDS: TurnFolds = {
  plan: -1,
  revert: -1,
  readDrop: NONE,
  fellBack: NO_SEQS,
  hookResults: NO_RESULTS,
};

/** `t`'s folds in `lane`. */
export function turnFolds(t: Turn, lane: string): TurnFolds {
  return foldsByLane(t.body).get(lane) ?? NO_FOLDS;
}

/** The turn's workflow-bearing `tool_result` entries, keyed by their own id, for `effectiveRunID`. */
export function runResults(t: Turn): RunResults {
  return resultsOfBody(t.body);
}

const results = bodyMemo<RunResults>();

function resultsOfBody(body: readonly Entry[]): RunResults {
  return results(body, "", () => {
    const out = new Map<string, string>();
    for (const e of body) {
      if (e.kind !== "tool_result") {
        continue;
      }
      const runID = payloadOf(e, "tool_result")?.workflow_id ?? "";
      if (runID !== "") {
        out.set(e.id, runID);
      }
    }
    return out;
  });
}

/**
 * A tool call's EFFECTIVE workflow id: its own, else its `tool_result`'s. A run whose
 * result has not landed has no owner here, so each mention draws its own card.
 */
export function effectiveRunID(e: Entry, results: RunResults): string {
  const own = payloadOf(e, "tool_call")?.workflow_id ?? "";
  return own !== "" ? own : (results.get(toolResultID(e.id)) ?? "");
}

/** The `tool_call` entry hosting a run's card, and the turn holding that entry. */
interface RunCardOwner {
  readonly turn: string;
  readonly entry: string;
}

/** run id → its card's host, in turn and `seq` order. */
export type RunCardOwners = ReadonlyMap<string, RunCardOwner>;

// The transcript's owners for the current pass, read by the renderer and the spacer alike so the two cannot price
// one mention differently. Derived per pass, never a live registry, which would let two mentions both claim a card.
let transcriptOwners: RunCardOwners = new Map();

/** Name the transcript's run-card owners for this pass, `runCardOwners` over its held turns. */
export function setRunCardOwners(owners: RunCardOwners): void {
  transcriptOwners = owners;
}

/** Whether `entryID` hosts `runID`'s card in the transcript: the run's owner, or any mention of a run with none. */
export function hostsRunCard(runID: string, entryID: string): boolean {
  const owner = transcriptOwners.get(runID);
  return owner === undefined || owner.entry === entryID;
}

/**
 * run id → the `tool_call` entry hosting that run's card: the first naming it in turn and
 * `seq` order. Derived per pass: the gates run in several passes, and a live registry
 * would let two calls each host. A fully paged-out run has no card.
 */
export function runCardOwners(turns: readonly Turn[], lane = ""): RunCardOwners {
  const out = new Map<string, RunCardOwner>();
  for (const t of turns) {
    for (const [runID, entry] of runMentions(t.body, lane, () => runMentionsOf(t.body, lane))) {
      if (!out.has(runID)) {
        out.set(runID, { turn: t.id, entry });
      }
    }
  }
  return out;
}

const runMentions = bodyMemo<ReadonlyMap<string, string>>();

/** run id → the first `tool_call` in `lane` naming it, in `seq` order. */
function runMentionsOf(body: readonly Entry[], lane: string): ReadonlyMap<string, string> {
  const results = resultsOfBody(body);
  const out = new Map<string, string>();
  for (const e of body) {
    const call = (e.lane ?? "") === lane ? payloadOf(e, "tool_call") : undefined;
    if (call === undefined || isInternalToolTitle(call.title)) {
      continue;
    }
    const runID = effectiveRunID(e, results);
    if (runID !== "" && !out.has(runID)) {
      out.set(runID, e.id);
    }
  }
  return out;
}

/**
 * Whether this view draws NOTHING at any of `t`'s ordinals; an existence test, answered at
 * the first drawn entry. The lane's first `plan` is resolved on arrival, in `seq` order.
 */
export function rendersNothing(t: Turn, lane = ""): boolean {
  // `entryRenders` reads `hookStatusShown`.
  return drawsNothing(t.body, `${String(hookStatusShown())}|${lane}`, () =>
    deriveDrawsNothing(t.body, lane),
  );
}

const drawsNothing = bodyMemo<boolean>();

function deriveDrawsNothing(body: readonly Entry[], lane: string): boolean {
  // The whole body is read only for a dropped agent note or a hook call, whose answers turn on entries anywhere in it.
  let readDrop: ReadonlySet<string> | undefined;
  let hookResults: ReadonlyMap<string, EntryToolResult> | undefined;
  let plan = -1;
  for (const e of body) {
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
              payloadOf(e, "steer")?.state === "dropped" ? (readDrop ??= readDrops(body)) : NONE,
            fellBack: NO_SEQS,
            hookResults:
              payloadOf(e, "tool_call")?.kind === "hook"
                ? (hookResults ??= hookResultsOf(body))
                : NO_RESULTS,
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
  return {
    from: runBounds(t, from, lane, f)?.from ?? from,
    to: runBounds(t, to - 1, lane, f)?.to ?? to,
  };
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
  return runBounds(t, seq, lane, folds);
}

/** Whether `e`, a text entry following the run `run`, continues its message: where `proseRunAt` places it sealed. */
export function continuesRun(
  t: Turn,
  run: EntryRange,
  e: { readonly id: string; readonly turn: string },
  lane: string,
  folds: TurnFolds,
): boolean {
  const msg = new MessageRun();
  for (let seq = run.from; seq < run.to; seq++) {
    const m = entryAt(t, seq);
    if (m !== undefined && inProseRun(t, seq, lane, folds)) {
      msg.take(m);
    }
  }
  return msg.take(e);
}

/** Whether `seq` is a `text` entry this view draws — a prose run's member. */
function inProseRun(t: Turn, seq: number, lane: string, folds: TurnFolds): boolean {
  const e = entryAt(t, seq);
  return e?.kind === "text" && entryRenders(e, lane, folds);
}

/**
 * The run `seq` sits in, from its first text member to past its last, or null when `seq` is not prose. Runs
 * partition a STRETCH (text entries with nothing drawn between them) by `MessageRun`, fed from the stretch's
 * start, so every caller sees one partition whichever member it asks from.
 */
function runBounds(t: Turn, seq: number, lane: string, folds: TurnFolds): EntryRange | null {
  if (!inProseRun(t, seq, lane, folds)) {
    return null;
  }
  let start = seq;
  for (let i = seq - 1; i > 0; i--) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (inProseRun(t, i, lane, folds)) {
      start = i;
    } else if (entryRenders(e, lane, folds)) {
      break;
    }
  }
  let run = new MessageRun();
  let from = start;
  let last = start;
  const span = turnSpan(t);
  for (let i = start; i < span; i++) {
    const e = entryAt(t, i);
    if (e === undefined) {
      break;
    }
    if (!inProseRun(t, i, lane, folds)) {
      if (entryRenders(e, lane, folds)) {
        break;
      }
      continue;
    }
    if (!run.take(e)) {
      if (i > seq) {
        break;
      }
      run = new MessageRun();
      run.take(e);
      from = i;
    }
    last = i;
  }
  return { from, to: last + 1 };
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
