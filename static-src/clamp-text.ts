// The clamp: a text element capped to N lines, with a show-more that opens it.
//
// Serves the in-turn steer note, the run page's instructions and results, and
// both of the dock's question cards. Two clamps are NOT among them, because both
// are CSS-only and so carry no constant and attach no observation: the turn
// header's request, which is fold-conditional, and the dock's steer row, which
// offers no opener. The two measured facts that decide the shape are on
// `watchClamp` below.

/** Above this many characters, assume the text overflows when layout cannot be
 *  measured — a first guess only, corrected by the observer. Deliberately
 *  generous: a false positive shows an unneeded opener for one frame, a false
 *  negative makes a long text unreadable for one. */
const CLAMP_FALLBACK_CHARS = 220;

/** Lines a clamp shows when the caller states none. No consumer relies on it —
 *  every one of them states its own count — so this is a floor rather than any
 *  surface's number. */
const CLAMP_LINES = 3;

const LABEL_MORE = "Show more";
const LABEL_LESS = "Show less";

/** The custom property `reviewClamp` writes a measured clip height into, read by
 *  a stylesheet as `max-block-size: var(--clamp-h, <nominal>)`. `reviewClamp` is
 *  its ONE writer; on the expanded path the rule reading it is gated on
 *  `[data-clamped]`, so a leftover value is inert. */
const CLAMP_H = "--clamp-h";

/** Below this many px of overhang the clip is already on a line boundary, so no
 *  property is written. Sub-pixel, because a rect edge and a resolved
 *  `max-block-size` are both fractional. ONE constant serves the edge filter's
 *  tolerance, the straddle test's slack and this threshold, and the first two MUST be
 *  one: a slacker straddle test awards a value its own oracle calls a cut. */
const SNAP_EPSILON = 0.5;

export interface ClampOptions {
  /** Lines the STYLESHEET clamps to; read here by the character fallback only. */
  readonly lines?: number;
  /** Character threshold for the no-layout guess. */
  readonly fallbackChars?: number;
  /** Snap the collapsed clip DOWN to the last whole line, by measuring the
   *  rendered line boxes and writing {@link CLAMP_H}. Opt-in, and the default is
   *  off, because it is only correct for a clamp whose stylesheet reads that
   *  property as a height: writing a `max-block-size` onto a `-webkit-line-clamp`
   *  box would double-clip and could itself cut mid-line. The one consumer is the
   *  run page's results box, the only clamp in the app over a markdown bubble's
   *  BLOCK children — see `reviewClamp` for why no fixed cap can land on a line
   *  boundary there. */
  readonly snapToLine?: boolean;
  /** Where the caller keeps the expanded flag, when it keeps one. */
  readonly isExpanded?: () => boolean;
  readonly setExpanded?: (on: boolean) => void;
}

export interface ClampHandle {
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

/** Every element the shared observer is watching. No new retention: the observer already
 *  holds each target strongly. It exists because a WeakMap cannot be enumerated and a
 *  subtree sweep has to be. */
const observed = new Set<HTMLElement>();

/** Clamp `text` behind `more`, and return the handle a repaint drives it with.
 *
 *  Idempotent: a repeat call returns the existing handle rather than wiring a
 *  second listener, so a caller holding only the element re-derives its handle.
 *
 *  Where the expanded flag is STORED stays the caller's: `turn-header.ts` keeps it
 *  on the header so a user expansion survives a repaint. */
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

/** Decide whether the opener is needed, and keep the clamp attribute in sync.
 *  Measurement is the truth when layout is available; the character fallback
 *  covers the no-layout case, so a long text is never clamped with no way to open
 *  it. */
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

  // EVERY REVIEW MEASURES AGAINST THE STYLESHEET'S OWN CAP, so a previous snap is
  // withdrawn first. Left standing it becomes the next review's budget, and each
  // pass then snaps to the last line inside the PREVIOUS snap — losing a line for
  // good on the first resize.
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
  // Gated on the OVERFLOW verdict, not merely on the mechanism: a report that fits
  // has nothing to clip, and a height written for it is one that can only make
  // `scrollHeight > clientHeight` true by a fraction and offer a Show more over
  // nothing to reveal.
  if (s.snapToLine && overflows && measured > 0 && visible > 0) {
    snapClampToLine(text);
  }
}

/** The rendered geometry one clip decision is made from, bounded to the clip region.
 *  `ink` holds only rects that START above the clip: straddling a position `y` needs
 *  `top < y - EPS` and every position considered is at or above the clip, so a rect
 *  beginning below it can straddle nothing — which is what bounds the store by the
 *  budget rather than by the report's length. */
interface ClipRegion {
  readonly ink: readonly { readonly top: number; readonly bottom: number }[];
  readonly edges: readonly number[];
}

/** Collect that region in ONE walk, bounded by a sibling stop: a child list stops at the
 *  first element with a box whose top is more than `EPS` below the deepest ink read so far.
 *  An element carrying a rect the answer depends on has a box top at or above that rect, so
 *  the stop cannot fire for it, and `readTo` is what keeps it from firing mid-row — inside a
 *  shared line the ink already read sits at or below the siblings' tops. Both directions of
 *  the residual are bounded: a rect is lost only where an element's box does not contain its
 *  own content, and a lost EDGE only ever trims more. Prune-only needs no sibling assumption
 *  and cost 3.7x on a 540-line report, so the bounded walk is what ships. */
const collectClipRegion = (text: HTMLElement, clipY: number): ClipRegion => {
  const ink: { top: number; bottom: number }[] = [];
  /** Candidate clip positions: the bottom of every rendered rect, and of every element that
   *  rendered NO text and the walk did not stop inside — an `<img>`, an `<hr>`, an empty
   *  fence, which a text-only walk trims back past (measured 16.4/27.4/35.4px in Chromium).
   *  The zero-height test on those is live: an empty paragraph paints nothing but still has a
   *  position, and awarding it moves the clip 11.4/9.7/10.7px into a margin gap. An image
   *  that gains its box after this review keeps the pre-fix clip until the next one. */
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

/** The greatest edge no rendered rect strictly straddles — the whole model, and an answer
 *  about the SET rather than an event in a traversal, so no arrival order can change it.
 *  The single-pass band machine it replaces cut a rendered rect on a table row in all three
 *  engines (67/66/66 of 564 samples, up to 9.91px into a 16px line box, 83-95% of that
 *  line's ink lost) because an awarded band bottom could not be withdrawn by a rect read
 *  later. The `edge <= fits` skip is an optimisation over a max, not a semantic. */
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

/** Move the clip edge UP to the bottom of the last line box that fits inside the
 *  stylesheet's own cap, by writing {@link CLAMP_H}.
 *
 *  MEASURED, NOT COMPUTED: a fixed cap lands on a line boundary only for content of one
 *  line metric, and this content is a markdown bubble whose block children each carry
 *  their own (13/16/17px ink boxes plus 8px margins in one measured report). A single
 *  `Range` over the box returns its bounding box rather than per-line rects — 1 rect
 *  where the walk returns 20 — so the walk is required. */
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
  // FLOORED AT ZERO, so the written value can never EXCEED the stylesheet's own cap —
  // a negative trim would open the box past the budget it is snapping inside. Its LIVE
  // arm is the edge filter's own `+ EPS`: an edge a fraction BELOW the cap floors to 0
  // and the next check then leaves the cap standing, which is what keeps the twelfth
  // line of uniform text visible. Removing the floor changes no measured behaviour,
  // because `trim <= SNAP_EPSILON` returns on that arm anyway.
  const trim = Math.max(0, clipY - fits);
  if (trim <= SNAP_EPSILON) {
    return;
  }
  // The TRIM is written rather than the measured height, so the value is correct
  // under either `box-sizing`: shortening the cap by `trim` shortens the padding
  // box by `trim` whatever the box model counts.
  const height = nominal - trim;
  // A NON-POSITIVE height is invalid at computed-value time, so `max-block-size`
  // would fall back to its initial `none` and the collapsed box would render the
  // WHOLE report — the outcome `31-exec-view.css`'s comment cites against
  // `-webkit-line-clamp`. Failing open is the right direction for this surface, so
  // this is not an error to raise; it is simply made unreachable. Reaching it needs a
  // fitting box above the clamp box's own top, which no report shape produces.
  if (!(height > 0)) {
    return;
  }
  text.style.setProperty(CLAMP_H, `${String(height)}px`);
}

/** Open or close the clamp, and hand the flag to whoever stores it. */
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

/** Stop clamping `text` and release its observation. PRECONDITION, the caller's: the
 *  element is being DISCARDED, so release at a teardown and never at a repaint.
 *  `attachClamp` is idempotent through the `clamps` entry, so releasing a LIVE element
 *  that is re-attached later wires a SECOND click listener and one press toggles twice.
 *  Explicit rather than callback-driven because WebKit may never deliver the final
 *  zero-size entry, and a parked view's `content-visibility: hidden` defers it while an
 *  EVICTED view never un-parks. */
export function releaseClamp(text: HTMLElement): void {
  clampWatcher?.unobserve(text);
  observed.delete(text);
  clamps.delete(text);
  // A discarded element leaves no scheduled write behind either, which is the same
  // precondition the observation itself has.
  pendingReview.delete(text);
  if (pendingReview.size === 0 && reviewFrame !== 0) {
    cancelAnimationFrame(reviewFrame);
    reviewFrame = 0;
  }
}

/** Release every clamp inside `root`, `root` itself included. Same precondition as
 *  {@link releaseClamp}. A sweep rather than a per-element call keeps the owner count at
 *  one per teardown, and lets `disposeChatView` cover every steer note in a chat. */
export function releaseClampsIn(root: HTMLElement): void {
  for (const text of [...observed]) {
    if (root === text || root.contains(text)) {
      releaseClamp(text);
    }
  }
}

/** How many elements the shared observer is watching. Test-only: `knip.json` treats every
 *  `*.test.ts` as an entry, so a test-consumed export is not an unused one. */
export function clampObservationCount(): number {
  return observed.size;
}

let clampWatcher: ResizeObserver | undefined;

/** One observer for every clamped element, because measurement is the only honest
 *  answer to "does this overflow N lines".
 *
 *  A DETACHED element measures 0 on both sides, so the first verdict can only be
 *  the character guess, and nothing revisited it: a settled block gets no repaint.
 *  A resize callback is delivered AFTER layout and BEFORE paint, so the first
 *  lands in the insertion frame and the guess is never painted — and it lands at
 *  every width, which measure-once could not. */
function watchClamp(text: HTMLElement): void {
  clampWatcher ??= new ResizeObserver((entries) => {
    for (const entry of entries) {
      // BELT AND BRACES, not the mechanism: an element discarded without a
      // `releaseClamp` is swept if this callback happens to arrive. See `releaseClamp`.
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

/** Elements whose resize-driven review is waiting on `reviewFrame`. A set, because one
 *  callback carries every element whose box moved. */
const pendingReview = new Set<HTMLElement>();

/** The single slot the deferred review is held in. */
let reviewFrame = 0;

/** Defer the resize-driven review one animation frame, behind a single slot.
 *
 *  THE VERDICT MAY NOT BE WRITTEN INSIDE THE RESIZE DELIVERY. `more.hidden` is
 *  `display: none`, so a flip changes the height of the card the opener sits in, and
 *  that card is observed at a SHALLOWER depth by `scroll.ts`'s per-child set: both are
 *  gathered into ONE broadcast, so re-activating the card's observation fails the
 *  `depth > shallowest` test and the engine reports "ResizeObserver loop completed with
 *  undelivered notifications". An unchanged-value guard cannot stand in for the
 *  deferral — `hidden = false` on an unhidden element already dirties nothing, so the
 *  write that reaches layout is a real state change. Same shape as `scroll.ts`'s
 *  `scheduleScrollbarWidth`; the synchronous `reviewClamp` calls in `attachClamp` and
 *  on the handle are outside any delivery and are what paint the first guess. */
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
