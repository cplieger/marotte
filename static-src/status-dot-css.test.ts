// The sidebar's connection MARK is STILL when the connection is healthy, and it is a decorative
// span rather than a control.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS, atRuleBody, loadCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The mark as `static/index.html` authors it: a decorative SPAN. */
function mountDot(cls: string): HTMLElement {
  const dot = document.createElement("span");
  dot.className = cls;
  dot.setAttribute("aria-hidden", "true");
  document.body.replaceChildren(dot);
  return dot;
}

describe("the sidebar connection mark", () => {
  it("carries no animation once connected", () => {
    const dot = mountDot("status-dot connected");
    expect(getComputedStyle(dot).animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("still breathes while connecting, on its own animation", () => {
    // The beat is the mark's OWN animation, created only while it is unsettled, so an idle app runs
    // none at all. Driven to two known phases rather than sampled over time — deterministic, and it
    // proves the mark follows the beat rather than merely naming it.
    const dot = mountDot("status-dot");
    const beat = dot.getAnimations()[0];
    expect(beat, "an unsettled mark must carry its own beat").toBeDefined();
    beat!.pause();

    // The midpoint is READ off the animation rather than restated: a literal transcription of
    // --dot-beat-dur is silently invalidated by a token retune.
    const period = beat!.effect?.getComputedTiming().duration;
    expect(typeof period, "the beat declares a resolved duration").toBe("number");
    const midpoint = (period as number) / 2;

    beat!.currentTime = 0; // rest -> the element's own opacity
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 2);

    beat!.currentTime = midpoint; // 50% -> the declared peak
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(0.45, 2);
  });

  it.each(["connected", "error"])("carries no beat at all when %s", (state) => {
    // BOTH settled states, because the two are separate rules and only one of them being wrong is
    // the live shape of this bug.
    const dot = mountDot(`status-dot ${state}`);
    expect(dot.getAnimations(), `a ${state} mark must not beat`).toEqual([]);
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 3);
  });
});

// ONE MARK RULE AT EVERY TIER, which is what the deleted phone block bought.
describe("the mark's size is one declaration", () => {
  it.each(["fine", "coarse"])("renders --dot-size on both axes at the %s tier", (tier) => {
    // `display: block` is DECLARED now.
    document.documentElement.dataset["pointer"] = tier;
    try {
      const dot = mountDot("status-dot connected");
      const box = dot.getBoundingClientRect();
      const probe = document.createElement("div");
      probe.style.setProperty("inline-size", "var(--dot-size)");
      document.body.appendChild(probe);
      const token = probe.getBoundingClientRect().width;
      probe.remove();
      expect(token, "--dot-size resolves").toBeGreaterThan(0);
      expect(box.width, `the mark is ${box.width}px wide at the ${tier} tier`).toBeCloseTo(
        token,
        1,
      );
      expect(box.height).toBeCloseTo(token, 1);
    } finally {
      delete document.documentElement.dataset["pointer"];
    }
  });

  it("leaves no status-dot selector inside the width <= 48rem block", () => {
    // A SOURCE assertion about what that block CONTAINS, not an attempt to render it.
    const body = atRuleBody(loadCSS("10-shell-app.css"), "width <= 48rem");
    expect(body).toContain(".sidebar-footer");
    expect(body, "the phone block names no mark selector").not.toContain("status-dot");
  });
});
