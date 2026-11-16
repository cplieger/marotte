// The holding area above a composer: the run bar, then the steer and queued rows, under one cap
// (`css/26-dock.css` `.holding-area`).

import { followBand, publishViewportHeight } from "./dock-ask.js";

/** The tab view's visible height in px, read by `.holding-area`'s cap. */
const PANE_PROP = "--hold-pane-h";

const mounted = new WeakSet<HTMLElement>();

/**
 * Keep `area`'s cap a share of its tab view's height clipped to the visual viewport. Idempotent per area; an area
 * outside a tab view keeps the stylesheet's viewport fallback.
 */
export function mountHoldingArea(area: HTMLElement): void {
  const pane = area.closest<HTMLElement>("[data-tab-view]");
  if (mounted.has(area) || pane === null) {
    return;
  }
  mounted.add(area);
  const paint = (): void => {
    area.style.setProperty(PANE_PROP, `${String(visibleHeight(pane))}px`);
  };
  // Measured rather than a container query: `container-type: size` on the pane would make it the containing block
  // and stacking context of the banner band and the composer's raised form.
  new ResizeObserver(paint).observe(pane);
  publishViewportHeight();
  followBand(paint);
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
