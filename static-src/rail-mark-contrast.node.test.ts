import { describe, it, expect } from "vitest";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { ruleContaining } from "./__test-helpers__/css-rules.js";

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, "..", "scripts", "css-contrast.py");

/** Read with `node:fs`, not the shared helper's `?raw` glob: in vitest's NODE project a
 *  `*.css?raw` import resolves to the EMPTY STRING, which would make every sweep here pass over
 *  nothing. Same reason `send-btn-contrast.node.test.ts` reads its own. */
const turns = readFileSync(join(here, "css", "29-turns.css"), "utf8");

interface Measurement {
  theme: string;
  ratio: number;
}

function pair(fg: string, bg: string): Measurement[] {
  const out = execFileSync("python3", [script, "pair", fg, bg], { encoding: "utf8" });
  const rows = out
    .trim()
    .split("\n")
    .map((line) => {
      const cols = line.split("\t");
      return { theme: cols[0] ?? "", ratio: Number(cols[3]) };
    });
  expect(
    rows.map((r) => r.theme),
    `expected both themes for ${fg} on ${bg}`,
  ).toEqual(["dark", "light"]);
  return rows;
}

/** One declaration's value out of a rule's body, comments stripped. */
function decl(body: string, prop: string): string {
  const m = new RegExp(`(?:^|[;{\\s])${prop}:\\s*([^;]+);`).exec(
    body.replace(/\/\*[\s\S]*?\*\//g, " "),
  );
  expect(m, `${prop} is declared`).not.toBeNull();
  return (m?.[1] ?? "").trim();
}

/** The surface the map hangs over: the page, in the gutter beside the cards. */
const PAGE = "var(--c-bg-primary)";

/** Each mark's ink, read off the rule that paints it, so a retune is measured as shipped. */
const MARKS: [string, () => string][] = [
  ["resting bar", () => decl(ruleContaining(turns, ".turn-pill").body, "--pill-ink")],
  ["in-view bar", () => decl(ruleContaining(turns, ".turn-pill[data-in-view]").body, "--pill-ink")],
  ["current bar", () => decl(ruleContaining(turns, ".turn-pill[data-current]").body, "--pill-ink")],
  [
    "running outline",
    () =>
      decl(
        ruleContaining(
          turns,
          '.turn-pill[data-severity="running"]:not([data-current]) > .turn-pill-link::before',
        ).body,
        "box-shadow",
      ).replace(/^inset 0 0 0 1px /u, ""),
  ],
  [
    "broken disc",
    () =>
      decl(
        ruleContaining(
          turns,
          '.turn-pill[data-severity="broken"] > .turn-pill-link > .turn-pill-mark',
        ).body,
        "background",
      ),
  ],
  [
    "stopped ring",
    () =>
      decl(
        ruleContaining(
          turns,
          '.turn-pill[data-severity="stopped"] > .turn-pill-link > .turn-pill-mark',
        ).body,
        "box-shadow",
      ).replace(/^inset 0 0 0 1\.5px /u, ""),
  ],
  [
    "search-hit square",
    () =>
      decl(
        ruleContaining(turns, ".turn-pill[data-hit] > .turn-pill-link > .turn-pill-hit").body,
        "background",
      ),
  ],
];

// The script lives outside static-src, and Stryker's sandbox copies static-src alone, so its
// absence is a skip, the same rule the sibling floors use.
describe.skipIf(!existsSync(script))("the turn map's marks, measured against the page", () => {
  // WCAG 1.4.11: each mark is a graphical object a reader has to pick out of the column.
  for (const [name, ink] of MARKS) {
    it(`holds the ${name} to 3:1 in both themes`, () => {
      const fg = ink();
      expect(fg).toMatch(/^var\(--c-[a-z-]+\)$/u);
      for (const m of pair(fg, PAGE)) {
        expect(m.ratio, `${m.theme}: ${fg} vs page`).toBeGreaterThanOrEqual(3.0);
      }
    });
  }
});
