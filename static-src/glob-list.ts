// A comma-separated list of globs as typed in one text box. KAS matches these with minimatch, whose
// brace sets (`*.{ts,js}`) carry their own commas, so only a comma outside every brace separates.

/** The patterns in raw, each trimmed, empty entries dropped. An unclosed brace keeps the rest of
 *  the text in one pattern, as minimatch then reads it literally. */
export function splitGlobList(raw: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (c === "{") {
      depth++;
    } else if (c === "}" && depth > 0) {
      depth--;
    } else if (c === "," && depth === 0) {
      out.push(raw.slice(start, i));
      start = i + 1;
    }
  }
  out.push(raw.slice(start));
  return out.map((s) => s.trim()).filter((s) => s !== "");
}

/** The text a box shows for patterns, which splitGlobList reads back as the same list when every
 *  brace in them is closed. */
export function joinGlobList(patterns: readonly string[]): string {
  return patterns.join(", ");
}
