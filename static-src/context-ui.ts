// Context bar update, from chat.ts's active-session effect: only the active session (and switches) refresh it.

import type { Session, MeteringItem, TurnState } from "./types.js";
import { updateContextBar } from "./status.js";
import { effortPillLabel } from "./effort.js";
import { getCachedModels } from "./picker.js";
import { getLastEffortFor } from "./session-context.js";
import { compactionPoint, compactionPolicy } from "./context-ring.js";
import { entryRenders } from "./block-window.js";

/**
 * Each lane's first `plan` seq, the one that renders, shared by both counts. block-window.ts's `firstPlanSeq`
 * answers the same over a projected turn's `body`, and the two must agree.
 */
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

/**
 * Reachable entries the watermark covers, or undefined when this window cannot say. Counted over `entryRenders`' set
 * because status.ts renders it paired with `entryCount`; a non-resident watermark (paged chat, rewind) discards the
 * scan, so 0 keeps meaning "no watermark".
 */
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
  // Rows the reader can reach, per entry's own lane (`entryRenders`, delegates included).
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
  // A tracked read, so the caller's effect repaints the ring on a setting change with no new usage frame.
  const compaction = compactionPoint(compactionPolicy.value, u.summarization_threshold_pct);
  updateContextBar({
    pct: u.context_pct,
    contextSize: u.context_size,
    compaction,
    credits: u.credits,
    turnCount: s.turn_count,
    lastTurnMs: u.last_turn_ms,
    model: s.model,
    // Resolved here, not in status.ts, so the pill repaints on every active-session change (set_effort, currentValue,
    // model switch).
    effort: effortPillLabel(s, getCachedModels(), getLastEffortFor(s.model)),
    // The header's own field, so a pick from another device or applied at turn close reaches the badge.
    pendingModel: s.pending_model ?? "",
    metering,
    entryCount,
    toolCount,
    // Withheld rather than sent as a number when the window cannot say.
    ...(summarized === undefined ? {} : { summarizedCount: summarized }),
  });
}
