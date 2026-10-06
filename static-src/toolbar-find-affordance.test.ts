// The toolbar's find control: when it is drawn, which glyph, and how it leaves. CSS and DOM halves
// fail separately. It COLLAPSES where there is nothing to search, and draws a filter glyph where the
// box only filters.
import { describe, it, expect } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { findGlyph } from "./icons.js";

/** Comments quote the values they explain, so they are stripped before a scan. */
function body(sheet: string, selector: string): string {
  return ruleContaining(loadCSS(sheet), selector, "top").body.replace(/\/\*[\s\S]*?\*\//g, " ");
}

describe("the collapse is animated, not a display swap", () => {
  const collapsed = body("12-chat.css", ".chat-toolbar > .icon-btn.is-collapsed");
  const base = body("12-chat.css", ".chat-toolbar > .icon-btn");

  it("never reaches for display, which cannot animate", () => {
    // `.hidden` is `display: none !important` (40-a11y.css) and `display` is a
    // discrete property: the button would vanish in one frame and the bar's width
    // would jump. 26-dock.css made the same choice for the same reason.
    expect(collapsed).not.toMatch(/display:/);
    expect(collapsed).not.toMatch(/!important/);
  });

  it("takes the box, the ink AND the gap to zero", () => {
    // Three, not one: `min-inline-size` beats the toolbar's `--btn-h` floor, and the negative margin
    // cancels the `gap` a zero-width item still costs.
    expect(collapsed, "the box").toMatch(/inline-size:\s*0/);
    expect(collapsed, "the floor the toolbar sets").toMatch(/min-inline-size:\s*0/);
    expect(collapsed, "the ink").toMatch(/opacity:\s*0/);
    expect(collapsed, "the gap it is still entitled to").toMatch(
      /margin-inline-start:\s*calc\(-1 \* var\(--toolbar-gap\)\)/,
    );
  });

  it("derives that gap from the one the toolbar declares", () => {
    // Named once, consumed twice, so the collapse cannot drift from the row.
    const toolbar = body("12-chat.css", ".chat-toolbar");
    expect(toolbar).toMatch(/--toolbar-gap:/);
    expect(toolbar).toMatch(/gap:\s*var\(--toolbar-gap\)/);
  });

  it("leaves the accessibility tree and the tab order, at the END of the fade", () => {
    // `visibility` flips only at the transition's end, so the fade plays and THEN the control leaves
    // focus and the accessibility tree.
    expect(collapsed).toMatch(/visibility:\s*hidden/);
    expect(collapsed).toMatch(/pointer-events:\s*none/);
    expect(base, "visibility must be in the transition or it flips at once").toMatch(
      /visibility var\(--dur-exit\)/,
    );
  });

  it("transitions every property it changes", () => {
    for (const prop of ["opacity", "inline-size", "min-inline-size", "margin-inline-start"]) {
      expect(base, prop).toContain(`${prop} var(--dur-exit)`);
    }
  });

  it("keeps the press and hover transitions it would otherwise steal", () => {
    // 03-base.css's trap: an unlayered `:where(…):active` (0,1,0) loses the whole `transition` to a
    // more specific rule, so these three must be restated.
    expect(base, "the press scale").toMatch(/transform var\(--dur-micro\)/);
    expect(base, "the hover wash").toMatch(/background var\(--dur-micro\)/);
    expect(base, "the hover ink").toMatch(/color var\(--dur-micro\)/);
  });
});

describe("the glyph says which of the two a page has", () => {
  it("draws a magnifier for a search and a funnel for a filter", () => {
    expect(findGlyph("search")).toContain("<circle");
    expect(findGlyph("search")).not.toContain("<polygon");
    expect(findGlyph("filter")).toContain("<polygon");
    expect(findGlyph("filter")).not.toContain("<circle");
  });

  it("draws them at one size tier, so the toolbar and the box agree by construction", () => {
    // One producer for the toolbar button and the field's glyph, sized by TIER, so they cannot disagree.
    expect(findGlyph("search")).toContain('class="ic-ui"');
    expect(findGlyph("filter")).toContain('class="ic-ui"');
    expect(findGlyph("search")).not.toContain("width=");
  });
});
