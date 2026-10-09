// Reading position for the transcript, as two named states:

import { el } from "@cplieger/reactive";
import { loadMoreSkeleton } from "./skeleton.js";
import { $ } from "./dom.js";

/** Distance from the top at which older messages start loading. */
const LOAD_MORE_THRESHOLD_PX = 100;
/** Distance from the bottom still counted as "at the bottom". Independent of
 *  LOAD_MORE_THRESHOLD_PX despite the equal value. */
const BOTTOM_TOLERANCE_PX = 100;
/** How long after their last input the reader still owns the scroller. A QUIET PERIOD rather
 *  than a gesture boundary, because `touchend` does not end a touch scroll: iOS momentum keeps
 *  delivering scroll events after the finger leaves, and one of those is what carries the reader
 *  out of the bottom tolerance band. 300ms covers a fling above ~330px/s and a key's smooth
 *  scroll animation (measured at ~8 events per press in Chromium). */
const READER_CONTROL_MS = 300;
/** How long a bottom pin keeps re-asserting the live edge. Sized to the fold choreography it
 *  releases: `--fold-slide` runs 0.3s and a close flips `content-visibility` at 0.42s
 *  (css/29-turns.css). */
const PIN_SETTLE_MS = 700;
let pinSettleMs = PIN_SETTLE_MS;
/** Where the reading line sits, as a fraction of the scrollport from its top. Scroller geometry,
 *  so the scroller owns it: a jump's landing and the turn the rail calls active are the same
 *  line, and two consumers deriving it separately can disagree. */
export const READING_LINE_FRACTION = 1 / 3;
/** How long a self-scroll epoch survives with no `scrollend` and no reader input. Bounds one
 *  animation plus its settle, so it is re-armed at every `scrollToOffset` rather than measured
 *  from the epoch's start. */
const SELF_SCROLL_MAX_MS = 1500;
/** Keys that scroll a box, by direction. `End` is in neither deliberately: the handler in `init`
 *  turns it into a resume, and a resume's own pin is not a reader scroll. Shift+Space scrolls
 *  UP, which no other key spelling distinguishes. */
const SCROLL_UP_KEYS = new Set(["ArrowUp", "PageUp", "Home"]);
const SCROLL_DOWN_KEYS = new Set(["ArrowDown", "PageDown", " "]);
/** The transcript's `content-visibility: auto` boxes (`transcript-layout-css.test.ts` `BULK`). */
const SKIPPABLE = ".msg-row, .tool-call, .subagent-block, .plan-message, .run-card";
/** How far past the scrollport, in scrollport heights, a skippable box is laid out ahead (`[data-near]`). Chromium's
 *  own lead (https://chromium.googlesource.com/chromium/src/+/main/third_party/blink/renderer/core/display_lock/display_lock_document_state.cc);
 *  WebKit's has no margin and lands a frame late
 *  (https://github.com/WebKit/WebKit/blob/main/Source/WebCore/dom/ContentVisibilityDocumentState.cpp), so a box
 *  would take its real size on screen, after the paint, where no anchoring hides it. Applied in pixels: the spec resolves
 *  a rootMargin percentage against the root's WIDTH (https://w3c.github.io/IntersectionObserver/#intersectionobserver-root-intersection-rectangle),
 *  which Chromium and WebKit do not follow. */
const NEAR_LEAD = 1.5;

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

/** A parked view's scroll-owned state: where the scroller stood and which reading state the
 *  reader was in. `messages.ts` carries it inside its ViewHandle across a park/unpark cycle. */
export interface ViewScrollState {
  scrollTop: number;
  readingState: ReadingState;
}

/** What `attach` needs: the incoming view element (the observers' new root) plus the state to
 *  restore into it. */
export interface ViewAttachHandle extends ViewScrollState {
  el: HTMLElement;
}

class ScrollController {
  readonly scrollEl: HTMLElement;

  /** The observers' root: the ACTIVE transcript view, or the multiplexer itself before any view
   *  attaches. Every mutation callback, the per-child ResizeObserver set and the pagination
   *  furniture key off this, so a parked view gets none. */
  private viewEl: HTMLElement;

  private state: ReadingState = "following";

  /** Until when the reader owns the scroller (`READER_CONTROL_MS`), refreshed by every input
   *  event a reader scroll produces. */
  private userScrollingUntil = 0;

  private hasMoreMessages = false;
  private loadingMore = false;
  private onLoadMore: (() => void) | null = null;

  /** Mutations that were postponed because the reader is Reading. Applied in arrival order on
   *  the return to Following. */
  private deferred: (() => void)[] = [];
  private stateListeners: ((s: ReadingState) => void)[] = [];
  /** Callbacks riding the transcript MutationObserver this module owns, so a consumer needs no
   *  observer of its own. */
  private mutateListeners: (() => void)[] = [];
  /** Reader-gesture subscribers; `onReaderGesture` owns the contract. */
  private readerGestureListeners: (() => void)[] = [];
  /** Callbacks riding the per-child ResizeObserver, so a consumer whose cached geometry a card's
   *  own growth invalidates needs no observer of its own. */
  private contentResizeListeners: (() => void)[] = [];
  /** Callbacks fired when a view takes the scroller; `onAttach` owns the contract. */
  private attachListeners: (() => void)[] = [];
  /** Callbacks riding the scroll listener, coalesced to one frame. Dispatched for the
   *  controller's OWN writes too: a `jumpTo` from the rail or from find-in-chat moves the reader
   *  a long way and the residency window has to follow, whichever side of the self marker that
   *  write falls on. */
  private viewportListeners: (() => void)[] = [];
  private viewportFrame = 0;
  /** Supplies the element Following should keep visible while a turn streams. Null (or a null
   *  return) falls back to the document bottom. */
  private anchorProvider: (() => HTMLElement | null) | null = null;

  private rafPending = false;

  /** The bottom pin's deadline, and the frame it has queued (0 = none). */
  private pinUntil = 0;
  private pinFrame = 0;

  /** The element at the scrollport's top edge after the last scroll event, and where it stood. */
  private viewTop: { el: Element; top: number } | null = null;

  /** The element on the reading line after the reader's last scroll, and where it stood (null =
   *  nothing to hold). Native anchoring holds the TOP edge, so a row there growing late — a
   *  `content-visibility` placeholder taking its real size — pushes this one away; `holdReadingLine`
   *  puts it back. Dropped by a press in the transcript: what a click opens is the reader's own. */
  private readLine: { el: Element; top: number } | null = null;

  /** Whether the last write was `holdReadingLine`'s: layout can move the line again between that write and its
   *  scroll event, so the event holds once more before it re-reads the line. */
  private holding = false;

  /** The scrollTop this controller last wrote, or -1. A `scroll` event landing on it is the
   *  controller's OWN, so it may not be PUBLISHED as a reader gesture: a streaming turn re-pins
   *  several times a second and none of those is the reader changing their mind. */
  private selfScrollTop = -1;

  /** An interval in which every scroll event belongs to this controller, and the timer that
   *  bounds it (null = none). */
  private epochOpen = false;
  private epochTimer: ReturnType<typeof setTimeout> | null = null;

  /** Did the reader's last directional input ask to go UP? The only thing that may enter
   *  Reading, and spent by `setState` at every door into Following. */
  private upwardIntent = false;

  /** A scrollbar drag in progress. Untimed, because the reader can hold the thumb for as long as
   *  they like and a drag produces no repeat input to refresh a deadline. */
  private barDragging = false;

  /** Last `clientY` seen from a touch and from a held scrollbar thumb, because both events carry
   *  a position rather than a delta (null = no gesture in progress). */
  private lastTouchY: number | null = null;
  private lastBarY: number | null = null;

  /** Last value written to `--scrollbar-w`, so a resize storm costs at most one style
   *  invalidation. */
  private scrollbarWidth = "";

  /** Teardown for the pagination pass in flight, or null when none is running. */
  private pendingLoad: (() => void) | null = null;
  /** Re-measures the pass's drift baseline where the reader now stands, until the page lands; null with no pass. */
  private rebaseLoad: (() => void) | null = null;

  /** The observers rooted on `viewEl`, held as fields so `attach` can re-root them on the
   *  incoming view. `resizeObserver` watches the view's CHILDREN and only reads; the scroller's
   *  own box belongs to `gutterObserver`, the only one that WRITES, because that write cannot be
   *  delivered in a loop already carrying every card. */
  private contentObserver: MutationObserver | null = null;
  private childObserver: MutationObserver | null = null;
  private resizeObserver: ResizeObserver | null = null;
  private gutterObserver: ResizeObserver | null = null;
  private nearObserver: IntersectionObserver | null = null;
  /** `nearObserver`'s targets, so the observer a new scrollport height needs takes over exactly these. */
  private nearTargets = new Set<Element>();
  /** The near lead `nearObserver` was built with, in px (-1 = none built). */
  private nearMargin = -1;
  private observedChildren = new Set<Element>();

  /** The frame the gutter write has queued (0 = none), a single slot so a resize storm costs one
   *  write. */
  private gutterFrame = 0;

  /** The frame the resize callback's own state re-derivation has queued (0 = none). A single
   *  slot for `gutterFrame`'s reason, and it carries NO measurement: a resize storm is one
   *  transition, decided by the geometry the apply reads for itself. */
  private revalidateFrame = 0;

  /** The live edge's own element: a zero-height marker at the end of the transcript's flow,
   *  watched by `edgeObserver`. Moves with the attached view. A marker rather than a measurement
   *  of the view itself, because an IntersectionObserver reports a THRESHOLD CROSSING and the
   *  view is many viewports tall — its ratio changes continuously and crosses nothing. */
  private edgeSentinel: HTMLElement | null = null;
  private edgeObserver: IntersectionObserver | null = null;

  /** Whether the live edge is in view, as last PUBLISHED rather than measured. Starts true
   *  because a fresh view is at its own bottom, and the state it has to agree with (`following`)
   *  makes the value unobservable until something publishes a real one. */
  private atLiveEdge = true;

  /** What the previous scroll event saw: its scrollTop (-1 = unknown) and its distance from the
   *  end (Infinity = unknown). The baselines a clamp and a size change are told apart against;
   *  the resize frame also refreshes the second. */
  private lastScrollTop = -1;
  private lastEdgeDistance = Number.POSITIVE_INFINITY;

  constructor(messagesEl: HTMLElement, scrollEl: HTMLElement) {
    this.scrollEl = scrollEl;
    this.viewEl = messagesEl;
  }

  init(): void {
    const scrollBtn = $.scrollBottom;

    // The transcript's scrollbar belongs in the gutter, not in the measure the column and the
    // composer share: a classic bar is placed at the scroller's inline-end border edge and takes
    // its width out of the CONTENT box, so `#messages` centred inside sits half a scrollbar left of
    // `.prompt-box` unless the scroller gives that width back.
    this.publishScrollbarWidth();

    // Every input marks the quiet period; the ones that carry a DIRECTION also aim it. `touchend`
    // marks but cannot aim, which costs nothing: a fling's direction is already known from the
    // `touchmove` that started it.
    const markInput = (dir: -1 | 0 | 1 = 0): void => {
      // Input is what ends an epoch, and it ends it BEFORE the scroll event it produces arrives, so
      // the reader's own gesture publishes normally.
      this.endSelfScroll();
      this.userScrollingUntil = Date.now() + READER_CONTROL_MS;
      if (dir !== 0) {
        this.upwardIntent = dir < 0;
      }
    };
    this.scrollEl.addEventListener(
      "wheel",
      (e) => {
        markInput(e.deltaY < 0 ? -1 : 1);
      },
      { passive: true },
    );
    this.scrollEl.addEventListener(
      "touchend",
      () => {
        markInput();
      },
      { passive: true },
    );
    // A finger moving DOWN the screen scrolls the content UP, so the sign inverts. Tracked between
    // moves because a `touchmove` carries a position, not a delta.
    this.scrollEl.addEventListener(
      "touchmove",
      (e) => {
        const y = e.touches[0]?.clientY ?? null;
        markInput(y === null || this.lastTouchY === null ? 0 : y > this.lastTouchY ? -1 : 1);
        this.lastTouchY = y;
      },
      { passive: true },
    );
    this.scrollEl.addEventListener(
      "touchstart",
      (e) => {
        this.lastTouchY = e.touches[0]?.clientY ?? null;
      },
      { passive: true },
    );
    // A scrollbar drag surfaces no wheel and no touch, so the press IS the input, scoped to the
    // gutter or an ordinary click in the transcript would suppress the next chunk's pin. Release on
    // the DOCUMENT: a drag that leaves the scroller still owns the bar.
    this.scrollEl.addEventListener(
      "pointerdown",
      (e) => {
        this.readLine = null;
        if (this.inScrollbarGutter(e)) {
          this.barDragging = true;
          this.lastBarY = e.clientY;
          markInput();
        }
      },
      { passive: true },
    );
    // The thumb moves the same way as the content, so this sign does NOT invert.
    document.addEventListener(
      "pointermove",
      (e) => {
        if (!this.barDragging) {
          return;
        }
        markInput(
          this.lastBarY === null || e.clientY === this.lastBarY
            ? 0
            : e.clientY < this.lastBarY
              ? -1
              : 1,
        );
        this.lastBarY = e.clientY;
      },
      { passive: true },
    );
    for (const type of ["pointerup", "pointercancel"] as const) {
      document.addEventListener(
        type,
        () => {
          if (this.barDragging) {
            this.barDragging = false;
            markInput();
          }
        },
        { passive: true },
      );
    }

    this.scrollEl.addEventListener(
      "scroll",
      () => {
        // Read before anything below can dirty the layout.
        const top = this.scrollEl.scrollTop;
        const edge = this.edgeDistance();
        const anchored = this.movedByAnchoring(top);
        // A clamp only ever lowers scrollTop, so moving DOWN is the one thing it cannot do; the
        // controller's own write and the browser's anchoring are not the reader moving either.
        const movedDown =
          !this.landedOnOwnWrite() &&
          !anchored &&
          this.lastScrollTop >= 0 &&
          top > this.lastScrollTop + 1;
        this.dispatchViewportChange();
        // An open epoch attributes this event to the controller's own animation: the state was
        // decided when the epoch opened, and a position the flight passes through inside the
        // tolerance band is not the reader reaching the live edge.
        if (!this.epochOpen) {
          // Free to read here (a scroll event is delivered after layout), and true whoever moved
          // the scroller.
          this.atLiveEdge = this.isAtBottom() && !this.parkedByOwnAim(movedDown);
          if (this.atLiveEdge) {
            this.setState("following");
          } else if (this.upwardIntent) {
            // The reader ASKED to go up. Position alone cannot say this: a block-window re-index
            // moved a reader 9600px up inside the window their own downward drag had opened, and
            // the transcript stopped following for the rest of the turn.
            this.setState("reading");
          }
        }
        // The self marker is consumed whichever branch ran above: it may only ever excuse the one
        // event its own write produced. What it excuses is the GESTURE and nothing else — the state
        // is derived from input, which a write of this controller's does not fire.
        const own = this.landedOnOwnWrite();
        this.selfScrollTop = -1;
        const held = own && this.holding;
        this.holding = false;
        if (held) {
          this.holdReadingLine();
        }
        if (!this.epochOpen && !own && !anchored) {
          this.publishReaderGesture();
        }
        this.maybeLoadMore();
        this.lastScrollTop = top;
        this.lastEdgeDistance = edge;
        this.noteViewTop();
      },
      { passive: true },
    );

    // The animation announcing its own end, which is the epoch's ordinary close.
    this.scrollEl.addEventListener(
      "scrollend",
      () => {
        this.endSelfScroll();
      },
      { passive: true },
    );

    scrollBtn.addEventListener("click", () => {
      this.resume();
    });

    // On the DOCUMENT, not the scroller: the transcript carries no tabindex, so Chromium scrolls it
    // with `activeElement` still on `body` and the keydown never passes through it (measured). Both
    // arms stop at a text field, where End means end-of-line and every other key is typing.
    document.addEventListener("keydown", (e) => {
      const t = e.target as HTMLElement | null;
      if (t !== null && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) {
        return;
      }
      // Activating a control in the transcript is a press, like the pointer's.
      if ((e.key === "Enter" || e.key === " ") && t !== null && this.viewEl.contains(t)) {
        this.readLine = null;
      }
      // Under a modifier too, and BEFORE the resume arm's guard: Ctrl+Home scrolls this box, so
      // dropping it leaves the reader at a position with no fingerprint on it, which reads as
      // Following and pins them straight back down.
      if (SCROLL_UP_KEYS.has(e.key) || (e.key === " " && e.shiftKey)) {
        markInput(-1);
        return;
      }
      if (SCROLL_DOWN_KEYS.has(e.key)) {
        markInput(1);
        return;
      }
      if (e.key === "End" && !e.ctrlKey && !e.metaKey && !e.altKey && this.state === "reading") {
        // Ctrl+End is left to the platform: it lands AT the bottom, where the listener promotes to
        // Following on the position alone.
        this.resume();
      }
    });

    // NOTICES change; measures nothing. It consumes the published edge state and hands the one
    // write it still owes to an animation frame (`autoScrollIfAnchored`), so a streamed delta costs
    // this callback no layout over a subtree that can hold several hundred cards.
    const mutationObserver = new MutationObserver((records) => {
      this.settleNear(records);
      this.revalidateReadingState(this.atLiveEdge);
      this.autoScrollIfAnchored();
      for (const cb of this.mutateListeners) {
        cb();
      }
    });
    this.contentObserver = mutationObserver;

    // The publisher. Its callback runs after layout, so the geometry it carries costs nothing — and
    // it is also the TRIGGER for a re-derivation, because a shrink that brings the edge back into
    // view makes no mutation and no gesture, so nothing else would re-ask the question.
    this.edgeSentinel = el("div", {
      className: "transcript-edge",
      "aria-hidden": "true",
    });
    this.edgeObserver = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (last === undefined) {
          return;
        }
        this.atLiveEdge = last.isIntersecting;
        this.revalidateReadingState(this.atLiveEdge);
      },
      {
        root: this.scrollEl,
        // The same tolerance `isAtBottom` applies, expressed as room BELOW the scrollport: the
        // sentinel counts as reached while it is within it.
        rootMargin: `0px 0px ${String(BOTTOM_TOLERANCE_PX)}px 0px`,
        threshold: 0,
      },
    );
    // Not observed here: `observeView` below is the single owner, because the sentinel is in no
    // view yet and `detach` unobserves it.

    // The one observer that sees a box change with no DOM mutation behind it (zoom, a scrollbar
    // swap, a code block expanding).
    this.resizeObserver = new ResizeObserver(() => {
      // First: the deliveries below may pin, and this reads the layout the change produced.
      this.holdReadingLine();
      this.scheduleRevalidate();
      // Ordered BEFORE the deferred transition, so a delivery whose release lands next frame pins
      // nothing here; the flush's own resize or the next mutation does it.
      this.autoScrollIfAnchored();
      for (const cb of this.contentResizeListeners) {
        cb();
      }
    });
    // Watches the scroller ALONE and owns the one write that reaches a shared ancestor.
    this.gutterObserver = new ResizeObserver(() => {
      this.scheduleScrollbarWidth();
      this.fitNearObserver();
    });
    this.gutterObserver.observe(this.scrollEl);
    this.fitNearObserver();
    this.childObserver = new MutationObserver(() => {
      this.reobserveChildren();
    });
    this.observeView(this.viewEl);
  }

  /** Root the content observers on `el`: the transcript MutationObserver, the childList watcher
   *  behind the per-child ResizeObserver set, and that set itself (the view's children = the
   *  turn cards, as before the multiplexer). `attach` calls this with the incoming view;
   *  `detach` disconnects without re-rooting, which is what makes a parked view observer-silent. */
  private observeView(view: HTMLElement): void {
    this.viewEl = view;
    // The edge marker follows the attached view, so one observer serves every chat: a parked view's
    // own bottom is not a live edge.
    if (this.edgeSentinel !== null) {
      view.appendChild(this.edgeSentinel);
      // Re-observed rather than left watching across the move, because `detach` unobserves it: a
      // parked view must produce no callback at all.
      this.edgeObserver?.observe(this.edgeSentinel);
    }
    this.contentObserver?.disconnect();
    this.contentObserver?.observe(view, {
      childList: true,
      subtree: true,
      characterData: true,
    });
    this.childObserver?.disconnect();
    this.childObserver?.observe(view, { childList: true });
    this.reobserveChildren();
    this.observeNear(view);
  }

  private disconnectView(): void {
    this.contentObserver?.disconnect();
    this.childObserver?.disconnect();
    // The edge marker rides with the outgoing view, so unobserving it is what makes `detach`'s
    // promise true for every view observer: parking the view sets `content-visibility: hidden`,
    // the sentinel stops being rendered, and the observer would otherwise publish `isIntersecting:
    // false` from a subtree nobody is reading.
    if (this.edgeSentinel !== null) {
      this.edgeObserver?.unobserve(this.edgeSentinel);
    }
    for (const child of this.observedChildren) {
      this.resizeObserver?.unobserve(child);
    }
    this.observedChildren.clear();
    // The near set holds only the attached view's boxes; `observeView` rescans the incoming one.
    this.nearObserver?.disconnect();
    this.nearTargets.clear();
  }

  /** Build the near observer for the scrollport's current height, if its lead changed: a rootMargin is fixed for an
   *  observer's lifetime. */
  private fitNearObserver(): void {
    const margin = Math.round(this.scrollEl.clientHeight * NEAR_LEAD);
    if (margin === this.nearMargin) {
      return;
    }
    this.nearMargin = margin;
    this.nearObserver?.disconnect();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          entry.target.toggleAttribute("data-near", entry.isIntersecting);
        }
      },
      { root: this.scrollEl, rootMargin: `${String(margin)}px 0px` },
    );
    this.nearObserver = observer;
    for (const box of this.nearTargets) {
      observer.observe(box);
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

  /** Lay every skippable box in `root` out while it is near the scrollport, so its first real size lands off screen,
   *  where native anchoring holds the reader. Observing a box twice is a no-op. */
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

  private reobserveChildren(): void {
    const observer = this.resizeObserver;
    if (observer === null) {
      return;
    }
    // The edge marker is not a child worth watching: its box is zero and never changes, and
    // `observe()` DELIVERS an entry for a new target — so watching it would fire a resize callback
    // (and with it an auto-scroll) on every attach, dragging the incoming view to its own bottom.
    const current = new Set<Element>();
    for (const child of this.viewEl.children) {
      if (child !== this.edgeSentinel) {
        current.add(child);
      }
    }
    for (const child of this.observedChildren) {
      if (!current.has(child)) {
        observer.unobserve(child);
        this.observedChildren.delete(child);
      }
    }
    for (const child of current) {
      if (!this.observedChildren.has(child)) {
        observer.observe(child);
        this.observedChildren.add(child);
      }
    }
  }

  /** Defer the gutter write one animation frame, behind a single slot. THE WRITE MAY NOT LAND
   *  INSIDE THE RESIZE DELIVERY: `--scrollbar-w` is read by the scroller's own `padding-inline`,
   *  so writing it from the callback observes a box already delivered in this loop, which
   *  Chromium reports as "ResizeObserver loop completed with undelivered notifications". */
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

  /** Defer the resize callback's state re-derivation one frame, behind a single slot, and
   *  RE-READ the geometry there rather than carry one forward: deferred for
   *  `scheduleScrollbarWidth`'s reason, re-read because content appended between the two moves
   *  the live edge with no input, so a carried `atBottom: true` parks the reader. */
  private scheduleRevalidate(): void {
    if (this.revalidateFrame !== 0) {
      return;
    }
    this.revalidateFrame = requestAnimationFrame(() => {
      this.revalidateFrame = 0;
      const edge = this.edgeDistance();
      // Growth only carries the end away from the reader, so only a change that brought the end
      // closer may release them.
      const shrank = edge < this.lastEdgeDistance - 1;
      // Recorded even when the release is gated: growth the gate skipped would leave the baseline
      // low, and a later shrink back to the end would read as no change.
      this.lastEdgeDistance = edge;
      this.revalidateReadingState(shrank && this.isAtBottom());
    });
  }

  private cancelRevalidate(): void {
    if (this.revalidateFrame !== 0) {
      cancelAnimationFrame(this.revalidateFrame);
      this.revalidateFrame = 0;
    }
  }

  /** Write the scroller's reserved gutter to `--scrollbar-w` — the width its own inline-END
   *  inset gives back so the scrollbar lands in the gutter rather than in the measure the column
   *  shares with the composer. Reads the real element rather than a probe div, so the number is
   *  the gutter actually reserved on the box being compensated. */
  private publishScrollbarWidth(): void {
    const next = `${String(this.scrollEl.offsetWidth - this.scrollEl.clientWidth)}px`;
    if (next === this.scrollbarWidth) {
      return;
    }
    this.scrollbarWidth = next;
    document.documentElement.style.setProperty("--scrollbar-w", next);
  }

  /** Did this press land on the scrollbar rather than on the transcript? */
  private inScrollbarGutter(e: PointerEvent): boolean {
    const gutter = this.scrollEl.offsetWidth - this.scrollEl.clientWidth;
    return gutter > 0 && e.clientX >= this.scrollEl.getBoundingClientRect().right - gutter;
  }

  readingState(): ReadingState {
    return this.state;
  }

  /** Px from the scrollport's top to the reading line. */
  readingLineOffset(): number {
    return this.scrollEl.clientHeight * READING_LINE_FRACTION;
  }

  /** The PUBLISHED edge verdict — aim-aware, so a consumer asks the same question the reading
   *  state answers. */
  atLiveEdgeNow(): boolean {
    return this.atLiveEdge;
  }

  /** Open an epoch: until it closes, every scroll event is this controller's own animation
   *  rather than the reader stating a position. */
  beginSelfScroll(): void {
    this.epochOpen = true;
    this.armEpochBackstop();
  }

  /** Close the open epoch, if any. Idempotent, because four different closers race for it:
   *  `scrollend`, the backstop, reader input, and a change of owner. */
  endSelfScroll(): void {
    this.epochOpen = false;
    if (this.epochTimer !== null) {
      clearTimeout(this.epochTimer);
      this.epochTimer = null;
    }
  }

  /** Re-arm the backstop, so it bounds one animation plus its settle rather than a whole
   *  sequence of them. */
  private armEpochBackstop(): void {
    if (!this.epochOpen) {
      return;
    }
    if (this.epochTimer !== null) {
      clearTimeout(this.epochTimer);
    }
    this.epochTimer = setTimeout(() => {
      this.endSelfScroll();
    }, SELF_SCROLL_MAX_MS);
  }

  /** Scroll to an absolute offset inside the open epoch, and PARK the reader there. */
  scrollToOffset(px: number, behavior: ScrollBehavior): void {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const landing = Math.max(0, Math.min(px, max));
    this.setState(this.landsAtOffsetLiveEdge(landing) ? "following" : "reading");
    this.armEpochBackstop();
    this.scrollSelfTo(landing, behavior);
  }

  onReadingStateChange(cb: (s: ReadingState) => void): void {
    this.stateListeners.push(cb);
  }

  /** Register `cb` on the transcript's own MutationObserver; returns the unregister. Delivery
   *  keeps the observer's microtask timing, so a consumer that mutates the transcript itself can
   *  suppress its own echo the same way it would with an observer of its own. */
  onTranscriptMutate(cb: () => void): () => void {
    this.mutateListeners.push(cb);
    return () => {
      const at = this.mutateListeners.indexOf(cb);
      if (at >= 0) {
        this.mutateListeners.splice(at, 1);
      }
    };
  }

  /** Register `cb` for a gesture in which the READER states where they want to be — a scroll, or
   *  a request for the live edge; returns the unregister. */
  onReaderGesture(cb: () => void): () => void {
    this.readerGestureListeners.push(cb);
    return () => {
      const at = this.readerGestureListeners.indexOf(cb);
      if (at >= 0) {
        this.readerGestureListeners.splice(at, 1);
      }
    };
  }

  private publishReaderGesture(): void {
    for (const cb of this.readerGestureListeners) {
      cb();
    }
  }

  /** Register `cb` for a size change in one of the view's own cards; returns the unregister. */
  onContentResize(cb: () => void): () => void {
    this.contentResizeListeners.push(cb);
    return () => {
      const at = this.contentResizeListeners.indexOf(cb);
      if (at >= 0) {
        this.contentResizeListeners.splice(at, 1);
      }
    };
  }

  /** Register `cb` for a view TAKING the scroller; returns the unregister. */
  onAttach(cb: () => void): () => void {
    this.attachListeners.push(cb);
    return () => {
      const at = this.attachListeners.indexOf(cb);
      if (at >= 0) {
        this.attachListeners.splice(at, 1);
      }
    };
  }

  /** Register `cb` for a scroll that has settled into one frame; returns the unregister. A
   *  second `scroll` listener elsewhere is not an option: this one owns the reading-state
   *  derivation and the input marks it reads, and a second owner would see this controller's own
   *  compensations without that context. */
  onViewportChange(cb: () => void): () => void {
    this.viewportListeners.push(cb);
    return () => {
      const at = this.viewportListeners.indexOf(cb);
      if (at >= 0) {
        this.viewportListeners.splice(at, 1);
      }
    };
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

  /** Set the resume control's label, so the one element that knows the reader is behind is the
   *  one that says how far. Counts BLOCKS, not messages: a long streaming turn should show
   *  progress rather than a static badge. */
  setResumeLabel(text: string): void {
    const btn = $.scrollBottom;
    const label = btn.querySelector("span");
    if (label !== null) {
      label.textContent = text;
    }
    // The label is HIDDEN in two places — docked in the rail's column on a window whose gutter
    // cannot hold it (css/13-messages.css) and on the phone (50-mobile.css) — so the tooltip
    // carries it.
    btn.dataset["tooltip"] = text;
  }

  /** Return to Following: pin to the live edge and flush deferred mutations. */
  resume(): void {
    this.pinToLiveEdge();
  }

  /** Enter Reading explicitly: a collapse the user asked for parks them on the content above it.
   *  Only `revalidateReadingState` revokes the park, when a later size change brings the end
   *  within BOTTOM_TOLERANCE_PX, so a collapse that removed everything below cannot leave them
   *  parked at the end. */
  setUserScrolledUp(v: boolean): void {
    this.setState(v ? "reading" : "following");
  }

  /** Park the reader on `target`: a timeline marker's jump, or a search hit. */
  jumpTo(target: HTMLElement, opts: ScrollIntoViewOptions = {}): void {
    // A jump is a new destination, so the pass serving the previous one dies here — the same
    // ownership rule `attach`, `detach` and `resetScrollState` follow.
    this.cancelPinPass();
    this.endSelfScroll();
    // A jump is the READER moving, so it holds the window the same way a wheel does: nothing may
    // re-derive the state or re-pin under a flight in progress.
    this.userScrollingUntil = Date.now() + READER_CONTROL_MS;
    this.setState(this.landsAtLiveEdge(target, opts.block ?? "start") ? "following" : "reading");
    // Guarded because jsdom does not implement scrollIntoView, and both callers are unit-tested
    // against the DOM they build.
    const fn = (target as { scrollIntoView?: (o?: ScrollIntoViewOptions) => void }).scrollIntoView;
    if (typeof fn === "function") {
      const before = this.scrollEl.scrollTop;
      fn.call(target, { block: "start", behavior: "smooth", ...opts });
      // A landing this module reached is recorded like every write it makes, or the event it
      // produces is read as the READER stating a position — which published a reader gesture and
      // revoked the pick the rail's own click had just set, on every jump that actually moved.
      const landed = this.scrollEl.scrollTop;
      if (landed !== before) {
        this.selfScrollTop = landed;
      }
    }
  }

  /** Would a jump to `target` leave the reader at the live edge? */
  private landsAtLiveEdge(target: HTMLElement, block: ScrollLogicalPosition): boolean {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const box = this.scrollFrameRect(target);
    if (box === null) {
      // No box means no landing, so the jump moves the reader nowhere — and this function's own
      // default is that a jump with nowhere to go keeps them Following rather than raising a resume
      // control over a transcript that did not move.
      return true;
    }
    const room = this.scrollEl.clientHeight - (box.bottom - box.top);
    let wanted = box.top;
    if (block === "center") {
      wanted = box.top - room / 2;
    } else if (block === "end") {
      wanted = box.top - room;
    }
    return this.landsAtOffsetLiveEdge(Math.max(0, Math.min(wanted, max)));
  }

  /** Would landing on `landing` leave the reader at the live edge? The offset twin of
   *  `landsAtLiveEdge`, so `BOTTOM_TOLERANCE_PX` stays inside the module that owns it and both
   *  doors compare against one expression. */
  private landsAtOffsetLiveEdge(landing: number): boolean {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    return landing >= max - BOTTOM_TOLERANCE_PX;
  }

  /** Apply `mutate` now if Following, or queue it until the reader returns. Content must never
   *  disappear from above the reader through no action of their own, which is exactly what a
   *  turn folding while they read does. */
  deferWhileReading(mutate: () => void): void {
    if (this.state === "following") {
      mutate();
      return;
    }
    this.deferred.push(mutate);
  }

  scrollToBottom(): void {
    this.pinToLiveEdge();
  }

  /** Land at the live edge and HOLD it there while the layout settles. The live edge is
   *  `followTarget`, the same number `autoScrollIfAnchored` writes: ONE target for both writers,
   *  or the hand-off at the deadline has to move the reader. */
  private pinToLiveEdge(): void {
    this.setState("following");
    // The reader is ASKING to follow, and the pass's own frames test their licence: a `barDragging`
    // latch left standing — a drag whose pointerup never arrived — would kill the pin on its first
    // frame and leave the resume control looking dead.
    this.forgetReaderGesture();
    this.pinUntil = Date.now() + pinSettleMs;
    this.pinLiveEdgeNow();
    this.queuePinFrame();
    // A request for the live edge is the reader saying where they want to be, so it publishes like
    // a scroll would — and it has to be published HERE, because every write above goes through
    // `scrollSelfTo` and the scroll listener therefore excuses all of them.
    this.publishReaderGesture();
  }

  private queuePinFrame(): void {
    if (this.pinFrame !== 0) {
      return;
    }
    this.pinFrame = requestAnimationFrame(() => {
      this.pinFrame = 0;
      // The reader outranks their own earlier click, and the state alone cannot see that: a gesture
      // landing inside BOTTOM_TOLERANCE_PX keeps Following, so the debounce is the condition that
      // catches it.
      if (this.state !== "following" || this.readerInControl() || Date.now() >= this.pinUntil) {
        this.pinUntil = 0;
        return;
      }
      this.pinLiveEdgeNow();
      this.queuePinFrame();
    });
  }

  /** The clamp is what makes the COMPARE work, rather than tidiness `scrollSelfTo` would repeat:
   *  a follow target of `scrollHeight` is a whole viewport past the maximum, so an unclamped
   *  compare never matches and every frame writes. */
  private pinLiveEdgeNow(): void {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const landing = Math.max(0, Math.min(this.followTarget(), max));
    if (this.scrollEl.scrollTop !== landing) {
      this.scrollSelfTo(landing, "instant");
    }
  }

  /** Where Following belongs: the anchor's pin while a turn streams, the document bottom
   *  otherwise. ONE definition, read by both writers — the streaming re-pin and the bottom pin's
   *  settle pass — so they cannot disagree about the position the state means. */
  private followTarget(): number {
    const anchor = this.anchorProvider?.() ?? null;
    const pin = anchor === null ? null : this.anchorTop(anchor);
    // An anchor with no box to measure is the same answer as no anchor at all: there is no position
    // to follow, so Following means the document bottom.
    return pin ?? this.scrollEl.scrollHeight;
  }

  private cancelPinPass(): void {
    if (this.pinFrame !== 0) {
      cancelAnimationFrame(this.pinFrame);
      this.pinFrame = 0;
    }
    this.pinUntil = 0;
  }

  /** Hand the scroller to a transcript view: re-root the observers on it and restore its saved
   *  reading state and scroll position. The unpark half of the park/unpark pair; a freshly
   *  created view attaches with `{scrollTop: 0, readingState: "following"}`. */
  attach(handle: ViewAttachHandle): void {
    // Before anything else: a live pin pass belongs to the OUTGOING view, and this method is about
    // to write the incoming one's own scrollTop.
    this.cancelPinPass();
    this.endSelfScroll();
    this.observeView(handle.el);
    this.forgetReaderGesture();
    this.setState(handle.readingState);
    this.scrollSelfTo(handle.scrollTop, "instant");
    this.lastScrollTop = this.scrollEl.scrollTop;
    this.lastEdgeDistance = this.edgeDistance();
    // Last, so a listener re-measuring this view reads the restored position.
    for (const cb of [...this.attachListeners]) {
      cb();
    }
  }

  /** Take the scroller away from the current view: snapshot the scroll-owned state for the
   *  view's handle, abandon the pagination pass in flight (its completion signal and its height
   *  compensation both belong to the outgoing transcript), drop the deferred-mutation queue (the
   *  unpark catch-up paint re-derives every fold the queue was holding), and disconnect the
   *  observers so the parked view can never produce a callback. */
  detach(): ViewScrollState {
    const snapshot: ViewScrollState = {
      scrollTop: this.scrollEl.scrollTop,
      readingState: this.state,
    };
    this.deferred = [];
    this.abandonLoadPass();
    this.cancelPinPass();
    this.endSelfScroll();
    this.cancelScrollbarWidth();
    // A queued re-derivation belongs to the OUTGOING view: a frame later it would read the incoming
    // transcript's geometry and release a state that is not its own.
    this.cancelRevalidate();
    this.forgetReaderGesture();
    this.forgetScrollBaselines();
    this.onLoadMore = null;
    this.hasMoreMessages = false;
    this.disconnectView();
    this.setState("following");
    return snapshot;
  }

  setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
    this.onLoadMore = fn;
    this.hasMoreMessages = hasMore;
    this.updateLoadMoreIndicator();
  }

  rebaseLoadMore(): void {
    this.rebaseLoad?.();
  }

  /** Fetch until the scroller actually overflows. */
  fillViewport(): void {
    if (this.scrollEl.scrollHeight > this.scrollEl.clientHeight + BOTTOM_TOLERANCE_PX) {
      return;
    }
    this.maybeLoadMore(true);
  }

  /** How far the transcript can scroll: content height minus viewport height, 0 when it fits. A
   *  pure MEASUREMENT with no threshold applied, because the only caller that wants one (the
   *  turn rail, deciding whether it is worth existing) has a different question from this
   *  module's own bottom-detection tolerance. */
  scrollableBy(): number {
    return Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
  }

  /** Hand the scroller to a different chat, or to no chat at all. */
  resetScrollState(): void {
    this.deferred = [];
    this.abandonLoadPass();
    this.cancelPinPass();
    this.endSelfScroll();
    this.cancelScrollbarWidth();
    this.cancelRevalidate();
    this.forgetReaderGesture();
    this.forgetScrollBaselines();
    this.setLoadMore(null, false);
    this.setState("following");
  }

  private setState(next: ReadingState): void {
    if (next === "following") {
      // Following and a standing upward aim contradict each other, so every door into Following
      // spends it, or the next in-band event parks a reader who stated no direction. Before the
      // early return, because a door that changes no state still has an aim to spend.
      this.upwardIntent = false;
    }
    if (this.state === next) {
      return;
    }
    this.state = next;
    if (next === "reading") {
      // The third publisher, and the one that makes the other two sufficient: Reading MEANS the
      // reader is not at the live edge, so entering it settles the question without a read.
      this.atLiveEdge = false;
    }
    // The single owner of the resume control's visibility: visible ⇔ Reading. Nothing else writes
    // this class, so a caller that has just moved the scroller does not need to hide the control
    // itself.
    $.scrollBottom.classList.toggle("hidden", next === "following");
    if (next === "following") {
      this.flushDeferred();
    }
    for (const cb of this.stateListeners) {
      cb(next);
    }
  }

  /** Apply queued mutations in arrival order: the reader is at the live edge now. */
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

  /** Drop every trace of a gesture in progress: the quiet period, the aim it carried, the held
   *  thumb and the two positions the touch and bar deltas are measured against. Called wherever
   *  the reader's own scroll stops being the question — a resume, and both halves of a view
   *  swap. */
  private forgetReaderGesture(): void {
    this.userScrollingUntil = 0;
    this.upwardIntent = false;
    this.barDragging = false;
    this.lastTouchY = null;
    this.lastBarY = null;
  }

  /** Is the reader working the scroller right now? The one window three rules read: it
   *  suppresses this controller's own writes, it blocks a promotion driven by a size change, and
   *  it is the only licence to enter Reading. */
  private readerInControl(): boolean {
    return this.barDragging || Date.now() < this.userScrollingUntil;
  }

  private isAtBottom(): boolean {
    return (
      this.scrollEl.scrollTop + this.scrollEl.clientHeight >=
      this.scrollEl.scrollHeight - BOTTOM_TOLERANCE_PX
    );
  }

  private edgeDistance(): number {
    return this.scrollEl.scrollHeight - this.scrollEl.clientHeight - this.scrollEl.scrollTop;
  }

  /** Did the browser's scroll anchoring produce this scroll? It moves `scrollTop` to hold the
   *  content in view still, so the element that was at the top edge has not moved. */
  private movedByAnchoring(top: number): boolean {
    const ref = this.viewTop;
    return (
      ref !== null &&
      top !== this.lastScrollTop &&
      ref.el.isConnected &&
      Math.abs(ref.el.getBoundingClientRect().top - ref.top) < 1
    );
  }

  private noteViewTop(): void {
    const box = this.scrollEl.getBoundingClientRect();
    this.viewTop = this.contentAt(box.left + box.width / 2, box.top + 1);
    this.readLine = this.pickReadLine();
  }

  private pickReadLine(): { el: Element; top: number } | null {
    const box = this.scrollEl.getBoundingClientRect();
    const x = box.left + box.width / 2;
    // Preferably the first element that starts in view at or below the line: a box reaching above
    // the scrollport (a body around a gap) does not move when the rows inside it do. A paragraph
    // taller than the scrollport starts in view nowhere, so the line's own element stands.
    const bottom = box.top + this.scrollEl.clientHeight;
    const line = box.top + this.scrollEl.clientHeight * READING_LINE_FRACTION;
    for (let y = line; y < bottom; y += 24) {
      const hit = this.contentAt(x, y);
      if (hit !== null && hit.top >= box.top) {
        return hit;
      }
    }
    return this.contentAt(x, line);
  }

  /** The transcript element at a client point and its top, or null over a gap or a spacer. */
  private contentAt(x: number, y: number): { el: Element; top: number } | null {
    const el = document.elementFromPoint(x, y);
    if (el === null || !this.viewEl.contains(el) || el.closest(".turn-space") !== null) {
      return null;
    }
    return { el, top: el.getBoundingClientRect().top };
  }

  /** Put the reading line's element back where the reader left it, if a size change moved it. */
  private holdReadingLine(): void {
    const line = this.readLine;
    if (line === null || this.state !== "reading" || this.epochOpen) {
      return;
    }
    // A box-less element reads a rect of zeros, which would pass for a drift of its whole offset.
    if (!line.el.isConnected || line.el.getClientRects().length === 0) {
      this.readLine = this.pickReadLine();
      return;
    }
    const top = line.el.getBoundingClientRect().top;
    const drift = top - line.top;
    if (Math.abs(drift) < 1) {
      return;
    }
    const from = this.scrollEl.scrollTop;
    this.scrollSelfTo(from + drift, "instant");
    if (Math.abs(this.scrollEl.scrollTop - from) < 1) {
      // Clamped where it stood: no scroll event follows to re-read the line, and the reader now sees it here.
      this.readLine = { el: line.el, top };
      return;
    }
    this.holding = true;
  }

  private landedOnOwnWrite(): boolean {
    return this.selfScrollTop >= 0 && Math.abs(this.scrollEl.scrollTop - this.selfScrollTop) <= 1;
  }

  private forgetScrollBaselines(): void {
    this.viewTop = null;
    this.readLine = null;
    this.holding = false;
    this.lastScrollTop = -1;
    this.lastEdgeDistance = Number.POSITIVE_INFINITY;
  }

  /** Is the reader holding a position of their own inside the tolerance band? */
  private parkedByOwnAim(movedDown: boolean): boolean {
    return this.upwardIntent && this.readerInControl() && !movedDown;
  }

  /** Release Reading when a size change put the reader back at the end, since a shrink need not
   *  fire a scroll event. One-directional: only the reader may enter Reading. `atBottom` is
   *  passed in because the mutation caller runs with the DOM dirty, where measuring forces a
   *  synchronous layout. */
  private revalidateReadingState(atBottom: boolean): void {
    if (!this.mayReleaseReading()) {
      return;
    }
    if (atBottom) {
      this.setState("following");
    }
  }

  /** Could a size change release Reading right now? */
  private mayReleaseReading(): boolean {
    return this.state === "reading" && !this.readerInControl() && !this.epochOpen;
  }

  /** Pin to the ACTIVE TEXT BLOCK, not to the document bottom. */
  private autoScrollIfAnchored(): void {
    if (!this.mayFollow() || this.rafPending) {
      return;
    }
    this.rafPending = true;
    requestAnimationFrame(() => {
      this.rafPending = false;
      if (!this.mayFollow()) {
        return;
      }
      this.scrollSelfTo(this.followTarget(), "instant");
    });
  }

  /** May a follow write happen? ONE predicate, read twice — once to decide to queue the frame
   *  and again inside it, as `queuePinFrame` re-reads its own conditions, because the licence
   *  can be revoked in between. */
  private mayFollow(): boolean {
    return (
      this.state !== "reading" && this.pinFrame === 0 && !this.readerInControl() && !this.epochOpen
    );
  }

  /** Move the scroller and record where it will LAND, so the `scroll` event the write produces
   *  is recognised as this controller's own. */
  private scrollSelfTo(top: number, behavior: ScrollBehavior): void {
    const max = Math.max(0, this.scrollEl.scrollHeight - this.scrollEl.clientHeight);
    const landing = Math.max(0, Math.min(top, max));
    this.selfScrollTop = landing;
    this.scrollEl.scrollTo({ top: landing, behavior });
  }

  /** Where `el` sits in the SCROLLER'S scroll frame — the frame `scrollTop`, `clientHeight` and
   *  `scrollHeight` are already expressed in — or null when it has no box to report. Rects,
   *  never `offsetTop`: that is measured against `offsetParent`, and a transcript bubble's
   *  offsetParent is its own `.msg-row`, because `content-visibility: auto`
   *  (css/13-messages.css) implies `contain: paint` and a paint-containing box is a containing
   *  block, which is where the offsetParent walk stops. */
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

  /** The scrollTop that puts `anchor`'s BOTTOM at the viewport's bottom, read in the scroller's
   *  own scroll frame and clamped to a real scroll position. Null when the anchor has no box to
   *  measure. */
  private anchorTop(anchor: HTMLElement): number | null {
    const box = this.scrollFrameRect(anchor);
    if (box === null) {
      return null;
    }
    const wanted = box.bottom - this.scrollEl.clientHeight + BOTTOM_TOLERANCE_PX / 2;
    return Math.max(0, Math.min(wanted, this.scrollEl.scrollHeight));
  }

  private maybeLoadMore(force = false): void {
    if (!force && this.scrollEl.scrollTop >= LOAD_MORE_THRESHOLD_PX) {
      return;
    }
    // A smooth flight toward an early turn crosses the threshold on its way, and this pass ends in
    // a drift correction — a second writer inside one animation. The FORCED call is a caller
    // stating a need rather than the listener guessing at one, so it still runs.
    if (!force && this.epochOpen) {
      return;
    }
    if (!this.hasMoreMessages || this.loadingMore || this.onLoadMore === null) {
      return;
    }
    this.loadingMore = true;
    const skel = loadMoreSkeleton();
    skel.id = "load-more-skeleton";
    // Scoped to the attached view: a parked view keeps its own pagination furniture (it is that
    // view's DOM), so a document-wide id lookup could find a sibling view's button and mount this
    // pass's skeleton there.
    const indicator = this.viewEl.querySelector(`[id="load-more-indicator"]`);
    // The skeleton and then the older messages land ABOVE the reader. Scroll anchoring holds them in
    // place except at offset 0, a suppression trigger (css-scroll-anchoring §2.2.2), so only the
    // drift it left is corrected: the height delta would count the page twice wherever it did anchor.
    const ref =
      [...this.viewEl.children].find((c) => c !== indicator && c !== this.edgeSentinel) ?? null;
    let refTop = 0;
    let prevTop = 0;
    let prevHeight = 0;
    const measure = (): void => {
      refTop = ref?.getBoundingClientRect().top ?? 0;
      prevTop = this.scrollEl.scrollTop;
      prevHeight = this.scrollEl.scrollHeight;
    };
    measure();
    const hold = (): void => {
      const drift =
        ref?.isConnected === true
          ? ref.getBoundingClientRect().top - refTop
          : this.scrollEl.scrollHeight - prevHeight - (this.scrollEl.scrollTop - prevTop);
      if (drift !== 0) {
        // Through `scrollSelfTo` so the write is clamped to a reachable landing.
        this.scrollSelfTo(this.scrollEl.scrollTop + drift, "instant");
      }
    };
    if (indicator !== null) {
      indicator.replaceWith(skel);
    } else {
      this.viewEl.prepend(skel);
    }
    hold();
    // Scrolling never moves `ref` in the scroll frame, so while it stands where the skeleton left it nothing has landed
    // above it, and a re-measure is a baseline from before the landing whatever moved the scroller.
    const frameTop = (): number | null =>
      ref === null ? null : (this.scrollFrameRect(ref)?.top ?? null);
    const unlanded = frameTop();
    this.rebaseLoad = (): void => {
      const frame = frameTop();
      if (unlanded !== null && frame !== null && Math.abs(frame - unlanded) < 1) {
        measure();
      }
    };
    this.onLoadMore();
    const observer = new MutationObserver(() => {
      if (document.getElementById("load-more-skeleton") === null) {
        this.endLoadPass();
        hold();
      }
    });
    const safetyTimer = setTimeout(() => {
      this.abandonLoadPass();
    }, 15_000);
    // What makes the pass CANCELLABLE, and it has to be a field because the observer and the timer
    // are locals nothing outside this method can reach.
    this.pendingLoad = (): void => {
      observer.disconnect();
      clearTimeout(safetyTimer);
    };
    observer.observe(this.viewEl, { childList: true });
  }

  /** End the pagination pass in flight, so neither its observer nor its timer can fire again.
   *  Idempotent, and safe to call when there is no pass — the in-flight flag is cleared either
   *  way, so a pass that died before it could be armed cannot wedge pagination off. */
  private endLoadPass(): void {
    const end = this.pendingLoad;
    this.pendingLoad = null;
    this.rebaseLoad = null;
    this.loadingMore = false;
    end?.();
  }

  /** Give up on the pass in flight and take its skeleton down. Order is load-bearing: ending the
   *  pass first stops the removal below from being read as the fetch completing and compensating
   *  with a stale height. */
  private abandonLoadPass(): void {
    this.endLoadPass();
    this.viewEl.querySelector(`[id="load-more-skeleton"]`)?.remove();
  }

  /** A real BUTTON, not inert text. */
  private updateLoadMoreIndicator(): void {
    // Scoped to the attached view — see maybeLoadMore: parked views keep their own furniture, and
    // removing "the" indicator by document id could reach into one of them.
    const existing = this.viewEl.querySelector(`[id="load-more-indicator"]`);
    if (!this.hasMoreMessages || this.onLoadMore === null) {
      existing?.remove();
      return;
    }
    if (existing !== null) {
      return;
    }
    const btn = el(
      "button",
      { id: "load-more-indicator", className: "load-more-btn", type: "button" },
      "Load older messages",
    );
    btn.addEventListener("click", () => {
      this.maybeLoadMore(true);
    });
    this.viewEl.prepend(btn);
  }
}

// Singleton instance + the module's public API.

let instance: ScrollController | null = null;

function getInstance(): ScrollController {
  if (instance === null) {
    instance = new ScrollController($.messages, $.messagesWrap);
    instance.init();
  }
  return instance;
}

/** Deferred DOM access — safe to import before DOMContentLoaded. */
export function getScrollEl(): HTMLElement {
  return getInstance().scrollEl;
}

/** How far the transcript can scroll, in px; 0 when it fits its viewport. */
export function scrollableBy(): number {
  return getInstance().scrollableBy();
}

export function setUserScrolledUp(v: boolean): void {
  getInstance().setUserScrolledUp(v);
}
/** Hand the scroller to a transcript view (unpark / fresh view). */
export function attach(handle: ViewAttachHandle): void {
  getInstance().attach(handle);
}
/** Snapshot and release the current view's scroll state (park). */
export function detach(): ViewScrollState {
  return getInstance().detach();
}
export function jumpTo(target: HTMLElement, opts?: ScrollIntoViewOptions): void {
  getInstance().jumpTo(target, opts);
}
/** Open a self-scroll epoch: every scroll event until it closes is the controller's own
 *  animation rather than a reader gesture. */
export function beginSelfScroll(): void {
  getInstance().beginSelfScroll();
}
/** Close the open epoch. */
export function endSelfScroll(): void {
  getInstance().endSelfScroll();
}
/** Scroll to an absolute offset inside the open epoch, parking the reader unless the landing is
 *  at the live edge. */
export function scrollToOffset(px: number, behavior: ScrollBehavior): void {
  getInstance().scrollToOffset(px, behavior);
}
/** Px from the scrollport's top to the reading line. */
export function readingLineOffset(): number {
  return getInstance().readingLineOffset();
}
/** The published live-edge verdict, aim-aware. */
export function atLiveEdgeNow(): boolean {
  return getInstance().atLiveEdgeNow();
}
/** Register `cb` for a size change in one of the view's own cards; returns the unregister. */
export function onContentResize(cb: () => void): () => void {
  return getInstance().onContentResize(cb);
}
/** Register `cb` for a view taking the scroller (unpark); returns the unregister. */
export function onAttach(cb: () => void): () => void {
  return getInstance().onAttach(cb);
}
export function scrollToBottom(): void {
  getInstance().scrollToBottom();
}
export function setLoadMore(fn: (() => void) | null, hasMore: boolean): void {
  getInstance().setLoadMore(fn, hasMore);
}
/** Re-measure the older-page pass in flight where the reader stands now; a no-op once the page has landed. A writer
 *  that may land the page calls it just before writing, because the browser can move the scroller before that move's
 *  `scroll` event runs. */
export function rebaseLoadMore(): void {
  getInstance().rebaseLoadMore();
}
export function resetScrollState(): void {
  getInstance().resetScrollState();
}
export function readingState(): ReadingState {
  return getInstance().readingState();
}
export function onReadingStateChange(cb: (s: ReadingState) => void): void {
  getInstance().onReadingStateChange(cb);
}
export function onTranscriptMutate(cb: () => void): () => void {
  return getInstance().onTranscriptMutate(cb);
}
export function onReaderGesture(cb: () => void): () => void {
  return getInstance().onReaderGesture(cb);
}
/** Register `cb` for a scroll that has settled into one frame; returns the unregister. */
export function onViewportChange(cb: () => void): () => void {
  return getInstance().onViewportChange(cb);
}
export function setAnchorProvider(fn: (() => HTMLElement | null) | null): void {
  getInstance().setAnchorProvider(fn);
}
export function setResumeLabel(text: string): void {
  getInstance().setResumeLabel(text);
}
export function deferWhileReading(mutate: () => void): void {
  getInstance().deferWhileReading(mutate);
}
export function fillViewport(): void {
  getInstance().fillViewport();
}
/** @internal Test seam: set how long the next bottom pin re-asserts the live edge.
 *  A pin already armed keeps its own deadline. Returns the previous value. */
export function setPinSettleMs(ms: number): number {
  const prev = pinSettleMs;
  pinSettleMs = ms;
  return prev;
}

// Init on load.
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    getInstance();
  });
} else {
  getInstance();
}
