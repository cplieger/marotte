// When stream frames reach the stores: decoded on arrival, then applied in arrival order once per
// display frame, in one `batch()` inside one scroll-owner hold, so a frame period costs one pass.

import { batch } from "@cplieger/reactive";

/** A visible document whose animation frames are suppressed (an occluded window, a throttled
 *  frame) still applies its queue within this. */
const FALLBACK_MS = 250;

let queue: (() => void)[] = [];
let frame: number | null = null;
let fallback: ReturnType<typeof setTimeout> | null = null;
let applying = false;
let hold: (fn: () => void) => void = (fn) => {
  fn();
};
const closers = new Set<() => void>();

const doc = (): Document | undefined => (globalThis as { readonly document?: Document }).document;

// rAF callbacks pause in a background tab (https://developer.mozilla.org/en-US/docs/Web/API/Window/requestAnimationFrame).
const hidden = (): boolean => doc()?.visibilityState === "hidden";

/** The scroll owner's hold, which answers every change of one batch with one read. */
export function setFrameHold(fn: (fn: () => void) => void): void {
  hold = fn;
}

/** Run `fn` inside every batch, after its frames applied and before its effects run. */
export function onBatchClose(fn: () => void): void {
  closers.add(fn);
}

/** Whether a batch is applying frames: a version bump made now joins the batch's one pass. */
export function inFrameBatch(): boolean {
  return applying;
}

/** Queue one decoded frame's apply. A hidden document applies it at once. */
export function queueFrame(apply: () => void): void {
  queue.push(apply);
  if (applying) {
    return;
  }
  if (hidden()) {
    flushFrames();
    return;
  }
  if (frame === null) {
    frame = requestAnimationFrame(flushFrames);
    fallback = setTimeout(flushFrames, FALLBACK_MS);
  }
}

/** Apply every queued frame now. A read applier, a revalidation and an action call it before
 *  they act, so they see every frame that arrived before them. */
export function flushFrames(): void {
  if (frame !== null) {
    cancelAnimationFrame(frame);
    frame = null;
  }
  if (fallback !== null) {
    clearTimeout(fallback);
    fallback = null;
  }
  if (applying || queue.length === 0) {
    return;
  }
  const run = (): void => {
    batch(() => {
      applying = true;
      try {
        // A frame a handler queues joins this batch: an array iterator reads the length each step.
        for (const apply of queue) {
          try {
            apply();
          } catch (e) {
            console.error("[frames] a frame's apply threw:", e);
          }
        }
        queue = [];
        for (const close of closers) {
          close();
        }
      } finally {
        // Cleared before `batch` returns: the effects it then runs bump synchronously.
        applying = false;
      }
    });
  };
  if (hidden()) {
    run();
  } else {
    hold(run);
  }
}

doc()?.addEventListener("visibilitychange", () => {
  if (hidden()) {
    flushFrames();
  }
});

/** Drop the queue and its timers, for suites that boot the app more than once. */
// deadset:ignore DS1102 -- test seam: the frame queue, its animation frame and its fallback timer
export function _resetFramesForTest(): void {
  if (frame !== null) {
    cancelAnimationFrame(frame);
    frame = null;
  }
  if (fallback !== null) {
    clearTimeout(fallback);
    fallback = null;
  }
  queue = [];
  applying = false;
}
