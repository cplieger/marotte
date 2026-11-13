import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { createRoot } from "@cplieger/reactive";

import { existingEngine } from "./dialog-engine.js";
import { createDialog, openDialog, closeDialog, listenBackdrop } from "./dialog.js";
import { topmostOpenDialog } from "./modal-host.js";
import { classHas, reactingDialog } from "./__test-helpers__/reacting.js";
import { nextTask } from "./__test-helpers__/reentry.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

/** Whether the library holds `dialog` open: a closeDialog then starts a fade rather
 *  than calling back at once. Starts that fade, so a test reads it last. */
function heldOpen(dialog: HTMLDialogElement): boolean {
  let calledBack = false;
  closeDialog(dialog, () => {
    calledBack = true;
  });
  return dialog.classList.contains("is-leaving") && !calledBack;
}

function makeDialog(): HTMLDialogElement {
  const d = document.createElement("dialog");
  document.body.appendChild(d);
  return d;
}

describe("openDialog / closeDialog", () => {
  it("openDialog opens the dialog", () => {
    const d = makeDialog();
    openDialog(d);
    expect(d.open).toBe(true);
  });

  it("closeDialog adds is-leaving, then closes + fires onClosed via the fallback", () => {
    const d = makeDialog();
    openDialog(d);
    const onClosed = vi.fn();
    closeDialog(d, onClosed);
    expect(d.classList.contains("is-leaving")).toBe(true);
    expect(d.open).toBe(true);
    vi.advanceTimersByTime(400);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(false);
    expect(onClosed).toHaveBeenCalledOnce();
  });

  it("closeDialog completes on transitionend before the fallback fires", () => {
    const d = makeDialog();
    openDialog(d);
    const onClosed = vi.fn();
    closeDialog(d, onClosed);
    d.dispatchEvent(new Event("transitionend"));
    expect(d.open).toBe(false);
    expect(onClosed).toHaveBeenCalledOnce();
  });

  it("closeDialog on an already-closed dialog fires onClosed immediately", () => {
    const d = makeDialog();
    const onClosed = vi.fn();
    closeDialog(d, onClosed);
    expect(onClosed).toHaveBeenCalledOnce();
  });
});

describe("createDialog", () => {
  it("closes when a backdrop press starts and ends on the dialog element", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();
    expect(d.classList.contains("uip-dialog")).toBe(true);
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
    ctrl.dispose();
  });

  it("does not close when the press starts inside content (drag-select safe)", () => {
    const d = makeDialog();
    const inner = document.createElement("button");
    d.appendChild(inner);
    const ctrl = createDialog(d);
    ctrl.open();
    inner.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);
    ctrl.dispose();
  });

  it("closes on Escape via the cancel event and fires onClose", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
    expect(onClose).toHaveBeenCalledOnce();
    ctrl.dispose();
  });

  it("stays open when a nested file input's picker-dismiss cancel bubbles up", () => {
    const d = makeDialog();
    const file = document.createElement("input");
    file.type = "file";
    d.appendChild(file);
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    const cancel = new Event("cancel", { bubbles: true, cancelable: true });
    file.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    expect(d.classList.contains("is-leaving")).toBe(false);
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
    ctrl.dispose();
  });

  it("ignores backdrop clicks when closeOnBackdrop is false", () => {
    const d = makeDialog();
    const ctrl = createDialog(d, { closeOnBackdrop: false });
    ctrl.open();
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    ctrl.dispose();
  });

  it("dispose removes listeners and the uip-dialog class", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();
    ctrl.dispose();
    expect(d.classList.contains("uip-dialog")).toBe(false);
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);
  });

  it("dispose closes an open dialog at once, without onClose", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.dispose();

    expect(d.open).toBe(false);
    vi.runAllTimers();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("dispose during the leave fade closes at once and drops the pending settle", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    expect(d.classList.contains("is-leaving")).toBe(true);
    ctrl.dispose();

    expect(d.open).toBe(false);
    expect(d.classList.contains("is-leaving")).toBe(false);
    vi.runAllTimers();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("dispose stops intercepting Escape, leaving the platform close to run", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();
    ctrl.dispose();
    d.showModal();
    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    expect(d.classList.contains("is-leaving")).toBe(false);
  });
});

describe("createDialog: canDismiss guard", () => {
  function backdropPress(d: HTMLDialogElement): void {
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
  }

  it("refuses backdrop and Escape dismissal while the guard returns false, and stays armed", () => {
    const d = makeDialog();
    let allowed = false;
    const canDismiss = vi.fn(() => allowed);
    const ctl = createDialog(d, { canDismiss });
    ctl.open();

    backdropPress(d);
    expect(d.open).toBe(true);

    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(d.open).toBe(true);
    expect(canDismiss).toHaveBeenCalledTimes(2);

    // A refusal must not disarm the wiring; a later attempt succeeds once allowed.
    allowed = true;
    backdropPress(d);
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
    expect(canDismiss).toHaveBeenCalledTimes(3);
  });

  it("programmatic close() ignores the guard", () => {
    const d = makeDialog();
    const canDismiss = vi.fn(() => false);
    const ctl = createDialog(d, { canDismiss });
    ctl.open();
    ctl.close();
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
    expect(canDismiss).not.toHaveBeenCalled();
  });
});

describe("openDialog: reopen during the leave fade", () => {
  it("cancels the pending close so the stale finalizer can't yank the dialog shut", () => {
    const d = makeDialog();
    openDialog(d);
    closeDialog(d);
    expect(d.classList.contains("is-leaving")).toBe(true);

    openDialog(d); // reopen mid-fade
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);

    vi.advanceTimersByTime(400); // stale finalizer must be a no-op
    expect(d.open).toBe(true);
  });
});

describe("openDialog / closeDialog: platform calls and degradation", () => {
  it("closes through the native close() so the dialog's own close event fires", async () => {
    const d = makeDialog();
    openDialog(d);
    // close() queues the close event as a task, so await delivery rather than
    // asserting right after the fade finalizer runs.
    const delivered = new Promise<void>((resolve) => {
      d.addEventListener("close", () => resolve(), { once: true });
    });

    closeDialog(d);
    await vi.advanceTimersByTimeAsync(400);
    await delivered;
    expect(d.open).toBe(false);
  });
});

describe("createDialog: a backdrop dismissal needs its own complete press", () => {
  it("ignores a release with no press before it (a drag that began off-window)", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();

    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);
    ctrl.dispose();
  });

  it("does not close when the press starts on the backdrop but ends on content", () => {
    const d = makeDialog();
    const inner = document.createElement("button");
    d.appendChild(inner);
    const ctrl = createDialog(d);
    ctrl.open();

    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    inner.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);
    ctrl.dispose();
  });

  it("consumes the press, so the next release alone does not dismiss", () => {
    const d = makeDialog();
    const inner = document.createElement("button");
    d.appendChild(inner);
    const ctrl = createDialog(d);
    ctrl.open();

    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    inner.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);
    ctrl.dispose();
  });
});

describe("createDialog: a refused dismissal changes nothing", () => {
  it("does not start the leave fade when the guard refuses", () => {
    const d = makeDialog();
    const ctl = createDialog(d, { canDismiss: () => false });
    ctl.open();

    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(d.classList.contains("is-leaving")).toBe(false);

    vi.advanceTimersByTime(400);
    expect(d.open).toBe(true);
    ctl.dispose();
  });

  it("ignores the cancel event when closeOnEscape is false (still preventDefaults it)", () => {
    const d = makeDialog();
    const ctl = createDialog(d, { closeOnEscape: false });
    ctl.open();

    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(d.open).toBe(true);
    ctl.dispose();
  });
});

describe("openDialog uses the platform's modal API", () => {
  it("opens through showModal(), not by setting the attribute", () => {
    const d = makeDialog();
    const showModal = vi.spyOn(d, "showModal");

    openDialog(d);

    // showModal() buys focus containment, the top layer and inertness that
    // `open = true` alone does not, so the CALL is what this test pins.
    expect(showModal).toHaveBeenCalledOnce();
    expect(d.open).toBe(true);
  });
});

describe("listenBackdrop: the cleanup is complete", () => {
  it("a press begun before the cleanup cannot dismiss after it", () => {
    const d = makeDialog();
    const onDismiss = vi.fn();
    const cleanup = listenBackdrop(d, onDismiss);

    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); // press on the backdrop
    cleanup();
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));

    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("gives back both listeners it took, not just one", () => {
    const d = makeDialog();
    const add = vi.spyOn(d, "addEventListener");
    const remove = vi.spyOn(d, "removeEventListener");
    const types = (spy: typeof add): string[] =>
      (spy.mock.calls as unknown[][]).map((c) => String(c[0])).sort();

    const cleanup = listenBackdrop(d, vi.fn());
    expect(types(add)).toEqual(["mousedown", "mouseup"]);

    cleanup();

    // A half-cleanup leaks a closure on an app-owned element permanently.
    expect(types(remove)).toEqual(["mousedown", "mouseup"]);
  });
});

describe("dialog: a binding ends with the reactive owner it was made under", () => {
  it("createDialog is disposed with its root, and its own dispose() after that is a no-op", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const [disposeRoot, ctrl] = createRoot(
      (dispose) => [dispose, createDialog(d, { onClose })] as const,
    );
    ctrl.open();

    disposeRoot();
    expect(d.open).toBe(false);
    expect(d.classList.contains("uip-dialog")).toBe(false);
    d.showModal();
    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);

    ctrl.dispose();
    expect(d.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
    d.close();
  });
});

/** Call `make` while a reactive owner is being disposed; return what it threw. */
function thrownWhileOwnerCloses(make: () => unknown): unknown {
  let thrown: unknown;
  createRoot((dispose, owner) => {
    owner.onDispose(() => {
      try {
        make();
      } catch (error) {
        thrown = error;
      }
    });
    dispose();
  });
  return thrown;
}

const CLOSING_OWNER = /cannot create a root under an owner that is being disposed/;

describe("dialog: a binding made while its owner is being disposed acquires nothing", () => {
  it("createDialog throws before adding its class, a listener or the dialog's engine", () => {
    const d = makeDialog();
    const add = vi.spyOn(d, "addEventListener");

    expect(String(thrownWhileOwnerCloses(() => createDialog(d)))).toMatch(CLOSING_OWNER);
    expect(existingEngine(d)).toBeUndefined();
    expect(add).not.toHaveBeenCalled();
    expect(d.classList.contains("uip-dialog")).toBe(false);
  });
});

describe("createDialog: its owner disposed while the dialog is being set up", () => {
  it("unwires once after setup, with no class or cancel handling left", () => {
    let disposeOwner = (): void => undefined;
    class OwnerClosingDialog extends HTMLDialogElement {
      static observedAttributes = ["class"];
      attributeChangedCallback(): void {
        disposeOwner();
      }
    }
    customElements.define("uip-test-owner-closing-dialog", OwnerClosingDialog, {
      extends: "dialog",
    });
    const d = document.createElement("dialog", { is: "uip-test-owner-closing-dialog" });
    document.body.appendChild(d);

    const ctrl = createRoot((dispose) => {
      disposeOwner = dispose;
      return createDialog(d);
    });
    expect(d.classList.contains("uip-dialog")).toBe(false);
    d.showModal();
    const cancel = new Event("cancel", { cancelable: true });
    d.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    d.close();
    ctrl.dispose();
  });
});

describe("createDialog: after dispose()", () => {
  it("open() throws an InvalidStateError and close() runs no onClose", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.dispose();

    let thrown: unknown;
    try {
      ctrl.open();
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(DOMException);
    expect((thrown as DOMException).name).toBe("InvalidStateError");
    expect(d.open).toBe(false);
    ctrl.close();
    vi.runAllTimers();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("createDialog: disposed from inside its own dismissal", () => {
  it("a canDismiss that disposes the dialog runs no onClose and leaves it closed", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    ctrl.current = createDialog(d, {
      canDismiss: () => {
        ctrl.current?.dispose();
        return true;
      },
      onClose,
    });
    ctrl.current.open();

    d.dispatchEvent(new Event("cancel", { cancelable: true }));
    vi.runAllTimers();

    expect(onClose).not.toHaveBeenCalled();
    expect(d.open).toBe(false);
    expect(d.classList.contains("uip-dialog")).toBe(false);
  });
});

describe("createDialog: an owner disposed from the setup class write", () => {
  it("ends the controller at once, so a cancel the same callback dispatches runs no onClose", () => {
    const d = reactingDialog();
    document.body.append(d);
    const onClose = vi.fn();
    let disposeOwner = (): void => undefined;
    d.onAttr = (name, value) => {
      if (name === "class" && classHas(value, "uip-dialog")) {
        d.onAttr = null;
        disposeOwner();
        d.dispatchEvent(new Event("cancel", { cancelable: true }));
      }
    };
    createRoot((dispose) => {
      disposeOwner = dispose;
      createDialog(d, { onClose });
    });
    vi.runAllTimers();

    expect(onClose).not.toHaveBeenCalled();
    expect(d.classList.contains("uip-dialog")).toBe(false);
  });
});

describe("createDialog: reopened from the class write of its own close", () => {
  it("stays open after the settle, with no leave left armed", () => {
    const d = reactingDialog();
    document.body.append(d);
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    d.onAttr = (name, value) => {
      if (name === "class" && classHas(value, "is-leaving")) {
        d.onAttr = null;
        ctrl.open();
      }
    };
    ctrl.close();
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("createDialog: state changed while canDismiss runs", () => {
  it("a dispose from the guard starts no leave after it returns", () => {
    const d = reactingDialog();
    document.body.append(d);
    const leaves: string[] = [];
    const onClose = vi.fn();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    ctrl.current = createDialog(d, {
      canDismiss: () => {
        ctrl.current?.dispose();
        d.onAttr = (name, value) => {
          if (name === "class" && classHas(value, "is-leaving")) {
            leaves.push("is-leaving added after dispose");
          }
        };
        return true;
      },
      onClose,
    });
    ctrl.current.open();

    d.dispatchEvent(new Event("cancel", { cancelable: true }));
    vi.runAllTimers();

    expect(leaves).toEqual([]);
    expect(onClose).not.toHaveBeenCalled();
    expect(d.open).toBe(false);
  });

  it("a close then open from the guard discards its answer: the dialog stays open", () => {
    const d = document.createElement("dialog");
    document.body.append(d);
    const onClose = vi.fn();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    ctrl.current = createDialog(d, {
      canDismiss: () => {
        ctrl.current?.close();
        ctrl.current?.open();
        return true;
      },
      onClose,
    });
    ctrl.current.open();

    d.dispatchEvent(new Event("cancel", { cancelable: true }));
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
  });
});

/** A dialog with a focusable child and a method=dialog form, opened from a focused opener. */
function stage(): { d: HTMLDialogElement; opener: HTMLButtonElement; form: HTMLFormElement } {
  const opener = document.createElement("button");
  const d = document.createElement("dialog");
  const form = document.createElement("form");
  form.method = "dialog";
  form.append(document.createElement("button"));
  d.append(form);
  document.body.append(opener, d);
  opener.focus();
  return { d, opener, form };
}

describe("createDialog: a close the browser made", () => {
  it("open() right after a raw close reopens it with no onClose, even when onClose disposes", async () => {
    const { d } = stage();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    const onClose = vi.fn(() => {
      ctrl.current?.dispose();
    });
    ctrl.current = createDialog(d, { onClose });
    ctrl.current.open();
    d.close();
    ctrl.current.open();
    await nextTask();

    expect(d.open).toBe(true);
    expect(d.classList.contains("uip-dialog")).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("an open() from the opener's focus restoration inside the raw close keeps it open with no onClose", async () => {
    const { d, opener } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    opener.addEventListener("focus", () => ctrl.open(), { once: true });
    d.close();
    await nextTask();

    expect(d.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("dispose() from that focus restoration throws nothing and calls no throwing onClose", async () => {
    const { d, opener } = stage();
    const onClose = vi.fn(() => {
      throw new Error("onClose");
    });
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    let thrown: unknown = null;
    opener.addEventListener(
      "focus",
      () => {
        try {
          ctrl.dispose();
        } catch (error) {
          thrown = error;
        }
      },
      { once: true },
    );
    d.close();
    await nextTask();

    expect(thrown).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(d.classList.contains("uip-dialog")).toBe(false);
    expect(() => createDialog(d)).not.toThrow();
  });

  it("a raw close and a raw showModal() from its focus restoration, during the fade, leave it open", async () => {
    const { d, opener } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    opener.addEventListener(
      "focus",
      () => {
        d.showModal();
      },
      { once: true },
    );
    d.close();
    await nextTask();
    await nextTask();
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("a raw close with no later call is reported once at the next microtask", async () => {
    const { d, form } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    form.requestSubmit();
    expect(d.open).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
    await Promise.resolve();

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("a raw close in the focusing steps, after an open() from the opening beforetoggle, stays closed", async () => {
    const { d } = stage();
    const ctrl = createDialog(d);
    d.addEventListener(
      "beforetoggle",
      () => {
        ctrl.open();
      },
      { once: true },
    );
    d.addEventListener(
      "focusin",
      () => {
        d.close();
      },
      { once: true },
    );
    ctrl.open();
    await nextTask();

    expect(d.open).toBe(false);
  });

  it("dispose() and closeDialog() inside a form close's beforetoggle start no fade and call back once closed", async () => {
    const { d, form } = stage();
    const ctrl = createDialog(d);
    ctrl.open();
    // Each record's old value is the class list before one write; together with the
    // final list they are every class list the dialog had.
    const classLists: (string | null)[] = [];
    const mo = new MutationObserver((records) => {
      classLists.push(...records.map((r) => r.oldValue));
    });
    mo.observe(d, { attributes: true, attributeFilter: ["class"], attributeOldValue: true });
    let thrown: unknown = null;
    const openAtCallback: boolean[] = [];
    d.addEventListener(
      "beforetoggle",
      () => {
        try {
          ctrl.dispose();
          closeDialog(d, () => openAtCallback.push(d.open));
        } catch (error) {
          thrown = error;
        }
      },
      { once: true },
    );
    form.requestSubmit();
    await nextTask();
    classLists.push(...mo.takeRecords().map((r) => r.oldValue), d.getAttribute("class"));
    mo.disconnect();

    expect(thrown).toBeNull();
    expect(d.open).toBe(false);
    expect(openAtCallback).toEqual([false]);
    expect(classLists.some((c) => classHas(c, "is-leaving"))).toBe(false);
  });

  it("a raw showModal() is adopted, and close() then fades it with one onClose", async () => {
    const { d } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    d.showModal();
    await nextTask();
    ctrl.close();
    expect(d.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("createDialog: a raw showModal() from the focus restoration of its own close", () => {
  it("ends open and followed, with no onClose", async () => {
    const { d, opener } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    opener.addEventListener(
      "focus",
      () => {
        d.showModal();
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);
    await nextTask();

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
    expect(heldOpen(d)).toBe(true);
  });
});

describe("closeDialog on a createDialog dialog", () => {
  it("fires the controller's onClose once, and its callback still runs when onClose disposes", () => {
    const { d } = stage();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    const onClose = vi.fn(() => {
      ctrl.current?.dispose();
    });
    ctrl.current = createDialog(d, { onClose });
    ctrl.current.open();
    const cb = vi.fn();
    closeDialog(d, cb);
    vi.advanceTimersByTime(400);

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledTimes(1);
    expect(d.open).toBe(false);
  });

  it("a callback given for an already-closed dialog from onClose survives a reopen there", () => {
    const { d } = stage();
    const after = vi.fn();
    const ctrl = createDialog(d, {
      onClose: () => {
        closeDialog(d, after);
        openDialog(d);
      },
    });
    ctrl.open();
    ctrl.close();
    vi.advanceTimersByTime(400);

    expect(after).toHaveBeenCalledTimes(1);
    expect(d.open).toBe(true);
  });

  it("a callback given during the settle's class removal runs once, after the one fade closes", () => {
    const d = reactingDialog();
    document.body.append(d);
    openDialog(d);
    const order: string[] = [];
    closeDialog(d, () => order.push("cb0"));
    d.onAttr = (name, value) => {
      if (name === "class" && !classHas(value, "is-leaving")) {
        d.onAttr = null;
        closeDialog(d, () => order.push("cb1"));
      }
    };
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(false);
    expect(order).toEqual(["cb0", "cb1"]);
    expect(vi.getTimerCount()).toBe(0);
    expect(d.classList.contains("is-leaving")).toBe(false);
  });
});

describe("createDialog: an open a beforetoggle listener cancels", () => {
  it("leaves the dialog closed, with no rank", () => {
    const { d } = stage();
    const cancel = (e: Event): void => {
      e.preventDefault();
    };
    d.addEventListener("beforetoggle", cancel);
    const ctrl = createDialog(d);
    ctrl.open();

    expect(d.open).toBe(false);
    expect(topmostOpenDialog()).not.toBe(d);
    d.removeEventListener("beforetoggle", cancel);
    ctrl.dispose();
  });
});

describe("closeDialog callbacks and a controller hook that changes the dialog", () => {
  it("a callback still runs once when onClose reopens the dialog", () => {
    const { d } = stage();
    const after = vi.fn();
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    let reopened = false;
    ctrl.current = createDialog(d, {
      onClose: () => {
        if (!reopened) {
          reopened = true;
          ctrl.current?.open();
        }
      },
    });
    ctrl.current.open();
    closeDialog(d, after);
    vi.advanceTimersByTime(400);

    expect(after).toHaveBeenCalledTimes(1);
    expect(d.open).toBe(true);
  });

  it("a callback given in the settle's closing beforetoggle runs once when dispose() follows it there", () => {
    const { d } = stage();
    const after = vi.fn();
    const ctrl = createDialog(d);
    ctrl.open();
    ctrl.close();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          closeDialog(d, after);
          ctrl.dispose();
        }
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(false);
    expect(after).toHaveBeenCalledTimes(1);
  });
});

describe("createDialog: disposed and replaced from its own onClose", () => {
  it("the new controller keeps its class and its one set of listeners", () => {
    const { d } = stage();
    const second = vi.fn();
    let next: ReturnType<typeof createDialog> | undefined;
    const ctrl: { current?: ReturnType<typeof createDialog> } = {};
    ctrl.current = createDialog(d, {
      onClose: () => {
        ctrl.current?.dispose();
        next = createDialog(d, { onClose: second });
      },
    });
    ctrl.current.open();
    ctrl.current.close();
    vi.advanceTimersByTime(400);

    expect(d.classList.contains("uip-dialog")).toBe(true);
    next?.open();
    d.dispatchEvent(new Event("cancel", { cancelable: true }));
    vi.advanceTimersByTime(400);
    expect(second).toHaveBeenCalledTimes(1);
  });
});

describe("createDialog: one controller per dialog", () => {
  it("a second createDialog throws InvalidStateError and acquires nothing; after dispose it succeeds", () => {
    const { d } = stage();
    const first = createDialog(d);
    expect(() => createDialog(d)).toThrow(expect.objectContaining({ name: "InvalidStateError" }));
    first.dispose();
    expect(() => createDialog(d)).not.toThrow();
    expect(d.classList.contains("uip-dialog")).toBe(true);
  });

  it("a pending close the browser made is realized before the controller attaches", () => {
    const { d } = stage();
    openDialog(d);
    const boom = new Error("foreign callback");
    closeDialog(d, () => {
      throw boom;
    });
    d.close();

    expect(() => createDialog(d)).toThrow(boom);
    expect(d.classList.contains("uip-dialog")).toBe(false);
    expect(() => createDialog(d)).not.toThrow();
  });

  it("an owner a pending close's callback disposes while the controller starts leaves nothing wired", () => {
    const { d } = stage();
    openDialog(d);
    let disposeOwner = (): void => undefined;
    closeDialog(d, () => {
      disposeOwner();
    });
    d.close();
    createRoot((dispose) => {
      disposeOwner = dispose;
      createDialog(d);
    });

    expect(d.classList.contains("uip-dialog")).toBe(false);
    const escape = new Event("cancel", { cancelable: true });
    d.dispatchEvent(escape);
    expect(escape.defaultPrevented).toBe(false);
    expect(() => createDialog(d)).not.toThrow();
  });
});

describe("dialog: the page rewrites the open attribute it already has", () => {
  it("dialog.open = true during a closeDialog fade: the fade still closes and calls back", async () => {
    const d = makeDialog();
    openDialog(d);
    const cb = vi.fn();
    closeDialog(d, cb);
    d.open = true;
    await nextTask();
    await nextTask();
    vi.advanceTimersByTime(400);
    await nextTask();

    expect(d.open).toBe(false);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(cb).toHaveBeenCalledTimes(1);
  });

  it("setAttribute('open', '') during a controller's fade: onClose still fires", async () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    d.setAttribute("open", "");
    await nextTask();
    await nextTask();
    vi.advanceTimersByTime(400);
    await nextTask();

    expect(d.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("dialog.open = true on an open dialog moves no rank: a dialog opened after it stays on top", async () => {
    const a = makeDialog();
    const b = makeDialog();
    openDialog(a);
    openDialog(b);
    a.open = true;
    await nextTask();
    openDialog(a);

    expect(topmostOpenDialog()).toBe(b);
  });

  it("showModal() on a dialog that is fading changes nothing: the fade still closes and calls back", async () => {
    const d = makeDialog();
    openDialog(d);
    const cb = vi.fn();
    closeDialog(d, cb);
    d.showModal();
    await nextTask();

    expect(d.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    await nextTask();
    expect(d.open).toBe(false);
    expect(cb).toHaveBeenCalledTimes(1);
  });

  it("close() then showModal() during a fade reopens the dialog and drops the fade and its callback", async () => {
    const d = makeDialog();
    openDialog(d);
    const cb = vi.fn();
    closeDialog(d, cb);
    d.close();
    d.showModal();
    await nextTask();

    expect(d.classList.contains("is-leaving")).toBe(false);
    vi.advanceTimersByTime(400);
    await nextTask();
    expect(d.open).toBe(true);
    expect(cb).not.toHaveBeenCalled();
  });
});

describe("createDialog: closed and reopened by the page inside its own showModal", () => {
  /** A and B controllers; A's first focusin, inside A's showModal, runs `during`. */
  function twoDialogs(
    during: (
      a: HTMLDialogElement,
      ca: ReturnType<typeof createDialog>,
      cb: ReturnType<typeof createDialog>,
    ) => void,
  ): {
    a: HTMLDialogElement;
    b: HTMLDialogElement;
    ca: ReturnType<typeof createDialog>;
  } {
    const a = stage().d;
    const b = stage().d;
    const ca = createDialog(a);
    const cb = createDialog(b);
    a.addEventListener(
      "focusin",
      () => {
        during(a, ca, cb);
      },
      { once: true },
    );
    return { a, b, ca };
  }

  it("a raw close, another dialog's open and a raw showModal() rank the reopened dialog on top before open() returns", () => {
    const { a, b, ca } = twoDialogs((a, _ca, cb) => {
      a.close();
      cb.open();
      a.showModal();
    });
    ca.open();

    expect(a.open && b.open).toBe(true);
    expect(topmostOpenDialog()).toBe(a);
  });

  it("the same with an open() after the reopen still ranks it on top", async () => {
    const { a, b, ca } = twoDialogs((a, ca, cb) => {
      a.close();
      cb.open();
      a.showModal();
      ca.open();
    });
    ca.open();
    const atReturn = topmostOpenDialog();
    await nextTask();
    await nextTask();

    expect(a.open && b.open).toBe(true);
    expect(atReturn).toBe(a);
    expect(topmostOpenDialog()).toBe(a);
  });
});

describe("createDialog: the page closes and reopens the dialog while canDismiss runs", () => {
  it("discards the guard's answer: the dialog stays open with no fade", () => {
    const { d } = stage();
    const ctrl = createDialog(d, {
      canDismiss: () => {
        d.close();
        d.showModal();
        return true;
      },
    });
    ctrl.open();
    d.dispatchEvent(new Event("cancel", { cancelable: true }));

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
  });
});

describe("dialog: the page reopens and closes a dialog around the engine's own close", () => {
  it("a raw showModal() from the focus restoration of the settle's close holds the dialog when the settle returns", () => {
    const { d, opener } = stage();
    openDialog(d);
    closeDialog(d);
    opener.addEventListener(
      "focus",
      () => {
        d.showModal();
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(topmostOpenDialog()).toBe(d);
    expect(heldOpen(d)).toBe(true);
  });

  it("after a dispose inside a form close's beforetoggle, a raw reopen and close are both followed", async () => {
    const { d, form } = stage();
    const ctrl = createDialog(d);
    ctrl.open();
    d.addEventListener(
      "beforetoggle",
      () => {
        ctrl.dispose();
      },
      { once: true },
    );
    form.requestSubmit();
    await nextTask();
    d.showModal();
    await nextTask();
    d.close();
    await nextTask();

    expect(d.open).toBe(false);
    expect(heldOpen(d)).toBe(false);
  });

  it("after a dispose inside a form close's beforetoggle, a raw reopen is followed", async () => {
    const { d, form } = stage();
    const ctrl = createDialog(d);
    ctrl.open();
    d.addEventListener(
      "beforetoggle",
      () => {
        ctrl.dispose();
      },
      { once: true },
    );
    form.requestSubmit();
    await nextTask();
    d.showModal();
    await nextTask();

    expect(heldOpen(d)).toBe(true);
  });
});

describe("createDialog: the page shows the dialog from the focus restoration of the settle's close", () => {
  it("then follows the page's close() of it", async () => {
    const { d, opener } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    opener.addEventListener(
      "focus",
      () => {
        d.show();
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);
    await nextTask();
    expect(d.open).toBe(true);
    onClose.mockClear();

    d.close();
    await nextTask();
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(heldOpen(d)).toBe(false);
  });
});

describe("dialog: a page change no toggle event reports", () => {
  it("showModal() inside the dialog's own close toggle event is adopted with its rank", async () => {
    const { d } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    d.addEventListener(
      "toggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          d.showModal();
        }
      },
      { once: true },
    );
    d.close();
    await nextTask();
    await nextTask();

    expect(d.open).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(topmostOpenDialog()).toBe(d);
    expect(heldOpen(d)).toBe(true);
  });

  it("show() inside the dialog's own close toggle event is adopted", async () => {
    const { d } = stage();
    const ctrl = createDialog(d);
    ctrl.open();
    d.addEventListener(
      "toggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          d.show();
        }
      },
      { once: true },
    );
    d.close();
    await nextTask();
    await nextTask();

    expect(d.open).toBe(true);
    expect(heldOpen(d)).toBe(true);
  });

  it("a close by removing the open attribute is applied at the next microtask", async () => {
    const { d } = stage();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    d.removeAttribute("open");
    await Promise.resolve();

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(topmostOpenDialog()).toBeNull();
  });
});

describe("createDialog: an open() inside the closing beforetoggle of its settle", () => {
  it("keeps the dialog open and followed, with no onClose", () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          ctrl.open();
        }
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(onClose).not.toHaveBeenCalled();
    expect(heldOpen(d)).toBe(true);
  });

  it("keeps the dialog open in a shadow root too", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const d = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(d);
    const ctrl = createDialog(d);
    ctrl.open();
    ctrl.close();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          ctrl.open();
        }
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
  });

  it("an open() and a close() there fade the dialog out again, then close it", () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    ctrl.open();
    ctrl.close();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          ctrl.open();
          ctrl.close();
        }
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);

    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
  });

  it("a page show() there does not stand: the browser removes open after the listeners", async () => {
    const d = makeDialog();
    const onClose = vi.fn();
    const ctrl = createDialog(d, { onClose });
    ctrl.open();
    ctrl.close();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "closed") {
          d.removeAttribute("open");
          d.show();
        }
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);
    await nextTask();

    expect(d.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("dialog: the page opens and closes it before an open() is realized", () => {
  it("an open() from a closeDialog callback, then the page's showModal() and close(), ends closed", async () => {
    const d = makeDialog();
    const ctrl = createDialog(d);
    closeDialog(d, () => {
      ctrl.open();
      d.showModal();
      d.close();
    });
    await nextTask();

    expect(d.open).toBe(false);
    expect(heldOpen(d)).toBe(false);
  });
});

describe("createDialog: the page opens the dialog from the beforetoggle of the engine's own showModal()", () => {
  /** A controller whose open() reaches `during` from the dialog's opening beforetoggle. */
  function openReaching(
    during: (d: HTMLDialogElement, ctrl: ReturnType<typeof createDialog>) => void,
  ): HTMLDialogElement {
    const { d } = stage();
    const ctrl = createDialog(d);
    d.addEventListener(
      "beforetoggle",
      () => {
        during(d, ctrl);
      },
      { once: true },
    );
    ctrl.open();
    return d;
  }

  it("a show() after dispose() keeps the page's dialog open", async () => {
    const d = openReaching((d, ctrl) => {
      ctrl.dispose();
      d.show();
    });
    await nextTask();
    await nextTask();

    expect(d.open).toBe(true);
  });

  it("a showModal() after dispose() keeps the page's modal open", async () => {
    const d = openReaching((d, ctrl) => {
      ctrl.dispose();
      d.showModal();
    });
    await nextTask();
    await nextTask();

    expect(d.matches(":modal")).toBe(true);
  });

  it("a showModal() after dispose(), then a new controller, keeps the page's modal open", async () => {
    const d = openReaching((d, ctrl) => {
      ctrl.dispose();
      d.showModal();
      createDialog(d);
    });
    await nextTask();
    await nextTask();

    expect(d.matches(":modal")).toBe(true);
  });

  it.each(["document", "shadow root"] as const)(
    "a showModal() after dispose() from a capture listener on the %s keeps the page's modal open",
    async (where) => {
      const { d } = stage();
      let root: Document | ShadowRoot = document;
      if (where === "shadow root") {
        const host = document.createElement("div");
        document.body.append(host);
        root = host.attachShadow({ mode: "open" });
        root.append(d);
      }
      const ctrl = createDialog(d);
      const listener = (): void => {
        root.removeEventListener("beforetoggle", listener, true);
        ctrl.dispose();
        d.showModal();
      };
      root.addEventListener("beforetoggle", listener, true);
      ctrl.open();
      await nextTask();
      await nextTask();

      expect(d.matches(":modal")).toBe(true);
    },
  );

  it("a dispose() alone still closes the dialog the engine's own showModal() opened", async () => {
    const d = openReaching((_d, ctrl) => {
      ctrl.dispose();
    });
    await nextTask();
    await nextTask();

    expect(d.open).toBe(false);
  });
});

describe("createDialog: the page closes and shows the dialog again in one task", () => {
  /** A controller over a dialog the page showed; the page then closes it and shows it
   *  again, running `during` from that show()'s beforetoggle. */
  async function reshown(during: (d: HTMLDialogElement) => void): Promise<HTMLDialogElement> {
    const { d } = stage();
    createDialog(d);
    d.show();
    await nextTask();
    d.close();
    d.addEventListener(
      "beforetoggle",
      () => {
        during(d);
      },
      { once: true },
    );
    d.show();
    await nextTask();
    await nextTask();
    return d;
  }

  it.each([
    [
      "an Escape",
      (d: HTMLDialogElement) => d.dispatchEvent(new Event("cancel", { cancelable: true })),
    ],
    [
      "a closeDialog()",
      (d: HTMLDialogElement) => {
        closeDialog(d);
      },
    ],
  ] as const)(
    "%s inside the show() meets the dialog closed, and the page's dialog opens with no fade",
    async (_name, during) => {
      const d = await reshown(during);

      expect(d.open).toBe(true);
      expect(d.classList.contains("is-leaving")).toBe(false);
      vi.advanceTimersByTime(400);
      expect(d.open).toBe(true);
    },
  );

  it("with nothing inside the show(), the page's dialog stays open with no fade", async () => {
    const d = await reshown(() => undefined);

    expect(d.open).toBe(true);
    expect(d.matches(":modal")).toBe(false);
    expect(d.classList.contains("is-leaving")).toBe(false);
  });
});

describe("createDialog: a page open and close in the beforetoggle of its own showModal", () => {
  function closedInsideOpen(dispose: boolean): {
    d: HTMLDialogElement;
    ctl: ReturnType<typeof createDialog>;
  } {
    const d = stage().d;
    const ctl = createDialog(d);
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "open") {
          if (dispose) {
            ctl.dispose();
          }
          d.showModal();
          d.close();
        }
      },
      { once: true },
    );
    return { d, ctl };
  }

  it("keeps a dispose made there: the dialog ends closed", async () => {
    const { d, ctl } = closedInsideOpen(true);
    ctl.open();
    await nextTask();
    expect(d.open).toBe(false);
    expect(existingEngine(d)?.state.phase).toBe("closed");
  });

  it("lets a page close there complete a close() made there, and only that one", async () => {
    const d = stage().d;
    const ctl = createDialog(d);
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "open") {
          ctl.close();
          d.showModal();
          d.close();
        }
      },
      { once: true },
    );
    ctl.open();
    await nextTask();
    expect(d.open).toBe(false);
    ctl.open();
    await nextTask();
    ctl.close();
    await nextTask();
    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(true);
  });

  it("lets the open it interrupted stand: the dialog ends open", async () => {
    const { d, ctl } = closedInsideOpen(false);
    ctl.open();
    await nextTask();
    expect(d.open).toBe(true);
    expect(existingEngine(d)?.state.phase).toBe("open");
  });
});

describe("createDialog: a teardown inside a later open the page refuses", () => {
  it("keeps the callbacks it found, since no fade was cut", async () => {
    const d = makeDialog();
    const ctl = createDialog(d);
    ctl.open();
    await nextTask();
    ctl.close();
    await vi.runAllTimersAsync();
    await nextTask();
    expect(d.open).toBe(false);
    const cb = vi.fn();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "open") {
          closeDialog(d, cb);
          ctl.dispose();
          e.preventDefault();
        }
      },
      { once: true },
    );
    ctl.open();
    await nextTask();
    await nextTask();
    expect(d.open).toBe(false);
    expect(cb).toHaveBeenCalledTimes(1);
  });
});

describe("createDialog: page edges around its own showModal's write", () => {
  function staged(): {
    d: HTMLDialogElement;
    ctl: ReturnType<typeof createDialog>;
    inner: HTMLButtonElement;
  } {
    const { d, form } = stage();
    const inner = form.querySelector("button");
    if (inner === null) {
      throw new Error("no inner button");
    }
    return { d, ctl: createDialog(d), inner };
  }
  const reopen = (d: HTMLDialogElement) => (): void => {
    d.close();
    d.showModal();
  };

  it("ends closed when the page's last edge before the write is a close", async () => {
    const { d, ctl, inner } = staged();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "open") {
          ctl.close();
          // The nested showModal's focus steps run the handler, before the close below.
          inner.addEventListener("focus", reopen(d), { once: true });
          d.showModal();
          d.close();
        }
      },
      { once: true },
    );
    ctl.open();
    await nextTask();
    expect(d.open).toBe(false);
  });

  it("lets a page reopen after the write stand", async () => {
    const { d, ctl, inner } = staged();
    d.addEventListener(
      "beforetoggle",
      (e) => {
        if ((e as ToggleEvent).newState === "open") {
          ctl.close();
          d.showModal();
          d.close();
          // Added after the page's own showModal, so the engine's focus steps run it.
          inner.addEventListener("focus", reopen(d), { once: true });
        }
      },
      { once: true },
    );
    ctl.open();
    await nextTask();
    expect(d.open).toBe(true);
  });
});
