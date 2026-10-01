// ---------------------------------------------------------------------------
// What a range of a turn's entries is worth in PIXELS: the height unmounted space holds, so
// the document's height cannot depend on the window. Measured at the DROP, which suffices:
// everything ABOVE the reader has been mounted and dropped once.
// ---------------------------------------------------------------------------

import {
  effectiveRunID,
  entryAt,
  entryRenders,
  firstPlanSeq,
  runResults,
  sliceTurn,
  turnSpan,
  type EntryRange,
  type RunResults,
} from "./block-window.js";
// The stage-to-driver join, off the stage's own tool-call id. Imported rather than
// re-derived, because that id format has two owners already and this leaf adds no DOM.
import { pipelineOf } from "./subagent-slice.js";
import type { Entry, EntryToolCall } from "./types.js";
import { payloadOf, type Turn } from "./turns.js";
// Type-only, so this costs no import edge at runtime: `tierNow` below reads the
// attribute rather than calling `pointer-tier.ts`, and this is only the vocabulary.
import type { PointerTier } from "./device-view.js";

/** What one entry is worth before it has ever been measured, keyed on what it MOUNTS AS
 *  rather than on its kind, because one kind mounts four shapes. A PROSE RUN is one row
 *  whatever its entry count, so `text` prices the run's first entry and the rest nothing.
 *
 *  EACH VALUE IS THE RENDERED BORDER BOX, never the declared `contain-intrinsic-size`,
 *  which states the CONTENT box alone: an estimate and a measurement are summed
 *  interchangeably. The rule each value shadows is named at it, and the two with no reserve
 *  to shadow are heights measured on the assembled stylesheet at both tiers. */
export const ENTRY_ESTIMATE_PX: Readonly<Record<PointerTier, EntryEstimates>> = {
  fine: {
    text: 48, // 13-messages.css `.msg-row` — `auto 3rem`, no padding or border
    emptyText: 0, // `.msg-row.is-empty` — `display: none`
    thinking: 25, // NO reserve: a sealed trace's own `<summary>` row
    runCard: 71, // 27-run-card.css `.run-card` — `auto var(--run-card-content)` + 2px
    toolCard: 38, // 14-tools.css `.tool-call` — `auto var(--btn-h)` + 2px
    subagentCard: 71, // `.subagent-block` — a delegate's card AND a pipeline's container
    planCard: 70, // `.plan-message` — `auto 2.75rem` + `--sp-3` twice + 2px
    steerNote: 62, // NO reserve: a one-line note, head + one text line + padding
    row: 48, // 13-messages.css `.msg-row` — an event row
  },
  coarse: {
    text: 48,
    emptyText: 0,
    // Tier-dependent because 61-mcp-tools.css's universal hit floor includes `summary`;
    // the fine value is that row's own line box, so it moves with the font stack.
    thinking: 44,
    runCard: 91,
    toolCard: 46,
    subagentCard: 91,
    // Tier-INVARIANT: the reserve is two line boxes plus a spacing token, and the card
    // reads no control-height token (14-tools.css `.plan-message`).
    planCard: 70,
    // The head is `--btn-h`, which tiers; the text line and the body's padding do not.
    steerNote: 70,
    row: 48,
  },
} as const;

/** One tier's prices. */
export interface EntryEstimates {
  readonly text: number;
  readonly emptyText: number;
  readonly thinking: number;
  readonly runCard: number;
  readonly toolCard: number;
  readonly subagentCard: number;
  readonly planCard: number;
  readonly steerNote: number;
  readonly row: number;
}

/** The flex `gap` (`--sp-3`) `.turn-body` puts between the entries it holds
 *  (css/29-turns.css `.turn-body`), which is itself an assertion about that rule.
 *  Tier-invariant: `--sp-3` reads no pointer query.
 *
 *  The PARENT adds it and no child's own height includes it, so K replaced children
 *  carry K−1 of them between themselves. The spacer standing in for them is a SIBLING
 *  of `.turn-body` under `.turn`, which declares no `gap`, so it carries the boundary
 *  one too — `spacerHeight` adds both terms. */
export const ROW_GAP_PX = 12;

/** Which tier the document is laid out for, READ off the attribute rather than through
 *  `pointer-tier.ts`, whose import chain reaches `device-view.ts` and `localStorage` where
 *  this module has to keep answering with no DOM at all.
 *
 *  The absent-attribute arm MIRRORS THE CASCADE rather than guessing: `01-tokens.css`
 *  carries a no-JS fallback under `@media (width <= 48rem)`, so an unset attribute takes
 *  the coarse values exactly when that query matches. */
function tierNow(): PointerTier {
  // A NULLABLE view of the global, not the DOM lib's: read through that type,
  // `no-unnecessary-condition` proves these guards dead and offers to cut them.
  const g = globalThis as {
    readonly document?: { readonly documentElement?: Element };
    readonly matchMedia?: (q: string) => { readonly matches: boolean };
  };
  const attr = g.document?.documentElement?.getAttribute("data-pointer");
  if (attr === "fine" || attr === "coarse") {
    return attr;
  }
  return (g.matchMedia?.("(width <= 48rem)").matches ?? false) ? "coarse" : "fine";
}

/** turn id → `seq` → the height that entry's element measured. */
const entryHeights = new Map<string, Map<number, number>>();

/** turn id → a prose run's first `seq` → the range that row measured and what it
 *  measured. Range-keyed because a row dropped under a PARTIAL window measured that
 *  slice only, and answering the whole run with it prices the rest at zero. */
const rowHeights = new Map<string, Map<number, { range: EntryRange; px: number }>>();

/** The tool call that OPENS a subagent-orchestration pipeline, whose entry mounts the
 *  pipeline's BOX rather than a tool row. Deliberately absent from `isSubagentInvocation`:
 *  one title with two owners makes a classification unpredictable.
 *
 *  LOCAL rather than imported, and MEASURED: `messages-blocks.ts` owns the twin and imports
 *  from here, and its graph reaches `router.ts`, which registers a `window` listener at
 *  module load while both of this module's suites run in the node project. The shape that
 *  removes all three copies is the predicate living in `tool-schema.ts`. */
function isPipelineDriver(call: EntryToolCall): boolean {
  return call.title === "Orchestrate Sub-agent";
}

/** What one `tool_call` entry mounts as, in pixels. `results` is the turn's own index, so
 *  the run test costs one lookup rather than a walk over the body per call. */
function callEstimate(
  e: Entry,
  call: EntryToolCall,
  est: EntryEstimates,
  results: RunResults,
): number {
  // The run test stays FIRST: a launch carries no subtask id, so the arms are disjoint, but
  // ordering them makes that independent of the titles.
  if (effectiveRunID(e, results) !== "") {
    return est.runCard;
  }
  // A PIPELINE IS WORTH ONE CARD however many stages it has, priced at its DRIVER's entry: a
  // collapsed `.subagent-body` is `content-visibility: hidden` at inline height 0, so a stage
  // inside contributes nothing. A PROMOTED single stage swaps the two prices, which costs one
  // card only when a window edge falls between them.
  if (isPipelineDriver(call)) {
    return est.subagentCard;
  }
  if ((call.agent_subtask_id ?? "") !== "") {
    return pipelineOf(call.id) === "" ? est.subagentCard : 0;
  }
  return est.toolCard;
}

/** What one entry mounts as, in pixels, for an entry this view draws. A `text` entry is
 *  priced as its RUN's row, so the caller charges it once per run. */
function estimateOf(e: Entry, est: EntryEstimates, results: RunResults): number {
  switch (e.kind) {
    case "text":
      // An empty text entry (a released steer carry) mounts an `is-empty` row, which is
      // `display: none`.
      return (payloadOf(e, "text")?.text ?? "") === "" ? est.emptyText : est.text;
    case "thinking":
      return est.thinking;
    case "tool_call": {
      const call = payloadOf(e, "tool_call");
      return call === undefined ? est.toolCard : callEstimate(e, call, est, results);
    }
    case "plan":
      // The turn's plan CARD: only its first `plan` entry renders, and `entryRenders` is
      // what keeps the later ones out of this walk.
      return est.planCard;
    case "steer":
      return est.steerNote;
    case "steer_ack":
      // Agent content at its own position, so a prose row rather than an event badge.
      return est.text;
    default:
      // The five event kinds — a compaction, a failed one, a safety block, a model
      // switch, a mode switch.
      return est.row;
  }
}

/** The gaps a run of `n` boxes carries. Zero-height ones are excluded by the caller:
 *  a blank row is `display: none` (css/13-messages.css), and `gap` counts an item
 *  rather than a height. */
function gapsBetween(n: number): number {
  return Math.max(0, n - 1) * ROW_GAP_PX;
}

/** Record what one entry's element measured, at the moment it is dropped. */
export function recordEntryHeight(turnID: string, seq: number, px: number): void {
  let per = entryHeights.get(turnID);
  if (per === undefined) {
    per = new Map<number, number>();
    entryHeights.set(turnID, per);
  }
  per.set(seq, px);
}

/** Record what one prose RUN's row measured, at the moment it is dropped, against the run's
 *  first `seq`. `range` is what the row HELD: its height answers for those entries and no
 *  others. */
export function recordRowHeight(turnID: string, range: EntryRange, px: number): void {
  let per = rowHeights.get(turnID);
  if (per === undefined) {
    per = new Map<number, { range: EntryRange; px: number }>();
    rowHeights.set(turnID, per);
  }
  per.set(range.from, { range, px });
}

/** The pixel height of the entries one spacer stands in for: everything on `side` of
 *  `range` — the turn's MOUNTED range — plus the gaps the rows it replaces contributed.
 *  Measured where measured, the per-outcome estimate where not.
 *
 *  `lane` is the VIEW's root and carries NO default: the price and the budget have to answer
 *  `entryRenders` identically, and a silent `""` here is that divergence. A prose run is one
 *  box, its first entry carrying the row, which is why the walk tracks the previous kind. */
export function spacerHeight(
  t: Turn,
  range: EntryRange,
  side: "head" | "tail",
  lane: string,
): number {
  const span = turnSpan(t);
  const stood: EntryRange =
    side === "head"
      ? { from: 0, to: Math.min(Math.max(range.from, 0), span) }
      : { from: Math.min(Math.max(range.to, 0), span), to: span };
  if (stood.from >= stood.to) {
    return 0;
  }
  // Both resolved ONCE per call, not per entry: neither answer can change inside one
  // spacer's arithmetic, and a per-entry read would put an attribute lookup and a walk
  // over the turn's body in a loop.
  const est = ENTRY_ESTIMATE_PX[tierNow()];
  const results = runResults(t);
  const firstPlan = firstPlanSeq(t, lane);
  const per = entryHeights.get(t.id);
  const rows = rowHeights.get(t.id);
  let px = 0;
  let boxes = 0;
  let inRun = false;
  for (let seq = stood.from; seq < stood.to; seq++) {
    const e = entryAt(t, seq);
    if (e === undefined || !entryRenders(e, lane, firstPlan)) {
      continue;
    }
    if (e.kind === "text") {
      if (inRun) {
        continue;
      }
      inRun = true;
      const run = sliceTurn(t, { from: seq, to: seq + 1 }, lane, firstPlan);
      const measured = rows?.get(seq);
      const own =
        measured?.range.from === run.from && measured.range.to === run.to
          ? measured.px
          : estimateOf(e, est, results);
      px += own;
      if (own > 0) {
        boxes++;
      }
      continue;
    }
    inRun = false;
    const own = per?.get(seq) ?? estimateOf(e, est, results);
    px += own;
    if (own > 0) {
      boxes++;
    }
  }
  // TWO gap terms, and the boundary one is what the parent no longer supplies: the spacer
  // sits under `.turn`, which declares no `gap`, where the boxes it replaces sat inside
  // `.turn-body`'s gapped column — so the gap between the last replaced box and the first
  // mounted row has to come from here. A spacer standing for no box replaces no gap either.
  return boxes === 0 ? px : px + gapsBetween(boxes) + ROW_GAP_PX;
}

/** Drop a turn's cache (view dispose, chat delete). */
export function forgetHeights(turnIDs: Iterable<string>): void {
  for (const id of turnIDs) {
    entryHeights.delete(id);
    rowHeights.delete(id);
  }
}
