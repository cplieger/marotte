// Touch mode grows a control's target, never its drawing: every glyph draws at its mouse-tier
// size. The measured case covers the shipped markup; the source cases cover the glyphs the app
// builds at run time, which no single page mount renders.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

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

const reads = (value: string, tokens: ReadonlySet<string>): boolean =>
  [...value.matchAll(/var\((--[\w-]+)/g)].some((m) => tokens.has(m[1] ?? ""));

describe("the glyph declarations, read from source", () => {
  const decls = declarations();
  const tiered = touchTokens(decls);

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

  it("sizes no glyph from a touch-tier token or under a touch-tier rule", () => {
    const grown = decls
      .filter((d) => {
        const subject = d.context.at(-1) ?? "";
        const where = d.context.join(" ");
        return (
          SIZE_PROP.test(d.prop) &&
          subject.split(",").some((s) => GLYPH_SUBJECT.test(s.trim())) &&
          !MOUSE_ONLY.test(where) &&
          (TOUCH.test(where) || reads(d.value, tiered))
        );
      })
      .map((d) => `${d.sheet} ${d.context.at(-1) ?? ""} { ${d.prop}: ${d.value} }`);
    expect(grown).toEqual([]);
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
   *  default `xMidYMid meet` draws it, so a box that widens around an unchanged drawing passes. */
  function drawings(): string[] {
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
        return `#${owner} svg ${w.toFixed(1)}x${h.toFixed(1)}`;
      });
  }

  it.each([
    ["an iPad in landscape", 1024, 768],
    ["an iPad in portrait", 820, 1180],
    ["a phone", 390, 844],
  ])("draws every glyph the same on a finger as on a mouse, on %s", async (_l, w, h) => {
    await mountPage(w, h, "fine");
    const fine = drawings();
    // A floor, so a markup change that emptied the page cannot pass vacuously.
    expect(fine.length).toBeGreaterThan(40);
    await mountPage(w, h, "coarse");
    const coarse = drawings();
    expect(coarse.length, "glyphs drawn on coarse").toBe(fine.length);
    expect(coarse.filter((g, i) => g !== fine[i])).toEqual([]);
  });
});
