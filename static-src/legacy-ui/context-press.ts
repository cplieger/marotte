// context-press.ts — Which click ends the press a `contextmenu` fired during. macOS Safari makes a Control-click's
// press a `contextmenu` and still sends `click` on its release (https://stackoverflow.com/q/53217104; Gecko fixed
// the same in https://bugzilla.mozilla.org/show_bug.cgi?id=1677334), where a right button's release sends none.

let menuPress = false;

// On window, capturing, at import: the flag must be set before any listener opens a menu from the event.
window.addEventListener(
  "pointerdown",
  () => {
    menuPress = false;
  },
  true,
);
window.addEventListener(
  "contextmenu",
  () => {
    menuPress = true;
  },
  true,
);

/** True for a pointer click (`detail` above 0) whose press fired a `contextmenu`: the menu's gesture, not a
 *  click. A keyboard or scripted click (`detail` 0) never is. */
export function isContextPressRelease(e: MouseEvent): boolean {
  return menuPress && e.detail > 0;
}
