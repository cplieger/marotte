// Under a finger Return is a new line and only Send sends. Android reports keyCode 229 for most keys, so a list
// continues from the `insertLineBreak` input event, never from keydown.

import { continueAfterBreak } from "./list-continue.js";
import { isIOS } from "./platform.js";
import { currentTier } from "./pointer-tier.js";
import { guardSmartPunctuation } from "./smart-punctuation.js";
import { applyEdit } from "./text-edit.js";

/** Whether the document is laid out for a coarse pointer now; read per event, since the tier moves without a reload. */
export function touchComposer(): boolean {
  return currentTier() === "coarse";
}

/** `blocked` reports a state that owns the line break instead, such as an open
 *  completion menu. */
export function wireTouchComposer(
  el: HTMLTextAreaElement,
  blocked: () => boolean = () => false,
): void {
  // The platform, not the tier: an iPad with a trackpad lays out fine and
  // still substitutes.
  if (isIOS) {
    guardSmartPunctuation(el);
  }
  // A break that replaced a selection collapses it before `input` fires, so
  // whether there was one is read here.
  let replacedSelection = false;
  el.addEventListener("beforeinput", () => {
    replacedSelection = el.selectionStart !== el.selectionEnd;
  });
  el.addEventListener("input", (ev) => {
    if (
      !(ev instanceof InputEvent) ||
      ev.inputType !== "insertLineBreak" ||
      ev.isComposing ||
      replacedSelection ||
      !touchComposer() ||
      blocked()
    ) {
      return;
    }
    const edit = continueAfterBreak(el.value, el.selectionStart);
    if (edit !== null) {
      applyEdit(el, edit);
    }
  });
}
