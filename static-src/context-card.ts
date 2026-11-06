// The prompt bar's context pill and its card: this chat's context use and credits.

import { $, byId } from "./dom.js";
import { makeExpandable, type Expandable } from "./pill-expand.js";

let card: Expandable | null = null;

/** Wire the context pill. Call once at startup. */
export function initContextCard(): void {
  // The card is its trigger's SIBLING (see 15-input.css .pill-slot), so it is looked up by id.
  card = makeExpandable($.contextIndicator, byId("context-card"));
}

/** Open the context pill's card. */
export function openContextCard(): void {
  card?.open();
}
