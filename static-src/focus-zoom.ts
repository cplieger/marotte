// iOS zooms the page when a text field whose computed size is under 16px takes focus, and the app's
// fields read at the type scale's own rungs (12-14px). A viewport `maximum-scale=1` stops that zoom,
// and Safari ignores the clause for the reader's own pinch (https://webkit.org/blog/7367/), so the
// page stays resizable (WCAG 1.4.4). Android Chrome honours it for the pinch too, so it is written on
// iOS only. A leaf module: the blocking /prepaint.js bundles it.

interface NavigatorView {
  readonly userAgent: string;
  readonly platform: string;
  readonly maxTouchPoints: number;
}

/** An iPhone or iPod, or an iPad, which reports itself as a Mac with touch points since iPadOS 13. */
export function isIOSFamily(nav: NavigatorView): boolean {
  return (
    /iPad|iPhone|iPod/.test(nav.userAgent) ||
    (nav.platform === "MacIntel" && nav.maxTouchPoints > 1)
  );
}

const CLAUSE = "maximum-scale=1";

/** Append the clause to the page's viewport meta on an iOS device; anywhere else, or with no meta,
 *  or with a scale limit already stated, the meta is left as written. */
export function applyFocusZoomGuard(
  doc: Document = document,
  nav: NavigatorView = navigator,
): void {
  if (!isIOSFamily(nav)) {
    return;
  }
  const meta = doc.querySelector<HTMLMetaElement>('meta[name="viewport"]');
  if (meta === null || meta.content.includes("maximum-scale")) {
    return;
  }
  meta.content = `${meta.content}, ${CLAUSE}`;
}
