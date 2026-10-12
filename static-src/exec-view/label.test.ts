// The exec view's one label writer: the verbatim id, with its scope qualifier dimmed and, in a
// bounded column, given up first.
import { afterEach, describe, expect, it } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { paintLabel } from "./label.js";

afterEach(() => {
  document.body.replaceChildren();
});

describe("paintLabel", () => {
  it("writes the verbatim id, split into qualifier and leaf", () => {
    const target = document.createElement("span");
    paintLabel(target, { label: "review/code-adversary-claude", qualifier: "review/" });
    expect(target.textContent).toBe("review/code-adversary-claude");
    expect(target.classList.contains("ev-qualified")).toBe(true);
    expect(target.querySelector(".ev-q")?.textContent).toBe("review/");
    expect(target.querySelector(".ev-leaf")?.textContent).toBe("code-adversary-claude");
  });

  it("writes plain text with no qualifier, or one that is not a strict prefix", () => {
    for (const node of [
      { label: "implement" },
      { label: "review", qualifier: "review" },
      { label: "implement", qualifier: "review/" },
    ]) {
      const target = document.createElement("span");
      paintLabel(target, node);
      expect(target.textContent).toBe(node.label);
      expect(target.children).toHaveLength(0);
      expect(target.classList.contains("ev-qualified")).toBe(false);
    }
  });

  it("leaves an unchanged label's spans in place, and drops them when the qualifier goes", () => {
    const target = document.createElement("span");
    const node = { label: "review/advisor", qualifier: "review/" };
    paintLabel(target, node);
    const leaf = target.querySelector(".ev-leaf");
    paintLabel(target, { ...node });
    expect(target.querySelector(".ev-leaf")).toBe(leaf);
    paintLabel(target, { label: "review/advisor" });
    expect(target.querySelector(".ev-leaf")).toBeNull();
    expect(target.classList.contains("ev-qualified")).toBe(false);
  });
});

describe("a qualified id in a bounded column", () => {
  it("gives up the qualifier before the leaf, keeping its trailing slash", () => {
    const style = mountAppCSS();
    const target = document.createElement("span");
    target.className = "ev-tl-name";
    target.style.inlineSize = "12ch";
    document.body.append(target);
    paintLabel(target, { label: "review/code-adversary-gpt", qualifier: "review/" });
    const q = target.querySelector<HTMLElement>(".ev-q")!;
    const leaf = target.querySelector<HTMLElement>(".ev-leaf")!;
    // The qualifier is down to its floor while the leaf keeps most of the column.
    expect(q.getBoundingClientRect().width).toBeLessThan(leaf.getBoundingClientRect().width / 3);
    expect(q.scrollWidth).toBeGreaterThan(q.clientWidth);
    // Ellipsized from its start, so the `/` is what stays visible at its end.
    expect(getComputedStyle(q).direction).toBe("rtl");
    style.remove();
  });
});
