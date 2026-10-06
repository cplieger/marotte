// The model glyph's geometry, derived from the shipped string. Pinned: the eye gap survives quantisation at the
// smallest render size, the head owns the whole square, and the face fits inside it with the mouth's dip counted.
// Node environment: the icon is text.

import { describe, it, expect } from "vitest";
import { ICON_MODEL, ICON_MODEL_UI } from "./icons.js";

/**
 * Every render size, in CSS pixels: the model pill (inline tier) and the picker heading (ui tier, 16 on a fine
 * pointer). The floors use the smallest member.
 */
const RENDER_SIZES = [12, 16];

interface Circle {
  cx: number;
  cy: number;
  r: number;
}

function attr(tag: string, name: string): number {
  const m = new RegExp(`${name}="([-\\d.]+)"`).exec(tag);
  expect(m, `<${tag.slice(0, 24)}…> has no ${name}`).not.toBeNull();
  return Number(m![1]);
}

function circles(svg: string): Circle[] {
  return [...svg.matchAll(/<circle [^>]*\/>/g)].map((m) => ({
    cx: attr(m[0], "cx"),
    cy: attr(m[0], "cy"),
    r: attr(m[0], "r"),
  }));
}

/** Square and at 0, so one number describes it. */
function viewBoxSide(svg: string): number {
  const m = /viewBox="0 0 (\d+) (\d+)"/.exec(svg);
  expect(m, "expected a square viewBox anchored at 0 0").not.toBeNull();
  expect(m![1], "viewBox must be square").toBe(m![2]);
  return Number(m![1]);
}

/** Found by shared radius and y, not index, so reordering cannot mislead and a third circle fails loudly. */
function eyes(svg: string): [Circle, Circle] {
  const groups = new Map<string, Circle[]>();
  for (const c of circles(svg)) {
    const key = `${String(c.cy)}:${String(c.r)}`;
    groups.set(key, [...(groups.get(key) ?? []), c]);
  }
  const pair = [...groups.values()].filter((g) => g.length === 2);
  expect(pair.length, "expected exactly one pair of circles sharing cy and r").toBe(1);
  const [a, b] = [...pair[0]!].sort((p, q) => p.cx - q.cx);
  return [a!, b!];
}

/** Declared once on the root, so every stroked edge spends half of it outward. */
/**
 * The stroke the glyph was drawn against, in viewBox units; 03-base.css owns the rendered stroke
 * (`non-scaling-stroke`), so it is declared here.
 */
const DRAWN_STROKE_UNITS = 2;

function strokeWidth(): number {
  return DRAWN_STROKE_UNITS;
}

function headRect(svg: string): { x: number; y: number; width: number; height: number } {
  const tag = /<rect [^>]*\/>/.exec(svg);
  expect(tag, "expected the head rect").not.toBeNull();
  return {
    x: attr(tag![0], "x"),
    y: attr(tag![0], "y"),
    width: attr(tag![0], "width"),
    height: attr(tag![0], "height"),
  };
}

/** The trailing ` 0` means the curve ends level with its start, so only the dip reaches past the geometry. */
function mouth(svg: string): { x0: number; y0: number; dx: number; dip: number } {
  const m = /<path d="M([\d.]+) ([\d.]+)q([\d.]+) ([\d.]+) ([\d.]+) 0"\/>/.exec(svg);
  expect(m, "expected the mouth as one quadratic ending level with its start").not.toBeNull();
  return {
    x0: Number(m![1]),
    y0: Number(m![2]),
    dx: Number(m![5]),
    dip: Number(m![4]) / 2,
  };
}

describe("the model glyph's eye gap", () => {
  it("clears a device pixel at the smallest size it is rendered at", () => {
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const [left, right] = eyes(svg);

    // Filled circles: the painted edge is the radius. A stroked ring spends its width both ways.
    expect(
      svg,
      "the eyes must be filled, not stroked: a ring at 12px has to hold a wall and a hole inside 1.5 CSS px and loses both",
    ).toContain(
      `<circle cx="${String(left.cx)}" cy="${String(left.cy)}" r="${String(left.r)}" fill="currentColor" stroke="none"/>`,
    );

    const gapUnits = right.cx - right.r - (left.cx + left.r);
    const smallest = Math.min(...RENDER_SIZES);
    const gapPx = (gapUnits * smallest) / side;

    expect(
      gapPx,
      `the eyes leave ${String(gapUnits)} user units between them, which is ${gapPx.toFixed(2)} CSS px ` +
        `at ${String(smallest)}px. Under 1 there is no whole device pixel to hold the gap at DPR 1 and the ` +
        `two eyes bridge into one blob — the defect this geometry was redrawn to fix.`,
    ).toBeGreaterThanOrEqual(1);
  });

  it("puts the gap's edges on whole pixel boundaries at the smallest size", () => {
    // At 12px one unit is 0.5 CSS px, so even unit values are the pixel boundaries.
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const [left, right] = eyes(svg);
    const smallest = Math.min(...RENDER_SIZES);
    const edges = [left.cx + left.r, right.cx - right.r].map((u) => (u * smallest) / side);

    for (const px of edges) {
      expect(
        Number.isInteger(px),
        `gap edge lands at ${String(px)} CSS px at ${String(smallest)}px; a fractional edge antialiases ` +
          `into the gap, which is how the previous drawing's 1-unit wall became two greys`,
      ).toBe(true);
    }
  });
});

describe("the model glyph", () => {
  it("draws both sizes from one path, so they cannot drift", () => {
    const body = (svg: string): string => svg.replace(/^<svg[^>]*>/, "").replace("</svg>", "");
    expect(
      body(ICON_MODEL),
      "the 12px and 20px glyphs were two byte-identical literals in index.html before this; " +
        "if their bodies differ now the shared `d` has been forked again",
    ).toBe(body(ICON_MODEL_UI));

    // The two differ only by size tier; 03-base.css owns the pixels.
    expect(ICON_MODEL).toContain('class="ic-inline"');
    expect(ICON_MODEL_UI).toContain('class="ic-ui"');
    // Scoped to the opening tag: the head rect has its own width and height.
    for (const svg of [ICON_MODEL, ICON_MODEL_UI]) {
      const openTag = /^<svg[^>]*>/.exec(svg)![0];
      expect(openTag, "the size belongs to the tier, not to the tag").not.toMatch(
        /\s(?:width|height)=/,
      );
    }
  });

  it("gives the whole box to the head", () => {
    // The head's stroked rect is the content bbox; anything above it shows up as a margin over one unit.
    const svg = ICON_MODEL;
    const side = viewBoxSide(svg);
    const half = strokeWidth() / 2;
    const h = headRect(svg);
    const box = {
      top: h.y - half,
      bottom: h.y + h.height + half,
      left: h.x - half,
      right: h.x + h.width + half,
    };

    expect(box.right - box.left, "the head's painted box must be square").toBe(
      box.bottom - box.top,
    );
    expect(box.left + box.right, "the box must be centred horizontally in the viewBox").toBe(side);
    expect(box.top + box.bottom, "the box must be centred vertically too").toBe(side);
    expect(
      box.left,
      "the head must reach the 1-unit margin on every side; a bigger margin means something " +
        "else is claiming space in the viewBox, which is what the antenna did",
    ).toBeLessThanOrEqual(1);
  });

  it("keeps the face inside the head, mouth dip included", () => {
    // A quadratic's dip is half its control offset; it must stay off the head's inner edge with its stroke.
    const svg = ICON_MODEL;
    const half = strokeWidth() / 2;
    const h = headRect(svg);
    const m = mouth(svg);
    const [left, right] = eyes(svg);

    const inner = {
      top: h.y + half,
      bottom: h.y + h.height - half,
      left: h.x + half,
      right: h.x + h.width - half,
    };
    const face = {
      top: Math.min(left.cy - left.r, right.cy - right.r),
      bottom: m.y0 + m.dip + half,
      left: Math.min(left.cx - left.r, m.x0 - half),
      right: Math.max(right.cx + right.r, m.x0 + m.dx + half),
    };

    expect(m.dip, "a dip under 2 units renders as a straight dash at 12px").toBeGreaterThanOrEqual(
      2,
    );
    expect(
      face.bottom,
      `the mouth's lowest painted edge must stay above the head's inner bottom (${String(inner.bottom)})`,
    ).toBeLessThanOrEqual(inner.bottom);
    expect(face.top, "the eyes must stay below the head's inner top").toBeGreaterThanOrEqual(
      inner.top,
    );
    expect(face.left, "the face must stay inside the head's left wall").toBeGreaterThanOrEqual(
      inner.left,
    );
    expect(face.right, "the face must stay inside the head's right wall").toBeLessThanOrEqual(
      inner.right,
    );

    // Centred within a unit: half a unit is 0.25 CSS px at 12px, invisible in review and cumulative.
    expect(
      Math.abs((face.top + face.bottom) / 2 - (inner.top + inner.bottom) / 2),
      "the face must sit within a unit of the head interior's vertical centre",
    ).toBeLessThanOrEqual(1);
  });
});
