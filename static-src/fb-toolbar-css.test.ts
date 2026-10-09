// The file browser's path field never renders under 12rem beside its buttons: the toolbar moves it
// to its own row exactly when one row would squeeze it below that. Measured over the shipped markup
// and stylesheet with the sidebar docked, because the bar's width is what the viewport leaves it.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** 12rem at the 16px root. */
const FLOOR = 192;

const TIERS = [["fine"], ["coarse"]] as const;

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
});

afterAll(async () => {
  style.remove();
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  document.body.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

/** The shipped page with the file browser as the one visible view. */
function mountPage(pointer: "fine" | "coarse"): void {
  document.documentElement.setAttribute("data-pointer", pointer);
  const body = indexHtml.slice(indexHtml.indexOf("<body"), indexHtml.indexOf("</body>"));
  document.body.innerHTML = body.slice(body.indexOf(">") + 1);
  for (const view of document.querySelectorAll<HTMLElement>("[data-tab-view]")) {
    view.classList.toggle("hidden", view.id !== "files-view");
  }
}

async function at(width: number, height: number): Promise<void> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
}

function rect(id: string): DOMRect {
  const el = document.getElementById(id);
  expect(el, `#${id} is not in the shipped markup`).not.toBeNull();
  return (el as HTMLElement).getBoundingClientRect();
}

describe("the file browser toolbar", () => {
  it.each(TIERS)("keeps the path 12rem wide on an 820px tablet, %s pointer", async (tier) => {
    await at(820, 1180);
    mountPage(tier);
    expect(rect("fb-path").width).toBeGreaterThanOrEqual(FLOOR);
  });

  it.each(TIERS)("keeps one row where it fits, at 1180px, %s pointer", async (tier) => {
    await at(1180, 820);
    mountPage(tier);
    const path = rect("fb-path");
    expect(path.top).toBe(rect("fb-back").top);
    expect(path.top).toBe(rect("fb-delete").top);
    expect(path.left).toBeGreaterThan(rect("fb-forward").right);
  });

  it.each(TIERS)("wraps the path only when one row would squeeze it, %s pointer", async (tier) => {
    // The view's width is set rather than the viewport's, so each step is a synchronous layout
    // and not a resize round trip. The one-row path grows 1px per px of bar, so the narrowest
    // one-row bar must leave it within one 2px step of the floor, or the bar wrapped too early.
    await at(1180, 820);
    mountPage(tier);
    const view = document.getElementById("files-view") as HTMLElement;
    const rows: { width: number; ownRow: boolean; path: number }[] = [];
    for (let width = 520; width <= 760; width += 2) {
      view.style.width = `${String(width)}px`;
      const path = rect("fb-path");
      rows.push({ width, ownRow: path.bottom <= rect("fb-back").top, path: path.width });
    }
    const oneRow = rows.filter((r) => !r.ownRow);
    expect(
      rows.some((r) => r.ownRow),
      "the sweep never reached a wrapped bar",
    ).toBe(true);
    expect(oneRow.length, "the sweep never reached a one-row bar").toBeGreaterThan(0);
    expect(oneRow.filter((r) => r.path < FLOOR)).toEqual([]);
    expect(Math.min(...oneRow.map((r) => r.path))).toBeLessThan(FLOOR + 2);
  });
});
