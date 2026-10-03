// The pre-paint script only works as a BLOCKING classic script ahead of the
// stylesheet, and the CSP (script-src 'self') refuses any inline script, so
// both are properties of the shipped index.html rather than of any module.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

/** An inert parse: a DOMParser document fetches no subresource. */
function parsed(): Document {
  return new DOMParser().parseFromString(indexHtml, "text/html");
}

describe("index.html loads /prepaint.js before first paint", () => {
  it("as exactly one blocking classic script in <head>", () => {
    const tags = parsed().querySelectorAll('script[src="/prepaint.js"]');
    expect(tags).toHaveLength(1);
    const tag = tags[0];
    expect(tag?.parentElement?.tagName).toBe("HEAD");
    for (const attr of ["defer", "async", "type"]) {
      expect(tag?.hasAttribute(attr), attr).toBe(false);
    }
  });

  it("ahead of the stylesheet", () => {
    const doc = parsed();
    const script = doc.querySelector('script[src="/prepaint.js"]');
    const sheet = doc.querySelector('link[rel="stylesheet"][href="/style.css"]');
    expect(script).not.toBeNull();
    expect(sheet).not.toBeNull();
    if (script === null || sheet === null) {
      return;
    }
    expect(script.compareDocumentPosition(sheet) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("with no inline script anywhere in the page", () => {
    const doc = parsed();
    expect(doc.querySelectorAll("script:not([src])")).toHaveLength(0);
    expect(doc.querySelector("[data-theme-init]")).toBeNull();
  });
});
