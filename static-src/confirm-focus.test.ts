// A confirm opened from a control returns focus to that control at close, even where the click gave the control no
// focus (WebKit's buttons), so the dialog's restore lands on the opener, not on the transcript scroller.
import { describe, it, expect, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { confirm } from "./confirm.js";

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
  document.querySelector<HTMLDialogElement>("dialog.uip-ask")?.close();
});

function closed(dialog: HTMLDialogElement): Promise<void> {
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

describe("a confirm's focus", () => {
  it("returns to the button that opened it, not to the element focused before the click", async () => {
    const scroller = document.createElement("div");
    scroller.tabIndex = -1;
    const opener = document.createElement("button");
    opener.type = "button";
    opener.textContent = "Rewind";
    // WebKit gives a clicked button no focus; a cancelled mousedown is that in Chromium.
    opener.addEventListener("mousedown", (e) => {
      e.preventDefault();
    });
    let answer: Promise<boolean> | undefined;
    opener.addEventListener("click", () => {
      answer = confirm({
        title: "Rewind to this turn?",
        confirmLabel: "Rewind",
        variant: "destructive",
      });
    });
    document.body.append(scroller, opener);
    hosts.push(scroller, opener);
    scroller.focus();

    await userEvent.click(opener);
    const dialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask");
    if (dialog === null || answer === undefined) {
      throw new Error("the click opened no confirm");
    }
    const shut = closed(dialog);
    const cancel = dialog.querySelector<HTMLButtonElement>(".uip-ask-cancel");
    if (cancel === null) {
      throw new Error("no Cancel");
    }
    await userEvent.click(cancel);

    await expect(answer).resolves.toBe(false);
    await shut;
    expect(document.activeElement).toBe(opener);
  });

  it("leaves focus alone for a confirm no click opened", async () => {
    const field = document.createElement("input");
    document.body.append(field);
    hosts.push(field);
    field.focus();
    await new Promise((r) => setTimeout(r, 0));

    const answer = confirm({ title: "Discard?" });
    const dialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask");
    if (dialog === null) {
      throw new Error("no confirm");
    }
    const shut = closed(dialog);
    dialog.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();

    await expect(answer).resolves.toBe(false);
    await shut;
    expect(document.activeElement).toBe(field);
  });

  it("resolves once its dialog has closed, so a focus the caller moves then lands", async () => {
    const opener = document.createElement("button");
    opener.type = "button";
    opener.textContent = "Rewind";
    const field = document.createElement("textarea");
    let answer: Promise<boolean> | undefined;
    opener.addEventListener("click", () => {
      answer = confirm({ title: "Rewind to this turn?", confirmLabel: "Rewind" });
    });
    document.body.append(opener, field);
    hosts.push(opener, field);

    await userEvent.click(opener);
    const ok = document.querySelector<HTMLButtonElement>("dialog.uip-ask[open] .uip-ask-ok");
    if (ok === null || answer === undefined) {
      throw new Error("the click opened no confirm");
    }
    await userEvent.click(ok);
    await expect(answer).resolves.toBe(true);
    field.focus();

    expect(document.activeElement).toBe(field);
  });

  it("hands focus to the first control left beside its opener when the opener is gone", async () => {
    const box = document.createElement("div");
    const opener = document.createElement("button");
    opener.type = "button";
    opener.textContent = "Rewind";
    const other = document.createElement("button");
    other.type = "button";
    other.textContent = "Copy";
    let answer: Promise<boolean> | undefined;
    opener.addEventListener("click", () => {
      answer = confirm({ title: "Rewind to this turn?", confirmLabel: "Rewind" });
    });
    box.append(opener, other);
    document.body.append(box);
    hosts.push(box);

    await userEvent.click(opener);
    const cancel = document.querySelector<HTMLButtonElement>(
      "dialog.uip-ask[open] .uip-ask-cancel",
    );
    if (cancel === null || answer === undefined) {
      throw new Error("the click opened no confirm");
    }
    opener.remove();
    await userEvent.click(cancel);
    await expect(answer).resolves.toBe(false);

    expect(document.activeElement).toBe(other);
  });

  it("resolves false when a later confirm preempts it, before that one is answered", async () => {
    const first = confirm({ title: "Delete notes.md?" });
    const second = confirm({ title: "Close this busy tab?" });

    await expect(first).resolves.toBe(false);

    const dialog = document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
    expect(dialog?.querySelector(".uip-ask-title")?.textContent, "the second still asks").toBe(
      "Close this busy tab?",
    );
    dialog?.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await expect(second).resolves.toBe(false);
  });
});
