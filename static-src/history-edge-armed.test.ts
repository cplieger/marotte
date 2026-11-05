// Opening a chat costs one page: the history edge asks for an older page only once the reader has moved since the
// view attached, or when the window does not fill the scroller.
import { describe, it, expect, afterEach, vi } from "vitest";

import { ScrollController } from "./scroll-controller.js";

const VIEWPORT_PX = 400;

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

interface Scene {
  readonly scroller: HTMLElement;
  readonly view: HTMLElement;
  readonly controller: ScrollController;
}

/** A real scroller `VIEWPORT_PX` tall holding one transcript view of `contentPx` of turns. */
function scene(contentPx: number): Scene {
  const scroller = document.createElement("div");
  scroller.style.cssText = `height:${String(VIEWPORT_PX)}px;overflow-y:auto;position:relative`;
  const view = document.createElement("div");
  view.className = "transcript-view is-active";
  const turns = document.createElement("div");
  turns.style.height = `${String(contentPx)}px`;
  view.appendChild(turns);
  scroller.appendChild(view);
  const resume = document.createElement("button");
  document.body.append(scroller, resume);
  hosts.push(scroller, resume);
  const controller = new ScrollController(view, scroller, () => resume, false);
  controller.init();
  return { scroller, view, controller };
}

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}

describe("the history edge after an attach", () => {
  it("asks for no older page across five frames of a view two viewports tall at the live edge", async () => {
    const s = scene(VIEWPORT_PX * 2);
    s.controller.attach({ el: s.view, scrollTop: VIEWPORT_PX, readingState: "following" });
    s.controller.applyPendingRestore();
    const load = vi.fn();
    s.controller.setLoadMore(load, true);
    await frames(5);
    expect(load, "older-page requests with no reader gesture").not.toHaveBeenCalled();
  });

  it("asks once the reader scrolls up", async () => {
    const s = scene(VIEWPORT_PX * 2);
    s.controller.attach({ el: s.view, scrollTop: VIEWPORT_PX, readingState: "following" });
    s.controller.applyPendingRestore();
    const load = vi.fn();
    s.controller.setLoadMore(load, true);
    await frames(5);
    const before = load.mock.calls.length;
    s.scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120 }));
    s.scroller.scrollTop = VIEWPORT_PX - 120;
    await frames(3);
    expect(before, "requests before the gesture").toBe(0);
    expect(load, "requests after the gesture").toHaveBeenCalledTimes(1);
  });

  it("fills an underfilled window at attach", async () => {
    const s = scene(VIEWPORT_PX / 4);
    s.controller.attach({ el: s.view, scrollTop: 0, readingState: "following" });
    s.controller.applyPendingRestore();
    const load = vi.fn();
    s.controller.setLoadMore(load, true);
    s.controller.fillViewport();
    await frames(2);
    expect(load).toHaveBeenCalledTimes(1);
  });
});
