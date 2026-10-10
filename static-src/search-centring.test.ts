// The vertical centring of a search bar's buttons, pinned as SOURCE facts in the stylesheets AND as
// a structural fact in the builders.

import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import findInChatSrc from "./find-in-chat.ts?raw";
import filesSearchSrc from "./files-search.ts?raw";
import editorFindSrc from "./editor-find.ts?raw";
import searchShellSrc from "./search-shell.ts?raw";
import searchPopupSrc from "./search-popup.ts?raw";

/** The five builders the glyph scan below is the whole population of. */
const searchBuilderSources: Record<string, string> = {
  "find-in-chat.ts": findInChatSrc,
  "files-search.ts": filesSearchSrc,
  "editor-find.ts": editorFindSrc,
  "search-shell.ts": searchShellSrc,
  "search-popup.ts": searchPopupSrc,
};

/** All three are in the table on purpose: the SAME defect was in each, so a fix in one would have
 *  left the others wrong and the bars looking different from each other. */
const BARS: { name: string; sheet: string; button: string; caseToggle: string }[] = [
  {
    name: "find in chat",
    sheet: "24-find.css",
    button: ".chat-find-btn",
    caseToggle: ".chat-find-case",
  },
  {
    name: "find in files",
    sheet: "19-files.css",
    button: ".fb-search-btn",
    caseToggle: ".fb-search-case",
  },
  {
    name: "find in file (editor)",
    sheet: "20-editor.css",
    button: ".editor-find-btn",
    caseToggle: ".editor-find-case",
  },
];

/** Strip comments so prose ABOUT a declaration is never read as one — these rules quote the
 *  values they exist to explain. */
function body(sheet: string, selector: string): string {
  return ruleContaining(loadCSS(sheet), selector, "top").body.replace(/\/\*[\s\S]*?\*\//g, " ");
}

describe("search-bar icon buttons", () => {
  it("collapse the strut around their SVG with line-height: 0", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(
        decls,
        `${bar.sheet}: ${bar.button} holds a replaced element, so the line box's strut is what ` +
          `oversizes it; line-height: 0 is what leaves the glyph's own box for align-items to centre`,
      ).toMatch(/line-height:\s*0\s*;/);
    }
  });

  it("never carry line-height: 1, the value that looked like the fix", () => {
    // All three bars shipped `line-height: 1`. It moves a glyph band by EXACTLY zero relative to
    // its box centre (the leading is symmetric either way) and shrinks the box while doing it, so
    // it was a change that measured as nothing.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      const m = /line-height:\s*([^;]+)/.exec(decls);
      expect(
        m?.[1]?.trim(),
        `${bar.sheet}: ${bar.button} must not restate line-height: 1 as a centring fix`,
      ).toBe("0");
    }
  });

  it("center on both axes and hold a fixed square", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} align-items`).toMatch(/align-items:\s*center/);
      expect(decls, `${bar.sheet}: ${bar.button} justify-content`).toMatch(
        /justify-content:\s*center/,
      );
    }
  });

  it("agree on ONE size, and it is the app's button token", () => {
    // The two original bars disagreed — 1.75rem against var(--btn-h) — so two surfaces that
    // deliberately share a vocabulary rendered at different sizes. 1.75rem (28px) was also short of
    // the input beside it, so the row read as misaligned before anything was centred.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} inline-size`).toMatch(
        /inline-size:\s*var\(--btn-h\)/,
      );
      expect(decls, `${bar.sheet}: ${bar.button} block-size`).toMatch(
        /block-size:\s*var\(--btn-h\)/,
      );
    }
  });

  it("declare no font-size, because there is no text left in them to size", () => {
    // A font-size on an SVG-only button is a value that looks load-bearing and is not.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.button);
      expect(decls, `${bar.sheet}: ${bar.button} should have no font-size`).not.toMatch(
        /font-size:/,
      );
    }
  });
});

describe("the Aa match-case toggle", () => {
  it("trims its box to the cap band in every bar", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      expect(
        decls,
        `${bar.sheet}: ${bar.caseToggle} keeps its letters, so it needs the cap-band trim rather ` +
          `than the strut collapse — the trim makes the box edges the cap band, so centring the ` +
          `box centres the letterforms with no per-font value`,
      ).toMatch(/text-box:\s*trim-both\s+cap\s+alphabetic/);
    }
  });

  it("restores the strut its sibling rule collapsed", () => {
    // It inherits `line-height: 0` from the icon-button rule, and that answer is for a REPLACED
    // element. A text node in a zero line box is a different problem, so the toggle says `normal`
    // explicitly and lets the trim do the centring.
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      expect(decls, `${bar.sheet}: ${bar.caseToggle} line-height`).toMatch(
        /line-height:\s*normal\s*;/,
      );
    }
  });

  it("declares no NUMERIC line-height, which would move the letters by zero", () => {
    for (const bar of BARS) {
      const decls = body(bar.sheet, bar.caseToggle);
      const m = /line-height:\s*([^;]+)/.exec(decls);
      expect(
        m?.[1]?.trim(),
        `${bar.sheet}: the offset formula has no line-height term, so a number here is a change ` +
          `that looks like the fix and measures as nothing`,
      ).toBe("normal");
    }
  });

  it("draws its latched fill from 70-selection.css and nowhere else", () => {
    // No local selected state, in any bar. The consolidated rule set is what makes one selected
    // treatment across the app, and it has to come last in the MANIFEST to beat each feature file's
    // equal-specificity :hover.
    const selection = loadCSS("70-selection.css");
    for (const bar of BARS) {
      const pressed = `${bar.caseToggle}[aria-pressed="true"]`;
      const rule = ruleContaining(selection, pressed, "top");
      expect(rule.body, `${pressed} must take the shared selected fill`).toMatch(
        /background:\s*var\(--c-selected-bg\)/,
      );
      const local = body(bar.sheet, bar.caseToggle);
      expect(
        local,
        `${bar.sheet}: ${bar.caseToggle} must not paint its own selected background`,
      ).not.toMatch(/--c-selected-bg/);
    }
  });
});

describe("a search bar's ARROWS are real elements, not characters", () => {
  // The DOM half of this contract — that `searchIconButton` produces an <svg> and no text node — is
  // in search-shell.test.ts. This file's half is the source scan below, read through Vite `?raw`
  // imports rather than `node:fs`.
  it("is never a bare ↑ or ↓ character anywhere in a search bar's builder", () => {
    // A grep-shaped guard, because the failure mode is a NEW button rather than an edit to an
    // existing one: these builders are the population, and an arrow character in any of them is the
    // bug coming back.
    for (const [file, src] of Object.entries(searchBuilderSources)) {
      // Comments explain the glyphs, so they are stripped before the scan.
      const code = src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
      for (const glyph of ["\\u2191", "\\u2193"]) {
        expect(
          code,
          `${file} must not carry a bare ${glyph} glyph: a text node's LINE BOX is what ` +
            `align-items centres, so the ink lands off-centre by a font-dependent amount`,
        ).not.toContain(glyph);
      }
    }
  });
});

describe("the page search popup's × takes the same shape", () => {
  // A FOURTH bar, and the one that is four surfaces: History, the configuration browser and the git
  // view's two panels all share `.page-find` (search-popup.ts), so one rule is the whole
  // population.
  const decls = body("24-find.css", ".page-find-btn");

  it("collapses the strut around its SVG", () => {
    expect(decls).toMatch(/line-height:\s*0\s*;/);
  });

  it("centres on both axes and holds the app's button square", () => {
    expect(decls).toMatch(/align-items:\s*center/);
    expect(decls).toMatch(/justify-content:\s*center/);
    expect(decls).toMatch(/inline-size:\s*var\(--btn-h\)/);
    expect(decls).toMatch(/block-size:\s*var\(--btn-h\)/);
  });

  it("declares no font-size, because there is no text in it", () => {
    expect(decls).not.toMatch(/font-size:/);
  });

  it("has no match-case rule to carry, in any stylesheet", () => {
    // The tell that its absence is deliberate rather than forgotten: no `.page-find-case` exists
    // anywhere, so nothing is styled for a control the builder does not make.
    const files = ["24-find.css", "70-selection.css"];
    for (const file of files) {
      expect(
        loadCSS(file),
        `${file} must not style a toggle this bar has no reason to hold`,
      ).not.toContain(".page-find-case");
    }
  });
});
