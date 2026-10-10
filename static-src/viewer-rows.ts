// The read state's display rows over a file's LF-normalized text: one row per line, a line over
// ROW_MAX UTF-16 units cut into continuation rows on a code-point boundary. Stored as typed
// arrays, so a 2 MiB file of only newlines is 2M offsets rather than 2M objects.

/** The longest row, in UTF-16 units. A longer line continues on the next row. */
export const ROW_MAX = 4096;

/** Where a 1-based line landed: its first row, and the line actually shown when it was past the
 *  end. */
export interface LineLocation {
  readonly row: number;
  readonly line: number;
  readonly clamped: boolean;
}

/** A gap between breaks, in UTF-16 units, under which `countLines` scans by code unit. */
const DENSE_GAP = 16;

/** One more than `text`'s LF count, allocating nothing. `indexOf` skips sparse breaks fast but
 *  pays a call per break, so a run of dense breaks is scanned by code unit instead. */
export function countLines(text: string): number {
  let n = 1;
  let at = 0;
  for (;;) {
    const lf = text.indexOf("\n", at);
    if (lf === -1) {
      return n;
    }
    n++;
    const dense = lf - at < DENSE_GAP;
    at = lf + 1;
    if (dense) {
      for (let gap = 0; at < text.length && gap < DENSE_GAP; at++) {
        if (text.charCodeAt(at) === 10) {
          n++;
          gap = 0;
        } else {
          gap++;
        }
      }
    }
  }
}

export class WholeRows {
  readonly text: string;
  /** Row i spans `[starts[i], ends[i])` of `text`, its break excluded. */
  readonly starts: Int32Array;
  readonly ends: Int32Array;
  /** The 1-based line row i belongs to. */
  readonly lines: Int32Array;
  readonly total: number;
  readonly totalLines: number;
  /** The widest row, in UTF-16 units with a tab counted as its tab stop. */
  readonly maxColumns: number;

  constructor(text: string) {
    this.text = text;
    const starts: number[] = [];
    const ends: number[] = [];
    const lines: number[] = [];
    let line = 1;
    let maxColumns = 0;
    let at = 0;
    for (;;) {
      const lf = text.indexOf("\n", at);
      const lineEnd = lf === -1 ? text.length : lf;
      let s = at;
      do {
        let e = Math.min(lineEnd, s + ROW_MAX);
        if (e < lineEnd && isLowSurrogate(text.charCodeAt(e))) {
          e--;
        }
        starts.push(s);
        ends.push(e);
        lines.push(line);
        maxColumns = Math.max(maxColumns, columns(text, s, e));
        s = e;
      } while (s < lineEnd);
      if (lf === -1) {
        break;
      }
      at = lf + 1;
      line++;
    }
    this.starts = Int32Array.from(starts);
    this.ends = Int32Array.from(ends);
    this.lines = Int32Array.from(lines);
    this.total = starts.length;
    this.totalLines = line;
    this.maxColumns = maxColumns;
  }

  /** Whether row i continues the line of row i - 1. */
  continues(i: number): boolean {
    return i > 0 && this.lines[i] === this.lines[i - 1];
  }

  /** The first row of `line`, clamped into the file. */
  lineToRow(line: number): LineLocation {
    const shown = Math.min(Math.max(1, Math.floor(line)), this.totalLines);
    return { row: this.firstRowAtOrAfter(this.lines, shown), line: shown, clamped: shown !== line };
  }

  /** The row `offset` rows into `line`, kept inside that line. */
  rowInLine(line: number, offset: number): number {
    const first = this.lineToRow(line);
    const end = first.line < this.totalLines ? this.lineToRow(first.line + 1).row : this.total;
    return Math.min(first.row + Math.max(0, offset), end - 1);
  }

  /** How many rows into its line row i sits. */
  offsetInLine(i: number): number {
    return i - this.lineToRow(this.lines[i] ?? 1).row;
  }

  /** The row holding text offset `off` (an offset at a row's end belongs to that row). */
  rowOfOffset(off: number): number {
    let lo = 0;
    let hi = this.total - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if ((this.starts[mid] ?? 0) <= off) {
        lo = mid;
      } else {
        hi = mid - 1;
      }
    }
    // A cut row's end equals the next row's start; the offset there belongs to the later row.
    return lo;
  }

  private firstRowAtOrAfter(arr: Int32Array, v: number): number {
    let lo = 0;
    let hi = this.total - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((arr[mid] ?? 0) < v) {
        lo = mid + 1;
      } else {
        hi = mid;
      }
    }
    return lo;
  }
}

function isLowSurrogate(c: number): boolean {
  return c >= 0xdc00 && c <= 0xdfff;
}

const TAB_STOP = 4;

function columns(text: string, from: number, to: number): number {
  let col = 0;
  for (let i = from; i < to; i++) {
    col = text.charCodeAt(i) === 9 ? col + TAB_STOP - (col % TAB_STOP) : col + 1;
  }
  return col;
}
