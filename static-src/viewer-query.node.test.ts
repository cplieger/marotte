import { describe, it, expect } from "vitest";

import { QUERY_INVALID, parseFindQuery } from "./viewer-query.js";

describe("parseFindQuery", () => {
  it.each([
    ["empty", "", { kind: "empty" }],
    ["at the boundary", "a".repeat(1024), { kind: "ok", text: "a".repeat(1024) }],
    ["one byte over", "a".repeat(1025), { kind: "invalid", message: QUERY_INVALID }],
    [
      "a 4-byte emoji ending at the boundary",
      "a".repeat(1020) + "😀",
      { kind: "ok", text: "a".repeat(1020) + "😀" },
    ],
    [
      "a 4-byte emoji crossing it",
      "a".repeat(1021) + "😀",
      { kind: "invalid", message: QUERY_INVALID },
    ],
    ["a line feed", "a\nb", { kind: "invalid", message: QUERY_INVALID }],
    ["a carriage return", "a\rb", { kind: "invalid", message: QUERY_INVALID }],
  ])("parses %s", (_name, raw, want) => {
    expect(parseFindQuery(raw)).toEqual(want);
  });
});
