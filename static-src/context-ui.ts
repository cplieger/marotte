// ---------------------------------------------------------------------------
// Context bar update + the single "context is nearly full" signal.
//
// Runs from chat.ts's active-session effect: every change to the ACTIVE
// session (and every switch of which session is active) refreshes the context
// bar; background session churn never reaches it. When the active chat's
// context is (nearly) full, `contextFull` flips true — that signal
// is the ONE source of truth for the condition; prompt-input.ts reads it and
// renders it as a placeholder + tooltip (see status.ts note). It is ADVISORY: it
// no longer disables the composer, because kiro-cli compacts on the next turn,
// so refusing the send told the user about a problem they could do nothing about.
// There is no module-global previous-thinking flag: this is a continuous per-active-chat
// signal, so no transition state — per-chat or global — is needed here.
// ---------------------------------------------------------------------------

import type { Session, MeteringItem, TurnState } from "./types.js";
import { updateContextBar } from "./status.js";
import { getActiveId } from "./store.js";
import { contextFull } from "./prompt-input.js";
import { effortPillLabel } from "./effort.js";
import { getCachedModels } from "./picker.js";
import { getLastEffortFor } from "./session-context.js";
import { KAS_SUMMARIZATION_PCT, KAS_TRUNCATION_PCT } from "./context-ring.js";
import { entryRenders } from "./block-window.js";

// The live cutoff, derived from the model's real window: a flat percentage leaves
// tens of thousands of tokens of slack on a large one. KAS_TRUNCATION_PCT is its
// fallback, for a window this chat does not know yet.
const CONTEXT_RESERVE_TOKENS = 16_000;

// `contextFull` is declared in prompt-input.ts — the module that owns the send
// button/textarea and is the sole renderer of the state. This module COMPUTES
// its value for the active chat below. Importing it from prompt-input (rather
// than declaring it here) keeps the light send-state → prompt-input import chain
// free of this module + status.ts.

/** The `seq` of each lane's first `plan` entry, the one that renders: hoisted per turn for
 *  `entryRenders` and shared by both counts, so the reachable set has ONE definition.
 *  block-window.ts's `firstPlanSeq` answers it over a PROJECTED turn's `body` while this
 *  side holds a store `TurnState`, and the two are meant to agree. */
function firstPlanByLane(t: TurnState): Map<string, number> {
  const firstPlan = new Map<string, number>();
  for (const e of t.entries) {
    const lane = e.lane ?? "";
    if (e.kind === "plan" && !firstPlan.has(lane)) {
      firstPlan.set(lane, e.seq);
    }
  }
  return firstPlan;
}

/** How many of the REACHABLE entries the compaction watermark covers, or undefined when
 *  this window cannot say. Counted over `entryRenders`' set, the one `entryCount` reports,
 *  because status.ts renders the pair as ONE string; the boundary is still looked for over
 *  every entry, so a watermark on a row nothing draws is found rather than lost. The count
 *  is kept only when it IS found, which keeps an unmet condition distinguishable from a
 *  satisfied one: a watermark that is not resident — the ordinary state of a paged chat,
 *  and permanent after a rewind — discards the scan, and 0 keeps meaning the chat has no
 *  watermark rather than standing in for that. */
function summarizedCount(s: Session): number | undefined {
  const watermark = s.compaction_watermark ?? "";
  if (watermark === "") {
    return 0;
  }
  let seen = 0;
  for (const turnID of s.turn_order) {
    const t = s.turns.get(turnID);
    if (t === undefined) {
      continue;
    }
    const firstPlan = firstPlanByLane(t);
    for (const e of t.entries) {
      const lane = e.lane ?? "";
      if (entryRenders(e, lane, firstPlan.get(lane) ?? -1)) {
        seen++;
      }
      if (e.id === watermark) {
        return seen;
      }
    }
  }
  return undefined;
}

export function refreshContextUI(s: Session): void {
  const u = s.usage;
  const metering: MeteringItem[] = u.metering_items ?? [];
  // Rows the reader can REACH, per entry's own lane: `entryRenders` is the test, delegates included.
  let entryCount = 0;
  let toolCount = 0;
  for (const turnID of s.turn_order) {
    const t = s.turns.get(turnID);
    if (t === undefined) {
      continue;
    }
    const firstPlan = firstPlanByLane(t);
    for (const e of t.entries) {
      const lane = e.lane ?? "";
      if (!entryRenders(e, lane, firstPlan.get(lane) ?? -1)) {
        continue;
      }
      entryCount++;
      if (e.kind === "tool_call") {
        toolCount++;
      }
    }
  }
  const summarized = summarizedCount(s);
  // The ONE fallback site: the streaming usage channel carries no threshold.
  const summarizationPct = u.summarization_threshold_pct ?? KAS_SUMMARIZATION_PCT;
  updateContextBar({
    pct: u.context_pct,
    contextSize: u.context_size,
    summarizationPct,
    credits: u.credits,
    turnCount: s.turn_count,
    lastTurnMs: u.last_turn_ms,
    model: s.model,
    // The reasoning tier the chat runs at (effort.ts resolves it, under the
    // per-model capability gate). Resolved HERE rather than in status.ts so the
    // renderer keeps writing what it is handed: this module already runs on every
    // active-session change, so the pill repaints when an optimistic set_effort
    // write lands, when the session reports a new currentValue, and when a model
    // switch changes which default applies.
    effort: effortPillLabel(s, getCachedModels(), getLastEffortFor(s.model)),
    // The header's own field, so a pick made on another device and a pick applied at a
    // turn's close both reach the badge without a local queue.
    pendingModel: s.pending_model ?? "",
    metering,
    entryCount,
    toolCount,
    // Withheld rather than sent as a number when the window cannot say. The
    // renderer already prints nothing for an absent count, so "unknowable" and
    // "nothing was summarized" read the same to the user and differ here.
    ...(summarized === undefined ? {} : { summarizedCount: summarized }),
  });

  // Only the active chat drives the shared prompt bar's advisory.
  if (s.id !== getActiveId()) {
    return;
  }
  const cutoff =
    u.context_size > 0
      ? ((u.context_size - CONTEXT_RESERVE_TOKENS) / u.context_size) * 100
      : (u.truncation_threshold_pct ?? KAS_TRUNCATION_PCT);
  contextFull.value = u.context_pct >= cutoff;
}
