import { afterEach, describe, expect, it, vi } from "vitest";

import { fakeViewport } from "./__test-helpers__/fake-viewport.js";
import { anchoredPopover } from "./anchored-popover.js";

const OFFSET_AND_MARGIN = 4 + 8;

let style: HTMLStyleElement | null = null;

afterEach(() => {
  document.body.replaceChildren();
  style?.remove();
  style = null;
  vi.unstubAllGlobals();
});

function anchorAt(top: number): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.textContent = "Open";
  btn.style.cssText = `position: fixed; top: ${String(top)}px; right: 16px; height: 30px`;
  document.body.append(btn);
  return btn;
}

function panelOf(text = "panel"): HTMLElement {
  const panel = document.createElement("div");
  panel.className = "t-panel";
  panel.textContent = text;
  return panel;
}

const frames = async (n = 2): Promise<void> => {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => requestAnimationFrame(r));
  }
};

describe.each([
  ["with no fade", ""],
  [
    "after its fade",
    ".t-panel { opacity: 0; transition: opacity 80ms } .t-panel.is-open { opacity: 1 }",
  ],
])("a closed popover %s", (_name, css) => {
  it("takes the panel it hosted out of the page", async () => {
    if (css !== "") {
      style = document.createElement("style");
      style.textContent = css;
      document.head.append(style);
    }
    const panel = panelOf();
    const pop = anchoredPopover(anchorAt(100), panel, { group: "t" });
    pop.show();
    expect(panel.isConnected, "hosted while open").toBe(true);
    pop.hide();
    await vi.waitFor(() => {
      expect(panel.isConnected).toBe(false);
    });
    pop.show();
    expect([pop.isOpen, panel.isConnected], "hosted again on the next open").toEqual([true, true]);
  });
});

describe("anchoredPopover", () => {
  it("leaves a panel the caller placed in the page where it is", async () => {
    const host = document.createElement("div");
    document.body.append(host);
    const panel = panelOf();
    host.append(panel);
    const pop = anchoredPopover(anchorAt(100), panel);
    pop.show();
    pop.hide();
    await vi.waitFor(() => {
      expect(panel.hidden).toBe(true);
    });
    expect(panel.parentElement).toBe(host);
  });

  it("closes an open peer of its group, and is closed by one", () => {
    const a = anchoredPopover(anchorAt(100), panelOf("a"), { group: "t" });
    const b = anchoredPopover(anchorAt(200), panelOf("b"), { group: "t" });
    a.show();
    b.show();
    expect([a.isOpen, b.isOpen]).toEqual([false, true]);
    a.show();
    expect([a.isOpen, b.isOpen]).toEqual([true, false]);
  });

  it("closes when its anchor leaves the page while it is open", async () => {
    const onClose = vi.fn();
    const btn = anchorAt(100);
    const panel = panelOf();
    const pop = anchoredPopover(btn, panel, { onClose });
    pop.show();
    btn.remove();
    await vi.waitFor(() => {
      expect(pop.isOpen).toBe(false);
    });
    expect(onClose).toHaveBeenCalledOnce();
    await vi.waitFor(() => {
      expect(panel.isConnected).toBe(false);
    });
  });

  it("caps the panel by the visible viewport as the keyboard moves it", async () => {
    const vv = fakeViewport(window.innerHeight, 0, window.innerWidth);
    vi.stubGlobal("visualViewport", vv);
    const btn = anchorAt(100);
    const panel = panelOf();
    const pop = anchoredPopover(btn, panel);
    pop.show();
    const room = (): string => panel.style.getPropertyValue("--popover-room");
    const below = (box: { offsetTop: number; height: number }): string =>
      `${String(box.offsetTop + box.height - btn.getBoundingClientRect().bottom - OFFSET_AND_MARGIN)}px`;
    expect(room()).toBe(below(vv));
    // A keyboard rising scrolls the visual viewport without resizing the layout one.
    vv.height = 500;
    vv.offsetTop = 40;
    vv.fire("scroll");
    await frames();
    expect(room()).toBe(below(vv));
    pop.hide();
    expect(vv.listeners(), "nothing listens once closed").toBe(0);
  });
});
