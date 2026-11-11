import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { announce, mountLiveRegions, _resetForTest as resetAnnounce } from "./announce.js";
import { ask, _resetForTest as resetAsk } from "./ask.js";
import { closeDialog, createDialog, openDialog } from "./dialog.js";
import { createModal, _resetForTest as resetModal } from "./modal.js";
import { topmostOpenDialog } from "./modal-host.js";

/** Matches the leave-transition fallback, so a close completes with no CSS. */
const SETTLE_MS = 400;

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  resetAsk();
  resetModal();
  resetAnnounce();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function pairIn(host: HTMLElement): { polite: HTMLElement; assertive: HTMLElement } {
  const own = [...host.children].filter(
    (c): c is HTMLElement => c instanceof HTMLElement && c.hasAttribute("aria-live"),
  );
  expect(own.map((r) => r.getAttribute("aria-live"))).toEqual(["polite", "assertive"]);
  return { polite: own[0]!, assertive: own[1]! };
}

/** The `[aria-live]` count inside each dialog at the moment its showModal() runs. */
function regionsAtShowModal(): number[] {
  const seen: number[] = [];
  const original = HTMLDialogElement.prototype.showModal;
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (
    this: HTMLDialogElement,
  ) {
    seen.push(this.querySelectorAll(":scope > [aria-live]").length);
    original.call(this);
  });
  return seen;
}

describe("dialog primitives mount their own pair before showModal()", () => {
  it("createDialog", () => {
    const seen = regionsAtShowModal();
    const dlg = document.createElement("dialog");
    document.body.appendChild(dlg);
    createDialog(dlg).open();

    expect(seen).toEqual([2]);
    expect(dlg.open).toBe(true);
    pairIn(dlg);
  });

  it("createModal", () => {
    const seen = regionsAtShowModal();
    const modal = createModal(document.createElement("div"));
    modal.open();

    expect(seen).toEqual([2]);
    pairIn(modal.el);
  });

  it("ask, both shapes", () => {
    const seen = regionsAtShowModal();
    void ask("Sure?");
    void ask("Name?", { input: true });

    expect(seen).toEqual([2, 2]);
    const dialogs = [...document.querySelectorAll<HTMLDialogElement>("dialog.uip-ask")];
    expect(dialogs).toHaveLength(2);
    for (const d of dialogs) {
      pairIn(d);
    }
  });
});

/** Resolves on `dialog`'s next native `close` event, which the platform fires
 *  on a later task than `close()`. A microtask checkpoint runs between
 *  listeners, so only listeners attached before this call have run by then. */
function nextClose(dialog: HTMLDialogElement): Promise<void> {
  return new Promise((resolve) => {
    dialog.addEventListener(
      "close",
      () => {
        resolve();
      },
      { once: true },
    );
  });
}

function callerDialog(): HTMLDialogElement {
  const dlg = document.createElement("dialog");
  document.body.appendChild(dlg);
  return dlg;
}

describe("a dialog's pair through closes and teardown", () => {
  it("survives a close event queued before a reopen", async () => {
    mountLiveRegions(document.body);
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    controller.open();
    dlg.close();
    controller.open();
    await nextClose(dlg);

    announce("back");
    vi.advanceTimersByTime(100);
    expect(pairIn(dlg).polite.textContent).toBe("back");
  });

  it("stays when dispose closes the open dialog, and only the root hears", () => {
    mountLiveRegions(document.body);
    const root = pairIn(document.body);
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    controller.open();
    controller.dispose();

    expect(dlg.open).toBe(false);
    const pair = pairIn(dlg);
    announce("root");
    vi.advanceTimersByTime(100);
    expect(root.polite.textContent).toBe("root");
    expect(pair.polite.textContent).toBe("");
  });

  it("hands announcements to the modal below once dispose closes the dialog over it", () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    lower.open();
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    controller.open();
    controller.dispose();

    announce("lower");
    vi.advanceTimersByTime(100);
    expect(pairIn(lower.el).polite.textContent).toBe("lower");
  });
});

/** Every listener added to `target` from now on that is still attached: neither removed
 *  nor aborted through its `signal`. The engine's `beforetoggle` listener lives as long
 *  as the dialog, like its `open` observer, so it is not counted. */
function attachedListeners(target: EventTarget): () => string[] {
  const added: { type: string; listener: unknown; capture: boolean; signal?: AbortSignal }[] = [];
  const add = target.addEventListener.bind(target);
  const remove = target.removeEventListener.bind(target);
  vi.spyOn(target, "addEventListener").mockImplementation((type, listener, options) => {
    const opts = typeof options === "object" ? options : { capture: options === true };
    added.push({
      type,
      listener,
      capture: opts.capture === true,
      ...(opts.signal === undefined ? {} : { signal: opts.signal }),
    });
    add(type, listener, options);
  });
  vi.spyOn(target, "removeEventListener").mockImplementation((type, listener, options) => {
    const capture = typeof options === "object" ? options.capture === true : options === true;
    const at = added.findIndex(
      (a) => a.type === type && a.listener === listener && a.capture === capture,
    );
    if (at !== -1) {
      added.splice(at, 1);
    }
    remove(type, listener, options);
  });
  return () =>
    added.filter((a) => a.signal?.aborted !== true && a.type !== "beforetoggle").map((a) => a.type);
}

describe("a pair is owned, and only its owners release it", () => {
  it("keeps a later root mount on a dialog its disposed controller once opened", async () => {
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    controller.open();
    const closed = nextClose(dlg);
    controller.close();
    vi.advanceTimersByTime(SETTLE_MS);
    await closed;
    controller.dispose();

    mountLiveRegions(dlg);
    dlg.showModal();
    const closedAgain = nextClose(dlg);
    dlg.close();
    await closedAgain;
    expect(dlg.querySelectorAll("[aria-live]")).toHaveLength(2);
  });

  it("keeps a driven dialog's pair after a root mount on it releases", async () => {
    const dlg = callerDialog();
    const releaseRoot = mountLiveRegions(dlg);
    const controller = createDialog(dlg);
    controller.open();
    const pair = pairIn(dlg);
    releaseRoot();
    const closed = nextClose(dlg);
    dlg.close();
    await closed;

    expect(pairIn(dlg).polite).toBe(pair.polite);
    expect(pairIn(dlg).assertive).toBe(pair.assertive);
  });

  it("ignores a repeated release, leaving the other owner's pair", () => {
    const first = mountLiveRegions(document.body);
    mountLiveRegions(document.body);
    first();
    first();

    pairIn(document.body);
    announce("still");
    vi.advanceTimersByTime(100);
    expect(pairIn(document.body).polite.textContent).toBe("still");
  });

  it("keeps a root pair on the same host through dispose before close", async () => {
    const dlg = callerDialog();
    mountLiveRegions(dlg);
    const pair = pairIn(dlg);
    const controller = createDialog(dlg);
    controller.open();
    const closed = nextClose(dlg);
    controller.dispose();
    await closed;

    expect(pairIn(dlg).polite).toBe(pair.polite);
  });

  it("holds one beforetoggle listener of the engine's, however often the dialog opens and closes", async () => {
    const dlg = callerDialog();
    const held = new Set<string>();
    const ids = new Map<unknown, number>();
    const key = (
      listener: unknown,
      options: boolean | EventListenerOptions | undefined,
    ): string => {
      if (!ids.has(listener)) {
        ids.set(listener, ids.size);
      }
      const capture = typeof options === "object" ? options.capture === true : options === true;
      return `${String(ids.get(listener))}:${String(capture)}`;
    };
    const add = dlg.addEventListener.bind(dlg);
    const remove = dlg.removeEventListener.bind(dlg);
    vi.spyOn(dlg, "addEventListener").mockImplementation((type, listener, options) => {
      if (type === "beforetoggle") {
        held.add(key(listener, options));
      }
      add(type, listener, options);
    });
    vi.spyOn(dlg, "removeEventListener").mockImplementation((type, listener, options) => {
      if (type === "beforetoggle") {
        held.delete(key(listener, options));
      }
      remove(type, listener, options);
    });
    const controller = createDialog(dlg);
    for (let i = 0; i < 3; i++) {
      const closed = nextClose(dlg);
      controller.open();
      controller.close();
      vi.advanceTimersByTime(SETTLE_MS);
      await closed;
    }
    controller.dispose();

    expect(held.size).toBe(1);
  });

  it.each(["open", "closed"] as const)(
    "leaves no listener on a dialog disposed while %s",
    async (state) => {
      const dlg = callerDialog();
      const closed = nextClose(dlg);
      const attached = attachedListeners(dlg);
      const controller = createDialog(dlg);
      controller.open();
      if (state === "closed") {
        controller.close();
        vi.advanceTimersByTime(SETTLE_MS);
        await closed;
        controller.open();
        dlg.open = false;
      }
      controller.dispose();

      expect(attached()).toEqual([]);
    },
  );
});

/** Puts `dialog` where `showModal()` throws an `InvalidStateError`, and
 *  returns what brings it back to a state `show()` accepts. */
const unshowable = {
  "showing as a popover": (dialog: HTMLDialogElement): (() => void) => {
    dialog.popover = "manual";
    dialog.showPopover();
    return () => {
      dialog.hidePopover();
      dialog.removeAttribute("popover");
    };
  },
  disconnected: (dialog: HTMLDialogElement): (() => void) => {
    dialog.remove();
    return () => {
      document.body.append(dialog);
    };
  },
} as const;

describe("a failed showModal() leaves nothing of the open but the dialog's pair", () => {
  it.each(Object.keys(unshowable) as (keyof typeof unshowable)[])(
    "openDialog on a dialog %s",
    (state) => {
      const dlg = callerDialog();
      const attached = attachedListeners(dlg);
      const restore = unshowable[state](dlg);

      expect(() => {
        openDialog(dlg);
      }).toThrow(expect.objectContaining({ name: "InvalidStateError" }));
      pairIn(dlg);
      expect(attached()).toEqual([]);

      restore();
      dlg.show();
      const direct = callerDialog();
      direct.showModal();
      expect(topmostOpenDialog()).toBe(direct);
    },
  );

  it.each(["showing as a popover"] as const)("createModal open on a modal %s", (state) => {
    const modal = createModal(document.createElement("div"));
    const attached = attachedListeners(modal.el);
    const restore = unshowable[state](modal.el);

    expect(() => {
      modal.open();
    }).toThrow(expect.objectContaining({ name: "InvalidStateError" }));
    expect(document.body.style.position).toBe("");
    expect(document.body.style.overflow).toBe("");
    pairIn(modal.el);
    expect(attached()).toEqual([]);

    restore();
    modal.el.show();
    const direct = callerDialog();
    direct.showModal();
    expect(topmostOpenDialog()).toBe(direct);
  });
});

describe("a modal the page removed", () => {
  it("is mounted again by open()", () => {
    const modal = createModal(document.createElement("div"));
    modal.el.remove();
    modal.open();
    expect(modal.el.isConnected).toBe(true);
    expect(modal.el.open).toBe(true);
    modal.dispose();
  });
});

describe("announcements follow the modal stack", () => {
  it("skip a modal once it closes, then reach the root alone", async () => {
    mountLiveRegions(document.body);
    const root = pairIn(document.body);
    const lower = createModal(document.createElement("div"));
    const upper = createModal(document.createElement("div"));
    lower.open();
    upper.open();

    upper.close();
    vi.advanceTimersByTime(SETTLE_MS);
    announce("below", "assertive");
    vi.advanceTimersByTime(100);
    expect(pairIn(lower.el).assertive.textContent).toBe("below");
    expect(pairIn(upper.el).assertive.textContent).toBe("");

    lower.close();
    vi.advanceTimersByTime(SETTLE_MS);
    await Promise.resolve();
    announce("root", "assertive");
    vi.advanceTimersByTime(100);
    expect(root.assertive.textContent).toBe("root");
    expect(pairIn(lower.el).assertive.textContent).toBe("");
  });

  it("reach an open ask dialog's pair", () => {
    mountLiveRegions(document.body);
    void ask("Delete?", { variant: "destructive" });
    const dialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask")!;

    announce("one item selected");
    vi.advanceTimersByTime(100);
    expect(pairIn(dialog).polite.textContent).toBe("one item selected");
  });

  it("reach an ask opened over a modal built after it", async () => {
    mountLiveRegions(document.body);
    const first = ask("Warm up?");
    document.querySelector<HTMLButtonElement>("dialog.uip-ask .uip-ask-cancel")!.click();
    await first;
    vi.advanceTimersByTime(SETTLE_MS);
    const askDialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask")!;
    expect(askDialog.open).toBe(false);

    const modal = createModal(document.createElement("div"));
    modal.open();
    void ask("Discard changes?");

    announce("hello");
    vi.advanceTimersByTime(100);
    expect(pairIn(askDialog).polite.textContent).toBe("hello");
  });

  it("skip a disposed modal that was on top", () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    const upper = createModal(document.createElement("div"));
    lower.open();
    upper.open();
    upper.dispose();

    announce("left");
    vi.advanceTimersByTime(100);
    expect(pairIn(lower.el).polite.textContent).toBe("left");
  });
});

describe("announcements reach every open modal the library drives, whatever the rank", () => {
  it("reach a driven dialog moved into a shadow root and opened over an older modal", async () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    const moved = callerDialog();
    const controller = createDialog(moved);
    const host = document.createElement("div");
    document.body.append(host);
    host.attachShadow({ mode: "open" }).append(moved);
    lower.open();
    await vi.advanceTimersByTimeAsync(10);
    controller.open();
    await vi.advanceTimersByTimeAsync(10);

    announce("top");
    await vi.advanceTimersByTimeAsync(100);
    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(pairIn(moved).polite.textContent).toBe("top");
  });

  it("reach the modal on top when the rank names the modal under it", async () => {
    mountLiveRegions(document.body);
    const under = callerDialog();
    createDialog(under).open();
    await vi.advanceTimersByTimeAsync(10);
    const host = document.createElement("div");
    document.body.append(host);
    const late = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(late);
    // Opened before any helper reached its shadow root, `late` has no rank and ranks
    // below `under`, though the browser put it on top.
    late.showModal();
    createDialog(late);
    await vi.advanceTimersByTimeAsync(10);
    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(under);

    announce("heard");
    await vi.advanceTimersByTimeAsync(100);
    expect(pairIn(late).polite.textContent).toBe("heard");
  });

  it("go to the root alone while no modal is open, never to an open non-modal dialog's pair", async () => {
    mountLiveRegions(document.body);
    const root = pairIn(document.body);
    const shown = callerDialog();
    createDialog(shown);
    shown.show();
    await vi.advanceTimersByTimeAsync(10);

    announce("once");
    await vi.advanceTimersByTimeAsync(100);
    expect(root.polite.textContent).toBe("once");
    expect(pairIn(shown).polite.textContent).toBe("");
  });

  it("go to the pair of every open modal and to the root's", () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    const upper = createModal(document.createElement("div"));
    lower.open();
    upper.open();

    announce("all", "assertive");
    vi.advanceTimersByTime(100);
    expect(pairIn(upper.el).assertive.textContent).toBe("all");
    expect(pairIn(lower.el).assertive.textContent).toBe("all");
    expect(pairIn(document.body).assertive.textContent).toBe("all");
  });
});

/** The polite then the assertive text of each host's pair. */
function texts(...hosts: HTMLElement[]): (string | null)[] {
  return hosts.flatMap((host) => {
    const pair = pairIn(host);
    return [pair.polite.textContent, pair.assertive.textContent];
  });
}

/** Two library modals open, the upper one in `parent`, and one message of each
 *  politeness landed in every pair. */
function announcedOverTwo(parent: HTMLElement = document.body): {
  lower: HTMLDialogElement;
  upper: HTMLDialogElement;
} {
  mountLiveRegions(document.body);
  const lower = createModal(document.createElement("div"));
  lower.open();
  const upper = document.createElement("dialog");
  parent.append(upper);
  createDialog(upper).open();
  announce("3 items");
  announce("Saved", "assertive");
  vi.advanceTimersByTime(100);
  expect(texts(upper, lower.el, document.body)).toEqual([
    "3 items",
    "Saved",
    "3 items",
    "Saved",
    "3 items",
    "Saved",
  ]);
  return { lower: lower.el, upper };
}

// Each region is checked at the microtask after the change, before the page renders:
// the browser exposes what it uncovered at the next rendering.
describe("a region a modal's close or removal uncovers holds no text", () => {
  it("the modal below and the root, once the library closes the modal on top", async () => {
    const { lower, upper } = announcedOverTwo();
    closeDialog(upper);
    vi.advanceTimersByTime(SETTLE_MS);
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page closes the modal on top", async () => {
    const { lower, upper } = announcedOverTwo();
    upper.close();
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page removes the open modal on top", async () => {
    const { lower, upper } = announcedOverTwo();
    upper.remove();
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page removes the shadow host of the open modal on top", async () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    lower.open();
    // Inside a wrapper, so only the path through the shadow host sees the removal.
    const wrapper = document.createElement("div");
    const host = document.createElement("div");
    wrapper.append(host);
    document.body.append(wrapper);
    const upper = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(upper);
    createDialog(upper).open();
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    expect(pairIn(upper).assertive.textContent).toBe("Saved");

    host.remove();
    await Promise.resolve();
    expect(document.elementFromPoint(1, 1)).toBe(lower.el);
    expect(texts(lower.el, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once a modal the page opened over them closes", async () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    lower.open();
    const page = callerDialog();
    page.showModal();
    await Promise.resolve();
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    expect(texts(lower.el, document.body)).toEqual(["", "Saved", "", "Saved"]);

    page.close();
    await Promise.resolve();
    expect(document.elementFromPoint(1, 1)).toBe(lower.el);
    expect(texts(lower.el, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal the page closes and opens again over the one above it", async () => {
    const { lower } = announcedOverTwo();
    lower.close();
    lower.showModal();
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower)).toEqual(["", ""]);
  });

  it("the root, written before a modal opened, once that modal closes", async () => {
    mountLiveRegions(document.body);
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    const modal = createModal(document.createElement("div"));
    modal.open();
    await Promise.resolve();
    expect(pairIn(document.body).assertive.textContent).toBe("Saved");

    modal.close();
    vi.advanceTimersByTime(SETTLE_MS);
    await Promise.resolve();
    expect(texts(document.body)).toEqual(["", ""]);
  });

  it("the modal below and the root, once the page moves the modal on top with moveBefore() and then removes it", async () => {
    const { lower, upper } = announcedOverTwo();
    const section = document.createElement("section");
    document.body.append(section);
    section.moveBefore(upper, null);
    await Promise.resolve();
    expect(upper.matches(":modal")).toBe(true);

    upper.remove();
    await Promise.resolve();
    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page moves the modal on top into a nested element with moveBefore() and then removes that element", async () => {
    const { lower, upper } = announcedOverTwo();
    const outer = document.createElement("div");
    const inner = document.createElement("div");
    outer.append(inner);
    document.body.append(outer);
    inner.moveBefore(upper, null);
    await Promise.resolve();
    expect(upper.matches(":modal")).toBe(true);

    inner.remove();
    await Promise.resolve();
    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page moves an ancestor of the modal on top with moveBefore() and then removes the ancestor", async () => {
    const holder = document.createElement("div");
    document.body.append(holder);
    const { lower, upper } = announcedOverTwo(holder);
    const section = document.createElement("section");
    document.body.append(section);
    section.moveBefore(holder, null);
    await Promise.resolve();
    expect(upper.matches(":modal")).toBe(true);

    holder.remove();
    await Promise.resolve();
    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the root, once the page moves the only modal with moveBefore() and then removes it", async () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    expect(pairIn(document.body).assertive.textContent).toBe("Saved");
    const box = document.createElement("div");
    document.body.append(box);
    box.moveBefore(modal.el, null);
    await Promise.resolve();
    expect(modal.el.matches(":modal")).toBe(true);

    modal.el.remove();
    await Promise.resolve();
    expect(texts(document.body)).toEqual(["", ""]);
  });

  it("the modal below and the root, once the page moves the modal on top with insertBefore()", async () => {
    const { lower, upper } = announcedOverTwo();
    const section = document.createElement("section");
    document.body.append(section);
    section.insertBefore(upper, null);
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("the modal below and the root, once the page moves the modal on top with append()", async () => {
    const { lower, upper } = announcedOverTwo();
    const section = document.createElement("section");
    document.body.append(section);
    section.append(upper);
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(lower);
    expect(texts(lower, document.body)).toEqual(["", "", "", ""]);
  });

  it("a modal's own pair, when it opens again after closing with text", async () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    modal.close();
    vi.advanceTimersByTime(SETTLE_MS);
    await Promise.resolve();

    modal.open();
    expect(texts(modal.el)).toEqual(["", ""]);
  });
});

// One rule for every written region: the pair still exposed is emptied too. Its message
// was read when it landed, and removing text from a live region is not announced.
describe("every written region is emptied once any covering modal closes or opens again", () => {
  it("the top modal's too, when the lower modal closes", async () => {
    const { lower, upper } = announcedOverTwo();
    lower.close();
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(upper);
    expect(texts(upper, lower, document.body)).toEqual(["", "", "", "", "", ""]);
  });

  it("the only modal's, when the page closes it and opens it again in one turn", async () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    announce("Saved", "assertive");
    vi.advanceTimersByTime(100);
    modal.el.close();
    modal.el.showModal();
    await Promise.resolve();

    expect(document.elementFromPoint(1, 1)).toBe(modal.el);
    expect(texts(modal.el, document.body)).toEqual(["", "", "", ""]);
  });
});

describe("a written region keeps its text while no covering modal closes, opens again or leaves the document", () => {
  it("the modal's, through the app's renders beside and inside it", async () => {
    mountLiveRegions(document.body);
    const modal = createModal(document.createElement("div"));
    modal.open();
    // Lands in the opening's own task (fake timers): the opening's record is not yet delivered.
    announce("kept");
    vi.advanceTimersByTime(100);
    const beside = document.createElement("p");
    document.body.append(beside);
    modal.el.append(document.createElement("p"));
    beside.remove();
    await Promise.resolve();

    expect(pairIn(modal.el).polite.textContent).toBe("kept");
  });

  it("the modal's, when a non-modal dialog under it closes", async () => {
    mountLiveRegions(document.body);
    const shown = callerDialog();
    shown.show();
    const modal = createModal(document.createElement("div"));
    modal.open();
    await Promise.resolve();
    announce("kept");
    vi.advanceTimersByTime(100);
    shown.close();
    await Promise.resolve();

    expect(pairIn(modal.el).polite.textContent).toBe("kept");
  });

  it("a modal's, when another modal opens over it", async () => {
    mountLiveRegions(document.body);
    const lower = createModal(document.createElement("div"));
    lower.open();
    // Lands in the opening's own task (fake timers): the opening's record is not yet delivered.
    announce("kept");
    vi.advanceTimersByTime(100);
    createModal(document.createElement("div")).open();
    await Promise.resolve();

    expect(pairIn(lower.el).polite.textContent).toBe("kept");
  });

  it("every pair, when the page moves the modal on top with moveBefore()", async () => {
    const { lower, upper } = announcedOverTwo();
    const section = document.createElement("section");
    document.body.append(section);
    section.moveBefore(upper, null);
    await Promise.resolve();

    expect(upper.matches(":modal")).toBe(true);
    expect(texts(upper, lower, document.body)).toEqual([
      "3 items",
      "Saved",
      "3 items",
      "Saved",
      "3 items",
      "Saved",
    ]);
  });
});

describe("a dialog's pair lives from the library's first call on it", () => {
  it("lets an announcement made as the page opens a driven dialog land on time", async () => {
    const dlg = callerDialog();
    createDialog(dlg);
    dlg.showModal();
    announce("x");
    await vi.advanceTimersByTimeAsync(100);

    expect(pairIn(dlg).polite.textContent).toBe("x");
  });

  it("createDialog mounts it, and it stays through the close and dispose", async () => {
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    const pair = pairIn(dlg);
    controller.open();
    expect(pairIn(dlg).polite).toBe(pair.polite);
    const closed = nextClose(dlg);
    controller.close();
    vi.advanceTimersByTime(SETTLE_MS);
    await closed;
    controller.dispose();

    expect(pairIn(dlg).polite).toBe(pair.polite);
    expect(pairIn(dlg).assertive).toBe(pair.assertive);
  });

  it("createModal mounts it before the modal opens", () => {
    const modal = createModal(document.createElement("div"));
    pairIn(modal.el);
  });

  it("an open puts back, before showModal(), a pair the app's render removed", () => {
    const seen = regionsAtShowModal();
    const dlg = callerDialog();
    const controller = createDialog(dlg);
    dlg.replaceChildren(document.createElement("p"));
    controller.open();

    expect(seen).toEqual([2]);
    pairIn(dlg);
  });
});

describe("createDialog: reopened by caller code its dispose reaches", () => {
  it("the open() from the restored focus throws, and the dialog stays closed", () => {
    mountLiveRegions(document.body);
    const opener = document.createElement("button");
    const dlg = document.createElement("dialog");
    dlg.appendChild(document.createElement("button"));
    document.body.append(opener, dlg);
    opener.focus();
    const controller = createDialog(dlg);
    controller.open();
    let thrown: unknown;
    opener.addEventListener(
      "focus",
      () => {
        try {
          controller.open();
        } catch (error) {
          thrown = error;
        }
      },
      { once: true },
    );

    controller.dispose();
    expect(document.activeElement).toBe(opener);
    expect(thrown).toBeInstanceOf(DOMException);
    expect((thrown as DOMException).name).toBe("InvalidStateError");
    expect(dlg.open).toBe(false);
    expect(dlg.classList.contains("uip-dialog")).toBe(false);
    announce("after");
    vi.advanceTimersByTime(100);
    expect(pairIn(document.body).polite.textContent).toBe("after");

    dlg.showModal();
    const cancel = new Event("cancel", { cancelable: true });
    dlg.dispatchEvent(cancel);
    expect(cancel.defaultPrevented).toBe(false);
    dlg.close();
  });
});
