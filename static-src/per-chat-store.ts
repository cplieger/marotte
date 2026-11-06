// Per-chat localStorage maps, bounded by chat count with least-recently-written eviction. Every
// window of a browser profile shares them, so a change goes through updatePerChat, which reads
// the stored document; nothing writes back a map it holds.

/** How many chats one of these maps tracks. */
export const MAX_CHATS = 100;

/** Read the whole map, dropping each entry `valid` rejects. Per entry, not per document: one
 *  hand-edited chat costs only its own state. Unreadable storage reads as an empty map. */
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

/** Replace `chatID`'s entry in the stored map with `update(stored entry)`; undefined deletes it.
 *  A changed entry moves to the end and the oldest chats past MAX_CHATS are evicted. When
 *  `update` returns its argument itself nothing is written, so the chat's recency is untouched.
 *  Returns whether storage now holds the update: false when the write was refused or `chatID`
 *  is empty. */
export function updatePerChat<T>(
  key: string,
  chatID: string,
  valid: (v: unknown) => T | undefined,
  update: (prev: T | undefined) => T | undefined,
): boolean {
  if (chatID === "") {
    return false;
  }
  const stored = Object.entries(readPerChat(key, valid));
  const prev = stored.find(([id]) => id === chatID)?.[1];
  const next = update(prev);
  if (next === prev) {
    return true;
  }
  const kept = stored.filter(([id]) => id !== chatID);
  if (next !== undefined) {
    kept.push([chatID, next]);
  }
  // Trim from the FRONT: the touched chat was just re-inserted at the end, so the head of this list
  // is the least recently written.
  const doc = Object.fromEntries(kept.slice(Math.max(0, kept.length - MAX_CHATS)));
  try {
    localStorage.setItem(key, JSON.stringify(doc));
    return true;
  } catch {
    return false;
  }
}

const changeListeners = new Map<string, Set<() => void>>();
let listening = false;

/** Call `fn` when another window changes `key`, or clears localStorage. The browser fires no
 *  `storage` event in the window that wrote, so a caller's own writes never reach `fn`.
 *  Returns the unsubscribe. */
export function onPerChatChange(key: string, fn: () => void): () => void {
  let fns = changeListeners.get(key);
  if (fns === undefined) {
    fns = new Set();
    changeListeners.set(key, fns);
  }
  fns.add(fn);
  if (!listening) {
    listening = true;
    window.addEventListener("storage", onStorage);
  }
  const registered = fns;
  return () => {
    registered.delete(fn);
  };
}

function onStorage(event: StorageEvent): void {
  // A same-origin sessionStorage change fires the same event; it says nothing about these maps.
  if (event.storageArea !== localStorage) {
    return;
  }
  const sets =
    event.key === null ? [...changeListeners.values()] : [changeListeners.get(event.key)];
  for (const fns of sets) {
    for (const fn of [...(fns ?? [])]) {
      fn();
    }
  }
}
