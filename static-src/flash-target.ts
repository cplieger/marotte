// Scroll a deep-linked target into view and flash a ring: a Settings control (`?highlight=<id>`) or a PR row.

import { forceReflow } from "./dom.js";

/** Keyframes live in css/30-utilities.css. */
const FLASH_CLASS = "deep-link-flash";

/** Both consumers centre their target in its own scroll container, so the options
 *  are the module's rather than a parameter with one value at every call site. */
const SCROLL_OPTIONS: ScrollIntoViewOptions = { block: "center", behavior: "smooth" };

/** About a third of a second at 60fps: past the panel swap and a local fetch. */
const MAX_FRAMES = 20;

/** Required: under `prefers-reduced-motion` `animationend` never fires. */
const FLASH_CLEAR_MS = 2500;

/** Per target, so re-flashing cancels the previous deadline instead of being stripped by it. */
const flashTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

/** False while its panel still carries `.hidden`. */
function isLaidOut(e: HTMLElement): boolean {
  return e.offsetParent !== null || e.getClientRects().length > 0;
}

/** Scroll `find`'s element into view and flash it. Quiet on a target that never resolves. */
export function flashTarget(find: () => HTMLElement | null): void {
  let frames = 0;
  const attempt = (): void => {
    const target = find();
    if (target === null || !isLaidOut(target)) {
      frames++;
      if (frames <= MAX_FRAMES) {
        requestAnimationFrame(attempt);
      }
      return;
    }
    target.scrollIntoView(SCROLL_OPTIONS);
    // The map entry is the single record of a running flash, so this is safe from a previous flash's listener.
    const clear = (): void => {
      const pending = flashTimers.get(target);
      if (pending === undefined) {
        return;
      }
      clearTimeout(pending);
      flashTimers.delete(target);
      target.classList.remove(FLASH_CLASS);
    };
    clear();
    // Re-add rather than toggle: removal applies only after a reflow, which restarts the animation.
    target.classList.remove(FLASH_CLASS);
    forceReflow(target);
    target.classList.add(FLASH_CLASS);
    target.addEventListener("animationend", clear, { once: true });
    flashTimers.set(target, setTimeout(clear, FLASH_CLEAR_MS));
  };
  attempt();
}
