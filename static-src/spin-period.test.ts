import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { allRules, loadCSS } from "./__test-helpers__/css-rules.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// Every rotating ring turns at ONE period.

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

describe("the spinner period", () => {
  it("is read from one token by every vk-spin consumer, in every stylesheet", () => {
    const offenders: string[] = [];
    let consumers = 0;

    for (const [path, css] of Object.entries(sheets)) {
      for (const { selector, body } of allRules(css)) {
        // `allRules` descends into at-rules, so a `@keyframes` STEP arrives here as a style rule
        // whose selector is `from` / `to` / a percentage list.
        if (/^(?:from|to|-?[\d.]+%)(?:\s*,\s*(?:from|to|-?[\d.]+%))*$/.test(selector)) {
          continue;
        }
        if (!body.includes("vk-spin")) {
          continue;
        }
        consumers++;
        // `vk-spin[\w-]*` rather than `vk-spin`, because the idiom has two keyframes now: `vk-spin`
        // turns a ring's painted arc and `vk-spin-dash` marches the one mark that is a stroked arc
        // instead. One period for both is the rule; which property carries it is not.
        if (!/animation:\s*vk-spin[\w-]*\s+var\(--spin-dur\)/.test(body)) {
          offenders.push(`${path} { ${selector} }`);
        }
      }
    }

    expect(offenders, "every vk-spin rule takes its period from --spin-dur").toEqual([]);
    // A sweep that matched nothing would pass vacuously.
    expect(consumers, "the sweep found the spinner rules").toBeGreaterThanOrEqual(9);
  });

  it("declares that token, so the animation actually runs", () => {
    // The declaration and its value, from source: an element cannot tell a missing token from one
    // whose value happens to be the default.
    expect(loadCSS("01-tokens.css")).toContain("--spin-dur:");

    const el = document.createElement("span");
    el.className = "spinner";
    stage.appendChild(el);

    const [anim] = el.getAnimations();
    expect(anim, "the .spinner ring is animating").toBeDefined();
    // `animationName` is CSSAnimation's, not the Animation base's — narrowing also asserts this is
    // a CSS animation rather than a WAAPI one.
    expect(anim).toBeInstanceOf(CSSAnimation);
    if (anim instanceof CSSAnimation) {
      expect(anim.animationName).toBe("vk-spin");
    }
    expect(anim?.effect?.getTiming().duration).toBe(600);
    expect(anim?.effect?.getTiming().iterations).toBe(Infinity);
  });

  it("gives two rings of different sizes the same period", () => {
    // `.spinner` (16px) and `.btn-loading`'s ring (12px) share one period, or a save button beside a
    // loading list drifts against it.
    const big = document.createElement("span");
    big.className = "spinner";
    const small = document.createElement("span");
    small.className = "spinner-sm";
    stage.append(big, small);

    const durOf = (e: Element): EffectTiming["duration"] =>
      e.getAnimations()[0]?.effect?.getTiming().duration;

    // Both halves stated, or the case passes vacuously the moment the token goes missing and each
    // ring reports `undefined` — equal, and both stopped.
    expect(durOf(big)).toBe(600);
    expect(durOf(small)).toBe(600);
    expect(durOf(big)).toBe(durOf(small));
  });
});
