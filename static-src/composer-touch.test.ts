// Off iOS (Chromium here is not an iPhone), a typographic character is left as typed. The iOS side is in
// `prompt-input-lists.test.ts`.

import { describe, it, expect, afterEach } from "vitest";
import { wireTouchComposer } from "./composer-touch.js";

afterEach(() => {
  document.body.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

function substitute(el: HTMLTextAreaElement, before: string, after: string): void {
  el.value = before;
  el.dispatchEvent(new InputEvent("beforeinput", { inputType: "insertText", bubbles: true }));
  el.value = after;
  el.setSelectionRange(after.length, after.length);
  el.dispatchEvent(new InputEvent("input", { inputType: "insertReplacementText", bubbles: true }));
}

describe("smart punctuation off iOS", () => {
  it.each(["fine", "coarse"])("leaves an em dash alone on a %s pointer", (tier) => {
    document.documentElement.setAttribute("data-pointer", tier);
    const el = document.createElement("textarea");
    document.body.append(el);
    wireTouchComposer(el);
    substitute(el, "a -", "a \u2014");
    expect(el.value).toBe("a \u2014");
  });
});
