// A faked timer clock that still runs with real time, so a case can step past a long hold (a
// button's outcome glyph, a retry backoff) instead of sleeping it out, while every zero-delay
// hop and short timer the page relies on still fires on its own.
import { vi } from "vitest";

const STEP_MS = 50;

/** Fake `setTimeout`/`clearTimeout`; the caller restores real timers in its `afterEach`. */
export function useSteppableClock(): void {
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout"],
    shouldAdvanceTime: true,
    advanceTimeDelta: 5,
  });
}

/** `vi.waitFor` on the faked clock: steps it STEP_MS at a time until `check` passes, and throws
 *  check's own error once `budgetMs` of clock has passed. Returns as soon as `check` passes, so
 *  no timer due after that point fires early. */
export async function waitForOnClock(check: () => void, budgetMs: number): Promise<void> {
  for (let spent = 0; ; spent += STEP_MS) {
    try {
      check();
      return;
    } catch (e) {
      if (spent >= budgetMs) {
        throw e;
      }
    }
    await vi.advanceTimersByTimeAsync(STEP_MS);
  }
}

/** Await `p` on the faked clock, stepping it STEP_MS at a time; throws once `budgetMs` of clock
 *  has passed with `p` still pending. */
export async function awaitOnClock<T>(p: Promise<T>, budgetMs: number): Promise<T> {
  let pending = true;
  const tracked = p.finally(() => {
    pending = false;
  });
  // Observed here so a rejection landing mid-step is not reported as unhandled; the return rethrows it.
  tracked.catch(() => undefined);
  for (let spent = 0; pending; spent += STEP_MS) {
    if (spent >= budgetMs) {
      throw new Error(`still pending after ${String(budgetMs)}ms of clock`);
    }
    await vi.advanceTimersByTimeAsync(STEP_MS);
  }
  return tracked;
}
