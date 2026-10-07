// The chat log of the report recorded the prompt as "`\b": the accent of a dead key that
// Backspace cancelled, plus a literal U+0008. These drive the input events that commit it.

import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { guardBackspaceChars } from "./backspace-char.js";

beforeAll(() => {
  guardBackspaceChars();
});

afterEach(() => {
  document.body.replaceChildren();
});

function field<T extends HTMLTextAreaElement | HTMLInputElement>(
  el: T,
  value: string,
  caret: number,
): T {
  document.body.append(el);
  el.focus();
  el.value = value;
  el.setSelectionRange(caret, caret);
  return el;
}

function commit(el: HTMLElement, isComposing = false): void {
  el.dispatchEvent(
    new InputEvent("input", { inputType: "insertText", data: "\b", isComposing, bubbles: true }),
  );
}

describe("a committed U+0008", () => {
  it("deletes itself and the character before it", () => {
    const el = field(document.createElement("textarea"), "type `\b", 7);
    commit(el);
    expect(el.value).toBe("type ");
    expect(el.selectionStart).toBe(5);
  });

  it("keeps the caret on the text after it", () => {
    const el = field(document.createElement("textarea"), "ab`\bcd", 4);
    commit(el);
    expect(el.value).toBe("abcd");
    expect(el.selectionStart).toBe(2);
  });

  it("deletes a whole astral character", () => {
    const el = field(document.createElement("textarea"), "x\u{1F600}\b", 4);
    commit(el);
    expect(el.value).toBe("x");
  });

  it("is dropped at the start of the field", () => {
    const el = field(document.createElement("textarea"), "\bfoo", 1);
    commit(el);
    expect(el.value).toBe("foo");
    expect(el.selectionStart).toBe(0);
  });

  it("is resolved in a text input", () => {
    const el = field(document.createElement("input"), "a`\b", 3);
    commit(el);
    expect(el.value).toBe("a");
  });

  it("waits for the composition to end", () => {
    const el = field(document.createElement("textarea"), "`\b", 2);
    commit(el, true);
    expect(el.value).toBe("`\b");
    el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    expect(el.value).toBe("");
  });

  it("reaches the terminal's input untouched", () => {
    const el = document.createElement("textarea");
    el.className = "term-input";
    field(el, "`\b", 2);
    commit(el);
    expect(el.value).toBe("`\b");
  });
});
