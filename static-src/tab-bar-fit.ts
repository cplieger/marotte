// The segmented pill bar (settings / git / docs) shows icon plus label, and hides EVERY label when
// one would truncate. MEASURED per bar (scrollWidth > clientWidth), never a breakpoint; one
// truncation flips the whole bar.

/** Bars are measured in icon-plus-label mode, so the icon-only class comes
 *  off before reading. The remove + read + toggle runs synchronously inside
 *  one frame, so there is no visible flicker. */
const ICONS_CLASS = "seg-bar-icons";

/** Set while this bar is VISIBLE with labels, so 12-chat.css can suppress the title bar's duplicate
 *  subtitle. Published here: a `:has()` chain for visibility plus label mode exceeds stylelint's
 *  complexity ceiling, and this module already measures. */
const NAMED_CLASS = "seg-bar-named";

function measure(bar: HTMLElement): void {
  bar.classList.remove(ICONS_CLASS);
  let overflows = false;
  for (const seg of bar.querySelectorAll<HTMLElement>(".seg")) {
    // A hidden bar (display: none view) reports 0/0 — no overflow, label
    // mode. The ResizeObserver fires again when the view shows and the bar
    // gains real geometry, so hidden bars self-correct on reveal. A withdrawn
    // segment reports 0/0 the same way, so only visible segments count.
    if (seg.scrollWidth > seg.clientWidth) {
      overflows = true;
      break;
    }
  }
  bar.classList.toggle(ICONS_CLASS, overflows);

  // `offsetParent` is null under a `display: none` ancestor (inactive views); a view switch resizes
  // the bar, so the ResizeObserver re-runs `measure`.
  bar.classList.toggle(NAMED_CLASS, !overflows && bar.offsetParent !== null);
}

/** Watch a tab bar and keep its label/icon mode fitted to its width.
 *  Call once per bar at init; the observer lives for the app's lifetime
 *  (the three bars are static singletons, so there is nothing to release). */
export function fitTabBar(bar: HTMLElement): void {
  // Re-measure on WIDTH changes only: toggling the class changes the bar's
  // height (icons and labels differ a few px), so an unfiltered callback
  // re-fires once per toggle — a benign but noisy RO loop.
  let lastWidth = -1;
  const ro = new ResizeObserver((entries) => {
    const w = entries[entries.length - 1]?.contentRect.width ?? -1;
    if (w === lastWidth) {
      return;
    }
    lastWidth = w;
    // Deferred a frame: mutating class state inside the RO delivery cycle
    // (labels hide, bar height moves) triggers the browser's benign-but-noisy
    // "loop completed with undelivered notifications" console error.
    requestAnimationFrame(() => {
      measure(bar);
    });
  });
  ro.observe(bar);
  measure(bar);
}

/** Re-measure now, for a change the width-only observer cannot see: a segment shown or hidden
 *  moves its siblings' widths while the bar's own width stays put. */
export function refitTabBar(bar: HTMLElement): void {
  measure(bar);
}
