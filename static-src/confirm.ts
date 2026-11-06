// Confirm dialog over `@cplieger/ui/ask`'s `ask`: a short question as the heading, what it does as the body.
// The destructive variant uses role="alertdialog" and focuses Cancel. Skin: `.uip-ask` in css/04-uip-skin.css.

import { ask } from "@cplieger/ui/ask";

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

/** One confirmation. The title is the question and names the dialog; the body says what follows and describes it. */
export interface Confirmation {
  /** The question, short: "Delete notes.md?". */
  readonly title: string;
  /** What confirming does and what it costs. Omit it when the title says everything. */
  readonly body?: string;
  /** The confirm button's text. Default "Confirm". */
  readonly confirmLabel?: string;
  readonly variant?: "destructive" | "normal";
}

/** Show a confirmation dialog. Resolves true if the user confirms, false on cancel / Escape / backdrop click /
 *  preemption by a later confirm(). Focus returns to the control whose click opened it. */
export function confirm(q: Confirmation): Promise<boolean> {
  if (activated?.isConnected === true && document.activeElement !== activated) {
    // Before `showModal`, which records the focused element as the one its close restores.
    activated.focus({ preventScroll: true });
  }
  return ask(q.body ?? "", {
    title: q.title,
    confirmLabel: q.confirmLabel ?? "Confirm",
    variant: q.variant ?? "normal",
  });
}
