// Synchronous view swaps with a compositor-only entry fade. NOT document view transitions: CSS View
// Transitions Level 1 §4.2 makes captured content non-hittable while one runs, blocking clicks.
// The swap is a plain DOM update; the fade is WAAPI opacity on the incoming view.

import { park, queryAll, unpark } from "./dom.js";

/** Entry-fade duration. Mirrors --dur-enter (css/01-tokens.css: 0.25s). */
export const DUR_ENTER_MS = 250;

/** Entry-fade easing. Mirrors --ease-enter (css/01-tokens.css). */
export const EASE_ENTER = "cubic-bezier(0, 0, 0, 1)";

// Boot fast-path flag: view swaps before the initial route is applied are bulk
// restores that shouldn't animate. app.ts flips this right after
// applyInitialRoute() (and on the unauthenticated path, right after login).
let bootDone = false;

/** Mark boot restore complete — view swaps animate from here on. */
export function markBootDone(): void {
  bootDone = true;
}

// One-slot handle registry: at most one entry animation is ever live. A new
// swap cancels the previous handle, so rapid swaps replace deterministically
// instead of stacking.
let entry: Animation | null = null;

/** A view swap's update callback: applies the DOM change synchronously and
 *  returns the incoming view to animate — or nothing, when the swap has no
 *  single incoming element (the animation is then skipped). */
type ViewSwap = (() => HTMLElement | null) | (() => void);

/** Apply a view swap NOW and fade the incoming view in; `update` runs synchronously. The animation
 *  is fire-and-forget (a replaced handle's rejection is swallowed) and skipped under
 *  prefers-reduced-motion (WAAPI ignores the CSS sweep), document.hidden, and before boot. */
export function swapViews(update: ViewSwap): void {
  entry?.cancel();
  entry = null;

  const view = update();

  if (
    view == null ||
    !bootDone ||
    document.hidden ||
    matchMedia("(prefers-reduced-motion: reduce)").matches
  ) {
    return;
  }

  const anim = view.animate([{ opacity: 0 }, { opacity: 1 }], {
    duration: DUR_ENTER_MS,
    easing: EASE_ENTER,
  });
  anim.finished.catch(() => undefined);
  entry = anim;
}

/** Reset boot + handle state. Exported for test isolation only. */
// deadset:ignore DS1102 -- test seam: resets the boot flag and the pending swap
export function _resetForTest(): void {
  entry?.cancel();
  entry = null;
  bootDone = false;
}

const VIEWS_SELECTOR = "[data-tab-view]";

/** Views that stay in the page while hidden: the chat view, whose transcript the residency window bounds, and the
 *  web view, whose iframes would lose their page on leaving the document (HTML: removing an iframe destroys its
 *  navigable). */
const KEPT_IN_PAGE: ReadonlySet<string> = new Set(["chat-view", "web-view"]);

/** A same-value `classList.add` still writes the attribute and queues a mutation record. */
function hide(v: HTMLElement): void {
  if (!v.classList.contains("hidden")) {
    v.classList.add("hidden");
  }
}

/** The gesture half of `showOnlyView`: hide and park every view but `target` and leave `target` as it is, so a view
 *  not yet shown stays out of layout until `showOnlyView` swaps it in with its fade. A null target hides every view. */
export function hideOtherViews(target: HTMLElement | null): void {
  const views = queryAll<HTMLElement>(VIEWS_SELECTOR);
  for (const v of views) {
    if (v !== target && !KEPT_IN_PAGE.has(v.id)) {
      park(v);
    }
  }
  for (const v of views) {
    if (v !== target) {
      hide(v);
    }
  }
}

/** Show `target` alone among the `[data-tab-view]` views, parking the hidden ones (dom.ts `park`). Swaps, with the
 *  entry fade, only when `target` is not already the one shown: callers re-run this on every projection change. */
export function showOnlyView(target: HTMLElement | null): void {
  const views = queryAll<HTMLElement>(VIEWS_SELECTOR);
  const shown = views.filter((v) => !v.classList.contains("hidden"));
  const swap = shown.length !== 1 || shown[0] !== target || !document.contains(target);
  // Before the swap's writes: park reads the outgoing view's scroll offsets.
  for (const v of views) {
    if (v !== target && !KEPT_IN_PAGE.has(v.id)) {
      park(v);
    }
  }
  if (!swap) {
    return;
  }
  swapViews(() => {
    for (const v of views) {
      hide(v);
    }
    if (target !== null) {
      unpark(target, () => {
        target.classList.remove("hidden");
      });
    }
    return target;
  });
}
