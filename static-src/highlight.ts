// Minimal dependency-free highlighter. Three tiers: a dedicated keyword table (an entry with an empty set falls
// through), the generic tokenizer (GENERIC_KW) for every other recognised language, and escaped passthrough for
// unknown ones.

import { escText } from "./strings.js";
import { KNOWN_EXTENSIONS, extToLang } from "./file-extensions.js";
import { SUPPORTED_LANGUAGES, FENCED_ALIASES, KEYWORDS, GENERIC_KW } from "./highlight-langs.js";

// charCode classification for the hot path.
function isDigitCode(c: number): boolean {
  return c >= 48 && c <= 57; // '0'-'9'
}

function isIdentStartCode(c: number): boolean {
  return (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || c === 95 || c === 36; // A-Z, a-z, _, $
}

function isIdentCharCode(c: number): boolean {
  return (
    (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || (c >= 48 && c <= 57) || c === 95 || c === 36
  );
}

const PUNCT_CODES = new Set<number>([
  123, 125, 40, 41, 91, 93, 59, 44, 46, 58, 33, 38, 124, 60, 62, 61, 43, 45, 42, 47, 37, 94, 126,
  63, 64,
  // { } ( ) [ ] ; , . : ! & | < > = + - * / % ^ ~ ? @
]);

function isPunctCode(c: number): boolean {
  return PUNCT_CODES.has(c);
}

function isTokenBoundaryCode(c: number): boolean {
  return (
    isIdentStartCode(c) ||
    isDigitCode(c) ||
    c === 34 ||
    c === 39 ||
    c === 96 ||
    c === 35 ||
    isPunctCode(c)
  );
}

interface Token {
  type: "keyword" | "string" | "comment" | "number" | "punctuation" | "text";
  value: string;
}

/** Where a backtick opens a string (JS/TS templates, Go raw strings); elsewhere it is punctuation. */
const BACKTICK_STRING_LANGS = new Set(["js", "ts", "go"]);

function tokenize(code: string, lang: string): Token[] {
  const tokens: Token[] = [];
  const kw = KEYWORDS[lang] ?? GENERIC_KW;
  let i = 0;
  const len = code.length;

  while (i < len) {
    const cc = code.charCodeAt(i);

    if (cc === 47 /* / */ && code.charCodeAt(i + 1) === 47) {
      const end = code.indexOf("\n", i);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }
    if (cc === 47 /* / */ && code.charCodeAt(i + 1) === 42 /* * */) {
      const end = code.indexOf("*/", i + 2);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end + 2);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }
    // Hash comments (Python, Shell, YAML, TOML).
    if (
      cc === 35 /* # */ &&
      (lang === "py" || lang === "sh" || lang === "yaml" || lang === "toml" || lang === "docker")
    ) {
      const end = code.indexOf("\n", i);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }

    // A backtick delimits a string only where the language has backtick strings; elsewhere a stray one would swallow text.
    if (cc === 34 || cc === 39 || (cc === 96 && BACKTICK_STRING_LANGS.has(lang))) {
      // " ' `
      let j = i + 1;
      if (cc === 96) {
        while (j < len && code.charCodeAt(j) !== 96) {
          j++;
        }
        if (j < len) {
          j++;
        }
      } else {
        while (j < len && code.charCodeAt(j) !== cc) {
          if (code.charCodeAt(j) === 92 /* \\ */) {
            j++;
          }
          j++;
        }
        if (j < len) {
          j++;
        }
      }
      tokens.push({ type: "string", value: code.substring(i, j) });
      i = j;
      continue;
    }

    if (
      isDigitCode(cc) ||
      (cc === 46 /* . */ && i + 1 < len && isDigitCode(code.charCodeAt(i + 1)))
    ) {
      let j = i;
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- defensive check
      if (cc === 48 /* 0 */ && j + 1 < len && /[xXoObB]/.test(code[j + 1]!)) {
        j += 2;
      }
      while (j < len) {
        const ch = code[j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
        if (/[\d.a-fA-F_eE]/.test(ch)) {
          j++;
          continue;
        }
        // `+`/`-` continue a number only as an exponent sign (1e-5), so `1-2` is not one token.
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- j > i inside the loop
        if ((ch === "+" || ch === "-") && j > i && /[eE]/.test(code[j - 1]!)) {
          j++;
          continue;
        }
        break;
      }
      tokens.push({ type: "number", value: code.substring(i, j) });
      i = j;
      continue;
    }

    if (isIdentStartCode(cc)) {
      let j = i;
      while (j < len && isIdentCharCode(code.charCodeAt(j))) {
        j++;
      }
      const word = code.substring(i, j);
      tokens.push({ type: kw.has(word) ? "keyword" : "text", value: word });
      i = j;
      continue;
    }

    if (isPunctCode(cc)) {
      tokens.push({ type: "punctuation", value: code[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
      continue;
    }

    let j = i;
    while (j < len && !isTokenBoundaryCode(code.charCodeAt(j))) {
      j++;
    }
    if (j === i) {
      j = i + 1;
    }
    tokens.push({ type: "text", value: code.substring(i, j) });
    i = j;
  }

  return tokens;
}

/** Highlight source code by language key into span-wrapped HTML. An unknown or empty lang returns escaped text. */
export function highlightByLang(code: string, lang: string): string {
  if (lang === "" || lang === "md") {
    return escText(code);
  }
  if (SUPPORTED_LANGUAGES.has(lang) || lang in KNOWN_EXTENSIONS || lang in FENCED_ALIASES) {
    const tokens = tokenize(code, lang);
    const parts: string[] = [];
    for (const t of tokens) {
      if (t.type === "text") {
        parts.push(escText(t.value));
      } else {
        parts.push(`<span class="hl-${t.type}">${escText(t.value)}</span>`);
      }
    }
    return parts.join("");
  }
  return escText(code);
}

/** The token classes a run can carry, indexed by `HighlightRuns.kinds`. */
export const RUN_CLASSES = ["keyword", "string", "comment", "number", "punctuation"] as const;

/** A file's highlighted tokens as offsets into its text, plain-text tokens omitted, so a row
 *  renderer can build spans for any slice without parsing markup. */
export interface HighlightRuns {
  readonly starts: Int32Array;
  readonly ends: Int32Array;
  /** An index into RUN_CLASSES per run. */
  readonly kinds: Uint8Array;
}

/** Tokenize `code` once by its filename's language; null for a language with no highlighting. */
export function highlightRuns(code: string, filename: string): HighlightRuns | null {
  const lang = detectLang(filename);
  if (!tokenizable(lang)) {
    return null;
  }
  const starts: number[] = [];
  const ends: number[] = [];
  const kinds: number[] = [];
  let at = 0;
  for (const t of tokenize(code, lang)) {
    const end = at + t.value.length;
    if (t.type !== "text") {
      starts.push(at);
      ends.push(end);
      kinds.push(RUN_CLASSES.indexOf(t.type));
    }
    at = end;
  }
  return {
    starts: Int32Array.from(starts),
    ends: Int32Array.from(ends),
    kinds: Uint8Array.from(kinds),
  };
}

/** Highlight source code by filename (extension-based language detection). */
export function highlight(code: string, filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return highlightByLang(code, extToLang(ext));
}

/**
 * Highlight source and mark character ranges with `markClass` (the diff pane's word changes). A token straddling a
 * mark is split and each piece keeps its class. Ranges are half-open, ascending, non-overlapping and non-empty
 * (`wordDiff` guarantees this). An empty list is exactly `highlightByLang`.
 */
export function highlightMarked(
  code: string,
  lang: string,
  marks: readonly { readonly start: number; readonly end: number }[],
  markClass: string,
): string {
  if (marks.length === 0) {
    return highlightByLang(code, lang);
  }
  const tokens = tokenizable(lang) ? tokenize(code, lang) : [{ type: "text", value: code }];
  const parts: string[] = [];
  let at = 0;
  let m = 0;
  for (const t of tokens) {
    const end = at + t.value.length;
    // Cut the token wherever the next mark starts or ends.
    let cut = at;
    while (cut < end) {
      while (m < marks.length && (marks[m]?.end ?? 0) <= cut) {
        m++;
      }
      const mark = marks[m];
      const inMark = mark !== undefined && mark.start <= cut;
      const stop = Math.min(end, inMark ? mark.end : (mark?.start ?? end));
      const text = code.slice(cut, stop);
      const cls = [t.type === "text" ? "" : `hl-${t.type}`, inMark ? markClass : ""]
        .filter((c) => c !== "")
        .join(" ");
      parts.push(cls === "" ? escText(text) : `<span class="${cls}">${escText(text)}</span>`);
      cut = stop;
    }
    at = end;
  }
  return parts.join("");
}

/** `md` and unknown languages are escaped passthrough, with no syntax spans. */
function tokenizable(lang: string): boolean {
  if (lang === "" || lang === "md") {
    return false;
  }
  return SUPPORTED_LANGUAGES.has(lang) || lang in KNOWN_EXTENSIONS || lang in FENCED_ALIASES;
}

/** Detect language from filename extension. */
export function detectLang(filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return extToLang(ext);
}

/** Normalize a fenced-code language tag to the internal key, or "" when unrecognised. */
export function normalizeLang(tag: string): string {
  const s = tag.trim().toLowerCase();
  if (s === "") {
    return "";
  }
  if (SUPPORTED_LANGUAGES.has(s)) {
    return s;
  }
  const fromExt = extToLang(s);
  if (fromExt !== "") {
    return fromExt;
  }
  return FENCED_ALIASES[s] ?? "";
}

/**
 * Resolve a language from a fence tag, a bare extension, or a file path; "" when nothing matches. `normalizeLang`
 * compares the whole string, so a path never matches there.
 */
export function resolveLangHint(tag: string): string {
  const direct = normalizeLang(tag);
  if (direct !== "") {
    return direct;
  }
  return tag.includes(".") ? detectLang(tag) : "";
}
