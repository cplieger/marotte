// What a collapsed turn's face shows (its last message and the fallback lines that moved its model), and whether
// folding the turn hides anything. Pure and DOM-free.

import {
  entryAt,
  entryRenders,
  proseRunAt,
  turnFolds,
  turnSpan,
  type EntryRange,
  type TurnFolds,
} from "./block-window.js";
import { bodyMemo } from "./body-memo.js";
import { namedSay } from "./entry-ids.js";
import { hookStatusShown } from "./hook-fold.js";
import { appliedFallback, payloadOf, type Turn } from "./turns.js";
import type { Entry } from "./types.js";
import type { EventEntry } from "./messages-events.js";

/** One part of a collapsed turn's face, in body order: a last-message bubble's prose, or a fallback line. */
type FacePart =
  | { readonly kind: "prose"; readonly text: string }
  | { readonly kind: "row"; readonly entry: EventEntry };

/**
 * The face a collapsed turn draws: every line the body draws for a fallback that moved the turn's model
 * (`fallbackRow`), wherever it sits, and the last message's bubbles (`lastMessageItems`), each trimmed, in body order.
 */
export function turnFace(t: Turn): readonly FacePart[] {
  // `entryRenders` reads `hookStatusShown`.
  return faces(t.body, String(hookStatusShown()), () => {
    const folds = turnFolds(t, "");
    const parts: { readonly seq: number; readonly part: FacePart }[] = [];
    for (const e of t.body) {
      if (fallbackRow(e, folds)) {
        parts.push({ seq: e.seq, part: { kind: "row", entry: e as EventEntry } });
      }
    }
    for (const r of lastMessageItems(t)) {
      const text = itemText(t, r).trim();
      if (text !== "") {
        parts.push({ seq: r.from, part: { kind: "prose", text } });
      }
    }
    return parts.sort((a, b) => a.seq - b.seq).map((p) => p.part);
  });
}

const faces = bodyMemo<readonly FacePart[]>();

/**
 * Whether the body draws `e` as the line of a fallback that moved the turn's model, which the face keeps so the fold
 * never hides a model the reader did not pick. A decline or a same-model retry moved nothing, so it is not one.
 */
function fallbackRow(e: Entry, folds: TurnFolds): boolean {
  if ((e.lane ?? "") !== "" || !entryRenders(e, "", folds) || !appliedFallback(e)) {
    return false;
  }
  const p = payloadOf(e, "model_fallback");
  return p !== undefined && p.to !== p.from;
}

/**
 * The turn's LAST MESSAGE as `seq` ranges, one per bubble the body draws for it, in order: prose runs, and one entry
 * each for a `steer_ack`. It ends at the turn's last non-empty top-level prose and reaches back over all the prose
 * before it, other says included. It reaches over a drawn non-prose entry (a tool call, a reasoning trace, a steer
 * note) only when the nearest earlier top-level text is the same named say. An entry that draws nothing
 * (`tool_result`, `steer_delivered`) never ends it, nor does an applied `model_fallback`: KAS continues the refused
 * text on the fallback model. Delegate prose is never in it.
 */
function lastMessageItems(t: Turn): EntryRange[] {
  const folds = turnFolds(t, "");
  const items: EntryRange[] = [];
  // The earliest named say collected so far: the one a drawn entry must sit inside to be reached over.
  let say: string | null = null;
  let seq = lastProseSeq(t);
  while (seq > 0) {
    const e = entryAt(t, seq);
    if (e === undefined || !entryRenders(e, "", folds) || appliedFallback(e)) {
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

/** Whether folding this turn would HIDE anything: an entry the face leaves out. A turn with only the last message and
 *  its fallback lines folds to an identical face, so no fold is offered. A revert's boundary row is not counted, so it
 *  stays on screen. */
export function turnFoldHides(t: Turn): boolean {
  return foldHides(t.body, String(hookStatusShown()), () => deriveFoldHides(t));
}

const foldHides = bodyMemo<boolean>();

function deriveFoldHides(t: Turn): boolean {
  const folds = turnFolds(t, "");
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
      case "model_routed":
      case "mode_switched":
      case "profile_switched":
      case "context_reset":
        return true;
      // A re-pin its fallback explains draws nothing, and the face keeps the fallback's line.
      case "model_switched":
      case "model_fallback":
        if (entryRenders(e, "", folds) && !fallbackRow(e, folds)) {
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
    const drawn = e !== undefined && entryRenders(e, "", folds) && !fallbackRow(e, folds);
    if (drawn && !items.some((r) => seq >= r.from && seq < r.to)) {
      return true;
    }
  }
  return false;
}
