// smart-punctuation.ts: which substitutions are reverted, and which typed characters are left
// alone.

import { describe, it, expect } from "vitest";
import { smartRevert } from "./smart-punctuation.js";
import type { Edit } from "./text-edit.js";

function revert(before: string, after: string, hyphenAt: number | null = null): string | null {
  const e: Edit | null = smartRevert(before, after, hyphenAt);
  return e === null ? null : after.slice(0, e.start) + e.text + after.slice(e.end);
}

describe("smartRevert", () => {
  it("turns an em dash that replaced a hyphen back into two hyphens", () => {
    expect(revert("git push -", "git push \u2014")).toBe("git push --");
  });

  it("turns an en dash that replaced a hyphen back into two hyphens", () => {
    expect(revert("a -", "a \u2013")).toBe("a --");
  });

  it("reverts a dash inserted where the previous backspace removed a hyphen", () => {
    expect(revert("a ", "a \u2014", 2)).toBe("a --");
  });

  it("keeps a dash typed after other text", () => {
    expect(revert("a ", "a \u2014")).toBeNull();
  });

  it("straightens a curly apostrophe", () => {
    expect(revert("don", "don\u2019")).toBe("don'");
  });

  it("straightens curly double quotes", () => {
    expect(revert("echo ", "echo \u201C")).toBe('echo "');
    expect(revert('echo "x', 'echo "x\u201D')).toBe('echo "x"');
  });

  it("straightens a quote that replaced its straight form", () => {
    expect(revert("it'", "it\u2019")).toBe("it'");
  });

  it("leaves ordinary typing alone", () => {
    expect(revert("ab", "abc")).toBeNull();
    expect(revert("a-", "a--")).toBeNull();
  });

  it("leaves a pasted span holding a dash alone", () => {
    expect(revert("x", "x \u2014 y")).toBeNull();
  });
});
