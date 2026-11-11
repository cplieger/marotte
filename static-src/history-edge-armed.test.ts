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

  it("asks for no older page when a shrink clamps the controller's own pin onto the new bottom", async () => {
    const s = scene(VIEWPORT_PX / 4);
    s.controller.attach({ el: s.view, scrollTop: 0, readingState: "following" });
    s.controller.applyPendingRestore();
    const load = vi.fn();
    s.controller.setLoadMore(load, true);
    await frames(5);
    // The page lands: the controller pins its bottom in the next frame. A frame callback queued after
    // that pin shrinks the page, so the browser clamps the pin's landing before its scroll event, onto
    // a bottom whose history edge is inside the lead.
    const page = document.createElement("div");
    page.style.height = `${String(VIEWPORT_PX * 8)}px`;
    s.view.appendChild(page);
    await Promise.resolve();
    requestAnimationFrame(() => {
      page.style.height = `${String(VIEWPORT_PX)}px`;
    });
    await frames(5);
    const premise = {
      atBottom: s.scroller.scrollTop === s.scroller.scrollHeight - s.scroller.clientHeight,
      top: s.scroller.scrollTop,
    };
    expect(premise, "the pin was clamped onto the shrunk page's bottom").toEqual({
      atBottom: true,
      top: VIEWPORT_PX * 0.25,
    });
    expect(load, "older-page requests with no reader gesture").not.toHaveBeenCalled();
  });

  it("asks for no older page when the reader's first scroll leaves the edge before the observer reports it", async () => {
    const s = scene(VIEWPORT_PX / 4);
    s.controller.attach({ el: s.view, scrollTop: 0, readingState: "following" });
    s.controller.applyPendingRestore();
    const load = vi.fn();
    s.controller.setLoadMore(load, true);
    await frames(5);
    // The page lands and the reader scrolls down to its end in one task: the scroll event is dispatched in the next
    // frame, before the edge observer's report of the new geometry. A move down keeps a Following reader Following,
    // so only the end is a landing the follower does not move.
    const page = document.createElement("div");
    page.style.height = `${String(VIEWPORT_PX * 8)}px`;
    s.view.appendChild(page);
    const end = s.scroller.scrollHeight - VIEWPORT_PX;
    s.scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: 120 }));
    s.scroller.scrollTop = end;
    await frames(3);
    expect(s.scroller.scrollTop, "the reader's scroll landed").toBe(end);
    expect(load, "older-page requests with the edge 7 viewports away").not.toHaveBeenCalled();
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
