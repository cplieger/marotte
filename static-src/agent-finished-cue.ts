// The agent-finished cue: raised when a turn ends and EVERYTHING it started is over. One
// module owns WHEN (per-kind switch, replay dedup, settle test, parking, re-fire); the
// server's notification owns what it says. A cue is parked with its notification and
// released by whatever ends the last outstanding thing, whatever its outcome. The
// re-fire is an effect over `chatSettled`'s tracked reads. Not persisted: after a reload
// the reader is back, and a persisted cue would notify for work already seen.

import { effect, signal, touch } from "@cplieger/reactive";
import { chatSettled } from "./chat-settled.js";
import { isAgentFinishedEnabled, notifyOffScreen } from "./notify.js";
import type { NotificationPayload } from "./wire/types.gen.js";

/** Per-chat dedup window. An SSE reconnect replays `turn_closed`, and the duplicates
 *  arrive within milliseconds of each other. */
const DEDUP_MS = 2000;
/**
 * How long a dedup stamp is worth keeping; pruning bounds the map while notifications are
 * off, a state the notify path never visits.
 */
const DEDUP_STALE_MS = 10_000;
/** Bound on both maps. */
const MAP_CAP = 200;

/** chat id -> the millisecond a cue was last raised for it. */
const lastNotifyMs = new Map<string, number>();

/**
 * chat id -> the notification withheld for it. Insertion-ordered, so eviction drops the
 * chat waiting longest.
 */
const deferred = new Map<string, NotificationPayload>();

/**
 * Bumped when a cue is PARKED, so the release effect has something to wake on before any
 * chat is in the set. Not bumped on removal: the effect writes that path, and writing a
 * signal it read is a cycle the reactive layer refuses.
 */
const deferredVersion = signal(0);

/** Drop dedup stamps nobody needs, and cap what is left. */
function pruneNotifyMap(now: number): void {
  for (const [chatID, at] of lastNotifyMs) {
    if (now - at > DEDUP_STALE_MS) {
      lastNotifyMs.delete(chatID);
    }
  }
  while (lastNotifyMs.size > MAP_CAP) {
    const oldest = lastNotifyMs.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    lastNotifyMs.delete(oldest);
  }
}

/**
 * Raise the cue now, through the per-kind switch and the dedup window. The switch is
 * re-read here because a deferral can outlive the reader's choice. No time bound on a
 * parked cue: the entry cap bounds the set, and an evicted cue is DROPPED, not fired.
 */
function raiseAgentFinished(chatID: string, notice: NotificationPayload): void {
  if (!isAgentFinishedEnabled()) {
    return;
  }
  const now = Date.now();
  pruneNotifyMap(now);
  if (now - (lastNotifyMs.get(chatID) ?? 0) <= DEDUP_MS) {
    return;
  }
  lastNotifyMs.set(chatID, now);
  notifyOffScreen(notice);
}

/**
 * A turn ended on `chatID` and the server says `notice` about it. Raises now when the chat is
 * settled, parks otherwise; the per-kind switch is checked before parking too.
 */
export function noteAgentFinished(chatID: string, notice: NotificationPayload): void {
  if (chatID === "" || !isAgentFinishedEnabled()) {
    return;
  }
  if (chatSettled(chatID)) {
    raiseAgentFinished(chatID, notice);
    return;
  }
  // A repeat park overwrites by key, so a replayed notification cannot produce two cues.
  deferred.set(chatID, notice);
  while (deferred.size > MAP_CAP) {
    const oldest = deferred.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    deferred.delete(oldest);
  }
  deferredVersion.value = deferredVersion.peek() + 1;
}

/**
 * Deliver one server notification on this page, whichever channel brought it (the `notification`
 * frame, or a Web Push that landed on a focused page): a finished turn waits for its work, every
 * other notice shows at once, and both stay silent while their own tab is on screen.
 */
export function deliverNotification(chatID: string, notice: NotificationPayload): void {
  if (notice.kind === "agent_finished") {
    noteAgentFinished(chatID, notice);
    return;
  }
  notifyOffScreen(notice);
}

/** Drop a chat's parked cue, wherever the chat goes away (tab close, remote delete). */
export function forgetDeferredCue(chatID: string): void {
  deferred.delete(chatID);
}

/** Whether a cue is currently parked for `chatID`. Read by tests and by nothing in
 *  production: the release is an effect, so no caller polls. */
// deadset:ignore DS1004 -- test seam: observes the parked-cue map
export function hasDeferredCue(chatID: string): boolean {
  return deferred.has(chatID);
}

/**
 * Wire the release; called from the composition root because an effect at module load
 * would read an unhydrated store. One effect: every input is a signal read inside its pass.
 */
export function installDeferredCueSubscriber(): () => void {
  return effect(() => {
    touch(deferredVersion);
    // A copy, so deleting the entry a pass is releasing cannot disturb the walk.
    for (const [chatID, notice] of [...deferred]) {
      if (!chatSettled(chatID)) {
        continue;
      }
      // Removed BEFORE the raise, or a refused raise would re-offer it on every later pass.
      deferred.delete(chatID);
      raiseAgentFinished(chatID, notice);
    }
  });
}

/** Reset every map. Exported for test isolation only. */
// deadset:ignore DS1004 -- test seam: resets the parked cues, notify times and deferred version
export function _resetAgentFinishedCueForTest(): void {
  deferred.clear();
  lastNotifyMs.clear();
  deferredVersion.value = 0;
}
