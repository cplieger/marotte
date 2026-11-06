// The confirm button's label against its fill, measured by scripts/css-contrast.py at rest and on hover.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { ruleBody } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** The destructive fill's resting and hover backgrounds, read from the rule that paints them. */
function destructiveFills(): string[] {
  const body = ruleBody(
    readFileSync(join(here, "css", "04-uip-skin.css"), "utf8"),
    ".uip-ask-ok.is-destructive",
  );
  const hover = /&:hover\s*\{([^}]*)\}/.exec(body)?.[1] ?? "";
  const resting = body.replace(/&:hover\s*\{[^}]*\}/, "");
  return [resting, hover].map((block) => {
    const fill = /background:\s*([^;]+);/.exec(block)?.[1];
    expect(
      fill,
      "the destructive confirm declares a background at rest and on hover",
    ).toBeDefined();
    return fill!.trim();
  });
}

/** Each theme's ratio for `fg` on `bg`. */
function ratios(fg: string, bg: string): { theme: string; ratio: number }[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  return out
    .trim()
    .split("\n")
    .map((line) => {
      const [theme = "", , , ratio = ""] = line.split("\t");
      return { theme, ratio: Number(ratio) };
    });
}

// Stryker's sandbox copies static-src alone, so a missing script is a skip.
describe.skipIf(!existsSync(script))("the confirm button's label contrast", () => {
  it("holds --c-on-accent to 4.5:1 on every confirm fill, at rest and on hover, in both themes", () => {
    const fills = ["--c-accent", "--c-accent-hover", ...destructiveFills()];
    const misses: string[] = [];
    for (const fill of fills) {
      const measured = ratios("--c-on-accent", fill);
      expect(measured.map((m) => m.theme)).toEqual(["dark", "light"]);
      for (const m of measured) {
        if (!(m.ratio >= 4.5)) {
          misses.push(`${m.theme} ${fill}: ${String(m.ratio)}:1`);
        }
      }
    }
    expect(misses, "WCAG 1.4.3 asks 4.5:1 of the small label on its fill").toEqual([]);
  });
});
