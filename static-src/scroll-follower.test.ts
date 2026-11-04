import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { platformScroll, scrollEnds, touchEvent } from "./__test-helpers__/scroll-scene.js";
import { framesDrained } from "./__test-helpers__/frame-queue.js";

const scroll = await import("./scroll-controller.js");

/** The follower's frame schedule, which a positive pins; an absence reads after `drained()`. */
async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
  // Resumed on a task, after the last frame's ResizeObserver delivery and the write it made.
  await new Promise((r) => setTimeout(r, 0));
}

/** Until no frame callback is left queued, so a write the follower still owes is seen. */
async function drained(): Promise<void> {
  await framesDrained("the follower's queued frames");
}

function block(height: number): HTMLElement {
  const b = document.createElement("div");
  b.style.height = `${String(height)}px`;
  return b;
}

let box: HTMLElement;
let view: HTMLElement;
let resume: HTMLButtonElement;

beforeEach(() => {
  box = document.createElement("div");
  box.style.cssText = "height:200px;width:300px;overflow-y:auto";
  view = document.createElement("div");
  box.appendChild(view);
  resume = document.createElement("button");
  resume.className = "hidden";
  resume.appendChild(document.createElement("span"));
  document.body.append(box, resume);
});

afterEach(() => {
  box.remove();
  resume.remove();
});

describe("createFollowScroller", () => {
  it("keeps a following view at its live edge as it grows", async () => {
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 0, readingState: "following" });
    view.appendChild(block(600));
    await frames();
    expect(box.scrollTop).toBe(box.scrollHeight - box.clientHeight);
    view.appendChild(block(400));
    await frames();
    expect(box.scrollTop).toBe(box.scrollHeight - box.clientHeight);
    expect(f.detach().readingState).toBe("following");
  });

  it("parks on a reader's scroll up and shows its own resume control", async () => {
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 0, readingState: "following" });
    view.appendChild(block(1000));
    await frames();
    box.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    box.scrollTop = 100;
    await frames();
    expect(resume.classList.contains("hidden")).toBe(false);
    // A reading view holds its place while it grows.
    view.appendChild(block(400));
    await drained();
    const { scrollTop, readingState } = f.detach();
    expect({ scrollTop, readingState }).toEqual({ scrollTop: 100, readingState: "reading" });
  });

  it("restores a parked reading position on attach", async () => {
    view.appendChild(block(1000));
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 300, readingState: "reading" });
    await frames();
    expect(box.scrollTop).toBe(300);
    expect(resume.classList.contains("hidden")).toBe(false);
  });

  // Two controllers share the document's keys; only the one whose scroller is on screen acts.
  it("leaves a hidden scroller's reader parked when End is pressed elsewhere", async () => {
    view.appendChild(block(1000));
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 300, readingState: "reading" });
    await frames();
    box.style.display = "none";
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    box.style.removeProperty("display");
    await drained();
    const { scrollTop, readingState } = f.detach();
    expect({ scrollTop, readingState }).toEqual({ scrollTop: 300, readingState: "reading" });
  });

  it("never writes the transcript's --scrollbar-w", async () => {
    box.style.scrollbarGutter = "stable";
    document.documentElement.style.setProperty("--scrollbar-w", "sentinel");
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 0, readingState: "following" });
    view.appendChild(block(1000));
    box.style.width = "320px";
    await drained();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("sentinel");
    document.documentElement.style.removeProperty("--scrollbar-w");
  });

  /** Following at the end (800) of 980px plus a 20px run clock; the platform carries the reader up `px` by one
   *  animated scroll after `input`, the clock ticks with the input's window long past, and the follower writes no
   *  position of its own. */
  async function staysWhereTheMotionLeft(input: () => void, px: number): Promise<unknown> {
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 0, readingState: "following" });
    view.appendChild(block(980));
    const clock = block(20);
    clock.textContent = "0:00";
    view.appendChild(clock);
    await frames();
    expect(box.scrollTop, "the premise: at the end").toBe(800);
    let now = 1000;
    const date = vi.spyOn(Date, "now").mockImplementation(() => now);
    try {
      input();
      await frames(1);
      const from = box.scrollTop;
      // Past a 300ms input window once the animation is under way, as an engine's own tick outlives it.
      const step = (): void => {
        if (from - box.scrollTop > 2) {
          box.removeEventListener("scroll", step);
          now += 350;
        }
      };
      box.addEventListener("scroll", step);
      platformScroll(box, 800 - px, "smooth");
      await scrollEnds(box);
      now += 1000;
      for (let s = 1; s <= 3; s++) {
        clock.firstChild!.nodeValue = `0:0${String(s)}`;
        now += 1000;
        await frames(20);
      }
      await drained();
    } finally {
      date.mockRestore();
    }
    const { scrollTop, readingState } = f.detach();
    return { scrollTop, readingState };
  }

  it("stays where a wheel tick's own animation left the reader", async () => {
    const result = await staysWhereTheMotionLeft(() => {
      box.dispatchEvent(new WheelEvent("wheel", { deltaY: -3, deltaMode: 1 }));
    }, 57);
    expect(result).toEqual({ scrollTop: 743, readingState: "reading" });
  });

  it("stays where a touch fling's momentum left the reader", async () => {
    const result = await staysWhereTheMotionLeft(() => {
      box.dispatchEvent(touchEvent("touchstart", box, 100));
      for (let i = 1; i <= 4; i++) {
        box.dispatchEvent(touchEvent("touchmove", box, 100 + 10 * i));
        platformScroll(box, 800 - 10 * i);
      }
      box.dispatchEvent(touchEvent("touchend", box));
    }, 90);
    expect(result).toEqual({ scrollTop: 710, readingState: "reading" });
  });
});
