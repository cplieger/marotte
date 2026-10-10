import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { ROW_MAX, WholeRows, countLines } from "./viewer-rows.js";

/** Row i's text, its break excluded. */
function rowText(rows: WholeRows, i: number): string {
  return rows.text.slice(rows.starts[i] ?? 0, rows.ends[i] ?? 0);
}

describe("WholeRows", () => {
  it("is one row per line, an empty last line included", () => {
    const rows = new WholeRows("a\nbb\n");
    expect([rows.total, rows.totalLines]).toEqual([3, 3]);
    expect([0, 1, 2].map((i) => rowText(rows, i))).toEqual(["a", "bb", ""]);
    expect(Array.from(rows.lines)).toEqual([1, 2, 3]);
  });

  it("cuts a long line into continuation rows without splitting a surrogate pair", () => {
    const line = "x".repeat(ROW_MAX - 1) + "😀" + "y".repeat(10);
    const rows = new WholeRows(`${line}\nz`);
    expect(rows.total).toBe(3);
    expect(rowText(rows, 0)).toBe("x".repeat(ROW_MAX - 1));
    expect(rowText(rows, 1)).toBe("😀" + "y".repeat(10));
    expect([rows.continues(1), rows.continues(2)]).toEqual([true, false]);
    expect(Array.from(rows.lines)).toEqual([1, 1, 2]);
  });

  it("partitions the text exactly: rows plus breaks rebuild it, and no row holds a break", () => {
    fc.assert(
      fc.property(
        fc.string({ unit: fc.constantFrom("a", "\n", "😀", "\t"), maxLength: 9000 }),
        (text) => {
          const rows = new WholeRows(text);
          let rebuilt = "";
          for (let i = 0; i < rows.total; i++) {
            const piece = rowText(rows, i);
            expect(piece.includes("\n")).toBe(false);
            expect(piece.length).toBeLessThanOrEqual(ROW_MAX);
            rebuilt += (i > 0 && !rows.continues(i) ? "\n" : "") + piece;
          }
          expect(rebuilt).toBe(text);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("finds a line's first row and clamps a line past the end", () => {
    const rows = new WholeRows(`${"x".repeat(ROW_MAX * 2)}\nsecond\nthird`);
    expect(rows.lineToRow(2)).toEqual({ row: 2, line: 2, clamped: false });
    expect(rows.lineToRow(99)).toEqual({ row: 3, line: 3, clamped: true });
    expect(rows.lineToRow(0)).toEqual({ row: 0, line: 1, clamped: true });
  });

  it("maps a text offset to its row, the offset at a cut belonging to the later row", () => {
    const rows = new WholeRows(`${"x".repeat(ROW_MAX + 5)}\nend`);
    expect(rows.rowOfOffset(0)).toBe(0);
    expect(rows.rowOfOffset(ROW_MAX)).toBe(1);
    expect(rows.rowOfOffset(ROW_MAX + 6)).toBe(2);
  });

  it("measures the widest row with tabs at their stops", () => {
    expect(new WholeRows("a\tb\nlonger line").maxColumns).toBe(11);
    expect(new WholeRows("\t\tx").maxColumns).toBe(9);
  });
});

describe("countLines", () => {
  it("counts one more line than there are breaks, across dense and sparse runs", () => {
    fc.assert(
      fc.property(
        fc.array(
          fc.oneof(fc.constant("\n"), fc.string({ unit: fc.constant("a"), maxLength: 40 })),
          { maxLength: 200 },
        ),
        (parts) => {
          const text = parts.join("");
          expect(countLines(text)).toBe(text.split("\n").length);
        },
      ),
    );
  });
});
