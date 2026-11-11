// The window the exec view's Steps and Timeline panes draw: the items their scroller shows plus an
// overscan, with a spacer standing in for each skipped run so the scroll range stays the list's.

import { el } from "@cplieger/ui";

/** Items seated past each edge of the visible band, so a scroll of a few rows meets no blank. */
const OVERSCAN_ROWS = 16;

/** Item `i`'s top is `offsets[i]`, the list's height `offsets[n]`. */
export function offsetsOf(heights: readonly number[]): Float64Array {
  const out = new Float64Array(heights.length + 1);
  for (let i = 0; i < heights.length; i++) {
    out[i + 1] = (out[i] ?? 0) + (heights[i] ?? 0);
  }
  return out;
}

/** The first index whose item ends below `y`. */
function firstEndingBelow(offsets: Float64Array, y: number): number {
  let lo = 0;
  let hi = offsets.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((offsets[mid + 1] ?? 0) > y) {
      hi = mid;
    } else {
      lo = mid + 1;
    }
  }
  return lo;
}

export interface ItemRange {
  readonly from: number;
  readonly to: number;
}

/** The half-open range of items the band `[top, bottom)` touches, widened by the overscan. */
export function rangeOf(offsets: Float64Array, top: number, bottom: number): ItemRange {
  const n = offsets.length - 1;
  const first = firstEndingBelow(offsets, top);
  const last = firstEndingBelow(offsets, Math.max(top, bottom - 1));
  return {
    from: Math.max(0, first - OVERSCAN_ROWS),
    to: Math.min(n, last + 1 + OVERSCAN_ROWS),
  };
}

/** The band of `view` that `scroller` shows, in `view`'s own block coordinates. Never taller than
 *  the window: a box that grows with its content would otherwise grow the window with it. */
export function bandOf(view: HTMLElement, scroller: HTMLElement): { top: number; bottom: number } {
  const top =
    scroller.getBoundingClientRect().top + scroller.clientTop - view.getBoundingClientRect().top;
  return { top, bottom: top + Math.min(scroller.clientHeight, window.innerHeight) };
}

/** Call `onMove` on each scroll or resize of `scroller`; returns the release. A scroll event
 *  already fires once per frame. A resize redraws on the next frame: the redraw can resize the
 *  scroller again, which inside the delivery is a ResizeObserver loop error. */
export function watchScroller(scroller: HTMLElement, onMove: () => void): () => void {
  let frame = 0;
  const observer = new ResizeObserver(() => {
    if (frame === 0) {
      frame = requestAnimationFrame(() => {
        frame = 0;
        onMove();
      });
    }
  });
  observer.observe(scroller);
  scroller.addEventListener("scroll", onMove, { passive: true });
  return () => {
    observer.disconnect();
    cancelAnimationFrame(frame);
    frame = 0;
    scroller.removeEventListener("scroll", onMove);
  };
}

/** A spacer for a skipped run of items: no content, a height only. */
export function gapEl(): HTMLElement {
  return el("div", { className: "ev-gap", "aria-hidden": "true" });
}

/** Size a spacer, writing nothing when it already has that height. */
export function sizeGap(gap: HTMLElement, px: number): void {
  const size = `${String(Math.round(px))}px`;
  if (gap.style.blockSize !== size) {
    gap.style.blockSize = size;
  }
}

/** `node`'s rendered block size plus its block-start margin, or `fallback` when it has none. */
export function pitchOf(node: Element | null, fallback: number): number {
  if (!(node instanceof HTMLElement) || node.offsetHeight === 0) {
    return fallback;
  }
  return node.offsetHeight + (Number.parseFloat(getComputedStyle(node).marginBlockStart) || 0);
}
