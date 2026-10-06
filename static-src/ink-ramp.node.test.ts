// The ink ramp's contract: every text ink clears 4.5:1 on every surface text sits on, hovered ones included, in both
// themes. One table, since a per-consumer rule leaked. The script resolves tokens from the stylesheet, so a retune moves
// these numbers.

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");
const tokens = readFileSync(join(here, "css", "01-tokens.css"), "utf8");

const report = execFileSync("python3", [script, "ink"], { encoding: "utf8" });

function themeBlock(theme: "dark" | "light"): string {
  const start = report.indexOf(`  ${theme}:\n`);
  expect(start, `the ink report names the ${theme} theme`).toBeGreaterThan(-1);
  const rest = report.slice(start);
  const next = rest.indexOf("\n  light:\n", 1);
  return next === -1 ? rest : rest.slice(0, next);
}

describe.each(["dark", "light"] as const)("%s ink ramp", (theme) => {
  const block = themeBlock(theme);

  it("clears 4.5:1 for every text ink on every text-hosting surface", () => {
    // The verdict line is the aggregate; the `!` marks name the failing pair.
    expect(block).toContain("=> PASS");
    expect(block).not.toMatch(/=> FAIL/);
    expect(block.match(/\d!/g) ?? [], "no cell under the floor").toEqual([]);
  });

  it("keeps primary and secondary readable on the three two-level surfaces", () => {
    for (const surface of ["elevated", "hover(band)", "selected"]) {
      const line = block.split("\n").find((l) => l.trim().startsWith(surface));
      expect(line, `a row for ${surface}`).toBeDefined();
      expect(line).toMatch(/primary [\d.]+:1 PASS/);
      expect(line).toMatch(/secondary [\d.]+:1 PASS/);
    }
  });

  it("keeps three distinguishable levels", () => {
    // The floor the app accepts for "visibly quieter" (the selected row's muted ink, 1.35:1, css-contrast.py HIERARCHY).
    const m = /steps: hint->secondary ([\d.]+):1 {2}secondary->primary ([\d.]+):1/.exec(block);
    expect(m, "the steps line").not.toBeNull();
    expect(Number(m?.[1])).toBeGreaterThanOrEqual(1.35);
    expect(Number(m?.[2])).toBeGreaterThanOrEqual(1.35);
  });

  it("carries the hint ink's sRGB in the select chevron's data URI", () => {
    // `--img-chevron` is a background image, which cannot read a custom property, so its stroke is the hint ink copied as
    // hex; a retune of one without the other fails here.
    const hex = /hint sRGB ([0-9a-f]{6})/.exec(block)?.[1];
    expect(hex, "the report prints the hint hex").toBeDefined();
    // The rule with its brace: the bare selector also appears in the file's header comment.
    const lightRule = tokens.lastIndexOf(':root[data-theme="light"] {');
    expect(lightRule, "the light theme block").toBeGreaterThan(-1);
    const declared = theme === "dark" ? tokens.slice(0, lightRule) : tokens.slice(lightRule);
    // `[^\n]*?`, not `[^;]*`: the data URI carries its own `;`.
    const stroke = /--img-chevron:[^\n]*?stroke='%23([0-9a-f]{6})'/.exec(declared)?.[1];
    expect(stroke, `${theme} declares --img-chevron with a hex stroke`).toBeDefined();
    expect(stroke).toBe(hex);
  });
});
