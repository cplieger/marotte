// The one owner of a transcript scroller's position. No import-time state: a second follower imports this module,
// never scroll.ts, which binds the chat's controller at load.

import { el } from "@cplieger/ui";
import { snapIconsIn } from "./icon-crisp.js";
import {
  ENTRY_STAMP,
  layoutDrift,
  lineDrift,
  readingAnchor,
  readingLineOffset,
  resolveAnchor,
  viewOffset,
  type AnchorScope,
  type ReadingAnchor,
} from "./reading-anchor.js";
import { edgeSlack, GestureTracker, type Aim } from "./scroll-gesture.js";
import { loadMoreSkeleton } from "./skeleton.js";

export type { ReadingAnchor } from "./reading-anchor.js";

/** Air under the live block's bottom when Following pins it above the scrollport's bottom edge. */
const LIVE_ANCHOR_AIR_PX = 50;
/** How long the owner's own smooth jump may fly with no `scrollend`: one animation and its settle. */
const FLIGHT_MAX_MS = 1500;
/** The transcript's `content-visibility: auto` boxes (`transcript-layout-css.test.ts` `BULK`). */
const SKIPPABLE = ".msg-row, .tool-call, .subagent-block, .plan-message, .run-card";
/** How far past the scrollport, in scrollport heights, a skippable box is laid out ahead (`[data-near]`). Chromium's
 *  own lead (https://chromium.googlesource.com/chromium/src/+/main/third_party/blink/renderer/core/display_lock/display_lock_document_state.cc);
 *  WebKit's has no margin and lands a frame late
 *  (https://github.com/WebKit/WebKit/blob/main/Source/WebCore/dom/ContentVisibilityDocumentState.cpp). Applied in
 *  pixels: the spec resolves a rootMargin percentage against the root's WIDTH
 *  (https://w3c.github.io/IntersectionObserver/#intersectionobserver-root-intersection-rectangle). */
const NEAR_LEAD = 1.5;
/** Consecutive frames a settle or a press window must see nothing to do before it lets go. */
const RESTORE_HOLD_FRAMES = 2;
/** The frames a settle or a press window may spend before it gives up. */
const RESTORE_MAX_FRAMES = 60;
/** View children either side of the reading line an anchor carries as fallbacks: it is re-taken at every owner move
 *  and reader scroll event, so it only has to outlive one layout change. */
const LIVE_REACH = 2;
const SKELETON_ID = "load-more-skeleton";

function forEachSkippable(root: Element, fn: (box: Element) => void): void {
  if (root.matches(SKIPPABLE)) {
    fn(root);
  }
  for (const box of root.querySelectorAll(SKIPPABLE)) {
    fn(box);
  }
}

/** The reader's position. */
export type ReadingState = "following" | "reading";

/** A hold's write decision. `none` keeps the anchor and its remainder; `clamped` means the clamp left no room, so
 *  the caller re-takes; `write` moves to `top`, re-taking after it when `clamped`. */
export type HoldTarget =
  { kind: "none" } | { kind: "clamped" } | { kind: "write"; top: number; clamped: boolean };

/** Where a hold of `drift` px from `before` should land on a scroller whose end is `max`. Engines land a write on a
 *  whole CSS pixel at every DPR, so the target is `Math.round`; the gate is the drift itself, so a fractional rest
 *  position with nothing moved never writes. */
export function holdTarget(before: number, drift: number, max: number): HoldTarget {
  const u = Math.round(before + drift);
  const t = Math.max(0, Math.min(u, max));
  if (Math.abs(drift) < 0.5 || u === before) {
    return { kind: "none" };
  }
  if (t === before && t !== u) {
    return { kind: "clamped" };
  }
  return { kind: "write", top: t, clamped: t !== u };
}

/** A parked view's scroll-owned state: where the scroller stood, which reading state the reader was in and, while
 *  Reading, the text they were reading. */
export interface ViewScrollState {
  scrollTop: number;
  readingState: ReadingState;
  anchor?: ReadingAnchor;
}

/** What `attach` needs: the incoming view element (the observers' new root) plus the state to restore into it. */
export interface ViewAttachHandle extends ViewScrollState {
  el: HTMLElement;
}

/** The reader's gesture as the state rules read it: where it started and the end it was let reach. */
interface GestureRead {
  base: number;
  downMax: number | null;
  last: { t: number; pin: number; max: number } | null;
}

export class ScrollController {
  readonly scrollEl: HTMLElement;

  /** The observers' root: the attached transcript view, or the multiplexer itself before any view attaches. */
  private viewEl: HTMLElement;
  /** Whether `viewEl` came from `attach`: only an attached view holds the clamp floor. */
  private attached = false;

  private state: ReadingState = "following";
  private readonly gesture: GestureTracker;
  private read: GestureRead | null = null;

  /** The text on the reading line, held still while Reading and through a Following gesture; null holds nothing. */
  private anchor: ReadingAnchor | null = null;

  /** Where the owner's last write landed, consumed by the scroll event it produced or cleared two frames later. */
  private ownWrite: number | null = null;
  private ownWriteGen = 0;
  /** The previous scroll event's clamped position (-1 = none yet). */
  private lastT = -1;

  private flight: {
    timer: ReturnType<typeof setTimeout>;
    seen: number;
    done: ((landed: boolean) => void)[];
    /** Where the flight's target stands now, re-read at a landing. */
    target: (() => number | null) | null;
  } | null = null;
  private settle: { held: number; frames: number; frame: number } | null = null;
  /** Open from a press until its layout settles; `from` is the state `pressHold` left, null when none. */
  private press: {
    still: number;
    frames: number;
    frame: number;
    from: ReadingState | null;
  } | null = null;
  private pressResized = false;

  private hasMoreMessages = false;
  private loadingMore = false;
  private onLoadMore: (() => void) | null = null;
  private pendingLoad: (() => void) | null = null;

  /** Mutations postponed while Reading, applied in arrival order on the return to Following. */
  private deferred: (() => void)[] = [];
  private stateListeners: ((s: ReadingState) => void)[] = [];
  private mutateListeners: (() => void)[] = [];
  private readerGestureListeners: (() => void)[] = [];
  private contentResizeListeners: (() => void)[] = [];
  private attachListeners: (() => void)[] = [];
  private liveEdgeListeners: (() => void)[] = [];
  /** Dispatched for the owner's own writes too: a jump moves the reader a long way and the residency window follows. */
  private viewportListeners: (() => void)[] = [];
  private viewportFrame = 0;
  private anchorProvider: (() => HTMLElement | null) | null = null;
  private followFrame = 0;

  /** The floor's held block size (null = none) and the frame that lowers it after a shrink (0 = none). */
  private floorPx: number | null = null;
  private lowerFrame = 0;
  /** Set when a shrink may have left space under the floor, so `max()` reads the content's own end. */
  private floorStale = false;
  /** Whether the owner's content delivery, or a microtask it queued, is running. */
  private delivering = false;
  private childSize = new WeakMap<Element, number>();

  private scrollbarWidth = "";
  private gutterFrame = 0;

  private contentObserver: MutationObserver | null = null;
  private childObserver: MutationObserver | null = null;
  private resizeObserver: ResizeObserver | null = null;
  private gutterObserver: ResizeObserver | null = null;
  private nearObserver: IntersectionObserver | null = null;
  private nearTargets = new Set<Element>();
  /** The skippable boxes currently `[data-near]`, the rows whose icons a layout move re-snaps. */
  private nearNow = new Set<Element>();
  private nearMargin = -1;
  private observedChildren = new Set<Element>();

  private topSentinel: HTMLElement | null = null;
  private topObserver: IntersectionObserver | null = null;
  private topNear = false;

  private readonly resumeBtn: () => HTMLElement;
  private readonly publishesGutter: boolean;
  private readonly scope: AnchorScope;

  constructor(
    messagesEl: HTMLElement,
    scrollEl: HTMLElement,
    resumeBtn: () => HTMLElement,
    publishesGutter: boolean,
  ) {
    this.scrollEl = scrollEl;
    this.viewEl = messagesEl;
    this.resumeBtn = resumeBtn;
    this.publishesGutter = publishesGutter;
    this.scope = {
      scroll: scrollEl,
      view: () => this.viewEl,
      furniture: (c) => c === this.topSentinel || c.id === SKELETON_ID,
    };
    this.gesture = new GestureTracker(scrollEl, () => this.viewEl, {
      opened: (aim) => {
        this.gestureOpened(aim);
      },
      aimed: (aim) => {
        if (aim > 0 && this.read !== null) {
          this.read.downMax = this.max();
        }
      },
      ended: () => {
        this.gestureEnded();
      },
      atEndDown: () => {
        this.setState("following");
        this.correct();
      },
      rejoinable: () =>
        this.state === "reading" && this.inRejoinWindow(this.clampedTop(), this.max()),
      pressed: () => {
        this.openPress(null);
      },
      endKey: () => {
        if (this.state === "reading") {
          this.resume();
        }
      },
      endTop: () => this.max(),
    });
  }

  init(): void {
    // The owner holds the reader's text; engine anchoring (https://drafts.csswg.org/css-scroll-anchoring/) would be a
    // second writer it cannot see, and every hold assumes it is off.
    this.scrollEl.style.overflowAnchor = "none";
    this.scrollEl.setAttribute("data-scroll-owner", "");
    this.publishScrollbarWidth();
    this.gesture.listen();
    this.scrollEl.addEventListener(
      "scroll",
      () => {
        this.onScroll();
      },
      { passive: true },
    );
    this.scrollEl.addEventListener(
      "scrollend",
      () => {
        // The flight's own end, never an owner write's, which fires one in its own frame.
        const f = this.flight;
        if (f !== null && f.seen > 0 && !this.gesture.ownWriteRecent()) {
          this.endFlight(true);
        }
      },
      { passive: true },
    );
    this.resumeBtn().addEventListener("click", () => {
      this.resume();
    });

    this.contentObserver = new MutationObserver((records) => {
      this.settleNear(records);
      this.queueFollowFrame();
      for (const cb of this.mutateListeners) {
        cb();
      }
    });
    this.topSentinel = el("div", { className: "transcript-top", "aria-hidden": "true" });
    this.resizeObserver = new ResizeObserver((entries) => {
      this.onChildResize(entries);
    });
    this.gutterObserver = new ResizeObserver(() => {
      this.scheduleScrollbarWidth();
      this.fitNearObserver();
      // The reading line is a fraction of the scrollport, so a resize moves the line and not the text.
      if (this.state === "reading") {
        if (this.press === null) {
          this.retake();
        }
      } else {
        this.correct();
      }
      this.floorAfter(true);
    });
    this.gutterObserver.observe(this.scrollEl);
    this.fitNearObserver();
    this.childObserver = new MutationObserver(() => {
      if (this.reobserveChildren() > 0) {
        this.floorStale = true;
        this.correct();
        this.floorAfter(!this.delivering);
      }
    });
    this.observeView(this.viewEl);
  }

  // ---- attribution ----

  /** Ordering invariant: per HTML "update the rendering"
   *  (https://html.spec.whatwg.org/multipage/webappapis.html#update-the-rendering), scroll events are dispatched
   *  before animation-frame callbacks and resize observations, so a reader's move is attributed and its anchor
   *  re-taken before any correction of the same frame reads the position. */
  private onScroll(): void {
    const max = this.max();
    const t = this.clampedTop();
    const prev = this.lastT;
    this.lastT = t;
    this.dispatchViewportChange();
    const slack = edgeSlack();
    if (this.ownWrite !== null && Math.abs(t - this.ownWrite) <= slack) {
      this.ownWrite = null;
      return;
    }
    if (this.flight !== null) {
      this.flight.seen++;
      return;
    }
    if (this.settle !== null) {
      this.settleStep();
      return;
    }
    // A clamp: with native anchoring off and the floor held, only a growing scrollport moves the scroller up onto
    // its end with no owner write, so this effect alone names it.
    if (prev >= 0 && t < prev && t >= max - slack) {
      // Never a reader move, but inside a gesture it shows the platform still moving it.
      if (this.gesture.isOpen()) {
        this.gesture.noteScroll();
      }
      return;
    }
    if (!this.gesture.isOpen()) {
      if (prev < 0 || Math.abs(t - prev) < slack) {
        return;
      }
      this.gesture.openAimless();
      if (this.read !== null) {
        this.read.base = prev;
      }
    }
    this.gesture.noteScroll();
    this.onReaderScroll(t, max);
  }

  /** A reader scroll event: hold first, so a size change of this frame is not re-taken as the reader's position. */
  private onReaderScroll(t: number, max: number): void {
    this.endPress();
    if (this.state === "reading" || this.gesture.isOpen()) {
      this.holdAnchor();
    }
    const now = this.clampedTop();
    const read = this.read ?? { base: t, downMax: null, last: null };
    this.read = read;
    const slack = edgeSlack();
    const pin = this.followPosition();
    if (this.state === "following") {
      const up = now < read.base - slack;
      const belowPin = pin < max - slack && now > pin + slack;
      if (up || belowPin) {
        this.setState("reading");
        read.base = now;
      }
    }
    read.last = { t: now, pin, max };
    this.retake();
    this.publishReaderGesture();
  }

  private gestureOpened(aim: Aim): void {
    this.endFlight(false);
    this.endSettle();
    // The last reported position, not the current one: a threaded scroller applies the tick before the input event
    // is dispatched, and its scroll event is still to come.
    const base = this.lastT >= 0 ? this.lastT : this.clampedTop();
    this.read = { base, downMax: aim > 0 ? this.max() : null, last: null };
    if (this.state === "following") {
      this.retake();
    }
  }

  /** At a gesture's end the reader rejoins when its last scroll event landed in the follow window; any follow
   *  correction the gesture held off runs now. */
  private gestureEnded(): void {
    const read = this.read;
    this.read = null;
    if (this.state === "reading" && read?.last != null) {
      const { t, pin, max } = read.last;
      const reach = read.downMax === null ? max : Math.min(read.downMax, max);
      const slack = edgeSlack();
      if (Math.min(pin, reach) - slack <= t && t <= pin + slack) {
        this.setState("following");
      }
    }
    if (this.state === "following") {
      this.anchor = null;
      this.correct();
    }
  }

  /** Whether a reader at `t` sits where a return to Following belongs: on the pin, or on an end the platform let them
   *  reach (`reach`) when the pin is the end. */
  private inRejoinWindow(t: number, reach: number): boolean {
    const pin = this.followPosition();
    const slack = edgeSlack();
    return Math.min(pin, reach) - slack <= t && t <= pin + slack;
  }

  // ---- corrections ----

  /** What the current state needs, written through `writeTop` or `shiftTop`. Every correction site calls this, so its
   *  gates are the only ones: no follow write while a gesture or flight is open, and no hold while a flight or press
   *  window is open. Resize-loop invariant: a correction inside a resize delivery moves no farther than the size
   *  change it answers, so it reveals only boxes the near lead has laid out (`ro-loop-gate.ts` fails a loop). */
  private correct(): void {
    if (this.flight !== null) {
      return;
    }
    if (this.state === "following") {
      if (this.gesture.isOpen()) {
        // The follow write waits for the gesture's end; until then the text the reader sees is held, pin air included.
        this.holdAnchor();
        return;
      }
      this.writeTop(this.followPosition());
      return;
    }
    if (this.press === null) {
      this.holdAnchor();
    }
  }

  /** Hold the anchor's text against layout drift. In Reading, every owner move either books into `takenTop` (this
   *  hold) or is followed by `retake()`; the settle write is the other exception, because it undoes a platform scroll
   *  and leaves the text's content position unchanged. A new owner move that does neither is read as layout drift. */
  private holdAnchor(): void {
    const a = this.anchor;
    if (a === null) {
      this.retake();
      return;
    }
    const drift = layoutDrift(this.scope, a);
    if (drift === null) {
      this.retake();
      return;
    }
    const r = this.shiftTop(drift);
    if (r.retake) {
      this.retake();
      return;
    }
    a.takenTop += r.moved;
    if (this.state === "following" && this.read !== null) {
      this.read.base += r.moved;
    }
  }

  /** The one way an anchor is taken: the text on the reading line while Reading or during a Following gesture. */
  private retake(): void {
    this.anchor =
      this.state === "reading" || this.gesture.isOpen()
        ? readingAnchor(this.scope, LIVE_REACH)
        : null;
  }

  /** The absolute write: skipped within `edgeSlack()` of where the scroller stands, so a half-pixel residue at the
   *  end is never rewritten. `"instant"` overrides any stylesheet `scroll-behavior`. */
  private writeTop(target: number): boolean {
    const t = Math.max(0, Math.min(Math.round(target), this.max()));
    if (Math.abs(t - this.scrollEl.scrollTop) <= edgeSlack()) {
      return false;
    }
    this.scrollEl.scrollTo({ top: t, behavior: "instant" });
    this.noteOwnWrite();
    return true;
  }

  /** The relative write for holds and the settle: returns the observed move in `viewOffset` terms, and whether the
   *  clamp changed the target so the caller must re-take. */
  private shiftTop(drift: number): { moved: number; retake: boolean } {
    const r = holdTarget(this.scrollEl.scrollTop, drift, this.max());
    if (r.kind === "none") {
      return { moved: 0, retake: false };
    }
    if (r.kind === "clamped") {
      return { moved: 0, retake: true };
    }
    const before = viewOffset(this.scope);
    this.scrollEl.scrollTo({ top: r.top, behavior: "instant" });
    this.noteOwnWrite();
    return { moved: viewOffset(this.scope) - before, retake: r.clamped };
  }

  /** Record the landing of a move the owner made, so the scroll event it produces is not read as the reader. */
  private noteOwnWrite(): void {
    this.ownWrite = this.clampedTop();
    this.lastT = this.ownWrite;
    const gen = ++this.ownWriteGen;
    this.gesture.noteOwnWrite();
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        if (gen === this.ownWriteGen) {
          this.ownWrite = null;
        }
      });
    });
  }

  private clampedTop(): number {
    return Math.max(0, Math.min(this.scrollEl.scrollTop, this.max()));
  }

  /** The scroller's end: under a floor a shrink has not yet been answered for, the content's own end. */
  private max(): number {
    const max = this.scrollEl.scrollHeight - this.scrollEl.clientHeight;
    return Math.max(0, this.floorStale ? max - this.floorExcess() : max);
  }

  /** Where Following belongs: the live block's pin, or the end. */
  private followPosition(): number {
    return Math.max(0, Math.min(Math.round(this.followTarget()), this.max()));
  }

  private followTarget(): number {
    const live = this.anchorProvider?.() ?? null;
    const box = live === null ? null : this.scrollFrameRect(live);
    if (box === null) {
      return this.scrollEl.scrollHeight;
    }
    return box.bottom - this.scrollEl.clientHeight + LIVE_ANCHOR_AIR_PX;
  }

  private onChildResize(entries: readonly ResizeObserverEntry[]): void {
    this.delivering = true;
    this.pressResized = true;
    let shrank = false;
    let from: Element | null = null;
    for (const e of entries) {
      const size = e.borderBoxSize[0]?.blockSize ?? e.contentRect.height;
      const prev = this.childSize.get(e.target);
      if (prev !== undefined && size < prev) {
        shrank = true;
      }
      this.childSize.set(e.target, size);
      if (
        from === null ||
        from.compareDocumentPosition(e.target) & Node.DOCUMENT_POSITION_PRECEDING
      ) {
        from = e.target;
      }
    }
    if (shrank) {
      this.floorStale = true;
    }
    this.correct();
    this.floorAfter(false);
    if (from !== null) {
      this.snapMoved(from);
    }
    for (const cb of this.contentResizeListeners) {
      cb();
    }
    queueMicrotask(() => {
      this.delivering = false;
    });
  }

  /** Re-snap the icons a layout move under `from` carried: the near band's rows at or after it, and their turns' own
   *  header and footer icons. A row outside the band is never read; its icons are not painted. */
  private snapMoved(from: Element): void {
    const rows = this.inSnapReach(
      [...this.nearNow].filter(
        (box) =>
          from.contains(box) ||
          (from.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0,
      ),
    );
    if (rows.length > 0) {
      snapIconsIn(rows, this.cardsOf(rows), SKIPPABLE);
    }
  }

  /** The boxes whose top is above the scrollport's bottom edge plus one scrollport, which bounds the walk's cost by the
   *  viewport. A row below keeps its last snap until a later delivery reaches it. */
  private inSnapReach(boxes: Element[]): Element[] {
    const limit = this.scrollEl.getBoundingClientRect().bottom + this.scrollEl.clientHeight;
    return boxes.filter((box) => box.getBoundingClientRect().top <= limit);
  }

  private cardsOf(boxes: Iterable<Element>): Set<Element> {
    const cards = new Set<Element>();
    for (const box of boxes) {
      let c: Element | null = box;
      while (c !== null && c.parentElement !== this.viewEl) {
        c = c.parentElement;
      }
      if (c !== null) {
        cards.add(c);
      }
    }
    return cards;
  }

  // ---- the clamp floor ----

  /** While the attached view overflows, hold `min-block-size` at its size, so a shrink in any task cannot clamp the
   *  scroller before the owner answers it. Raising never changes the view's box, so it is safe in any delivery. */
  private holdFloor(): void {
    if (!this.attached) {
      return;
    }
    const view = this.viewEl;
    if (this.scrollEl.scrollHeight <= this.scrollEl.clientHeight) {
      this.clearFloor();
      return;
    }
    const size = view.getBoundingClientRect().height;
    if (this.floorPx !== size) {
      view.style.minBlockSize = `${String(size)}px`;
      this.floorPx = size;
    }
    view.setAttribute("data-scroll-floor", "");
  }

  private clearFloor(): void {
    this.cancelLower();
    this.floorStale = false;
    this.floorPx = null;
    this.viewEl.style.minBlockSize = "";
    this.viewEl.removeAttribute("data-scroll-floor");
  }

  /** After a correction: lower the floor when a shrink left space under it, else hold it at the view's size. Lowering
   *  changes the view's own box, so inside the content delivery it waits for the next frame, where no observer of the
   *  view is skipped; the correction already ran against the content's own end, so the lowering clamps nothing. */
  private floorAfter(now: boolean): void {
    if (this.floorPx === null || !this.floorStale) {
      this.holdFloor();
      return;
    }
    if (this.floorExcess() < 0.01) {
      this.floorStale = false;
      this.holdFloor();
      return;
    }
    if (now) {
      this.lowerFloor();
      return;
    }
    // The lowering's clamp, made now so the paint shows no free space under the content. A view that will fit is
    // packed at the bottom, as the lowered layout will be, and shown at the floored end.
    const fits = this.naturalSize() <= this.scrollEl.clientHeight;
    let end = this.max();
    if (fits) {
      this.viewEl.removeAttribute("data-scroll-floor");
      end = this.scrollEl.scrollHeight - this.scrollEl.clientHeight;
    }
    if (this.scrollEl.scrollTop > end + 0.5 || (fits && this.scrollEl.scrollTop < end - 0.5)) {
      const before = viewOffset(this.scope);
      this.scrollEl.scrollTo({ top: end, behavior: "instant" });
      this.noteOwnWrite();
      if (this.state === "reading" && this.press === null) {
        this.retake();
      } else if (this.read !== null) {
        this.read.base += viewOffset(this.scope) - before;
      }
    }
    if (this.lowerFrame === 0) {
      this.lowerFrame = requestAnimationFrame(() => {
        this.lowerFrame = 0;
        this.lowerFloor();
      });
    }
  }

  private cancelLower(): void {
    if (this.lowerFrame !== 0) {
      cancelAnimationFrame(this.lowerFrame);
      this.lowerFrame = 0;
    }
  }

  /** Clear the floor and hold it again at the lowered size. A clamp the lowering causes is the owner's move: recorded
   *  as its write and, in Reading, followed by a re-take, so no hold pays it back later. */
  private lowerFloor(): void {
    this.cancelLower();
    this.floorStale = false;
    if (this.floorPx === null) {
      return;
    }
    const before = viewOffset(this.scope);
    this.floorPx = null;
    this.viewEl.style.minBlockSize = "";
    if (this.scrollEl.scrollHeight <= this.scrollEl.clientHeight) {
      this.viewEl.removeAttribute("data-scroll-floor");
    }
    const moved = viewOffset(this.scope) - before;
    if (Math.abs(moved) > 0.01) {
      this.noteOwnWrite();
      if (this.state === "reading" && this.press === null) {
        this.retake();
      } else if (this.read !== null) {
        this.read.base += moved;
      }
    }
    this.holdFloor();
  }

  /** How much of the held floor the content no longer fills. */
  private floorExcess(): number {
    if (this.floorPx === null) {
      return 0;
    }
    return Math.max(0, this.viewEl.getBoundingClientRect().height - this.naturalSize());
  }

  /** The view's size without the floor: its last laid-out child's bottom, plus that child's margin and the view's own
   *  end padding and border. The floor packs the content at the start, so the children stand where they would. */
  private naturalSize(): number {
    const view = this.viewEl;
    const top = view.getBoundingClientRect().top;
    let bottom = top;
    for (let c = view.lastElementChild; c !== null; c = c.previousElementSibling) {
      if (c.getClientRects().length > 0 && c !== this.topSentinel) {
        bottom =
          c.getBoundingClientRect().bottom + (parseFloat(getComputedStyle(c).marginBlockEnd) || 0);
        break;
      }
    }
    const cs = getComputedStyle(view);
    return (
      bottom -
      top +
      (parseFloat(cs.paddingBlockEnd) || 0) +
      (parseFloat(cs.borderBlockEndWidth) || 0)
    );
  }

  // ---- the follow frame ----

  /** While Following, one frame after a transcript mutation or a follow-target change: the live anchor can change
   *  identity (a seal only clears a class) with no size change. At most one per frame. */
  private queueFollowFrame(): void {
    if (this.state !== "following" || this.followFrame !== 0) {
      return;
    }
    this.followFrame = requestAnimationFrame(() => {
      this.followFrame = 0;
      if (this.state === "following") {
        this.correct();
      }
    });
  }

  /** The anchor provider's answer may have changed: while Following, re-read it in the next frame. */
  followTargetChanged(): void {
    this.queueFollowFrame();
  }

  // ---- settle and press windows ----

  private openSettle(): void {
    this.endSettle();
    this.settle = { held: 0, frames: 0, frame: 0 };
    this.queueSettle();
  }

  private queueSettle(): void {
    const s = this.settle;
    if (s?.frame !== 0) {
      return;
    }
    s.frame = requestAnimationFrame(() => {
      s.frame = 0;
      if (this.settle !== s) {
        return;
      }
      s.held = this.settleStep() ? 0 : s.held + 1;
      s.frames++;
      if (s.held >= RESTORE_HOLD_FRAMES || s.frames >= RESTORE_MAX_FRAMES) {
        this.endSettle();
        return;
      }
      this.queueSettle();
    });
  }

  /** Put back a position the platform moved with no size report (WebKit applying a write one rendering update late
   *  after a view becomes visible, #1723); true when it wrote. */
  private settleStep(): boolean {
    if (this.state === "following") {
      return this.writeTop(this.followPosition());
    }
    const a = this.anchor;
    const drift = a === null ? null : lineDrift(this.scope, a);
    if (drift === null) {
      return false;
    }
    const r = this.shiftTop(drift);
    if (r.retake) {
      this.retake();
    }
    return r.moved !== 0;
  }

  private endSettle(): void {
    const s = this.settle;
    this.settle = null;
    if (s !== null && s.frame !== 0) {
      cancelAnimationFrame(s.frame);
    }
  }

  /** A press drops the anchor until its layout settles, so what a click opens is not pushed back. */
  private openPress(from: ReadingState | null): void {
    if (this.press === null) {
      this.press = { still: 0, frames: 0, frame: 0, from };
      this.anchor = null;
      this.pressResized = false;
      this.queuePress();
    } else if (from !== null) {
      this.press.from = from;
    }
  }

  private queuePress(): void {
    const p = this.press;
    if (p?.frame !== 0) {
      return;
    }
    p.frame = requestAnimationFrame(() => {
      p.frame = 0;
      if (this.press !== p) {
        return;
      }
      // A slide the press started can sit pending, or in its delay, for frames that resize nothing.
      p.still = this.pressResized || this.viewAnimating() ? 0 : p.still + 1;
      this.pressResized = false;
      p.frames++;
      if (p.still >= RESTORE_HOLD_FRAMES || p.frames >= RESTORE_MAX_FRAMES) {
        this.closePress();
        return;
      }
      this.queuePress();
    });
  }

  /** Whether a finite animation runs in the view; an endless one (a spinner) never settles. */
  private viewAnimating(): boolean {
    return this.viewEl
      .getAnimations({ subtree: true })
      .some((a) => a.playState === "running" && a.effect?.getTiming().iterations !== Infinity);
  }

  private closePress(): void {
    const p = this.press;
    this.endPress();
    if (p === null) {
      return;
    }
    this.retake();
    if (
      p.from === "following" &&
      this.state === "reading" &&
      Math.abs(this.clampedTop() - this.followPosition()) <= edgeSlack()
    ) {
      this.setState("following");
    }
  }

  private endPress(): void {
    const p = this.press;
    this.press = null;
    if (p !== null && p.frame !== 0) {
      cancelAnimationFrame(p.frame);
    }
  }

  /** A collapse or expand the reader pressed: park on the content above it until the press's layout settles. */
  pressHold(): void {
    const from = this.state;
    this.setState("reading");
    this.openPress(from);
  }

  // ---- flights and jumps ----

  private openFlight(target: (() => number | null) | null = null): Promise<boolean> {
    this.endFlight(false);
    return new Promise((resolve) => {
      this.flight = {
        timer: setTimeout(() => {
          this.endFlight(true);
        }, FLIGHT_MAX_MS),
        seen: 0,
        done: [resolve],
        target,
      };
    });
  }

  /** A flight ends at its own `scrollend` or at `FLIGHT_MAX_MS` (landed), or at reader input or another jump (not);
   *  the text it stopped on is held. */
  private endFlight(landed: boolean): void {
    const f = this.flight;
    if (f === null) {
      return;
    }
    this.flight = null;
    clearTimeout(f.timer);
    // A smooth flight's destination is fixed when it starts, and layout above the target can move it meanwhile.
    const now = landed ? (f.target?.() ?? null) : null;
    if (now !== null) {
      this.writeTop(now);
    }
    this.retake();
    this.correct();
    this.pokeTop();
    for (const done of f.done) {
      done(landed);
    }
  }

  /** Reduced motion is the owner's decision: a requested smooth jump lands instantly under it. */
  private behaviorFor(requested: ScrollBehavior | undefined): "instant" | "smooth" {
    if (requested === "instant" || requested === "auto") {
      return "instant";
    }
    return matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth";
  }

  private dropMotion(): void {
    this.gesture.forget();
    this.read = null;
    this.endSettle();
    this.endPress();
    this.endFlight(false);
  }

  /** Jump to an absolute offset and park the reader there, or follow when it lands on the end. Resolves true once the
   *  jump has landed, false when the reader's input or another jump took the scroller first. */
  jumpToOffset(px: number, behavior: "instant" | "smooth"): Promise<boolean> {
    this.dropMotion();
    const max = this.max();
    const landing = Math.max(0, Math.min(Math.round(px), max));
    this.setState(landing >= max - edgeSlack() ? "following" : "reading");
    const b = this.behaviorFor(behavior);
    if (b === "instant" || Math.abs(landing - this.scrollEl.scrollTop) <= edgeSlack()) {
      this.writeTop(landing);
      this.retake();
      return Promise.resolve(true);
    }
    const landed = this.openFlight();
    this.scrollEl.scrollTo({ top: landing, behavior: "smooth" });
    return landed;
  }

  /** Park the reader on `target`: a turn map jump, a search hit, a run card reveal. A target with no box keeps the
   *  reader Following rather than parking them on a guess. */
  jumpTo(target: HTMLElement, opts: ScrollIntoViewOptions = {}): void {
    this.dropMotion();
    const behavior = this.behaviorFor(opts.behavior ?? "smooth");
    const before = this.scrollEl.scrollTop;
    target.scrollIntoView({ block: "start", ...opts, behavior });
    // Measured after the write: a skipped row takes its box only once `scrollIntoView` lays it out.
    const block = opts.block ?? "start";
    const landing = this.jumpLanding(target, block);
    const max = this.max();
    this.setState(landing !== null && landing < max - edgeSlack() ? "reading" : "following");
    if (
      landing !== null &&
      behavior === "smooth" &&
      Math.abs(landing - this.scrollEl.scrollTop) >= 1
    ) {
      void this.openFlight(this.relander(target, block, landing));
      return;
    }
    if (landing !== null && behavior === "instant") {
      this.writeTop(landing);
    }
    if (this.scrollEl.scrollTop !== before) {
      this.noteOwnWrite();
    }
    this.retake();
  }

  /** Where `target`'s landing stands now; a target rebuilt meanwhile (find re-marking its hits) is found through the
   *  transcript entry that held it, which keeps its element. */
  private relander(
    target: HTMLElement,
    block: ScrollLogicalPosition,
    landing: number,
  ): () => number | null {
    const entry = target.closest(ENTRY_STAMP);
    const from = entry === null ? null : this.scrollFrameRect(entry);
    return () => {
      if (target.isConnected) {
        return this.jumpLanding(target, block);
      }
      const now = entry === null ? null : this.scrollFrameRect(entry);
      return from === null || now === null
        ? null
        : Math.max(0, Math.min(landing + now.top - from.top, this.max()));
    };
  }

  /** Move the scroller by `px` as an owner write that sets no reading state: a layout compensation. */
  shiftBy(px: number): void {
    this.endSettle();
    this.writeTop(this.scrollEl.scrollTop + px);
    this.retake();
  }

  private jumpLanding(target: HTMLElement, block: ScrollLogicalPosition): number | null {
    const box = this.scrollFrameRect(target);
    if (box === null) {
      return null;
    }
    const room = this.scrollEl.clientHeight - (box.bottom - box.top);
    let wanted = box.top;
    if (block === "center") {
      wanted = box.top - room / 2;
    } else if (block === "end") {
      wanted = box.top - room;
    }
    return Math.max(0, Math.min(wanted, this.max()));
  }

  /** Return to Following: the live-edge listeners mount what the landing shows, then the owner lands and settles. */
  resume(): void {
    this.dropMotion();
    this.setState("following");
    this.anchor = null;
    for (const cb of [...this.liveEdgeListeners]) {
      cb();
    }
    this.correct();
    this.openSettle();
    this.publishReaderGesture();
  }

  scrollToBottom(): void {
    this.resume();
  }

  // ---- attach and detach ----

  /** Hand the scroller to a transcript view and restore its saved state; a fresh view attaches with
   *  `{scrollTop: 0, readingState: "following"}`. */
  attach(handle: ViewAttachHandle): void {
    this.dropMotion();
    this.clearFloor();
    this.observeView(handle.el);
    this.attached = true;
    this.setState(handle.readingState);
    this.writeTop(handle.scrollTop);
    const saved = handle.anchor;
    if (
      handle.readingState === "reading" &&
      saved !== undefined &&
      resolveAnchor(handle.el, saved) !== null
    ) {
      this.anchor = { marks: saved.marks, takenTop: viewOffset(this.scope) };
    } else {
      this.retake();
    }
    this.openSettle();
    this.holdFloor();
    this.lastT = this.clampedTop();
    for (const cb of [...this.attachListeners]) {
      cb();
    }
  }

  /** Take the scroller from the current view: snapshot its state, abandon the older-page pass, drop the deferred
   *  folds, and disconnect the observers so the parked view produces no callback. */
  detach(): ViewScrollState {
    // Read before the abandoned pass takes its skeleton out from above the reader.
    const anchor = this.state === "reading" ? readingAnchor(this.scope) : null;
    this.abandonLoadPass();
    const drift = anchor === null ? null : lineDrift(this.scope, anchor);
    const snapshot: ViewScrollState = {
      scrollTop: Math.max(0, Math.min(this.scrollEl.scrollTop + (drift ?? 0), this.max())),
      readingState: this.state,
      ...(anchor === null ? {} : { anchor }),
    };
    this.deferred = [];
    this.dropMotion();
    this.cancelScrollbarWidth();
    this.cancelFollowFrame();
    this.clearFloor();
    this.attached = false;
    this.lastT = -1;
    this.onLoadMore = null;
    this.hasMoreMessages = false;
    this.disconnectView();
    this.setState("following");
    return snapshot;
  }

  resetScrollState(): void {
    this.deferred = [];
    this.abandonLoadPass();
    this.dropMotion();
    this.cancelScrollbarWidth();
    this.cancelFollowFrame();
    this.anchor = null;
    this.lastT = -1;
    this.topNear = false;
    this.setLoadMore(null, false);
    this.setState("following");
  }

  private cancelFollowFrame(): void {
    if (this.followFrame !== 0) {
      cancelAnimationFrame(this.followFrame);
      this.followFrame = 0;
    }
  }

  // ---- observers ----

  private observeView(view: HTMLElement): void {
    this.viewEl = view;
    if (this.topSentinel !== null) {
      view.prepend(this.topSentinel);
      this.topObserver?.observe(this.topSentinel);
    }
    this.contentObserver?.disconnect();
    this.contentObserver?.observe(view, { childList: true, subtree: true, characterData: true });
    this.childObserver?.disconnect();
    this.childObserver?.observe(view, { childList: true });
    this.reobserveChildren();
    this.observeNear(view);
  }

  private disconnectView(): void {
    this.contentObserver?.disconnect();
    this.childObserver?.disconnect();
    if (this.topSentinel !== null) {
      this.topObserver?.unobserve(this.topSentinel);
      this.topNear = false;
    }
    for (const child of this.observedChildren) {
      this.resizeObserver?.unobserve(child);
    }
    this.observedChildren.clear();
    this.nearObserver?.disconnect();
    this.nearTargets.clear();
    this.nearNow.clear();
  }

  /** Build the near observer for the scrollport's current height, if its lead changed: a rootMargin is fixed for an
   *  observer's lifetime. */
  private fitNearObserver(): void {
    const margin = Math.round(this.scrollEl.clientHeight * NEAR_LEAD);
    if (margin === this.nearMargin) {
      return;
    }
    this.nearMargin = margin;
    this.fitTopObserver(margin);
    this.nearObserver?.disconnect();
    const observer = new IntersectionObserver(
      (entries) => {
        this.onNear(entries);
      },
      { root: this.scrollEl, rootMargin: `${String(margin)}px 0px` },
    );
    this.nearObserver = observer;
    for (const box of this.nearTargets) {
      observer.observe(box);
    }
  }

  /** The `[data-near]` toggle changes sizes in this task, after the previous paint and before the next frame's
   *  animation-frame callbacks, so it is corrected here, with the floor raised before the write. */
  private onNear(entries: readonly IntersectionObserverEntry[]): void {
    const entering: Element[] = [];
    for (const entry of entries) {
      entry.target.toggleAttribute("data-near", entry.isIntersecting);
      if (entry.isIntersecting) {
        this.nearNow.add(entry.target);
        entering.push(entry.target);
      } else {
        this.nearNow.delete(entry.target);
        this.floorStale = true;
      }
    }
    this.holdFloor();
    this.correct();
    this.floorAfter(true);
    if (entering.length > 0) {
      snapIconsIn(entering, this.cardsOf(entering), SKIPPABLE);
    }
  }

  private fitTopObserver(margin: number): void {
    this.topObserver?.disconnect();
    const observer = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (last !== undefined) {
          this.noteHistoryEdge(last.isIntersecting);
        }
      },
      { root: this.scrollEl, rootMargin: `${String(margin)}px 0px 0px 0px`, threshold: 0 },
    );
    this.topObserver = observer;
    if (this.topSentinel?.isConnected === true) {
      observer.observe(this.topSentinel);
    }
  }

  /** Record whether the history edge is inside the lead, and ask for the next older page when it is. */
  noteHistoryEdge(near: boolean): void {
    this.topNear = near;
    this.pokeTop();
  }

  private pokeTop(): void {
    if (this.topNear) {
      this.maybeLoadMore();
    }
  }

  private settleNear(records: readonly MutationRecord[]): void {
    const observer = this.nearObserver;
    if (observer === null) {
      return;
    }
    // Removals first: a node moved within one record is listed as both, and must stay observed.
    for (const record of records) {
      for (const node of record.removedNodes) {
        if (node instanceof Element) {
          forEachSkippable(node, (box) => {
            observer.unobserve(box);
            this.nearTargets.delete(box);
            this.nearNow.delete(box);
          });
        }
      }
      for (const node of record.addedNodes) {
        if (node instanceof Element) {
          this.observeNear(node);
        }
      }
    }
  }

  private observeNear(root: Element): void {
    const observer = this.nearObserver;
    if (observer === null) {
      return;
    }
    forEachSkippable(root, (box) => {
      observer.observe(box);
      this.nearTargets.add(box);
    });
  }

  /** Observe every view child but the history sentinel; returns how many observed children went away. */
  private reobserveChildren(): number {
    const observer = this.resizeObserver;
    if (observer === null) {
      return 0;
    }
    const current = new Set<Element>();
    for (const child of this.viewEl.children) {
      if (child !== this.topSentinel) {
        current.add(child);
      }
    }
    let removed = 0;
    for (const child of this.observedChildren) {
      if (!current.has(child)) {
        observer.unobserve(child);
        this.observedChildren.delete(child);
        removed++;
      }
    }
    for (const child of current) {
      if (!this.observedChildren.has(child)) {
        observer.observe(child);
        this.observedChildren.add(child);
      }
    }
    return removed;
  }

  /** `--scrollbar-w` is read by the scroller's own `padding-inline`, so writing it inside the resize delivery would
   *  resize a box already delivered in this loop. */
  private scheduleScrollbarWidth(): void {
    if (this.gutterFrame !== 0) {
      return;
    }
    this.gutterFrame = requestAnimationFrame(() => {
      this.gutterFrame = 0;
      this.publishScrollbarWidth();
    });
  }

  private cancelScrollbarWidth(): void {
    if (this.gutterFrame !== 0) {
      cancelAnimationFrame(this.gutterFrame);
      this.gutterFrame = 0;
    }
  }

  /** A classic scrollbar takes its width out of the scroller's content box, so the column centred inside lands half a
   *  scrollbar off the composer unless the scroller gives that width back. */
  private publishScrollbarWidth(): void {
    if (!this.publishesGutter) {
      return;
    }
    const next = `${String(this.scrollEl.offsetWidth - this.scrollEl.clientWidth)}px`;
    if (next === this.scrollbarWidth) {
      return;
    }
    this.scrollbarWidth = next;
    document.documentElement.style.setProperty("--scrollbar-w", next);
  }

  // ---- reads and registrations ----

  readingState(): ReadingState {
    return this.state;
  }

  /** Whether a reader gesture is open on this scroller. */
  gestureOpen(): boolean {
    return this.gesture.isOpen();
  }

  readingLineOffset(): number {
    return readingLineOffset(this.scrollEl);
  }

  onReadingStateChange(cb: (s: ReadingState) => void): void {
    this.stateListeners.push(cb);
  }

  /** Register `cb` on the transcript's own MutationObserver; returns the unregister. */
  onTranscriptMutate(cb: () => void): () => void {
    return register(this.mutateListeners, cb);
  }

  /** Register `cb` for a gesture in which the reader states where they want to be: a scroll of theirs, or a request
   *  for the live edge; returns the unregister. */
  onReaderGesture(cb: () => void): () => void {
    return register(this.readerGestureListeners, cb);
  }

  private publishReaderGesture(): void {
    for (const cb of this.readerGestureListeners) {
      cb();
    }
  }

  /** Register `cb` for a size change in one of the view's own cards; returns the unregister. */
  onContentResize(cb: () => void): () => void {
    return register(this.contentResizeListeners, cb);
  }

  /** Register `cb` for the reader asking for the live edge, called in Following before the landing write, so what it
   *  mounts is laid out by the frame the reader lands in; returns the unregister. */
  onLiveEdgeRequest(cb: () => void): () => void {
    return register(this.liveEdgeListeners, cb);
  }

  /** Register `cb` for a view taking the scroller; returns the unregister. */
  onAttach(cb: () => void): () => void {
    return register(this.attachListeners, cb);
  }

  /** Register `cb` for a scroll that has settled into one frame; returns the unregister. */
  onViewportChange(cb: () => void): () => void {
    return register(this.viewportListeners, cb);
  }

  private dispatchViewportChange(): void {
    if (this.viewportFrame !== 0 || this.viewportListeners.length === 0) {
      return;
    }
    this.viewportFrame = requestAnimationFrame(() => {
      this.viewportFrame = 0;
      for (const cb of [...this.viewportListeners]) {
        cb();
      }
    });
  }

  setAnchorProvider(fn: (() => HTMLElement | null) | null): void {
    this.anchorProvider = fn;
  }

  /** Set the resume control's label; the tooltip carries it where the label is hidden (the phone, a narrow gutter). */
  setResumeLabel(text: string): void {
    const btn = this.resumeBtn();
    const label = btn.querySelector("span");
    if (label !== null) {
      label.textContent = text;
    }
    btn.dataset["tooltip"] = text;
  }

  /** Apply `mutate` now if Following, or queue it until the reader returns: content must never disappear from above
   *  the reader through no action of their own. */
  deferWhileReading(mutate: () => void): void {
    if (this.state === "following") {
      mutate();
      return;
    }
    this.deferred.push(mutate);
  }

  setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
    this.onLoadMore = fn;
    this.hasMoreMessages = hasMore;
    this.pokeTop();
  }

  /** Fetch until the scroller overflows. */
  fillViewport(): void {
    if (this.scrollEl.scrollHeight > this.scrollEl.clientHeight) {
      return;
    }
    this.maybeLoadMore(true);
  }

  /** How far the transcript can scroll: content height minus viewport height, 0 when it fits. */
  scrollableBy(): number {
    return Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
  }

  /** The single owner of the state and of the resume control's visibility (visible while Reading). */
  private setState(next: ReadingState): void {
    if (this.state === next) {
      return;
    }
    this.state = next;
    if (this.read !== null) {
      this.read.base = this.clampedTop();
    }
    this.resumeBtn().classList.toggle("hidden", next === "following");
    if (next === "following") {
      this.flushDeferred();
    }
    for (const cb of this.stateListeners) {
      cb(next);
    }
  }

  private flushDeferred(): void {
    if (this.deferred.length === 0) {
      return;
    }
    const queue = this.deferred;
    this.deferred = [];
    for (const fn of queue) {
      fn();
    }
  }

  /** Where `el` sits in the scroller's scroll frame, or null with no box. Rects, never `offsetTop`: a bubble's
   *  offsetParent is its own `content-visibility: auto` row, which `contain: paint` makes a containing block. */
  private scrollFrameRect(el: Element): { top: number; bottom: number } | null {
    if (!el.isConnected || el.getClientRects().length === 0) {
      return null;
    }
    const rect = el.getBoundingClientRect();
    const origin = this.scrollEl.getBoundingClientRect().top + this.scrollEl.clientTop;
    return {
      top: this.scrollEl.scrollTop + (rect.top - origin),
      bottom: this.scrollEl.scrollTop + (rect.bottom - origin),
    };
  }

  // ---- older pages ----

  private maybeLoadMore(force = false): void {
    // A smooth flight toward an early turn crosses the history edge on its way; the forced call is a caller stating a
    // need, so it still runs.
    if (!force && this.flight !== null) {
      return;
    }
    if (!this.hasMoreMessages || this.loadingMore || this.onLoadMore === null) {
      return;
    }
    this.loadingMore = true;
    const skel = loadMoreSkeleton();
    skel.id = SKELETON_ID;
    const firstContent = (): Element | null =>
      [...this.viewEl.children].find((c) => c !== this.topSentinel && c !== skel) ?? null;
    const ref = firstContent();
    this.viewEl.prepend(skel);
    this.holdFloor();
    this.correct();
    this.floorAfter(true);
    this.onLoadMore();
    const observer = new MutationObserver(() => {
      if (document.getElementById(SKELETON_ID) === null) {
        this.endLoadPass();
        // A short page may leave the edge inside the lead; an empty one stays quiet until the edge re-enters, or the
        // same failing request would loop.
        if (firstContent() !== ref) {
          this.pokeTop();
        }
      }
    });
    const safetyTimer = setTimeout(() => {
      this.abandonLoadPass();
    }, 15_000);
    this.pendingLoad = (): void => {
      observer.disconnect();
      clearTimeout(safetyTimer);
    };
    observer.observe(this.viewEl, { childList: true });
  }

  /** End the pagination pass in flight; idempotent, and safe with no pass. */
  private endLoadPass(): void {
    const end = this.pendingLoad;
    this.pendingLoad = null;
    this.loadingMore = false;
    end?.();
  }

  /** Ending the pass first stops the removal below from being read as the fetch completing. */
  private abandonLoadPass(): void {
    this.endLoadPass();
    this.viewEl.querySelector(`[id="${SKELETON_ID}"]`)?.remove();
  }
}

function register<T>(list: T[], cb: T): () => void {
  list.push(cb);
  return () => {
    const at = list.indexOf(cb);
    if (at >= 0) {
      list.splice(at, 1);
    }
  };
}

/** A second scroll-follow over another transcript-shaped view, with the chat's reading rules: it follows the live
 *  edge until the reader scrolls up, and `resumeBtn` takes them back. */
export interface FollowScroller {
  /** Hand the scroller to a view with the state it was parked in. */
  attach(handle: ViewAttachHandle): void;
  /** Take the scroller from the current view and return the state to park it with. */
  detach(): ViewScrollState;
  /** Whether a reader gesture is open on the scroller. */
  gestureOpen(): boolean;
}

/** Build a follower over `view` inside `scroller`. It never publishes `--scrollbar-w`, which the chat owns. */
export function createFollowScroller(
  view: HTMLElement,
  scroller: HTMLElement,
  resumeBtn: HTMLElement,
): FollowScroller {
  const c = new ScrollController(view, scroller, () => resumeBtn, false);
  c.init();
  return {
    attach: (handle) => {
      c.attach(handle);
    },
    detach: () => c.detach(),
    gestureOpen: () => c.gestureOpen(),
  };
}
