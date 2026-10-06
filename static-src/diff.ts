// Line-level LCS diff: a flat add/del/ctx array in reading order, so the renderer walks it once.

type DiffKind = "add" | "del" | "ctx";

export interface DiffLine {
  kind: DiffKind;
  /** 1-based line number in the old text (0 for pure adds). */
  oldNo: number;
  /** 1-based line number in the new text (0 for pure dels). */
  newNo: number;
  /** Raw line content without trailing newline. */
  text: string;
}

export interface DiffStats {
  adds: number;
  dels: number;
  ctx: number;
}

export function stats(lines: DiffLine[]): DiffStats {
  const s: DiffStats = { adds: 0, dels: 0, ctx: 0 };
  for (const l of lines) {
    if (l.kind === "add") {
      s.adds++;
    } else if (l.kind === "del") {
      s.dels++;
    } else {
      s.ctx++;
    }
  }
  return s;
}

/**
 * Split into lines: on "\n", stripping one trailing "\r" each, dropping the empty element a final newline produces
 * ("a\nb\n" and "a\nb" are two lines, "\n" is one empty line). One vocabulary for every diff consumer, so the pane and
 * the footer agree; twin of `splitDiffLines` in `internal/buffer/linediff.go`.
 */
function splitLines(s: string): string[] {
  if (s === "") {
    return [];
  }
  const lines = s.split("\n");
  if (s.endsWith("\n")) {
    lines.pop();
  }
  if (s.includes("\r")) {
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      if (l.endsWith("\r")) {
        lines[i] = l.slice(0, -1);
      }
    }
  }
  return lines;
}

/** Above this many m×n cells, use linear-space Hirschberg; 4M cells is about 32MB for the dense table. */
const SPACE_THRESHOLD = 4_000_000;

/**
 * Time budget in m×n cells (~tens of ms): both algorithms are O(mn) time on the main thread and the 2 MiB server cap
 * admits far larger inputs, so a trimmed middle past this falls back to a coarse but valid del-all/add-all script.
 */
const TIME_BUDGET_CELLS = 25_000_000;

/** Dense LCS table, bottom-up; O(mn) space, small inputs only. */
function lcsTable(a: string[], b: string[]): number[][] {
  const m = a.length;
  const n = b.length;
  const t: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      if (a[i] === b[j]) {
        t[i]![j] = (t[i + 1]?.[j + 1] ?? 0) + 1; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      } else {
        t[i]![j] = Math.max(t[i + 1]?.[j] ?? 0, t[i]?.[j + 1] ?? 0); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
  }
  return t;
}

/** Last row of the LCS table for a[aLo..aHi) vs b[bLo..bHi), in two rows of space. */
function lcsLastRow(
  a: string[],
  aLo: number,
  aHi: number,
  b: string[],
  bLo: number,
  bHi: number,
): number[] {
  const cols = bHi - bLo;
  let prev = new Array<number>(cols + 1).fill(0);
  let curr = new Array<number>(cols + 1).fill(0);
  for (let i = aLo; i < aHi; i++) {
    for (let j = bLo; j < bHi; j++) {
      if (a[i] === b[j]) {
        curr[j - bLo + 1] = prev[j - bLo]! + 1; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      } else {
        curr[j - bLo + 1] = Math.max(curr[j - bLo]!, prev[j - bLo + 1]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
    [prev, curr] = [curr, prev];
    curr.fill(0);
  }
  return prev;
}

/** Hirschberg linear-space diff: split `a` at its midpoint and find the best split in `b` from forward and reverse rows. */
function hirschbergDiff(
  a: string[],
  aLo: number,
  aHi: number,
  b: string[],
  bLo: number,
  bHi: number,
  aOrig: string[],
  bOrig: string[],
  aOffset: number,
  bOffset: number,
): DiffLine[] {
  const m = aHi - aLo;
  const n = bHi - bLo;

  // No `m === 0` base case: `lineDiff` enters only with m >= 1 and the split runs only for m >= 2, so both halves keep
  // m >= 1. `n === 0` is reachable, since `bestJ` may land on either end.
  if (n === 0) {
    const out: DiffLine[] = [];
    for (let i = aLo; i < aHi; i++) {
      out.push({ kind: "del", oldNo: aOffset + i + 1, newNo: 0, text: aOrig[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return out;
  }
  if (m === 1) {
    // Base case: single line in a vs b[bLo..bHi)
    const line = a[aLo]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    let matchIdx = -1;
    for (let j = bLo; j < bHi; j++) {
      if (b[j] === line) {
        matchIdx = j;
        break;
      }
    }
    const out: DiffLine[] = [];
    if (matchIdx === -1) {
      out.push({ kind: "del", oldNo: aOffset + aLo + 1, newNo: 0, text: aOrig[aLo]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      for (let j = bLo; j < bHi; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    } else {
      for (let j = bLo; j < matchIdx; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
      out.push({
        kind: "ctx",
        oldNo: aOffset + aLo + 1,
        newNo: bOffset + matchIdx + 1,
        text: aOrig[aLo]!, // eslint-disable-line @typescript-eslint/no-non-null-assertion
      });
      for (let j = matchIdx + 1; j < bHi; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
    return out;
  }

  const aMid = aLo + Math.floor(m / 2);

  const fwd = lcsLastRow(a, aLo, aMid, b, bLo, bHi);

  const aRev = a.slice(aMid, aHi).reverse();
  const bRev = b.slice(bLo, bHi).reverse();
  const rev = lcsLastRow(aRev, 0, aRev.length, bRev, 0, bRev.length);

  let bestJ = bLo;
  let bestScore = -1;
  for (let j = bLo; j <= bHi; j++) {
    const score = fwd[j - bLo]! + rev[bHi - j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    if (score > bestScore) {
      bestScore = score;
      bestJ = j;
    }
  }

  const left = hirschbergDiff(a, aLo, aMid, b, bLo, bestJ, aOrig, bOrig, aOffset, bOffset);
  const right = hirschbergDiff(a, aMid, aHi, b, bestJ, bHi, aOrig, bOrig, aOffset, bOffset);
  return left.concat(right);
}

/** Line-level diff in order. `ignoreWhitespace` collapses lines differing only in whitespace to context. */
export function lineDiff(
  oldText: string,
  newText: string,
  opts: { ignoreWhitespace?: boolean } = {},
): DiffLine[] {
  return diffLineArrays(splitLines(oldText), splitLines(newText), opts);
}

/** The engine both entry points share, so `lineDiff` and `lineDelta` differ only in options. */
function diffLineArrays(
  a: string[],
  b: string[],
  opts: { ignoreWhitespace?: boolean },
): DiffLine[] {
  const normalize =
    opts.ignoreWhitespace === true
      ? (s: string): string => s.replace(/\s+/g, " ").trim()
      : (s: string): string => s;
  const aNorm = opts.ignoreWhitespace === true ? a.map(normalize) : a;
  const bNorm = opts.ignoreWhitespace === true ? b.map(normalize) : b;

  // Common prefix/suffix trim: edits cluster, so this removes most of the m×n area and bounds the budget check.
  let p = 0;
  const maxTrim = Math.min(a.length, b.length);
  while (p < maxTrim && aNorm[p] === bNorm[p]) {
    p++;
  }
  let s = 0;
  while (s < maxTrim - p && aNorm[a.length - 1 - s] === bNorm[b.length - 1 - s]) {
    s++;
  }

  const out: DiffLine[] = [];
  for (let k = 0; k < p; k++) {
    out.push({ kind: "ctx", oldNo: k + 1, newNo: k + 1, text: a[k]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  out.push(...diffMiddle(a, b, aNorm, bNorm, p, s));
  for (let k = 0; k < s; k++) {
    const ai = a.length - s + k;
    const bi = b.length - s + k;
    out.push({ kind: "ctx", oldNo: ai + 1, newNo: bi + 1, text: a[ai]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  return out;
}

/** Diff the trimmed middle by size, or the coarse fallback past the time budget. */
function diffMiddle(
  a: string[],
  b: string[],
  aNorm: string[],
  bNorm: string[],
  p: number,
  s: number,
): DiffLine[] {
  const aHi = a.length - s;
  const bHi = b.length - s;
  const m = aHi - p;
  const n = bHi - p;
  if (m === 0 && n === 0) {
    return [];
  }

  // Every consumer holds for any valid script, so the fallback costs only hunk granularity.
  if (m * n > TIME_BUDGET_CELLS) {
    const out: DiffLine[] = [];
    for (let i = p; i < aHi; i++) {
      out.push({ kind: "del", oldNo: i + 1, newNo: 0, text: a[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    for (let j = p; j < bHi; j++) {
      out.push({ kind: "add", oldNo: 0, newNo: j + 1, text: b[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return out;
  }

  if (m * n > SPACE_THRESHOLD) {
    return hirschbergDiff(
      aNorm.slice(p, aHi),
      0,
      m,
      bNorm.slice(p, bHi),
      0,
      n,
      a.slice(p, aHi),
      b.slice(p, bHi),
      p,
      p,
    );
  }

  const aNormMid = aNorm.slice(p, aHi);
  const bNormMid = bNorm.slice(p, bHi);
  const t = lcsTable(aNormMid, bNormMid);
  const out: DiffLine[] = [];

  let i = 0;
  let j = 0;
  while (i < m && j < n) {
    if (aNormMid[i] === bNormMid[j]) {
      out.push({ kind: "ctx", oldNo: p + i + 1, newNo: p + j + 1, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
      j++;
    } else if ((t[i + 1]?.[j] ?? 0) >= (t[i]?.[j + 1] ?? 0)) {
      out.push({ kind: "del", oldNo: p + i + 1, newNo: 0, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
    } else {
      out.push({ kind: "add", oldNo: 0, newNo: p + j + 1, text: b[p + j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      j++;
    }
  }
  while (i < m) {
    out.push({ kind: "del", oldNo: p + i + 1, newNo: 0, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    i++;
  }
  while (j < n) {
    out.push({ kind: "add", oldNo: 0, newNo: p + j + 1, text: b[p + j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    j++;
  }
  return out;
}

/**
 * Lines a change added and removed, read through `splitLines` like `lineDiff`. Go twin: `lineDelta` in
 * `internal/buffer/linediff.go`; shared fixture `internal/buffer/testdata/line_delta.json`.
 */
export function lineDelta(oldText: string, newText: string): { added: number; removed: number } {
  const s = stats(diffLineArrays(splitLines(oldText), splitLines(newText), {}));
  return { added: s.adds, removed: s.dels };
}

// Intra-line (word-level) diff: which characters changed on a modified line.

/** A half-open character range `[start, end)` within one line. */
export interface CharRange {
  /** 0-based index of the first character in the range. */
  readonly start: number;
  /** 0-based index one past the last character. */
  readonly end: number;
}

/** The changed character ranges of one del/add line pair. */
export interface WordDiff {
  /** Ranges into the OLD line that the new line does not have. */
  readonly del: CharRange[];
  /** Ranges into the NEW line that the old line did not have. */
  readonly add: CharRange[];
}

/** Past this a line is too long for word hunting, and the m×n table stops being free. */
const MAX_WORD_TOKENS = 400;

/** Past this, per-word marks read as confetti and say less than the row tint. */
const MAX_WORD_RUNS = 8;

function isWordCharCode(c: number): boolean {
  return (
    (c >= 48 && c <= 57) ||
    (c >= 65 && c <= 90) ||
    (c >= 97 && c <= 122) ||
    c === 95 ||
    c === 36 ||
    c >= 0x80
  );
}

function isSpaceCode(c: number): boolean {
  return c === 32 || c === 9;
}

interface WordToken {
  readonly text: string;
  readonly start: number;
}

/** Split a line into identifier runs, whitespace runs, and single other
 *  characters. Every character lands in exactly one token, so a token's `start`
 *  plus its length addresses the original line. */
function splitWords(s: string): WordToken[] {
  const out: WordToken[] = [];
  let i = 0;
  while (i < s.length) {
    const start = i;
    const c = s.charCodeAt(i);
    if (isWordCharCode(c)) {
      while (i < s.length && isWordCharCode(s.charCodeAt(i))) {
        i++;
      }
    } else if (isSpaceCode(c)) {
      while (i < s.length && isSpaceCode(s.charCodeAt(i))) {
        i++;
      }
    } else {
      i++;
    }
    out.push({ text: s.slice(start, i), start });
  }
  return out;
}

/** Merges touching ranges, so `foo(` and `bar` produce one mark. */
function pushRange(out: CharRange[], tok: WordToken): void {
  const end = tok.start + tok.text.length;
  const last = out[out.length - 1];
  if (last?.end === tok.start) {
    out[out.length - 1] = { start: last.start, end };
    return;
  }
  out.push({ start: tok.start, end });
}

/**
 * Ranges differing between two versions of one line, or null when per-word marks would not beat the row tint (no
 * shared non-blank token, or more than `MAX_WORD_RUNS` changes). Null is not a failure.
 */
export function wordDiff(oldLine: string, newLine: string): WordDiff | null {
  if (oldLine === newLine || oldLine === "" || newLine === "") {
    return null;
  }
  const a = splitWords(oldLine);
  const b = splitWords(newLine);
  if (a.length > MAX_WORD_TOKENS || b.length > MAX_WORD_TOKENS) {
    return null;
  }

  const at = a.map((t) => t.text);
  const bt = b.map((t) => t.text);
  const t = lcsTable(at, bt);

  const del: CharRange[] = [];
  const add: CharRange[] = [];
  let matchedInk = 0;
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (at[i] === bt[j]) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- i < a.length
      const tok = a[i]!;
      if (tok.text.trim() !== "") {
        matchedInk += tok.text.length;
      }
      i++;
      j++;
    } else if ((t[i + 1]?.[j] ?? 0) >= (t[i]?.[j + 1] ?? 0)) {
      pushRange(del, a[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
    } else {
      pushRange(add, b[j]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      j++;
    }
  }
  while (i < a.length) {
    pushRange(del, a[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    i++;
  }
  while (j < b.length) {
    pushRange(add, b[j]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    j++;
  }

  // No shared ink means every token changed; this also covers a whole-line rewrite (a matched token always leaves a gap).
  if (matchedInk === 0) {
    return null;
  }
  if (del.length > MAX_WORD_RUNS || add.length > MAX_WORD_RUNS) {
    return null;
  }
  return { del, add };
}

/**
 * Word marks for every modified line, keyed by line. A del and add pair when at the same offset within one changed
 * run, which survives `lineDiff`'s del/add interleaving; unpaired lines get no marks.
 */
export function wordMarks(lines: readonly DiffLine[]): Map<DiffLine, CharRange[]> {
  const marks = new Map<DiffLine, CharRange[]>();
  let dels: DiffLine[] = [];
  let adds: DiffLine[] = [];

  const flush = (): void => {
    const pairs = Math.min(dels.length, adds.length);
    for (let k = 0; k < pairs; k++) {
      const d = dels[k]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      const s = adds[k]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      const wd = wordDiff(d.text, s.text);
      if (wd !== null) {
        marks.set(d, wd.del);
        marks.set(s, wd.add);
      }
    }
    dels = [];
    adds = [];
  };

  for (const l of lines) {
    if (l.kind === "del") {
      dels.push(l);
    } else if (l.kind === "add") {
      adds.push(l);
    } else {
      flush();
    }
  }
  flush();
  return marks;
}

/**
 * Window a diff to whole hunks with `context` lines each side, capped at `maxRows`. Context runs over 2×context
 * collapse. Returns the lines and the number of whole hunks omitted.
 */
export function windowHunks(
  lines: DiffLine[],
  opts: { maxRows?: number; context?: number } = {},
): { lines: DiffLine[]; hunksOmitted: number } {
  const maxRows = opts.maxRows ?? 24;
  const context = opts.context ?? 2;

  const runs: { changed: boolean; lines: DiffLine[] }[] = [];
  for (const l of lines) {
    const changed = l.kind !== "ctx";
    const last = runs[runs.length - 1];
    if (last?.changed === changed) {
      last.lines.push(l);
    } else {
      runs.push({ changed, lines: [l] });
    }
  }

  const totalHunks = runs.filter((r) => r.changed).length;
  if (totalHunks === 0) {
    return { lines: [], hunksOmitted: 0 };
  }

  const out: DiffLine[] = [];
  let kept = 0;
  for (let i = 0; i < runs.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const run = runs[i]!;
    if (!run.changed) {
      // Context adjoining a hunk keeps up to `context` lines on the touching side; a run between hunks keeps both ends.
      const head = runs[i - 1]?.changed === true ? run.lines.slice(0, context) : [];
      const tail = runs[i + 1]?.changed === true ? run.lines.slice(-context) : [];
      if (head.length + tail.length >= run.lines.length) {
        out.push(...run.lines); // the two ends meet; nothing to elide
      } else {
        out.push(...head, ...tail);
      }
      continue;
    }
    // A hunk goes in whole or not at all.
    if (out.length + run.lines.length > maxRows && kept > 0) {
      break;
    }
    out.push(...run.lines);
    kept++;
  }
  return { lines: out, hunksOmitted: totalHunks - kept };
}
