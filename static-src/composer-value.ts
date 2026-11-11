// The ways to write the composer's value programmatically, and whether it holds a send. A `.value` assignment fires
// no `input` event, and the per-chat draft layer (composer-state.ts) learns of text only through it. The event
// bubbles, as a keystroke's does. A leaf module so the writers and the senders stay peers without an import cycle.

import { signal, type ReadonlySignal } from "@cplieger/reactive";
import { $ } from "./dom.js";

const text = signal("");

/** The composer's text, current after every write as long as each writer goes through this module: an `input`
 *  event reaches `syncComposerText` through the listener prompt-input.ts wires, and `writeComposerSilently` syncs
 *  itself. A bare `.value` assignment leaves it stale. */
export const composerText: ReadonlySignal<string> = text;

/** Write the composer's value and announce it, so every listener on the box
 *  sees the change whoever made it. */
export function setComposerValue(v: string): void {
  const input = $.promptInput;
  input.value = v;
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

/** Write the composer's value with no `input` event: the draft layer's own restores and seeds, which must not be
 *  recorded as typing. */
export function writeComposerSilently(v: string): void {
  $.promptInput.value = v;
  syncComposerText();
}

/** Re-read the box into `composerText`. */
export function syncComposerText(): void {
  text.value = $.promptInput.value;
}

/** Whether a composer holding `text` with `staged` attachments has something to send. Staged attachments are a
 *  prompt on their own, so a blank box with files staged sends. */
export function isSendable(text: string, staged: boolean): boolean {
  return text.trim() !== "" || staged;
}
