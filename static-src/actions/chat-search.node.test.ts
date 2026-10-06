// Cross-language pin: Go's TestSearchAllWireContract writes the fixture from a real SearchAll, and
// this decodes it through the generated decodeSearchAllResult that `searchChats` runs.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeSearchAllResult } from "../wire/decoders.gen.js";

const FIXTURE_PATH = "../../internal/chat/testdata/search_all.json";

interface SearchAllFixture {
  query: string;
  result: unknown;
}

function loadFixture(): SearchAllFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as SearchAllFixture;
}

describe("the cross-chat search reply shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes through the generated decoder", () => {
    expect(() => decodeSearchAllResult(fx.result)).not.toThrow();
  });

  it("ranks the title-and-body match above the title-only one and counts every match", () => {
    const r = decodeSearchAllResult(fx.result);
    expect(r.matches.map((m) => m.id)).toEqual(["chat-001", "chat-003"]);
    expect(r.matched).toBe(r.matches.length);
    expect(r.scanned).toBe(3);
    expect(r.truncated).toBe(false);
  });

  it("carries a best hit only where the transcript holds a line to show", () => {
    const r = decodeSearchAllResult(fx.result);
    const [withLine, titleOnly] = r.matches;
    // The hit's TURN and ENTRY are equal here because the match is the turn's prompt (`turn_open`).
    expect(withLine?.best).toMatchObject({
      turn_id: "chat-001-t1",
      entry_id: "chat-001-t1",
      segment_kind: "prompt",
      offset: 22,
      segment_len: 33,
    });
    expect(withLine?.hits).toBe(3);
    // A title-only match has no `best`: a zero hit's empty segment kind fails the registered enum.
    expect(titleOnly?.best).toBeUndefined();
    expect(titleOnly?.hits).toBe(0);
  });

  it("refuses a title-only match spelled as a zero hit", () => {
    const r = fx.result as { matches: Record<string, unknown>[] };
    // Every other field is valid, so the refusal is attributable to the empty `segment_kind` alone.
    const zeroHit = {
      turn_id: "chat-003-t1",
      entry_id: "chat-003-t1",
      excerpt: "",
      segment_kind: "",
      turn: 0,
      offset: 0,
      segment_len: 0,
    };
    const forged = { ...r, matches: [{ ...r.matches[1], best: zeroHit }] };
    expect(() => decodeSearchAllResult(forged)).toThrow(/segment_kind/);
    // Control: with a real segment kind the same hit decodes.
    const named = {
      ...r,
      matches: [{ ...r.matches[1], best: { ...zeroHit, segment_kind: "prompt" } }],
    };
    expect(() => decodeSearchAllResult(named)).not.toThrow();
  });
});
