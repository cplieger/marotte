import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

import { applyFocusZoomGuard, isIOSFamily } from "./focus-zoom.js";

// User agents as each device sends them. iPadOS 13+ Safari sends the desktop Mac string and is told
// apart only by its touch points.
const IPHONE = {
  userAgent:
    "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
  platform: "iPhone",
  maxTouchPoints: 5,
};
const IPAD = {
  userAgent:
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
  platform: "MacIntel",
  maxTouchPoints: 5,
};
const MAC = { ...IPAD, maxTouchPoints: 0 };
const ANDROID = {
  userAgent:
    "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Mobile Safari/537.36",
  platform: "Linux armv81",
  maxTouchPoints: 5,
};

/** The shipped page's head, parsed inertly. */
function shippedDoc(): Document {
  return new DOMParser().parseFromString(indexHtml, "text/html");
}

function viewport(doc: Document): string {
  return doc.querySelector<HTMLMetaElement>('meta[name="viewport"]')?.content ?? "";
}

describe("isIOSFamily", () => {
  it.each([
    ["an iPhone", IPHONE, true],
    ["an iPad reporting a Mac", IPAD, true],
    ["a Mac", MAC, false],
    ["an Android phone", ANDROID, false],
  ])("answers %s", (_l, nav, want) => {
    expect(isIOSFamily(nav)).toBe(want);
  });
});

describe("applyFocusZoomGuard", () => {
  it("ships a viewport meta with no scale limit, so Android keeps its pinch zoom", () => {
    expect(viewport(shippedDoc())).not.toMatch(/maximum-scale|user-scalable/);
  });

  it.each([
    ["an iPhone", IPHONE],
    ["an iPad", IPAD],
  ])("caps the scale on %s, keeping every clause the page wrote", (_l, nav) => {
    const doc = shippedDoc();
    const written = viewport(doc);
    applyFocusZoomGuard(doc, nav);
    expect(viewport(doc)).toBe(`${written}, maximum-scale=1`);
  });

  it.each([
    ["a Mac", MAC],
    ["an Android phone", ANDROID],
  ])("leaves the meta as written on %s", (_l, nav) => {
    const doc = shippedDoc();
    const written = viewport(doc);
    applyFocusZoomGuard(doc, nav);
    expect(viewport(doc)).toBe(written);
  });

  it("adds the clause once when it runs twice", () => {
    const doc = shippedDoc();
    applyFocusZoomGuard(doc, IPAD);
    const once = viewport(doc);
    applyFocusZoomGuard(doc, IPAD);
    expect(viewport(doc)).toBe(once);
  });

  it("does nothing on a page with no viewport meta", () => {
    const doc = new DOMParser().parseFromString("<head></head>", "text/html");
    expect(() => {
      applyFocusZoomGuard(doc, IPHONE);
    }).not.toThrow();
    expect(doc.head.innerHTML).toBe("");
  });
});
