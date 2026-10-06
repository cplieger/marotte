/**
 * Lowercases for a case-insensitive scan with Unicode's simple per-code-point mapping (no
 * Final_Sigma, no locale; U+0130 becomes `i`). Length-preserving in UTF-16 code units, so an index
 * into fold(s) is an index into s. A lone surrogate is kept.
 */
export function fold(s: string): string {
  let out = "";
  for (const cp of s) {
    out += cp === "\u0130" ? "i" : cp.toLowerCase();
  }
  return out;
}
