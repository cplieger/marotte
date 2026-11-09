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

/** Fixed, on an opaque overlay surface, against its trigger's top or bottom edge and over its
 *  inline span, wholly inside the viewport. */
export function expectAnchored(panel: HTMLElement, trigger: HTMLElement): void {
  const p = panel.getBoundingClientRect();
  const t = trigger.getBoundingClientRect();
  const cs = getComputedStyle(panel);
  expect(cs.position, "a floating panel").toBe("fixed");
  expect(cs.backgroundColor, "an opaque surface").not.toBe("rgba(0, 0, 0, 0)");
  expect(cs.boxShadow, "the overlay's shadow").not.toBe("none");
  expect(p.left, "inside the viewport's inline start").toBeGreaterThanOrEqual(0);
  expect(p.right, "inside the viewport's inline end").toBeLessThanOrEqual(window.innerWidth);
  expect(p.top, "inside the viewport's top").toBeGreaterThanOrEqual(0);
  expect(p.bottom, "inside the viewport's bottom").toBeLessThanOrEqual(window.innerHeight);
  const gap = Math.min(Math.abs(p.top - t.bottom), Math.abs(t.top - p.bottom));
  expect(gap, "against its trigger's top or bottom edge").toBeLessThanOrEqual(8);
  expect(p.left < t.right && p.right > t.left, "over its trigger's inline span").toBe(true);
}
