import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

// A pre-paint script blocks first paint, so what it imports must load alone.
describe("theme.ts", () => {
  it("imports nothing, so a pre-paint script loads it alone", async () => {
    const source = await readFile(join(import.meta.dirname, "theme.ts"), "utf8");
    expect(source).not.toMatch(/^\s*import[\s{*"']/m);
    expect(source).not.toMatch(/\bimport\(/);
  });
});
