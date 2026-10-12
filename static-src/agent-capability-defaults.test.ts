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
    ["flag-model-fallback", false],
    ["flag-spec-ask-first", false],
    ["flag-steering-reminders", false],
    ["flag-workflows", true],
  ] as const)("paints #%s at its default (%s)", (id, on) => {
    expect(shippedSwitch(id).checked).toBe(on);
  });

  it("#flag-workflows hint names what each position does to delegation", () => {
    const doc = new DOMParser().parseFromString(indexHtml, "text/html");
    expect(doc.getElementById("flag-workflows-hint")?.textContent).toContain(
      "On: the agent hands work to workflows that run in the background while you keep chatting. " +
        "Off: it uses subagents instead and waits for each to finish before it continues.",
    );
  });
});
