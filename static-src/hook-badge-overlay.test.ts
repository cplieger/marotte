// The hooks badge lies over its header's top-right corner, the group's or a lone tool card's: the header's toggle
// surface, and so its hover, press and focus highlight, spans the whole card, and the badge is a second control on
// top of it. The list the badge
// opens is an anchored menu with the tab menu's surface, right-aligned under the badge and inside the turn card.

import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";
import { page, userEvent } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const { headerPointsOutsideBadge, mountHookGroup, rendered, unmountHookGroups } =
  await import("./__test-helpers__/hook-group.js");

let style: HTMLStyleElement | undefined;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
});
afterAll(async () => {
  style?.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});
afterEach(async () => {
  unmountHookGroups();
  delete document.documentElement.dataset["theme"];
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

const LONG =
  "Check what deadset-spec dev.40 to dev.41 brings and what the analyzers must change for it";

/** Each state the header can carry the badge in. */
const STATES = [
  { name: "expanded", commands: ["cmd a", "cmd b"], hooks: ["guard"] },
  { name: "collapsed", commands: ["cmd a", "cmd b"], hooks: ["guard"], folded: true },
  { name: "wrapped", commands: [LONG, LONG, LONG], hooks: ["guard", "seat guard"], folded: true },
  {
    name: "failed",
    commands: ["cmd a", "cmd b"],
    hooks: ["notify"],
    outcome: "failed",
    trigger: "Stop",
  },
  {
    name: "refused",
    commands: ["cmd a", "cmd b"],
    hooks: ["notify"],
    outcome: "declined",
    trigger: "Stop",
  },
  { name: "one command", commands: ["cmd a"], hooks: ["guard", "seat guard"], output: "ok" },
] as const;

/** The header's fold: the group's, or a lone card's details. */
function expanded(header: HTMLElement): string | null {
  return (header.querySelector(".tool-disclosure") ?? header).getAttribute("aria-expanded");
}

const HEADER = ".tool-group-header, .tool-summary";

describe("the hooks badge over its header", () => {
  for (const s of STATES) {
    it(`leaves the header's surface the full card width (${s.name})`, async () => {
      const { group, header, badge } = mountHookGroup(s);
      await rendered();
      const g = group.getBoundingClientRect();
      const h = header.getBoundingClientRect();
      const b = badge.getBoundingClientRect();
      // The header is the group's whole inner width, so its hover and press fill reaches both card edges.
      expect(h.left - g.left).toBe(group.clientLeft);
      expect(h.width).toBe(group.clientWidth);
      // The badge sits inside the header's box, at its top right.
      expect(b.top).toBeGreaterThanOrEqual(h.top);
      expect(b.right).toBeLessThanOrEqual(h.right);
      for (const p of headerPointsOutsideBadge(header, badge)) {
        expect(document.elementFromPoint(p.x, p.y)?.closest(HEADER), JSON.stringify(p)).toBe(
          header,
        );
      }
      // The summary stops short of the badge.
      const count = header
        .querySelector<HTMLElement>(".tool-group-count, .tool-title")
        ?.getBoundingClientRect();
      expect(count?.right ?? Infinity).toBeLessThanOrEqual(b.left);
    });
  }

  for (const s of STATES) {
    it(`opens the hooks list from a click on the badge and leaves the group as it was (${s.name})`, async () => {
      const { group, header, badge } = mountHookGroup(s);
      await rendered();
      const b = badge.getBoundingClientRect();
      const h = header.getBoundingClientRect();
      // On the header's surface, and the topmost control there.
      expect(b.right).toBeLessThanOrEqual(h.right);
      expect(b.top).toBeGreaterThanOrEqual(h.top);
      expect(
        document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2)?.closest("button"),
      ).toBe(badge);
      const before = expanded(header);
      const inert = group.querySelector<HTMLElement>(":scope > .tool-group-body")?.inert;
      await userEvent.click(badge);
      expect(document.querySelector(".tool-hook-popover.is-open")).not.toBeNull();
      expect(badge.getAttribute("aria-expanded")).toBe("true");
      expect(expanded(header)).toBe(before);
      expect(group.querySelector<HTMLElement>(":scope > .tool-group-body")?.inert).toBe(inert);
      await userEvent.keyboard("{Escape}");
    });

    it(`toggles the group from a click anywhere on the header outside the badge (${s.name})`, async () => {
      const { header, badge } = mountHookGroup(s);
      await rendered();
      for (const p of headerPointsOutsideBadge(header, badge)) {
        const before = expanded(header);
        const hit = document.elementFromPoint(p.x, p.y);
        expect(hit, JSON.stringify(p)).not.toBeNull();
        (hit as HTMLElement).click();
        expect(expanded(header), JSON.stringify(p)).toBe(before === "true" ? "false" : "true");
      }
      expect(document.querySelector(".tool-hook-popover.is-open")).toBeNull();
    });
  }

  it("puts the header before the badge in the tab order, as two stops", async () => {
    const { header, badge } = mountHookGroup(STATES[0]);
    header.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(badge);
    await userEvent.tab({ shift: true });
    expect(document.activeElement).toBe(header);
  });
});

/** A tab context menu with one item, the anchored menu the hooks list matches. */
function referenceMenu(): { menu: HTMLElement; item: HTMLElement } {
  const menu = document.createElement("div");
  menu.className = "tab-context-menu";
  const item = document.createElement("button");
  item.type = "button";
  item.className = "tab-context-item";
  item.textContent = "Rename";
  menu.append(item);
  document.body.append(menu);
  return { menu, item };
}

async function openList(badge: HTMLElement): Promise<HTMLElement> {
  await userEvent.click(badge);
  const panel = document.querySelector<HTMLElement>(".tool-hook-popover.is-open");
  if (panel === null) {
    throw new Error("the hooks list did not open");
  }
  return panel;
}

const PHONE_HOOKS = ["animap seat guard", "Destructive command guard with a long name"];

describe("the hooks list", () => {
  for (const theme of ["dark", "light"] as const) {
    it(`takes the tab menu's surface and row (${theme})`, async () => {
      if (theme === "light") {
        document.documentElement.dataset["theme"] = "light";
      }
      const ref = referenceMenu();
      try {
        const { badge } = mountHookGroup({ commands: ["cmd a"], hooks: PHONE_HOOKS });
        const panel = await openList(badge);
        const got = getComputedStyle(panel);
        const want = getComputedStyle(ref.menu);
        for (const prop of [
          "background-color",
          "border-top-width",
          "border-top-style",
          "border-top-color",
          "border-top-left-radius",
          "box-shadow",
          "padding-top",
        ]) {
          expect(got.getPropertyValue(prop), prop).toBe(want.getPropertyValue(prop));
        }
        const row = panel.querySelector<HTMLElement>(".tool-hook-item");
        const name = row?.querySelector<HTMLElement>(".tool-title");
        expect(row).not.toBeNull();
        const r = getComputedStyle(row as HTMLElement);
        const ri = getComputedStyle(ref.item);
        for (const prop of [
          "min-height",
          "padding-top",
          "padding-bottom",
          "padding-left",
          "padding-right",
        ]) {
          expect(r.getPropertyValue(prop), `row ${prop}`).toBe(ri.getPropertyValue(prop));
        }
        expect(getComputedStyle(name as HTMLElement).color, "row ink").toBe(ri.color);
      } finally {
        ref.menu.remove();
      }
    });
  }

  it("opens right-aligned under the badge and inside the turn card on a phone", async () => {
    await page.viewport(375, 700);
    const { turn, badge } = mountHookGroup({ commands: ["cmd a"], hooks: PHONE_HOOKS });
    const panel = await openList(badge);
    const p = panel.getBoundingClientRect();
    const b = badge.getBoundingClientRect();
    const t = turn.getBoundingClientRect();
    expect(Math.abs(p.right - b.right), "right edge on the badge's").toBeLessThanOrEqual(1);
    expect(p.top, "below the badge").toBeGreaterThanOrEqual(b.bottom);
    expect(p.left, "inside the card's start edge").toBeGreaterThanOrEqual(t.left);
    expect(p.right, "inside the card's end edge").toBeLessThanOrEqual(t.right);
    // A name too long for that width ellipsizes, and the full name stays reachable.
    const name = [...panel.querySelectorAll<HTMLElement>(".tool-hook-item .tool-title")].find((n) =>
      n.textContent.includes("long name"),
    );
    expect(name).toBeDefined();
    expect((name as HTMLElement).scrollWidth).toBeGreaterThan((name as HTMLElement).clientWidth);
    expect(name?.dataset["tooltip"]).toBe(PHONE_HOOKS[1]);
  });

  it("opens above the badge, still right-aligned, with no room below", async () => {
    await page.viewport(375, 700);
    const { turn, badge } = mountHookGroup({ commands: ["cmd a"], hooks: PHONE_HOOKS });
    turn.style.marginBlockStart = `${String(window.innerHeight - 80)}px`;
    const panel = await openList(badge);
    const p = panel.getBoundingClientRect();
    const b = badge.getBoundingClientRect();
    const t = turn.getBoundingClientRect();
    expect(p.bottom, "above the badge").toBeLessThanOrEqual(b.top);
    expect(Math.abs(p.right - b.right), "right edge on the badge's").toBeLessThanOrEqual(1);
    expect(p.left).toBeGreaterThanOrEqual(t.left);
  });

  it("refits to a narrower card when the viewport shrinks while it is open", async () => {
    await page.viewport(700, 700);
    const { turn, badge } = mountHookGroup({ commands: ["cmd a"], hooks: PHONE_HOOKS });
    const panel = await openList(badge);
    await page.viewport(375, 700);
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          r();
        });
      });
    });
    const p = panel.getBoundingClientRect();
    const b = badge.getBoundingClientRect();
    expect(Math.abs(p.right - b.right)).toBeLessThanOrEqual(1);
    expect(p.left).toBeGreaterThanOrEqual(turn.getBoundingClientRect().left);
  });

  it("closes when its group leaves the page", async () => {
    const { turn, badge } = mountHookGroup({ commands: ["cmd a"], hooks: PHONE_HOOKS });
    const panel = await openList(badge);
    turn.remove();
    await vi.waitFor(() => {
      expect(panel.isConnected).toBe(false);
    });
  });
});
