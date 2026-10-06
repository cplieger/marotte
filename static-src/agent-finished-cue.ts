// The agent-finished cue: raised when a turn ends and EVERYTHING it started is over. One
// module owns the whole policy (per-kind switch, replay dedup, settle test, parking,
// re-fire) so no two copies can disagree. A cue is parked with the turn's own body and
// released by whatever ends the last outstanding thing, whatever its outcome. The
// re-fire is an effect over `chatSettled`'s tracked reads. Not persisted: after a reload
// the reader is back, and a persisted cue would notify for work already seen.

import { effect, signal, touch } from "@cplieger/reactive";
import { chatSettled } from "./chat-settled.js";
import { isAgentFinishedEnabled, notifyIfHidden, NOTIFY_TITLE } from "./notify.js";
import { chatTarget } from "./push-subject.js";

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
 * chat id -> the body of the cue withheld for it. Insertion-ordered, so eviction drops the
 * chat waiting longest.
 */
const deferred = new Map<string, string>();

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
function raiseAgentFinished(chatID: string, body: string): boolean {
  if (!isAgentFinishedEnabled()) {
    return false;
  }
  const now = Date.now();
  pruneNotifyMap(now);
  if (now - (lastNotifyMs.get(chatID) ?? 0) <= DEDUP_MS) {
    return false;
  }
  lastNotifyMs.set(chatID, now);
  return notifyIfHidden(NOTIFY_TITLE, body, chatTarget(chatID));
}

/**
 * A turn ended on `chatID`; `body` is what its cue would say ("" for a turn that says
 * nothing). Raises now when the chat is settled, parks otherwise; the per-kind switch is
 * checked before parking too.
 */
export function noteAgentFinished(chatID: string, body: string): void {
  if (chatID === "" || body === "" || !isAgentFinishedEnabled()) {
    return;
  }
  if (chatSettled(chatID)) {
    raiseAgentFinished(chatID, body);
    return;
  }
  // A repeat park overwrites by key, so a replayed `turn_closed` cannot produce two cues.
  deferred.set(chatID, body);
  while (deferred.size > MAP_CAP) {
    const oldest = deferred.keys().next().value;
    if (oldest === undefined) {
      break;
    }
    deferred.delete(oldest);
  }
  deferredVersion.value = deferredVersion.peek() + 1;
}

/** Drop a chat's parked cue, wherever the chat goes away (tab close, remote delete). */
export function forgetDeferredCue(chatID: string): void {
  deferred.delete(chatID);
}

/** Whether a cue is currently parked for `chatID`. Read by tests and by nothing in
 *  production: the release is an effect, so no caller polls. */
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
    for (const [chatID, body] of [...deferred]) {
      if (!chatSettled(chatID)) {
        continue;
      }
      // Removed BEFORE the raise, or a refused raise would re-offer it on every later pass.
      deferred.delete(chatID);
      raiseAgentFinished(chatID, body);
    }
  });
}

/** Reset every map. Exported for test isolation only. */
export function _resetAgentFinishedCueForTest(): void {
  deferred.clear();
  lastNotifyMs.clear();
  deferredVersion.value = 0;
}
