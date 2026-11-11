import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createPopover, type PopoverController } from "./popover.js";

let anchor: HTMLButtonElement;
let panel: HTMLDivElement;
let controller: PopoverController | null = null;

beforeEach(() => {
  // Fake timers that the test never advances: a page whose task queue is saturated runs input
  // events but not timers, so a dismissal armed on a timer never arrives.
  vi.useFakeTimers();
  anchor = document.createElement("button");
  panel = document.createElement("div");
  document.body.append(anchor);
});

afterEach(() => {
  controller?.dispose();
  controller = null;
  anchor.remove();
  panel.remove();
  vi.useRealTimers();
});

describe("popup dismissal arming", () => {
  it("closes on an Escape that arrives before any timer runs", () => {
    controller = createPopover(anchor, panel);
    controller.show();
    expect(controller.isOpen).toBe(true);

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));

    expect(controller.isOpen).toBe(false);
    expect(anchor.getAttribute("aria-expanded")).toBe("false");
  });

  it("an outside click before any timer runs closes it, but the opening click does not", () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    controller = createPopover(anchor, panel);
    const shown = controller;
    opener.addEventListener("click", () => {
      shown.show();
    });

    opener.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(shown.isOpen).toBe(true);
    document.body.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(shown.isOpen).toBe(false);
    opener.remove();
  });

  it("the Escape that opened it does not close it, and the next Escape does", () => {
    controller = createPopover(anchor, panel);
    const shown = controller;
    anchor.addEventListener("keydown", () => {
      if (!shown.isOpen) {
        shown.show();
      }
    });

    anchor.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(shown.isOpen).toBe(true);
    anchor.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(shown.isOpen).toBe(false);
  });
});
