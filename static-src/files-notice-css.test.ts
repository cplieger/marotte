// A synthetic hover drives no style recalc, so the shipped rules are read from the CSSOM and matched with `:hover`
// stripped, which answers "would this rule apply under the pointer".

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { listNotice } from "./files-shared.js";

function hoverSelectors(rules: CSSRuleList, parent = ""): string[] {
  const out: string[] = [];
  for (const rule of rules) {
    let scope = parent;
    if (rule instanceof CSSStyleRule) {
      const own = rule.selectorText;
      scope = parent === "" ? own : own.replaceAll("&", `:is(${parent})`);
      if (!own.includes("&") && parent !== "") {
        scope = `:is(${parent}) ${own}`;
      }
      if (scope.includes(":hover")) {
        out.push(scope);
      }
    }
    if ("cssRules" in rule) {
      out.push(...hoverSelectors((rule as CSSGroupingRule).cssRules, scope));
    }
  }
  return out;
}

function hoverMatches(selectors: readonly string[], el: Element): string[] {
  return selectors.filter((sel) => {
    try {
      return el.matches(sel.replaceAll(":hover", ""));
    } catch {
      // A pseudo-element or an emptied `:not()` names no element.
      return false;
    }
  });
}

describe("the listing notice's hover", () => {
  let style: HTMLStyleElement;
  let list: HTMLDivElement;
  let selectors: string[];

  beforeAll(() => {
    style = mountAppCSS();
    const sheet = style.sheet;
    if (sheet === null) {
      throw new Error("the app stylesheet did not parse");
    }
    selectors = hoverSelectors(sheet.cssRules);
    list = document.createElement("div");
    list.id = "fb-list";
    list.className = "fb-list";
    document.body.appendChild(list);
  });

  afterAll(() => {
    list.remove();
    style.remove();
  });

  it("matches no hover rule, while a file row does", () => {
    const notice = listNotice("not found", [{ label: "Retry", run: () => undefined }]);
    const row = document.createElement("div");
    row.className = "fb-row";
    list.replaceChildren(notice, row);
    expect(hoverMatches(selectors, notice)).toEqual([]);
    expect(hoverMatches(selectors, row).length, "positive control").toBeGreaterThan(0);
  });

  it("paints no background of its own", () => {
    const notice = listNotice("not found");
    list.replaceChildren(notice);
    expect(getComputedStyle(notice).backgroundColor).toBe("rgba(0, 0, 0, 0)");
  });

  it("leaves its buttons their own hover", () => {
    const notice = listNotice("not found", [{ label: "Retry", run: () => undefined }]);
    list.replaceChildren(notice);
    const btn = notice.querySelector("button");
    expect(btn).not.toBeNull();
    expect(hoverMatches(selectors, btn as Element).length).toBeGreaterThan(0);
  });
});
