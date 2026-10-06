// Raw code inside PROSE is never turned into a path badge, while the prose around it still is.

import { describe, it, expect } from "vitest";
import { renderMarkdownInto, createMarkdownStream } from "./markdown.js";

function host(): HTMLDivElement {
  return document.createElement("div");
}

function links(root: HTMLElement): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>("button.inline-file-link")];
}

/** A fence NESTED IN A LIST ITEM: the shape where the linkify pass genuinely walks over code,
 *  because the completed top-level block is the <ul> and the <pre><code> is one of its
 *  descendants. A TOP-LEVEL fence cannot show this — `decorate` returns from its PRE branch
 *  before the walk is reached — so the walker's own skip is only observable one level in. */
const LIST_FENCE = "- see src/outer.ts\n  ```go\n  internal/agent/auth.go\n  ```\n\ncloser\n";

/** The same shape one construct in: a code SPAN inside a list item's prose. */
const LIST_INLINE_CODE = "- see src/outer.ts and `internal/agent/auth.go` inline\n\ncloser\n";

describe("code inside prose is never linkified", () => {
  it("leaves a top-level fenced block alone (replay path)", () => {
    const h = host();
    renderMarkdownInto(
      h,
      "prose naming src/outer.ts\n\n```go\nsee internal/agent/auth.go now\n```\n\ncloser\n",
    );
    const pre = h.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(links(pre!)).toHaveLength(0);
    expect(pre!.textContent).toContain("internal/agent/auth.go");
    expect(links(h).map((b) => b.title)).toEqual(["src/outer.ts"]);
  });

  it("leaves a fence inside a list item alone, and still links the item's prose", () => {
    const h = host();
    renderMarkdownInto(h, LIST_FENCE);
    const pre = h.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(links(pre!)).toHaveLength(0);
    expect(pre!.textContent).toContain("internal/agent/auth.go");
    expect(links(h).map((b) => b.title)).toEqual(["src/outer.ts"]);
  });

  it("leaves an inline `code` span alone, and still links the prose around it", () => {
    const h = host();
    renderMarkdownInto(h, LIST_INLINE_CODE);
    const code = h.querySelector("code");
    expect(code).not.toBeNull();
    expect(links(code!)).toHaveLength(0);
    expect(code!.textContent).toBe("internal/agent/auth.go");
    expect(links(h).map((b) => b.title)).toEqual(["src/outer.ts"]);
  });

  it("leaves a fence inside a list item alone on the STREAMING path", () => {
    // Pins the precondition the replay cases cannot reach: while text streams, code text must stay
    // a DIRECT child of its <code> rather than being wrapped in a per-chunk fade span, which would
    // route it past linkify's parent test.
    const h = host();
    const r = createMarkdownStream(h, { flushIntervalMs: 0 });
    r.writeDelta(LIST_FENCE);
    r.end(); // drains synchronously, so no timer and no await
    const pre = h.querySelector("pre");
    expect(pre).not.toBeNull();
    expect(links(pre!)).toHaveLength(0);
    expect(pre!.textContent).toContain("internal/agent/auth.go");
    expect(links(h).map((b) => b.title)).toEqual(["src/outer.ts"]);
  });
});
