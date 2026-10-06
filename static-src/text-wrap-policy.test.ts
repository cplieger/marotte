// The app asks for NO `text-wrap: pretty`, anywhere. `pretty` optimizes a
// paragraph's closing lines, so while text is appended it revises breaks in lines
// the reader is already looking at; `auto` is greedy and a chosen break only ever
// has text added after it. Measured in Chromium 151 over 1,408 appends: `pretty`
// moved an on-screen break on 40 and a settled line's on 7, `auto` on none. A SOURCE
// guard, because the fact is that no stylesheet carries the declaration. `balance`
// stays legal (static headings, no streaming exposure).

import { describe, it, expect } from "vitest";
import { allRules, loadCSS } from "./__test-helpers__/css-rules.js";
import manifest from "./css/MANIFEST?raw";

/** Every sheet the bundle concatenates, by name, excluding the vendored base
 *  (ui-primitives is a dependency, so its declarations are not ours to police). */
function ownSheets(): string[] {
  return manifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#") && !l.startsWith("../"));
}

describe("text-wrap policy", () => {
  it("declares `pretty` in no stylesheet, under either spelling", () => {
    const offenders: string[] = [];
    for (const name of ownSheets()) {
      for (const rule of allRules(loadCSS(name))) {
        // The shorthand and the longhand both reach `text-wrap-style`.
        if (/text-wrap(-style)?\s*:\s*pretty/.test(rule.body)) {
          offenders.push(`${name} { ${rule.selector} }`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("needs no carve-out on form controls, because nothing sets the style", () => {
    // The reset used to spend `text-wrap: wrap` on `:where(input, textarea,
    // select)` purely to undo the body rule — Safari 26 re-runs whole-paragraph
    // optimization on every keystroke, so a draft visibly re-wrapped while
    // typing. With the body rule gone there is nothing to undo, and a reset that
    // resets a default is the line that rots.
    //
    // A SWEEP over every form-control selector rather than a lookup of one rule.
    // It used to key on `:where(input, textarea, select)` and assert that rule
    // existed, which tied this guard to whatever ELSE that rule happened to
    // carry: the anchor was a `caret-color` declaration with nothing to do with
    // text-wrap, so deleting that unrelated line took the guard's subject with
    // it. Sweeping also covers a carve-out re-added under a different selector or
    // in a different sheet, which the lookup could not see.
    const offenders: string[] = [];
    for (const name of ownSheets()) {
      for (const rule of allRules(loadCSS(name))) {
        if (!/\b(input|textarea|select)\b/.test(rule.selector)) {
          continue;
        }
        if (/text-wrap/.test(rule.body)) {
          offenders.push(`${name} { ${rule.selector} }`);
        }
      }
    }
    expect(offenders, "no form-control rule sets text-wrap").toEqual([]);
  });

  it("leaves `balance` alone", () => {
    // Not a blanket blessing: these are the declarations that existed, all on
    // short static text. A new one wants its own reasoning, not this test's.
    //
    // Was three. `.page-title` was the fourth-to-last and went with the title bar:
    // every view's heading is the bar's `<h1>` now, which is a single nowrap line
    // that ellipsizes, so it has nothing to balance.
    const found: string[] = [];
    for (const name of ownSheets()) {
      for (const rule of allRules(loadCSS(name))) {
        if (/text-wrap(-style)?\s*:\s*balance/.test(rule.body)) {
          found.push(name);
        }
      }
    }
    expect(found.length).toBe(2);
  });
});
