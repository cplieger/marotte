// The cross-language line-delta contract, from the fixture the Go side reads (internal/buffer/linediff_test.go).
// Both footers render the same component, so Go and diff.ts must agree. Add cases to the fixture, never one side.
import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { lineDelta } from "./diff.js";

interface DeltaCase {
  name: string;
  old: string;
  new: string;
  added: number;
  removed: number;
}

const FIXTURE_PATH = "../internal/buffer/testdata/line_delta.json";

describe("the line-delta contract shared with the Go implementation", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as { cases: DeltaCase[] };

  it("carries cases (an empty table would pass forever)", () => {
    expect(fx.cases.length).toBeGreaterThan(0);
  });

  it.each(fx.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    expect(lineDelta(c.old, c.new)).toEqual({ added: c.added, removed: c.removed });
  });
});
