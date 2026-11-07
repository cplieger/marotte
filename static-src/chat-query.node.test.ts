// The client half of the chat query contract: Go PRODUCES internal/chat/testdata/search_query.json
// (TestSearchQueryFixture) and this file CONSUMES it through parseChatQuery. Node placement: a disk read.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { parseChatQuery } from "./chat-query.js";

interface Row {
  readonly raw: string;
  readonly terms: readonly { readonly words: readonly string[]; readonly phrase: boolean }[];
  readonly filters: { readonly file: string; readonly tool: string; readonly turn: number };
}

function loadRows(): readonly Row[] {
  const raw = readFileSync(
    new URL("../internal/chat/testdata/search_query.json", import.meta.url),
    "utf8",
  );
  return (JSON.parse(raw) as { rows: Row[] }).rows;
}

describe("the chat query contract shared with the Go implementation", () => {
  it("reads the rows that separate a filter from text", () => {
    const raws = loadRows().map((r) => r.raw);
    for (const want of ["turn:0", "http://x", "file:a.go file:b.go", '"file:a.go"']) {
      expect(raws).toContain(want);
    }
  });

  for (const row of loadRows()) {
    it(`agrees with Go on ${JSON.stringify(row.raw)}`, () => {
      const q = parseChatQuery(row.raw);
      expect(q.terms).toEqual(row.terms);
      expect({ file: q.file, tool: q.tool, turn: q.turn }).toEqual(row.filters);
    });
  }
});
