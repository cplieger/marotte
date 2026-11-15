// A disabled item stays in the menu and its roving focus (WAI-ARIA menu pattern), and a click on it keeps the menu open.
import { describe, it, expect, vi, afterEach } from "vitest";

import { showContextMenu } from "./context-menu.js";

let dismiss: (() => void) | null = null;

afterEach(() => {
  dismiss?.();
  dismiss = null;
});

function items(): HTMLButtonElement[] {
  return [...document.querySelectorAll<HTMLButtonElement>(".tab-context-item")];
}

describe("a disabled context-menu item", () => {
  it("stays in the menu as a focusable item marked unavailable", () => {
    dismiss = showContextMenu(
      [
        { label: "Move up", disabled: true, action: vi.fn() },
        { label: "Move down", action: vi.fn() },
      ],
      { x: 10, y: 10 },
    );
    const [up, down] = items();
    expect(up?.getAttribute("aria-disabled")).toBe("true");
    expect(up?.disabled, "focusable, so not the disabled attribute").toBe(false);
    expect(down?.hasAttribute("aria-disabled")).toBe(false);
  });

  it("does nothing on a click and leaves the menu open", () => {
    const action = vi.fn();
    dismiss = showContextMenu([{ label: "Move up", disabled: true, action }], { x: 10, y: 10 });
    items()[0]?.click();
    expect(action).not.toHaveBeenCalled();
    expect(items()).toHaveLength(1);
  });

  it("runs an enabled item and closes the menu", () => {
    const action = vi.fn();
    dismiss = showContextMenu([{ label: "Move down", action }], { x: 10, y: 10 });
    items()[0]?.click();
    expect(action).toHaveBeenCalledTimes(1);
    expect(items()).toHaveLength(0);
  });
});

describe("the element a context menu belongs to", () => {
  let owners: HTMLElement[] = [];
  afterEach(() => {
    for (const o of owners) {
      o.remove();
    }
    owners = [];
  });
  function owner(): HTMLButtonElement {
    const b = document.createElement("button");
    document.body.append(b);
    owners.push(b);
    return b;
  }

  it("carries data-menu-open while its menu shows and loses it on Escape", () => {
    const row = owner();
    dismiss = showContextMenu(
      [{ label: "Reopen", action: vi.fn() }],
      { x: 10, y: 10 },
      { returnFocus: row },
    );
    expect(row.hasAttribute("data-menu-open")).toBe(true);
    document.activeElement?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(row.hasAttribute("data-menu-open")).toBe(false);
  });

  it("moves to the next menu's element when that menu replaces it", () => {
    const first = owner();
    const second = owner();
    showContextMenu(
      [{ label: "Rename", action: vi.fn() }],
      { x: 10, y: 10 },
      { returnFocus: first },
    );
    dismiss = showContextMenu(
      [{ label: "Reopen", action: vi.fn() }],
      { x: 40, y: 40 },
      { returnFocus: second },
    );
    expect(first.hasAttribute("data-menu-open")).toBe(false);
    expect(second.hasAttribute("data-menu-open")).toBe(true);
  });
});

describe("a second context menu", () => {
  it("closes the open one, so one menu shows", () => {
    const first = vi.fn();
    showContextMenu([{ label: "Rename", action: first }], { x: 10, y: 10 });
    dismiss = showContextMenu([{ label: "Reopen", action: vi.fn() }], { x: 40, y: 40 });
    const open = [...document.querySelectorAll(".tab-context-menu")].filter(
      (m) => !m.classList.contains("is-leaving"),
    );
    expect(open).toHaveLength(1);
    expect(items().map((b) => b.textContent)).toEqual(["Reopen"]);
  });
});
