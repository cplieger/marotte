// The root's text-inflation pin, guarded at the SOURCE: iOS Safari's default
// `-webkit-text-size-adjust: auto` scales each block by its own width, so a diff's
// `white-space: pre` lines in a scroller render at a different size per line, and no
// engine available here observes it (Chromium's autosizing is Android-only and off
// under `width=device-width`). The likely regression is a `stylelint --fix` of
// `property-no-vendor-prefix` rewriting the prefixed form away (Safari implements
// only that one); the synced config exempts this property by name, and this guards
// that exemption being narrowed.

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
