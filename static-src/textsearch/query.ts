// The query grammar and matcher, the twin of Go's textsearch.ParseQuery and Matcher, pinned to it by
// internal/textsearch/testdata/match.json.

import { foldText } from "./fold.js";

/** One thing that must match: a word, or a quoted phrase whose words match in order across any whitespace. */
export interface Term {
  readonly words: readonly string[];
}

/** An unquoted `name:value` word the caller's rule accepted; `name` is lowercased. */
interface Filter {
  readonly name: string;
  readonly value: string;
}

interface Query {
  readonly terms: readonly Term[];
  readonly filters: readonly Filter[];
}

/** Unicode `White_Space`, the set Go's `unicode.IsSpace` answers. */
const SPACE = /^\p{White_Space}$/u;

/**
 * Split `raw` into terms and filters: whitespace separates words, `"` opens and closes a phrase (an unclosed one
 * runs to the end), and an unquoted `name:value` with a non-empty value is offered to `isFilter`. No OR or NOT.
 */
export function parseQuery(
  raw: string,
  isFilter?: (name: string, value: string) => boolean,
): Query {
  const terms: Term[] = [];
  const filters: Filter[] = [];
  let word = "";
  let phrase: string[] = [];
  let inPhrase = false;
  const endWord = (): void => {
    if (word === "") {
      return;
    }
    const w = word;
    word = "";
    if (inPhrase) {
      phrase.push(w);
      return;
    }
    const colon = w.indexOf(":");
    const name = colon < 0 ? "" : w.slice(0, colon).toLowerCase();
    const value = colon < 0 ? "" : w.slice(colon + 1);
    if (value !== "" && isFilter?.(name, value) === true) {
      filters.push({ name, value });
      return;
    }
    terms.push({ words: [w] });
  };
  const endPhrase = (): void => {
    if (phrase.length > 0) {
      terms.push({ words: phrase });
    }
    phrase = [];
    inPhrase = false;
  };
  for (const cp of raw.toWellFormed()) {
    if (cp === '"') {
      endWord();
      if (inPhrase) {
        endPhrase();
      } else {
        inPhrase = true;
      }
    } else if (SPACE.test(cp)) {
      endWord();
    } else {
      word += cp;
    }
  }
  endWord();
  endPhrase();
  return { terms, filters };
}

/** A query's terms prepared once: folded for a case-insensitive scan, literal otherwise. */
export interface Matcher {
  readonly terms: readonly (readonly string[])[];
  readonly caseSensitive: boolean;
}

export function prepareMatcher(terms: readonly Term[], caseSensitive: boolean): Matcher {
  const out: string[][] = [];
  for (const t of terms) {
    const words = t.words.map((w) => (caseSensitive ? w : foldText(w))).filter((w) => w !== "");
    if (words.length > 0) {
      out.push(words);
    }
  }
  return { terms: out, caseSensitive };
}

/** One term's match: `at` and `len` in UTF-16 code units of the ORIGINAL text. */
interface Span {
  readonly term: number;
  readonly at: number;
  readonly len: number;
}

/**
 * Every term's matches in text order, ties by term index. One term's spans never overlap; a phrase matches its
 * first word as a substring, then each next word after one or more whitespace code points, and a failed
 * extension resumes one code point after its start.
 */
export function spans(text: string, m: Matcher): Span[] {
  const out: Span[] = [];
  if (m.terms.length === 0) {
    return out;
  }
  const hay = m.caseSensitive ? text : foldText(text);
  m.terms.forEach((words, term) => {
    const first = words[0] ?? "";
    let from = 0;
    while (from < hay.length) {
      const at = hay.indexOf(first, from);
      if (at < 0) {
        break;
      }
      const end = phraseEnd(hay, at + first.length, words.slice(1));
      if (end < 0) {
        from = at + codePointWidth(hay, at);
        continue;
      }
      out.push({ term, at, len: end - at });
      from = end;
    }
  });
  return out.sort((a, b) => a.at - b.at || a.term - b.term);
}

function phraseEnd(hay: string, from: number, rest: readonly string[]): number {
  let pos = from;
  for (const w of rest) {
    const start = pos;
    while (pos < hay.length) {
      const cp = String.fromCodePoint(hay.codePointAt(pos) ?? 0);
      if (!SPACE.test(cp)) {
        break;
      }
      pos += cp.length;
    }
    if (pos === start || !hay.startsWith(w, pos)) {
      return -1;
    }
    pos += w.length;
  }
  return pos;
}

function codePointWidth(s: string, at: number): number {
  return (s.codePointAt(at) ?? 0) > 0xffff ? 2 : 1;
}
