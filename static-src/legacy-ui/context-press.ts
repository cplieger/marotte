// context-press.ts — A row's context-menu press: its touch hold, and which click ends the press a
// `contextmenu` fired during. macOS Safari makes a Control-click's press a `contextmenu` and still sends
// `click` on its release (https://stackoverflow.com/q/53217104; Gecko fixed the same in
// https://bugzilla.mozilla.org/show_bug.cgi?id=1677334), where a right button's release sends none.

let menuPress = false;

// On window, capturing, at import: the flag must be set before any listener opens a menu from the event.
window.addEventListener(
  "pointerdown",
  () => {
    menuPress = false;
  },
  true,
);
window.addEventListener(
  "contextmenu",
  () => {
    menuPress = true;
  },
  true,
);

/** True for a pointer click (`detail` above 0) whose press fired a `contextmenu`: the menu's gesture, not a
 *  click. A keyboard or scripted click (`detail` 0) never is. */
export function isContextPressRelease(e: MouseEvent): boolean {
  return menuPress && e.detail > 0;
}

const HOLD_MS = 500;
/** Android's touch slop (`ViewConfiguration.TOUCH_SLOP`): travel past it is a scroll or a drag, not a hold.
 *  https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/core/java/android/view/ViewConfiguration.java */
export const TOUCH_SLOP_PX = 8;
/** How long a touch lift's click stays swallowed, as `tabs-drag.ts` expires its release suppression. */
const LIFT_CLICK_MS = 80;

/** Open `target`'s menu with `open(x, y)` from its `contextmenu` or from a touch held `HOLD_MS` within the slop,
 *  once per press: iOS WebKit fires no `contextmenu` for a long press (https://bugs.webkit.org/show_bug.cgi?id=213953).
 *  `mayOpen`, asked when the hold completes, can refuse it. The held touch's lift click is swallowed. */
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
    if (e.pointerType !== "touch") {
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
