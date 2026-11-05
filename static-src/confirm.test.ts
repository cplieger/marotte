// A confirm reads as a question over its consequences: the title names the dialog, the body describes it, and a
// question with nothing to add shows no empty body.
import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { confirm } from "./confirm.js";

let styles: HTMLStyleElement | undefined;
beforeAll(() => {
  styles = mountAppCSS();
});
afterAll(() => {
  styles?.remove();
});

function openDialog(): HTMLDialogElement {
  const dialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
  if (dialog === null) {
    throw new Error("no open confirm");
  }
  return dialog;
}

afterEach(() => {
  document.querySelector<HTMLButtonElement>("dialog.uip-ask[open] .uip-ask-cancel")?.click();
});

describe("a confirm's layout", () => {
  it("puts the question in the title and the consequences in the body", async () => {
    const answer = confirm({
      title: "Delete notes.md?",
      body: "This cannot be undone.",
      confirmLabel: "Delete",
      variant: "destructive",
    });
    const dialog = openDialog();
    const title = dialog.querySelector<HTMLElement>(".uip-ask-title");
    const body = dialog.querySelector<HTMLElement>(".uip-ask-msg");
    expect(title?.textContent).toBe("Delete notes.md?");
    expect(title?.hidden).toBe(false);
    expect(body?.textContent).toBe("This cannot be undone.");
    expect(dialog.getAttribute("aria-labelledby")).toBe(title?.id);
    expect(dialog.getAttribute("aria-describedby")).toBe(body?.id);
    expect(getComputedStyle(title!).fontSize).not.toBe(getComputedStyle(body!).fontSize);
    dialog.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await expect(answer).resolves.toBe(false);
  });

  it("shows no body when the title says everything", async () => {
    const answer = confirm({ title: "Reopen PR #12?", confirmLabel: "Reopen" });
    const dialog = openDialog();
    const body = dialog.querySelector<HTMLElement>(".uip-ask-msg");
    expect(body?.textContent).toBe("");
    expect(getComputedStyle(body!).display).toBe("none");
    expect(dialog.querySelector(".uip-ask-ok")?.textContent).toBe("Reopen");
    dialog.querySelector<HTMLButtonElement>(".uip-ask-ok")?.click();
    await expect(answer).resolves.toBe(true);
  });

  it("fills the confirm button, unlike Cancel", async () => {
    const answer = confirm({ title: "Discard?", confirmLabel: "Discard", variant: "destructive" });
    const dialog = openDialog();
    const ok = dialog.querySelector<HTMLElement>(".uip-ask-ok")!;
    const cancel = dialog.querySelector<HTMLElement>(".uip-ask-cancel")!;
    expect(getComputedStyle(ok).backgroundColor).toBe(getComputedStyle(ok).borderTopColor);
    expect(getComputedStyle(ok).backgroundColor).not.toBe(getComputedStyle(cancel).backgroundColor);
    cancel.click();
    await expect(answer).resolves.toBe(false);
  });
});
