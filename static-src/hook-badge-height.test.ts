// The hooks badge is a button the size of its glyph's one-line box, whatever height its group's header grows to: a
// header that wraps to several lines must not stretch the badge's hover fill and click area down beside it.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const { mountHookGroup, unmountHookGroups } = await import("./__test-helpers__/hook-group.js");

let style: HTMLStyleElement | undefined;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});
afterAll(() => {
  style?.remove();
});
afterEach(() => {
  unmountHookGroups();
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

/** A group with the badge in a 360px card, its header summarising `commands`. */
function group(...commands: string[]): { header: HTMLElement; badge: HTMLElement } {
  return mountHookGroup({ commands, hooks: ["guard"], width: "360px" });
}

const LONG = "Check what deadset-spec dev.40 to dev.41 brings and what the analyzers must change";

/** A turn footer action button in its footer row, the badge's styling reference. */
function footerButton(): HTMLElement {
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const b = document.createElement("button");
  b.type = "button";
  b.className = "turn-action-btn";
  b.innerHTML = '<svg viewBox="0 0 24 24"></svg>';
  footer.append(b);
  document.body.append(footer);
  hosts.push(footer);
  return b;
}

describe("the hooks badge", () => {
  it("keeps its one-line size and place beside a header that wraps", () => {
    const one = group("cmd a", "cmd b");
    const many = group(LONG, LONG, LONG);
    const h1 = one.badge.getBoundingClientRect();
    const h4 = many.badge.getBoundingClientRect();
    expect(many.header.getBoundingClientRect().height).toBeGreaterThan(h1.height * 2);
    expect(h4.height).toBe(h1.height);
    expect(h4.top - many.header.getBoundingClientRect().top).toBe(
      h1.top - one.header.getBoundingClientRect().top,
    );
  });

  it("paints as a turn footer button: same height, corner, fill and ink", () => {
    const { badge } = group("cmd a", "cmd b");
    const ref = getComputedStyle(footerButton());
    const got = getComputedStyle(badge);
    for (const prop of [
      "height",
      "border-top-left-radius",
      "background-color",
      "color",
      "transition-property",
    ]) {
      expect(got.getPropertyValue(prop), prop).toBe(ref.getPropertyValue(prop));
    }
  });
});
