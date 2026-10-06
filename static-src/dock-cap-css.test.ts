// The dock's prose regions cap at 14rem, 10rem under `@media (width <= 40rem)` (26-dock.css). Its own file because a
// resize block must sit last and restore its size. Source half: both scopes cap the same regions. Measurement half:
// the override applies at the boundary.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/**
 * The one member that can locate each rule: `ruleContaining` splits on commas, gluing the first and last names to the
 * `:where(` parentheses, and `.dock-ask-body` has other rules of its own.
 */
const ANCHOR = ".elicitation-body";

/** Parsed here: `ruleContaining`'s split cannot tell names inside `:where()` from siblings. */
function cappedRegions(prelude: string): string[] {
  const open = prelude.indexOf(":where(");
  expect(open, `no :where() in ${prelude}`).toBeGreaterThan(-1);
  const close = prelude.indexOf(")", open);
  return prelude
    .slice(open + ":where(".length, close)
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function declaredRem(body: string): number {
  const m = /max-block-size:\s*([\d.]+)rem/.exec(body);
  expect(m, `no rem max-block-size in ${body}`).not.toBeNull();
  return Number(m?.[1]);
}

const css = loadCSS("26-dock.css");
const base = ruleContaining(css, ANCHOR, "top");
const phone = ruleContaining(css, ANCHOR, "40rem");

describe("the set of regions the phone override reaches", () => {
  it("is the same set the base rule caps", () => {
    // A region forgotten in the override keeps the desktop cap on a phone, invisible to a computed style.
    expect(cappedRegions(phone.selector)).toEqual(cappedRegions(base.selector));
  });

  it("steps the cap DOWN rather than up", () => {
    // A reversed override would give the phone the larger cap.
    expect(declaredRem(phone.body)).toBeLessThan(declaredRem(base.body));
  });
});

describe("the phone step-down, measured at real viewport sizes", () => {
  // Sits last and restores the size, read off the frame (`page.viewport` has no getter).
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** In rem against the live root size. The resize is asserted, or a stuck viewport makes every case vacuous. */
  async function capAt(width: number, height: number): Promise<number> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    const card = document.createElement("div");
    card.className = "dock-card";
    const region = document.createElement("div");
    region.className = "dock-ask-body";
    card.appendChild(region);
    document.body.appendChild(card);
    const rootPx = parseFloat(getComputedStyle(document.documentElement).fontSize);
    return parseFloat(getComputedStyle(region).maxBlockSize) / rootPx;
  }

  it("caps a region at the phone figure on a 390px viewport", async () => {
    expect(await capAt(390, 844)).toBeCloseTo(declaredRem(phone.body), 2);
  });

  it("still caps it at the phone figure at the boundary width itself", async () => {
    // `width <= 40rem` includes 640; a `<` would pass every other case.
    expect(await capAt(640, 900)).toBeCloseTo(declaredRem(phone.body), 2);
  });

  it("caps it at the base figure one pixel wider", async () => {
    expect(await capAt(641, 900)).toBeCloseTo(declaredRem(base.body), 2);
  });
});
