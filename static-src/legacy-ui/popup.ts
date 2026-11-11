/**
 * popup.ts — Reveal + light-dismiss lifecycle for a caller-supplied panel,
 * WITHOUT placement. Use for an in-flow or self-positioned panel; use popover
 * when the panel floats anchored to something. Public facade over the shared
 * lifecycle core (`popup-core.ts`, internal).
 *
 * @module
 */

import { untracked } from "@cplieger/reactive";

import { createPopupCore, normalizePopupKey, normalizePopupOptions } from "./popup-core.js";
import type { PopupController, PopupOptions } from "./popup-core.js";

export { closePopupGroup } from "./popup-core.js";
export type { PopupController } from "./popup-core.js";

/** Wire `panel` into a revealable, light-dismissing popup. Never positions or
 *  removes it from the DOM — the caller owns the element, placement, and motion. */
export function createPopup(panel: HTMLElement, opts?: PopupOptions): PopupController {
  const config = untracked(() => normalizePopupOptions(opts));
  return createPopupCore(panel, config, { stateClass: "uip-popup" }, normalizePopupKey).controller;
}
