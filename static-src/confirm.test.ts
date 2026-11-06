// A confirm reads as a question over its consequences: the title names the dialog, the body describes it, and a
// question with nothing to add shows no empty body.
import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";
import { userEvent } from "vitest/browser";

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

const probes: HTMLElement[] = [];

/** An element styled by the app CSS alone, for a reference computed value. */
function probe(tag: string, className: string, style = ""): HTMLElement {
  const node = document.createElement(tag);
  node.className = className;
  node.style.cssText = style;
  document.body.append(node);
  probes.push(node);
  return node;
}

/** The shared dialog keeps its OK button between questions, so a variant change transitions its fill. */
async function settled(node: HTMLElement): Promise<void> {
  await Promise.all(node.getAnimations().map((a) => a.finished));
}

afterEach(() => {
  document.querySelector<HTMLButtonElement>("dialog.uip-ask[open] .uip-ask-cancel")?.click();
  probes.splice(0).forEach((node) => {
    node.remove();
  });
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

  it("wraps a long path in the title inside the dialog", async () => {
    const path = "src/" + "deeplynesteddirectory/".repeat(8) + "file.ts";
    const answer = confirm({ title: `Discard ${path}?`, confirmLabel: "Discard" });
    const dialog = openDialog();
    const title = dialog.querySelector<HTMLElement>(".uip-ask-title")!;
    expect(title.scrollWidth).toBeLessThanOrEqual(title.clientWidth);
    dialog.querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
    await expect(answer).resolves.toBe(false);
  });

  it("fills a normal confirm button as the app's primary button", async () => {
    const primary = probe("button", "btn-small btn-primary");
    const answer = confirm({ title: "Reopen PR #12?", confirmLabel: "Reopen" });
    const ok = openDialog().querySelector<HTMLElement>(".uip-ask-ok")!;
    await settled(ok);
    expect(getComputedStyle(ok).backgroundColor).toBe(getComputedStyle(primary).backgroundColor);
    expect(getComputedStyle(ok).color).toBe(getComputedStyle(primary).color);
    ok.click();
    await expect(answer).resolves.toBe(true);
  });

  it.each(["normal", "destructive"] as const)(
    "keeps the on-accent ink on a hovered %s confirm button",
    async (variant) => {
      const ink = getComputedStyle(probe("span", "", "color: var(--c-on-accent)")).color;
      const answer = confirm({ title: "Go?", confirmLabel: "Go", variant });
      const ok = openDialog().querySelector<HTMLElement>(".uip-ask-ok")!;
      await userEvent.hover(ok);
      await settled(ok);
      expect(ok.matches(":hover")).toBe(true);
      expect(getComputedStyle(ok).color).toBe(ink);
      openDialog().querySelector<HTMLButtonElement>(".uip-ask-cancel")?.click();
      await expect(answer).resolves.toBe(false);
    },
  );
});
