// Which typed keys belong to the shell's terminal, and which chords stay the app's there.

import { $ } from "./dom.js";

/** Whether keys.ts runs `e` with focus in a field: Ctrl/Cmd+Enter, Ctrl/Cmd+N and Cmd+K, plus Ctrl+K
 *  outside the shell. In the shell Ctrl+K is the line editor's (bash, nano and emacs use it); a
 *  terminal never gets a Cmd chord, so Cmd+K stays the app's there. */
export function appFieldChord(e: KeyboardEvent, inShell: boolean): boolean {
  if (!(e.ctrlKey || e.metaKey) || e.shiftKey) {
    return false;
  }
  const key = e.key.toLowerCase();
  return key === "enter" || key === "n" || (key === "k" && !(inShell && e.ctrlKey));
}

let pressedInTerminal = false;
let tracking = false;

/** Start recording whether the last press landed in the terminal. Idempotent. */
export function trackShellPresses(): void {
  if (tracking) {
    return;
  }
  tracking = true;
  document.addEventListener(
    "pointerdown",
    (ev) => {
      pressedInTerminal = ev.target instanceof Node && $.shellTerminal.contains(ev.target);
    },
    { capture: true, passive: true },
  );
}

/** Whether the terminal takes a key aimed at `target`: typed into its focused input, or from the
 *  body as {@link shellTakesBodyKeys} decides. */
export function shellTakesKey(target: EventTarget | null): boolean {
  return (
    (target instanceof HTMLTextAreaElement && $.shellTerminal.contains(target)) ||
    shellTakesBodyKeys()
  );
}

/** Whether the terminal takes the next typed key from the document while the panel is open. With a
 *  hardware keyboard seen on a touch device (an iPad), web-terminal-ui releases focus to the body
 *  on a tap in the terminal and types the next key itself when the last press was inside it (its
 *  kernel's `pressedInsideShell`), so a page-wide type-ahead must leave that key alone. A closed
 *  panel's terminal cannot take focus and would drop the key, so the key stays with the page then. */
export function shellTakesBodyKeys(): boolean {
  return (
    pressedInTerminal &&
    document.activeElement === document.body &&
    !$.shellPanel.classList.contains("shell-closed")
  );
}
