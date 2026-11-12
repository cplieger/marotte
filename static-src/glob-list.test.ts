import { describe, expect, it } from "vitest";

import { joinGlobList, splitGlobList } from "./glob-list.js";

describe("splitGlobList", () => {
  it.each([
    ["", []],
    [" , ,", []],
    ["docs/**", ["docs/**"]],
    ["docs/**, **/*.md ,src/*", ["docs/**", "**/*.md", "src/*"]],
    // minimatch brace sets keep their commas, nested ones too.
    ["**/*.{ts,js}, docs/**", ["**/*.{ts,js}", "docs/**"]],
    ["{a,{b,c}}/x,y", ["{a,{b,c}}/x", "y"]],
    // A stray closing brace does not swallow the next separator.
    ["a},b", ["a}", "b"]],
    // An unclosed brace keeps the rest as one literal pattern.
    ["a{b,c", ["a{b,c"]],
    ["my docs/*.md, x", ["my docs/*.md", "x"]],
  ] as const)("reads %j as %j", (raw, want) => {
    expect(splitGlobList(raw)).toEqual(want);
  });
});

describe("joinGlobList", () => {
  it.each([[[]], [["docs/**"]], [["**/*.{ts,js}", "docs/**", "my docs/*.md"]]] as const)(
    "shows %j as text that reads back as the same list",
    (patterns) => {
      expect(splitGlobList(joinGlobList(patterns))).toEqual(patterns);
    },
  );

  it("separates patterns with a comma and a space", () => {
    expect(joinGlobList(["a/**", "b/*.md"])).toBe("a/**, b/*.md");
  });
});
