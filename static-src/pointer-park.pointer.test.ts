// The real pointer stays where a test left it, and Chromium re-hovers whatever the next test lays
// out under it. pointer-park-setup.ts moves it off the page before each test, so a later test reads
// no hover it did not make.

import { afterEach, describe, expect, it } from "vitest";
import { userEvent } from "vitest/browser";

import { pointerInPage } from "./__test-helpers__/park-pointer.js";

let target: HTMLElement | null = null;
let style: HTMLStyleElement | null = null;

afterEach(() => {
  target?.remove();
  target = null;
  style?.remove();
  style = null;
});

/** A hover target at the page's top-left, the spot most tests mount their own element on. */
function mountTarget(): HTMLElement {
  style = document.createElement("style");
  style.textContent = ".park-target:hover { background: rgb(255, 0, 0); }";
  document.head.append(style);
  target = document.createElement("button");
  target.className = "park-target";
  target.style.cssText =
    "position:fixed;inset-block-start:0;inset-inline-start:0;inline-size:200px;block-size:200px";
  document.body.append(target);
  return target;
}

/** Long enough for Chromium's layout-driven hover update to reach an element laid out under the pointer. */
async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }
  await new Promise<void>((resolve) => setTimeout(resolve, 150));
}

describe("the pointer between tests", () => {
  it("leaves the pointer over a hover target", async () => {
    const el = mountTarget();
    await userEvent.hover(el);
    expect(el.matches(":hover")).toBe(true);
  });

  it("starts the next test with no hover on the same spot", async () => {
    const el = mountTarget();
    await frames(4);
    expect(el.matches(":hover"), "the element under the last test's pointer").toBe(false);
    expect(getComputedStyle(el).backgroundColor).not.toBe("rgb(255, 0, 0)");
    expect(pointerInPage(), "the pointer is off the page").toBe(false);
  });
});
