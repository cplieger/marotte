// A unified diff row's green/red background has to span the column's whole
// SCROLL RANGE, not its scrollport.
//
// Measured against real layout rather than read out of the CSS, because the
// defect is a box the source cannot show: `.diff-row` is a block-level flex
// container, so its used width is its containing block's — the scrollport — while
// `white-space: pre` text overflows past it, and the tint therefore stopped
// wherever the reader happened to have scrolled to. Measured on the shipped
// stylesheet at a 430px viewport before the fix: rows painted 342px against a
// 1149px range, so 70% of every changed line was untinted at the far end of the
// scroll. Nothing in the declarations looks wrong; only the boxes disagree.
//
// Both directions are asserted, and the second is what makes the first honest: a
// bare `max-content` track fixes the long case and leaves a diff that FITS with
// rows narrower than the column it sits in, which is the same defect with the
// sign flipped.
//
// Assertions are RELATIONSHIPS (row width against the column's scroll range, the
// right edges against each other), never pixel counts, because a max-content row
// is as wide as the font makes it.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { renderDiffPane } from "./diff-pane.js";
import type { DiffLine } from "./diff.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Comfortably narrower than the long rows below, so there is a range to scroll. */
const HOST_WIDTH = 360;

let sheet: HTMLStyleElement;
let host: HTMLDivElement | undefined;

beforeAll(() => {
  sheet = mountAppCSS();
});

afterAll(() => {
  sheet.remove();
});

// Browser Mode isolates per FILE, not per test, and nothing in this config clears
// the page: without this every later case measures the previous case's pane too.
afterEach(() => {
  host?.remove();
  host = undefined;
});

const LONG =
  "The rewritten paragraph explains the same mechanism at greater length, so the row is wider than the column it is rendered in.";
const MID = "A shorter added sentence that still overflows the column a little.";

function ctx(no: number, text: string): DiffLine {
  return { kind: "ctx", oldNo: no, newNo: no, text };
}
function add(no: number, text: string): DiffLine {
  return { kind: "add", oldNo: 0, newNo: no, text };
}
function del(no: number, text: string): DiffLine {
  return { kind: "del", oldNo: no, newNo: 0, text };
}

/** The real builder, in the shape `insertDiffPreview` asks for, inside a box of a
 *  definite width — which is what the transcript gives it. */
function mount(lines: DiffLine[]): HTMLDivElement {
  const box = document.createElement("div");
  box.style.cssText = `width: ${String(HOST_WIDTH)}px`;
  box.appendChild(renderDiffPane(lines, { unified: true, lineNumbers: true, syncScroll: false }));
  document.body.appendChild(box);
  host = box;
  const col = box.querySelector<HTMLElement>(".diff-col-unified");
  if (col === null) {
    throw new Error("the unified column is not mounted");
  }
  return col as HTMLDivElement;
}

function rowsOf(col: HTMLElement): HTMLElement[] {
  return [...col.querySelectorAll<HTMLElement>(".diff-row")];
}

/** The distinct rendered widths of a column's rows. One entry means every row
 *  agrees, which is the property a shared background depends on. */
function distinctWidths(col: HTMLElement): number[] {
  return [...new Set(rowsOf(col).map((r) => Math.round(r.getBoundingClientRect().width)))];
}

describe("the unified diff column's rows span its scroll range", () => {
  it("gives every row the column's full scrollWidth when a line overflows", () => {
    const col = mount([ctx(12, "Short context line."), del(13, LONG), add(13, LONG), add(14, MID)]);

    expect(col.scrollWidth, "the long rows give the column a range to scroll").toBeGreaterThan(
      col.clientWidth,
    );
    // ONE width, and it is the whole range: a per-row `max-content` would leave the
    // short rows behind (measured 619px against an 811px offset).
    expect(distinctWidths(col)).toEqual([col.scrollWidth]);
  });

  it("keeps every row's right edge on the scrollport's at the far end of the scroll", () => {
    const col = mount([ctx(12, "Short context line."), del(13, LONG), add(13, LONG), add(14, MID)]);
    col.scrollLeft = col.scrollWidth;

    expect(col.scrollLeft, "the column really scrolled").toBeGreaterThan(0);
    const right = col.getBoundingClientRect().right;
    for (const row of rowsOf(col)) {
      // Sub-pixel: the row's box and the scrollport's are both fractional.
      expect(Math.abs(row.getBoundingClientRect().right - right)).toBeLessThan(1.5);
    }
  });

  it("still fills the column when every line fits, and adds no overflow", () => {
    const col = mount([ctx(12, "short"), del(13, "was here"), add(13, "is here now")]);

    expect(col.scrollWidth, "nothing to scroll").toBe(col.clientWidth);
    expect(distinctWidths(col)).toEqual([col.clientWidth]);
  });
});
