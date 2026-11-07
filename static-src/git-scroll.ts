// replaceChildren on any descendant of #git-view resets its scrollTop to 0, so tab repaints run through this.

import { maybeEl } from "./dom.js";

/** Run `paint` while preserving #git-view's scroll position; without the element, `paint` runs unchanged. */
export function preserveGitScroll(paint: () => void): void {
  const view = maybeEl("git-view");
  if (view === null) {
    paint();
    return;
  }
  const saved = view.scrollTop;
  paint();
  // Restored next frame and clamped to the post-paint max, since `paint` may change the scroll height.
  requestAnimationFrame(() => {
    const max = view.scrollHeight - view.clientHeight;
    view.scrollTop = Math.min(saved, Math.max(0, max));
  });
}
