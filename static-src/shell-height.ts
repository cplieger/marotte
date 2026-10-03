import { shellHeight, shellOpen } from "./device-view.js";

/** Smallest useful panel height (6rem): a few terminal rows + the header. */
export const SHELL_MIN_H = 96;

/** Upper clamp: leave at least 20% of the viewport for the chat column. */
export function shellMaxH(): number {
  return Math.round(window.innerHeight * 0.8);
}

export function clampShellH(h: number): number {
  return Math.min(Math.max(h, SHELL_MIN_H), shellMaxH());
}

/** The panel height in whole px; one rounding for the drag and the pre-paint
 *  state, so the hand-over between them paints the same height. */
export function shellPanelPx(h: number): number {
  return Math.round(clampShellH(h));
}

const ATTR = "data-shell-open";
const VAR = "--shell-h";

/** Paint a stored-open panel before the shell module loads: a root attribute the
 *  CSS turns into "visible at --shell-h, no transition". Released by
 *  `releaseStoredShellPanel`. */
export function applyStoredShellPanel(): void {
  if (!shellOpen()) {
    return;
  }
  const root = document.documentElement;
  root.setAttribute(ATTR, "");
  const h = shellHeight();
  if (h > 0) {
    root.style.setProperty(VAR, `${String(shellPanelPx(h))}px`);
  }
}

/** End the pre-paint state; a no-op when it was never applied. */
export function releaseStoredShellPanel(): void {
  const root = document.documentElement;
  root.removeAttribute(ATTR);
  root.style.removeProperty(VAR);
}

/** End the pre-paint state with the panel shut at once, for a boot that will not
 *  restore it: the base rule would otherwise animate it closed at page load. */
export function dropStoredShellPanel(panel: HTMLElement): void {
  if (!document.documentElement.hasAttribute(ATTR)) {
    return;
  }
  panel.style.setProperty("transition", "none");
  releaseStoredShellPanel();
  // Commit the closed style while transitions are off, so restoring them starts none.
  panel.getBoundingClientRect();
  panel.style.removeProperty("transition");
}
