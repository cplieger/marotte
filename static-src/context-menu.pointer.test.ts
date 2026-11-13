// macOS Safari opens the context menu on a Control-click's press and still sends `click` on its release
// (https://stackoverflow.com/q/53217104). Linux engines make no `contextmenu` of a Control-click, so the
// press listener below stands in for macOS's conversion; the release and its click are the engine's own.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { userEvent } from "vitest/browser";

import { showContextMenu } from "./context-menu.js";

let dismiss: () => void = () => undefined;

function asMacOS(e: MouseEvent): void {
  if (e.ctrlKey && e.button === 0 && e.target instanceof Element) {
    e.target.dispatchEvent(
      new MouseEvent("contextmenu", {
        bubbles: true,
        cancelable: true,
        ctrlKey: true,
        buttons: 1,
        clientX: e.clientX,
        clientY: e.clientY,
      }),
    );
  }
}

function button(label: string, left: number): HTMLButtonElement {
  const b = document.createElement("button");
  b.textContent = label;
  b.style.cssText = `position:fixed;left:${String(left)}px;top:40px;width:120px;height:32px`;
  document.body.append(b);
  return b;
}

function menuOpen(): boolean {
  const menu = document.querySelector(".tab-context-menu");
  return menu !== null && !menu.classList.contains("is-leaving");
}

beforeEach(() => {
  window.addEventListener("mousedown", asMacOS, true);
});

afterEach(() => {
  window.removeEventListener("mousedown", asMacOS, true);
  dismiss();
  dismiss = () => undefined;
  document.body.replaceChildren();
});

describe("a context menu opened by a Control-click", () => {
  it("stays open through the click its release sends, and the next click outside closes it", async () => {
    const tab = button("tab", 40);
    const elsewhere = button("elsewhere", 400);
    tab.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      dismiss = showContextMenu([{ label: "Rename\u2026", action: () => undefined }], {
        x: e.clientX,
        y: e.clientY,
      });
    });
    await userEvent.click(tab, { modifiers: ["Control"] });
    expect(menuOpen(), "open after the release").toBe(true);
    await userEvent.click(elsewhere);
    expect(document.querySelector(".tab-context-menu")).toBeNull();
  });
});
