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
  // Painted a frame later: the cap and gutter resize the observed area, which inside the delivery
  // is a ResizeObserver loop.
  let roFrame = 0;
  const ro = new ResizeObserver(() => {
    if (roFrame === 0) {
      roFrame = requestAnimationFrame(() => {
        roFrame = 0;
        paint();
      });
    }
  });
  ro.observe(area);
  if (pane !== null) {
    // Measured rather than a container query: `container-type: size` on the pane would make it the containing
    // block and stacking context of the banner band and the composer's raised form.
    ro.observe(pane);
    publishViewportHeight();
    followBand(paint);
    // A pan moves `offsetTop` with no resize, so `publishViewportHeight`'s listener never sees it.
    let frame = 0;
    window.visualViewport?.addEventListener("scroll", () => {
      if (frame === 0) {
        frame = requestAnimationFrame(() => {
          frame = 0;
          paint();
        });
      }
    });
  }
  paint();
}

/** A raised soft keyboard shrinks only the visual viewport (no `interactive-widget` in the meta), and iOS pans it to
 *  the focused field, so the pane is clipped to it at both edges. */
function visibleHeight(pane: HTMLElement): number {
  const r = pane.getBoundingClientRect();
  const vv = window.visualViewport;
  const top = vv == null ? 0 : vv.offsetTop;
  const bottom = vv == null ? window.innerHeight : top + vv.height;
  return Math.max(0, Math.min(r.bottom, bottom) - Math.max(r.top, top));
}
