// A code block's bar buttons (Run, and a diagram's source, download and expand controls) join the
// library's Copy as one family (@cplieger/ui's markdown docs, "The code block"): the same box,
// nothing painted at rest, and none of them overflowing the bar.
import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { setShellRunCallback } from "./code-blocks.js";
import { releaseMarkdownIn, renderMarkdownInto } from "./markdown.js";

let style: HTMLStyleElement;
let host: HTMLElement;

function mount(): { bar: HTMLElement; copy: HTMLButtonElement; run: HTMLButtonElement } {
  releaseMarkdownIn(host);
  host.replaceChildren();
  renderMarkdownInto(host, "```bash\necho hi\n```\n");
  const bar = host.querySelector<HTMLElement>('.cp-code > [data-part="bar"]');
  const copy = host.querySelector<HTMLButtonElement>(".cp-copy");
  const run = host.querySelector<HTMLButtonElement>('[aria-label="Run in shell"]');
  if (bar === null || copy === null || run === null) {
    throw new Error("the fence rendered no bar with Copy and Run");
  }
  return { bar, copy, run };
}

beforeAll(() => {
  setShellRunCallback(vi.fn());
  style = mountAppCSS();
  host = document.createElement("div");
  host.className = "message assistant";
  host.style.cssText = "position:fixed;top:0;left:0;width:774px;";
  document.body.appendChild(host);
});

afterEach(() => {
  document.documentElement.removeAttribute("data-pointer");
});

afterAll(() => {
  releaseMarkdownIn(host);
  style.remove();
  host.remove();
});

describe.each(["fine", "coarse"])("a code block's bar button on a %s pointer", (tier) => {
  it("paints what Copy paints at rest", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { copy, run } = mount();
    const [a, b] = [getComputedStyle(run), getComputedStyle(copy)];
    expect(a.backgroundColor).toBe(b.backgroundColor);
    expect(a.borderTopWidth).toBe(b.borderTopWidth);
    expect(a.borderTopColor).toBe(b.borderTopColor);
    expect(a.borderRadius).toBe(b.borderRadius);
  });

  it("is as tall as Copy and sits on its row", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { copy, run } = mount();
    const [a, b] = [run.getBoundingClientRect(), copy.getBoundingClientRect()];
    expect(a.height).toBe(b.height);
    expect(a.top).toBe(b.top);
  });

  it("stays inside the bar", () => {
    document.documentElement.setAttribute("data-pointer", tier);
    const { bar, copy, run } = mount();
    const row = bar.getBoundingClientRect();
    for (const btn of [copy, run]) {
      const box = btn.getBoundingClientRect();
      expect(box.top, btn.getAttribute("aria-label") ?? "Copy").toBeGreaterThanOrEqual(row.top);
      expect(box.bottom).toBeLessThanOrEqual(row.bottom);
      expect(box.right).toBeLessThanOrEqual(row.right);
    }
  });
});
