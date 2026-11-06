// The cross-language pin for the query grammar and matcher: Go PRODUCES testdata/match.json
// (TestMatchFixture, UPDATE_GOLDEN=1) and this file CONSUMES it. Node placement: the fixture is a disk read.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { parseQuery, prepareMatcher, spans } from "./query.js";

const FIXTURE_PATH = "../../internal/textsearch/testdata/match.json";

interface Row {
  readonly name: string;
  readonly text: string;
  readonly query: string;
  readonly case_sensitive: boolean;
  readonly terms: readonly { readonly words: readonly string[]; readonly phrase: boolean }[];
  readonly filters: readonly { readonly name: string; readonly value: string }[];
  readonly spans: readonly {
    readonly term: number;
    readonly rune: number;
    readonly utf16: number;
    readonly utf16_len: number;
  }[];
  readonly all: boolean;
}

function loadRows(): readonly Row[] {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return (JSON.parse(raw) as { rows: Row[] }).rows;
}

/** The fixture's filter rule: the name `file` only. */
const acceptFile = (name: string): boolean => name === "file";

describe("the query contract shared with the Go implementation", () => {
  const rows = loadRows();

  it("carries the rows this contract exists for", () => {
    const names = rows.map((r) => r.name);
    for (const want of [
      "all_terms_in_another_order",
      "two_of_three_terms",
      "phrase_out_of_order",
      "phrase_across_a_newline",
      "accent_in_the_text",
      "astral_rune_before_the_match",
    ]) {
      expect(names).toContain(want);
    }
    expect(rows.filter((r) => r.all).length).toBeGreaterThanOrEqual(10);
    expect(rows.filter((r) => !r.all).length).toBeGreaterThanOrEqual(5);
  });

  for (const row of loadRows()) {
    it(`agrees with Go on "${row.name}"`, () => {
      const q = parseQuery(row.query, acceptFile);
      expect(q.terms, "terms").toEqual(row.terms);
      expect(q.filters, "filters").toEqual(row.filters);
      const m = prepareMatcher(q.terms, row.case_sensitive);
      const got = spans(row.text, m);
      expect(got, "spans").toEqual(
        row.spans.map((s) => ({ term: s.term, at: s.utf16, len: s.utf16_len })),
      );
      const all = m.terms.length > 0 && new Set(got.map((s) => s.term)).size === m.terms.length;
      expect(all, "every term matched").toBe(row.all);
    });
  }

  it("has utf16 and rune columns that name one position", () => {
    for (const row of loadRows()) {
      for (const s of row.spans) {
        expect([...row.text.slice(0, s.utf16)].length, row.name).toBe(s.rune);
      }
    }
  });
});
