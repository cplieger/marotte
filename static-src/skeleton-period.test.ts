import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// Every placeholder pulses at ONE period, read from one token.
//
// Modelled on spin-period.test.ts, and structured as its two halves for the same
// reason: either alone passes while the app is broken. The SWEEP is over every
// stylesheet rather than the one that carries the rule today, so a second
// consumer written with a literal fails here instead of shipping — which is the
// rule that file's own token comment generalises ("a literal here let two rings
// of one size sit side by side visibly drifting"). And a literal is not the only
// way to get this wrong: `var(--skeleton-dur)` with no declaration behind it is
// valid CSS text that resolves to nothing at computed-value time, which
// invalidates the whole `animation` shorthand and runs NO animation — a
// placeholder that has stopped rather than one at the wrong speed, and nothing
// to grep for. So the second half reads the period off a real element.
//
// The easing is the third case, on the same element, because the vocabulary rule
// is the same shape as the period rule: `marotte-ui.md` states four named
// easings and "no bare `ease` or `ease-in-out`", and a bare keyword is greppable
// only if you already know to look for it.
// ---------------------------------------------------------------------------

/** Every shipped stylesheet, so the sweep cannot miss a file. */
const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

/** A real bar: `.skeleton` carries the pulse, `.skeleton-line` gives it a box. */
function bar(): HTMLDivElement {
  const el = document.createElement("div");
  el.className = "skeleton skeleton-line";
  stage.appendChild(el);
  return el;
}

describe("the skeleton period", () => {
  it("is read from one token by every vk-skeleton consumer, in every stylesheet", () => {
    const offenders: string[] = [];
    let consumers = 0;

    for (const [path, css] of Object.entries(sheets)) {
      for (const { selector, body } of allRules(css)) {
        // `allRules` descends into at-rules, so a `@keyframes` STEP arrives here
        // as a style rule whose selector is `from` / `to` / a percentage list.
        // `vk-skeleton`'s own steps declare opacity and no animation, so they
        // would read as consumers that take no period.
        if (/^(?:from|to|-?[\d.]+%)(?:\s*,\s*(?:from|to|-?[\d.]+%))*$/.test(selector)) {
          continue;
        }
        if (!body.includes("vk-skeleton")) {
          continue;
        }
        consumers++;
        if (!/animation:\s*vk-skeleton\s+var\(--skeleton-dur\)/.test(body)) {
          offenders.push(`${path} { ${selector} }`);
        }
      }
    }

    expect(offenders, "every vk-skeleton rule takes its period from --skeleton-dur").toEqual([]);
    // A sweep that matched nothing would pass vacuously.
    expect(consumers, "the sweep found the skeleton rule").toBeGreaterThanOrEqual(1);
  });

  it("declares that token, so the animation actually runs", () => {
    // The declaration and its value, from source: an element cannot tell a
    // missing token from one whose value happens to be the default.
    expect(loadCSS("01-tokens.css")).toContain("--skeleton-dur:");

    const el = bar();

    const [anim] = el.getAnimations();
    expect(anim, "the .skeleton bar is animating").toBeDefined();
    // `animationName` is CSSAnimation's, not the Animation base's — narrowing
    // also asserts this is a CSS animation rather than a WAAPI one.
    expect(anim).toBeInstanceOf(CSSAnimation);
    if (anim instanceof CSSAnimation) {
      expect(anim.animationName).toBe("vk-skeleton");
    }
    expect(anim?.effect?.getTiming().duration).toBe(1500);
    expect(anim?.effect?.getTiming().iterations).toBe(Infinity);
  });

  it("eases on --ease-standard's curve rather than a bare keyword", () => {
    const el = bar();
    const style = getComputedStyle(el);

    // The token's own resolved value rather than a literal, so this asserts the
    // RELATION the rule states (the pulse reads --ease-standard) and survives a
    // retune of that curve, which a hardcoded triple would turn red.
    const strip = (s: string): string => s.replace(/\s+/g, "");
    const standard = strip(style.getPropertyValue("--ease-standard"));
    expect(standard, "--ease-standard resolves on the element").not.toBe("");

    const used = strip(style.animationTimingFunction);
    expect(used).toBe(standard);
    // The bare keyword is the shape being excluded, and it is excluded by VALUE
    // as well as by the equality above: `ease-in-out` computes to a different
    // curve, so a rule that regressed to it fails both halves.
    expect(used).toMatch(/^cubic-bezier\(/);
    expect(used).not.toBe(strip("cubic-bezier(0.42, 0, 0.58, 1)"));
  });
});
