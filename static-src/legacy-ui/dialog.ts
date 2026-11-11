/**
 * dialog.ts — Thin behavior layer over the native `<dialog>` element. The
 * platform gives us focus containment, the top layer, and Escape-to-close for
 * free; this module adds backdrop-click dismissal (drag-safe) and a fade-out
 * lifecycle via a namespaced `is-leaving` class before the element is closed.
 *
 * @module
 */

import { untracked } from "@cplieger/reactive";

import {
  dialogEngine,
  existingEngine,
  type DialogEngine,
  type DialogFacet,
} from "./dialog-engine.js";
import { SUPERSEDED, ownedBinding } from "./owner.js";

/** Which user gestures dismiss a wrapped `<dialog>`, an optional veto over
 *  them, and a hook that runs once the fade-out has finished. All optional; by
 *  default both backdrop and Escape dismiss it. */
export interface DialogOptions {
  /** Close when the backdrop is clicked. Default `true`. */
  closeOnBackdrop?: boolean;
  /** Close on Escape. Default `true`. */
  closeOnEscape?: boolean;
  /** Dismiss guard, consulted on every USER dismissal attempt (backdrop click,
   *  Escape): return `false` to refuse it and keep the dialog open. The wiring
   *  stays armed, so later attempts re-consult the guard. Perform any "why
   *  not" feedback (a toast, a shake) inside the guard before returning.
   *  Programmatic `close()` is unaffected. Omitted = always dismissible. */
  canDismiss?: () => boolean;
  /** Invoked once each time the dialog goes from shown to closed, whoever
   *  closed it: the fade, `closeDialog`, or the browser. */
  onClose?: () => void;
}

/** Handle on a wrapped `<dialog>`. The element is the caller's throughout: this
 *  controller adds wiring, classes and the dialog's live-region pair, and creates
 *  or removes no other node. The pair is appended as the dialog's last two children
 *  at the first helper or controller call on it and stays for the dialog's life,
 *  through closes and `dispose()`: render content into a child element, never by
 *  replacing the dialog's children, or announcements miss it until the next open. */
export interface DialogController {
  /** Open as a platform modal, cancelling a leave fade still in flight so a
   *  reopen mid-fade is safe. A no-op while already open. Throws as
   *  `openDialog` does, and an `InvalidStateError` once `dispose()` has begun,
   *  also from a handler that `dispose()` reaches. */
  open(): void;
  /** Close through the fade-out lifecycle. Programmatic, so `canDismiss` does
   *  not apply. Does nothing once `dispose()` has begun. */
  close(): void;
  /** The `<dialog>` handed to `createDialog`, never a node this controller
   *  made. Only the caller may remove it, and removing it neither unwires the
   *  controller nor clears this reference: it keeps reading back a detached
   *  node, listeners and all, until `dispose()` runs. */
  readonly el: HTMLDialogElement;
  /** Close the dialog at once if it is open (no fade, no `onClose`) and remove
   *  the dismissal wiring and the `uip-dialog` class. The live-region pair stays. */
  dispose(): void;
}

/** Open a native <dialog> as a modal, first putting back its live-region pair if
 *  the app's render removed it, so `announce` reaches it while the rest of the page
 *  is inert. Toasts and popups go to the modal opened last; announcements are written
 *  to every open modal these helpers drive and to the root. A reopen during the leave
 *  fade cancels it. Throws `showModal()`'s `InvalidStateError` for a disconnected
 *  dialog or one showing as a popover, keeping only the pair, and an
 *  `InvalidStateError` for the dialog of a disposed modal. */
// deadset:ignore DS1102 -- test seam: drives the dialog engine createDialog, createModal and ask share
export function openDialog(dialog: HTMLDialogElement): void {
  const engine = dialogEngine(dialog);
  engine.open();
  engine.flush();
}

/** Close a native <dialog> after a fade-out: add `is-leaving`, wait for
 *  `transitionend` (or the fallback), then close and invoke `onClosed`. A
 *  reopen cancels a pending close and drops its `onClosed`. On a closed dialog
 *  `onClosed` runs at once, or, when called from code a change of that dialog
 *  is running, as soon as that change finishes. */
// deadset:ignore DS1102 -- test seam: drives the dialog engine createDialog, createModal and ask share
export function closeDialog(dialog: HTMLDialogElement, onClosed?: () => void): void {
  const engine = dialogEngine(dialog);
  engine.close(onClosed);
  engine.flush();
}

/** @internal Install drag-safe backdrop-press detection on `dialog`, with no
 *  ownership: `onPress` fires only when a press both starts and ends on the
 *  dialog element itself. Returns the remover. */
export function listenBackdrop(dialog: HTMLDialogElement, onPress: () => void): () => void {
  let downOnDialog = false;
  const onMouseDown = (e: MouseEvent): void => {
    // A backdrop press targets the dialog element itself (content presses
    // target a descendant), so a drag-select ending on the backdrop is not one.
    downOnDialog = e.target === dialog;
  };
  const onMouseUp = (e: MouseEvent): void => {
    const onBackdrop = e.target === dialog && downOnDialog;
    downOnDialog = false;
    if (onBackdrop) {
      onPress();
    }
  };
  dialog.addEventListener("mousedown", onMouseDown);
  dialog.addEventListener("mouseup", onMouseUp);
  return () => {
    dialog.removeEventListener("mousedown", onMouseDown);
    dialog.removeEventListener("mouseup", onMouseUp);
  };
}

/** Throw unless `dialog` can take a new controller. Creates nothing. */
function assertNoController(dialog: HTMLDialogElement): void {
  const engine = existingEngine(dialog);
  if (engine !== undefined && (engine.state.retired || engine.state.facet !== null)) {
    throw new DOMException("The dialog already has a controller", "InvalidStateError");
  }
}

/** Wrap an existing native <dialog> with backdrop + Escape dismissal and the
 *  fade-out close lifecycle. Adds the `uip-dialog` class for the base skin.
 *  Throws `InvalidStateError`, acquiring nothing, on a dialog that already
 *  has a controller (a live `createDialog`, or a modal's `el`). */
export function createDialog(dialog: HTMLDialogElement, opts?: DialogOptions): DialogController {
  const o = untracked(() => ({
    closeOnBackdrop: opts?.closeOnBackdrop ?? true,
    closeOnEscape: opts?.closeOnEscape ?? true,
    canDismiss: opts?.canDismiss,
    onClose: opts?.onClose,
  }));
  assertNoController(dialog);

  return ownedBinding((binding) => {
    let engine: DialogEngine | null = null;
    let disarm: (() => void) | null = null;
    let reported: "shown" | "closed" = "closed";

    const dismiss = (): void => {
      const e = engine;
      if (binding.ended || e?.phase !== "open") {
        return;
      }
      const guard = o.canDismiss;
      if (guard !== undefined) {
        const answer = e.consult(guard);
        if (answer === SUPERSEDED || !answer) {
          return;
        }
      }
      e.close();
      e.flush();
    };
    const onCancel = (e: Event): void => {
      // A nested `<input type=file>` fires a bubbling `cancel` when its picker
      // is dismissed (https://developer.mozilla.org/en-US/docs/Web/API/HTMLInputElement/cancel_event);
      // only the dialog's own `cancel` is an Escape.
      if (e.target !== dialog) {
        return;
      }
      // The platform closes at once after `cancel`; prevented, the fade runs instead.
      e.preventDefault();
      if (o.closeOnEscape) {
        dismiss();
      }
    };

    const facet: DialogFacet = {
      prepare(fx) {
        if (disarm === null) {
          dialog.addEventListener("cancel", onCancel);
          const removeBackdrop = o.closeOnBackdrop ? listenBackdrop(dialog, dismiss) : null;
          disarm = () => {
            dialog.removeEventListener("cancel", onCancel);
            removeBackdrop?.();
          };
        }
        fx.addClass(dialog, "uip-dialog");
      },
      notify(fx, d) {
        const shown = d.phase !== "closed";
        if (reported === "shown" && !shown) {
          reported = "closed";
          if (o.onClose !== undefined) {
            fx.call(o.onClose);
          }
        } else if (shown) {
          reported = "shown";
        }
      },
      cleanup(fx) {
        disarm?.();
        disarm = null;
        fx.removeClass(dialog, "uip-dialog");
      },
    };

    return {
      controller: {
        el: dialog,
        open: () => {
          if (binding.ended || engine === null) {
            throw new DOMException("The dialog controller is disposed", "InvalidStateError");
          }
          engine.open();
          engine.flush();
        },
        close: () => {
          if (!binding.ended && engine !== null) {
            engine.close();
            engine.flush();
          }
        },
        dispose: binding.dispose,
      },
      start: () => {
        const e = dialogEngine(dialog);
        engine = e;
        reported = e.phase === "closed" ? "closed" : "shown";
        e.attach(facet);
        e.flush();
      },
      release: () => {
        if (engine !== null) {
          engine.detach(facet);
          engine.flush();
        }
      },
    };
  });
}
