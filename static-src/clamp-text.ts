// A text element capped to N lines with a show-more opener. The measurement facts are on `watchClamp`.

/** Above this, assume overflow when layout cannot be measured. Generous: a false positive costs one frame of opener. */
const CLAMP_FALLBACK_CHARS = 220;

/** Lines when the caller states none; every consumer states its own. */
const CLAMP_LINES = 3;

const LABEL_MORE = "Show more";
const LABEL_LESS = "Show less";

/** Measured clip height written by `reviewClamp` (its one writer), read as `max-block-size: var(--clamp-h, <nominal>)`. */
const CLAMP_H = "--clamp-h";

/**
 * Overhang (px) below which no height is written. One constant serves the edge filter and the straddle test: a
 * slacker straddle test awards a cut.
 */
const SNAP_EPSILON = 0.5;

interface ClampOptions {
  /** Lines the STYLESHEET clamps to; read here by the character fallback only. */
  readonly lines?: number;
  /** Character threshold for the no-layout guess. */
  readonly fallbackChars?: number;
  /**
   * Snap the collapsed clip down to the last whole line by writing {@link CLAMP_H}. Only for a stylesheet that reads it
   * as a height: on a `-webkit-line-clamp` box it double-clips. The run results box (markdown blocks) is the consumer.
   */
  readonly snapToLine?: boolean;
  /** Where the caller keeps the expanded flag, when it keeps one. */
  readonly isExpanded?: () => boolean;
  readonly setExpanded?: (on: boolean) => void;
}

interface ClampHandle {
  /** Re-decide the opener against the current text, PRESERVING an expansion. */
  sync(): void;
  /** Collapse and forget any expansion — for content that has changed. */
  collapse(): void;
  /** Stop clamping: no attribute, no opener. Reversed by the next `sync`. */
  disable(): void;
}

interface ClampState {
  readonly more: HTMLButtonElement;
  readonly lines: number;
  readonly fallbackChars: number;
  readonly snapToLine: boolean;
  readonly isExpanded: () => boolean;
  readonly setExpanded: (on: boolean) => void;
  /** Set by `disable`: a resize must not put the clamp back. */
  off: boolean;
}

interface ClampEntry {
  readonly state: ClampState;
  readonly handle: ClampHandle;
}

const clamps = new WeakMap<HTMLElement, ClampEntry>();

/** Exists because a WeakMap cannot be enumerated and a subtree sweep must be. */
const observed = new Set<HTMLElement>();

/**
 * Clamp `text` behind `more`; returns the handle a repaint drives. Idempotent: a repeat call returns the existing
 * handle. The caller stores the expanded flag (`isExpanded`).
 */
export function attachClamp(
  text: HTMLElement,
  more: HTMLButtonElement,
  opts: ClampOptions = {},
): ClampHandle {
  const found = clamps.get(text);
  if (found !== undefined) {
    return found.handle;
  }

  let localExpanded = false;
  const state: ClampState = {
    more,
    lines: opts.lines ?? CLAMP_LINES,
    fallbackChars: opts.fallbackChars ?? CLAMP_FALLBACK_CHARS,
    snapToLine: opts.snapToLine ?? false,
    isExpanded: opts.isExpanded ?? ((): boolean => localExpanded),
    setExpanded:
      opts.setExpanded ??
      ((on: boolean): void => {
        localExpanded = on;
      }),
    off: false,
  };
  const handle: ClampHandle = {
    sync: () => {
      state.off = false;
      reviewClamp(text, state);
    },
    collapse: () => {
      state.off = false;
      writeExpanded(text, state, false);
      reviewClamp(text, state);
    },
    disable: () => {
      state.off = true;
      text.removeAttribute("data-clamped");
      more.hidden = true;
    },
  };
  clamps.set(text, { state, handle });

  more.hidden = true;
  more.addEventListener("click", () => {
    writeExpanded(text, state, !state.isExpanded());
  });
  watchClamp(text);
  reviewClamp(text, state);
  return handle;
}

/** Measurement decides when layout exists, the character fallback otherwise, so a long text always has an opener. */
function reviewClamp(text: HTMLElement, s: ClampState): void {
  if (s.off) {
    return;
  }
  if (s.isExpanded()) {
    s.more.hidden = false;
    return;
  }
  text.setAttribute("data-clamped", "");
  s.more.textContent = LABEL_MORE;
  s.more.setAttribute("aria-expanded", "false");

  // Withdraw a previous snap first, or it becomes the next review's budget and each resize loses a line.
  if (s.snapToLine) {
    text.style.removeProperty(CLAMP_H);
  }
  const measured = text.scrollHeight;
  const visible = text.clientHeight;
  const body = text.textContent;
  const overflows =
    measured > 0 && visible > 0
      ? measured - visible > 1
      : body.length > s.fallbackChars || countLines(body) > s.lines;
  s.more.hidden = !overflows;
  // Gated on overflow: a height written for text that fits makes `scrollHeight > clientHeight` by a fraction and offers
  // Show more over nothing.
  if (s.snapToLine && overflows && measured > 0 && visible > 0) {
    snapClampToLine(text);
  }
}

/** `ink` holds only rects that start above the clip; one starting below can straddle nothing, which bounds the store. */
interface ClipRegion {
  readonly ink: readonly { readonly top: number; readonly bottom: number }[];
  readonly edges: readonly number[];
}

/**
 * One walk, stopping at the first sibling whose box top is more than `EPS` below the deepest ink read; `readTo` keeps
 * it from stopping mid-row. A lost rect only ever trims more. Prune-only cost 3.7x on a 540-line report.
 */
const collectClipRegion = (text: HTMLElement, clipY: number): ClipRegion => {
  const ink: { top: number; bottom: number }[] = [];
  /**
   * Candidate clip positions: every rect bottom plus every box that rendered no text (`<img>`, `<hr>`, empty fence),
   * which a text-only walk trims past. Zero-height boxes are excluded: they would move the clip into a margin gap.
   */
  const edges: number[] = [];
  const range = document.createRange();
  let readTo = clipY;

  const readText = (node: Text): boolean => {
    range.selectNodeContents(node);
    let rendered = false;
    for (const rect of range.getClientRects()) {
      if (rect.height <= 0) {
        continue;
      }
      rendered = true;
      if (rect.top < clipY - SNAP_EPSILON) {
        ink.push({ top: rect.top, bottom: rect.bottom });
        readTo = Math.max(readTo, rect.bottom);
      }
      if (rect.bottom <= clipY + SNAP_EPSILON) {
        edges.push(rect.bottom);
      }
    }
    return rendered;
  };

  const collect = (parent: Node): boolean => {
    let rendered = false;
    let stopped = false;
    for (let child = parent.firstChild; child !== null; child = child.nextSibling) {
      if (child.nodeType === Node.TEXT_NODE) {
        rendered = readText(child as Text) || rendered;
        continue;
      }
      if (child.nodeType !== Node.ELEMENT_NODE) {
        continue;
      }
      const el = child as Element;
      const box = el.getBoundingClientRect();
      // A box-less element (`display: none`) reports an all-zero rect, so the stop tests its
      // extent before its position — defence in depth, since `fits <= 0` refuses there.
      if ((box.width > 0 || box.height > 0) && box.top > readTo + SNAP_EPSILON) {
        stopped = true;
        break;
      }
      if (collect(el)) {
        rendered = true;
        continue;
      }
      if (box.height > 0 && box.bottom <= clipY + SNAP_EPSILON) {
        edges.push(box.bottom);
      }
    }
    // A subtree the walk STOPPED inside is not one that rendered nothing, so it is never
    // classified as an atom and contributes no edge of its own.
    return rendered || stopped;
  };

  collect(text);
  return { ink, edges };
};

/**
 * The greatest edge no rendered rect strictly straddles: a property of the set, so arrival order cannot change it.
 * A single-pass band machine cut table-row rects in all three engines because it could not withdraw an awarded band.
 */
const lastWholeLine = (region: ClipRegion): number => {
  let fits = 0;
  for (const edge of region.edges) {
    if (edge <= fits) {
      continue;
    }
    const cuts = region.ink.some(
      (i) => i.top < edge - SNAP_EPSILON && i.bottom > edge + SNAP_EPSILON,
    );
    if (!cuts) {
      fits = edge;
    }
  }
  return fits;
};

/**
 * Move the clip edge up to the last line box inside the stylesheet's cap by writing {@link CLAMP_H}. Measured, not
 * computed: markdown blocks carry different line metrics, and one `Range` returns a bounding box, not per-line rects.
 */
function snapClampToLine(text: HTMLElement): void {
  const styles = getComputedStyle(text);
  const nominal = Number.parseFloat(styles.maxBlockSize);
  if (!Number.isFinite(nominal)) {
    return;
  }
  const box = text.getBoundingClientRect();
  // `overflow: hidden` clips at the PADDING box, so the clip edge is the border
  // box's bottom less its own border. Read off rects rather than `clientHeight`,
  // which is rounded to an integer.
  const clipY = box.bottom - (Number.parseFloat(styles.borderBottomWidth) || 0);

  const fits = lastWholeLine(collectClipRegion(text, clipY));

  // Nothing fits: one line taller than the whole budget, which no clip height can
  // show whole. The stylesheet's own cap stands.
  if (fits <= 0) {
    return;
  }
  // Floored at zero so the written value never exceeds the stylesheet's cap; an edge just below the cap leaves it standing.
  const trim = Math.max(0, clipY - fits);
  if (trim <= SNAP_EPSILON) {
    return;
  }
  // The TRIM is written rather than the measured height, so the value is correct
  // under either `box-sizing`: shortening the cap by `trim` shortens the padding
  // box by `trim` whatever the box model counts.
  const height = nominal - trim;
  // A non-positive height is invalid at computed-value time, so `max-block-size` falls back to `none` and shows the
  // whole report. Unreachable by construction.
  if (!(height > 0)) {
    return;
  }
  text.style.setProperty(CLAMP_H, `${String(height)}px`);
}

function writeExpanded(text: HTMLElement, s: ClampState, on: boolean): void {
  s.setExpanded(on);
  if (on) {
    text.removeAttribute("data-clamped");
  } else {
    text.setAttribute("data-clamped", "");
  }
  s.more.textContent = on ? LABEL_LESS : LABEL_MORE;
  s.more.setAttribute("aria-expanded", on ? "true" : "false");
}

/**
 * Stop clamping `text` and release its observation. Precondition (the caller's): the element is being discarded;
 * a released live element that is re-attached wires a second click listener. Explicit because WebKit may never
 * deliver the final zero-size entry, and a parked view defers it.
 */
export function releaseClamp(text: HTMLElement): void {
  clampWatcher?.unobserve(text);
  observed.delete(text);
  clamps.delete(text);
  pendingReview.delete(text);
  if (pendingReview.size === 0 && reviewFrame !== 0) {
    cancelAnimationFrame(reviewFrame);
    reviewFrame = 0;
  }
}

/** Release every clamp inside `root`, `root` included. Same precondition as {@link releaseClamp}. */
export function releaseClampsIn(root: HTMLElement): void {
  for (const text of [...observed]) {
    if (root === text || root.contains(text)) {
      releaseClamp(text);
    }
  }
}

/** Number of elements the shared observer watches. Test-only. */
// deadset:ignore DS1004 -- test seam: observes the shared clamp observer's watched set
export function clampObservationCount(): number {
  return observed.size;
}

let clampWatcher: ResizeObserver | undefined;

/** A detached element measures 0; a resize callback lands after layout and before paint, so the
 *  first character guess is never painted. */
function watchClamp(text: HTMLElement): void {
  clampWatcher ??= new ResizeObserver((entries) => {
    for (const entry of entries) {
      // Fallback only; `releaseClamp` is the mechanism.
      if (!entry.target.isConnected) {
        releaseClamp(entry.target as HTMLElement);
        continue;
      }
      scheduleReview(entry.target as HTMLElement);
    }
  });
  clampWatcher.observe(text);
  observed.add(text);
}

const pendingReview = new Set<HTMLElement>();

let reviewFrame = 0;

/**
 * Deferred a frame: the verdict may not be written inside the resize delivery. `more.hidden` resizes the card, which
 * `scroll.ts` observes at a shallower depth, so the engine reports a ResizeObserver loop error. An unchanged-value
 * guard cannot replace the deferral.
 */
function scheduleReview(text: HTMLElement): void {
  pendingReview.add(text);
  if (reviewFrame !== 0) {
    return;
  }
  reviewFrame = requestAnimationFrame(() => {
    reviewFrame = 0;
    const due = [...pendingReview];
    pendingReview.clear();
    for (const target of due) {
      // A detached element measures 0 on both sides, so the character guess would
      // answer for one discarded between the delivery and this frame.
      if (!target.isConnected) {
        continue;
      }
      const found = clamps.get(target);
      if (found !== undefined) {
        reviewClamp(target, found.state);
      }
    }
  });
}

function countLines(s: string): number {
  let n = 1;
  for (const ch of s) {
    if (ch === "\n") {
      n++;
    }
  }
  return n;
}
