// `clientX`/`clientY` are LAYOUT-viewport coordinates and iOS offsets the visual viewport for the
// keyboard (https://developer.mozilla.org/en-US/docs/Web/API/VisualViewport), so a still pointer's
// coordinates jump when the offset closes; pinch zoom moves the inline axis the same way.

/** The visible viewport's box, under the API's own field names. This module is the
 *  app's one reader of that object, so a caller clamping or baselining against either
 *  axis takes its numbers from here. */
export interface ViewportBox {
  readonly offsetLeft: number;
  readonly offsetTop: number;
  readonly width: number;
  readonly height: number;
}

/** Read the box now, falling back to the layout viewport where the API is absent, so
 *  no caller needs a null branch. */
export function viewportBox(): ViewportBox {
  const vv = window.visualViewport;
  return vv == null
    ? { offsetLeft: 0, offsetTop: 0, width: window.innerWidth, height: window.innerHeight }
    : { offsetLeft: vv.offsetLeft, offsetTop: vv.offsetTop, width: vv.width, height: vv.height };
}

/** Sub-pixel jitter absorbed, and far under any gesture threshold, so it cannot
 *  mask the shift this exists to catch (an offset change, in the hundreds). */
const BOX_TOLERANCE_PX = 1;

/** Whether two boxes differ enough to move a stationary pointer or invalidate a clamp. All four
 *  fields: gating on one axis reads an inline pan while zoomed as a real gesture. */
export function viewportMoved(a: ViewportBox, b: ViewportBox): boolean {
  return (
    Math.abs(a.height - b.height) >= BOX_TOLERANCE_PX ||
    Math.abs(a.offsetTop - b.offsetTop) >= BOX_TOLERANCE_PX ||
    Math.abs(a.width - b.width) >= BOX_TOLERANCE_PX ||
    Math.abs(a.offsetLeft - b.offsetLeft) >= BOX_TOLERANCE_PX
  );
}

/** Subscribe to every event that can move the box; the returned function detaches all
 *  of them. `scroll` is required beside `resize`, because an offset change fires only
 *  `scroll`. */
export function onViewportChange(fn: () => void): () => void {
  const vv = window.visualViewport;
  vv?.addEventListener("resize", fn);
  vv?.addEventListener("scroll", fn);
  window.addEventListener("resize", fn);
  return () => {
    vv?.removeEventListener("resize", fn);
    vv?.removeEventListener("scroll", fn);
    window.removeEventListener("resize", fn);
  };
}
