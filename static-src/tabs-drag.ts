// ---------------------------------------------------------------------------
// Tab drag-to-reorder interaction (Pointer Events, unified mouse + touch).
// Extracted from tabs.ts — self-contained subsystem that calls back into the
// tab store only via the reorderTabs callback at the end of a drag.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

import type { ViewportBox } from "./viewport-frame.js";
import { onViewportChange, viewportBox, viewportMoved } from "./viewport-frame.js";

/** How far a primary pointer may travel before the strip reads a DRAG rather than
 *  a tap. One number for the whole strip: it arms the mouse reorder drag here, and
 *  `tabs.ts` reads it to decide whether a release still activates the row it
 *  started on. Two thresholds could disagree, and a gesture that is a drag to one
 *  and a tap to the other both reorders and activates. */
export const DRAG_THRESHOLD_PX = 5;
const DRAG_HOLD_MS = 300;

/** How a drag ended. `commit` and `cancelled` both arrive on a pointer RELEASE, so
 *  both suppress the click that release would otherwise fire on the row;
 *  `abandoned` has no release of its own, and swallowing the NEXT gesture's is the
 *  reported "it does not recover, it breaks further". */
type DragEnd = "commit" | "cancelled" | "abandoned";

/** A press that has not become a drag yet. ONE per controller rather than a
 *  closure per row: a release ANYWHERE has to disarm it, and the strip has one
 *  primary gesture at a time. */
interface PendingPress {
  readonly el: HTMLElement;
  readonly pointerId: number;
  clientY: number;
  frame: ViewportBox;
  holdTimer: ReturnType<typeof setTimeout> | null;
}

class TabDragController {
  private dragEl: HTMLElement | null = null;
  private dragGhost: HTMLElement | null = null;
  private dragIndicator: HTMLElement | null = null;
  private dragOffsetY = 0;
  private dragTargetIdx = -1;
  private dragHandled = false;
  private dragFrame: ViewportBox = { offsetLeft: 0, offsetTop: 0, width: 0, height: 0 };
  private releaseViewport: (() => void) | null = null;
  private reorderCallback: ((order: string[]) => void) | null = null;
  private pending: PendingPress | null = null;

  // Bound handlers for add/removeEventListener identity.
  private readonly boundDragMove = (e: PointerEvent): void => {
    this.onDragMove(e);
  };
  private readonly boundCommit = (): void => {
    this.endDrag("commit");
  };
  private readonly boundCancelled = (): void => {
    this.endDrag("cancelled");
  };
  private readonly boundAbandon = (): void => {
    this.endDrag("abandoned");
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
  // A TRIGGER, not a decision: `visualViewport`'s `scroll` also fires on an
  // ordinary page scroll with the geometry unchanged.
  private readonly boundViewportChange = (): void => {
    if (viewportMoved(viewportBox(), this.dragFrame)) {
      this.endDrag("abandoned");
    }
  };
  private readonly boundPendingRelease = (e: PointerEvent): void => {
    if (this.pending?.pointerId === e.pointerId) {
      this.clearPending();
    }
  };

  /** Whether a drag interaction just completed — used to suppress the
   *  pointerup click that would otherwise fire on the tab element. */
  isDragHandled(): boolean {
    return this.dragHandled;
  }

  /** Set the callback invoked when a drag completes with the new tab order. */
  setReorderCallback(fn: (order: string[]) => void): void {
    this.reorderCallback = fn;
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

    tabEl.addEventListener("pointermove", (e) => {
      this.onPressMove(tabEl, e);
    });
  }

  private arm(tabEl: HTMLElement, e: PointerEvent): void {
    this.clearPending();
    const pending: PendingPress = {
      el: tabEl,
      pointerId: e.pointerId,
      clientY: e.clientY,
      frame: viewportBox(),
      holdTimer: null,
    };
    this.pending = pending;
    if (e.pointerType === "touch") {
      pending.holdTimer = setTimeout(() => {
        pending.holdTimer = null;
        if (this.pending !== pending) {
          return;
        }
        // A hold whose baseline moved cannot be verified, and restarting the timer
        // under the reader's finger is worse than asking for a fresh press.
        if (viewportMoved(viewportBox(), pending.frame)) {
          this.clearPending();
          return;
        }
        this.startDrag(tabEl, pending.clientY, pending.pointerId);
      }, DRAG_HOLD_MS);
    }
    // Capture phase on `window`, so a release the row never sees still disarms:
    // the row moved out from under the pointer, or the release landed elsewhere.
    // No `isPrimary` condition — a non-primary release must not clear a primary
    // arm, and the pointerId is what says so.
    window.addEventListener("pointerup", this.boundPendingRelease, true);
    window.addEventListener("pointercancel", this.boundPendingRelease, true);
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
    window.removeEventListener("pointerup", this.boundPendingRelease, true);
    window.removeEventListener("pointercancel", this.boundPendingRelease, true);
  }

  private onPressMove(tabEl: HTMLElement, e: PointerEvent): void {
    const pending = this.pending;
    if (pending?.el !== tabEl || pending.pointerId !== e.pointerId) {
      return;
    }
    // A held button is the one thing a reflow cannot fake, which is what makes the
    // gate independent of any coordinate frame.
    if (e.buttons === 0) {
      return;
    }
    const frame = viewportBox();
    if (viewportMoved(frame, pending.frame)) {
      pending.clientY = e.clientY;
      pending.frame = frame;
      return;
    }
    if (e.pointerType !== "touch" && Math.abs(e.clientY - pending.clientY) > DRAG_THRESHOLD_PX) {
      this.startDrag(tabEl, e.clientY, e.pointerId);
    }
  }

  /** The rows a drag reorders: everything except the dragged row, the drop
   *  indicator, and any sub-tab collapsed for the duration of the drag.
   *
   *  Excluding collapsed children is not cosmetic. The indicator's target index
   *  is a position in THIS list and the dropped order is read back from it, so a
   *  hidden child left in the list would contribute a zero-height rect that can
   *  never be hit and an id at a position nothing can be dropped into. */
  private draggableSiblings(list: HTMLElement): HTMLElement[] {
    return [...list.children].filter(
      (c) =>
        c !== this.dragEl &&
        c !== this.dragIndicator &&
        !(c as HTMLElement).hasAttribute("data-drag-collapsed"),
    ) as HTMLElement[];
  }

  /** Fold sub-tabs into their parent for the duration of the drag, and unfold on
   *  drop. Without this, dragging a parent past its own children reads as
   *  chaos — the children do not move with it, because their position is derived
   *  rather than dragged. */
  private setChildrenCollapsed(list: HTMLElement | null, on: boolean): void {
    for (const c of list?.querySelectorAll<HTMLElement>(".tab-child") ?? []) {
      if (on) {
        c.dataset["dragCollapsed"] = "";
      } else {
        delete c.dataset["dragCollapsed"];
      }
    }
  }

  private startDrag(tabEl: HTMLElement, clientY: number, pointerId: number): void {
    this.clearPending();
    this.dragEl = tabEl;
    this.dragFrame = viewportBox();
    this.setChildrenCollapsed(tabEl.parentElement, true);
    const rect = tabEl.getBoundingClientRect();
    this.dragOffsetY = clientY - rect.top;

    this.dragGhost = tabEl.cloneNode(true) as HTMLElement;
    this.dragGhost.classList.add("tab-drag-ghost");
    this.dragGhost.style.width = `${rect.width}px`;
    this.dragGhost.style.left = `${rect.left}px`;
    this.dragGhost.style.top = `${rect.top}px`;
    document.body.appendChild(this.dragGhost);

    this.dragIndicator = el("div", { className: "tab-drag-indicator" });
    tabEl.insertAdjacentElement("afterend", this.dragIndicator);
    const list0 = tabEl.parentElement;
    const siblings0 = list0 === null ? [] : this.draggableSiblings(list0);
    this.dragTargetIdx = siblings0.indexOf(this.dragIndicator.nextElementSibling as HTMLElement);
    if (this.dragTargetIdx === -1) {
      this.dragTargetIdx = siblings0.length;
    }

    tabEl.classList.add("tab-drag-placeholder");
    tabEl.parentElement?.classList.add("dragging");

    // `window` in the CAPTURE phase for the pointer trio, because pointer capture
    // retargeting the release to the dragged element is exactly the thing that
    // fails when a drag gets stuck. `pointermove` there too, so a drag survives a
    // capture that never took.
    window.addEventListener("pointermove", this.boundDragMove);
    window.addEventListener("pointerup", this.boundCommit, true);
    window.addEventListener("pointercancel", this.boundCancelled, true);
    window.addEventListener("pointerdown", this.boundAbandon, true);
    window.addEventListener("keydown", this.boundKeydown);
    window.addEventListener("blur", this.boundAbandon);
    document.addEventListener("visibilitychange", this.boundVisibility);
    // `abandoned` rather than `cancelled`, unlike `pointercancel` beside it: after a
    // real release the window listeners have already ended the drag and this one is
    // detached, so a live drag reaching here lost capture some OTHER way (the row
    // left the document during a renderDOM rebuild) and there is no release for the
    // 80ms suppression to swallow — it would take the reader's next click instead.
    tabEl.addEventListener("lostpointercapture", this.boundAbandon);
    this.releaseViewport = onViewportChange(this.boundViewportChange);
    document.body.style.userSelect = "none";

    // LAST, after every field and listener: this throws `NotFoundError` for a
    // pointer that is no longer active, and a throw must leave a fully wired drag
    // the window listeners can still end rather than a half-built one.
    tabEl.setPointerCapture(pointerId);
  }

  private detachDragListeners(tabEl: HTMLElement): void {
    window.removeEventListener("pointermove", this.boundDragMove);
    window.removeEventListener("pointerup", this.boundCommit, true);
    window.removeEventListener("pointercancel", this.boundCancelled, true);
    window.removeEventListener("pointerdown", this.boundAbandon, true);
    window.removeEventListener("keydown", this.boundKeydown);
    window.removeEventListener("blur", this.boundAbandon);
    document.removeEventListener("visibilitychange", this.boundVisibility);
    tabEl.removeEventListener("lostpointercapture", this.boundAbandon);
    this.releaseViewport?.();
    this.releaseViewport = null;
  }

  private onDragMove(e: PointerEvent): void {
    if (this.dragEl === null || this.dragGhost === null) {
      return;
    }
    this.dragGhost.style.top = `${e.clientY - this.dragOffsetY}px`;

    const list = this.dragEl.parentElement;
    if (list === null) {
      return;
    }
    const siblings = this.draggableSiblings(list);

    let target = siblings.length;
    for (let i = 0; i < siblings.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const r = siblings[i]!.getBoundingClientRect();
      if (e.clientY < r.top + r.height / 2) {
        target = i;
        break;
      }
    }

    if (target === this.dragTargetIdx && this.dragIndicator?.isConnected) {
      return;
    }

    const firstRects = new Map<HTMLElement, DOMRect>();
    for (const s of siblings) {
      firstRects.set(s, s.getBoundingClientRect());
    }

    this.dragTargetIdx = target;

    this.dragIndicator ??= el("div", { className: "tab-drag-indicator" });
    if (target >= siblings.length) {
      list.appendChild(this.dragIndicator);
    } else {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      list.insertBefore(this.dragIndicator, siblings[target]!);
    }

    for (const s of siblings) {
      const first = firstRects.get(s);
      if (first === undefined) {
        continue;
      }
      const last = s.getBoundingClientRect();
      const dy = first.top - last.top;
      if (dy === 0) {
        continue;
      }
      s.style.transform = `translateY(${String(dy)}px)`;
      s.style.transition = "none";
      requestAnimationFrame(() => {
        s.style.transform = "";
        s.style.transition = `transform 200ms var(--ease-standard)`;
      });
    }
  }

  /** Take the strip back to a settled state. `commit` moves the row to the
   *  indicator and reports the new order; the other two leave the row where it is
   *  and report nothing, which needs no snapshot because a live drag never
   *  reorders the DOM — `onDragMove` moves the indicator and writes FLIP
   *  transforms, and the row itself moves once, here. Idempotent: several of the
   *  recovery paths can fire in one turn (a blur then a visibilitychange). */
  private endDrag(end: DragEnd): void {
    const tabEl = this.dragEl;
    if (tabEl === null) {
      return;
    }
    const list = tabEl.parentElement;

    if (this.dragIndicator !== null) {
      // Guarded on POSITION, because re-inserting an attached row destroys and
      // recreates every animation in it: a tab's status dot restarts its
      // `vk-dot-beat`, and `beat-phase.ts` re-stamps the phase off that restart,
      // so an unguarded move spends a re-seat plus a re-stamp on a drop that
      // changed nothing. The indicator is seated immediately after the dragged row
      // at drag start, so a drag released without crossing a neighbour lands here
      // with the row already in place.
      if (end === "commit" && list !== null && tabEl.nextSibling !== this.dragIndicator) {
        list.insertBefore(tabEl, this.dragIndicator);
      }
      this.dragIndicator.remove();
      this.dragIndicator = null;
    }
    tabEl.classList.remove("tab-drag-placeholder");
    this.dragGhost?.remove();
    this.dragGhost = null;

    if (list !== null) {
      for (const child of list.children) {
        const c = child as HTMLElement;
        c.style.transform = "";
        c.style.transition = "";
      }
    }
    // Read the order back BEFORE unfolding, so it is top-level ids only — which is
    // exactly what the tab store persists and what reorderTabs re-anchors children
    // against.
    const order =
      end === "commit" && list !== null
        ? [...list.children]
            .filter((c) => !(c as HTMLElement).hasAttribute("data-drag-collapsed"))
            .map((c) => (c as HTMLElement).dataset["tabId"] ?? "")
            .filter((v) => v !== "")
        : null;
    this.setChildrenCollapsed(list, false);

    this.detachDragListeners(tabEl);
    list?.classList.remove("dragging");
    document.body.style.userSelect = "";
    this.dragEl = null;
    this.dragTargetIdx = -1;
    if (order !== null) {
      this.reorderCallback?.(order);
    }
    if (end !== "abandoned") {
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

/** Set the callback invoked when a drag completes with the new tab order. */
export function setReorderCallback(fn: (order: string[]) => void): void {
  instance.setReorderCallback(fn);
}

/** Attach drag-to-reorder behavior to a tab element. */
export function attachDrag(tabEl: HTMLElement): void {
  instance.attachDrag(tabEl);
}
