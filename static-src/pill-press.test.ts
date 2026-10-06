// Cascade guard for the pill press (03-base.css + 15-input.css + 70-selection.css).

import { describe, it, expect } from "vitest";
// The stylesheet reader is shared with tab-dot.test.ts (see the note there): no app stylesheet is
// loaded, so both suites assert SOURCE facts, and a second copy of a brace-matching parser is the
// duplication to avoid.
import { loadCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The `transition:` declaration of a rule body, excluding nested blocks — a nested `&:hover`/`&
 *  svg` can carry its own and must not be mistaken for the rule's. */
function ownTransition(body: string): string {
  const flat = body.replace(/&[^{]*\{[^{}]*\}/g, "").replace(/&[^{]*\{[\s\S]*?\n {2}\}/g, "");
  const m = /transition:([^;]*);/.exec(flat);
  expect(m, "rule declares no transition").not.toBeNull();
  return m?.[1] ?? "";
}

describe("the pill press is a colour step, and it animates", () => {
  it("names background in the transition that actually wins", async () => {
    const css = loadCSS("15-input.css");
    const transition = ownTransition(ruleBody(css, ".pill"));
    expect(
      /\bbackground\b/.test(transition),
      `.pill's own transition must name background, or the press colour lands in
one frame. Its list is the one that wins the (0,1,0) tie against 03-base.css's
:where(…):active. Got: ${transition.trim()}`,
    ).toBe(true);
  });

  it("presses one rung deeper than it hovers", async () => {
    const css = loadCSS("15-input.css");
    // Rest is --c-bg-primary, hover --c-bg-tertiary, press --c-bg-elevated: three rungs of the one
    // surface ramp, so the press is visibly past the hover instead of being a differently-named
    // token that happens to be nearby.
    expect(/background: var\(--c-bg-tertiary\)/.test(ruleBody(css, ".pill"))).toBe(true);
    const press = ruleBody(css, ".pill:active");
    expect(/background: var\(--c-bg-elevated\)/.test(press)).toBe(true);
    expect(/transition:/.test(press)).toBe(false);
  });

  it("is not joined by a scale, because no scale press exists any more", async () => {
    const css = loadCSS("03-base.css").replace(/\/\*[\s\S]*?\*\//g, " ");
    expect(css, "no rule in 03-base.css may press with a transform").not.toMatch(
      /:active[^{]*\{[^}]*transform:/u,
    );
    expect(css, "the app-wide press is the inset wash").toMatch(
      /:where\(button, summary, \[role="button"\]\):active\s*\{\s*box-shadow: inset/u,
    );
  });

  it("lets the toggled-on press win on specificity, not on file order", async () => {
    // Without the press declaration a toggled-on pill would show the resting selected fill under
    // the finger.
    const sel = loadCSS("70-selection.css");
    const press = ruleContaining(sel, '.pill[aria-pressed="true"]:active');
    expect(
      /background:\s*var\(--c-selected-bg-press\)/.test(press.body),
      `The rule listing .pill[aria-pressed="true"]:active must be the one that
sets the press fill. Got body: ${press.body.trim().slice(0, 200)}`,
    ).toBe(true);
  });

  it("puts each selected state's selector in the rule that sets that state", async () => {
    // The same association for the other two rungs, so a selector cannot drift between the resting,
    // hover and press lists unnoticed. `.pill-role-item` is the one member that appears in all
    // three plus a :focus-visible.
    const sel = loadCSS("70-selection.css");
    for (const [selector, decl] of [
      [".pill-role-item.active", "background: var(--c-selected-bg)"],
      [".pill-role-item.active:hover", "background: var(--c-selected-bg-hover)"],
      [".pill-role-item.active:active", "background: var(--c-selected-bg-press)"],
    ] as const) {
      const rule = ruleContaining(sel, selector);
      expect(
        rule.body.includes(decl),
        `${selector} is listed on a rule that does not declare ${decl}`,
      ).toBe(true);
    }
  });

  it("colours the metadata a selected row's ink cannot reach", async () => {
    // `color` on the parent reaches only what INHERITS it, and every one of these declares its own.
    // Measured on the selected fill they ran 1.8-3.7:1, so the row the consolidation exists to make
    // legible had illegible metadata.
    const sel = loadCSS("70-selection.css");
    for (const selector of [
      ".pill-model-item.active .pill-model-meta",
      ".pill-role-item.active .pill-role-scope",
      ".popup-item.active .popup-meta",
      ".picker-btn.active .picker-meta",
      ".fb-row.fb-row-selected .fb-meta",
    ]) {
      const rule = ruleContaining(sel, selector);
      expect(
        /color:\s*var\(--c-selected-[\w-]+-fg\)/.test(rule.body),
        `${selector} must take an on-selected ink, not the row's inherited colour`,
      ).toBe(true);
    }
  });

  it("keeps both files unlayered, which is what makes source order decide", async () => {
    for (const name of ["03-base.css", "15-input.css"]) {
      const css = loadCSS(name);
      expect(/^@layer\s+[\w-]+\s*\{/m.test(css), `${name} opened a layer`).toBe(false);
    }
  });
});
