// The bottom-bar path boxes (the editor's file path, the web preview's page path) under a path too
// long to fit: the file name is in view at rest, and a swipe reaches the rest of the path.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { page } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import indexHtml from "../static/index.html?raw";

const LONG_PATH = `${"very-long-folder/".repeat(12)}recap-for-deadset-2.md`;
const BOXES = ["editor-filename", "web-path"] as const;

let style: HTMLStyleElement;
let host: HTMLElement;
let entry: { readonly width: number; readonly height: number } | null = null;

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
  host = document.createElement("div");
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const id of ["editor-view", "web-view"]) {
    const view = doc.getElementById(id)!;
    view.classList.remove("hidden");
    host.append(document.importNode(view, true));
  }
  document.body.append(host);
  await page.viewport(390, 844);
  document.documentElement.dataset["pointer"] = "coarse";
});

afterEach(() => {
  for (const id of BOXES) {
    box(id).scrollLeft = 0;
  }
});

afterAll(async () => {
  host.remove();
  style.remove();
  document.body.style.margin = "";
  delete document.documentElement.dataset["pointer"];
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

function box(id: (typeof BOXES)[number]): HTMLElement {
  return host.querySelector<HTMLElement>(`#${id}`)!;
}

/** Write the path the way the views do: one `<bdi>`, so the RTL box keeps the path's own order. */
function show(id: (typeof BOXES)[number], path: string): HTMLElement {
  const bdi = document.createElement("bdi");
  bdi.textContent = path;
  box(id).replaceChildren(bdi);
  return bdi;
}

const inner = (b: HTMLElement): { left: number; right: number } => {
  const r = b.getBoundingClientRect();
  const cs = getComputedStyle(b);
  const px = (v: string): number => Number.parseFloat(v);
  return {
    left: r.left + px(cs.borderLeftWidth) + px(cs.paddingLeft),
    right: r.right - px(cs.borderRightWidth) - px(cs.paddingRight),
  };
};

describe.each(BOXES)("the #%s path box on a phone", (id) => {
  it("keeps the file name in view at rest", () => {
    const text = show(id, LONG_PATH);
    expect(box(id).scrollWidth).toBeGreaterThan(box(id).clientWidth);
    expect(text.getBoundingClientRect().right).toBeCloseTo(inner(box(id)).right, 0);
  });

  it("scrolls on its own inline axis, so a swipe reaches the start of the path", () => {
    const text = show(id, LONG_PATH);
    expect(["auto", "scroll"]).toContain(getComputedStyle(box(id)).overflowX);
    box(id).scrollLeft = -box(id).scrollWidth;
    // Within a pixel: a scroll offset snaps to whole device pixels.
    expect(Math.abs(text.getBoundingClientRect().left - inner(box(id)).left)).toBeLessThanOrEqual(
      1,
    );
  });

  it("does not scroll vertically", () => {
    show(id, LONG_PATH);
    expect(box(id).scrollHeight).toBeLessThanOrEqual(box(id).clientHeight);
  });

  it("lets the reader select the path to copy it", () => {
    show(id, LONG_PATH);
    expect(getComputedStyle(box(id)).userSelect).not.toBe("none");
  });

  it("starts a path that fits at its left edge", () => {
    const text = show(id, "demo/index.html");
    expect(text.getBoundingClientRect().left).toBeCloseTo(inner(box(id)).left, 0);
  });
});
