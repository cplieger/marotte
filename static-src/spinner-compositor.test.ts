import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// A ring that spins for as long as work runs animates only properties the compositor runs, so a
// spinning ring costs the page's main thread no style, paint or commit per frame.

/** Properties a browser animates off the main thread. */
const COMPOSITED = new Set(["transform", "rotate", "scale", "translate", "opacity"]);

/** The properties a `@keyframes` rule animates, read from the CSSOM: `getKeyframes()` drops
 *  custom properties, which is the case this test exists to see. */
function keyframeProperties(name: string): string[] {
  const props = new Set<string>();
  let found = false;
  for (const s of document.styleSheets) {
    for (const r of s.cssRules) {
      if (r instanceof CSSKeyframesRule && r.name === name) {
        found = true;
        for (const f of r.cssRules) {
          const style = (f as CSSKeyframeRule).style;
          for (let i = 0; i < style.length; i++) {
            props.add(style.item(i));
          }
        }
      }
    }
  }
  expect(found, `@keyframes ${name} is defined`).toBe(true);
  return [...props];
}

let sheet: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  host.replaceChildren();
});

afterAll(() => {
  sheet.remove();
  host.remove();
});

function frame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** Each ring a surface keeps spinning while its work runs, mounted as its module writes it. */
const RINGS: readonly (readonly [string, string])[] = [
  [
    "the exec tree's running step",
    `<div class="ev-page"><div class="ev-row" data-state="running" data-kind="step"><div class="ev-row-main"><span class="ev-state"></span></div></div></div>`,
  ],
  [
    "the run dock's running row",
    `<div class="ev-dock"><div class="ev-dock-row" data-state="running"><span class="ev-state"></span></div></div>`,
  ],
  [
    "the run card's running step",
    `<div class="run-card"><div class="run-body"><div class="run-steps"><div class="run-step" data-status="running"><div class="run-step-head"><span class="run-step-glyph"></span></div></div></div></div></div>`,
  ],
  ["a running tool card", `<span class="tool-spinner"></span>`],
  ["a running subagent", `<span class="tool-icon subagent-icon subagent-spinner"></span>`],
  [
    "a compacting steer",
    `<span class="steer-state-icon"><span class="steer-spinner"></span></span>`,
  ],
  ["the page spinner", `<div class="spinner"></div>`],
  ["the small spinner", `<div class="spinner-sm"></div>`],
];

describe("a spinning ring", () => {
  it.each(RINGS)("in %s animates only compositor properties", async (_name, markup) => {
    host.innerHTML = markup;
    // The run card is `content-visibility: auto`: its rings start once a frame has found it on screen.
    await frame();
    await frame();
    const spins = host
      .getAnimations({ subtree: true })
      .filter(
        (a): a is CSSAnimation =>
          a instanceof CSSAnimation &&
          a.animationName.startsWith("vk-spin") &&
          a.effect?.getTiming().iterations === Infinity,
      );
    expect(spins.length, "the ring spins").toBeGreaterThan(0);
    for (const spin of spins) {
      const animated = keyframeProperties(spin.animationName).filter((k) => !COMPOSITED.has(k));
      expect(animated, `${spin.animationName} animates only compositor properties`).toEqual([]);
    }
  });
});
