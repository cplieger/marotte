import { touch } from "@cplieger/reactive";
import { messagesVersionOf, promptOpened, turnLive } from "./store.js";
import type { PendingSteer, QueuedPrompt, Session } from "./types.js";

/** When the server sends a queued row. `turns` counts turn ends, the running turn's included, or
 *  from the next turn's while none runs. */
type QueuedWait =
  | { readonly kind: "turns"; readonly turns: number; readonly running: boolean }
  | { readonly kind: "held" }
  | { readonly kind: "sent" };

export interface QueuedSlot {
  readonly row: QueuedPrompt;
  readonly wait: QueuedWait;
}

/** Mirrors the server's drain (`drainAfterClose`, `NextUserRow`): one prompt per turn end, the
 *  unsent steers together ahead of the first user row that is not held, carried or opened, unless
 *  the server marks them `awaitsPrompt` or no turn runs: then a prompt's start carries them. A sent
 *  steer is counted as read inside its turn. `opened`
 *  answers whether a turn for the row's id has opened, which the header shows until the server's
 *  dequeue lands. `next` is the wait a row queued now would get. */
function drainSlots(
  steers: readonly PendingSteer[],
  queued: readonly QueuedPrompt[],
  running: boolean,
  opened: (id: string) => boolean,
): { rows: QueuedSlot[]; next: number } {
  const unreadTurn = steers.some((s) => s.unsent === true && s.awaitsPrompt !== true);
  let ahead = running && unreadTurn ? 1 : 0;
  const rows = queued.map((row): QueuedSlot => {
    if (opened(row.id)) {
      return { row, wait: { kind: "sent" } };
    }
    if (row.held === true || (row.resends ?? []).length > 0) {
      return { row, wait: { kind: "held" } };
    }
    ahead += 1;
    return { row, wait: { kind: "turns", turns: ahead, running } };
  });
  return { rows, next: ahead + 1 };
}

/** `drainSlots` over a chat, as a tracked read. Opening or closing a turn changes the turn map in
 *  place and bumps only the transcript version, so the read subscribes to that too. */
export function sessionDrain(s: Session): { rows: QueuedSlot[]; next: number } {
  touch(messagesVersionOf(s.id));
  return drainSlots(s.steers ?? [], s.queued ?? [], turnLive(s), (id) => promptOpened(s, id));
}

/** The turns a wait covers, as "after …" completes it. */
export function turnsPhrase(turns: number, running: boolean): string {
  if (turns > 1) {
    return `${String(turns)} turns`;
  }
  return running ? "this turn" : "the next turn";
}
