// Confirm dialog over `@cplieger/ui-primitives`' `ask`, keeping marotte's positional `confirm(message, label?, variant?)`.
// The destructive variant uses role="alertdialog" and focuses Cancel. Skin: `.uip-ask` in css/04-uip-skin.css.

import { ask } from "@cplieger/ui-primitives/ask";

const CONTROL = "button, summary, a[href], [role='button'], [role='menuitem']";

/** The control the click being dispatched landed on, cleared once that dispatch's task ends. */
let activated: HTMLElement | null = null;

// WebKit focuses no button on a click (MDN `<button>` "Clicking and focus"), so a dialog opened from one restores
// focus at close to whatever held it before: the transcript scroller, which then paints its focus ring.
(globalThis as { readonly document?: Document }).document?.addEventListener(
  "click",
  (e) => {
    activated = e.target instanceof Element ? e.target.closest<HTMLElement>(CONTROL) : null;
    setTimeout(() => {
      activated = null;
    }, 0);
  },
  { capture: true },
);

/** Show a confirmation dialog. Resolves true if the user confirms, false on cancel / Escape / backdrop click /
 *  preemption by a later confirm(). Focus returns to the control whose click opened it. */
export function confirm(
  message: string,
  confirmLabel = "Confirm",
  variant: "destructive" | "normal" = "normal",
): Promise<boolean> {
  if (activated?.isConnected === true && document.activeElement !== activated) {
    // Before `showModal`, which records the focused element as the one its close restores.
    activated.focus({ preventScroll: true });
  }
  return ask(message, { confirmLabel, variant });
}
