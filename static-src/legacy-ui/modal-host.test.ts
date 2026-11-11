import { describe, it, expect, afterEach, vi } from "vitest";

import { createDialog, openDialog as openTracked } from "./dialog.js";
import { topmostOpenDialog } from "./modal-host.js";
import type * as ModalHost from "./modal-host.js";

/** Long enough for the engine to realize a change the page made. */
async function toggled(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 10));
}

// `matches` is stubbed per dialog to declare which ones the platform would
// call modal, so a stack of open dialogs can be stated directly rather than
// driven through real showModal() calls.

afterEach(() => {
  document.body.innerHTML = "";
});

/** An open <dialog> whose `:modal` answer is `modal`. */
function openDialog(modal: boolean): HTMLDialogElement {
  const d = document.createElement("dialog");
  d.setAttribute("open", "");
  document.body.appendChild(d);
  vi.spyOn(d, "matches").mockImplementation((selector: string) => selector === ":modal" && modal);
  return d;
}

describe("topmostOpenDialog", () => {
  it("returns null when no dialog is open", () => {
    document.body.appendChild(document.createElement("dialog")); // present but closed
    expect(topmostOpenDialog()).toBeNull();
  });

  it("prefers a modal dialog over a later non-modal one", () => {
    // Chrome must land inside the MODAL one, the only subtree showModal() leaves live.
    const modal = openDialog(true);
    openDialog(false);
    expect(topmostOpenDialog()).toBe(modal);
  });

  it("takes the last modal dialog when several are open", () => {
    openDialog(true);
    const newest = openDialog(true);
    expect(topmostOpenDialog()).toBe(newest);
  });

  it("falls back to the last open dialog when none reports itself modal", () => {
    openDialog(false);
    const last = openDialog(false);
    expect(topmostOpenDialog()).toBe(last);
  });

  it("ranks dialogs opened through openDialog by open order", () => {
    const first = document.createElement("dialog");
    const second = document.createElement("dialog");
    document.body.append(first, second);
    openTracked(second);
    openTracked(first);
    expect(topmostOpenDialog()).toBe(first);

    first.close();
    expect(topmostOpenDialog()).toBe(second);
  });

  it("hosts into a modal the page opened over a dialog the library holds", async () => {
    const page = document.createElement("dialog");
    const held = document.createElement("dialog");
    document.body.append(page, held);
    openTracked(held);
    await toggled();
    page.showModal();
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(page);
    expect(topmostOpenDialog()).toBe(page);
  });

  it("hosts into a modal the page opened in a shadow root a controller reached, over a held dialog", async () => {
    const held = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(held, host);
    const watched = document.createElement("dialog");
    const page = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(watched, page);
    createDialog(watched);
    openTracked(held);
    page.showModal();
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(page);
  });

  it("ranks a shadow-root dialog the page opened before any helper reached its root below an observed opening", async () => {
    const seen = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(seen, host);
    const unseen = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(unseen);
    openTracked(seen);
    await toggled();
    unseen.showModal();
    createDialog(unseen);
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(seen);
  });

  it("hosts into a dialog the library opens after it moved from the document into a shadow root, over an older modal", async () => {
    const older = document.createElement("dialog");
    const moved = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(older, moved, host);
    createDialog(moved);
    host.attachShadow({ mode: "open" }).append(moved);
    openTracked(older);
    await toggled();
    openTracked(moved);
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(moved);
  });

  it("hosts into a dialog the library opens after it moved from one shadow root to another, over an older modal", async () => {
    const older = document.createElement("dialog");
    const first = document.createElement("div");
    const second = document.createElement("div");
    document.body.append(older, first, second);
    const moved = document.createElement("dialog");
    first.attachShadow({ mode: "open" }).append(moved);
    createDialog(moved);
    second.attachShadow({ mode: "open" }).append(moved);
    openTracked(older);
    await toggled();
    openTracked(moved);
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(second);
    expect(topmostOpenDialog()).toBe(moved);
  });

  it("ranks the page's showModal() of a driven dialog moved into a shadow root newest, from its focus steps on", async () => {
    const lower = document.createElement("dialog");
    const moved = document.createElement("dialog");
    moved.append(document.createElement("button"));
    const host = document.createElement("div");
    document.body.append(lower, moved, host);
    createDialog(moved);
    host.attachShadow({ mode: "open" }).append(moved);
    openTracked(lower);
    // Past the microtask, so the document's registration no longer follows the moved node.
    await toggled();
    let duringFocus: HTMLDialogElement | null | undefined;
    moved.addEventListener(
      "focus",
      () => {
        duringFocus = topmostOpenDialog();
      },
      { capture: true, once: true },
    );
    moved.showModal();
    const afterCall = topmostOpenDialog();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(duringFocus).toBe(moved);
    expect(afterCall).toBe(moved);
  });

  it("sees the page's modal in the shadow root a driven dialog was moved into once the library opened it there", () => {
    const moved = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(moved, host);
    createDialog(moved);
    const sibling = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(moved, sibling);
    openTracked(moved);
    sibling.showModal();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(sibling);
  });

  it("sees the page's modal in the shadow root a driven dialog was moved into once the page's opening of it was ranked", async () => {
    const moved = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(moved, host);
    createDialog(moved);
    const sibling = document.createElement("dialog");
    host.attachShadow({ mode: "open" }).append(moved, sibling);
    await toggled();
    moved.showModal();
    expect(topmostOpenDialog()).toBe(moved);
    sibling.showModal();

    expect(document.elementFromPoint(1, 1)).toBe(host);
    expect(topmostOpenDialog()).toBe(sibling);
  });

  it("still counts a driven dialog moved into an unreached shadow root while it is open", () => {
    const moved = document.createElement("dialog");
    const host = document.createElement("div");
    document.body.append(moved, host);
    createDialog(moved);
    moved.show();
    expect(topmostOpenDialog()).toBe(moved);
    host.attachShadow({ mode: "open" }).append(moved);

    expect(moved.open).toBe(true);
    expect(topmostOpenDialog()).toBe(moved);
  });

  it("ranks an opening inside a detached driven dialog first reached through a driven dialog nested in it", () => {
    const older = document.createElement("dialog");
    document.body.append(older);
    older.show();
    expect(topmostOpenDialog()).toBe(older);
    const outer = document.createElement("dialog");
    const inner = document.createElement("dialog");
    const nested = document.createElement("dialog");
    outer.append(inner, nested);
    createDialog(inner);
    createDialog(outer);
    nested.show();
    document.body.append(outer);

    expect(topmostOpenDialog()).toBe(nested);
  });

  it("ranks the page's openings by their order from the moment the module loads, before any controller exists", async () => {
    const later = document.createElement("dialog");
    const earlier = document.createElement("dialog");
    document.body.append(earlier, later);
    const fresh = (await import(
      /* @vite-ignore */ `./modal-host.ts?fresh=${String(Date.now())}`
    )) as typeof ModalHost;
    earlier.showModal();
    later.showModal();
    earlier.close();
    earlier.showModal();

    expect(document.elementFromPoint(1, 1)).toBe(earlier);
    expect(fresh.topmostOpenDialog()).toBe(earlier);
  });

  it("skips a tracked dialog once it leaves the document", () => {
    const tracked = document.createElement("dialog");
    document.body.appendChild(tracked);
    openTracked(tracked);
    const untracked = openDialog(true);
    tracked.remove();
    expect(topmostOpenDialog()).toBe(untracked);
  });

  it("drops a dialog's rank once the browser's close of it is seen", async () => {
    const tracked = document.createElement("dialog");
    const reused = document.createElement("dialog");
    document.body.append(tracked, reused);
    openTracked(tracked);
    openTracked(reused);
    const toggled = new Promise((resolve) => {
      reused.addEventListener("toggle", resolve, { once: true });
    });
    reused.close();
    await toggled;
    expect(topmostOpenDialog()).toBe(tracked);
  });

  it("ranks a direct showModal() of a dialog the library drives newest, before and after it is adopted", async () => {
    const tracked = document.createElement("dialog");
    const reused = document.createElement("dialog");
    document.body.append(tracked, reused);
    openTracked(reused);
    openTracked(tracked);
    reused.close();
    await toggled();
    reused.showModal();
    expect(topmostOpenDialog()).toBe(reused);
    await toggled();
    expect(topmostOpenDialog()).toBe(reused);
  });

  it("keeps an already-open dialog's rank when it is opened again", () => {
    const lower = document.createElement("dialog");
    const upper = document.createElement("dialog");
    document.body.append(lower, upper);
    openTracked(lower);
    openTracked(upper);
    openTracked(lower);
    expect(topmostOpenDialog()).toBe(upper);
  });

  it("ranks a dialog the page opened before its controller existed below one the library opened after it", async () => {
    const early = document.createElement("dialog");
    const late = document.createElement("dialog");
    document.body.append(early, late);
    early.showModal();
    openTracked(late);
    createDialog(early);
    await toggled();

    expect(document.elementFromPoint(1, 1)).toBe(late);
    expect(topmostOpenDialog()).toBe(late);
  });

  it("takes the modal the page opened last, not the last in document order", () => {
    const watched = document.createElement("dialog");
    const earlier = document.createElement("dialog");
    const later = document.createElement("dialog");
    document.body.append(watched, earlier, later);
    createDialog(watched);
    later.showModal();
    earlier.showModal();

    expect(document.elementFromPoint(1, 1)).toBe(earlier);
    expect(topmostOpenDialog()).toBe(earlier);
  });

  it("ranks dialogs inside a shadow root by the order the library opened them", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = host.attachShadow({ mode: "open" });
    const first = document.createElement("dialog");
    const second = document.createElement("dialog");
    root.append(first, second);
    openTracked(second);
    openTracked(first);

    expect(topmostOpenDialog()).toBe(first);
  });

  it("hosts into the open modal, not a held dialog the page reopened with show() after it", () => {
    const shown = document.createElement("dialog");
    const modal = document.createElement("dialog");
    document.body.append(shown, modal);
    openTracked(shown);
    openTracked(modal);
    shown.close();
    shown.show();
    expect(topmostOpenDialog()).toBe(modal);
  });
});
