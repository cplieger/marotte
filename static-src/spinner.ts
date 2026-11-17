// The app's one loading mark, the `.spinner` ring (15-input.css). A site varies its size and ink through the
// ring's custom properties, never with a ring of its own.

import { el } from "@cplieger/reactive";
import { bindLoadingState } from "./actions/index.js";

/** A ring for a face its caller paints and removes; `modifier` is a class that only sets the ring's properties. */
export function spinnerEl(modifier?: string): HTMLSpanElement {
  const className = modifier === undefined ? "spinner" : `spinner ${modifier}`;
  return el("span", { className, "aria-hidden": "true" });
}

/**
 * `bindLoadingState` with the ring: the button keeps one dormant ring, which 15-input.css shows in its glyph's place
 * (before its label on a text button) only while the binding holds `aria-busy`.
 */
export function bindLoadingSpinner(
  actionName: string,
  btn: HTMLButtonElement,
  opts?: Parameters<typeof bindLoadingState>[2],
): () => void {
  if (btn.querySelector(":scope > .spinner-on-busy") === null) {
    btn.prepend(spinnerEl("spinner-on-busy"));
  }
  return bindLoadingState(actionName, btn, opts);
}
