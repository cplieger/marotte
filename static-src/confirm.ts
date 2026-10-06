// Confirm dialog over `@cplieger/ui-primitives`' `ask`, keeping marotte's positional `confirm(message, label?, variant?)`.
// The destructive variant uses role="alertdialog" and focuses Cancel. Skin: `.uip-ask` in css/04-uip-skin.css.

import { ask } from "@cplieger/ui-primitives/ask";

/** Show a confirmation dialog. Resolves true if the user confirms, false on
 *  cancel / Escape / backdrop click / preemption by a later confirm(). */
export function confirm(
  message: string,
  confirmLabel = "Confirm",
  variant: "destructive" | "normal" = "normal",
): Promise<boolean> {
  return ask(message, { confirmLabel, variant });
}
