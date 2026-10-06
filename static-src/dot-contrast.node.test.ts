// Runs `scripts/css-contrast.py dot`: the tab-strip marks and run-bar glyph against WCAG 1.4.11's 3:1, and each row
// as a WCAG 1.4.1 population (states differ on a non-colour channel).

import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** Named, not discovered, so a script that stops printing a cluster fails. */
const CLUSTERS = [
  "chat row, motion available",
  "run row, motion available",
  "chat row, prefers-reduced-motion",
  "run row, prefers-reduced-motion",
] as const;

// Stryker's sandbox copies static-src alone and the script lives outside it, so its absence is a skip.
describe.skipIf(!existsSync(script))("the dot cluster's contrast gate passes", () => {
  const out = execFileSync("python3", [script, "dot"], { encoding: "utf8" });

  it("separates every pair on each row without relying on hue", () => {
    // Each verdict asserted by name: "no FAIL anywhere" also passes when the sweep stops running.
    for (const cluster of CLUSTERS) {
      const line = out.split("\n").find((l) => l.trim().startsWith(cluster));
      expect(line, `${cluster} has no verdict`).toBeDefined();
      expect(line, cluster).toContain("PASS");
    }
  });

  it("holds every state to the 3:1 graphical-object floor on every fill", () => {
    // Counting verdict columns says the table was populated.
    const verdicts = out.split("\n").filter((l) => /^ {4}\w+\s+(var\(--|--)/.test(l));
    expect(verdicts.length, "the floor table printed no state rows").toBeGreaterThan(8);
    for (const line of verdicts) {
      expect(line.trim(), "state row under the floor").toContain("PASS");
    }
  });

  it("reports no failure of any kind", () => {
    // Last: covers the remaining verdicts, while the cases above rule out an empty run.
    const fails = out.split("\n").filter((l) => l.includes("FAIL"));
    expect(fails, fails.join("\n")).toEqual([]);
  });
});
