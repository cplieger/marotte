// A clamp's line count is one fact in two languages: the TS constant `attachClamp` falls back against, and the CSS clip.
// Moving one side alone withdraws the opener from clipped text or leaves one over nothing. Source test: both are literals.

import { describe, it, expect } from "vitest";

import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import execPageSrc from "./exec-view/page.ts?raw";
import steerNoteSrc from "./fundamentals/steer-note.ts?raw";
import dockAskSrc from "./dock-ask.ts?raw";

/** `css-rules.ts` exports no sheet map, so the sweep globs its own. */
const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

const CLAMPED_RULE = /^(\.[a-z-]+\[data-clamped\])\s*\{/gm;

interface ClampPair {
  readonly what: string;
  readonly tsFile: string;
  readonly tsSrc: string;
  readonly constant: string;
  readonly sheet: string;
  readonly selector: string;
}

// Two CSS-only clamps are deliberately absent (no TS constant, no `[data-clamped]` rule): `.turn-req-text` and
// `.steer-text`. Their counts are asserted against layout in `disclosure-row-css.test.ts` and `pending-steers.test.ts`.
const PAIRS: readonly ClampPair[] = [
  {
    what: "the run page's instructions",
    tsFile: "exec-view/page.ts",
    tsSrc: execPageSrc,
    constant: "CLAMP",
    sheet: "31-exec-view.css",
    selector: ".ev-in-text[data-clamped]",
  },
  {
    what: "the run page's results",
    tsFile: "exec-view/page.ts",
    tsSrc: execPageSrc,
    constant: "RESULT_CLAMP",
    sheet: "31-exec-view.css",
    selector: ".ev-r-text[data-clamped]",
  },
  {
    what: "a steer note in the transcript",
    tsFile: "fundamentals/steer-note.ts",
    tsSrc: steerNoteSrc,
    constant: "CLAMP_LINES",
    sheet: "13-messages.css",
    selector: ".steer-note-text[data-clamped]",
  },
  {
    // One row for both dock ask cards: `dock-ask.ts` owns one constant for them.
    what: "a dock ask card's question",
    tsFile: "dock-ask.ts",
    tsSrc: dockAskSrc,
    constant: "CLAMP_LINES",
    sheet: "26-dock.css",
    selector: ".dock-ask-question[data-clamped]",
  },
];

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/^\s*\/\/.*$/gm, " ");
}

/** Matches both constant shapes (options object, bare count); comments are stripped because some docs quote the number. */
function tsClampLines(pair: ClampPair): number {
  const code = stripComments(pair.tsSrc);
  // `\s+CLAMP` cannot match inside `RESULT_CLAMP`, which keeps the two exec-view constants apart.
  const re = new RegExp(
    String.raw`\bconst\s+${pair.constant}\s*=\s*(?:\{[^{}]*?\blines\s*:\s*(\d+)|(\d+)\s*;)`,
  );
  const m = re.exec(code);
  expect(m, `${pair.tsFile}: no line count found for \`${pair.constant}\``).not.toBeNull();
  return Number.parseInt(m?.[1] ?? m?.[2] ?? "", 10);
}

/**
 * Every count a clamp rule declares in any spelling (`-webkit-line-clamp`, `line-clamp`, `max-block-size` in `lh`), so a
 * rule cannot half-move. The `max-block-size` count is read anywhere in the value: it is a `var(--clamp-h, …)` fallback.
 */
function cssClampCounts(pair: ClampPair): { prop: string; lines: number }[] {
  const body = ruleBody(loadCSS(pair.sheet), pair.selector).replace(/\/\*[\s\S]*?\*\//g, " ");
  return [
    // `(?:-webkit-)?` rather than an alternation, so `-webkit-line-clamp` cannot be
    // read as a bare `line-clamp`.
    ...body.matchAll(/((?:-webkit-)?line-clamp)\s*:\s*(\d+)/g),
    ...body.matchAll(/(max-block-size)\s*:[^;}]*?(\d+)lh/g),
  ].map((m) => ({ prop: m[1] ?? "", lines: Number.parseInt(m[2] ?? "", 10) }));
}

describe("a clamp's line count is one fact in two languages", () => {
  for (const pair of PAIRS) {
    it(`${pair.what}: the stylesheet clamps to the same count as ${pair.constant}`, () => {
      const tsLines = tsClampLines(pair);
      const declared = cssClampCounts(pair);
      expect(
        declared.length,
        `css/${pair.sheet} \`${pair.selector}\` declares no line count`,
      ).toBeGreaterThan(0);
      for (const found of declared) {
        expect(
          found.lines,
          `${pair.what}: ${pair.tsFile} \`${pair.constant}\` clamps to ${String(tsLines)} lines, ` +
            `but css/${pair.sheet} \`${pair.selector}\` declares ` +
            `${found.prop}: ${String(found.lines)}. One clamp is one count — move both or neither.`,
        ).toBe(tsLines);
      }
    });
  }

  // Premise: `toBe` reads NaN as equal to NaN, so a regex that stopped matching would agree with itself.
  it("reads a real count from both sides of every pair", () => {
    for (const pair of PAIRS) {
      const tsLines = tsClampLines(pair);
      expect(
        Number.isInteger(tsLines) && tsLines > 0,
        `${pair.tsFile} \`${pair.constant}\` read as ${String(tsLines)}`,
      ).toBe(true);
      for (const found of cssClampCounts(pair)) {
        expect(
          Number.isInteger(found.lines) && found.lines > 0,
          `css/${pair.sheet} \`${pair.selector}\` ${found.prop} read as ${String(found.lines)}`,
        ).toBe(true);
      }
    }
  });

  // Exhaustiveness: a clamp rule with no row fails here.
  it("covers every clamp rule in the shipped stylesheets", () => {
    const declared = new Set<string>();
    for (const css of Object.values(sheets)) {
      for (const m of css.replace(/\/\*[\s\S]*?\*\//g, " ").matchAll(CLAMPED_RULE)) {
        declared.add(m[1] ?? "");
      }
    }
    expect([...declared].sort(), "a new clamp site needs a row in this file's table").toEqual(
      [...new Set(PAIRS.map((p) => p.selector))].sort(),
    );
  });
});
