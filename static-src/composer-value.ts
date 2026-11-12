// The ways to write the composer's value programmatically, and whether it holds a send. A `.value` assignment fires
// no `input` event, and the per-chat draft layer (composer-state.ts) learns of text only through it. The event
// bubbles, as a keystroke's does. A leaf module so the writers and the senders stay peers without an import cycle.

import { signal, type ReadonlySignal } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { applyEdit } from "./text-edit.js";

const text = signal("");

let beforeWrite: () => void = () => undefined;

/** `fn` runs ahead of every programmatic write but `restoreComposerDraft`; one hook, the last registration wins. */
export function onComposerWrite(fn: () => void): void {
  beforeWrite = fn;
}

/** The composer's text, current after every write as long as each writer goes through this module: an `input`
 *  event reaches `syncComposerText` through the listener prompt-input.ts wires, and `write` syncs the silent ones.
 *  A bare `.value` assignment leaves it stale. */
export const composerText: ReadonlySignal<string> = text;

/** Every programmatic write of the box. `announce` fires a bubbling `input` event, as a keystroke does; `recall`
 *  writes one undo step with none; `restore` is no edit, so the write hook does not run. */
function write(v: string, how: "announce" | "silent" | "recall" | "restore"): void {
  if (how !== "restore") {
    beforeWrite();
  }
  const box = $.promptInput;
  switch (how) {
    case "announce":
      box.value = v;
      box.dispatchEvent(new Event("input", { bubbles: true }));
      return;
    case "silent":
    case "restore":
      box.value = v;
      break;
    case "recall":
      applyEdit(
        box,
        { start: 0, end: box.value.length, text: v, caret: v.length },
        { silent: true },
      );
      break;
  }
  syncComposerText();
}

/** Write the composer's value and announce it, so every listener on the box
 *  sees the change whoever made it. */
export function setComposerValue(v: string): void {
  write(v, "announce");
}

/** Write the composer's value with no `input` event, so it is not recorded as typing: another chat's draft, an
 *  adopted or remote draft, a failed send put back, a cleared box. The write hook runs. */
export function writeComposerSilently(v: string): void {
  write(v, "silent");
}

/** Put the chat the box was parked from back on screen with its own draft, with no `input` event: the reader comes
 *  back to the text they left, so nothing in the box was edited. */
export function restoreComposerDraft(v: string): void {
  write(v, "restore");
}

/** Show `v` in the box as one undo step with no `input` event: history recall is navigation, so the draft layer
 *  must not record a recalled prompt as typing, and Ctrl+Z takes it back. */
export function recallComposerValue(v: string): void {
  write(v, "recall");
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
