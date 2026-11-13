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
    expect(shownWhileHidden()).toEqual([]);
  });

  it("keeps a closed pill card, hidden by script, out of layout and the Tab order", () => {
    // makeExpandable swaps the card's `hidden` class for the attribute at init.
    const cards = [...host.querySelectorAll<HTMLElement>(".pill-expand-content")];
    expect(cards.length).toBeGreaterThanOrEqual(3);
    for (const card of cards) {
      card.classList.remove("hidden");
      card.hidden = true;
    }
    expect(shownWhileHidden()).toEqual([]);
    const row = document.createElement("button");
    cards[0]!.appendChild(row);
    row.focus();
    expect(document.activeElement).not.toBe(row);
  });
});
