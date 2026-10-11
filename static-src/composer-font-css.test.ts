// The composer reads the transcript's own rung (`--fs-md`), except where the coarse-tier 16px iOS text-entry floor
// (61-mcp-tools.css) wins. Source half: no literal and no `--fs-field` token. Measurement half: the sizes agree, and the
// font moves the resting box by nothing, because padding is `--composer-rest-h` minus `1lh`.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

const RUNG = "--fs-md";

/** Sliced from the shipped page rather than hand-written: the pill row's height is the sum of its real controls. */
function composerMarkup(): string {
  const open = indexHtml.indexOf('<form id="prompt-form"');
  expect(open, 'static/index.html has no <form id="prompt-form">').toBeGreaterThan(-1);
  const close = indexHtml.indexOf("</form>", open);
  expect(close, "the prompt form is not closed").toBeGreaterThan(open);
  return indexHtml.slice(open, close + "</form>".length);
}

describe("the declarations, read from source", () => {
  // The run tab's composer shares this rule (`[id="run-composer-input"]`), so membership, not equality.
  const inputRule = allRules(loadCSS("15-input.css")).find((r) =>
    r.selector
      .split(",")
      .map((sel) => sel.trim())
      .includes('[id="prompt-input"]'),
  );

  it("reads the type scale's own rung, never a literal", () => {
    expect(inputRule, '15-input.css has no [id="prompt-input"] rule').toBeDefined();
    expect(inputRule?.body).toMatch(new RegExp(`font-size:\\s*var\\(${RUNG}\\)`));
    // A literal would put a platform fact in a component file; `--fs-lg` would decouple the composer from the transcript.
    expect(inputRule?.body).not.toMatch(/font-size:\s*1rem/);
    expect(inputRule?.body).not.toMatch(/font-size:\s*\d+px/);
    expect(inputRule?.body).not.toMatch(/font-size:\s*var\(--fs-lg\)/);
  });

  it("declares no --fs-field token, so a tier floor cannot come back silently", () => {
    // A re-added token would deliver 16px through a file this test does not read, so the absence is asserted at its home.
    expect(loadCSS("01-tokens.css")).not.toContain("--fs-field");
  });
});

describe("the composer, measured at real viewport sizes", () => {
  // Restores the entry size in `afterAll`, read off the frame (`page.viewport` has no getter) rather than copied from
  // `vitest.config.ts`, so later files cannot silently measure at a stale size.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
    document.documentElement.style.removeProperty("font-size");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /**
   * The composer at one viewport and pointer tier, beside a transcript bubble. `pointer` is what `pointer-tier.ts`
   * writes, or null for the no-JS case. The resize is asserted, or a stuck viewport would make every case vacuous.
   */
  async function mountAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<{ input: HTMLTextAreaElement; pills: HTMLElement; prose: HTMLElement }> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    if (pointer === null) {
      document.documentElement.removeAttribute("data-pointer");
    } else {
      document.documentElement.setAttribute("data-pointer", pointer);
    }
    document.body.innerHTML =
      `<div class="message assistant"><p id="probe-prose">prose</p></div>` + composerMarkup();
    const input = document.getElementById("prompt-input");
    const pills = document.querySelector<HTMLElement>(".prompt-pills");
    const prose = document.getElementById("probe-prose");
    if (!(input instanceof HTMLTextAreaElement) || pills === null || prose === null) {
      throw new Error("the composer subtree did not mount");
    }
    return { input, pills, prose };
  }

  function fontPx(el: Element): number {
    return Number.parseFloat(getComputedStyle(el).fontSize);
  }

  it.each([
    ["a phone with a coarse pointer", 390, 844, "coarse" as const],
    // An iPad in landscape: coarse past 48rem, where the width fallback does not reach. The tier is the pointer, not the width.
    ["a WIDE coarse viewport", 1024, 768, "coarse" as const],
    // `pointer-tier.ts` corrects the tier on the first real PointerEvent, so there is a window with no attribute.
    ["a phone with no pointer tier resolved yet", 390, 844, null],
    ["a desktop with a fine pointer", 1280, 800, "fine" as const],
    ["a desktop with no pointer tier resolved yet", 1280, 800, null],
  ])("computes the transcript's own size on %s", async (_label, w, h, pointer) => {
    const { input, prose } = await mountAt(w, h, pointer);
    // Compared against the rendered transcript, not 14, so a `--fs-md` retune keeps the meaning. Coarse arms are
    // excluded: the 16px iOS text-entry floor beats this control there (the page ships no `maximum-scale=1.0`,
    // and `#prompt-input` has `autofocus`). The next case pins that side.
    if (pointer === "coarse" || (pointer === null && w <= 768)) {
      expect(fontPx(input), "the coarse floor is in force").toBe(16);
      return;
    }
    expect(fontPx(input)).toBe(fontPx(prose));
  });

  it("is 14px on a mouse today, so a retune of the rung is a visible change rather than a silent one", async () => {
    // On a coarse pointer this control is 16px by platform requirement, so the rung is observable only on the fine tier.
    const { input } = await mountAt(1280, 800, "fine");
    expect(fontPx(input)).toBe(14);
  });

  it("is 16px on a finger, which is iOS's auto-zoom threshold rather than a rung", async () => {
    const { input } = await mountAt(390, 844, "coarse");
    expect(fontPx(input)).toBe(16);
  });

  // composer-row-height-css.test.ts owns the box heights; this case is only that the font does not move the box.
  it.each([
    [390, 844, "coarse" as const, 44, 45],
    [1280, 800, "fine" as const, 40, 41],
  ])(
    "leaves the resting box and the pill row alone at %ix%i on a %s pointer",
    async (w, h, pointer, boxH, rowH) => {
      // Padding is `calc((var(--composer-rest-h) - 1lh) / 2)`, so a line-height change eats the padding it adds and the
      // resting height is unchanged by construction. A two-line composer does grow by the delta.
      const { input, pills } = await mountAt(w, h, pointer);
      expect(input.getBoundingClientRect().height).toBe(boxH);
      // The pill row's border-block-start is the boundary between the rows, so its box is one hairline taller than its band.
      expect(pills.getBoundingClientRect().height, "the two rows are one band").toBe(rowH);

      // 16px set on the element alone: the root size and `--fs-md` would also move the `rem`-derived `--composer-rest-h`
      // being compared, while `1lh` follows the font.
      input.style.setProperty("font-size", "16px");
      expect(fontPx(input), "the pre-change size is what is being compared").toBe(16);
      expect(input.getBoundingClientRect().height, "raising the font moved the box").toBe(boxH);
    },
  );
});
