// The cross-language pin for the in-chat search reply: the TS decoders are generated from
// the Go structs, so this pins the ENCODER. Go's TestSearchWireContract writes the fixture
// from a real scan, decoded here through `decodeSearchResult`. Node: a disk read.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { SearchResult } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/chat/testdata/search_hits.json";

interface SearchFixture {
  queries: {
    name: string;
    query: string;
    case_sensitive: boolean;
    result: unknown;
  }[];
}

function loadFixture(): SearchFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as SearchFixture;
}

describe("the in-chat search reply shared with the Go implementation", () => {
  const fx = loadFixture();
  // Decoded per test rather than at describe scope, so a drifted fixture fails
  // the named case below instead of aborting collection.
  const decodeAll = (): SearchResult[] => fx.queries.map((q) => decodeSearchResult(q.result));
  const resultOf = (query: string): SearchResult => {
    const q = fx.queries.find((c) => c.query === query);
    expect(q, `the fixture carries a ${query} reply`).toBeDefined();
    return decodeSearchResult(q?.result);
  };

  it("carries queries and hits (an empty fixture would pass forever)", () => {
    expect(fx.queries.length).toBeGreaterThan(0);
    for (const r of decodeAll()) {
      expect(r.matches.length).toBeGreaterThan(0);
    }
  });

  it.each(fx.queries.map((q) => [q.name, q.result] as const))(
    "decodes %s through the generated decoder",
    (_name, raw) => {
      expect(() => decodeSearchResult(raw)).not.toThrow();
    },
  );

  it("carries the tally beside the hits: every entry read, every item counted", () => {
    for (const r of decodeAll()) {
      // The fixture's log is twenty-four entries, all read whatever matched.
      expect(r.scanned).toBe(24);
      // Nothing in the fixture reaches the cap, so the count IS the list.
      expect(r.matched).toBe(r.matches.length);
      expect(r.truncated).toBe(false);
    }
  });

  it("refuses a hit whose item or segment kind the client has no arm for", () => {
    // Both enums are registered, so the decoder is strict: an unknown kind fails the whole reply rather than
    // reaching the navigation as an item it cannot resolve.
    const r = fx.queries[0]?.result as { matches: Record<string, unknown>[] };
    const badKind = { ...r, matches: [{ ...r.matches[0], kind: "footnote" }] };
    expect(() => decodeSearchResult(badKind)).toThrow(/kind/);
    const badSegment = { ...r, matches: [{ ...r.matches[0], segments: ["footnote"] }] };
    expect(() => decodeSearchResult(badSegment)).toThrow(/segments/);
  });

  it("names one hit per item: a tool card is one hit on its call, its segments in card order", () => {
    const hits = resultOf("retry").matches;
    expect(new Set(hits.map((h) => `${h.turn_id}/${h.entry_id}`)).size).toBe(hits.length);
    const tool = hits.find((h) => h.entry_id === "tc1");
    expect(tool?.kind).toBe("tool");
    expect(tool?.segments).toEqual(["tool_title", "tool_output"]);
    expect(hits.some((h) => h.entry_id.endsWith(":result"))).toBe(false);
    // The delegate's message names its lane; every OTHER hit carries none, which routes in place.
    const delegate = hits.filter((h) => h.lane !== undefined);
    expect(delegate.map((h) => [h.entry_id, h.lane, h.kind])).toEqual([["a2", "sub-1", "message"]]);
  });

  it("lists every item kind but the spread turn under a free-text query", () => {
    const kinds = new Set(resultOf("retry").matches.map((h) => h.kind));
    expect([...kinds].sort()).toEqual(
      ["header", "message", "notice", "plan", "reasoning", "steer", "tool", "turn_failure"].sort(),
    );
    expect(resultOf("retry").spread).toBe(false);
  });

  it("answers a filter-only query with items and no segments", () => {
    const r = resultOf("turn:2");
    expect(r.matches.length).toBeGreaterThan(0);
    for (const h of r.matches) {
      expect(h.segments).toEqual([]);
      expect(h.turn).toBe(2);
    }
  });

  it("answers words spread across one turn's items with turn hits", () => {
    const r = resultOf("semantics budget");
    expect(r.spread).toBe(true);
    expect(r.matches.map((h) => [h.turn_id, h.kind])).toEqual([["t-1", "turn"]]);
  });
});
