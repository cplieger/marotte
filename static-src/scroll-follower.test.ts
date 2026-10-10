import { describe, it, expect, beforeEach, afterEach } from "vitest";

const scroll = await import("./scroll-controller.js");

async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
  await new Promise((r) => setTimeout(r, 25));
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
    await frames();
    expect(f.detach()).toEqual({ scrollTop: 100, readingState: "reading" });
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
    await frames();
    expect(f.detach()).toEqual({ scrollTop: 300, readingState: "reading" });
  });

  it("never writes the transcript's --scrollbar-w", async () => {
    box.style.scrollbarGutter = "stable";
    document.documentElement.style.setProperty("--scrollbar-w", "sentinel");
    const f = scroll.createFollowScroller(view, box, resume);
    f.attach({ el: view, scrollTop: 0, readingState: "following" });
    view.appendChild(block(1000));
    box.style.width = "320px";
    await frames();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("sentinel");
    document.documentElement.style.removeProperty("--scrollbar-w");
  });
});
