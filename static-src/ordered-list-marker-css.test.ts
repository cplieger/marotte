// An outside `::marker` paints in its list's start padding, and `.msg-row`'s `content-visibility:
// auto` clips anything past the row's edge, so a marker wider than that padding loses its leading
// digit. A pseudo-element has no box to read, so the marker is measured as a same-font probe
// carrying the UA `::marker` text styling.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { page } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { renderMarkdownInto } from "./markdown.js";

let style: HTMLStyleElement;
let row: HTMLElement;
let bubble: HTMLElement;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  row = document.createElement("div");
  row.className = "msg-row";
  bubble = document.createElement("div");
  bubble.className = "message assistant";
  row.append(bubble);
  document.body.append(row);
});

afterAll(async () => {
  row.remove();
  style.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

function render(md: string): void {
  bubble.replaceChildren();
  renderMarkdownInto(bubble, md);
}

const items = (count: number, start = 1): string =>
  Array.from({ length: count }, (_, i) => `${start + i}. item`).join("\n");

/** How far `li`'s marker overflows its own list's start edge, in px; 0 or less means it fits. */
function markerOverflow(li: HTMLLIElement, marker: string): number {
  const probe = document.createElement("span");
  probe.textContent = `${marker}. `;
  probe.style.cssText =
    "position:absolute;visibility:hidden;white-space:pre;font-variant-numeric:tabular-nums";
  li.append(probe);
  const width = probe.getBoundingClientRect().width;
  probe.remove();
  const list = li.parentElement!;
  return width - (li.getBoundingClientRect().left - list.getBoundingClientRect().left);
}

const lastItem = (list: Element): HTMLLIElement => list.querySelector(":scope > li:last-child")!;

describe.each([
  ["phone", 390, 844],
  ["desktop", 1440, 900],
] as const)("an ordered list's marker gutter at %s width", (_name, width, height) => {
  beforeAll(async () => {
    await page.viewport(width, height);
  });

  it.each([
    [9, "9"],
    [10, "10"],
    [100, "100"],
    [1000, "1000"],
  ])("fits the widest marker of a %i-item list", (count, widest) => {
    render(items(count));
    const ol = bubble.querySelector(":scope > ol")!;
    expect(ol.children).toHaveLength(count);
    expect(markerOverflow(lastItem(ol), widest)).toBeLessThanOrEqual(0);
  });

  it("fits a start= list whose markers cross into another digit", () => {
    render(items(10, 995));
    const ol = bubble.querySelector(":scope > ol")!;
    expect(ol.getAttribute("start")).toBe("995");
    expect(markerOverflow(lastItem(ol), "1004")).toBeLessThanOrEqual(0);
  });

  it("fits a one-item start= list with a three-digit marker", () => {
    render("100. only");
    const ol = bubble.querySelector(":scope > ol")!;
    expect(markerOverflow(lastItem(ol), "100")).toBeLessThanOrEqual(0);
  });

  it("fits a nested ordered list inside its parent item", () => {
    const nested = Array.from({ length: 12 }, (_, i) => `   ${i + 1}. sub`).join("\n");
    render(`1. parent\n${nested}\n2. sibling`);
    const inner = bubble.querySelector(":scope > ol > li > ol")!;
    expect(inner.children).toHaveLength(12);
    expect(markerOverflow(lastItem(inner), "12")).toBeLessThanOrEqual(0);
  });

  it("fits an ordered list nested in a bullet item", () => {
    const nested = Array.from({ length: 10 }, (_, i) => `  ${i + 1}. sub`).join("\n");
    render(`- bullet\n${nested}`);
    const inner = bubble.querySelector(":scope > ul > li > ol")!;
    expect(inner.children).toHaveLength(10);
    expect(markerOverflow(lastItem(inner), "10")).toBeLessThanOrEqual(0);
  });

  it("sizes a one-digit list nested in a three-digit list for its own marker", () => {
    render("100. parent\n     1. child\n101. sibling");
    const outer = bubble.querySelector(":scope > ol")!;
    const inner = bubble.querySelector(":scope > ol > li > ol")!;
    expect(outer.getAttribute("start")).toBe("100");
    expect(inner.children).toHaveLength(1);
    const em = Number.parseFloat(getComputedStyle(inner).fontSize);
    expect(Number.parseFloat(getComputedStyle(inner).paddingInlineStart)).toBeCloseTo(1.5 * em, 1);
  });

  it("keeps a single-digit list's gutter where it was", () => {
    render(items(9));
    const ol = bubble.querySelector(":scope > ol")!;
    const em = Number.parseFloat(getComputedStyle(ol).fontSize);
    expect(Number.parseFloat(getComputedStyle(ol).paddingInlineStart)).toBeCloseTo(1.5 * em, 1);
  });
});
