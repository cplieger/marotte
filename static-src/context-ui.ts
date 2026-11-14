// Context bar update, from chat.ts's active-session effect: only the active session (and switches) refresh it.

import type { Entry, Session, MeteringItem } from "./types.js";
import { updateContextBar } from "./status.js";
import { effortPillLabel } from "./effort.js";
import { getCachedModels } from "./picker.js";
import { getLastEffortFor } from "./session-context.js";
import { compactionPoint, compactionPolicy } from "./context-ring.js";
import { entryRenders, foldsByLane, NO_FOLDS } from "./block-window.js";
import { bodyMemo } from "./body-memo.js";
import { hookStatusShown } from "./hook-fold.js";
import { bodyOf } from "./turns.js";

interface BodyCounts {
  readonly entries: number;
  readonly tools: number;
}

const counts = bodyMemo<BodyCounts>();

/** `body`'s rows the reader can reach, per entry's own lane (`entryRenders`, delegates included). */
function bodyCounts(body: readonly Entry[]): BodyCounts {
  // `entryRenders` reads `hookStatusShown`.
  return counts(body, String(hookStatusShown()), () => {
    const folds = foldsByLane(body);
    let entries = 0;
    let tools = 0;
    for (const e of body) {
      const lane = e.lane ?? "";
      if (entryRenders(e, lane, folds.get(lane) ?? NO_FOLDS)) {
        entries++;
        tools += e.kind === "tool_call" ? 1 : 0;
      }
    }
    return { entries, tools };
  });
}

const throughMark = bodyMemo<number>();

/** `body`'s reachable rows up to and including the entry `watermark`, or -1 when no entry is it. */
function countThrough(body: readonly Entry[], watermark: string): number {
  return throughMark(body, `${String(hookStatusShown())}|${watermark}`, () => {
    const folds = foldsByLane(body);
    let seen = 0;
    for (const e of body) {
      const lane = e.lane ?? "";
      if (entryRenders(e, lane, folds.get(lane) ?? NO_FOLDS)) {
        seen++;
      }
      if (e.id === watermark) {
        return seen;
      }
    }
    return -1;
  });
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
    if (t.entries[0]?.id === watermark) {
      return seen;
    }
    const body = bodyOf(t);
    const through = countThrough(body, watermark);
    if (through >= 0) {
      return seen + through;
    }
    seen += bodyCounts(body).entries;
  }
  return undefined;
}

export function refreshContextUI(s: Session): void {
  const u = s.usage;
  const metering: MeteringItem[] = u.metering_items ?? [];
  let entryCount = 0;
  let toolCount = 0;
  for (const turnID of s.turn_order) {
    const t = s.turns.get(turnID);
    if (t !== undefined) {
      const c = bodyCounts(bodyOf(t));
      entryCount += c.entries;
      toolCount += c.tools;
    }
  }
  const summarized = summarizedCount(s);
  // A tracked read, so the caller's effect repaints the ring on a setting change with no new usage frame.
  const compaction = compactionPoint(compactionPolicy.value);
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
