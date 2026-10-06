// A drawing shared with the registry is spelled the way `svg()` spells it (menu-icons.test.ts owns which drawing a
// concept gets). Inline copies in static/index.html once lacked `stroke-linejoin="round"`, and the closed pencil
// rendered a miter spike. A byte guard is the cheap, stronger proxy for a raster one. Tier, attribute order and extra
// attributes are not checked.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import iconsSrc from "./icons.ts?raw";

/** `class` is excluded: the tier is the markup's call. */
const CANONICAL = [
  'viewBox="0 0 24 24"',
  'fill="none"',
  'stroke="currentColor"',
  'stroke-linecap="round"',
  'stroke-linejoin="round"',
] as const;

/** Whitespace runs only: inside path data whitespace separates coordinates. */
function norm(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

function inner(svg: string): string {
  return norm(svg.replace(/^<svg\b[^>]*>/, "").replace(/<\/svg>$/, ""));
}

/** Read off the source, so a `PATH_` const and a literal string are both in the population. */
function registryDrawings(src: string): Map<string, string[]> {
  const paths = new Map<string, string>();
  for (const m of src.matchAll(/const (PATH_\w+) =\s*'([^']*)'/g)) {
    paths.set(m[1] ?? "", m[2] ?? "");
  }
  const out = new Map<string, string[]>();
  const add = (drawing: string, name: string): void => {
    const key = norm(drawing);
    out.set(key, [...(out.get(key) ?? []), name]);
  };
  for (const m of src.matchAll(/export const (ICON_\w+) = svg\(\s*"\w+",\s*(PATH_\w+|'[^']*')/g)) {
    const arg = m[2] ?? "";
    add(arg.startsWith("PATH_") ? (paths.get(arg) ?? "") : arg.slice(1, -1), m[1] ?? "");
  }
  // `\s*(?:\+\s*)?`, not `\s*\+?\s*`: the latter splits a whitespace run between two `\s*` under the enclosing `+`,
  // which is exponential (CodeQL js/redos).
  for (const m of src.matchAll(/export const (ICON_\w+) =\s*((?:'[^']*'\s*(?:\+\s*)?)+);/g)) {
    const lit = [...(m[2] ?? "").matchAll(/'([^']*)'/g)].map((q) => q[1] ?? "").join("");
    if (lit.startsWith("<svg")) {
      add(inner(lit), m[1] ?? "");
    }
  }
  return out;
}

function sharedSVGs(): { line: number; attrs: string; owners: string[] }[] {
  const reg = registryDrawings(iconsSrc);
  const out: { line: number; attrs: string; owners: string[] }[] = [];
  for (const m of indexHtml.matchAll(/<svg\b([^>]*)>[\s\S]*?<\/svg>/g)) {
    const owners = reg.get(inner(m[0]));
    if (owners === undefined) {
      continue;
    }
    out.push({
      line: indexHtml.slice(0, m.index).split("\n").length,
      attrs: norm(m[1] ?? ""),
      owners,
    });
  }
  return out;
}

describe("a registry drawing hand-authored in index.html", () => {
  const shared = sharedSVGs();

  it("has a population to check, so the scan below cannot pass vacuously", () => {
    // The regexes read two real files; matching nothing would leave every assertion trivially green.
    expect(registryDrawings(iconsSrc).size).toBeGreaterThan(20);
    expect(shared.length).toBeGreaterThan(10);
  });

  for (const attr of CANONICAL) {
    it(`carries ${attr} at every site`, () => {
      const missing = shared
        .filter((s) => !s.attrs.includes(attr))
        .map((s) => `line ${String(s.line)} (${s.owners.join(", ")})`);
      expect(
        missing,
        `these hand-authored glyphs omit ${attr}, which svg() emits for the registry ` +
          `copy of the same drawing — on a drawing with joins that renders differently ` +
          `(PATH_PENCIL measured 770 differing pixels)`,
      ).toEqual([]);
    });
  }
});
