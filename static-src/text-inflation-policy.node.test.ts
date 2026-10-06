// The root's text-inflation pin, guarded at the SOURCE (no engine here observes it): iOS Safari's
// `-webkit-text-size-adjust: auto` sized a diff's per-line scrollers differently. A `stylelint
// --fix` dropping the prefixed form, the only one Safari implements, is the likely regression.

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const reset = readFileSync(join(here, "css", "02-reset.css"), "utf8");

/** The `html { … }` block of the reset layer, without its comments. */
function rootRule(): string {
  const stripped = reset.replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /(^|\})\s*html\s*\{([^}]*)\}/m.exec(stripped);
  return m?.[2] ?? "";
}

describe("text inflation", () => {
  it("pins the PREFIXED property at the root, which is the only spelling Safari reads", () => {
    expect(rootRule()).toMatch(/-webkit-text-size-adjust:\s*100%/);
  });

  it("pins the standard property too, for the engines that implement it", () => {
    expect(rootRule()).toMatch(/(^|[\s;]) *text-size-adjust:\s*100%/);
  });

  it("declares it in exactly one place, so no surface can opt itself back in", () => {
    const declarations =
      reset.replace(/\/\*[\s\S]*?\*\//g, "").match(/text-size-adjust\s*:/g) ?? [];
    expect(declarations).toHaveLength(2);
  });

  it("never uses `none`, which also blocks the reader's own zoom-to-scale text", () => {
    expect(reset.replace(/\/\*[\s\S]*?\*\//g, "")).not.toMatch(/text-size-adjust:\s*none/);
  });
});
