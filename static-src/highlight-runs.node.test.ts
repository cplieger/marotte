import { describe, it, expect } from "vitest";

import { RUN_CLASSES, highlightRuns } from "./highlight.js";

function runsOf(code: string, file: string): [string, string][] {
  const r = highlightRuns(code, file);
  if (r === null) {
    return [];
  }
  return Array.from(r.starts, (s, i) => [
    RUN_CLASSES[r.kinds[i] ?? 0] ?? "",
    code.slice(s, r.ends[i]),
  ]);
}

describe("highlightRuns", () => {
  it("gives each highlighted token its class and offsets, plain text omitted", () => {
    expect(runsOf("const x = 1; // hi", "/w/a.ts")).toEqual([
      ["keyword", "const"],
      ["punctuation", "="],
      ["number", "1"],
      ["punctuation", ";"],
      ["comment", "// hi"],
    ]);
  });

  // A token spanning rows keeps its class on every piece the renderer slices from it.
  it("keeps a block comment across lines as one run", () => {
    expect(runsOf("/* a\nb\nc */x", "/w/a.ts")).toEqual([["comment", "/* a\nb\nc */"]]);
  });

  it("answers null for a language with no highlighting", () => {
    expect(highlightRuns("plain", "/w/notes.txt")).toBeNull();
  });
});
