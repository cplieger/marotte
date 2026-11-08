// On a touch device the hooks badge's 44px target still lies over its header's corner, the group's or a lone tool
// card's: a tap anywhere else on the header toggles the group or the card, and a tap on the badge opens its list and
// leaves them as they were.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const { headerPointsOutsideBadge, mountHookGroup, unmountHookGroups } =
  await import("./__test-helpers__/hook-group.js");
const { dispatchTouch, frames } = await import("./__test-helpers__/turn-rail-dom.js");

let style: HTMLStyleElement | undefined;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  await page.viewport(375, 700);
});
afterAll(async () => {
  style?.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});
afterEach(() => {
  unmountHookGroups();
});

async function tap(at: { readonly x: number; readonly y: number }): Promise<void> {
  await dispatchTouch("touchStart", at);
  await dispatchTouch("touchEnd");
  await frames();
}

const STATES = [
  { name: "expanded", commands: ["cmd a", "cmd b"], hooks: ["guard", "seat guard"] },
  { name: "collapsed", commands: ["cmd a", "cmd b"], hooks: ["guard", "seat guard"], folded: true },
  { name: "one command", commands: ["cmd a"], hooks: ["guard", "seat guard"], output: "ok" },
] as const;

/** The header's fold: the group's, or a lone card's details. */
function expanded(header: HTMLElement): string | null {
  return (header.querySelector(".tool-disclosure") ?? header).getAttribute("aria-expanded");
}

describe("a tap on a header carrying the hooks badge", () => {
  for (const s of STATES) {
    it(`toggles the group anywhere outside the badge (${s.name})`, async () => {
      const { header, badge } = mountHookGroup(s);
      const h = header.getBoundingClientRect();
      const points = headerPointsOutsideBadge(header, badge);
      expect(points.some((p) => p.x > h.right - 2 * badge.getBoundingClientRect().width)).toBe(
        true,
      );
      for (const p of points) {
        const before = expanded(header);
        await tap(p);
        expect(expanded(header), JSON.stringify(p)).toBe(before === "true" ? "false" : "true");
      }
      expect(document.querySelector(".tool-hook-popover.is-open")).toBeNull();
    });

    it(`opens the list from the badge and leaves the group as it was (${s.name})`, async () => {
      const { header, badge } = mountHookGroup(s);
      const before = expanded(header);
      const b = badge.getBoundingClientRect();
      await tap({ x: b.left + b.width / 2, y: b.top + b.height / 2 });
      expect(document.querySelector(".tool-hook-popover.is-open")).not.toBeNull();
      expect(expanded(header)).toBe(before);
    });
  }
});
