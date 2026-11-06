// A stamp per held turn that moves when its entries change in place (a payload replaced, a waking mark
// flipped), which neither its entry count nor its open lanes show. A new `TurnState` object reads a new stamp.

import type { TurnState } from "./types.js";

const stamps = new WeakMap<TurnState, number>();
let next = 0;

/** Record that one of `state`'s held entries changed without the entry count moving. */
export function touchTurn(state: TurnState): void {
  stamps.set(state, ++next);
}

/** Equal across two reads only when the same object was read and no `touchTurn` came between. */
export function turnRevision(state: TurnState): number {
  let stamp = stamps.get(state);
  if (stamp === undefined) {
    stamp = ++next;
    stamps.set(state, stamp);
  }
  return stamp;
}
