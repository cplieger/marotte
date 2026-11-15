// context-hold.ts — A row's context menu from its `contextmenu` or from a touch hold, once per press.

const HOLD_MS = 500;
/** Android's touch slop (`ViewConfiguration.TOUCH_SLOP`): travel past it is a scroll or a drag, not a hold.
 *  https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/core/java/android/view/ViewConfiguration.java */
export const TOUCH_SLOP_PX = 8;
/** How long a touch lift's click stays swallowed, as `tabs-drag.ts` expires its release suppression. */
const LIFT_CLICK_MS = 80;
/** A momentum scroll fires `scroll` every frame, so a scroller silent this long had stopped before the touch. */
const SCROLL_SETTLED_MS = 100;

const lastScroll = new WeakMap<EventTarget, number>();
let scrollsTracked = false;

/** Start recording each scroller's last scroll time, which `caughtFling` reads. Idempotent. */
export function watchForFlings(): void {
  if (scrollsTracked) {
    return;
  }
  scrollsTracked = true;
  // `scroll` does not bubble, so the capture phase on `document` sees every scroller's.
  document.addEventListener(
    "scroll",
    (e) => {
      if (e.target !== null) {
        lastScroll.set(e.target, e.timeStamp);
      }
    },
    { capture: true, passive: true },
  );
}

/** The one verdict on whether a press caught a fling: a scroller holding its target scrolled within
 *  `SCROLL_SETTLED_MS` before it, so the finger rests on whichever row the fling stopped under. A
 *  scroll after the press is never the finger's, whose own travel ends a hold by the slop or a
 *  `pointercancel`: it is a layout clamp, such as the drag lift folding sub-tabs, or a programmatic
 *  scroll. False for every press until `watchForFlings` has run. */
export function caughtFling(e: PointerEvent): boolean {
  for (let n = e.target instanceof Node ? e.target : null; n !== null; n = n.parentNode) {
    const t = lastScroll.get(n);
    if (t !== undefined && e.timeStamp - t < SCROLL_SETTLED_MS) {
      return true;
    }
  }
  return false;
}

/** `pending` until settled: `menu` opened one, `refused` was a caught fling reaching its hold or a platform
 *  `contextmenu`, `ended` travelled past the slop or was refused by `mayOpen` and lifts as a tap, unless a
 *  platform `contextmenu` still opens its menu, which makes it `menu`. */
type Outcome = "pending" | "menu" | "refused" | "ended";

type Opener = (x: number, y: number) => void;

interface Press {
  readonly id: number;
  readonly type: string;
  readonly target: HTMLElement;
  readonly open: Opener;
  readonly fling: boolean;
  readonly x: number;
  readonly y: number;
  timer: number | null;
  outcome: Outcome;
}

/** The document's one touch or pen press, from its `pointerdown` to its pointer's lift or cancel. */
let press: Press | null = null;

function settle(p: Press, outcome: Exclude<Outcome, "pending">): void {
  p.outcome = outcome;
  if (p.timer !== null) {
    clearTimeout(p.timer);
    p.timer = null;
  }
}

function dropPress(): void {
  if (press?.timer != null) {
    clearTimeout(press.timer);
  }
  press = null;
  window.removeEventListener("pointermove", onMove, true);
  window.removeEventListener("pointerup", onLift, true);
  window.removeEventListener("pointercancel", onCancel, true);
}

function onMove(e: PointerEvent): void {
  if (
    e.pointerId === press?.id &&
    press.outcome === "pending" &&
    Math.hypot(e.clientX - press.x, e.clientY - press.y) > TOUCH_SLOP_PX
  ) {
    settle(press, "ended");
  }
}

function onCancel(e: PointerEvent): void {
  if (e.pointerId === press?.id) {
    dropPress();
  }
}

function onLift(e: PointerEvent): void {
  if (e.pointerId !== press?.id) {
    return;
  }
  const { outcome } = press;
  dropPress();
  if (outcome === "menu" || outcome === "refused") {
    swallowLiftClick();
  }
}

/** The press a `contextmenu` belongs to, by its pointer (https://www.w3.org/TR/pointerevents3/#the-click-auxclick-and-contextmenu-events):
 *  the live press for its own pointer, or for one with no pointer type; null for a mouse, the keyboard or no press.
 *  Another touch or pen pointer answers `"other"`: a second finger's long press starts nothing. */
function pressOf(e: MouseEvent): Press | "other" | null {
  if (press === null) {
    return null;
  }
  if (!(e instanceof PointerEvent)) {
    return press;
  }
  if (e.pointerType !== "touch" && e.pointerType !== "pen") {
    return null;
  }
  return e.pointerId === press.id ? press : "other";
}

/** Settle `p` for its pointer's `contextmenu`, answering whether its menu opens. */
function menuFromPress(p: Press): boolean {
  switch (p.outcome) {
    case "pending":
      settle(p, p.fling ? "refused" : "menu");
      return !p.fling;
    case "ended":
      settle(p, "menu");
      return true;
    case "menu":
    case "refused":
      return false;
  }
}

/** Open `target`'s menu with `open(x, y)` from its `contextmenu` or from a touch held `HOLD_MS` within the slop,
 *  once per press: iOS WebKit fires no `contextmenu` for a long press (https://bugs.webkit.org/show_bug.cgi?id=213953).
 *  A touch or pen press that caught a fling opens no menu by either path, and `mayOpen`, asked when the hold
 *  completes, can refuse it. The lift's click is swallowed after a menu or a refusal. */
export function bindContextPress(
  target: HTMLElement,
  open: Opener,
  mayOpen: () => boolean = () => true,
): void {
  watchForFlings();
  target.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    const p = pressOf(e);
    if (p === "other" || (p !== null && !menuFromPress(p))) {
      return;
    }
    // The pressed row's menu, wherever the press's own event landed: the drag lift can fold rows under it.
    const row = p?.target ?? target;
    // A keyboard-opened menu carries no point of its own.
    const box = row.getBoundingClientRect();
    const keyed = e.clientX === 0 && e.clientY === 0;
    (p?.open ?? open)(keyed ? box.left : e.clientX, keyed ? box.bottom : e.clientY);
  });
  target.addEventListener("pointerdown", (e) => {
    if (e.pointerType !== "touch" && e.pointerType !== "pen") {
      return;
    }
    // A primary pointer of the press's own type means its lift was never seen
    // (https://www.w3.org/TR/pointerevents3/#the-primary-pointer); any other is a second pointer.
    if (press !== null && !(e.isPrimary && e.pointerType === press.type)) {
      return;
    }
    dropPress();
    const p: Press = {
      id: e.pointerId,
      type: e.pointerType,
      target,
      open,
      fling: caughtFling(e),
      x: e.clientX,
      y: e.clientY,
      timer: null,
      outcome: "pending",
    };
    // Only a touch arms the hold: a pen's menu comes from the platform's `contextmenu`.
    if (p.type === "touch") {
      // Every settle and drop clears this timer, so it fires only for the live, pending press.
      p.timer = window.setTimeout(() => {
        if (p.fling) {
          settle(p, "refused");
        } else if (mayOpen()) {
          settle(p, "menu");
          p.open(p.x, p.y);
        } else {
          settle(p, "ended");
        }
      }, HOLD_MS);
    }
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
