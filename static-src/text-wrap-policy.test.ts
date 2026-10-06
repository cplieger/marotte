// The app asks for NO `text-wrap: pretty`: it revises breaks in lines already on screen as text
// streams in, while `auto` only appends after a chosen break. A SOURCE guard, since a rendered
// check covers only what a test page mounts. `balance` on static headings stays legal.

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
    // No form-control carve-out either (Safari 26 re-wraps whole paragraphs per keystroke): swept over
    // every form-control selector in every sheet, so the guard is tied to no single rule.
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
    // Not a blanket blessing: these existed, all on short static text. A new one needs its own reason.
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
