// Real-layout scenes for the chat scroller's suites: a 400px scrollport over `#messages`, the reader's inputs as a
// device delivers them, the platform's motion as the engine's own scrolls, and a record of the owner's writes only.
// A suite mocks `./dom.js` with `domMock` and `./skeleton.js` itself, then passes the loaded `scroll.js` here.
import { expect, vi } from "vitest";
import { FRAME_BUDGET_MS } from "./frame-budget.js";
import { framesDrained } from "./frame-queue.js";

type ScrollModule = typeof import("../scroll.js");

/** The `$` proxy every scroll suite mocks `dom.js` with: an element per id, created on first read. */
export function domProxy(): object {
  return new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        const id = String(prop);
        let e = document.getElementById(id);
        if (e === null) {
          e = document.createElement(id === "scrollBottom" ? "button" : "div");
          e.id = id;
          if (id === "scrollBottom") {
            e.appendChild(document.createElement("span"));
          }
          document.body.appendChild(e);
        }
        return e;
      },
    },
  );
}

/** One owner write: where it asked to go, and how far it moved the scroller. */
interface Write {
  readonly top: number | null;
  readonly delta: number;
}

const nextFrame = window.requestAnimationFrame.bind(window);
/** The platform's own scroll methods, typed to their options overload. */
const protoScrollTo = HTMLElement.prototype.scrollTo as (
  this: HTMLElement,
  o: ScrollToOptions,
) => void;
const protoScrollBy = HTMLElement.prototype.scrollBy as (
  this: HTMLElement,
  o: ScrollToOptions,
) => void;

export async function taskHop(): Promise<void> {
  await new Promise<void>((r) => {
    const hop = new MessageChannel();
    hop.port1.onmessage = (): void => {
      hop.port1.close();
      r();
    };
    hop.port2.postMessage(null);
  });
}

/** Each frame resumes on a fresh task, outside the frame's callbacks and deliveries. */
export async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => nextFrame(r));
    await taskHop();
  }
}

/** The platform moving the scroller: the prototype's own `scrollTo`, which `recordWrites` does not see. */
export function platformScroll(
  wrap: HTMLElement,
  top: number,
  behavior: ScrollBehavior = "instant",
): void {
  protoScrollTo.call(wrap, { top, behavior });
}

/** An ordinary event carrying `touches`, the one field the owner reads. */
export function touchEvent(
  type: "touchstart" | "touchmove" | "touchend" | "touchcancel",
  target: HTMLElement,
  clientY?: number,
  clientX = 10,
): Event {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "touches", {
    value: clientY === undefined ? [] : [{ identifier: 1, target, clientX, clientY }],
  });
  return ev;
}

export function wheel(target: EventTarget, deltaY: number, init: WheelEventInit = {}): void {
  target.dispatchEvent(new WheelEvent("wheel", { deltaY, deltaMode: 1, bubbles: true, ...init }));
}

/** Resolve on the scroller's next `scrollend`. */
export async function scrollEnds(wrap: HTMLElement): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const bound = setTimeout(() => {
      reject(
        new Error(
          `no scrollend within ${String(FRAME_BUDGET_MS)}ms (scrollTop ${String(wrap.scrollTop)})`,
        ),
      );
    }, FRAME_BUDGET_MS);
    wrap.addEventListener(
      "scrollend",
      () => {
        clearTimeout(bound);
        resolve();
      },
      { once: true },
    );
  });
  await taskHop();
}

/** Every position the scroller's scroll events reported, and whether a `scrollend` came before the last one. */
export function scrollLog(wrap: HTMLElement): { tops: number[]; ends: number[]; off: () => void } {
  const tops: number[] = [];
  const ends: number[] = [];
  const onScroll = (): void => {
    tops.push(wrap.scrollTop);
  };
  const onEnd = (): void => {
    ends.push(tops.length);
  };
  wrap.addEventListener("scroll", onScroll);
  wrap.addEventListener("scrollend", onEnd);
  return {
    tops,
    ends,
    off: () => {
      wrap.removeEventListener("scroll", onScroll);
      wrap.removeEventListener("scrollend", onEnd);
    },
  };
}

export function scene(scroll: ScrollModule) {
  const messagesEl = document.getElementById("messages")!;
  const scrollBtn = document.getElementById("scrollBottom")!;

  let domChanges = 0;
  const domWatch = new MutationObserver((records) => {
    domChanges += records.length;
  });
  domWatch.observe(document.documentElement, {
    subtree: true,
    childList: true,
    attributes: true,
    characterData: true,
  });

  const now = (): string => {
    domChanges += domWatch.takeRecords().length;
    const wrap = scroll.getScrollEl();
    return [
      wrap.scrollTop,
      wrap.scrollHeight,
      wrap.clientHeight,
      scroll.readingState(),
      scrollBtn.classList.contains("hidden"),
      domChanges,
    ].join("|");
  };

  /** Frames until the scroller, the reading state and the DOM held still for two frames. */
  async function stillness(): Promise<void> {
    const start = performance.now();
    let seen = now();
    let still = 0;
    while (still < 2) {
      if (performance.now() - start > FRAME_BUDGET_MS) {
        throw new Error(`the scene was still moving after ${String(FRAME_BUDGET_MS)}ms: ${seen}`);
      }
      await frames(1);
      const next = now();
      still = next === seen ? still + 1 : 0;
      seen = next;
    }
  }

  /** Let `ms` of the owner's clock pass, with every frame the scene needs either side. */
  async function land(ms = 120): Promise<void> {
    await stillness();
    vi.advanceTimersByTime(ms);
    await stillness();
  }

  /** A real 400px scrollport over an empty transcript, the shipped `scrollbar-gutter`, no owner write recorded. */
  function realScroller(): HTMLElement {
    const wrap = scroll.getScrollEl();
    for (const key of ["scrollHeight", "clientHeight", "scrollTop", "scrollTo", "scrollBy"]) {
      Reflect.deleteProperty(wrap, key);
    }
    // `overflow-anchor: none` is the owner's own inline declaration, which this `cssText` replaces.
    wrap.style.cssText =
      "height:400px;overflow-y:auto;position:relative;scrollbar-gutter:stable;overflow-anchor:none;";
    if (messagesEl.parentElement !== wrap) {
      wrap.appendChild(messagesEl);
    }
    messagesEl.replaceChildren();
    platformScroll(wrap, 0);
    return wrap;
  }

  function block(px: number, className = ""): HTMLElement {
    const d = document.createElement("div");
    if (className !== "") {
      d.className = className;
    }
    d.style.cssText = `height:${String(px)}px;flex-shrink:0;`;
    messagesEl.appendChild(d);
    return d;
  }

  /** Every write the owner makes through the scroller's own `scrollTo` or `scrollBy`, still performing it. `delta` is
   *  the true move, read from the content's rect: an engine reports `scrollTop` on whole pixels. */
  function recordWrites(wrap: HTMLElement): Write[] {
    const writes: Write[] = [];
    const contentTop = (): number => messagesEl.getBoundingClientRect().top;
    wrap.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
      const before = contentTop();
      if (typeof arg === "number") {
        protoScrollTo.call(wrap, { left: arg, top: y ?? 0 });
        writes.push({ top: y ?? 0, delta: before - contentTop() });
        return;
      }
      protoScrollTo.call(wrap, arg ?? {});
      writes.push({ top: arg?.top ?? null, delta: before - contentTop() });
    }) as typeof wrap.scrollTo;
    wrap.scrollBy = ((arg?: number | ScrollToOptions, y?: number): void => {
      const before = contentTop();
      if (typeof arg === "number") {
        protoScrollBy.call(wrap, { left: arg, top: y ?? 0 });
      } else {
        protoScrollBy.call(wrap, arg ?? {});
      }
      writes.push({ top: null, delta: before - contentTop() });
    }) as typeof wrap.scrollBy;
    return writes;
  }

  /** The facts a landing names: where the scroller is, the state, and whether the resume control is down. */
  function landing(wrap: HTMLElement): { scrollTop: number; state: string; hidden: boolean } {
    return {
      scrollTop: wrap.scrollTop,
      state: scroll.readingState(),
      hidden: scrollBtn.classList.contains("hidden"),
    };
  }

  /** Close the reader's gesture before an "after the gesture" assertion: an owner write's own `scrollend` is ignored
   *  by design, so a gesture whose motion a hold stopped closes only at the idle bound. */
  async function closeGesture(): Promise<void> {
    vi.advanceTimersByTime(250 + 16);
    await frames(16);
    expect(scroll.readerGestureOpen(), "the premise: the gesture has closed").toBe(false);
  }

  /** A scene's starting point: no anchor provider, an empty real scroller, Following at the top. */
  async function reset(): Promise<HTMLElement> {
    scroll.setAnchorProvider(null);
    document.getElementById("load-more-skeleton")?.remove();
    const wrap = realScroller();
    await framesDrained("the previous scene's frames");
    scroll.resetScrollState();
    await land();
    return wrap;
  }

  /** Where `el`'s top sits relative to the scroller's top edge, on screen. */
  function screenTop(wrap: HTMLElement, el: Element): number {
    return el.getBoundingClientRect().top - wrap.getBoundingClientRect().top;
  }

  return {
    messagesEl,
    scrollBtn,
    stillness,
    land,
    realScroller,
    block,
    recordWrites,
    landing,
    closeGesture,
    reset,
    screenTop,
  };
}
