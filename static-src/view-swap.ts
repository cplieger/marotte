// Synchronous view swaps with a compositor-only entry fade. NOT document view transitions: CSS View
// Transitions Level 1 §4.2 makes captured content non-hittable while one runs, blocking clicks.
// The swap is a plain DOM update; the fade is WAAPI opacity on the incoming view.

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
export function _resetForTest(): void {
  entry?.cancel();
  entry = null;
  bootDone = false;
}
