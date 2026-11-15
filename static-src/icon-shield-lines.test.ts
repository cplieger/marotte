// The security profile shields count their lines, so each line must stay its own line at the size the card and
// the pill draw them. A one-pixel stroke centred on a pixel row paints that row solid; centred between two rows it
// smears into both at half ink and runs into its neighbour, which is how four lines read as three on a 1x screen.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { iconEl } from "./icon-el.js";
import {
  ICON_SHIELD_LINES_ONE,
  ICON_SHIELD_LINES_TWO,
  ICON_SHIELD_LINES_THREE,
  ICON_SHIELD_LINES_FOUR,
} from "./icons.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

const SHIELDS = [
  { lines: 1, markup: ICON_SHIELD_LINES_ONE },
  { lines: 2, markup: ICON_SHIELD_LINES_TWO },
  { lines: 3, markup: ICON_SHIELD_LINES_THREE },
  { lines: 4, markup: ICON_SHIELD_LINES_FOUR },
];

/** The horizontal strokes inside the outline: each `M x y h w` path's y, in viewBox units, top first. */
function lineRows(svg: SVGSVGElement): number[] {
  return [...svg.querySelectorAll("path")]
    .map((p) => /^M[\d.]+ ([\d.]+)h[\d.]+$/u.exec(p.getAttribute("d") ?? "")?.[1])
    .filter((y): y is string => y !== undefined)
    .map(Number)
    .sort((a, b) => a - b);
}

describe("the security profile shields at their drawn size", () => {
  for (const { lines, markup } of SHIELDS) {
    it(`paints each of the ${lines}-line shield's lines on its own pixel row`, () => {
      const svg = iconEl(markup) as SVGSVGElement;
      document.body.appendChild(svg);
      const px = svg.getBoundingClientRect().height / svg.viewBox.baseVal.height;
      const stroke = Number.parseFloat(getComputedStyle(svg).strokeWidth);
      const rows = lineRows(svg);
      expect(rows, "one stroke per line").toHaveLength(lines);
      for (const y of rows) {
        const centre = y * px - stroke / 2;
        expect(
          Math.abs(centre - Math.round(centre)),
          `the line at y=${y} sits on a pixel row`,
        ).toBeLessThan(0.01);
      }
      for (let i = 1; i < rows.length; i++) {
        const gap = (rows[i]! - rows[i - 1]!) * px - stroke;
        expect(
          gap,
          `a clear row between the lines at y=${rows[i - 1]} and y=${rows[i]}`,
        ).toBeGreaterThanOrEqual(stroke);
      }
    });
  }
});
