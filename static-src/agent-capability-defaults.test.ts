// The Agent capabilities switches as static/index.html ships them, which is also what
// stays on screen when settings cannot be applied, so each must start at
// settings.EffectiveDefaults() for its key.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

function shippedSwitch(id: string): HTMLInputElement {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  const box = doc.getElementById(id);
  if (!(box instanceof HTMLInputElement)) {
    throw new Error(`static/index.html has no input #${id}`);
  }
  return box;
}

describe("Agent capabilities switches before settings load", () => {
  it.each([
    ["flag-knowledge", true],
    ["flag-tool-search", false],
    ["flag-spec-ask-first", false],
    ["flag-inline-agents", false],
    ["flag-steering-reminders", false],
    ["flag-workflows", true],
  ] as const)("paints #%s at its default (%s)", (id, on) => {
    expect(shippedSwitch(id).checked).toBe(on);
  });
});
