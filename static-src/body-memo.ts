// Values derived from one turn body, kept per body array. `projectTurns` hands a turn the same array until its entries
// change (`turns.ts` `bodyOf`), so a full pass over unchanged turns costs a lookup per turn, not a walk of its entries.

import type { Entry } from "./types.js";

/** One memo: `derive`'s answer per (body array, key). */
type BodyMemo<T> = (body: readonly Entry[], key: string, derive: () => T) => T;

/**
 * A memo keyed by body array identity. The body must be a projected `Turn.body`, never a store `TurnState.entries`,
 * which grows in place under one identity; `key` names every other input `derive` reads.
 */
export function bodyMemo<T>(): BodyMemo<T> {
  const memo = new WeakMap<readonly Entry[], Map<string, T>>();
  return (body, key, derive) => {
    let byKey = memo.get(body);
    if (byKey === undefined) {
      byKey = new Map();
      memo.set(body, byKey);
    }
    if (byKey.has(key)) {
      return byKey.get(key) as T;
    }
    const value = derive();
    byKey.set(key, value);
    return value;
  };
}
