// The press is an inset shadow painted on the control's own box (03-base.css), so a control with no
// radius presses as a square. These controls are bare icon hit targets whose resting state is
// transparent: the press is the only fill they ever show.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  host.remove();
  style.remove();
});

function radiusOf(tag: string, className: string): string {
  const node = document.createElement(tag);
  node.className = className;
  host.replaceChildren(node);
  return getComputedStyle(node).borderTopLeftRadius;
}

describe("a bare icon toggle presses rounded", () => {
  it("gives the turn header's fold chevron the radius of the action buttons at the row's other end", () => {
    const fold = radiusOf("button", "turn-fold-toggle");
    expect(fold).not.toBe("0px");
    expect(fold).toBe(radiusOf("button", "turn-action-btn"));
  });

  it("gives the tool card's disclosure chevron the same radius", () => {
    expect(radiusOf("button", "tool-disclosure")).toBe(radiusOf("button", "turn-fold-toggle"));
  });
});
