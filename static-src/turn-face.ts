// What a collapsed turn's face shows, and whether folding the turn hides anything. Pure and DOM-free.

import {
  entryAt,
  entryRenders,
  proseRunAt,
  turnFolds,
  turnSpan,
  type EntryRange,
} from "./block-window.js";
import { namedSay } from "./entry-ids.js";
import { isSubagentFallback, payloadOf, type Turn } from "./turns.js";
import type { Entry } from "./types.js";

/**
 * The turn's LAST MESSAGE, which a collapsed turn's face renders whole: one trimmed string per bubble the body draws
 * for it, in order. It ends at the turn's last non-empty top-level prose (`text`, or a `steer_ack`, its own bubble) and
 * reaches back over all the prose before it, other says too (a reply, then the one a notification woke the agent for).
 * It reaches over an entry the body draws that is not prose (a tool call, a reasoning trace, a steer note) only when
 * the nearest earlier top-level text is the same named say: one message written around it. An entry that draws
 * nothing (`tool_result`, `steer_delivered`) never ends it. Delegate prose is never in it.
 */
export function turnLastMessage(t: Turn): string[] {
  return lastMessageItems(t)
    .map((r) => itemText(t, r).trim())
    .filter((s) => s !== "");
}

/** The last message's bubbles as `seq` ranges, in order: prose runs, and one entry each for a `steer_ack`. */
function lastMessageItems(t: Turn): EntryRange[] {
  const folds = turnFolds(t, "");
  const items: EntryRange[] = [];
  // The earliest named say collected so far: the one a drawn entry must sit inside to be reached over.
  let say: string | null = null;
  let seq = lastProseSeq(t);
  while (seq > 0) {
    const e = entryAt(t, seq);
    if (e === undefined || !entryRenders(e, "", folds)) {
      seq--;
      continue;
    }
    const item =
      e.kind === "steer_ack" ? { from: seq, to: seq + 1 } : proseRunAt(t, seq, "", folds);
    if (item !== null) {
      items.unshift(item);
      say = runSay(t, item) ?? say;
      seq = item.from - 1;
      continue;
    }
    const prev = textBefore(t, seq);
    if (say === null || prev === undefined || namedSay(prev) !== say) {
      break;
    }
    seq--;
  }
  return items;
}

/** The `seq` of the turn's last non-empty top-level prose entry, or -1. */
function lastProseSeq(t: Turn): number {
  for (let seq = turnSpan(t) - 1; seq > 0; seq--) {
    const e = entryAt(t, seq);
    if (e !== undefined && (e.lane ?? "") === "" && proseOf(e).trim() !== "") {
      return seq;
    }
  }
  return -1;
}

/** A top-level entry's prose: a `text`'s or a `steer_ack`'s words, else "". */
function proseOf(e: Entry): string {
  return payloadOf(e, "text")?.text ?? payloadOf(e, "steer_ack")?.text ?? "";
}

/** The first named say among `item`'s text members, or null. */
function runSay(t: Turn, item: EntryRange): string | null {
  for (let seq = item.from; seq < item.to; seq++) {
    const e = entryAt(t, seq);
    const say = e?.kind === "text" && (e.lane ?? "") === "" ? namedSay(e) : null;
    if (say !== null) {
      return say;
    }
  }
  return null;
}

/** The nearest top-level `text` entry before `seq`. */
function textBefore(t: Turn, seq: number): Entry | undefined {
  for (let i = seq - 1; i > 0; i--) {
    const e = entryAt(t, i);
    if (e?.kind === "text" && (e.lane ?? "") === "") {
      return e;
    }
  }
  return undefined;
}

function itemText(t: Turn, item: EntryRange): string {
  let out = "";
  for (let seq = item.from; seq < item.to; seq++) {
    const e = entryAt(t, seq);
    out += e === undefined || (e.lane ?? "") !== "" ? "" : proseOf(e);
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
  return faceLeavesOut(t);
}

/**
 * Whether the face leaves out prose before the last message, or an entry the body draws inside its span (a steer
 * note one message was written around).
 */
function faceLeavesOut(t: Turn): boolean {
  const items = lastMessageItems(t);
  const first = items[0]?.from ?? turnSpan(t);
  for (let seq = 1; seq < first; seq++) {
    const e = entryAt(t, seq);
    if (e !== undefined && (e.lane ?? "") === "" && proseOf(e).trim() !== "") {
      return true;
    }
  }
  const folds = turnFolds(t, "");
  const end = items.at(-1)?.to ?? first;
  for (let seq = first; seq < end; seq++) {
    const e = entryAt(t, seq);
    const drawn = e !== undefined && entryRenders(e, "", folds);
    if (drawn && !items.some((r) => seq >= r.from && seq < r.to)) {
      return true;
    }
  }
  return false;
}
