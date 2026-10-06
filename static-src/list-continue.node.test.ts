// What a new line does inside a markdown list item. `|` marks the caret.

import { describe, it, expect } from "vitest";
import { continueAfterBreak, continueList } from "./list-continue.js";
import type { Edit } from "./text-edit.js";

function apply(value: string, e: Edit): string {
  return value.slice(0, e.start) + e.text + value.slice(e.end);
}

/** `|` marks the new caret; null means the browser's plain line break. */
function run(marked: string): string | null {
  const caret = marked.indexOf("|");
  const value = marked.replace("|", "");
  const e = continueList(value, caret);
  if (e === null) {
    return null;
  }
  const out = apply(value, e);
  return `${out.slice(0, e.caret)}|${out.slice(e.caret)}`;
}

describe("continueList", () => {
  it.each([
    ["- one|", "- one\n- |"],
    ["* one|", "* one\n* |"],
    ["+ one|", "+ one\n+ |"],
    ["1. one|", "1. one\n2. |"],
    ["9) one|", "9) one\n10) |"],
    ["- [ ] task|", "- [ ] task\n- [ ] |"],
    ["- [x] done|", "- [x] done\n- [ ] |"],
    ["  - nested|", "  - nested\n  - |"],
    ["1.  wide gap|", "1.  wide gap\n2.  |"],
    ["07. padded|", "07. padded\n08. |"],
    ["- split| here", "- split\n- | here"],
    ["intro\n- one|", "intro\n- one\n- |"],
  ])("continues %j", (input, want) => {
    expect(run(input)).toBe(want);
  });

  it.each([
    ["- |", "|"],
    ["1. |", "|"],
    ["- [ ] |", "|"],
    ["- [ ]|", "|"],
    ["- one\n  - |", "- one\n|"],
  ])("exits the list on the empty item %j", (input, want) => {
    expect(run(input)).toBe(want);
  });

  it.each([
    ["plain text|"],
    ["-5 degrees|"],
    ["1.5 litres|"],
    ["-flag|"],
    ["- - -|"],
    ["* * *|"],
    ["-| one"],
    ["1|. one"],
    ["```\n- in a fence|"],
    ["~~~~\n- in a fence\n~~~\n- still|"],
    ["999999999. last|"],
  ])("leaves the plain line break to the browser in %j", (input) => {
    expect(run(input)).toBeNull();
  });

  it("continues again once the fence is closed", () => {
    expect(run("```\ncode\n```\n- after|")).toBe("```\ncode\n```\n- after\n- |");
  });
});

describe("continueAfterBreak", () => {
  it("adds the marker after a line break the browser already inserted", () => {
    const value = "- one\n";
    const e = continueAfterBreak(value, value.length);
    expect(e).not.toBeNull();
    expect(e === null ? "" : apply(value, e)).toBe("- one\n- ");
  });

  it("takes the inserted break back when it ended an empty item", () => {
    const value = "intro\n- \n";
    const e = continueAfterBreak(value, value.length);
    expect(e === null ? null : apply(value, e)).toBe("intro\n");
  });

  it("does nothing when the character before the caret is not a line break", () => {
    expect(continueAfterBreak("- one", 5)).toBeNull();
  });
});
