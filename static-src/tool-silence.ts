// ---------------------------------------------------------------------------
// Per-tool-call SILENCE: the wall clock this client last applied a frame for a
// call, and the words a card states about it.
//
// OBSERVABILITY, NEVER A BOUND. Nothing here cancels a call, kills a turn or
// orders an entry: the value is `Date.now()`-derived and reads NO entry `ts`, so
// it is a display value in the same class as `elapsed_ms` and the property that
// no comparison function reads a timestamp is untouched.
//
// A per-call CANCEL is refused separately and for its own reason: ACP's cancel
// verb is turn-scoped, so bounding one call means cancelling its turn, which is
// the class the run-bounds rewrite exists to stop.
// ---------------------------------------------------------------------------

import { signal, touch } from "@cplieger/reactive";
import { toolCallSigKey } from "./store-signals.js";

/** Past this much silence a card STATES the fact. Minutes, because the reader's
 *  question is whether a call is doing slow work or will never answer, and a
 *  threshold in seconds fires on every ordinary compile. */
export const SILENCE_THRESHOLD_MS = 120_000;

/** How often the marker's words are re-derived while a call is tracked. The
 *  marker is worded in whole MINUTES, so a coarser tick than the run card's own
 *  1s clock costs nothing a reader can see. */
const SILENCE_TICK_MS = 15_000;

/** Keyed `toolCallSigKey(chat, call)` — the same composite the card's own signal
 *  uses, because a tool call id is backend-authored and carries no uniqueness
 *  guarantee, so a background chat's call would otherwise report for a visible
 *  card holding the same id. */
const lastActivity = new Map<string, number>();

/** Bumped by the ticker below. A reader inside an effect subscribes by reading
 *  it, which is what makes the marker APPEAR on a call that has gone quiet with
 *  no frame left to repaint its card. */
const tick = signal(0);

let timer: ReturnType<typeof setInterval> | undefined;

/** ONE interval for every tracked call, and it exists only while at least one is
 *  tracked, so an idle transcript costs nothing. */
function ensureTicker(): void {
  if (timer !== undefined) {
    return;
  }
  timer = setInterval(() => {
    if (lastActivity.size === 0) {
      stopTicker();
      return;
    }
    tick.value = tick.peek() + 1;
  }, SILENCE_TICK_MS);
}

function stopTicker(): void {
  if (timer === undefined) {
    return;
  }
  clearInterval(timer);
  timer = undefined;
}

/** Record that this client APPLIED a frame for a call. Two producers, and both
 *  are frames rather than facts read off an entry: the live create a card is
 *  built from, and every folded `tool_progress` (`store.ts` `applyToolProgress`,
 *  the one client-side application point for that frame). */
export function noteToolActivity(chatID: string, toolID: string, now = Date.now()): void {
  lastActivity.set(toolCallSigKey(chatID, toolID), now);
  ensureTicker();
}

/** How long this call has been silent, or undefined when this client has applied
 *  no frame for it at all. TRACKED: it reads the ticker, so a caller inside an
 *  effect repaints while the call stays quiet. */
export function silenceMsFor(chatID: string, toolID: string, now = Date.now()): number | undefined {
  // Subscribe-only read, through the library's own helper (`run-store.ts`'s
  // `liveRunsForChat` spells it the same way). Without it the marker could only
  // appear on a frame, which is exactly the frame a silent call does not send.
  touch(tick);
  const at = lastActivity.get(toolCallSigKey(chatID, toolID));
  return at === undefined ? undefined : now - at;
}

/** The marker's words: the FACT and never a verdict. A call doing slow work and
 *  one that will never answer read the same here, because this client cannot tell
 *  them apart and a guess would be wrong half the time. */
export function silenceLabel(ms: number): string {
  const mins = Math.max(1, Math.floor(ms / 60_000));
  return `no output for ${mins} ${mins === 1 ? "minute" : "minutes"}`;
}

/** Stop tracking one call: its own settle, which is the one event that says no
 *  further frame is coming. */
export function forgetToolSilence(chatID: string, toolID: string): void {
  lastActivity.delete(toolCallSigKey(chatID, toolID));
  if (lastActivity.size === 0) {
    stopTicker();
  }
}

/** Drop every stamp and the ticker with them: full teardown, and the tests' own
 *  reset between cases. */
export function clearToolSilence(): void {
  lastActivity.clear();
  stopTicker();
}
