// What a range of a turn's entries is worth in PIXELS, so the document's height does not
// depend on the window. Measured at the DROP: everything above the reader mounted once.

import {
  effectiveRunID,
  entryAt,
  entryRenders,
  hostsRunCard,
  turnFolds,
  runResults,
  sliceTurn,
  turnSpan,
  type EntryRange,
  type RunResults,
} from "./block-window.js";
import { toolResultID } from "./entry-ids.js";
import { callRefused, hookQuiet, hookStatusShown, isToolTriggerHook } from "./hook-fold.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";
import type { Entry, EntryToolCall, EntryToolResult } from "./types.js";
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
    runCard: 75, // 27-run-card.css `.run-card` — `auto var(--run-card-content)` + 2px
    toolCard: 38, // 14-tools.css `.tool-call` — `auto var(--btn-h)` + 2px
    toolGroup: 38, // NO reserve: a collapsed `.tool-group`, its `--row-tap` header + 2px
    subagentCard: 71, // `.subagent-block` — a delegate's card
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
    toolGroup: 46,
    subagentCard: 99,
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
  readonly toolGroup: number;
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
 * turn id → a tool run's first member `seq` → the range its group measured and its height. Range-keyed for the
 * reason `rowHeights` is.
 */
const groupHeights = new Map<string, Map<number, { range: EntryRange; px: number }>>();

/** turn id → a stamp that moves with every height recorded or forgotten for it. */
const heightStamps = new Map<string, number>();
let heightStamp = 0;

/** Moves whenever a height is recorded or forgotten for `turnID`, so a spacer priced from them is stale. */
export function heightsRevision(turnID: string): number {
  return heightStamps.get(turnID) ?? 0;
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
  if (runCardHost(e, results)) {
    return est.runCard;
  }
  return (call.agent_subtask_id ?? "") === "" ? est.toolCard : est.subagentCard;
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
  heightStamps.set(turnID, ++heightStamp);
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
  heightStamps.set(turnID, ++heightStamp);
}

/** Record what one tool run's group measured when dropped, against the run's member range. */
export function recordGroupHeight(turnID: string, range: EntryRange, px: number): void {
  let per = groupHeights.get(turnID);
  if (per === undefined) {
    per = new Map<number, { range: EntryRange; px: number }>();
    groupHeights.set(turnID, per);
  }
  per.set(range.from, { range, px });
  heightStamps.set(turnID, ++heightStamp);
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
    boxes += box.gapless === true ? 0 : 1;
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
    y += box.px + (box.gapless === true ? 0 : ROW_GAP_PX);
    if (px < y) {
      return box.seq;
    }
  }
  return last;
}

/** Whether `e` mounts its workflow run's card: an unstamped call naming the run, and its owner (`hostsRunCard`). */
function runCardHost(e: Entry, results: RunResults): boolean {
  const runID = effectiveRunID(e, results);
  return runID !== "" && hostsRunCard(runID, e.id);
}

/** How a drawn entry takes part in a tool group: a member, nothing, a box that ends the run, or another break. */
type GroupRole = "member" | "none" | "box" | "break";

/** How an entry the caller's `entryRenders` drew takes part in a tool group: a plain call is a member, an internal
 *  one or a delegate's `repeats` mounts nothing, a delegate's invocation or the run's card opens a box, and any other
 *  kind breaks the run. It matches `messages-blocks.ts` `indexGroups` for the transcript's chat lane, the only lane
 *  priced. */
function groupRole(e: Entry, results: RunResults, repeats: ReadonlySet<number>): GroupRole {
  if (e.kind !== "tool_call") {
    return "break";
  }
  const call = payloadOf(e, "tool_call");
  if (call === undefined) {
    return "break";
  }
  if (isInternalToolTitle(call.title) || repeats.has(e.seq)) {
    return "none";
  }
  return opensBox(e, call, results) ? "box" : "member";
}

/** The `seq`s of the drawn invocations of a delegate whose card an earlier one in `lane` opened. */
function repeatedInvocations(
  t: Turn,
  lane: string,
  folds: ReturnType<typeof turnFolds>,
): Set<number> {
  const opened = new Set<string>();
  const out = new Set<number>();
  for (const e of t.body) {
    const call = payloadOf(e, "tool_call");
    const subtask = call?.agent_subtask_id ?? "";
    if (
      call === undefined ||
      subtask === "" ||
      !isSubagentInvocation(call) ||
      !entryRenders(e, lane, folds)
    ) {
      continue;
    }
    if (opened.has(subtask)) {
      out.add(e.seq);
    } else {
      opened.add(subtask);
    }
  }
  return out;
}

/** Whether a drawn call opens a box of its own: its workflow run's card, or a delegate's. */
function opensBox(e: Entry, call: EntryToolCall, results: RunResults): boolean {
  return (call.agent_subtask_id ?? "") === ""
    ? runCardHost(e, results)
    : isSubagentInvocation(call);
}

/** `callRefused` with the result merged over the call as `store.ts` `settledToolCall` merges them. */
function refusedCall(call: EntryToolCall, result: EntryToolResult | undefined): boolean {
  return callRefused({
    status: result?.status ?? call.status,
    denial: result?.denial ?? call.denial,
    declined: result?.declined === true || call.declined === true,
    interaction: result?.interaction,
  });
}

/** The tool run holding member `seq`, its members in order over the turn's `span`, and the box owning it. A box owns
 *  the PostToolUse hooks right after it, and the PreToolUse hooks right before it in a run of hooks only, each set a
 *  run of its own (`messages-blocks.ts` `indexGroups`). */
function toolRun(
  seq: number,
  span: number,
  role: (at: number) => GroupRole,
  hook: (at: number) => EntryToolCall | undefined,
): { members: number[]; box: number | undefined } {
  const trigger = (at: number): string | undefined => hook(at)?.hook_trigger;
  const members = [seq];
  let before: number | undefined;
  for (let at = seq - 1; at >= 0; at--) {
    const r = role(at);
    if (r === "member") {
      members.unshift(at);
    } else if (r !== "none") {
      before = r === "box" ? at : undefined;
      break;
    }
  }
  let after: number | undefined;
  for (let at = seq + 1; at < span; at++) {
    const r = role(at);
    if (r === "member") {
      members.push(at);
    } else if (r !== "none") {
      after = r === "box" ? at : undefined;
      break;
    }
  }
  let from = 0;
  while (before !== undefined && trigger(members[from] ?? -1) === "PostToolUse") {
    from++;
  }
  let to = members.length;
  const hooksOnly = members.every((m) => hook(m) !== undefined);
  while (
    after !== undefined &&
    hooksOnly &&
    to > from &&
    trigger(members[to - 1] ?? -1) === "PreToolUse"
  ) {
    to--;
  }
  const i = members.indexOf(seq);
  if (i < from) {
    return { members: members.slice(0, from), box: before };
  }
  if (i >= to) {
    return { members: members.slice(to), box: after };
  }
  return { members: members.slice(from, to), box: undefined };
}

/** A prose run is one box, keyed by its first `seq`; a tool run is one box, its group shell, keyed by its first
 *  member. */
function* spacerBoxes(
  t: Turn,
  range: EntryRange,
  side: "head" | "tail",
  lane: string,
): Generator<{ seq: number; px: number; gapless?: true }> {
  const span = turnSpan(t);
  const stood: EntryRange =
    side === "head"
      ? { from: 0, to: Math.min(Math.max(range.from, 0), span) }
      : { from: Math.min(Math.max(range.to, 0), span), to: span };
  // Resolved ONCE per call: neither can change within one spacer's arithmetic.
  const est = ENTRY_ESTIMATE_PX[tierNow()];
  const results = runResults(t);
  const folds = turnFolds(t, lane);
  const per = entryHeights.get(t.id);
  const rows = rowHeights.get(t.id);
  const groups = groupHeights.get(t.id);
  const repeats = repeatedInvocations(t, lane, folds);
  const roleAt = (at: number): GroupRole => {
    const e = entryAt(t, at);
    return e === undefined || !entryRenders(e, lane, folds)
      ? "none"
      : groupRole(e, results, repeats);
  };
  // A group is collapsed by position once anything renders after its run (`tool-group.ts`).
  let lastRendered = -1;
  for (let seq = span - 1; seq >= 0 && lastRendered < 0; seq--) {
    if (roleAt(seq) !== "none") {
      lastRendered = seq;
    }
  }
  const hookAt = (at: number): EntryToolCall | undefined => {
    const e = entryAt(t, at);
    const call = e === undefined ? undefined : payloadOf(e, "tool_call");
    return call?.kind === "hook" ? call : undefined;
  };
  // A box counts as lane work as a tool box does.
  let laneWork = false;
  for (let seq = 0; seq < span && !laneWork; seq++) {
    const r = roleAt(seq);
    laneWork = r === "box" || (r === "member" && hookAt(seq) === undefined);
  }
  let settled: Map<string, EntryToolResult> | undefined;
  const refusedAt = (m: number): boolean => {
    const e = entryAt(t, m);
    const call = e === undefined ? undefined : payloadOf(e, "tool_call");
    if (e === undefined || call === undefined) {
      return false;
    }
    settled ??= new Map(
      t.body.flatMap((r) => {
        const result = payloadOf(r, "tool_result");
        return result === undefined ? [] : [[r.id, result] as const];
      }),
    );
    return refusedCall(call, settled.get(toolResultID(e.id)));
  };
  // An unmeasured hook member is priced by the fate `hook-fold.ts` `hookFate` gives it. A row when it failed or
  // printed; when its run's tool box was refused and it is a tool-trigger hook, or an untriggered one while status
  // shows; when its run belongs to a refused box (`toolRun`); or, while status shows, when its run has no tool box
  // and belongs to no box and it is a tool-trigger hook or its lane holds no tool box to fold into. Otherwise it
  // folds into a badge or hides, and costs nothing.
  const priceMember = (
    m: number,
    runWork: boolean,
    refused: boolean,
    box: number | undefined,
  ): number => {
    const known = per?.get(m);
    const call = hookAt(m);
    if (known !== undefined || call === undefined) {
      return known ?? est.toolCard;
    }
    const result = folds.hookResults.get(entryAt(t, m)?.id ?? "");
    const quiet = hookQuiet({
      status: result?.status ?? call.status,
      output: result?.output ?? call.output,
    });
    const shown = hookStatusShown();
    const row =
      !quiet ||
      (refused && (isToolTriggerHook(call) || (shown && call.hook_trigger === undefined))) ||
      (box === undefined
        ? shown && !runWork && (isToolTriggerHook(call) || !laneWork)
        : refusedAt(box));
    return row ? est.toolCard : 0;
  };
  const mounted = range.to > range.from;
  // The exclusive end of the run being priced: its later members share its box.
  let runTo = -1;
  for (let seq = stood.from; seq < stood.to; seq++) {
    const e = entryAt(t, seq);
    if (e === undefined || !entryRenders(e, lane, folds)) {
      continue;
    }
    let own: number;
    const role = roleAt(seq);
    if (role === "none") {
      continue;
    }
    if (role === "member") {
      if (seq < runTo) {
        continue;
      }
      const { members, box } = toolRun(seq, span, roleAt, hookAt);
      const first = members[0] ?? seq;
      const last = members[members.length - 1] ?? seq;
      runTo = last + 1;
      const inside = members.filter((m) => m >= stood.from && m < stood.to);
      const work = members.filter((m) => hookAt(m) === undefined);
      const refused = work.some(refusedAt);
      const px = new Map(members.map((m) => [m, priceMember(m, work.length > 0, refused, box)]));
      const member = (m: number): number => px.get(m) ?? 0;
      // Only non-hook cards form a group; hook rows alone form one only in a lane with none (`tool-group.ts`
      // `paintGroup`).
      const boxes =
        work.length > 0 ? work.length : laneWork ? 0 : members.filter((m) => member(m) > 0).length;
      const grouped = boxes > 1;
      const collapsed = grouped && lastRendered > last;
      // A run crossing into the mounted rows is their group's: only an expanded one grows by the members here.
      const joined = (first < stood.from || last >= stood.to) && mounted;
      if (joined) {
        // Inside the mounted group each member is its own box with none of the body's gaps; under a fold, nothing.
        for (const m of collapsed ? [] : inside) {
          yield { seq: m, px: member(m), gapless: true };
        }
        continue;
      }
      if (first < stood.from) {
        // An empty window splits the run across both spacers; the one holding its first member prices it.
        continue;
      }
      const measured = groups?.get(first);
      if (measured?.range.from === first && measured.range.to === last + 1) {
        own = measured.px;
      } else if (collapsed) {
        own = est.toolGroup;
      } else {
        own = members.reduce((sum, m) => sum + member(m), grouped ? est.toolGroup : 0);
      }
    } else if (e.kind === "text") {
      if (seq < runTo) {
        continue;
      }
      const run = sliceTurn(t, { from: seq, to: seq + 1 }, lane, folds);
      runTo = run.to;
      const measured = rows?.get(seq);
      own =
        measured?.range.from === run.from && measured.range.to === run.to
          ? measured.px
          : estimateOf(e, est, results);
    } else {
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
    groupHeights.delete(id);
    heightStamps.delete(id);
  }
}
