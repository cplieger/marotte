// The cross-language pin for the file-search reply: Go's TestFileSearchWireContract writes the fixture from the real
// handler in both modes, decoded here through the generated decoder.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeFileSearchResult } from "./wire/decoders.gen.js";

const FIXTURE_PATH = "../internal/filebrowse/testdata/file_search.json";

interface FileSearchRun {
  query: string;
  mode: string;
  result: unknown;
}

interface FileSearchFixture {
  names: FileSearchRun;
  contents: FileSearchRun;
}

function loadFixture(): FileSearchFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as FileSearchFixture;
}

describe("the file-search replies shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes both modes through the generated decoder", () => {
    expect(() => decodeFileSearchResult(fx.names.result)).not.toThrow();
    expect(() => decodeFileSearchResult(fx.contents.result)).not.toThrow();
  });

  it("ranks names over the whole walk and marks each basename in UTF-16 units", () => {
    const r = decodeFileSearchResult(fx.names.result);
    expect(r.matches).toEqual([
      {
        path: "/workspace/needle-dir",
        excerpt: "",
        kind: "dir",
        line: 0,
        ranges: [{ start: 0, end: 6 }],
      },
      {
        path: "/workspace/\u{1F3AF}-needle.txt",
        excerpt: "",
        kind: "name",
        line: 0,
        ranges: [{ start: 3, end: 9 }],
      },
      {
        path: "/workspace/notes-needle.md",
        excerpt: "",
        kind: "name",
        line: 0,
        ranges: [{ start: 6, end: 12 }],
      },
    ]);
    const base = "\u{1F3AF}-needle.txt";
    expect(base.slice(3, 9)).toBe("needle");
    expect(r.scanned).toBe(6);
  });

  it("returns content rows only, in path order, marking every occurrence in the excerpt", () => {
    const r = decodeFileSearchResult(fx.contents.result);
    expect(r.matches.every((m) => m.kind === "content" && m.line >= 1)).toBe(true);
    const last = r.matches.at(-1);
    expect(last?.path).toBe("/workspace/\u{1F3AF}-needle.txt");
    expect(last?.ranges.map((g) => last.excerpt.slice(g.start, g.end))).toEqual([
      "needle",
      "needle",
    ]);
  });

  it("counts the lines the per-file cap cut while reporting the scan as whole", () => {
    const r = decodeFileSearchResult(fx.contents.result);
    const manyRows = r.matches.filter((m) => m.path === "/workspace/many.txt");
    expect(manyRows).toHaveLength(20);
    expect(r.matched).toBe(23);
    expect(r.matched).toBeGreaterThan(r.matches.length);
    expect(r.scanned).toBe(5);
    expect(r.truncated).toBe(false);
  });

  it("refuses a row whose kind the bundle has no arm for", () => {
    const r = fx.names.result as { matches: Record<string, unknown>[] };
    const forged = { ...r, matches: [{ ...r.matches[0], kind: "sigil" }] };
    expect(() => decodeFileSearchResult(forged)).toThrow();
  });

  it("refuses a reply with the tally absent, so a missing count cannot read as zero", () => {
    const r = fx.contents.result as Record<string, unknown>;
    const { matched: _matched, ...withoutMatched } = r;
    expect(() => decodeFileSearchResult(withoutMatched)).toThrow();
  });
});
