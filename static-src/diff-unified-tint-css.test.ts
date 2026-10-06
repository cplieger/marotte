// A unified row's tint must span the column's scroll range: `.diff-row` is a block flex container sized to the
// scrollport while `pre` text overflows it. Measured on real layout.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { renderDiffPane } from "./diff-pane.js";
import type { DiffLine } from "./diff.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const HOST_WIDTH = 360;

let sheet: HTMLStyleElement;
let host: HTMLDivElement | undefined;

beforeAll(() => {
  sheet = mountAppCSS();
});

afterAll(() => {
  sheet.remove();
});

// Browser Mode isolates per file, not per test, so the page is cleared here.
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

/** The real builder, inside a definite width as the transcript gives it. */
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

function distinctWidths(col: HTMLElement): number[] {
  return [...new Set(rowsOf(col).map((r) => Math.round(r.getBoundingClientRect().width)))];
}

describe("the unified diff column's rows span its scroll range", () => {
  it("gives every row the column's full scrollWidth when a line overflows", () => {
    const col = mount([ctx(12, "Short context line."), del(13, LONG), add(13, LONG), add(14, MID)]);

    expect(col.scrollWidth, "the long rows give the column a range to scroll").toBeGreaterThan(
      col.clientWidth,
    );
    // One width, the whole range: per-row `max-content` would leave short rows behind.
    expect(distinctWidths(col)).toEqual([col.scrollWidth]);
  });

  it("keeps every row's right edge on the scrollport's at the far end of the scroll", () => {
    const col = mount([ctx(12, "Short context line."), del(13, LONG), add(13, LONG), add(14, MID)]);
    col.scrollLeft = col.scrollWidth;

    expect(col.scrollLeft, "the column really scrolled").toBeGreaterThan(0);
    const right = col.getBoundingClientRect().right;
    for (const row of rowsOf(col)) {
      // Sub-pixel: both boxes are fractional.
      expect(Math.abs(row.getBoundingClientRect().right - right)).toBeLessThan(1.5);
    }
  });

  it("still fills the column when every line fits, and adds no overflow", () => {
    const col = mount([ctx(12, "short"), del(13, "was here"), add(13, "is here now")]);

    expect(col.scrollWidth, "nothing to scroll").toBe(col.clientWidth);
    expect(distinctWidths(col)).toEqual([col.clientWidth]);
  });
});
