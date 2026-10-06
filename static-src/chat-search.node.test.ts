// The cross-language pin for the in-chat search reply: the TS decoders are generated from
// the Go structs, so this pins the ENCODER. Go's TestSearchWireContract writes the fixture
// from a real scan, decoded here through `decodeSearchResult`. Node: a disk read.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/chat/testdata/search_hits.json";
const GO_SEARCH_PATH = "../internal/chat/search.go";
const CLIENT_FIND_PATH = "./find-in-chat.ts";

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

/**
 * One declared constant, read out of a source file as TEXT: the Go value is unexported and
 * the client's lives in a DOM module. No wire field or codegen holds the pair together.
 */
function declaredNumber(rel: string, pattern: RegExp): number {
  const src = readFileSync(new URL(rel, import.meta.url), "utf8");
  const m = pattern.exec(src);
  expect(m?.[1], `${rel} declares nothing matching ${pattern.source}`).toBeDefined();
  return Number(m?.[1]);
}

describe("the in-chat search reply shared with the Go implementation", () => {
  const fx = loadFixture();
  // Decoded per test rather than at describe scope, so a drifted fixture fails
  // the named case below instead of aborting collection.
  const decodeAll = (): SearchResult[] => fx.queries.map((q) => decodeSearchResult(q.result));
  const allHits = (): Hit[] => decodeAll().flatMap((r) => r.matches);

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

  it("carries the tally beside the hits: every entry read, every occurrence counted", () => {
    for (const r of decodeAll()) {
      // The fixture's log is nineteen entries, all read whatever matched.
      expect(r.scanned).toBe(19);
      // Nothing in the fixture reaches the cap, so the count IS the list.
      expect(r.matched).toBe(r.matches.length);
      expect(r.truncated).toBe(false);
    }
  });

  it("refuses a hit whose segment kind the client has no arm for", () => {
    // The enum is registered, so the decoder is strict: an unknown kind fails the
    // whole reply rather than reaching the navigation as a span it cannot resolve.
    const r = fx.queries[0]?.result as { matches: Record<string, unknown>[] };
    const forged = { ...r, matches: [{ ...r.matches[0], segment_kind: "footnote" }] };
    expect(() => decodeSearchResult(forged)).toThrow(/segment_kind/);
  });

  it("keeps the entry-kind contract: offset 0, zero length, no lane", () => {
    // Entry hits locate ENTRIES, not spans, so they carry no position (`landOnHit` routes them).
    const entries = allHits().filter((h) => h.segment_kind === "entry");
    expect(entries.length).toBeGreaterThan(0);
    for (const h of entries) {
      expect(h.offset).toBe(0);
      expect(h.segment_len).toBe(0);
      expect(h.lane).toBeUndefined();
    }
  });

  it("keeps offsets RUNE-counted: a multibyte word before the match must not skew it", () => {
    // The second occurrence sits behind "naïve" (ï is two bytes), so byte offsets would make
    // this 57. Scoped to the PROMPT segment; the attachment has its own.
    const prompt = allHits().filter((h) => h.segment_kind === "prompt");
    expect(prompt.map((h) => h.offset)).toEqual([15, 56]);
    // And segment-relative rather than turn-relative: the tool output is one entry
    // of a long turn, yet its match indexes the OUTPUT alone ("func retry…" → 5).
    const output = allHits().find((h) => h.segment_kind === "tool_output");
    expect(output?.offset).toBe(5);
    expect(output?.segment_len).toBe(37);
  });

  it("addresses entries: a result pairs to its call by id, the delegate names its lane", () => {
    // A call and its value are TWO entries paired by `<call>:result`, minted on both sides.
    const hits = allHits();
    const title = hits.find((h) => h.segment_kind === "tool_title");
    const output = hits.find((h) => h.segment_kind === "tool_output");
    expect(title?.entry_id).toBe("tc1");
    expect(output?.entry_id).toBe("tc1:result");
    const delegate = hits.find((h) => h.lane !== undefined);
    expect(delegate?.lane).toBe("sub-1");
    expect(delegate?.entry_id).toBe("a2");
    expect(delegate?.segment_kind).toBe("content");
    // Every OTHER hit carries no lane at all, which is the chat's own agent: the
    // optionality is load-bearing, and it is what routes a hit to another tab.
    expect(hits.filter((h) => h.lane !== undefined)).toHaveLength(1);
  });
});

describe("the excerpt radius the ranker compares against", () => {
  it("is the same number on both sides", () => {
    // Both sides slice the same radius around a hit before a Dice score with a similarity floor;
    // drift silently moves which occurrence a hit lands on.
    const go = declaredNumber(GO_SEARCH_PATH, /^const searchExcerptRadius = (\d+)$/m);
    const client = declaredNumber(CLIENT_FIND_PATH, /^const EXCERPT_RADIUS = (\d+);$/m);
    expect(client, "find-in-chat.ts EXCERPT_RADIUS must equal chat.searchExcerptRadius").toBe(go);
  });
});
