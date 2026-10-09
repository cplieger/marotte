// What a range of a turn's entries is worth in PIXELS, so the document's height does not
// depend on the window. Measured at the DROP: everything above the reader mounted once.

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

/**
 * What one entry is worth before it is measured, keyed on what it MOUNTS AS (one kind
 * mounts four shapes); a prose run is one row priced at its first entry. Each value is the
 * rendered BORDER box, so estimates and measurements sum interchangeably. Each shadows a
 * named rule; the two with none are measured heights at both tiers.
 */
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

/**
 * The flex `gap` (`--sp-3`) `.turn-body` puts between entries (css/29-turns.css),
 * tier-invariant. K replaced children carry K−1 gaps; the tail spacer is a sibling under the
 * gapless `.turn`, so it carries the boundary gap too (`spacerHeight`).
 */
export const ROW_GAP_PX = 12;

/**
 * The document's tier, READ off the attribute (`pointer-tier.ts` reaches `localStorage`;
 * this module runs with no DOM). An unset attribute mirrors `01-tokens.css`'s no-JS
 * fallback under `@media (width <= 48rem)`.
 */
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

/**
 * turn id → a prose run's first `seq` → the range that row measured and its height.
 * Range-keyed: a row dropped under a PARTIAL window measured that slice only.
 */
const rowHeights = new Map<string, Map<number, { range: EntryRange; px: number }>>();

/**
 * The tool call that OPENS a subagent pipeline, mounting the pipeline's BOX. Kept out of
 * `isSubagentInvocation`. Local: `messages-blocks.ts` owns the twin and its graph reaches
 * `router.ts`, which needs `window`; the fix is the predicate living in `tool-schema.ts`.
 */
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
  // A pipeline is worth ONE card at its DRIVER's entry: stages inside a collapsed body add
  // nothing. A promoted single stage swaps the two prices.
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

/**
 * The gaps a run of `n` boxes carries. The caller excludes zero-height ones: a blank row
 * is `display: none`, and `gap` counts items.
 */
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

/**
 * Record what one prose RUN's row measured when dropped, against the run's first `seq`;
 * `range` is what the row held.
 */
export function recordRowHeight(turnID: string, range: EntryRange, px: number): void {
  let per = rowHeights.get(turnID);
  if (per === undefined) {
    per = new Map<number, { range: EntryRange; px: number }>();
    rowHeights.set(turnID, per);
  }
  per.set(range.from, { range, px });
}

/**
 * The pixel height of the entries one spacer stands for: everything on `side` of the
 * mounted `range`, plus the gaps they carried, measured or estimated. `lane` has NO
 * default: price and budget must answer `entryRenders` identically.
 */
export function spacerHeight(
  t: Turn,
  range: EntryRange,
  side: "head" | "tail",
  lane: string,
): number {
  let px = 0;
  let boxes = 0;
  for (const box of spacerBoxes(t, range, side, lane)) {
    px += box.px;
    boxes++;
  }
  // The boundary gap: the head spacer is the body's first item, so the body's own `gap` follows it; the tail
  // spacer sits under the gapless `.turn`, so its gap to the last mounted row comes from here. No boxes, no gap.
  if (boxes === 0) {
    return px;
  }
  return side === "head" ? px + gapsBetween(boxes) : px + gapsBetween(boxes) + ROW_GAP_PX;
}

/**
 * The ordinal a point `px` down one spacer stands for, priced as `spacerHeight` prices it, so a reader dropped
 * into the spacer anchors where the rows will land. Clamped to the spacer's own ordinals; undefined when it stands
 * for none.
 */
export function spacerOrdinalAt(
  t: Turn,
  range: EntryRange,
  side: "head" | "tail",
  lane: string,
  px: number,
): number | undefined {
  // The tail spacer's first box sits one boundary gap below its top.
  let y = side === "tail" ? ROW_GAP_PX : 0;
  let last: number | undefined;
  for (const box of spacerBoxes(t, range, side, lane)) {
    last = box.seq;
    y += box.px + ROW_GAP_PX;
    if (px < y) {
      return box.seq;
    }
  }
  return last;
}

/** A prose run is one box, keyed by its first `seq`. */
function* spacerBoxes(
  t: Turn,
  range: EntryRange,
  side: "head" | "tail",
  lane: string,
): Generator<{ seq: number; px: number }> {
  const span = turnSpan(t);
  const stood: EntryRange =
    side === "head"
      ? { from: 0, to: Math.min(Math.max(range.from, 0), span) }
      : { from: Math.min(Math.max(range.to, 0), span), to: span };
  // Resolved ONCE per call: neither can change within one spacer's arithmetic.
  const est = ENTRY_ESTIMATE_PX[tierNow()];
  const results = runResults(t);
  const firstPlan = firstPlanSeq(t, lane);
  const per = entryHeights.get(t.id);
  const rows = rowHeights.get(t.id);
  let inRun = false;
  for (let seq = stood.from; seq < stood.to; seq++) {
    const e = entryAt(t, seq);
    if (e === undefined || !entryRenders(e, lane, firstPlan)) {
      continue;
    }
    let own: number;
    if (e.kind === "text") {
      if (inRun) {
        continue;
      }
      inRun = true;
      const run = sliceTurn(t, { from: seq, to: seq + 1 }, lane, firstPlan);
      const measured = rows?.get(seq);
      own =
        measured?.range.from === run.from && measured.range.to === run.to
          ? measured.px
          : estimateOf(e, est, results);
    } else {
      inRun = false;
      own = per?.get(seq) ?? estimateOf(e, est, results);
    }
    if (own > 0) {
      yield { seq, px: own };
    }
  }
}

/** Drop a turn's cache (view dispose, chat delete). */
export function forgetHeights(turnIDs: Iterable<string>): void {
  for (const id of turnIDs) {
    entryHeights.delete(id);
    rowHeights.delete(id);
  }
}
