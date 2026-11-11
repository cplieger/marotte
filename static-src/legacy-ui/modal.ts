/**
 * modal.ts — Native-`<dialog>` modal built from caller content. `dialog` wraps
 * an EXISTING `<dialog>`; `modal` builds one from arbitrary content. Adds ARIA
 * wiring, drag-safe backdrop dismissal, the shared fade-out lifecycle, and an
 * iOS-safe scroll-lock (`scroll-lock.ts`).
 *
 * @module
 */

import { el, untracked } from "@cplieger/reactive";

import { listenBackdrop } from "./dialog.js";
import { dialogEngine, type DialogEngine, type DialogFacet } from "./dialog-engine.js";
import { SUPERSEDED, ownedBinding } from "./owner.js";
import { _resetScrollLockForTest, flushScrollLock, holdScrollLock } from "./scroll-lock.js";

/** Everything {@link createModal} needs beyond the content: the dismissal
 *  gestures and their veto, the ARIA role and the label/description ids (both
 *  auto-detected from a `-title` / `-desc` suffix when omitted), initial focus,
 *  the background scroll lock, and a post-close hook. All optional. */
interface ModalOptions {
  /** Close when the backdrop is clicked (drag-safe). Default `true`. */
  closeOnBackdrop?: boolean;
  /** Close on Escape. Default `true`. */
  closeOnEscape?: boolean;
  /** ARIA role for the dialog. Default `"dialog"` (the <dialog> implicit role);
   *  `"alertdialog"` sets the role + the `.uip-modal--alert` modifier. */
  role?: "dialog" | "alertdialog";
  /** `id` for `aria-labelledby`. When omitted, a descendant whose `id` ends in
   *  `-title` is auto-detected. */
  labelledBy?: string;
  /** `id` for `aria-describedby`. When omitted, a descendant whose `id` ends in
   *  `-desc` / `-description` is auto-detected. */
  describedBy?: string;
  /** Element to focus on open. Omit to leave focus to the platform (the first
   *  focusable / an `autofocus` element inside the dialog). */
  initialFocus?: HTMLElement | null;
  /** Dismiss guard, consulted on every USER dismissal attempt (backdrop click,
   *  Escape): return `false` to refuse it and keep the modal open. The wiring
   *  stays armed, so later attempts re-consult the guard. Programmatic
   *  `close()` is unaffected. Omitted = always dismissible. */
  canDismiss?: () => boolean;
  /** Lock background page scroll while open, ref-counted across nested modals.
   *  iOS-safe (position:fixed body + scroll restore). Default `true`. */
  scrollLock?: boolean;
  /** Invoked once each time the modal goes from shown to closed, whoever
   *  closed it. */
  onClose?: () => void;
}

/** Handle on a modal built from caller content. The `<dialog>` is built once
 *  and reused across open/close, so a controller is worth keeping for the
 *  lifetime of the thing it belongs to and disposing with it. */
export interface ModalController {
  /** Show as a platform modal — focus containment, top layer and background
   *  inerting come from `showModal()` — and take the scroll lock. Throws an
   *  `InvalidStateError` once `dispose()` has begun; a refused `showModal()`
   *  throws as `openDialog` does, with no scroll lock held. */
  open(): void;
  /** Close through the fade-out lifecycle. Programmatic, so `canDismiss` does
   *  not apply. */
  close(): void;
  /** The `<dialog>` this controller built, with the caller's content inside.
   *  `openDialog(el)` and `closeDialog(el)` drive this modal as `open()` and
   *  `close()` do, and `createDialog(el)` throws. `dispose()` spends it: the
   *  node, and the caller's content with it, leaves the document, while this
   *  reference keeps reading it back. */
  readonly el: HTMLDialogElement;
  readonly isOpen: boolean;
  /** Unwire the dismissal listeners, close immediately (no fade), release the
   *  scroll lock, and remove the dialog from the document. */
  dispose(): void;
}

/** Auto-detect an accessible-name target: a descendant whose id ends `-title`. */
function autoLabelId(content: HTMLElement): string | null {
  const titled = content.querySelector<HTMLElement>("[id$='-title']");
  return titled !== null && titled.id !== "" ? titled.id : null;
}

/** Auto-detect a description target: a descendant whose id ends `-desc` or
 *  `-description`. */
function autoDescribeId(content: HTMLElement): string | null {
  const described = content.querySelector<HTMLElement>("[id$='-desc'], [id$='-description']");
  return described !== null && described.id !== "" ? described.id : null;
}

/** Build a modal from `content`: wrap it in a native `<dialog class="uip-modal">`
 *  appended to `<body>`, and return a controller. The platform provides focus
 *  containment, the top layer, background inerting, Escape, nested stacking, and
 *  focus-return-to-opener; this adds ARIA wiring, drag-safe backdrop dismissal,
 *  the fade-out lifecycle, and an iOS-safe scroll-lock. */
export function createModal(content: HTMLElement, opts?: ModalOptions): ModalController {
  const o = untracked(() => ({
    closeOnBackdrop: opts?.closeOnBackdrop ?? true,
    closeOnEscape: opts?.closeOnEscape ?? true,
    role: opts?.role ?? "dialog",
    scrollLock: opts?.scrollLock ?? true,
    onClose: opts?.onClose,
    canDismiss: opts?.canDismiss,
    initialFocus: opts?.initialFocus ?? null,
    labelledBy: opts?.labelledBy,
    describedBy: opts?.describedBy,
  }));

  return ownedBinding((binding) => {
    const labelledBy = o.labelledBy ?? autoLabelId(content);
    const describedBy = o.describedBy ?? autoDescribeId(content);
    const dialog = el("dialog", {
      className: o.role === "alertdialog" ? "uip-modal uip-modal--alert" : "uip-modal",
      ...(o.role === "alertdialog" ? { role: "alertdialog" } : {}),
      ...(labelledBy === null ? {} : { "aria-labelledby": labelledBy }),
      ...(describedBy === null ? {} : { "aria-describedby": describedBy }),
    }) as HTMLDialogElement;
    const me = Symbol("modal");
    let engine: DialogEngine | null = null;
    let disarm: (() => void) | null = null;
    let focusedFor = 0;
    let reported: "shown" | "closed" = "closed";

    const dismiss = (): void => {
      const e = engine;
      if (binding.ended || e?.phase !== "open") {
        return;
      }
      if (o.canDismiss !== undefined) {
        const answer = e.consult(o.canDismiss);
        if (answer === SUPERSEDED || !answer) {
          return;
        }
      }
      e.close();
      e.flush();
    };
    const onCancel = (e: Event): void => {
      // A descendant's bubbling `cancel` is not an Escape (see createDialog).
      if (e.target !== dialog) {
        return;
      }
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
        fx.addClass(content, "uip-modal-dialog");
        if (content.parentNode !== dialog) {
          fx.append(dialog, content);
        }
        if (!dialog.isConnected) {
          fx.append(document.body, dialog);
        }
      },
      derive(d) {
        if (!o.scrollLock) {
          return;
        }
        const e = engine;
        // Read live at every lock check, so a close the browser makes stops the lock's writes.
        holdScrollLock(me, d === null || e === null ? null : () => e.phase !== "closed");
      },
      follow(fx, d) {
        if (o.scrollLock) {
          fx.call(flushScrollLock);
        }
        const target = o.initialFocus;
        if (d.phase === "open" && focusedFor !== d.opens) {
          focusedFor = d.opens;
          if (target?.isConnected === true) {
            fx.focus(target);
          }
        }
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
        if (o.scrollLock) {
          fx.call(flushScrollLock);
        }
        fx.removeClass(content, "uip-modal-dialog");
        fx.remove(dialog);
      },
    };

    return {
      controller: {
        el: dialog,
        get isOpen(): boolean {
          return !binding.ended && engine?.phase === "open";
        },
        open(): void {
          if (binding.ended || engine === null) {
            throw new DOMException("The modal is disposed", "InvalidStateError");
          }
          engine.open();
          engine.flush();
        },
        close(): void {
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
        e.attach(facet);
        e.flush();
      },
      release: () => {
        if (engine !== null) {
          engine.retire(facet);
          engine.flush();
        }
      },
    };
  });
}

/** Test-only: restore any active scroll-lock and reset shared state. */
// deadset:ignore DS1102 -- test seam: restores the body styles the scroll lock holds
export function _resetForTest(): void {
  _resetScrollLockForTest();
}
