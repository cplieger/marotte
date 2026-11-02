// A code block's header buttons (Copy, Run, and a diagram's source, download and expand controls) are the turn
// footer's icon buttons: no border or fill at rest, the same painted box, and a row tall enough that the touch
// expander stays inside `.code-wrap`'s clip.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;

function glyph(): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  return svg;
}

function button(className: string): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = className;
  btn.setAttribute("aria-label", "Copy");
  btn.appendChild(glyph());
  return btn;
}

function mount(): { head: HTMLElement; code: HTMLButtonElement; turn: HTMLButtonElement } {
  const code = button("code-act-btn");
  const actions = document.createElement("div");
  actions.className = "code-actions";
  actions.appendChild(code);
  const lang = document.createElement("span");
  lang.className = "code-lang";
  lang.textContent = "mermaid";
  const head = document.createElement("div");
  head.className = "code-head";
  head.append(lang, actions);
  const wrap = document.createElement("div");
  wrap.className = "code-wrap";
  wrap.append(head, document.createElement("pre"));

  const turn = button("turn-action-btn");
  const slot = document.createElement("span");
  slot.className = "turn-actions-buttons";
  slot.appendChild(turn);
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  footer.appendChild(slot);

  host.replaceChildren(wrap, footer);
  return { head, code, turn };
}

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;width:774px;";
  document.body.appendChild(host);
});

afterEach(() => {
  document.documentElement.removeAttribute("data-pointer");
});

afterAll(() => {
  style.remove();
  host.remove();
});

describe.each(["fine", "coarse"])("a code block's header button on a %s pointer", (tier) => {
  it("paints no border or fill at rest", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { code } = mount();
    const cs = getComputedStyle(code);
    expect(cs.borderTopWidth).toBe("0px");
    expect(cs.backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("takes the turn footer's icon-button box and radius", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { code, turn } = mount();
    const a = code.getBoundingClientRect();
    const b = turn.getBoundingClientRect();
    expect(a.width).toBe(b.width);
    expect(a.height).toBe(b.height);
    expect(getComputedStyle(code).borderRadius).toBe(getComputedStyle(turn).borderRadius);
  });

  it("keeps its hit-floor expander inside the head's row", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { head, code } = mount();
    const after = getComputedStyle(code, "::after");
    const reach = -Number.parseFloat(after.top);
    const box = code.getBoundingClientRect();
    const row = head.getBoundingClientRect();
    expect(box.top - reach, "expander above the head").toBeGreaterThanOrEqual(row.top);
    expect(box.bottom + reach, "expander below the head").toBeLessThanOrEqual(row.bottom);
  });
});
