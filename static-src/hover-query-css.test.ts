// Hover is gated on `any-hover`, never on `hover`: the `hover` media feature reports
// only the PRIMARY input, so a `(hover: hover)` gate drops the rule on every
// touch-primary device that also has a pointer (an iPad with a trackpad answers
// `hover: none`). A source sweep over every stylesheet the bundle assembles, so a
// sheet added to css/MANIFEST is covered with no edit here.
import { describe, it, expect } from "vitest";
import { manifestSheets } from "./__test-helpers__/css-rules.js";

const PRIMARY_HOVER = /\(\s*hover\s*:\s*(hover|none)\s*\)/;

function mediaPreludes(css: string): { line: number; prelude: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
  return [...text.matchAll(/@media\b([^{]*)\{/g)].map((m) => ({
    line: text.slice(0, m.index).split("\n").length,
    prelude: (m[1] ?? "").trim(),
  }));
}

describe("hover media queries", () => {
  it("finds media queries to check", () => {
    const count = manifestSheets().reduce((n, s) => n + mediaPreludes(s.css).length, 0);
    expect(count).toBeGreaterThan(10);
  });

  it("gates on any-hover in every sheet, never on the primary-input hover feature", () => {
    const hits = manifestSheets().flatMap((s) =>
      mediaPreludes(s.css)
        .filter((p) => PRIMARY_HOVER.test(p.prelude))
        .map((p) => `${s.name}:${String(p.line)}: @media ${p.prelude}`),
    );
    expect(hits).toEqual([]);
  });
});
