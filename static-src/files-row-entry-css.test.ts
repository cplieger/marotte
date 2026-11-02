// A listing's rows fade in staggered, but a row files.ts marks `data-no-enter` does not:
// a large folder would otherwise start thousands of entry animations at once.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FB_NO_ENTER_ATTR } from "./files-shared.js";

const ROWS = 130;
const FIRST_MARKED = 100;

let sheet: HTMLStyleElement;
let list: HTMLDivElement;
let rows: HTMLElement[];

beforeAll(() => {
  sheet = mountAppCSS();
  list = document.createElement("div");
  rows = Array.from({ length: ROWS }, (_, i) => {
    const el = document.createElement("div");
    el.className = "fb-row";
    if (i >= FIRST_MARKED) {
      el.setAttribute(FB_NO_ENTER_ATTR, "");
    }
    list.appendChild(el);
    return el;
  });
  document.body.appendChild(list);
});

afterAll(() => {
  sheet.remove();
  list.remove();
});

describe("file browser row entry animation", () => {
  it("animates every unmarked row", () => {
    const missing = rows
      .slice(0, FIRST_MARKED)
      .map((el, i) => (el.getAnimations().length === 1 ? 0 : i + 1))
      .filter((n) => n > 0);
    expect(missing, "rows with no entry animation").toEqual([]);
  });

  it("runs no animation on a marked row", () => {
    const animated = rows
      .slice(FIRST_MARKED)
      .map((el, i) => (el.getAnimations().length === 0 ? 0 : FIRST_MARKED + i + 1))
      .filter((n) => n > 0);
    expect(animated, "rows still animating").toEqual([]);
  });

  it("keeps the stagger: 30ms per row, capped at the ninth", () => {
    const delays = [0, 1, 8, 9, FIRST_MARKED - 1].map(
      (i) => getComputedStyle(rows[i]!).animationDelay,
    );
    expect(delays).toEqual(["0s", "0.03s", "0.24s", "0.24s", "0.24s"]);
  });

  it("starts nothing on a marked row that a removal above moves to the hundredth place", () => {
    rows[0]!.remove();
    const shifted = rows[FIRST_MARKED]!;
    expect(list.children[FIRST_MARKED - 1]).toBe(shifted);
    expect(shifted.getAnimations()).toEqual([]);
  });
});
