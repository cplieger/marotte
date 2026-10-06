// The one way to write the composer's value programmatically. A `.value` assignment fires no `input` event, and the
// per-chat draft layer (composer-state.ts) learns of text only through it. The event bubbles, as a keystroke's does.
// A leaf module so the writers stay peers without an import cycle. Not for the draft layer's own restore/seed writes.

import { $ } from "./dom.js";

/** Write the composer's value and announce it, so every listener on the box
 *  sees the change whoever made it. */
export function setComposerValue(v: string): void {
  const input = $.promptInput;
  input.value = v;
  input.dispatchEvent(new Event("input", { bubbles: true }));
}
