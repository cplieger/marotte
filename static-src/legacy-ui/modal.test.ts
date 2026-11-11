import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { createRoot } from "@cplieger/reactive";

import { closeDialog, createDialog, openDialog } from "./dialog.js";
import { createModal, _resetForTest } from "./modal.js";
import { topmostOpenDialog } from "./modal-host.js";
import { reactingBody } from "./__test-helpers__/reacting.js";
import { nextTask } from "./__test-helpers__/reentry.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  document.body.style.cssText = "";
  document.body.innerHTML = "";
});

/** Content (the modal body) with `count` focusable buttons. */
function makeContent(
  count = 1,
  prefix = "b",
): { content: HTMLElement; buttons: HTMLButtonElement[] } {
  const content = document.createElement("div");
  const buttons: HTMLButtonElement[] = [];
  for (let i = 0; i < count; i++) {
    const btn = document.createElement("button");
    btn.textContent = `${prefix}${String(i)}`;
    content.appendChild(btn);
    buttons.push(btn);
  }
  return { content, buttons };
}

describe("createModal — structure", () => {
  it("wraps content in a native <dialog class='uip-modal'> appended to <body>", () => {
    const { content } = makeContent();
    const m = createModal(content);
    expect(m.el.tagName).toBe("DIALOG");
    expect(m.el.classList.contains("uip-modal")).toBe(true);
    expect(content.classList.contains("uip-modal-dialog")).toBe(true);
    expect(content.parentElement).toBe(m.el);
    expect(m.el.parentElement).toBe(document.body);
    expect(m.el.open).toBe(false);
    expect(m.isOpen).toBe(false);
  });

  it("open() opens the dialog and marks isOpen", () => {
    const m = createModal(makeContent().content);
    m.open();
    expect(m.el.open).toBe(true);
    expect(m.isOpen).toBe(true);
  });

  it("open() focuses an explicit initialFocus element", () => {
    const { content, buttons } = makeContent(2);
    const m = createModal(content, { initialFocus: buttons[1]! });
    m.open();
    expect(document.activeElement).toBe(buttons[1]);
  });

  it("dispose() removes the dialog from the DOM", () => {
    const m = createModal(makeContent().content);
    m.open();
    m.dispose();
    expect(m.el.parentElement).toBeNull();
    expect(m.isOpen).toBe(false);
  });

  it("dispose() hands the caller's content element back without the modal class", () => {
    const { content } = makeContent();
    const m = createModal(content);
    expect(content.classList.contains("uip-modal-dialog")).toBe(true);
    m.dispose();
    expect(content.classList.contains("uip-modal-dialog")).toBe(false);
  });

  it("dispose() during the fade cancels the pending close and its leaving state, so onClose never fires", () => {
    const onClose = vi.fn();
    const m = createModal(makeContent().content, { onClose });
    m.open();
    m.close(); // starts the leave
    m.dispose(); // tears down instead: the pending finalizer must no-op
    expect(m.el.classList.contains("is-leaving")).toBe(false);
    vi.advanceTimersByTime(400);
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe("createModal — ARIA", () => {
  it("auto-detects an [id$='-title'] descendant for aria-labelledby", () => {
    const { content } = makeContent();
    const title = document.createElement("h2");
    title.id = "settings-title";
    content.prepend(title);
    const m = createModal(content);
    expect(m.el.getAttribute("aria-labelledby")).toBe("settings-title");
  });

  it("auto-detects an [id$='-desc'] or [id$='-description'] descendant for aria-describedby", () => {
    const a = makeContent();
    const desc = document.createElement("p");
    desc.id = "a-desc";
    a.content.append(desc);
    expect(createModal(a.content).el.getAttribute("aria-describedby")).toBe("a-desc");

    const b = makeContent();
    const desc2 = document.createElement("p");
    desc2.id = "dialog-description";
    b.content.append(desc2);
    expect(createModal(b.content).el.getAttribute("aria-describedby")).toBe("dialog-description");
  });

  it("honors explicit labelledBy / describedBy over auto-detection", () => {
    const { content } = makeContent();
    const title = document.createElement("h2");
    title.id = "auto-title";
    content.prepend(title);
    const m = createModal(content, { labelledBy: "explicit", describedBy: "body" });
    expect(m.el.getAttribute("aria-labelledby")).toBe("explicit");
    expect(m.el.getAttribute("aria-describedby")).toBe("body");
  });

  it("role alertdialog sets the dialog role + the .uip-modal--alert modifier", () => {
    const m = createModal(makeContent().content, { role: "alertdialog" });
    expect(m.el.getAttribute("role")).toBe("alertdialog");
    expect(m.el.classList.contains("uip-modal--alert")).toBe(true);
  });
});

describe("drag-safe backdrop dismissal", () => {
  it("closes when a press starts AND ends on the dialog element", () => {
    const m = createModal(makeContent().content);
    m.open();
    m.el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    m.el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(m.el.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(false);
  });

  it("does not close when the press starts inside content (drag-select safe)", () => {
    const { content, buttons } = makeContent();
    const m = createModal(content);
    m.open();
    buttons[0]!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    m.el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(m.el.classList.contains("is-leaving")).toBe(false);
    expect(m.isOpen).toBe(true);
  });

  it("ignores backdrop presses when closeOnBackdrop is false", () => {
    const m = createModal(makeContent().content, { closeOnBackdrop: false });
    m.open();
    m.el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    m.el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(m.isOpen).toBe(true);
  });
});

describe("Escape (native cancel event)", () => {
  it("closes via the cancel event and preventDefaults it", () => {
    const m = createModal(makeContent().content);
    m.open();
    const cancel = new Event("cancel", { cancelable: true });
    m.el.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(m.el.classList.contains("is-leaving")).toBe(true);
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(false);
  });

  it("stays open when a nested file input's picker-dismiss cancel bubbles up", () => {
    const { content } = makeContent();
    const file = document.createElement("input");
    file.type = "file";
    content.appendChild(file);
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    const cancel = new Event("cancel", { bubbles: true, cancelable: true });
    file.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    expect(m.el.classList.contains("is-leaving")).toBe(false);
    vi.advanceTimersByTime(400);
    expect(m.isOpen).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("does not close on cancel when closeOnEscape is false (still preventDefaults)", () => {
    const m = createModal(makeContent().content, { closeOnEscape: false });
    m.open();
    const cancel = new Event("cancel", { cancelable: true });
    m.el.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(m.isOpen).toBe(true);
  });
});

describe("iOS-safe scroll-lock (ref-counted)", () => {
  it("pins the body on open and restores it when the modal closes", () => {
    const m = createModal(makeContent().content);
    m.open();
    expect(document.body.style.position).toBe("fixed");
    expect(document.body.style.width).toBe("100%");
    m.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("");
  });

  it("locks once across nested modals and releases only at the last close", () => {
    const m1 = createModal(makeContent().content);
    const m2 = createModal(makeContent().content);
    m1.open();
    expect(document.body.style.position).toBe("fixed");
    m2.open();
    expect(document.body.style.position).toBe("fixed");
    m2.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("fixed"); // m1 still open
    m1.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("");
  });

  it("skips the scroll-lock when scrollLock is false", () => {
    const m = createModal(makeContent().content, { scrollLock: false });
    m.open();
    expect(document.body.style.position).toBe("");
  });

  it("restores the scroll position when the lock is released", () => {
    // A document with no overflow cannot scroll, so scrollY needs real content.
    const spacer = document.createElement("div");
    spacer.style.height = "3000px";
    document.body.appendChild(spacer);
    try {
      window.scrollTo(0, 250);
      expect(window.scrollY).toBe(250); // the premise, not the assertion
      const m = createModal(makeContent().content);
      m.open(); // saves scrollY 250 and pins the body at -250px
      window.scrollTo(0, 0); // a pinned body sits at the top while the modal is up
      m.close();
      vi.advanceTimersByTime(400);
      expect(window.scrollY).toBe(250);
    } finally {
      spacer.remove();
      window.scrollTo(0, 0);
    }
  });

  it("dispose releases the scroll-lock of a still-open modal", () => {
    const m = createModal(makeContent().content);
    m.open();
    expect(document.body.style.position).toBe("fixed");
    m.dispose();
    expect(document.body.style.position).toBe("");
  });
});

describe("leave lifecycle", () => {
  it("completes on the dialog's transitionend before the fallback, firing onClose", () => {
    const onClose = vi.fn();
    const m = createModal(makeContent().content, { onClose });
    m.open();
    m.close();
    expect(m.el.classList.contains("is-leaving")).toBe(true);
    expect(m.isOpen).toBe(false);
    m.el.dispatchEvent(new Event("transitionend"));
    expect(m.el.open).toBe(false);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("completes via the fallback timeout when transitionend never fires", () => {
    const onClose = vi.fn();
    const m = createModal(makeContent().content, { onClose });
    m.open();
    m.close();
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(false);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("reopening mid fade-out cancels the close and keeps the lock", () => {
    const m = createModal(makeContent().content);
    m.open();
    m.close();
    expect(m.el.classList.contains("is-leaving")).toBe(true);
    m.open(); // revive
    expect(m.el.classList.contains("is-leaving")).toBe(false);
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(true);
    expect(m.isOpen).toBe(true);
    expect(document.body.style.position).toBe("fixed");
  });

  it("close() on an already-closed modal is a no-op", () => {
    const onClose = vi.fn();
    const m = createModal(makeContent().content, { onClose });
    m.close(); // never opened
    vi.advanceTimersByTime(400);
    expect(onClose).not.toHaveBeenCalled();
    expect(m.isOpen).toBe(false);
  });
});

describe("createModal: canDismiss guard", () => {
  it("refuses backdrop and Escape dismissal while the guard returns false, and stays armed", () => {
    const { content } = makeContent();
    let allowed = false;
    const canDismiss = vi.fn(() => allowed);
    const m = createModal(content, { canDismiss });
    m.open();

    m.el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    m.el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    expect(m.isOpen).toBe(true);

    const cancel = new Event("cancel", { cancelable: true });
    m.el.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(true);
    expect(m.isOpen).toBe(true);
    expect(canDismiss).toHaveBeenCalledTimes(2);

    allowed = true;
    m.el.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    m.el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(false);
    expect(canDismiss).toHaveBeenCalledTimes(3);
  });

  it("programmatic close() ignores the guard", () => {
    const { content } = makeContent();
    const canDismiss = vi.fn(() => false);
    const m = createModal(content, { canDismiss });
    m.open();
    m.close();
    vi.advanceTimersByTime(400);
    expect(m.el.open).toBe(false);
    expect(canDismiss).not.toHaveBeenCalled();
  });
});

describe("createModal — default role", () => {
  it("leaves a plain modal on the <dialog> implicit role, with no alert modifier", () => {
    const m = createModal(makeContent().content);
    expect(m.el.hasAttribute("role")).toBe(false);
    expect(m.el.classList.contains("uip-modal--alert")).toBe(false);
  });
});

describe("scroll-lock ref-counting across mixed and revived modals", () => {
  it("a nested scrollLock:false modal does not release the outer modal's lock", () => {
    const outer = createModal(makeContent().content);
    const inner = createModal(makeContent().content, { scrollLock: false });
    outer.open();
    inner.open();
    expect(document.body.style.position).toBe("fixed");

    inner.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("fixed");
  });

  it("disposing a nested modal after closing it does not release the outer lock twice", () => {
    const outer = createModal(makeContent().content);
    const inner = createModal(makeContent().content);
    outer.open();
    inner.open();

    inner.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("fixed");

    inner.dispose();
    expect(document.body.style.position).toBe("fixed");
  });

  it("reopening mid fade-out does not stack a second scroll-lock", () => {
    const m = createModal(makeContent().content);
    m.open();
    m.close();
    m.open(); // revived inside the fade window
    vi.advanceTimersByTime(400);

    m.close();
    vi.advanceTimersByTime(400);
    expect(document.body.style.position).toBe("");
  });

  it("_resetForTest releases a scroll-lock left active by an open modal", () => {
    const m = createModal(makeContent().content);
    m.open();
    expect(document.body.style.position).toBe("fixed");

    _resetForTest();
    expect(document.body.style.position).toBe("");
  });
});

describe("createModal — dispose hands the element back to the platform", () => {
  it("stops intercepting the platform's cancel event", () => {
    const m = createModal(makeContent().content);
    m.open();
    m.dispose();

    // A disposed modal has no fade left to protect, so Escape must go back to
    // meaning what the platform says it means.
    const evt = new Event("cancel", { cancelable: true });
    m.el.dispatchEvent(evt);

    expect(evt.defaultPrevented).toBe(false);
  });

  it("leaves the return value of an already-closed dialog alone", () => {
    const m = createModal(makeContent().content);
    m.open();
    // A <form method="dialog"> submit also closes the dialog and records why
    // in returnValue; tearing the wrapper down must not overwrite it.
    m.el.close("save");
    expect(m.el.returnValue).toBe("save");

    m.dispose();

    expect(m.el.returnValue).toBe("save");
  });
});

describe("scroll-lock accounting cannot go negative", () => {
  it("a release that outlives the count it belonged to does not break the next lock", () => {
    const a = createModal(makeContent().content);
    a.open();
    expect(document.body.style.position).toBe("fixed");

    _resetForTest(); // the shared count is cleared while `a` still holds a lock
    a.dispose(); // ...so a's release arrives with nothing left to release

    const b = createModal(makeContent().content);
    b.open();

    // An underflowed count leaves this lock one increment short.
    expect(document.body.style.position).toBe("fixed");
    b.dispose();
  });
});

describe("createModal: ends with the reactive owner it was made under", () => {
  it("disposing the root removes the modal, and dispose() after that is a no-op", () => {
    const { content } = makeContent();
    const [disposeRoot, m] = createRoot((dispose) => [dispose, createModal(content)] as const);
    m.open();
    expect(document.body.style.position).toBe("fixed");

    disposeRoot();
    expect(m.el.isConnected).toBe(false);
    expect(document.body.style.position).toBe("");

    document.body.appendChild(m.el);
    m.dispose();
    expect(m.el.isConnected).toBe(true);
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

describe("createModal: made while its owner is being disposed", () => {
  it("throws before building or mounting a dialog", () => {
    const { content } = makeContent();
    const host = document.createElement("section");
    host.appendChild(content);
    document.body.appendChild(host);

    expect(String(thrownWhileOwnerCloses(() => createModal(content)))).toMatch(
      /cannot create a root under an owner that is being disposed/,
    );
    expect(document.querySelector("dialog")).toBeNull();
    expect(content.parentElement).toBe(host);
    expect(content.classList.contains("uip-modal-dialog")).toBe(false);
  });
});

describe("createModal: its owner disposed while the modal is being set up", () => {
  let disposeOnConnect: (() => void) | null = null;
  customElements.define(
    "uip-test-owner-closing-content",
    class extends HTMLElement {
      connectedCallback(): void {
        const dispose = disposeOnConnect;
        disposeOnConnect = null;
        dispose?.();
      }
    },
  );

  it.each([{ closeOnBackdrop: false }, { closeOnBackdrop: true }])(
    "removes it once after setup, with no cancel handling left (%o)",
    (opts) => {
      const content = document.createElement("uip-test-owner-closing-content");

      const m = createRoot((dispose) => {
        disposeOnConnect = dispose;
        return createModal(content, opts);
      });
      expect(m.el.isConnected).toBe(false);
      expect(content.classList.contains("uip-modal-dialog")).toBe(false);
      const evt = new Event("cancel", { cancelable: true });
      m.el.dispatchEvent(evt);
      expect(evt.defaultPrevented).toBe(false);

      document.body.appendChild(m.el);
      m.dispose();
      expect(m.el.isConnected).toBe(true);
    },
  );
});

describe("createModal: reopened by caller code its dispose reaches", () => {
  it("open() from the restored focus throws and takes no scroll lock", () => {
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const m = createModal(makeContent().content);
    m.open();
    let thrown: unknown;
    opener.addEventListener(
      "focus",
      () => {
        try {
          m.open();
        } catch (error) {
          thrown = error;
        }
      },
      { once: true },
    );

    m.dispose();
    expect(document.activeElement).toBe(opener);
    expect(thrown).toBeInstanceOf(DOMException);
    expect((thrown as DOMException).name).toBe("InvalidStateError");
    expect(m.el.open).toBe(false);
    expect(document.body.style.position).toBe("");
  });
});

function locked(): boolean {
  const st = document.body.style;
  return (
    st.position === "fixed" &&
    st.left === "0px" &&
    st.right === "0px" &&
    st.width === "100%" &&
    st.overflow === "hidden" &&
    (st.top === "0px" || st.top === "-0px")
  );
}

function unlocked(): boolean {
  const st = document.body.style;
  return (
    st.position === "" &&
    st.top === "" &&
    st.left === "" &&
    st.right === "" &&
    st.width === "" &&
    st.overflow === ""
  );
}

describe("createModal: one engine per dialog", () => {
  it("a plain open() holds the scroll lock with all six body styles", () => {
    const { content } = makeContent();
    const m = createModal(content);
    m.open();
    expect(locked()).toBe(true);
  });

  it("openDialog(modal.el) drives the modal: lock, initial focus and onClose follow", () => {
    const { content, buttons } = makeContent(2);
    const onClose = vi.fn();
    const m = createModal(content, { initialFocus: buttons[1] ?? null, onClose });
    openDialog(m.el);
    expect(m.isOpen).toBe(true);
    expect(locked()).toBe(true);
    expect(document.activeElement).toBe(buttons[1]);
    closeDialog(m.el);
    vi.advanceTimersByTime(400);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(unlocked()).toBe(true);
  });

  it("createDialog(modal.el) throws InvalidStateError", () => {
    const { content } = makeContent();
    const m = createModal(content);
    expect(() => createDialog(m.el)).toThrow(
      expect.objectContaining({ name: "InvalidStateError" }),
    );
  });

  it("once disposed, openDialog(modal.el) throws and closeDialog(modal.el, cb) calls cb at once", () => {
    const { content } = makeContent();
    const m = createModal(content);
    m.dispose();
    expect(() => {
      openDialog(m.el);
    }).toThrow(expect.objectContaining({ name: "InvalidStateError" }));
    const cb = vi.fn();
    closeDialog(m.el, cb);
    expect(cb).toHaveBeenCalledTimes(1);
  });
});

describe("createModal: the browser opens or closes it", () => {
  it("a raw showModal() is adopted with its scroll lock at the next microtask", async () => {
    const { content } = makeContent();
    const m = createModal(content);
    m.el.showModal();
    expect(m.isOpen).toBe(true);
    await Promise.resolve();
    expect(locked()).toBe(true);
  });

  it("open() from a form close's focus restoration reopens it, locked, with no onClose", async () => {
    const { content } = makeContent();
    const form = document.createElement("form");
    form.method = "dialog";
    const submit = document.createElement("button");
    form.append(submit);
    content.append(form);
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    let seen: boolean | null = null;
    opener.addEventListener(
      "focus",
      () => {
        seen = m.isOpen;
        m.open();
      },
      { once: true },
    );
    form.requestSubmit();
    await nextTask();

    expect(seen).toBe(false);
    expect(m.isOpen).toBe(true);
    expect(locked()).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("a raw showModal() from the focus restoration of its own close ends open, locked and quiet", async () => {
    const { content } = makeContent();
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    m.close();
    opener.addEventListener(
      "focus",
      () => {
        m.el.showModal();
      },
      { once: true },
    );
    vi.advanceTimersByTime(400);
    await nextTask();

    expect(m.isOpen).toBe(true);
    expect(locked()).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("dispose() inside a form close's beforetoggle removes the closed modal and releases the lock", async () => {
    const { content } = makeContent();
    const form = document.createElement("form");
    form.method = "dialog";
    form.append(document.createElement("button"));
    content.append(form);
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    let thrown: unknown = null;
    m.el.addEventListener(
      "beforetoggle",
      () => {
        try {
          m.dispose();
        } catch (error) {
          thrown = error;
        }
      },
      { once: true },
    );
    form.requestSubmit();
    await nextTask();

    expect(thrown).toBeNull();
    expect(m.el.open).toBe(false);
    expect(m.el.isConnected).toBe(false);
    expect(unlocked()).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("a raw close and a reopen in one task re-rank the reopened modal on top", () => {
    const a = createModal(makeContent(1, "a").content);
    const b = createModal(makeContent(1, "b").content);
    a.open();
    b.open();
    a.el.close();
    a.open();
    expect(topmostOpenDialog()).toBe(a.el);
  });
});

describe("createModal: a disposed modal's dialog the page shows again", () => {
  it("is closed in the same microtask, also when the page reopens it inside that close", async () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    const m = createModal(makeContent().content);
    m.dispose();
    document.body.append(m.el);
    opener.focus();
    let reopened = false;
    opener.addEventListener(
      "focus",
      () => {
        reopened = true;
        m.el.showModal();
      },
      { once: true },
    );
    m.el.showModal();
    await Promise.resolve();

    expect(reopened).toBe(true);
    expect(m.el.open).toBe(false);
  });
});

describe("createModal: the scroll lock follows the dialog's live state", () => {
  it("a raw open and close in one task leave no lock, though another modal's dispose flushed the lock between", async () => {
    const a = createModal(makeContent(1, "a").content);
    const b = createModal(makeContent(1, "b").content);
    b.el.showModal();
    a.dispose();
    b.el.close();
    await nextTask();

    expect(b.isOpen).toBe(false);
    expect(unlocked()).toBe(true);
  });

  it("a raw close at the first lock write leaves no lock behind", async () => {
    const { body, restore } = reactingBody();
    try {
      const { content } = makeContent();
      const m = createModal(content);
      const writesAfterClose: string[] = [];
      let closed = false;
      body.onAttr = (name) => {
        if (name !== "style") {
          return;
        }
        if (closed) {
          writesAfterClose.push(body.style.position);
          return;
        }
        if (m.el.open) {
          closed = true;
          m.el.close();
        }
      };
      m.open();
      await nextTask();
      body.onAttr = null;

      expect(closed).toBe(true);
      expect(m.el.open).toBe(false);
      expect(writesAfterClose.every((p) => p !== "fixed")).toBe(true);
      expect(unlocked()).toBe(true);
    } finally {
      restore();
    }
  });

  it("a raw open at the first restore write keeps the lock", async () => {
    const { body, restore } = reactingBody();
    try {
      const { content } = makeContent();
      const m = createModal(content);
      m.open();
      m.close();
      let reopened = false;
      body.onAttr = (name) => {
        if (name === "style" && !reopened && body.style.position !== "fixed") {
          reopened = true;
          m.el.showModal();
        }
      };
      vi.advanceTimersByTime(400);
      await nextTask();
      body.onAttr = null;

      expect(reopened).toBe(true);
      expect(m.el.open).toBe(true);
      expect(locked()).toBe(true);
    } finally {
      restore();
    }
  });
});

describe("createModal: a raw close with no later call", () => {
  it("releases the scroll lock and calls onClose at the next microtask", async () => {
    const { content } = makeContent();
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    m.el.close();
    expect(locked()).toBe(true);
    await Promise.resolve();

    expect(locked()).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closed by the page while its fade settles inside that close, it still calls onClose once and unlocks", async () => {
    const { content } = makeContent();
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const onClose = vi.fn();
    const m = createModal(content, { onClose });
    m.open();
    m.close();
    // The settle's own close runs inside the page's close, which the browser ignores.
    m.el.addEventListener(
      "beforetoggle",
      () => {
        vi.advanceTimersByTime(400);
      },
      { once: true },
    );
    // A read inside that close takes its `open` record before the observer sees it.
    let read: boolean | null = null;
    opener.addEventListener(
      "focus",
      () => {
        read = m.isOpen;
      },
      { once: true },
    );
    m.el.close();
    await nextTask();
    await nextTask();

    expect(read).toBe(false);
    expect(m.el.open).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(unlocked()).toBe(true);
  });
});

describe("createModal: content a focusout handler moves", () => {
  it("opens when moving its focused content makes a handler move it again", () => {
    const content = document.createElement("div");
    const button = document.createElement("button");
    content.append(button);
    const page = document.createElement("div");
    page.append(content);
    document.body.append(page);
    button.focus();
    const aside = document.createElement("div");
    document.body.append(aside);
    button.addEventListener(
      "focusout",
      () => {
        aside.append(content);
      },
      { once: true },
    );
    const m = createModal(content);
    expect(() => {
      m.open();
    }).not.toThrow();
    expect(m.isOpen).toBe(true);
    expect(m.el.contains(content)).toBe(true);
    m.dispose();
  });
});
