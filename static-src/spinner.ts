// The markup for the `.spinner` ring (15-input.css owns its drawing and how a site tunes it).

import { el } from "@cplieger/reactive";
import { bindLoadingState } from "./actions/index.js";

/** A ring for a face its caller paints and removes; `modifier` is space-separated classes added beside `spinner`. */
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
