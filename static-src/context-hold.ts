// context-hold.ts — A row's context menu from its `contextmenu` or from a touch hold, once per press.

const HOLD_MS = 500;
/** Android's touch slop (`ViewConfiguration.TOUCH_SLOP`): travel past it is a scroll or a drag, not a hold.
 *  https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/core/java/android/view/ViewConfiguration.java */
export const TOUCH_SLOP_PX = 8;
/** How long a touch lift's click stays swallowed, as `tabs-drag.ts` expires its release suppression. */
const LIFT_CLICK_MS = 80;

/** Open `target`'s menu with `open(x, y)` from its `contextmenu` or from a touch held `HOLD_MS` within the slop,
 *  once per press: iOS WebKit fires no `contextmenu` for a long press (https://bugs.webkit.org/show_bug.cgi?id=213953).
 *  A scroll of a scroller holding `target` ends the hold, and `mayOpen`, asked when the hold completes, can refuse
 *  it. The held touch's lift click is swallowed. */
export function bindContextPress(
  target: HTMLElement,
  open: (x: number, y: number) => void,
  mayOpen: () => boolean = () => true,
): void {
  let press: { id: number; x: number; y: number; timer: number | null; opened: boolean } | null =
    null;
  const endPress = (): void => {
    if (press?.timer != null) {
      clearTimeout(press.timer);
    }
    press = null;
    window.removeEventListener("pointermove", onMove, true);
    window.removeEventListener("pointerup", onLift, true);
    window.removeEventListener("pointercancel", onCancel, true);
    document.removeEventListener("scroll", onScroll, true);
  };
  const onMove = (e: PointerEvent): void => {
    if (
      e.pointerId === press?.id &&
      press.timer !== null &&
      Math.hypot(e.clientX - press.x, e.clientY - press.y) > TOUCH_SLOP_PX
    ) {
      endPress();
    }
  };
  // A finger that stops a momentum scroll rests on the row it stopped over, as the drag's
  // `boundPendingScroll` knows.
  const onScroll = (e: Event): void => {
    if (press?.timer != null && e.target instanceof Node && e.target.contains(target)) {
      endPress();
    }
  };
  const onCancel = (e: PointerEvent): void => {
    if (e.pointerId === press?.id) {
      endPress();
    }
  };
  const onLift = (e: PointerEvent): void => {
    if (e.pointerId !== press?.id) {
      return;
    }
    const opened = press.opened;
    endPress();
    if (opened) {
      swallowLiftClick();
    }
  };
  const menuFromPress = (): boolean => {
    if (press === null) {
      return true;
    }
    if (press.opened) {
      return false;
    }
    press.opened = true;
    if (press.timer !== null) {
      clearTimeout(press.timer);
      press.timer = null;
    }
    return true;
  };

  target.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    if (!menuFromPress()) {
      return;
    }
    // A keyboard-opened menu carries no point of its own.
    const box = target.getBoundingClientRect();
    const keyed = e.clientX === 0 && e.clientY === 0;
    open(keyed ? box.left : e.clientX, keyed ? box.bottom : e.clientY);
  });
  target.addEventListener("pointerdown", (e) => {
    // A second finger (never primary) on a held row leaves the first finger's lift to be swallowed.
    if (e.pointerType !== "touch" || (press?.opened === true && !e.isPrimary)) {
      return;
    }
    endPress();
    const p = {
      id: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      timer: null as number | null,
      opened: false,
    };
    p.timer = window.setTimeout(() => {
      p.timer = null;
      if (press === p && mayOpen() && menuFromPress()) {
        const box = target.getBoundingClientRect();
        open(p.x || box.left, p.y || box.bottom);
      }
    }, HOLD_MS);
    press = p;
    // Passive: a blocking window `pointermove` listener makes WebKit lay the page out again (`tabs-drag.ts`).
    window.addEventListener("pointermove", onMove, { capture: true, passive: true });
    window.addEventListener("pointerup", onLift, true);
    window.addEventListener("pointercancel", onCancel, true);
    // `scroll` does not bubble, so the capture phase on `document` sees every scroller's.
    document.addEventListener("scroll", onScroll, { capture: true, passive: true });
  });
}

/** Swallow the click a held touch sends when it lifts, so neither the row nor the menu's outside dismissal sees it. */
function swallowLiftClick(): void {
  const swallow = (e: MouseEvent): void => {
    e.stopPropagation();
    e.preventDefault();
    done();
  };
  const timer = window.setTimeout(() => {
    done();
  }, LIFT_CLICK_MS);
  function done(): void {
    clearTimeout(timer);
    window.removeEventListener("click", swallow, true);
  }
  window.addEventListener("click", swallow, true);
}
