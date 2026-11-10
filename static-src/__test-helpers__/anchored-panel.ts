// What "anchored to its trigger" means for a floating panel, measured in real layout.

import { expect } from "vitest";
import { page } from "vitest/browser";

/** Resize and assert the resize: `page.viewport` has no getter, so a call that stopped moving the
 *  frame would leave a case reporting about the project's own size while naming another. */
export async function resizeViewport(width: number, height: number): Promise<void> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
}

/** The popover's viewport margin, which its clamp pins a too-wide panel to. */
const EDGE_MARGIN = 8;

/** Fixed on the popover rung of the z-index ladder, on an opaque overlay surface, against its
 *  trigger's top or bottom edge and over its inline span, wholly inside the viewport, its end
 *  edge on the trigger's unless clamped, and every visible control inside its inline span. */
export function expectAnchored(panel: HTMLElement, trigger: HTMLElement): void {
  const p = panel.getBoundingClientRect();
  const t = trigger.getBoundingClientRect();
  const cs = getComputedStyle(panel);
  expect(cs.position, "a floating panel").toBe("fixed");
  const rung = getComputedStyle(document.documentElement).getPropertyValue("--uip-z-popover");
  expect(rung.trim(), "the app's ladder is mounted").not.toBe("");
  expect(cs.zIndex, "on the popover rung, over the fullscreen shell").toBe(rung.trim());
  expect(cs.backgroundColor, "an opaque surface").not.toBe("rgba(0, 0, 0, 0)");
  expect(cs.boxShadow, "the overlay's shadow").not.toBe("none");
  expect(p.left, "inside the viewport's inline start").toBeGreaterThanOrEqual(0);
  expect(p.right, "inside the viewport's inline end").toBeLessThanOrEqual(window.innerWidth);
  expect(p.top, "inside the viewport's top").toBeGreaterThanOrEqual(0);
  expect(p.bottom, "inside the viewport's bottom").toBeLessThanOrEqual(window.innerHeight);
  const gap = Math.min(Math.abs(p.top - t.bottom), Math.abs(t.top - p.bottom));
  expect(gap, "against its trigger's top or bottom edge").toBeLessThanOrEqual(8);
  expect(p.left < t.right && p.right > t.left, "over its trigger's inline span").toBe(true);
  const endAligned = Math.abs(p.right - t.right) <= 1;
  const pinned = Math.abs(p.left - EDGE_MARGIN) <= 1;
  expect(
    endAligned || pinned,
    `end-aligned to its trigger's ${String(Math.round(t.right))}, or pinned to the edge margin (left ${String(Math.round(p.left))}, right ${String(Math.round(p.right))})`,
  ).toBe(true);
  // Overflow toward the start edge adds no scroll width, so only the boxes show it.
  for (const c of panel.querySelectorAll<HTMLElement>("input, textarea, select, button")) {
    const b = c.getBoundingClientRect();
    if (b.width === 0) {
      continue;
    }
    expect(
      [b.left >= p.left, b.right <= p.right],
      `${c.tagName.toLowerCase()} ${c.className} at ${String(Math.round(b.left))}..${String(Math.round(b.right))} inside the panel's ${String(Math.round(p.left))}..${String(Math.round(p.right))}`,
    ).toEqual([true, true]);
  }
}
