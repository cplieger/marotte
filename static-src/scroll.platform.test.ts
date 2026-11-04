// The engine behaviour the chat scroller's owner (`scroll-controller.ts`) is built on, pinned so an engine that
// changes it fails here rather than as a wiggle. Runs in every engine of the `browser-engines` project too.
import { describe, expect, it } from "vitest";
import { taskHop } from "./__test-helpers__/scroll-scene.js";

/** Resumes on a task after the next frame, where input arrives: never between a frame's callbacks and its layout. */
async function frame(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(r));
  await taskHop();
}
async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await frame();
  }
}

function scrollerOver(contentPx: number): { s: HTMLElement; c: HTMLElement; done: () => void } {
  const s = document.createElement("div");
  s.style.cssText = "height:300px;overflow-y:auto;overflow-anchor:none;";
  const c = document.createElement("div");
  c.style.cssText = `height:${String(contentPx)}px;`;
  s.appendChild(c);
  document.body.appendChild(s);
  return { s, c, done: () => s.remove() };
}

function events(s: HTMLElement): { log: string[]; off: () => void } {
  const log: string[] = [];
  const onScroll = (): void => {
    log.push(`scroll ${String(Math.round(s.scrollTop))}`);
  };
  const onEnd = (): void => {
    log.push("end");
  };
  s.addEventListener("scroll", onScroll);
  s.addEventListener("scrollend", onEnd);
  return {
    log,
    off: () => {
      s.removeEventListener("scroll", onScroll);
      s.removeEventListener("scrollend", onEnd);
    },
  };
}

describe("the engine the scroll owner relies on", () => {
  it("dispatches a task's scroll before the same frame's resize delivery", async () => {
    // https://html.spec.whatwg.org/multipage/webappapis.html#update-the-rendering runs the scroll steps before
    // resize observers, so a reader's move reaches the owner before any correction reads the layout.
    const { s, c, done } = scrollerOver(3000);
    const g = document.createElement("div");
    g.style.cssText = "height:40px;";
    c.prepend(g);
    s.scrollTop = 500;
    const order: string[] = [];
    s.addEventListener("scroll", () => order.push("scroll"));
    const ro = new ResizeObserver(() => order.push("resize"));
    ro.observe(g);
    await frames(2);
    order.length = 0;

    s.scrollTop = 440;
    g.style.height = "100px";
    await frames(2);
    ro.disconnect();
    done();

    expect(order).toEqual(["scroll", "resize"]);
  });

  it("fires one scroll and one scrollend for an instant write, and nothing for a write to where it is", async () => {
    // https://drafts.csswg.org/cssom-view/#scrolling-events: an instant scroll that moved fires `scrollend` once.
    const { s, done } = scrollerOver(4000);
    s.scrollTop = 500;
    await frames(2);
    const ev = events(s);

    s.scrollTo({ top: 530, behavior: "instant" });
    await frames(3);
    const moved = [...ev.log];
    ev.log.length = 0;
    s.scrollTo({ top: 530, behavior: "instant" });
    await frames(3);
    const still = [...ev.log];
    ev.off();
    done();

    expect({ moved, still }).toEqual({ moved: ["scroll 530", "end"], still: [] });
  });

  it("animates a smooth write through several positions with one scrollend at its end", async () => {
    const { s, done } = scrollerOver(4000);
    s.scrollTop = 2000;
    await frames(4);
    const ev = events(s);

    s.scrollTo({ top: 1500, behavior: "smooth" });
    const start = performance.now();
    while (ev.log.at(-1) !== "end" && performance.now() - start < 3000) {
      await frame();
    }
    ev.off();
    done();

    const tops = ev.log.filter((e) => e !== "end");
    expect(tops.length, `positions: ${tops.join(",")}`).toBeGreaterThanOrEqual(3);
    expect(ev.log.filter((e) => e === "end")).toHaveLength(1);
    expect(ev.log.at(-1)).toBe("end");
    expect(tops.at(-1)).toBe("scroll 1500");
  });

  it("lands an instant write on a whole CSS pixel", async () => {
    // Why the owner rounds its targets: a fractional target is never the resting position, at any ratio.
    const { s, done } = scrollerOver(4000);
    s.scrollTo({ top: 1300.6, behavior: "instant" });
    await frames(1);
    const top = s.scrollTop;
    done();

    expect(Math.abs(top - Math.round(top))).toBeLessThan(0.01);
  });
});
