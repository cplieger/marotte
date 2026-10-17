// What a collapsed turn's face shows, and whether folding the turn hides anything. Pure and DOM-free.

import { isSubagentFallback, payloadOf, type Turn } from "./turns.js";

/** Whether folding this turn would HIDE anything. The face shows run cards, the final top-level
 *  prose and a failed turn's error row; a turn with nothing else folds to an identical face, so no
 *  fold is offered. A revert's boundary row is not counted, so it stays on screen. */
export function turnFoldHides(t: Turn): boolean {
  let texts = 0;
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
      case "text":
        if ((payloadOf(e, "text")?.text ?? "").trim() !== "") {
          texts++;
        }
        break;
      default:
        break;
    }
  }
  return texts > 1;
}

/** The turn's final answer: the last non-empty TOP-LEVEL text entry. A collapsed
 *  turn's face renders it in full — the prompt in the header, this in the footer.
 *  Delegate prose (a non-empty lane) is a delegate's report rather than the turn's
 *  answer, so it never qualifies. */
export function turnFaceProse(t: Turn): string {
  for (let i = t.body.length - 1; i >= 0; i--) {
    const e = t.body[i];
    if (e === undefined || (e.lane ?? "") !== "") {
      continue;
    }
    const text = (payloadOf(e, "text")?.text ?? "").trim();
    if (text !== "") {
      return text;
    }
  }
  return "";
}
