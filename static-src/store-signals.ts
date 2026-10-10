// Per-entity streaming signal maps — generic registry for reactive per-ID signals that bypass the
// global reconcile loop.

import type { ToolCall } from "./types.js";
import { SignalMap, type Signal } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";

// SignalMap (the dynamic per-id signal registry) is provided by @cplieger/reactive.

/** One per-entry streaming write: the open entry's accumulated text plus the growth this write
 *  carries, so a consumer can append `delta` instead of re-deriving the tail from an ever-longer
 *  `full`. INVARIANT: the pair is meaningful only under @cplieger/reactive's synchronous flush
 *  contract — a write re-runs every subscribed effect before the setter returns, so a consumer
 *  observes one value per delta with none skipped. */
interface EntrySignalValue {
  readonly full: string;
  readonly delta: string;
}

/** The STREAMING signal, one per OPEN entry, keyed `(turn, entryID)`: `openEntry` mints it,
 *  `applyDelta` writes it, `sealEntry` retires it. The entry's kind is on the entry, so text and
 *  thinking share one map — a subscriber addresses the entry it mounted and needs no second
 *  question about what is in it. */
export const entryTextSigs = new SignalMap<EntrySignalValue>();

/** The COARSE signal, one per `(turn, lane)`, a version bumped on every open, delta, seal and
 *  laned append in that lane. It is what a surface that is not the entry's own bubble subscribes
 *  to — the subagent tail, a delegate's detached page — in place of the whole chat's transcript
 *  version. */
export const laneSigs = new SignalMap<number>();

/** Per-(chat-id, tool-call-id) signal. The chat is part of the key because a tool call id is
 *  BACKEND-authored and the wire carries no uniqueness guarantee for it, while a `tool_progress`
 *  frame arrives for whatever chat sent it — a background chat's data lands unconditionally and
 *  only the repaint is gated. */
export const toolCallSigs = new SignalMap<ToolCall>();

/** Key for `toolCallSigs`. Through keyenc rather than a template literal because a chat id is
 *  opaque hex while a tool call id is arbitrary text, so a separator the id may contain must not
 *  be able to shift the boundary. */
export function toolCallSigKey(chatID: string, toolID: string): string {
  return join(chatID, toolID);
}

export function ensureToolCallSig(
  chatID: string,
  toolID: string,
  initial: ToolCall,
): Signal<ToolCall> {
  return toolCallSigs.ensure(toolCallSigKey(chatID, toolID), initial);
}

/** The current value of a tool call's signal, untracked, or undefined when no signal exists. For
 *  derivations that run inside someone ELSE's effect (the delegate footer sums its members on
 *  the invocation's ticks) — reading `.value` there would subscribe that effect to every member. */
export function peekToolCallSig(chatID: string, toolID: string): ToolCall | undefined {
  return toolCallSigs.get(toolCallSigKey(chatID, toolID))?.peek();
}

/** Key for the streaming signal. Both components are upstream text (an entry id is KAS's own
 *  record id), so the separator goes through keyenc. */
export function entryKey(turnID: string, entryID: string): string {
  return join(turnID, entryID);
}

/** Key for the coarse signal. The empty lane is the log's own agent, which is a VALUE rather
 *  than an absence, so it keys like any other. */
export function laneKey(turnID: string, lane: string): string {
  return join(turnID, lane);
}

/** Keys minted per turn across both maps, so a turn's disposal can clear its signals without
 *  enumerating them (SignalMap exposes no key walk). */
const sigKeysByTurn = new Map<string, { entries: Set<string>; lanes: Set<string> }>();

function keysFor(turnID: string): { entries: Set<string>; lanes: Set<string> } {
  let keys = sigKeysByTurn.get(turnID);
  if (keys === undefined) {
    keys = { entries: new Set(), lanes: new Set() };
    sigKeysByTurn.set(turnID, keys);
  }
  return keys;
}

/** Mint or fetch an open entry's streaming signal. `initial` is the text the entry arrived with,
 *  which is already on screen, so the pair carries no growth. */
export function ensureEntryTextSig(
  turnID: string,
  entryID: string,
  initial: string,
): Signal<EntrySignalValue> {
  const key = entryKey(turnID, entryID);
  keysFor(turnID).entries.add(key);
  return entryTextSigs.ensure(key, { full: initial, delta: "" });
}

/** The open entry's streaming signal, or undefined when nothing minted one. */
// deadset:ignore DS1004 -- test seam: observes the open entry streaming signals
export function entryTextSig(
  turnID: string,
  entryID: string,
): Signal<EntrySignalValue> | undefined {
  return entryTextSigs.get(entryKey(turnID, entryID));
}

/** Write one delta into an open entry's signal. Reports whether a cell EXISTS for the entry,
 *  which is a weaker fact than a subscriber: `openEntry` mints one per live stream and a mounted
 *  surface mints its own, so false means only that neither has happened yet. */
export function writeEntryText(
  turnID: string,
  entryID: string,
  full: string,
  delta: string,
): boolean {
  const sig = entryTextSigs.get(entryKey(turnID, entryID));
  if (sig === undefined) {
    return false;
  }
  sig.value = { full, delta };
  return true;
}

/** Retire one entry's streaming signal: the seal is the end of its growth. */
export function clearEntryTextSig(turnID: string, entryID: string): void {
  const key = entryKey(turnID, entryID);
  entryTextSigs.clear(key);
  sigKeysByTurn.get(turnID)?.entries.delete(key);
}

/** Subscribe to a lane's coarse signal. */
export function laneSig(turnID: string, lane: string): Signal<number> {
  const key = laneKey(turnID, lane);
  keysFor(turnID).lanes.add(key);
  return laneSigs.ensure(key, 0);
}

/** Bump a lane's coarse signal. Every open, delta, seal and laned append in the lane goes
 *  through here, so a lane subscriber needs no entry-id lookup. */
export function bumpLane(turnID: string, lane: string): void {
  const key = laneKey(turnID, lane);
  keysFor(turnID).lanes.add(key);
  const sig = laneSigs.ensure(key, 0);
  sig.value = sig.peek() + 1;
}

/** Drop every signal a turn minted, both maps. Without it a signal lives until the last chat
 *  closes, one entry per streamed entry, for the whole page's life. */
export function clearTurnSigs(turnID: string): void {
  const keys = sigKeysByTurn.get(turnID);
  if (keys === undefined) {
    return;
  }
  for (const k of keys.entries) {
    entryTextSigs.clear(k);
  }
  for (const k of keys.lanes) {
    laneSigs.clear(k);
  }
  sigKeysByTurn.delete(turnID);
}

/** Drop every streaming and lane signal between cases; production disposes per turn through
 *  `clearTurnSigs`. */
// deadset:ignore DS1004 -- test seam: resets the streaming and lane signals and the per-turn keys
export function clearAllEntrySigs(): void {
  entryTextSigs.clearAll();
  laneSigs.clearAll();
  sigKeysByTurn.clear();
}
