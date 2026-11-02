// Which workflow-message rows a later turn's wake draws as its trigger, so the row draws nothing where
// it was filed. A WeakSet over the store's own entry objects, so a page that replaces them starts
// unmarked until `markWakingRows` (turn-wake.ts) reads it. Its own module so `turns.ts` can ask it
// without importing the wake's wording.

import type { Entry } from "./types.js";

const wakingRows = new WeakSet<Entry>();

/** Whether a later turn's wake draws this workflow message as its trigger. */
export function isWakingRow(e: Entry): boolean {
  return wakingRows.has(e);
}

export function setWakingRow(e: Entry, waking: boolean): void {
  if (waking) {
    wakingRows.add(e);
  } else {
    wakingRows.delete(e);
  }
}
