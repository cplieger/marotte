// The hourglass glyph's geometry, derived from the shipped string. A half-unit asymmetry is 0.28 CSS px at 13.6px,
// invisible in review and cumulative, so the invariants are the two mirror axes, the cap overhang and the margin.
// Absolute commands only: a relative form hides drift in its deltas.

import { describe, it, expect } from "vitest";
import { ICON_HOURGLASS } from "./icons.js";

/** The steer dock renders it at 0.85rem (26-dock.css), the smallest size it ships at, so the floors use that. */
const VIEWBOX = 24;
const SMALLEST_PX = 13.6;

/** 4.9 + 19.1 is 24.000000000000004 in float64; one part in a billion is far below anything visible. */
const EPS = 1e-9;

interface Point {
  readonly x: number;
  readonly y: number;
}

/** Accepts only absolute commands: a relative or arc command fails rather than being interpreted. */
function points(svg: string): Point[] {
  const d = /<path d="([^"]+)"/g;
  const out: Point[] = [];
  let bodies = 0;
  for (let m = d.exec(svg); m !== null; m = d.exec(svg)) {
    bodies += 1;
    let x = Number.NaN;
    let y = Number.NaN;
    const tokens = m[1]!.match(/[A-Za-z][-\d.\s]*/g) ?? [];
    for (const token of tokens) {
      const cmd = token[0]!;
      const nums = (token.slice(1).match(/-?[\d.]+/g) ?? []).map(Number);
      expect(
        cmd,
        `${cmd} is not an absolute M/H/V/L command; this glyph is authored in absolute ` +
          `coordinates so the mirror axes are readable without a path parser`,
      ).toMatch(/^[MHVL]$/);
      if (cmd === "M" || cmd === "L") {
        expect(nums.length, `${token} must carry an x and a y`).toBe(2);
        [x, y] = [nums[0]!, nums[1]!];
      } else if (cmd === "H") {
        x = nums[0]!;
      } else {
        y = nums[0]!;
      }
      out.push({ x, y });
    }
  }
  expect(bodies, "expected the two cap bars and the two chambers").toBeGreaterThanOrEqual(3);
  return out;
}

/** Sorted and walked from both ends, so a value repeated an odd number of times off the axis fails. */
function mirrored(values: readonly number[], axis: number): boolean {
  const sorted = [...values].sort((a, b) => a - b);
  for (let i = 0, j = sorted.length - 1; i <= j; i += 1, j -= 1) {
    if (Math.abs(sorted[i]! + sorted[j]! - 2 * axis) > EPS) {
      return false;
    }
  }
  return true;
}

describe("the hourglass glyph", () => {
  it("mirrors about both axes of the grid", () => {
    // The y check catches a square top over a rounded bottom; the x check guards the other direction.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;

    expect(
      mirrored(
        p.map((q) => q.y),
        centre,
      ),
      `the glyph is not mirrored about y=${String(centre)}: ` +
        `${[...new Set(p.map((q) => q.y))].sort((a, b) => a - b).join(", ")}. ` +
        `A top and bottom that differ is what made the previous drawing read as a figure-8.`,
    ).toBe(true);

    expect(
      mirrored(
        p.map((q) => q.x),
        centre,
      ),
      `the glyph is not mirrored about x=${String(centre)}: ` +
        `${[...new Set(p.map((q) => q.x))].sort((a, b) => a - b).join(", ")}`,
    ).toBe(true);
  });

  it("pinches to a single point on the centre line", () => {
    // Both chambers meet at the grid's centre, or it is a bowtie.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;
    const waist = p.filter((q) => Math.abs(q.y - centre) < EPS);

    expect(waist.length, "expected both chambers to reach the waist").toBe(2);
    for (const q of waist) {
      expect(q.x, "the waist must sit on the vertical centre line").toBeCloseTo(centre, 9);
    }
  });

  it("gives the caps an overhang wide enough to survive 13.6px", () => {
    // The caps are the widest thing, which separates an hourglass from an X; under half a CSS px they quantise away.
    const p = points(ICON_HOURGLASS);
    const centre = VIEWBOX / 2;
    const halfWidthAt = (y: number): number =>
      Math.max(...p.filter((q) => Math.abs(q.y - y) < EPS).map((q) => Math.abs(q.x - centre)));

    const ys = [...new Set(p.map((q) => q.y))].sort((a, b) => a - b);
    const capY = ys[0]!;
    const shoulderY = ys[1]!;
    const overhangUnits = halfWidthAt(capY) - halfWidthAt(shoulderY);
    const overhangPx = (overhangUnits * SMALLEST_PX) / VIEWBOX;

    expect(
      overhangPx,
      `the cap overhangs its chamber by ${String(overhangUnits)} units, which is ` +
        `${overhangPx.toFixed(2)} CSS px at ${String(SMALLEST_PX)}px. Under half a device pixel at ` +
        `DPR 1 the bar and the chamber paint the same width and the cap stops reading as a cap.`,
    ).toBeGreaterThanOrEqual(0.5);
  });

  it("keeps the artwork inside the grid's 2-unit margin", () => {
    // The icon set's margin, so optical size matches neighbours and the stroke is not clipped by the viewBox.
    const p = points(ICON_HOURGLASS);
    for (const q of p) {
      for (const [axis, v] of [
        ["x", q.x],
        ["y", q.y],
      ] as const) {
        expect(v, `${axis}=${String(v)} is outside the 2..22 margin`).toBeGreaterThanOrEqual(2);
        expect(v, `${axis}=${String(v)} is outside the 2..22 margin`).toBeLessThanOrEqual(
          VIEWBOX - 2,
        );
      }
    }
  });
});
