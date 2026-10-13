// The model picker's short last row is centred under a full row, at every width.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let appStyle: HTMLStyleElement;
const frames: HTMLIFrameElement[] = [];

/** The picker as `picker.ts` fills it: one `.picker-btn` per model inside `.picker-grid`, in the
 *  overlay `static/index.html` authors. */
function cardBoxes(
  width: number,
  models: number,
): { cards: { left: number; right: number; top: number; width: number }[]; grid: DOMRect } {
  const frame = document.createElement("iframe");
  frame.width = String(width);
  frame.height = "700";
  document.body.appendChild(frame);
  frames.push(frame);
  const d = frame.contentDocument;
  if (d === null) {
    throw new Error("iframe has no contentDocument");
  }
  const sheet = d.createElement("style");
  sheet.textContent = appStyle.textContent;
  d.head.appendChild(sheet);

  const picker = d.createElement("div");
  picker.id = "model-picker";
  const grid = d.createElement("div");
  grid.className = "picker-grid";
  for (let i = 0; i < models; i++) {
    const btn = d.createElement("button");
    btn.type = "button";
    btn.className = "picker-btn";
    btn.textContent = `Model ${i + 1}`;
    grid.appendChild(btn);
  }
  picker.appendChild(grid);
  d.body.replaceChildren(picker);
  return {
    cards: [...grid.children].map((c) => {
      const r = c.getBoundingClientRect();
      return { left: r.left, right: r.right, top: r.top, width: r.width };
    }),
    grid: grid.getBoundingClientRect(),
  };
}

beforeAll(() => {
  appStyle = mountAppCSS();
});

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
  appStyle.remove();
});

describe("the model picker grid", () => {
  it.each([
    ["a desktop", 1106, 5],
    ["a desktop", 1106, 4],
    ["a tablet", 820, 5],
    ["a phone", 390, 5],
    ["a phone", 390, 4],
  ] as const)("centres the short last row of %s (%ipx, %i models)", (_, width, models) => {
    const { cards, grid } = cardBoxes(width, models);
    const firstRowTop = cards[0]?.top;
    const rows = Map.groupBy(cards, (c) => Math.round(c.top));
    const last = [...rows.values()].at(-1) ?? [];
    expect(rows.size, "the grid wraps into rows").toBeGreaterThan(1);
    expect(last.length, "the last row is short").toBeLessThan(
      rows.get(Math.round(firstRowTop ?? 0))?.length ?? 0,
    );
    const centre =
      (Math.min(...last.map((c) => c.left)) + Math.max(...last.map((c) => c.right))) / 2;
    expect(centre, "the row's centre is the grid's centre").toBeCloseTo(
      grid.left + grid.width / 2,
      0,
    );
  });

  it("keeps the desktop grid within its measure and the phone grid three equal columns", () => {
    const desktop = cardBoxes(1106, 5).grid;
    expect(desktop.width, "35rem measure").toBeLessThanOrEqual(560);
    const phone = cardBoxes(390, 7);
    expect(phone.grid.width, "the whole overlay").toBeGreaterThan(380);
    const widths = new Set(phone.cards.map((c) => Math.round(c.width)));
    expect(widths.size, "every card is the same width").toBe(1);
    expect(
      Map.groupBy(phone.cards, (c) => Math.round(c.top)).get(Math.round(phone.cards[0]?.top ?? 0))
        ?.length,
      "three per row",
    ).toBe(3);
  });
});
