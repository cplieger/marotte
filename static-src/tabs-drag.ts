// Twin of web-terminal-ui src/features/tabs/{index,strip}.ts; when changing drag
// behaviour or its constants, check the other app. Both judge a live drag's viewport
// against the PRESS-time frame, so a move a hold lifted across still ends the drag.

import { announce } from "@cplieger/ui-primitives/announce";

import type { ViewportBox } from "./viewport-frame.js";
import { onViewportChange, viewportBox, viewportMoved } from "./viewport-frame.js";

/** How long a press must hold, and how far it may stray meanwhile, to become a drag.
 *  A `holdMs` of 0 means no hold: travel past `slopPx` starts the drag. */
export interface DragActivation {
  readonly holdMs: number;
  readonly slopPx: number;
}

// 8px is Android's touch slop; 150ms clears its 100ms tap window and wins the race
// against the platform long press. A mouse competes with no native pan, so a time
// gate there is pure latency.
// https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/core/java/android/view/ViewConfiguration.java
export const TOUCH_DRAG: DragActivation = { holdMs: 150, slopPx: 8 };
export const PEN_DRAG: DragActivation = { holdMs: 150, slopPx: 8 };
export const MOUSE_DRAG: DragActivation = { holdMs: 0, slopPx: 5 };

/** How long (ms) the pointer must have been un-moved before a position report at an
 *  unchanged position is believed as "stopped". A still pointer delivers no
 *  pointermove, so a live drag re-reports its last position on a tick of this
 *  length; `REORDER_REST_MS` is the net for a tick that gets starved. */
export const REORDER_STILL_MS = 50;

/** Fallback: commit the pending slot this long after the last MOVEMENT. */
export const REORDER_REST_MS = 450;

/** Travel (px) along the strip's axis below which the pointer counts as still, so a
 *  resting hand's tremor does not keep pushing the slot out. */
export const REORDER_MOVE_EPS_PX = 3;

/** The slide every displaced row takes. `translate`, never `transform`: a running
 *  CSS animation on `transform` (a row's entry) outranks the inline style. */
export const REORDER_SHIFT_TRANS = "translate 0.2s cubic-bezier(0.2, 0, 0, 1)";

/** When the inline slide comes off the rows and the stylesheet has them back. A
 *  margin past the transition rather than its end event, which an interrupted
 *  transition never fires. */
export const REORDER_SETTLE_MS = 300;

/** How long `.tab-slotted` stays on a row that just took the slot. */
export const REORDER_SLOT_FADE_MS = 300;

/** The activation rules for a `PointerEvent.pointerType`. An unknown type takes the
 *  touch rules, because hold-plus-slop can never hijack a scroll and distance-only
 *  can. */
export function pointerDragActivation(pointerType: string): DragActivation {
  switch (pointerType) {
    case "mouse":
      return MOUSE_DRAG;
    case "pen":
      return PEN_DRAG;
    default:
      return TOUCH_DRAG;
  }
}

/** Whether travel `(dx, dy)` from the press origin is past `rule`'s slop. ONE metric
 *  for the strip: the drag reads it, and so does `tabs.ts`'s tap guard, because two
 *  metrics could call one gesture both a reorder and an activation. */
export function exceedsSlop(dx: number, dy: number, rule: DragActivation): boolean {
  return Math.hypot(dx, dy) > rule.slopPx;
}

/** The last position seen along the strip's axis, and when it last moved by more than
 *  REORDER_MOVE_EPS_PX. */
export interface RestState {
  at: number | null;
  movedAt: number;
}

/** Folds one position report into `rest` and answers whether the pointer has now been
 *  still for REORDER_STILL_MS. */
export function noteRestSample(rest: RestState, pos: number, now: number): boolean {
  const moved = rest.at === null || Math.abs(pos - rest.at) > REORDER_MOVE_EPS_PX;
  rest.at = pos;
  if (moved) {
    rest.movedAt = now;
  }
  return !moved && now - rest.movedAt >= REORDER_STILL_MS;
}

/** How a drag ended. `commit` and `cancelled` both arrive on a pointer RELEASE, so
 *  both suppress the click that release would otherwise fire on the row; `tap` is a
 *  lifted hold released without travel, which is the reader's tap and activates the
 *  row; `abandoned` has no release of its own, so it suppresses nothing, or the NEXT
 *  gesture's click would be swallowed and the strip would stop answering taps. */
type DragEnd = "commit" | "cancelled" | "tap" | "abandoned";

/** A press that has not become a drag yet. ONE per controller rather than a
 *  closure per row: a release ANYWHERE has to disarm it, and the strip has one
 *  primary gesture at a time. */
interface PendingPress {
  readonly el: HTMLElement;
  readonly list: HTMLElement | null;
  readonly pointerId: number;
  readonly pointerType: string;
  readonly clientX: number;
  readonly clientY: number;
  readonly frame: ViewportBox;
  holdTimer: ReturnType<typeof setTimeout> | null;
  releaseViewport: () => void;
}

function isSlotRow(node: Element | null): node is HTMLElement {
  return (
    node instanceof HTMLElement &&
    node.dataset["tabId"] !== undefined &&
    !node.hasAttribute("data-drag-collapsed") &&
    !node.classList.contains("exiting")
  );
}

function topLevelOrder(list: HTMLElement): string[] {
  return [...list.children].filter(isSlotRow).map((c) => c.dataset["tabId"] ?? "");
}

/** The next row a drop could land before, past a dragged parent's folded children. */
function nextSlotSibling(row: HTMLElement): HTMLElement | null {
  let next = row.nextElementSibling;
  while (next !== null && !isSlotRow(next)) {
    next = next.nextElementSibling;
  }
  return next;
}

/** A row's top in the list's content frame, from LAYOUT offsets, so a row a slide is
 *  drawing elsewhere is hit-tested where it sits. `#tab-list` is unpositioned in the
 *  app and positioned in the tests, so both arms are live. */
function rowTopInList(row: HTMLElement, list: HTMLElement): number {
  return row.offsetParent === list ? row.offsetTop : row.offsetTop - list.offsetTop;
}

function sameOrder(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((id, i) => id === b[i]);
}

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

class TabDragController {
  private dragEl: HTMLElement | null = null;
  private dragList: HTMLElement | null = null;
  private dragGhost: HTMLElement | null = null;
  private dragOriginX = 0;
  private dragOriginY = 0;
  private dragRule: DragActivation = TOUCH_DRAG;
  private dragMoved = false;
  private dragStartOrder: readonly string[] = [];
  private rest: RestState = { at: null, movedAt: 0 };
  private stillTick: ReturnType<typeof setInterval> | null = null;
  private restTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly shifted = new Set<HTMLElement>();
  private shiftTimer: ReturnType<typeof setTimeout> | null = null;
  private slotFadeEl: HTMLElement | null = null;
  private slotFadeTimer: ReturnType<typeof setTimeout> | null = null;
  private dragHandled = false;
  private dragFrame: ViewportBox = { offsetLeft: 0, offsetTop: 0, width: 0, height: 0 };
  private releaseViewport: (() => void) | null = null;
  private reorderCallback: ((order: string[]) => readonly string[] | null) | null = null;
  private reprojectCallback: (() => void) | null = null;
  private tapCallback: ((tabID: string) => void) | null = null;
  private pending: PendingPress | null = null;

  // Bound handlers for add/removeEventListener identity.
  private readonly boundDragMove = (e: PointerEvent): void => {
    this.onDragMove(e);
  };
  private readonly boundRelease = (e: PointerEvent): void => {
    this.onRelease(e);
  };
  private readonly boundCancelled = (): void => {
    this.endDrag("cancelled");
  };
  private readonly boundAbandon = (): void => {
    this.endDrag("abandoned");
  };
  // `lostpointercapture` bubbles, and the row's implicit touch capture ending when
  // the list takes it over is not a loss.
  private readonly boundLostCapture = (e: Event): void => {
    if (e.target === this.dragList) {
      this.endDrag("abandoned");
    }
  };
  private readonly boundKeydown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      this.endDrag("abandoned");
    }
  };
  private readonly boundVisibility = (): void => {
    if (document.visibilityState === "hidden") {
      this.endDrag("abandoned");
    }
  };
  // A long press that has not moved belongs to the row's own menu, so the lift
  // yields to it; once the row is travelling, a platform long-press must not open a
  // menu under the finger.
  private readonly boundContextMenu = (e: Event): void => {
    if (this.dragMoved) {
      e.preventDefault();
      e.stopPropagation();
    } else {
      this.endDrag("abandoned");
    }
  };
  // A TRIGGER, not a decision: `visualViewport`'s `scroll` also fires on an
  // ordinary page scroll with the geometry unchanged.
  private readonly boundViewportChange = (): void => {
    if (viewportMoved(viewportBox(), this.dragFrame)) {
      this.endDrag("abandoned");
    }
  };
  // A press whose viewport moved under it can no longer be verified: the row that
  // was under the pointer may not be under it now, so the reader presses again.
  private readonly boundPendingViewport = (): void => {
    if (this.pending !== null && viewportMoved(viewportBox(), this.pending.frame)) {
      this.clearPending();
    }
  };
  // A press landing during a momentum scroll must not lift.
  private readonly boundPendingScroll = (): void => {
    this.clearPending();
  };
  private readonly boundPendingMove = (e: PointerEvent): void => {
    this.onPressMove(e);
  };
  private readonly boundPendingRelease = (e: PointerEvent): void => {
    if (this.pending?.pointerId === e.pointerId) {
      this.clearPending();
    }
  };
  private readonly boundPendingPointerDown = (e: PointerEvent): void => {
    if (this.pending !== null && this.pending.pointerId !== e.pointerId) {
      this.clearPending();
    }
  };
  private readonly boundPendingKeydown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      this.clearPending();
    }
  };
  private readonly boundPendingVisibility = (): void => {
    if (document.visibilityState === "hidden") {
      this.clearPending();
    }
  };
  private readonly boundPendingBlur = (): void => {
    this.clearPending();
  };
  // `setPointerCapture` routes events and does not stop a pan, and `touch-action`
  // is fixed when the gesture begins (Pointer Events 3), so a live drag's movement
  // is kept from the scroller here. Bound per row at attach rather than at lift,
  // because a non-passive listener added mid-gesture is not honoured by Safari.
  // https://www.w3.org/TR/pointerevents3/#determining-supported-direct-manipulation-behavior
  private readonly boundTouchMove = (e: TouchEvent): void => {
    if (this.dragEl !== null && e.cancelable) {
      e.preventDefault();
    }
  };

  /** Whether a drag interaction just completed — used to suppress the
   *  pointerup click that would otherwise fire on the tab element. */
  isDragHandled(): boolean {
    return this.dragHandled;
  }

  /** Whether a drag holds the strip: true from the lift until the drag ends. */
  ownsStrip(): boolean {
    return this.dragEl !== null;
  }

  setReorderCallback(fn: (order: string[]) => readonly string[] | null): void {
    this.reorderCallback = fn;
  }

  setReprojectCallback(fn: () => void): void {
    this.reprojectCallback = fn;
  }

  setTapCallback(fn: (tabID: string) => void): void {
    this.tapCallback = fn;
  }

  /** Attach drag-to-reorder behavior to a tab element. */
  attachDrag(tabEl: HTMLElement): void {
    tabEl.addEventListener("pointerdown", (e) => {
      if ((e.target as HTMLElement).closest(".tab-close") !== null) {
        return;
      }
      if (!e.isPrimary) {
        return;
      }
      this.arm(tabEl, e);
    });
    tabEl.addEventListener("touchmove", this.boundTouchMove, { passive: false });
  }

  private reproject(): void {
    this.reprojectCallback?.();
  }

  private arm(tabEl: HTMLElement, e: PointerEvent): void {
    this.clearPending();
    const list = tabEl.parentElement;
    const pending: PendingPress = {
      el: tabEl,
      list,
      pointerId: e.pointerId,
      pointerType: e.pointerType,
      clientX: e.clientX,
      clientY: e.clientY,
      frame: viewportBox(),
      holdTimer: null,
      releaseViewport: onViewportChange(this.boundPendingViewport),
    };
    this.pending = pending;
    list?.addEventListener("scroll", this.boundPendingScroll, { passive: true });
    const { holdMs } = pointerDragActivation(e.pointerType);
    if (holdMs > 0) {
      pending.holdTimer = setTimeout(() => {
        pending.holdTimer = null;
        if (this.pending === pending) {
          this.startDrag(pending);
        }
      }, holdMs);
    }
    // Capture phase on `window`, so moves and releases the row never sees still reach the arm. No
    // `isPrimary` check: the pointerId decides, so a non-primary release cannot clear a primary arm.
    window.addEventListener("pointermove", this.boundPendingMove, true);
    window.addEventListener("pointerup", this.boundPendingRelease, true);
    window.addEventListener("pointercancel", this.boundPendingRelease, true);
    window.addEventListener("pointerdown", this.boundPendingPointerDown, true);
    window.addEventListener("keydown", this.boundPendingKeydown);
    window.addEventListener("blur", this.boundPendingBlur);
    document.addEventListener("visibilitychange", this.boundPendingVisibility);
  }

  private clearPending(): void {
    const pending = this.pending;
    if (pending === null) {
      return;
    }
    this.pending = null;
    if (pending.holdTimer !== null) {
      clearTimeout(pending.holdTimer);
    }
    pending.releaseViewport();
    pending.list?.removeEventListener("scroll", this.boundPendingScroll);
    window.removeEventListener("pointermove", this.boundPendingMove, true);
    window.removeEventListener("pointerup", this.boundPendingRelease, true);
    window.removeEventListener("pointercancel", this.boundPendingRelease, true);
    window.removeEventListener("pointerdown", this.boundPendingPointerDown, true);
    window.removeEventListener("keydown", this.boundPendingKeydown);
    window.removeEventListener("blur", this.boundPendingBlur);
    document.removeEventListener("visibilitychange", this.boundPendingVisibility);
  }

  private onPressMove(e: PointerEvent): void {
    const pending = this.pending;
    if (pending?.pointerId !== e.pointerId) {
      return;
    }
    // A held button is the one thing a reflow cannot fake, which is what makes the
    // gate independent of any coordinate frame.
    if (e.buttons === 0) {
      return;
    }
    // A viewport event waits for the next rendering step, so this move can read the
    // moved geometry before it arrives, and a mouse lifts on the move itself.
    if (viewportMoved(viewportBox(), pending.frame)) {
      this.clearPending();
      return;
    }
    const rule = pointerDragActivation(pending.pointerType);
    if (!exceedsSlop(e.clientX - pending.clientX, e.clientY - pending.clientY, rule)) {
      return;
    }
    if (rule.holdMs > 0) {
      // Travel before the hold completes is a scroll, and it stays the scroller's.
      this.clearPending();
      return;
    }
    this.startDrag(pending);
    this.onDragMove(e);
  }

  /** Fold sub-tabs into their parent during the drag (their position is derived, not dragged). */
  private setChildrenCollapsed(list: HTMLElement | null, on: boolean): void {
    for (const c of list?.querySelectorAll<HTMLElement>(".tab-child") ?? []) {
      if (on) {
        c.dataset["dragCollapsed"] = "";
      } else {
        delete c.dataset["dragCollapsed"];
      }
    }
  }

  private startDrag(press: PendingPress): void {
    this.clearPending();
    this.endShift();
    this.endSlotFade();
    const tabEl = press.el;
    const list = tabEl.parentElement;
    this.dragEl = tabEl;
    this.dragList = list;
    this.dragFrame = press.frame;
    this.dragOriginX = press.clientX;
    this.dragOriginY = press.clientY;
    this.dragRule = pointerDragActivation(press.pointerType);
    this.dragMoved = false;
    this.rest = { at: null, movedAt: 0 };
    this.setChildrenCollapsed(list, true);
    this.dragStartOrder = list === null ? [] : topLevelOrder(list);

    const rect = tabEl.getBoundingClientRect();
    // Cloned BEFORE the row takes the slot's look, so the ghost is the pristine row.
    this.dragGhost = tabEl.cloneNode(true) as HTMLElement;
    this.dragGhost.classList.add("tab-drag-ghost");
    this.dragGhost.style.width = `${String(rect.width)}px`;
    this.dragGhost.style.left = `${String(rect.left)}px`;
    this.dragGhost.style.top = `${String(rect.top)}px`;
    document.body.appendChild(this.dragGhost);

    tabEl.classList.add("dragging");
    document.body.classList.add("tab-dragging");

    // `window` in the CAPTURE phase for the pointer trio, because pointer capture
    // retargeting the release is exactly the thing that fails when a drag gets
    // stuck. `pointermove` there too, so a drag survives a capture that never took.
    window.addEventListener("pointermove", this.boundDragMove);
    window.addEventListener("pointerup", this.boundRelease, true);
    window.addEventListener("pointercancel", this.boundCancelled, true);
    window.addEventListener("pointerdown", this.boundAbandon, true);
    window.addEventListener("contextmenu", this.boundContextMenu, true);
    window.addEventListener("keydown", this.boundKeydown);
    window.addEventListener("blur", this.boundAbandon);
    document.addEventListener("visibilitychange", this.boundVisibility);
    // `abandoned`: after a real release the window listeners already ended the drag, so this lost
    // capture another way; there is no release for the 80ms click suppression to swallow.
    list?.addEventListener("lostpointercapture", this.boundLostCapture);
    this.releaseViewport = onViewportChange(this.boundViewportChange);

    // On the LIST, which the preview never moves (re-inserting the row would release capture). LAST:
    // it can throw `NotFoundError`, which must leave an endable drag.
    list?.setPointerCapture(press.pointerId);
  }

  private detachDragListeners(list: HTMLElement | null): void {
    window.removeEventListener("pointermove", this.boundDragMove);
    window.removeEventListener("pointerup", this.boundRelease, true);
    window.removeEventListener("pointercancel", this.boundCancelled, true);
    window.removeEventListener("pointerdown", this.boundAbandon, true);
    window.removeEventListener("contextmenu", this.boundContextMenu, true);
    window.removeEventListener("keydown", this.boundKeydown);
    window.removeEventListener("blur", this.boundAbandon);
    document.removeEventListener("visibilitychange", this.boundVisibility);
    list?.removeEventListener("lostpointercapture", this.boundLostCapture);
    this.releaseViewport?.();
    this.releaseViewport = null;
  }

  private onDragMove(e: PointerEvent): void {
    if (this.dragEl === null || this.dragGhost === null) {
      return;
    }
    this.dragGhost.style.translate = `0 ${String(e.clientY - this.dragOriginY)}px`;
    if (!this.dragMoved) {
      if (!exceedsSlop(e.clientX - this.dragOriginX, e.clientY - this.dragOriginY, this.dragRule)) {
        return;
      }
      this.dragMoved = true;
      this.stillTick = setInterval(() => {
        if (this.rest.at !== null) {
          this.trackRest(this.rest.at);
        }
      }, REORDER_STILL_MS);
    }
    this.trackRest(e.clientY);
  }

  /** The first row whose layout midpoint is below the pointer, or null for the end.
   *  `scrollTop` is in the sum, which is what lets a wheel scroll during a drag
   *  re-aim the slot. */
  private dropTargetBefore(clientY: number): HTMLElement | null {
    const list = this.dragList;
    if (list === null) {
      return null;
    }
    const y = clientY - list.getBoundingClientRect().top - list.clientTop + list.scrollTop;
    for (const child of list.children) {
      if (child === this.dragEl || !isSlotRow(child)) {
        continue;
      }
      if (y < rowTopInList(child, list) + child.offsetHeight / 2) {
        return child;
      }
    }
    return null;
  }

  /** A travelling pointer rearranges nothing; the slot opens when it comes to rest. */
  private trackRest(clientY: number): void {
    const dragged = this.dragEl;
    if (dragged === null) {
      return;
    }
    const still = noteRestSample(this.rest, clientY, Date.now());
    // Recomputed from THIS report, never a stored pending target.
    const before = this.dropTargetBefore(clientY);
    if (before === nextSlotSibling(dragged)) {
      this.endRestNet();
      return;
    }
    if (still) {
      this.endRestNet();
      this.commitSlot(before);
      return;
    }
    this.armRestNet(before);
  }

  private armRestNet(before: HTMLElement | null): void {
    this.endRestNet();
    this.restTimer = setTimeout(() => {
      this.restTimer = null;
      this.commitSlot(before);
    }, REORDER_REST_MS);
  }

  private endRestNet(): void {
    if (this.restTimer !== null) {
      clearTimeout(this.restTimer);
      this.restTimer = null;
    }
  }

  private stopTick(): void {
    if (this.stillTick !== null) {
      clearInterval(this.stillTick);
      this.stillTick = null;
    }
  }

  /** Move the dragged row itself into the slot before `before` (null: the end). */
  private commitSlot(before: HTMLElement | null): void {
    const dragged = this.dragEl;
    const list = this.dragList;
    if (dragged?.isConnected !== true || list === null) {
      return;
    }
    // A render can remove a row while the gesture is open, and insertBefore throws
    // on a reference that is no longer a child.
    if (before !== null && before.parentNode !== list) {
      return;
    }
    if (before === dragged || before === nextSlotSibling(dragged)) {
      return;
    }
    this.flipTo(() => {
      // A re-seat blurs the row, which would drop a keyboard user's place.
      const hadFocus = dragged.contains(document.activeElement);
      list.insertBefore(dragged, before);
      if (hadFocus) {
        dragged.focus({ preventScroll: true });
      }
    }, dragged);
    this.flashSlot(dragged);
    this.announceTarget(dragged);
  }

  /** FLIP a rearranging mutation so every row that ends up somewhere new slides
   *  there. `hold` is the one row that must not slide. */
  private flipTo<T>(mutate: () => T, hold: HTMLElement | null): T {
    const list = this.dragList;
    if (list === null || prefersReducedMotion()) {
      this.endShift();
      return mutate();
    }
    // Rects, because this is VISUAL position, mid-slide included, so a second
    // commit continues from wherever the first got to.
    const first = new Map<HTMLElement, number>();
    for (const row of list.querySelectorAll<HTMLElement>(":scope > [data-tab-id]")) {
      if (row !== hold) {
        first.set(row, row.getBoundingClientRect().top);
      }
    }
    this.endShift();
    const result = mutate();
    const invert = new Map<HTMLElement, number>();
    for (const [row, was] of first) {
      const dy = row.isConnected ? was - row.getBoundingClientRect().top : 0;
      if (Math.abs(dy) >= 0.5) {
        invert.set(row, dy);
      }
    }
    if (invert.size === 0) {
      return result;
    }
    this.applyShift(invert, "none");
    // The read commits the inverted state; without it the two writes collapse into
    // one style change and the row snaps instead of sliding.
    list.getBoundingClientRect();
    this.applyShift(new Map([...invert.keys()].map((row) => [row, 0])), REORDER_SHIFT_TRANS);
    this.shiftTimer = setTimeout(() => {
      this.endShift();
    }, REORDER_SETTLE_MS);
    return result;
  }

  private applyShift(px: ReadonlyMap<HTMLElement, number>, trans: string): void {
    if (this.shiftTimer !== null) {
      clearTimeout(this.shiftTimer);
      this.shiftTimer = null;
    }
    for (const [row, dy] of px) {
      row.style.transition = trans;
      row.style.translate = `0 ${String(Math.round(dy))}px`;
      this.shifted.add(row);
    }
  }

  /** Hands every displaced row back to the stylesheet. Idempotent. */
  private endShift(): void {
    if (this.shiftTimer !== null) {
      clearTimeout(this.shiftTimer);
      this.shiftTimer = null;
    }
    for (const row of this.shifted) {
      row.style.transition = "";
      row.style.translate = "";
    }
    this.shifted.clear();
  }

  private flashSlot(row: HTMLElement): void {
    this.endSlotFade();
    // The layout read between the two writes keeps them from collapsing into one,
    // which would restart nothing.
    row.classList.remove("tab-slotted");
    row.getBoundingClientRect();
    row.classList.add("tab-slotted");
    this.slotFadeEl = row;
    this.slotFadeTimer = setTimeout(() => {
      this.slotFadeTimer = null;
      this.endSlotFade();
    }, REORDER_SLOT_FADE_MS);
  }

  private endSlotFade(): void {
    if (this.slotFadeTimer !== null) {
      clearTimeout(this.slotFadeTimer);
      this.slotFadeTimer = null;
    }
    this.slotFadeEl?.classList.remove("tab-slotted");
    this.slotFadeEl = null;
  }

  /** A slot is a PREVIEW Escape can still undo, so it announces a target and only
   *  the drop announces a move. */
  private announceTarget(row: HTMLElement): void {
    const list = this.dragList;
    const at = list === null ? -1 : topLevelOrder(list).indexOf(row.dataset["tabId"] ?? "");
    if (at >= 0) {
      announce(`Drop position ${String(at + 1)}`);
    }
  }

  private announceMoved(row: HTMLElement, order: readonly string[]): void {
    const at = order.indexOf(row.dataset["tabId"] ?? "");
    if (at >= 0) {
      const name = row.querySelector(".tab-name")?.textContent ?? "tab";
      announce(`Moved ${name} to position ${String(at + 1)}`);
    }
  }

  /** Put the strip back in the committed order, sliding the rows home. */
  private revertPreview(announceCancel: boolean): void {
    this.flipTo(() => {
      this.reproject();
    }, null);
    if (announceCancel) {
      announce("Move cancelled");
    }
  }

  /** A release decides by POSITION, never by a slot a timer had pending. One outside
   *  the strip is a refused drop and reverts, as an HTML5 drop outside its target
   *  does. */
  private onRelease(e: PointerEvent): void {
    this.endRestNet();
    if (!this.dragMoved) {
      const row = this.dragEl;
      // Under the list's capture the release targets the list; it reaches the row
      // only when the capture never took, and then the row's own listener answers.
      const reachesRow = e.target instanceof Node && row?.contains(e.target) === true;
      this.endDrag("tap");
      if (row !== null && !reachesRow) {
        this.tapCallback?.(row.dataset["tabId"] ?? "");
      }
      return;
    }
    const box = this.dragList?.getBoundingClientRect();
    const inside =
      box !== undefined &&
      e.clientX >= box.left &&
      e.clientX <= box.right &&
      e.clientY >= box.top &&
      e.clientY <= box.bottom;
    if (!inside) {
      this.endDrag("cancelled");
      return;
    }
    this.commitSlot(this.dropTargetBefore(e.clientY));
    this.endDrag("commit");
  }

  /** Take the strip back to a settled state: every end re-projects (a commit from its order, else
   *  the held order). Idempotent, since several recovery paths can fire in one turn. */
  private endDrag(end: DragEnd): void {
    const tabEl = this.dragEl;
    if (tabEl === null) {
      return;
    }
    const list = this.dragList;
    // Read BEFORE the children unfold, so it is top-level ids only — which is what
    // the tab store persists and what reorderTabs re-anchors children against.
    const order = list === null ? [...this.dragStartOrder] : topLevelOrder(list);
    const changed = !sameOrder(order, this.dragStartOrder);
    this.stopTick();
    this.endRestNet();
    this.endSlotFade();
    this.dragGhost?.remove();
    this.dragGhost = null;
    tabEl.classList.remove("dragging");
    this.detachDragListeners(list);
    document.body.classList.remove("tab-dragging");
    // Over BEFORE any callback, so a render the callbacks cause re-seats rows.
    this.dragEl = null;
    this.dragStartOrder = [];
    this.rest = { at: null, movedAt: 0 };
    if (end === "commit" && changed) {
      this.setChildrenCollapsed(list, false);
      // The projection may undo the drop (the pin partition), so the order it
      // applied, not the previewed one, is what slides in and what is announced.
      const applied = this.flipTo(() => {
        const result = this.reorderCallback?.(order) ?? null;
        this.reproject();
        return result;
      }, null);
      if (applied === null) {
        announce("Move cancelled");
      } else {
        this.announceMoved(tabEl, applied);
      }
    } else {
      this.setChildrenCollapsed(list, false);
      this.revertPreview(changed);
    }
    this.dragList = null;
    if (end === "commit" || end === "cancelled") {
      this.dragHandled = true;
      setTimeout(() => {
        this.dragHandled = false;
      }, 80);
    }
  }
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new TabDragController();

/** Whether a drag interaction just completed — used to suppress the
 *  pointerup click that would otherwise fire on the tab element. */
export function isDragHandled(): boolean {
  return instance.isDragHandled();
}

/** Whether a live drag holds the strip's order: from the lift until the drag
 *  starts ending. A render in that window must not re-seat an existing row. */
export function dragOwnsStrip(): boolean {
  return instance.ownsStrip();
}

/** Set the callback invoked when a drag commits a changed top-level order. It
 *  answers the top-level order it applied, or null when it applied none. */
export function setReorderCallback(fn: (order: string[]) => readonly string[] | null): void {
  instance.setReorderCallback(fn);
}

/** Set the callback that re-renders the strip's rows in the committed order. Every
 *  end of a drag calls it, so a cancel and an unchanged drop both put the rows back. */
export function setReprojectCallback(fn: () => void): void {
  instance.setReprojectCallback(fn);
}

/** Set the callback that activates a tab when a lifted hold is released as a tap. */
export function setTapCallback(fn: (tabID: string) => void): void {
  instance.setTapCallback(fn);
}

/** Attach drag-to-reorder behavior to a tab element. */
export function attachDrag(tabEl: HTMLElement): void {
  instance.attachDrag(tabEl);
}
