// The version rules a turn's open tails obey, shared by the chat's and the run's log. Every frame
// carries its version (a sealed entry its `seq`, an open one its `(id, n)`), so a read's answer and
// the frames that race it land in the same state in either order.

import type { Entry, OpenEntry } from "./types.js";

interface SealedIndex {
  readonly ids: Set<string>;
  upTo: number;
}

/** Keyed on the entries array, which only grows: each lookup indexes just the entries appended
 *  since the last one. */
const sealedIndex = new WeakMap<readonly Entry[], SealedIndex>();

/** Whether `entries` holds the entry `id` sealed. */
function isSealed(entries: readonly Entry[], id: string): boolean {
  let index = sealedIndex.get(entries);
  if (index === undefined || index.upTo > entries.length) {
    index = { ids: new Set(), upTo: 0 };
    sealedIndex.set(entries, index);
  }
  for (; index.upTo < entries.length; index.upTo++) {
    const e = entries[index.upTo];
    if (e !== undefined) {
      index.ids.add(e.id);
    }
  }
  return index.ids.has(id);
}

/** The tails a turn holds once a read's answer adopts, with `entries` its sealed entries after the
 *  answer's are seated. A tail whose entry is sealed goes; on one id the higher `n` stays; a lane
 *  only `held` names keeps its tail, which opened after the answer's snapshot; otherwise the
 *  answer's tail is the newer word. */
export function mergeOpenTails(
  entries: readonly Entry[],
  held: ReadonlyMap<string, OpenEntry>,
  answer: Iterable<OpenEntry>,
): Map<string, OpenEntry> {
  const out = new Map<string, OpenEntry>();
  for (const [lane, open] of held) {
    if (!isSealed(entries, open.id)) {
      out.set(lane, open);
    }
  }
  for (const open of answer) {
    const lane = open.lane ?? "";
    const mine = out.get(lane);
    if (isSealed(entries, open.id) || (mine?.id === open.id && mine.n >= open.n)) {
      continue;
    }
    out.set(lane, { ...open, lane });
  }
  return out;
}

/** Whether an `entry_opened` repeats what the turn already holds: its entry sealed, or open on its
 *  lane with at least as many deltas folded in. */
export function openIsRedelivery(
  entries: readonly Entry[],
  held: ReadonlyMap<string, OpenEntry>,
  open: OpenEntry,
): boolean {
  const mine = held.get(open.lane ?? "");
  return (mine?.id === open.id && mine.n >= open.n) || isSealed(entries, open.id);
}

/** Whether an `entry_delta` with running count `n` repeats what the turn already holds. */
export function deltaIsRedelivery(
  entries: readonly Entry[],
  open: OpenEntry | undefined,
  entryID: string,
  n: number,
): boolean {
  return open?.id === entryID ? n <= open.n : isSealed(entries, entryID);
}

/** Whether an `entry_sealed` at `seq` repeats a seal the turn already holds. */
export function sealIsRedelivery(entries: readonly Entry[], entryID: string, seq: number): boolean {
  return entries[seq]?.id === entryID;
}
