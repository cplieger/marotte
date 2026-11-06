// The hooks badge is a button the size of its glyph's one-line box, whatever height its group's header grows to: a
// header that wraps to several lines must not stretch the badge's hover fill and click area down beside it.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement | undefined;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});
afterAll(() => {
  style?.remove();
});
afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

/** A group with the badge, its header holding `lines` forced lines of summary text. */
function group(lines: number): { header: HTMLElement; badge: HTMLElement } {
  const host = document.createElement("div");
  host.style.inlineSize = "360px";
  const g = document.createElement("div");
  g.className = "tool-group tool-group-has-hooks";
  const header = document.createElement("div");
  header.className = "tool-group-header";
  header.append(
    ...Array.from({ length: lines }, (_, i) => {
      const line = document.createElement("div");
      line.textContent = `Ran a command ${String(i + 1)}`;
      return line;
    }),
  );
  const badge = document.createElement("button");
  badge.type = "button";
  badge.className = "tool-group-hooks";
  badge.innerHTML =
    '<span class="tool-group-hooks-icon"><svg viewBox="0 0 24 24"></svg></span><span class="tool-group-hooks-n">4</span>';
  const body = document.createElement("div");
  body.className = "tool-group-body";
  g.append(header, badge, body);
  host.append(g);
  document.body.append(host);
  hosts.push(host);
  return { header, badge };
}

/** A turn footer action button, the badge's styling reference. */
function footerButton(): HTMLElement {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "turn-action-btn";
  b.innerHTML = '<svg viewBox="0 0 24 24"></svg>';
  document.body.append(b);
  hosts.push(b);
  return b;
}

describe("the hooks badge", () => {
  it("keeps its one-line size and place beside a header that wraps", () => {
    const one = group(1);
    const many = group(4);
    const h1 = one.badge.getBoundingClientRect();
    const h4 = many.badge.getBoundingClientRect();
    expect(many.header.getBoundingClientRect().height).toBeGreaterThan(h1.height * 2);
    expect(h4.height).toBe(h1.height);
    expect(h4.top - many.header.getBoundingClientRect().top).toBe(
      h1.top - one.header.getBoundingClientRect().top,
    );
  });

  it("paints as a turn footer button: same height, corner, fill and ink", () => {
    const { badge } = group(1);
    const ref = getComputedStyle(footerButton());
    const got = getComputedStyle(badge);
    for (const prop of [
      "height",
      "border-top-left-radius",
      "background-color",
      "transition-property",
    ]) {
      expect(got.getPropertyValue(prop), prop).toBe(ref.getPropertyValue(prop));
    }
  });
});
