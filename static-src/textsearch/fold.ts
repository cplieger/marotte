import { ACCENT_FROM, ACCENT_TO } from "./fold-table.gen.js";

/**
 * Lowercases for a case-insensitive scan with Unicode's simple per-code-point mapping (no
 * Final_Sigma, no locale; U+0130 becomes `i`). Length-preserving in UTF-16 code units, so an index
 * into fold(s) is an index into s. A lone surrogate is kept.
 */
export function fold(s: string): string {
  let out = "";
  for (const cp of s) {
    out += lower(cp);
  }
  return out;
}

function lower(cp: string): string {
  return cp === "\u0130" ? "i" : cp.toLowerCase();
}

/**
 * `fold` plus accent folding (`é` to `e`), the twin of Go's `textsearch.FoldText`, through the table both
 * kernels generate from one source. Length-preserving in UTF-16 code units like `fold`.
 */
export function foldText(s: string): string {
  let out = "";
  for (const cp of s) {
    const low = lower(cp);
    const code = low.codePointAt(0) ?? 0;
    out += code < 0x80 ? low : String.fromCodePoint(accentBase(code));
  }
  return out;
}

function accentBase(code: number): number {
  let lo = 0;
  let hi = ACCENT_FROM.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const at = ACCENT_FROM[mid] ?? 0;
    if (at === code) {
      return ACCENT_TO[mid] ?? code;
    }
    if (at < code) {
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return code;
}
