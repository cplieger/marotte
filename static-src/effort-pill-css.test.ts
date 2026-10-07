// The model pill's tier and Send's word are hidden on a phone-shaped viewport (narrow OR short). Its own file because a
// resize block must sit last and restore the size.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const TIER = ".pill-model-effort";

describe("the markup the phone rule keys on", () => {
  it("marks the tier span with the class the stylesheet hides and the class JS toggles", () => {
    // `status.ts` writes the tier by id and the CSS reaches it by class, so the markup must carry both.
    const at = indexHtml.indexOf('id="ctx-effort-pill"');
    expect(at, 'static/index.html has no id="ctx-effort-pill"').toBeGreaterThan(-1);
    const open = indexHtml.lastIndexOf("<span", at);
    const end = indexHtml.indexOf(">", at);
    const tag = indexHtml.slice(open, end + 1);

    expect(tag, "the phone rule's hook").toContain("pill-model-effort");
    expect(tag, "the JS gate's channel").toContain("hidden");
  });
});

describe("the phone-shaped arm of the tier's visibility rule", () => {
  it("is display:none under BOTH the narrow and the short arm", () => {
    // `ruleContaining` demands one match per scope, so asking under each arm fails when one is dropped; both bodies
    // must carry the same declaration.
    const narrow = ruleContaining(loadCSS("15-input.css"), TIER, "48rem");
    const short = ruleContaining(loadCSS("15-input.css"), TIER, "30rem");
    expect(narrow.body).toMatch(/display:\s*none/);
    expect(short.body, "both arms hide it the same way").toBe(narrow.body);
  });
});

describe("the phone-shaped gate, measured at real viewport sizes", () => {
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

  /**
   * The span carries the class, not `.hidden` (the JS gate's `display: none !important`), or the CSS gate goes
   * unmeasured. The resize is asserted.
   */
  async function displayAt(
    width: number,
    height: number,
    className = "pill-model-effort",
  ): Promise<string> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    const span = document.createElement("span");
    span.className = className;
    span.textContent = "· max";
    document.body.appendChild(span);
    return getComputedStyle(span).display;
  }

  it("hides the tier on a narrow, tall viewport — a phone in portrait", async () => {
    expect(await displayAt(360, 800)).toBe("none");
  });

  it("hides the tier on a wide, SHORT viewport — the same phone rotated", async () => {
    // Wide and short: the narrow arm alone misses it.
    expect(await displayAt(900, 400)).toBe("none");
  });

  it("shows the tier on a viewport that is neither narrow nor short", async () => {
    // A landscape tablet clears both arms: measuring the short edge separates 1024x768 from 900x400.
    expect(await displayAt(1024, 768)).not.toBe("none");
  });

  it.each([
    [360, 800],
    [900, 400],
  ] as const)("hides Send's word at %ix%i", async (width, height) => {
    expect(await displayAt(width, height, "send-btn-label")).toBe("none");
  });

  it("shows Send's word on a viewport that is neither narrow nor short", async () => {
    expect(await displayAt(1024, 768, "send-btn-label")).not.toBe("none");
  });
});
