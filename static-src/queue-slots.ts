import { touch } from "@cplieger/ui";
import { messagesVersionOf, promptOpened, sessionBusy } from "./store.js";
import type { PendingSteer, QueuedPrompt, Session } from "./types.js";

/** When the server sends a queued row. `turns` counts turn ends, the running turn's included, or
 *  from the next turn's while none runs; behind a running compaction whose end sends a row, that
 *  row goes at its end (`compaction`) and `turns` counts from the turn it opens. */
type QueuedWait =
  | {
      readonly kind: "turns";
      readonly turns: number;
      readonly running: boolean;
      readonly afterCompaction: boolean;
    }
  | { readonly kind: "compaction" }
  | { readonly kind: "held" }
  | { readonly kind: "sent" };

export interface QueuedSlot {
  readonly row: QueuedPrompt;
  readonly wait: QueuedWait;
}

/** Mirrors the server's drain (`drainAfterClose`, `NextUserRow`): one prompt per turn end, the
 *  unsent steers together ahead of the first user row that is not held, carried or opened, unless
 *  the server marks them `awaitsPrompt`, no turn runs, or a row is marked `next` (the send-now
 *  arrow's): then a prompt's start carries them. A sent steer is counted as read inside its turn.
 *  A compaction running with no turn sends at its end the row marked `next`, else the unsent
 *  steers, else the first sendable row when `sends` (`PendingCompaction.Sends`). `opened` answers
 *  whether a turn for the row's id has opened, which the header shows until the server's dequeue
 *  lands. The returned `next` is the wait a row queued now would get while a turn runs. */
function drainSlots(
  steers: readonly PendingSteer[],
  queued: readonly QueuedPrompt[],
  running: boolean,
  compacting: { readonly running: boolean; readonly sends: boolean },
  opened: (id: string) => boolean,
): { rows: QueuedSlot[]; next: number } {
  const unreadTurn = steers.some((s) => s.unsent === true && s.awaitsPrompt !== true);
  const marked = queued.some(
    (row) =>
      row.next === true && row.held !== true && (row.resends ?? []).length === 0 && !opened(row.id),
  );
  const afterCompaction = compacting.running && !running;
  const compactionSendsRow = marked || (compacting.sends && !unreadTurn);
  let ahead = running && unreadTurn && !marked ? 1 : afterCompaction && compactionSendsRow ? -1 : 0;
  const rows = queued.map((row): QueuedSlot => {
    if (opened(row.id)) {
      return { row, wait: { kind: "sent" } };
    }
    if (row.held === true || (row.resends ?? []).length > 0) {
      return { row, wait: { kind: "held" } };
    }
    ahead += 1;
    if (ahead === 0) {
      return { row, wait: { kind: "compaction" } };
    }
    return { row, wait: { kind: "turns", turns: ahead, running, afterCompaction } };
  });
  return { rows, next: ahead + 1 };
}

/** `drainSlots` over a chat, as a tracked read. Opening or closing a turn changes the turn map in
 *  place and bumps only the transcript version, so the read subscribes to that too. */
export function sessionDrain(s: Session): { rows: QueuedSlot[]; next: number } {
  touch(messagesVersionOf(s.id));
  return drainSlots(
    s.steers ?? [],
    s.queued ?? [],
    sessionBusy(s),
    { running: s.compact_running === true, sends: s.compact_sends === true },
    (id) => promptOpened(s, id),
  );
}

/** The turns a wait covers, as "after …" completes it; zero turns is a running compaction's end. */
export function turnsPhrase(turns: number, running: boolean): string {
  if (turns === 0) {
    return "the compaction";
  }
  if (turns > 1) {
    return `${String(turns)} turns`;
  }
  return running ? "this turn" : "the next turn";
}
