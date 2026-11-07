// Which typed keys belong to the shell's terminal while focus sits on the page body.

import { $ } from "./dom.js";

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

/** Whether the terminal takes the next typed key from the document while the panel is open. With a
 *  hardware keyboard seen on a touch device (an iPad), web-terminal-ui releases focus to the body
 *  on a tap in the terminal and types the next key itself when the last press was inside it (its
 *  kernel's `pressedInsideShell`), so a page-wide type-ahead must leave that key alone. A closed
 *  panel's terminal cannot take focus, so the library drops the key. */
export function shellTakesBodyKeys(): boolean {
  return (
    pressedInTerminal &&
    document.activeElement === document.body &&
    !$.shellPanel.classList.contains("shell-closed")
  );
}
