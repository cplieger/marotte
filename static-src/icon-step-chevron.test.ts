// The turn map's step buttons and the resume control docked at the map's foot draw one chevron: same width, same
// depth, same tier, so the column's navigators read as one set.
import { describe, it, expect } from "vitest";

import indexHtml from "../static/index.html?raw";
import { ICON_STEP_NEXT, ICON_STEP_PREV } from "./icons.js";

interface Chevron {
  /** The arms' horizontal span and vertical depth, in viewBox units. */
  span: number;
  depth: number;
  /** +1 points down, -1 up. */
  dir: number;
}

/** Every `M x y l a b c d` chevron in a path, as span, depth and direction. */
function chevrons(d: string): Chevron[] {
  const out: Chevron[] = [];
  for (const m of d.matchAll(/M[\d.]+ [\d.]+l(-?[\d.]+) ?(-?[\d.]+) ?(-?[\d.]+) ?(-?[\d.]+)/gu)) {
    const [dx1, dy1, dx2, dy2] = [m[1], m[2], m[3], m[4]].map(Number);
    out.push({
      span: (dx1 ?? 0) + (dx2 ?? 0),
      depth: Math.abs(dy1 ?? 0),
      dir: Math.sign(dy1 ?? 0),
    });
    expect(dy2, "the arms meet symmetrically").toBe(-(dy1 ?? 0));
  }
  return out;
}

function svgOf(markup: string): SVGSVGElement {
  const svg = new DOMParser().parseFromString(markup, "text/html").querySelector("svg");
  if (svg === null) {
    throw new Error("no svg");
  }
  return svg;
}

function pathOf(svg: SVGSVGElement): string {
  return svg.querySelector("path")?.getAttribute("d") ?? "";
}

function resumeGlyphs(): SVGSVGElement[] {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  return ["scroll-bottom", "run-scroll-bottom"].map((id) => {
    const svg = doc.getElementById(id)?.querySelector("svg");
    if (svg === null || svg === undefined) {
      throw new Error(`#${id} has no glyph`);
    }
    return svg as SVGSVGElement;
  });
}

describe("the turn map's step chevrons", () => {
  it("draw the resume control's chevron: one of its pair, at its tier", () => {
    for (const resume of resumeGlyphs()) {
      const pair = chevrons(pathOf(resume));
      expect(pair).toHaveLength(2);
      for (const [name, markup, dir] of [
        ["next", ICON_STEP_NEXT, 1],
        ["prev", ICON_STEP_PREV, -1],
      ] as const) {
        const step = svgOf(markup);
        expect(step.getAttribute("class"), name).toBe(resume.getAttribute("class"));
        expect(step.getAttribute("viewBox"), name).toBe(resume.getAttribute("viewBox"));
        const [one] = chevrons(pathOf(step));
        expect(one, name).toEqual({ span: pair[0]?.span, depth: pair[0]?.depth, dir });
      }
    }
  });
});
