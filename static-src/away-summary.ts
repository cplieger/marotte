// Away summary: after >15 minutes away with >5K tokens consumed, a "Welcome back" toast
// summarizes what happened, heuristically (reply and tool counts).

import { getActive } from "./store.js";
import { payloadOf } from "./turns.js";
import { showToast } from "./toast.js";

const AWAY_THRESHOLD_MS = 15 * 60 * 1000; // 15 minutes
const TOKEN_THRESHOLD = 5_000;

class AwaySummaryController {
  private lastHiddenAt = Date.now();
  private lastContextPct = 0;
  private lastTurnCount = 0;
  private lastChatId = "";

  init(): void {
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        this.checkAway();
      } else {
        this.snapshotState();
      }
    });
    this.snapshotState();
  }

  private snapshotState(): void {
    this.lastHiddenAt = Date.now();
    const s = getActive();
    if (s !== undefined) {
      this.lastChatId = s.id;
      this.lastContextPct = s.usage.context_pct;
      this.lastTurnCount = s.turn_order.length;
    }
  }

  private checkAway(): void {
    const elapsed = Date.now() - this.lastHiddenAt;
    if (elapsed < AWAY_THRESHOLD_MS) {
      return;
    }

    const s = getActive();
    if (s === undefined) {
      return;
    }

    if (s.id !== this.lastChatId) {
      this.snapshotState();
      return;
    }

    // Detect compaction: the resident turn window shrank while away.
    if (s.turn_order.length < this.lastTurnCount) {
      this.snapshotState();
      return;
    }

    const contextGrowth = s.usage.context_pct - this.lastContextPct;
    const contextSize = s.usage.context_size;
    const tokensConsumed = contextSize > 0 ? (contextGrowth / 100) * contextSize : 0;
    if (tokensConsumed < TOKEN_THRESHOLD) {
      return;
    }

    const newTurns = s.turn_order.length - this.lastTurnCount;
    if (newTurns <= 0) {
      return;
    }

    let replies = 0;
    let toolCalls = 0;
    const changedPaths = new Set<string>();

    for (let i = this.lastTurnCount; i < s.turn_order.length; i++) {
      const turnID = s.turn_order[i];
      const t = turnID === undefined ? undefined : s.turns.get(turnID);
      if (t === undefined) {
        continue;
      }
      let spoke = false;
      for (const e of t.entries) {
        if (e.kind === "text") {
          // A REPLY is the owning lane's alone; tool calls and changed paths count over every lane,
          // since a delegate's work is the news.
          if ((e.lane ?? "") === "") {
            spoke = true;
          }
          continue;
        }
        const call = payloadOf(e, "tool_call");
        if (call === undefined) {
          continue;
        }
        toolCalls++;
        for (const d of call.diffs ?? []) {
          changedPaths.add(d.path);
        }
      }
      if (spoke) {
        replies++;
      }
    }

    const parts: string[] = [];
    if (replies > 0) {
      parts.push(`${String(replies)} response${replies > 1 ? "s" : ""}`);
    }
    if (toolCalls > 0) {
      parts.push(`${String(toolCalls)} tool call${toolCalls > 1 ? "s" : ""}`);
    }
    if (changedPaths.size > 0) {
      parts.push(`${String(changedPaths.size)} file${changedPaths.size > 1 ? "s" : ""} changed`);
    }

    if (parts.length === 0) {
      return;
    }

    const awayMins = Math.round(elapsed / 60_000);
    const msg = `Welcome back (${String(awayMins)}m away). ${parts.join(", ")}.`;
    showToast(msg, "info");

    this.snapshotState();
  }
}

const instance = new AwaySummaryController();

export function initAwaySummary(): void {
  instance.init();
}
