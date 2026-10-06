// An element the shipped page marks `hidden` computes `display: none` under the app's
// own stylesheet. The UA's `[hidden] { display: none }` loses to ANY author `display`
// on origin, so a class rule that sets `display: flex` silently shows a hidden
// element unless the sheet carries a `[hidden]` arm for it. Every `hidden` element in
// static/index.html is checked against the real assembled stylesheet.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

function address(el: Element): string {
  if (el.id !== "") {
    return `#${el.id}`;
  }
  const own = [el.tagName.toLowerCase(), ...el.classList].join(".");
  const anchor = el.parentElement?.closest("[id]");
  return anchor === null || anchor === undefined ? own : `#${anchor.id} ${own}`;
}

const parsed = new DOMParser().parseFromString(indexHtml, "text/html");
const hiddenCount = parsed.body.querySelectorAll("[hidden]").length;

let host: HTMLDivElement;
let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  host.innerHTML = parsed.body.innerHTML;
  document.body.appendChild(host);
});

afterAll(() => {
  host.remove();
  style.remove();
});

describe("the hidden attribute under the app's stylesheet", () => {
  // Shown today although hidden: `.page-section` sets `display: flex` and has no
  // `[hidden]` arm, so this empty section takes a flex gap in the General panel. A
  // known defect, recorded as a failing case so the fix flips it.
  const KNOWN_SHOWN = ["#general-governance-section"];

  function shownWhileHidden(): string[] {
    return [...host.querySelectorAll("[hidden]")]
      .filter((el) => getComputedStyle(el).display !== "none")
      .map((el) => address(el));
  }

  it("finds hidden elements on the shipped page", () => {
    expect.assertions(1);
    expect(hiddenCount).toBeGreaterThanOrEqual(5);
  });

  it("computes display none for every hidden element", () => {
    expect.assertions(1);
    expect(shownWhileHidden().filter((a) => !KNOWN_SHOWN.includes(a))).toEqual([]);
  });

  it.fails.each(KNOWN_SHOWN)("computes display none for %s", (addr) => {
    expect(shownWhileHidden()).not.toContain(addr);
  });
});
