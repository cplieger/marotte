// The holding area above a composer: the run bar, then the steer and queued rows, under one cap
// (`css/26-dock.css` `.holding-area`).

import { followBand, publishViewportHeight } from "./dock-ask.js";

/** The tab view's visible height in px, read by `.holding-area`'s cap. */
const PANE_PROP = "--hold-pane-h";

/** The area's reserved scrollbar strip in px, which its inline end gives back to the bar's padding. */
const GUTTER_PROP = "--hold-gutter";

const mounted = new WeakSet<HTMLElement>();

/**
 * Keep `area`'s cap a share of its tab view's height clipped to the visual viewport, and its rows on the composer's
 * edges past its scrollbar. Idempotent per area; an area outside a tab view keeps the stylesheet's viewport cap.
 */
export function mountHoldingArea(area: HTMLElement): void {
  if (mounted.has(area)) {
    return;
  }
  mounted.add(area);
  const pane = area.closest<HTMLElement>("[data-tab-view]");
  const paint = (): void => {
    if (pane !== null) {
      area.style.setProperty(PANE_PROP, `${String(visibleHeight(pane))}px`);
    }
    area.style.setProperty(GUTTER_PROP, `${String(area.offsetWidth - area.clientWidth)}px`);
  };
  // The area too: it has no box while empty, so its gutter is first measurable when it shows.
  const ro = new ResizeObserver(paint);
  ro.observe(area);
  if (pane !== null) {
    // Measured rather than a container query: `container-type: size` on the pane would make it the containing
    // block and stacking context of the banner band and the composer's raised form.
    ro.observe(pane);
    publishViewportHeight();
    followBand(paint);
  }
  paint();
}

/** A raised soft keyboard shrinks only the visual viewport (no `interactive-widget` in the meta), so the pane's
 *  bottom is clipped to it. */
function visibleHeight(pane: HTMLElement): number {
  const r = pane.getBoundingClientRect();
  const vv = window.visualViewport;
  const bottom = vv == null ? window.innerHeight : vv.offsetTop + vv.height;
  return Math.max(0, Math.min(r.bottom, bottom) - r.top);
}
