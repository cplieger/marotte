// Typing anywhere on the chat view lands in the composer.

import { $ } from "./dom.js";

/**
 * Redirect a bare printable keystroke with no modifier to the composer, unless focus
 * already sits somewhere that wants keys (a field, the terminal, an open dialog).
 */
export function focusComposerOnTyping(e: KeyboardEvent): void {
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // `key.length === 1` excludes Enter, Escape, Tab, the arrows and the F-keys without
  // enumerating them.
  if (e.key.length !== 1) {
    return;
  }
  const active = document.activeElement;
  if (active instanceof HTMLElement) {
    if (
      active instanceof HTMLInputElement ||
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLSelectElement ||
      active.isContentEditable ||
      active.closest("#shell-panel, dialog[open], .wt-root") !== null
    ) {
      return;
    }
  }
  // Only when a transcript is on screen; typing on Settings or the file browser must not
  // yank focus into a composer the user cannot see.
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return;
  }
  const input = $.promptInput;
  if (input.disabled) {
    return;
  }
  // Let the SAME keystroke land in the box: preventing the default and appending by hand
  // would drop dead keys and IME composition.
  input.focus();
}
