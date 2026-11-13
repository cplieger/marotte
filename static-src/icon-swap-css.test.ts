// The icon-swap wrapper clips its glyphs' slide at the button's own box, so the corner it
// inherits from the button is the button's corner at every pointer tier.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

/** The theme button in the shape `index.html` ships it. */
function mount(tier: "fine" | "coarse"): { button: HTMLElement; swap: HTMLElement } {
  document.documentElement.dataset["pointer"] = tier;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "icon-btn";
  const swap = document.createElement("span");
  swap.className = "icon-swap";
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.classList.add("ic-ui");
  swap.append(glyph);
  button.append(swap);
  host.replaceChildren(button);
  return { button, swap };
}

describe("an icon-swap button", () => {
  it.each(["fine", "coarse"] as const)("on a %s pointer clips at the button's box", (tier) => {
    const { button, swap } = mount(tier);
    const b = button.getBoundingClientRect();
    const s = swap.getBoundingClientRect();
    expect(b.height, "the button is at least the hit floor").toBeGreaterThanOrEqual(
      tier === "coarse" ? 44 : 24,
    );
    expect({ x: s.x, y: s.y, w: s.width, h: s.height }).toEqual({
      x: b.x,
      y: b.y,
      w: b.width,
      h: b.height,
    });
  });
});
