// A disabled menu item: still in the menu and its roving focus (the WAI-ARIA menu
// pattern), announced as unavailable, and inert to a click, which leaves the menu
// open rather than dismissing it for nothing.
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
