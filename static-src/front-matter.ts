// YAML front-matter split for the editor's read mode. Pure and DOM-free. Mirrors the value syntax of
// internal/steering/frontmatter.go for the subset `.kiro` documents use (flat and block scalars, quoted strings,
// flow and block sequences). Stricter in one place: the closing fence must be exactly `---`, since a misread here
// removes body text. Parsed client-side because three of the editor's four entry points carry only a path.
// Schema-free: it reports the keys the file declared, in order, and invents no default.

/** One declared front-matter key: a scalar carries `value`, a sequence `items`; an empty key carries neither. */
export interface FrontMatterField {
  key: string;
  value: string;
  items: string[];
}

export interface FrontMatterSplit {
  /** Whether a well-formed `---` fenced block opened the document. */
  present: boolean;
  fields: FrontMatterField[];
  /** The document with its front-matter block removed and line endings folded
   *  to "\n". This is what gets rendered as markdown. */
  body: string;
}

const OPEN_FENCE = "---\n";

/**
 * Split a document into its declared front-matter fields and its markdown body. A malformed header is never an
 * error: it stays as body text.
 */
export function splitFrontMatter(text: string): FrontMatterSplit {
  const content = normalizeText(text);
  if (!content.startsWith(OPEN_FENCE)) {
    return { present: false, fields: [], body: content };
  }
  // The closing fence must sit at least one line below the opening one, so this index means an empty block.
  const close = findCloseFence(content);
  if (close <= OPEN_FENCE.length) {
    return { present: false, fields: [], body: content };
  }
  const block = content.slice(OPEN_FENCE.length, close);
  const afterFenceLine = content.indexOf("\n", close + 1);
  const body = afterFenceLine === -1 ? "" : content.slice(afterFenceLine + 1);
  return { present: true, fields: parseFields(block), body };
}

/**
 * Index of the newline that begins the closing fence line, or -1. The line must be exactly `---` (trailing
 * whitespace tolerated): a prefix match let an unterminated header absorb text up to a later horizontal rule.
 */
function findCloseFence(content: string): number {
  // A fence needs its own line, so the search is over line starts from here on.
  let from = OPEN_FENCE.length - 1;
  for (;;) {
    const at = content.indexOf("\n---", from);
    if (at === -1) {
      return -1;
    }
    const lineEnd = content.indexOf("\n", at + 1);
    const line = lineEnd === -1 ? content.slice(at + 1) : content.slice(at + 1, lineEnd);
    if (line.trimEnd() === "---") {
      return at;
    }
    from = at + 1;
  }
}

/** Strip a leading BOM and fold every line ending to "\n", a lone "\r" included, or a Mac-classic header is one line. */
function normalizeText(text: string): string {
  const s = text.startsWith("\ufeff") ? text.slice(1) : text;
  return s.replace(/\r\n?/g, "\n");
}

/** Not a per-line `split(":")`: a block scalar or sequence owns every more-indented line after its key. */
function parseFields(block: string): FrontMatterField[] {
  const lines = block.split("\n");
  const out: FrontMatterField[] = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (isSkippable(line) || leadingSpaces(line) > 0) {
      // An indented line at top level was already consumed by a block reader, or is malformed. Either way not a key.
      continue;
    }
    const colon = line.indexOf(":");
    if (colon < 0) {
      continue;
    }
    const key = line.slice(0, colon).trim();
    const val = line.slice(colon + 1).trim();
    if (isBlockScalarIndicator(val)) {
      const folded = readBlockScalar(lines, i + 1);
      out.push({ key, value: folded.value, items: [] });
      i = folded.lastIdx;
      continue;
    }
    if (val === "") {
      const seq = readBlockSequence(lines, i + 1);
      out.push({ key, value: "", items: seq.items });
      i = seq.lastIdx;
      continue;
    }
    if (val.startsWith("[")) {
      out.push({ key, value: "", items: parseFlowSequence(val) });
      continue;
    }
    out.push({ key, value: unquote(val), items: [] });
  }
  return out;
}

function isSkippable(line: string): boolean {
  const t = line.trim();
  return t === "" || t.startsWith("#");
}

/** Whether a value is a block-scalar header: `>` or `|`, optionally with a chomping or indent indicator. `>foo` is not. */
function isBlockScalarIndicator(val: string): boolean {
  if (!val.startsWith(">") && !val.startsWith("|")) {
    return false;
  }
  for (const c of val.slice(1)) {
    if (c !== "-" && c !== "+" && (c < "0" || c > "9")) {
      return false;
    }
  }
  return true;
}

/**
 * Fold the indented lines from `from` into one string; returns it and the last line consumed. Both indicators fold
 * `>`-style: the value renders into one metadata row, which a literal newline would break.
 */
function readBlockScalar(lines: string[], from: number): { value: string; lastIdx: number } {
  const parts: string[] = [];
  let i = from;
  for (; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      continue;
    }
    if (leadingSpaces(line) === 0) {
      break;
    }
    parts.push(line.trim());
  }
  return { value: parts.join(" "), lastIdx: i - 1 };
}

/** Returns `lastIdx = from - 1` when the next content line is not an entry, leaving the caller's cursor in place. */
function readBlockSequence(lines: string[], from: number): { items: string[]; lastIdx: number } {
  const items: string[] = [];
  let i = from;
  for (; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      continue;
    }
    const t = line.trim();
    if (leadingSpaces(line) === 0 || !t.startsWith("- ")) {
      break;
    }
    items.push(unquote(t.slice(2).trim()));
  }
  if (items.length === 0) {
    return { items: [], lastIdx: from - 1 };
  }
  return { items, lastIdx: i - 1 };
}

/** Commas inside quotes are not supported: no `.kiro` document uses one. */
function parseFlowSequence(val: string): string[] {
  let inner = val.startsWith("[") ? val.slice(1) : val;
  inner = inner.endsWith("]") ? inner.slice(0, -1) : inner;
  if (inner.trim() === "") {
    return [];
  }
  const out: string[] = [];
  for (const part of inner.split(",")) {
    const v = unquote(part.trim());
    if (v !== "") {
      out.push(v);
    }
  }
  return out;
}

/** Count a line's indentation, treating a tab as one level. */
function leadingSpaces(line: string): number {
  let n = 0;
  for (const c of line) {
    if (c !== " " && c !== "\t") {
      break;
    }
    n++;
  }
  return n;
}

/** Not an escape decoder: `.kiro` front-matter quotes to protect a leading `*` or a colon, never to encode one. */
function unquote(s: string): string {
  if (s.length >= 2) {
    const first = s[0];
    const last = s[s.length - 1];
    if ((first === '"' && last === '"') || (first === "'" && last === "'")) {
      return s.slice(1, -1);
    }
  }
  return s;
}
