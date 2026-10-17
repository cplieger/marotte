// What a collapsed turn's face shows, and whether folding the turn hides anything. Pure and DOM-free.

import { entryAt, entryRenders, proseRunAt, turnFolds, turnSpan } from "./block-window.js";
import { isSubagentFallback, payloadOf, type Turn } from "./turns.js";

/**
 * The turn's LAST MESSAGE, which a collapsed turn's face renders whole: one trimmed string per message (say), in
 * order. It is the top-level prose that ends at the turn's last non-empty top-level `text` entry and reaches back
 * over the prose runs before it until an entry the body draws that is not prose (a tool call, a reasoning trace, a
 * steer note). An entry that draws nothing (a `tool_result`, a `steer_delivered`) does not end it, so a reply and the
 * reply a notification woke the agent for, back to back, are both in it. Delegate prose is a delegate's report,
 * never the turn's answer. Empty for a turn that wrote no top-level prose.
 */
export function turnLastMessage(t: Turn): string[] {
  const runs = lastMessageRuns(t);
  return runs.map((r) => runText(t, r.from, r.to).trim()).filter((s) => s !== "");
}

/** The last message's prose runs as `seq` ranges, in order. */
function lastMessageRuns(t: Turn): { from: number; to: number }[] {
  const folds = turnFolds(t, "");
  let seq = lastProseSeq(t);
  const runs: { from: number; to: number }[] = [];
  while (seq > 0) {
    const run = proseRunAt(t, seq, "", folds);
    if (run === null) {
      break;
    }
    runs.unshift(run);
    seq = run.from - 1;
    while (seq > 0) {
      const e = entryAt(t, seq);
      if (e !== undefined && entryRenders(e, "", folds)) {
        break;
      }
      seq--;
    }
  }
  return runs;
}

/** The `seq` of the turn's last non-empty top-level `text` entry, or -1. */
function lastProseSeq(t: Turn): number {
  for (let seq = turnSpan(t) - 1; seq > 0; seq--) {
    const e = entryAt(t, seq);
    if (e === undefined || (e.lane ?? "") !== "") {
      continue;
    }
    if ((payloadOf(e, "text")?.text ?? "").trim() !== "") {
      return seq;
    }
  }
  return -1;
}

function runText(t: Turn, from: number, to: number): string {
  let out = "";
  for (let seq = from; seq < to; seq++) {
    const e = entryAt(t, seq);
    out += e === undefined || (e.lane ?? "") !== "" ? "" : (payloadOf(e, "text")?.text ?? "");
  }
  return out;
}

/** Whether folding this turn would HIDE anything. The face shows run cards, the last message
 *  and a failed turn's error row; a turn with nothing else folds to an identical face, so no
 *  fold is offered. A revert's boundary row is not counted, so it stays on screen. */
export function turnFoldHides(t: Turn): boolean {
  for (const e of t.body) {
    if ((e.lane ?? "") !== "") {
      return true;
    }
    switch (e.kind) {
      case "tool_call":
      case "thinking":
      case "plan":
      case "compaction":
      case "compaction_failed":
      case "safety_blocked":
      case "model_switched":
      case "model_routed":
      case "mode_switched":
        return true;
      case "model_fallback":
        if (!isSubagentFallback(e)) {
          return true;
        }
        break;
      default:
        break;
    }
  }
  return proseBeforeLastMessage(t);
}

/** Whether a non-empty top-level `text` entry sits before the last message, where the face does not show it. */
function proseBeforeLastMessage(t: Turn): boolean {
  const first = lastMessageRuns(t)[0]?.from ?? turnSpan(t);
  for (let seq = 1; seq < first; seq++) {
    const e = entryAt(t, seq);
    if (
      e !== undefined &&
      (e.lane ?? "") === "" &&
      (payloadOf(e, "text")?.text ?? "").trim() !== ""
    ) {
      return true;
    }
  }
  return false;
}
