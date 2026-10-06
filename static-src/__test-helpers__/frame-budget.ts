// Failure bounds for FRAME-driven tests: deep in a large suite Chromium can deliver rAF at ~1Hz, so
// budgets are seconds per frame. Bounds, never targets. Keep a consumer's per-test timeout above its
// budget, or vitest's default preempts the named assertion.

/** Rough wall-clock cost of one frame delivery when the throttle is in force. */
const THROTTLED_FRAME_MS = 1_200;

/** Budget for a poll that settles within a handful of frames. */
export const FRAME_BUDGET_MS = 10_000;

/** Budget for a wait of `frames` fixed frames, floored at {@link FRAME_BUDGET_MS}. */
export function framesBudgetMs(frames: number): number {
  return Math.max(FRAME_BUDGET_MS, frames * THROTTLED_FRAME_MS);
}

/** A per-test timeout that cannot preempt `budget`. */
export function testTimeoutFor(budget: number): number {
  return budget + 5_000;
}
