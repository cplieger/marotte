// A per-chat localStorage map, bounded by chat count with oldest-first eviction.

/** How many chats one of these maps tracks. */
const MAX_CHATS = 100;

/** Read the whole map, dropping anything that is not the expected shape. Validated per ENTRY
 *  rather than as a document, for the reason the server-side sanitizer did it that way:
 *  hand-edited or stale data must not reach a renderer's open/closed decision, and the honest
 *  failure is a missing fold rather than a blank transcript. */
export function readPerChat<T>(
  key: string,
  valid: (v: unknown) => T | undefined,
): Record<string, T> {
  const out: Record<string, T> = {};
  try {
    const raw = localStorage.getItem(key);
    if (raw === null) {
      return out;
    }
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      return out;
    }
    for (const [chatID, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (chatID === "") {
        continue;
      }
      const ok = valid(value);
      if (ok !== undefined) {
        out[chatID] = ok;
      }
    }
  } catch {
    // Disabled storage, a quota failure, or bytes nothing wrote. An arrangement
    // of folds is not data worth reporting a failure over.
  }
  return out;
}

/** Write `value` for `chatID`, evicting the oldest-touched chats past the cap. */
export function writePerChat<T>(
  key: string,
  map: Record<string, T>,
  chatID: string,
  value: T | undefined,
): void {
  if (chatID === "") {
    return;
  }
  const kept = Object.entries(map).filter(([id]) => id !== chatID);
  if (value !== undefined) {
    kept.push([chatID, value]);
  }
  // Trim from the FRONT: the touched chat was just re-inserted at the end, so the head of this list
  // is the least recently written.
  const next = Object.fromEntries(kept.slice(Math.max(0, kept.length - MAX_CHATS)));
  try {
    localStorage.setItem(key, JSON.stringify(next));
  } catch {
    // ignore quota / disabled storage
  }
}

/** @internal Test seam: the cap, so a test states it in the unit production uses
 *  rather than restating the number. */
export const MAX_TRACKED_CHATS = MAX_CHATS;
