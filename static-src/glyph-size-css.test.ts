// Touch mode grows a control's target, and a glyph only as the button box's glyph grows: every glyph
// draws at its mouse-tier size, or one step (16px to 20px) up inside a button. The measured case covers
// the shipped markup; the source cases cover the glyphs the app builds at run time, which no single
// page mount renders.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { BUTTON_BOX, GLYPH_STEP } from "./__test-helpers__/button-reference.js";
import { manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** A context naming the touch tier: the attribute, the no-JS width arm's "not fine", or the media
 *  query. A negated coarse (`not (any-pointer: coarse)`, `:not([data-pointer="coarse"])`) is the
 *  mouse tier's own form and does not count. */
const TOUCH =
  /data-pointer="coarse"\]|:not\(\[data-pointer="fine"\]\)|(?<!not \()any-pointer:\s*coarse/;
const MOUSE_ONLY = /not \(any-pointer:\s*coarse\)|:not\(\[data-pointer="coarse"\]\)/;

/** A selector whose subject is a drawing: an `<svg>`, a registry tier class, or an icon or glyph
 *  wrapper. A `-btn` subject is a control box, which may grow with its row. */
const GLYPH_SUBJECT = /(^|[\s>+~&])svg\b[^\s>+~]*$|\.ic-[a-z]+[^\s>+~]*$|-(icon|glyph)\b[^\s>+~]*$/;
const SIZE_PROP =
  /^(inline-size|block-size|width|height|min-|max-|flex-basis|font-size|zoom|scale)/;

interface Decl {
  readonly sheet: string;
  /** At-rule preludes and selectors from the outermost block in. */
  readonly context: readonly string[];
  readonly prop: string;
  readonly value: string;
}

/** Every declaration in the shipped sheets, with the blocks it sits in. */
function declarations(): Decl[] {
  const out: Decl[] = [];
  for (const { name, css } of manifestSheets()) {
    const src = css.replace(/\/\*[\s\S]*?\*\//g, " ");
    const stack: string[] = [];
    let buf = "";
    const flush = (): void => {
      for (const d of buf.split(";")) {
        const at = d.indexOf(":");
        if (at > 0 && stack.length > 0) {
          out.push({
            sheet: name,
            context: [...stack],
            prop: d.slice(0, at).trim(),
            value: d.slice(at + 1).trim(),
          });
        }
      }
      buf = "";
    };
    for (const ch of src) {
      if (ch === "{") {
        const parts = buf.split(";");
        const selector = (parts.pop() ?? "").trim().replace(/\s+/g, " ");
        buf = parts.join(";");
        flush();
        stack.push(selector);
      } else if (ch === "}") {
        flush();
        stack.pop();
      } else {
        buf += ch;
      }
    }
  }
  return out;
}

/** The custom properties whose value differs on the touch tier: declared under a touch context, or
 *  reading one that is. */
function touchTokens(decls: readonly Decl[]): Set<string> {
  const custom = decls.filter((d) => d.prop.startsWith("--"));
  const out = new Set(custom.filter((d) => TOUCH.test(d.context.join(" "))).map((d) => d.prop));
  for (let grew = true; grew;) {
    grew = false;
    for (const d of custom) {
      if (!out.has(d.prop) && reads(d.value, out)) {
        out.add(d.prop);
        grew = true;
      }
    }
  }
  return out;
}

/** The button glyph's tokens, the one way a glyph follows the tier. */
const BUTTON_GLYPH: ReadonlySet<string> = new Set(["--icon-btn", "--icon-grow"]);

/** Where the button glyph is read: `.icon-btn`, the composer's pills and Send, the turn footer's and code
 *  block's action box, the hooks badge and the header room it keeps for it, the context pie and the mode pill's
 *  role icon. A new reader is a control that paints `--btn-h` or `--btn-h-sm`, added here. */
const BUTTON_GLYPH_READERS: readonly string[] = [
  "10-shell-app.css .icon-btn >> & > :where(.ic-ui), & > :where(.icon-swap) > :where(.ic-ui) inline-size",
  "10-shell-app.css .icon-btn >> & > :where(.ic-ui), & > :where(.icon-swap) > :where(.ic-ui) block-size",
  "14-tools.css .tool-group-has-hooks --hooks-w",
  "14-tools.css .tool-group-hooks >> & svg inline-size",
  "14-tools.css .tool-group-hooks >> & svg block-size",
  "15-input.css .pill >> & > :where(.ic-ui) inline-size",
  "15-input.css .pill >> & > :where(.ic-ui) block-size",
  "15-input.css .pill >> & > :where(.ic-inline) inline-size",
  "15-input.css .pill >> & > :where(.ic-inline) block-size",
  "15-input.css .pill-context >> & .context-ring inline-size",
  "15-input.css .pill-context >> & .context-ring block-size",
  "15-input.css .pill-role-icon >> & svg width",
  "15-input.css .pill-role-icon >> & svg height",
  "15-input.css .send-btn >> & svg width",
  "15-input.css .send-btn >> & svg height",
  "61-mcp-tools.css .turn-action-btn, .turn-rewind, .code-act-btn >> & > :where(.ic-ui) inline-size",
  "61-mcp-tools.css .turn-action-btn, .turn-rewind, .code-act-btn >> & > :where(.ic-ui) block-size",
];

/** The icon buttons that paint a row action's token or a menu row (`marotte/button-size.md`), each keeping `--icon-ui`. */
const DENSE_GLYPH_RESETS: readonly string[] = [
  "11-page-lists.css .entry .entry-actions > *: var(--icon-ui)",
  "31-exec-view.css .run-icon-btn: var(--icon-ui)",
  "17-settings.css .native-rule-remove: var(--icon-ui)",
  "18-pages.css .list-row-btn: var(--icon-ui)",
  "19-files.css @media (width <= 48rem) >> .fb-new-actions > .icon-btn: var(--icon-ui)",
  "60-mcp.css .mcp-row-actions > .icon-btn: var(--icon-ui)",
  "60-mcp.css .mcp-pair-del: var(--icon-ui)",
];

const reads = (value: string, tokens: ReadonlySet<string>): boolean =>
  [...value.matchAll(/var\((--[\w-]+)/g)].some((m) => tokens.has(m[1] ?? ""));

describe("the glyph declarations, read from source", () => {
  const decls = declarations();
  const tiered = touchTokens(decls);
  const other = new Set([...tiered].filter((t) => !BUTTON_GLYPH.has(t)));

  it("finds the touch tier's tokens", () => {
    // Premise: the sweep below is empty only because no glyph reads one of these.
    expect(tiered.has("--hit-floor")).toBe(true);
    expect(tiered.has("--row-tap")).toBe(true);
  });

  it("declares every icon token once, on :root, on every tier", () => {
    const arms = decls
      .filter((d) => d.prop.startsWith("--icon-") && d.context.at(-1) !== ":root")
      .map((d) => `${d.sheet} ${d.context.join(" >> ")} ${d.prop}`);
    expect(arms).toEqual([]);
    expect(decls.filter((d) => d.prop === "--icon-ui").length, "--icon-ui is declared").toBe(1);
  });

  it("grows the button glyph one step on touch", () => {
    expect(
      [...BUTTON_GLYPH].every((t) => tiered.has(t)),
      "the button glyph follows the tier",
    ).toBe(true);
  });

  it("sizes no glyph from a touch-tier token but the button glyph's, or under a touch-tier rule", () => {
    const grown = decls
      .filter((d) => {
        const subject = d.context.at(-1) ?? "";
        const where = d.context.join(" ");
        return (
          SIZE_PROP.test(d.prop) &&
          subject.split(",").some((s) => GLYPH_SUBJECT.test(s.trim())) &&
          !MOUSE_ONLY.test(where) &&
          (TOUCH.test(where) || reads(d.value, other))
        );
      })
      .map((d) => `${d.sheet} ${d.context.at(-1) ?? ""} { ${d.prop}: ${d.value} }`);
    expect(grown).toEqual([]);
  });

  it("reads the button glyph only in the rules of a control that paints the button box", () => {
    const readers = decls
      .filter((d) => d.context.at(-1) !== ":root" && reads(d.value, BUTTON_GLYPH))
      .map((d) => `${d.sheet} ${d.context.join(" >> ")} ${d.prop}`);
    expect(readers).toEqual(BUTTON_GLYPH_READERS);
  });

  it("keeps the 16px glyph on every icon button painted at a dense token", () => {
    const resets = decls
      .filter((d) => d.prop === "--btn-glyph")
      .map((d) => `${d.sheet} ${d.context.join(" >> ")}: ${d.value}`);
    expect(resets).toEqual(DENSE_GLYPH_RESETS);
  });
});

describe("the glyphs, measured over the shipped markup", () => {
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The whole shipped body at one size and tier, hidden containers revealed and dialogs shown. */
  async function mountPage(width: number, height: number, tier: "fine" | "coarse"): Promise<void> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    document.documentElement.setAttribute("data-pointer", tier);
    const body = indexHtml.slice(indexHtml.indexOf("<body"), indexHtml.indexOf("</body>"));
    document.body.innerHTML = body.slice(body.indexOf(">") + 1);
    for (const el of document.querySelectorAll<HTMLElement>("[hidden], .hidden")) {
      el.removeAttribute("hidden");
      el.classList.remove("hidden");
    }
    for (const dlg of document.querySelectorAll("dialog")) {
      dlg.show();
    }
  }

  /** Each drawn glyph's painted size: its box fitted to the viewBox, as `preserveAspectRatio`'s
   *  default `xMidYMid meet` draws it, so a box that widens around an unchanged drawing passes. With
   *  it, the painted height of the control that holds it, if any. */
  function drawings(): { readonly glyph: string; readonly box: number | null }[] {
    return [...document.body.querySelectorAll<SVGSVGElement>("svg")]
      .filter((g) => g.checkVisibility())
      .map((g) => {
        const r = g.getBoundingClientRect();
        const vb = g.viewBox.baseVal;
        const fit =
          vb.width > 0 && vb.height > 0 ? Math.min(r.width / vb.width, r.height / vb.height) : 1;
        const w = vb.width > 0 ? vb.width * fit : r.width;
        const h = vb.height > 0 ? vb.height * fit : r.height;
        const owner = g.closest("[id]")?.id ?? "?";
        const control = g.closest('button, summary, a[href], [role="button"]');
        return {
          glyph: `#${owner} svg ${w.toFixed(1)}x${h.toFixed(1)}`,
          box: control === null ? null : control.getBoundingClientRect().height,
        };
      });
  }

  /** The button box's painted heights: `--btn-h` and `--btn-h-sm` on a mouse, one 44px box on touch. */
  const BOXES = { fine: [BUTTON_BOX.fine, 24], coarse: [BUTTON_BOX.coarse] } as const;
  const isBox = (h: number | null, tier: "fine" | "coarse"): boolean =>
    h !== null && BOXES[tier].some((b) => Math.abs(h - b) < 0.05);

  it.each([
    ["an iPad in landscape", 1024, 768],
    ["an iPad in portrait", 820, 1180],
    ["a phone", 390, 844],
  ])(
    "draws every glyph the same on a finger as on a mouse, or one step up in the button box, on %s",
    async (_l, w, h) => {
      await mountPage(w, h, "fine");
      const fine = drawings();
      // A floor, so a markup change that emptied the page cannot pass vacuously.
      expect(fine.length).toBeGreaterThan(40);
      await mountPage(w, h, "coarse");
      const coarse = drawings();
      expect(coarse.length, "glyphs drawn on coarse").toBe(fine.length);
      const size = (g: string): number[] => (g.split(" ").at(-1) ?? "").split("x").map(Number);
      const stepped = (g: string, was: string): boolean =>
        size(g).every((v, k) => Math.abs(v - (size(was)[k] ?? 0) - GLYPH_STEP) < 0.05);
      // A glyph may grow only where its control paints the button box on both tiers.
      const wrong = coarse.flatMap((d, i) => {
        const was = fine[i];
        if (was === undefined || d.glyph === was.glyph) {
          return [];
        }
        const grown =
          isBox(was.box, "fine") && isBox(d.box, "coarse") && stepped(d.glyph, was.glyph);
        return grown ? [] : [`${d.glyph} in a ${String(d.box)}px box (fine: ${was.glyph})`];
      });
      expect(wrong).toEqual([]);
      // The chat toolbar's glyphs are the reference: one step up on touch.
      expect(coarse.map((d) => d.glyph).filter((g) => g.startsWith("#settings-btn svg"))).toEqual([
        `#settings-btn svg ${(16 + GLYPH_STEP).toFixed(1)}x${(16 + GLYPH_STEP).toFixed(1)}`,
      ]);
    },
  );
});
