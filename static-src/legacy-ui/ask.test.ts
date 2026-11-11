import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { ask, _resetForTest, type AskOptions } from "./ask.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function booleanDlg(): HTMLDialogElement {
  const d = document.querySelector<HTMLDialogElement>("dialog.uip-ask:not(.uip-ask--input)");
  if (d === null) {
    throw new Error("boolean ask dialog not found");
  }
  return d;
}

function inputDlg(): HTMLDialogElement {
  const d = document.querySelector<HTMLDialogElement>("dialog.uip-ask.uip-ask--input");
  if (d === null) {
    throw new Error("input ask dialog not found");
  }
  return d;
}

function click(scope: HTMLElement, selector: string): void {
  scope
    .querySelector<HTMLButtonElement>(selector)!
    .dispatchEvent(new MouseEvent("click", { bubbles: true }));
}

describe("ask (boolean shape)", () => {
  it("resolves true when the confirm button is clicked", async () => {
    const p = ask("Delete?");
    click(booleanDlg(), ".uip-ask-ok");
    await expect(p).resolves.toBe(true);
  });

  it("resolves false when the cancel button is clicked", async () => {
    const p = ask("Delete?");
    click(booleanDlg(), ".uip-ask-cancel");
    await expect(p).resolves.toBe(false);
  });

  it("labels the dialog by its title and describes it by the message when a title is given", () => {
    void ask("Are you sure?", { title: "Heads up", confirmLabel: "Yes", cancelLabel: "No" });
    const d = booleanDlg();
    expect(d.querySelector(".uip-ask-msg")!.textContent).toBe("Are you sure?");
    expect(d.querySelector(".uip-ask-title")!.textContent).toBe("Heads up");
    expect(d.querySelector(".uip-ask-ok")!.textContent).toBe("Yes");
    expect(d.querySelector(".uip-ask-cancel")!.textContent).toBe("No");
    expect(d.getAttribute("aria-labelledby")).toBe("uip-ask-title");
    expect(d.getAttribute("aria-describedby")).toBe("uip-ask-msg");
  });

  it("labels a title-less ask by its message and sets no describedby", () => {
    void ask("Just a plain message");
    const d = booleanDlg();
    expect(d.getAttribute("aria-labelledby")).toBe("uip-ask-msg");
    expect(d.getAttribute("aria-describedby")).toBeNull();
  });

  it("shows the title element only when there is a title", () => {
    void ask("Are you sure?", { title: "Heads up" });
    const title = booleanDlg().querySelector<HTMLElement>(".uip-ask-title")!;
    expect(title.hidden).toBe(false);
  });

  it("treats an empty title as no title (labelled by the message, title hidden)", () => {
    void ask("Just a plain message", { title: "" });
    const d = booleanDlg();
    expect(d.getAttribute("aria-labelledby")).toBe("uip-ask-msg");
    expect(d.querySelector<HTMLElement>(".uip-ask-title")!.hidden).toBe(true);
  });

  it("re-hides the title and drops the description when a later ask has no title", () => {
    void ask("first", { title: "Heads up" });
    void ask("second"); // reuses the same dialog
    const d = booleanDlg();
    expect(d.querySelector<HTMLElement>(".uip-ask-title")!.hidden).toBe(true);
    expect(d.getAttribute("aria-describedby")).toBeNull();
    expect(d.getAttribute("aria-labelledby")).toBe("uip-ask-msg");
  });

  it('defaults the OK label to "Confirm"', () => {
    void ask("Delete?");
    expect(booleanDlg().querySelector(".uip-ask-ok")!.textContent).toBe("Confirm");
    expect(booleanDlg().querySelector(".uip-ask-cancel")!.textContent).toBe("Cancel");
  });

  it("closes the dialog once the answer resolves", async () => {
    const p = ask("Delete?");
    const d = booleanDlg();
    expect(d.open).toBe(true);
    click(d, ".uip-ask-ok");
    await expect(p).resolves.toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(true); // fading out
    vi.advanceTimersByTime(400);
    expect(d.open).toBe(false);
  });

  it("prevents the native Escape close so the fade-out lifecycle runs", async () => {
    const p = ask("Sure?");
    const evt = new Event("cancel", { cancelable: true });
    booleanDlg().dispatchEvent(evt);
    expect(evt.defaultPrevented).toBe(true);
    await expect(p).resolves.toBe(false);
  });

  it("gives the action row its documented class hook", () => {
    void ask("Delete?");
    const row = booleanDlg().querySelector(".uip-ask-actions");
    expect(row).not.toBeNull();
    expect(row!.querySelectorAll("button")).toHaveLength(2);
  });

  it("destructive variant uses role=alertdialog and focuses Cancel", () => {
    void ask("Delete everything?", { variant: "destructive" });
    const d = booleanDlg();
    expect(d.getAttribute("role")).toBe("alertdialog");
    expect(d.querySelector(".uip-ask-ok")!.classList.contains("is-destructive")).toBe(true);
    expect(document.activeElement).toBe(d.querySelector(".uip-ask-cancel"));
  });

  it("normal variant clears any prior alertdialog role and destructive modifier", () => {
    void ask("danger", { variant: "destructive" });
    void ask("normal");
    const d = booleanDlg();
    expect(d.getAttribute("role")).toBeNull();
    expect(d.querySelector(".uip-ask-ok")!.classList.contains("is-destructive")).toBe(false);
  });

  it("Escape (cancel event) resolves false", async () => {
    const p = ask("Sure?");
    booleanDlg().dispatchEvent(new Event("cancel", { cancelable: true }));
    await expect(p).resolves.toBe(false);
  });

  it("a backdrop press+release on the dialog resolves false", async () => {
    const p = ask("Sure?");
    const d = booleanDlg();
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    await expect(p).resolves.toBe(false);
  });

  it("preempts a prior open ask, resolving it false; the newer one still resolves", async () => {
    const p1 = ask("First?");
    const p2 = ask("Second?");
    await expect(p1).resolves.toBe(false);
    click(booleanDlg(), ".uip-ask-ok");
    await expect(p2).resolves.toBe(true);
  });

  it("reuses a single dialog element across calls", () => {
    void ask("one");
    void ask("two");
    expect(document.querySelectorAll("dialog.uip-ask")).toHaveLength(1);
  });
});

describe("ask (input shape)", () => {
  it("resolves the typed value on submit (Enter / OK)", async () => {
    const p = ask("Name?", { input: true });
    const d = inputDlg();
    d.querySelector<HTMLInputElement>(".uip-ask-input")!.value = "zaphod";
    d.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await expect(p).resolves.toBe("zaphod");
  });

  it('resolves an empty submission as "", distinct from cancellation\'s null', async () => {
    const p = ask("Name?", { input: true });
    inputDlg()
      .querySelector("form")!
      .dispatchEvent(new Event("submit", { cancelable: true }));
    await expect(p).resolves.toBe("");
  });

  it("resolves null on cancel", async () => {
    const p = ask("Name?", { input: true });
    click(inputDlg(), ".uip-ask-cancel");
    await expect(p).resolves.toBeNull();
  });

  it("resolves null on Escape (cancel event) and backdrop press", async () => {
    const p1 = ask("Name?", { input: true });
    inputDlg().dispatchEvent(new Event("cancel", { cancelable: true }));
    await expect(p1).resolves.toBeNull();

    const p2 = ask("Name?", { input: true });
    const d = inputDlg();
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
    await expect(p2).resolves.toBeNull();
  });

  it("does not settle on a cancel event bubbling from a descendant", async () => {
    const p = ask("Name?", { input: true });
    const d = inputDlg();
    const input = d.querySelector<HTMLInputElement>(".uip-ask-input")!;
    const cancel = new Event("cancel", { bubbles: true, cancelable: true });
    input.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    input.value = "kept";
    d.querySelector("form")!.dispatchEvent(new Event("submit", { cancelable: true }));
    await expect(p).resolves.toBe("kept");
  });

  it("applies type / initialValue / placeholder / maxLength / autocomplete, and resets them", async () => {
    void ask("Password?", {
      input: {
        type: "password",
        initialValue: "hunter2",
        placeholder: "secret",
        maxLength: 32,
        autocomplete: "current-password",
      },
    });
    const input = inputDlg().querySelector<HTMLInputElement>(".uip-ask-input")!;
    expect(input.type).toBe("password");
    expect(input.value).toBe("hunter2");
    expect(input.placeholder).toBe("secret");
    expect(input.maxLength).toBe(32);
    expect(input.autocomplete).toBe("current-password");
    expect(document.activeElement).toBe(input);
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe("hunter2".length);

    inputDlg().dispatchEvent(new Event("cancel", { cancelable: true }));
    await Promise.resolve();
    void ask("Plain?", { input: true });
    expect(input.type).toBe("text");
    expect(input.value).toBe("");
    expect(input.hasAttribute("placeholder")).toBe(false);
    expect(input.hasAttribute("maxlength")).toBe(false);
    expect(input.autocomplete).toBe("off");
  });

  it("the message is the input's real <label>", () => {
    void ask("API key label:", { input: true });
    const d = inputDlg();
    const label = d.querySelector<HTMLLabelElement>("label.uip-ask-msg")!;
    expect(label.htmlFor).toBe("uip-ask-input");
    expect(label.textContent).toBe("API key label:");
  });

  it('defaults the OK label to "OK"', () => {
    void ask("Name?", { input: true });
    expect(inputDlg().querySelector(".uip-ask-ok")!.textContent).toBe("OK");
  });

  it("labels the input dialog by its own title element when a title is given", () => {
    void ask("Name?", { title: "Rename", input: true });
    const d = inputDlg();
    expect(d.getAttribute("aria-labelledby")).toBe("uip-ask-input-title");
    expect(d.querySelector("#uip-ask-input-title")!.textContent).toBe("Rename");
    expect(d.getAttribute("aria-describedby")).toBe("uip-ask-input-msg");
  });

  it("prevents the form's default submission (an ask must never navigate)", async () => {
    const p = ask("Name?", { input: true });
    const d = inputDlg();
    d.querySelector<HTMLInputElement>(".uip-ask-input")!.value = "trillian";
    const evt = new Event("submit", { cancelable: true });
    d.querySelector("form")!.dispatchEvent(evt);
    expect(evt.defaultPrevented).toBe(true);
    await expect(p).resolves.toBe("trillian");
  });

  it("destructive input ask keeps focus on the input (type-to-confirm flows)", () => {
    void ask("Type the name to delete:", { variant: "destructive", input: true });
    const d = inputDlg();
    expect(d.getAttribute("role")).toBe("alertdialog");
    expect(d.querySelector(".uip-ask-ok")!.classList.contains("is-destructive")).toBe(true);
    expect(document.activeElement).toBe(d.querySelector(".uip-ask-input"));
  });
});

describe("ask — one preemption domain across shapes", () => {
  it("an input ask preempts an open boolean ask (resolves false) and closes its dialog", async () => {
    const p1 = ask("Sure?");
    const d1 = booleanDlg();
    expect(d1.open).toBe(true);

    const p2 = ask("Name?", { input: true });
    await expect(p1).resolves.toBe(false);
    expect(d1.classList.contains("is-leaving")).toBe(true);
    expect(inputDlg().open).toBe(true);

    click(inputDlg(), ".uip-ask-cancel");
    await expect(p2).resolves.toBeNull();
  });

  it("a boolean ask preempts an open input ask (resolves null)", async () => {
    const p1 = ask("Name?", { input: true });
    const p2 = ask("Sure?");
    await expect(p1).resolves.toBeNull();
    click(booleanDlg(), ".uip-ask-ok");
    await expect(p2).resolves.toBe(true);
  });
});

describe("ask — _resetForTest (test-only seam)", () => {
  it("resolves a still-pending ask to its cancel value", async () => {
    const p = ask("Sure?");
    _resetForTest();
    await expect(p).resolves.toBe(false);
  });

  it("removes both shapes' dialogs from the document", () => {
    void ask("boolean shape");
    void ask("input shape", { input: true }); // preempts the boolean one
    expect(document.querySelectorAll("dialog.uip-ask")).toHaveLength(2);
    _resetForTest();
    expect(document.querySelectorAll("dialog.uip-ask")).toHaveLength(0);
  });
});

/** Listener registrations we observe by invocation rather than by count. */
type Listener = EventListenerOrEventListenerObject;

/**
 * Wraps every listener `target` registers from now on, appending its event
 * type to `ran` when it fires. Registration options and `removeEventListener`
 * for the original function still work, since the same wrapper is reused.
 */
function watchListeners(target: EventTarget, ran: string[]): void {
  const wrappers = new Map<Listener, Map<string, EventListener>>();
  const add = target.addEventListener.bind(target);
  const remove = target.removeEventListener.bind(target);
  const wrap = (type: string, listener: Listener): EventListener => {
    let byType = wrappers.get(listener);
    if (byType === undefined) {
      byType = new Map<string, EventListener>();
      wrappers.set(listener, byType);
    }
    let wrapper = byType.get(type);
    if (wrapper === undefined) {
      wrapper = (e: Event): void => {
        ran.push(type);
        if (typeof listener === "function") {
          listener(e);
        } else {
          listener.handleEvent(e);
        }
      };
      byType.set(type, wrapper);
    }
    return wrapper;
  };
  vi.spyOn(target, "addEventListener").mockImplementation(
    (type: string, listener: Listener | null, options?: AddEventListenerOptions | boolean) => {
      if (listener !== null) {
        add(type, wrap(type, listener), options);
      }
    },
  );
  vi.spyOn(target, "removeEventListener").mockImplementation(
    (type: string, listener: Listener | null, options?: EventListenerOptions | boolean) => {
      if (listener !== null) {
        // Look up, never create: an unwrapped removal (e.g. from an abort
        // signal) must pass through as-is or the registration is never taken off.
        remove(type, wrappers.get(listener)?.get(type) ?? listener, options);
      }
    },
  );
}

/** Drive every dismissal affordance the boolean ask wires, in one go. */
function pokeEverything(d: HTMLDialogElement): void {
  const fire = (el: Element, e: Event): void => {
    el.dispatchEvent(e);
  };
  fire(d.querySelector(".uip-ask-ok")!, new MouseEvent("click", { bubbles: true }));
  fire(d.querySelector(".uip-ask-cancel")!, new MouseEvent("click", { bubbles: true }));
  fire(d, new MouseEvent("mousedown", { bubbles: true }));
  fire(d, new MouseEvent("mouseup", { bubbles: true }));
  fire(d, new Event("cancel", { cancelable: true }));
}

describe("ask — an ask that is over owns nothing on the shared dialog", () => {
  /** Build the boolean shape's reused dialog and leave it fully closed. */
  async function warmBooleanDialog(): Promise<HTMLDialogElement> {
    const p = ask("warm up");
    const d = booleanDlg();
    click(d, ".uip-ask-cancel");
    await expect(p).resolves.toBe(false);
    vi.advanceTimersByTime(400);
    return d;
  }

  it("stops intercepting the dialog's native cancel once the answer has resolved", async () => {
    const p = ask("Sure?");
    const d = booleanDlg();
    click(d, ".uip-ask-ok");
    await expect(p).resolves.toBe(true);
    vi.advanceTimersByTime(400);

    const evt = new Event("cancel", { cancelable: true });
    d.dispatchEvent(evt);
    // A resolved ask must hand the shared dialog back to the platform, so
    // Escape reaches the native close instead of a dead handler.
    expect(evt.defaultPrevented).toBe(false);
  });

  it("detaches every listener it registered when the answer resolves", async () => {
    const d = await warmBooleanDialog();
    const ran: string[] = [];
    watchListeners(d, ran);
    watchListeners(d.querySelector(".uip-ask-ok")!, ran);
    watchListeners(d.querySelector(".uip-ask-cancel")!, ran);

    const p = ask("Delete?");
    click(d, ".uip-ask-ok");
    await expect(p).resolves.toBe(true);
    vi.advanceTimersByTime(400);

    ran.length = 0;
    pokeEverything(d);
    // Nothing the resolved ask registered may still run.
    expect(ran).toEqual([]);
  });

  it("detaches the listeners of an ask its successor preempted", async () => {
    const d = await warmBooleanDialog();
    const ran: string[] = [];
    watchListeners(d, ran);
    watchListeners(d.querySelector(".uip-ask-ok")!, ran);
    watchListeners(d.querySelector(".uip-ask-cancel")!, ran);

    const p1 = ask("First?");
    const p2 = ask("Second?"); // same shape: takes over the still-open dialog
    await expect(p1).resolves.toBe(false);

    ran.length = 0;
    // Each of these must find exactly ONE handler — the successor's — or the
    // preempted ask is still wired to a dialog it no longer owns.
    d.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    d.dispatchEvent(new Event("cancel", { cancelable: true }));
    expect(ran).toEqual(["mousedown", "cancel"]);
    await expect(p2).resolves.toBe(false);
  });

  it("detaches the input shape's submit handler when the value resolves, and a late submit stays prevented", async () => {
    const p0 = ask("warm up", { input: true });
    const d = inputDlg();
    click(d, ".uip-ask-cancel");
    await expect(p0).resolves.toBeNull();
    vi.advanceTimersByTime(400);

    const form = d.querySelector("form")!;
    const ran: string[] = [];
    watchListeners(form, ran);

    const p = ask("Name?", { input: true });
    d.querySelector<HTMLInputElement>(".uip-ask-input")!.value = "ford";
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await expect(p).resolves.toBe("ford");
    vi.advanceTimersByTime(400);

    ran.length = 0;
    const late = new Event("submit", { cancelable: true });
    form.dispatchEvent(late);
    expect(ran).toEqual([]);
    expect(late.defaultPrevented).toBe(true);
  });
});

describe("ask — preemption arms no stray leave lifecycle", () => {
  it("a same-shape preemption reuses the open dialog instead of fading it out", async () => {
    const p1 = ask("First?");
    const d = booleanDlg();
    const p2 = ask("Second?");
    await expect(p1).resolves.toBe(false);

    // No leave may start against the dialog the successor reuses, or the
    // stale timer yanks it shut moments later.
    expect(d.open).toBe(true);
    expect(d.classList.contains("is-leaving")).toBe(false);
    expect(vi.getTimerCount()).toBe(0);

    click(d, ".uip-ask-ok");
    await expect(p2).resolves.toBe(true);
  });

  it("a resolved ask is not pending, so the next ask has nothing to preempt", async () => {
    const p1 = ask("Sure?");
    const d1 = booleanDlg();
    click(d1, ".uip-ask-ok");
    await expect(p1).resolves.toBe(true);
    expect(vi.getTimerCount()).toBe(1); // its own leave fallback

    const p2 = ask("Name?", { input: true });
    // A resolved ask is not pending, so no second leave overlaps d1's own.
    expect(vi.getTimerCount()).toBe(1);

    vi.advanceTimersByTime(400);
    expect(d1.open).toBe(false);
    click(inputDlg(), ".uip-ask-cancel");
    await expect(p2).resolves.toBeNull();
  });
});

describe("ask — a gesture answers the question its dialog shows", () => {
  it("an Escape inside the dialog's opening, after a newer unshown ask, leaves the newer one live", async () => {
    const first = ask("First?");
    click(booleanDlg(), ".uip-ask-cancel");
    await expect(first).resolves.toBe(false);
    vi.advanceTimersByTime(400);
    const dlg = booleanDlg();
    const settled: boolean[] = [];
    dlg.addEventListener(
      "focusin",
      () => {
        void ask("Second?").then((v) => settled.push(v));
        dlg.dispatchEvent(new Event("cancel", { cancelable: true }));
      },
      { once: true },
    );
    const third = ask("Third?");
    await expect(third).resolves.toBe(false);
    await Promise.resolve();

    expect(settled).toEqual([]);
    expect(dlg.open).toBe(true);
    expect(dlg.querySelector(".uip-ask-msg")?.textContent).toBe("Second?");
    click(dlg, ".uip-ask-ok");
    await Promise.resolve();
    expect(settled).toEqual([true]);
  });
});

describe("ask — an ask() started from code the outer call runs", () => {
  it("one from an option getter runs first, and the outer call preempts it", async () => {
    let inner: Promise<boolean> | undefined;
    const outer = ask("outer", {
      get title(): string {
        inner ??= ask("inner");
        return "T";
      },
    });
    await expect(inner).resolves.toBe(false);
    expect(booleanDlg().querySelector(".uip-ask-msg")?.textContent).toBe("outer");
    click(booleanDlg(), ".uip-ask-ok");
    await expect(outer).resolves.toBe(true);
  });

  it("one from a focus listener the outer call reaches is the later call and preempts it", async () => {
    let inner: Promise<boolean> | undefined;
    const onFocus = (): void => {
      inner ??= ask("inner");
    };
    document.addEventListener("focusin", onFocus, true);
    const outer = ask("outer");
    document.removeEventListener("focusin", onFocus, true);
    await expect(outer).resolves.toBe(false);
    expect(inner).toBeDefined();
    expect(booleanDlg().querySelector(".uip-ask-msg")?.textContent).toBe("inner");
    click(booleanDlg(), ".uip-ask-ok");
    await expect(inner).resolves.toBe(true);
  });
});

describe("ask — options are read once", () => {
  it("reads each option getter once, at the call", async () => {
    const reads = { title: 0, confirmLabel: 0 };
    const p = ask("Read?", {
      get title(): string {
        reads.title++;
        return "T";
      },
      get confirmLabel(): string {
        reads.confirmLabel++;
        return "Yes";
      },
    });
    click(booleanDlg(), ".uip-ask-ok");
    await expect(p).resolves.toBe(true);
    vi.advanceTimersByTime(400);

    expect(reads).toEqual({ title: 1, confirmLabel: 1 });
  });
});

describe("ask — a close the browser makes", () => {
  it("resolves the question with its cancel value at the next microtask", async () => {
    let settled: boolean | null = null;
    void ask("Delete?").then((v) => {
      settled = v;
    });
    booleanDlg().close();
    await Promise.resolve();
    await Promise.resolve();

    expect(settled).toBe(false);
  });
});

type Details = NonNullable<AskOptions["details"]>;

describe("ask — details", () => {
  const facts = [
    { label: "Turns removed", value: "4" },
    { label: "Files restored", value: "2" },
    { label: "Branch", value: "main" },
  ];

  function pairs(d: HTMLDialogElement): string[][] {
    const list = d.querySelector(".uip-ask-details")!;
    return [...list.children].map((c) => [c.tagName, c.textContent]);
  }

  it("renders each entry in order as a dt and dd pair after the message", () => {
    void ask("Rewind?", { details: facts });
    const d = booleanDlg();
    expect(pairs(d)).toEqual([
      ["DT", "Turns removed"],
      ["DD", "4"],
      ["DT", "Files restored"],
      ["DD", "2"],
      ["DT", "Branch"],
      ["DD", "main"],
    ]);
    const list = d.querySelector(".uip-ask-details")!;
    expect(list.tagName).toBe("DL");
    expect(list.previousElementSibling).toBe(d.querySelector(".uip-ask-msg"));
  });

  it("puts the input shape's list between its label and its input", () => {
    void ask("New name?", { input: true, details: facts });
    const list = inputDlg().querySelector(".uip-ask-details")!;
    expect(list.previousElementSibling).toBe(inputDlg().querySelector("label.uip-ask-msg"));
    expect(list.nextElementSibling).toBe(inputDlg().querySelector(".uip-ask-input"));
  });

  it.each([
    ["absent", undefined],
    ["empty", []],
  ])("renders nothing and leaves no stray id when details are %s", (_name, details) => {
    void ask("Rewind?", { title: "Rewind", ...(details === undefined ? {} : { details }) });
    const d = booleanDlg();
    expect(d.querySelector("dl")).toBeNull();
    expect(document.getElementById("uip-ask-details")).toBeNull();
    expect(d.getAttribute("aria-describedby")).toBe("uip-ask-msg");
  });

  it("drops a previous ask's list when the next one has none", () => {
    void ask("first", { details: facts });
    void ask("second");
    const d = booleanDlg();
    expect(d.querySelector("dl")).toBeNull();
    expect(document.getElementById("uip-ask-details")).toBeNull();
    expect(d.getAttribute("aria-describedby")).toBeNull();
  });

  it.each([
    [
      "boolean with a title",
      () => ask("Rewind?", { title: "T", details: facts }),
      booleanDlg,
      "uip-ask-msg uip-ask-details",
    ],
    [
      "boolean without a title",
      () => ask("Rewind?", { details: facts }),
      booleanDlg,
      "uip-ask-details",
    ],
    [
      "input with a title",
      () => ask("Rewind?", { title: "T", input: true, details: facts }),
      inputDlg,
      "uip-ask-input-msg uip-ask-input-details",
    ],
    [
      "input without a title",
      () => ask("Rewind?", { input: true, details: facts }),
      inputDlg,
      "uip-ask-input-details",
    ],
  ] as const)("describes the %s dialog by the ids listed", (_name, open, dlg, expected) => {
    void open();
    const d = dlg();
    expect(d.getAttribute("aria-describedby")).toBe(expected);
    for (const id of expected.split(" ")) {
      expect(d.querySelector(`#${id}`)).not.toBeNull();
    }
  });

  it.each([
    ["a string", "Turns removed: 4"],
    ["an object", { label: "Turns", value: "4" }],
    ["null", null],
    ["a null entry", [null]],
    ["a string entry", ["Turns removed"]],
    ["a number label", [{ label: 4, value: "4" }]],
    ["a missing value", [{ label: "Turns" }]],
    ["a number value", [{ label: "Turns", value: 4 }]],
    ["a hole", new Array<unknown>(1)],
    [
      "a bad later entry",
      [
        { label: "Turns", value: "4" },
        { label: "Files", value: 2 },
      ],
    ],
  ])(
    "throws a TypeError naming details for %s, before the dialog opens",
    async (_name, details) => {
      const live = ask("Still here?");
      const call = (): unknown => ask("Rewind?", { details: details as unknown as Details });
      expect(call).toThrow(TypeError);
      expect(call).toThrow(/details/);
      expect(booleanDlg().querySelector(".uip-ask-msg")!.textContent).toBe("Still here?");
      click(booleanDlg(), ".uip-ask-ok");
      await expect(live).resolves.toBe(true);
    },
  );

  it("reads no other option when the details are refused", () => {
    const live = ask("Still here?");
    let inner: Promise<boolean> | undefined;
    expect(() =>
      ask("Rewind?", {
        get title(): string {
          inner ??= ask("inner");
          return "T";
        },
        details: "Turns removed: 4" as unknown as Details,
      }),
    ).toThrow(TypeError);
    expect(inner).toBeUndefined();
    expect(booleanDlg().querySelector(".uip-ask-msg")!.textContent).toBe("Still here?");
    click(booleanDlg(), ".uip-ask-ok");
    return expect(live).resolves.toBe(true);
  });

  it("reads each entry once, at the call", () => {
    const reads = { label: 0, value: 0 };
    const entry = {
      get label(): string {
        reads.label++;
        return "Turns removed";
      },
      get value(): string {
        reads.value++;
        return "4";
      },
    };
    const later = { label: "Branch", value: "main" };
    void ask("Rewind?", { details: [entry, later] });
    later.label = "changed";
    later.value = "changed";
    expect(reads).toEqual({ label: 1, value: 1 });
    expect(pairs(booleanDlg())).toEqual([
      ["DT", "Turns removed"],
      ["DD", "4"],
      ["DT", "Branch"],
      ["DD", "main"],
    ]);
  });

  it("opens no dialog when the first ask's details are refused", () => {
    expect(() =>
      ask("Rewind?", {
        details: [{ label: "Turns", value: 4 }] as unknown as Details,
      }),
    ).toThrow(TypeError);
    expect(document.querySelector("dialog")).toBeNull();
  });

  it("sets markup in a label or value as text", () => {
    const markup = '<img src="x" onerror="alert(1)"><b>bold</b>';
    void ask("Rewind?", { details: [{ label: markup, value: markup }] });
    const list = booleanDlg().querySelector(".uip-ask-details")!;
    expect(list.querySelector("img, b")).toBeNull();
    expect(pairs(booleanDlg())).toEqual([
      ["DT", markup],
      ["DD", markup],
    ]);
  });
});
