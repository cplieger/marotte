// One owner for the run's progress wording (`model.ts` `progressText`, `passText` and the pass
// counts): a second "step N of M", "pass N" or "N passes" literal elsewhere is a copy that drifts.

import { describe, it, expect } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const ROOT = join(import.meta.dirname, "..");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (name === "node_modules" || name.startsWith(".")) {
      return [];
    }
    if (statSync(path).isDirectory()) {
      return sources(path);
    }
    return name.endsWith(".ts") && !name.endsWith(".test.ts") ? [path] : [];
  });
}

describe("the run's progress wording", () => {
  for (const [what, literal] of [
    ["step count", /`step \$\{[^}`]*\} of /],
    ["loop pass", /`pass \$\{/],
    ["pass count", /\$\{[^}`]*\} passes\b/],
  ] as const) {
    it(`spells the ${what} only in exec-view/model.ts`, () => {
      const spelled = sources(ROOT).filter((path) => literal.test(readFileSync(path, "utf8")));
      expect(spelled.map((p) => p.slice(ROOT.length + 1))).toEqual(["exec-view/model.ts"]);
    });
  }
});
