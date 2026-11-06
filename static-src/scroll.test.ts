// The follow model: reading state, the live-edge pins and the reader's own gestures.
import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach, vi } from "vitest";

// Read for the premise test below: the regression cases only reproduce production while `.msg-row`
// really is a containment box, so the declaration is asserted out of the shipped stylesheet rather
// than copied into a fixture.
import messagesCss from "./css/13-messages.css?raw";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { until } from "./__test-helpers__/until.js";
import { framesDrained } from "./__test-helpers__/frame-queue.js";

// The module builds its singleton against $.messages / $.messagesWrap at import, and reads
// $.scrollBottom in init.
vi.mock("./dom.js", () => ({
  $: new Proxy(
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
  ),
}));
// Zero height unless a scene sets one: the shipped skeleton's own height is what most pagination scenes do not measure.
const skeleton = vi.hoisted(() => ({ px: 0 }));
vi.mock("./skeleton.js", () => ({
  loadMoreSkeleton: () => {
    const d = document.createElement("div");
    d.style.height = `${String(skeleton.px)}px`;
    return d;
  },
}));

const scroll = await import("./scroll.js");

/** The scroller is faked with writable properties. That is the right level here: the helper's
 *  contract is arithmetic over three numbers, not real layout. */
function fakeScroller(init: { scrollHeight: number; clientHeight: number; scrollTop: number }) {
  const el = scroll.getScrollEl();
  const state = { ...init };
  for (const key of ["scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(el, key, {
      configurable: true,
      get: () => state[key],
    });
  }
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => state.scrollTop,
    set: (v: number) => {
      state.scrollTop = v;
    },
  });
  // `scrollTo` has to be shadowed alongside the metrics, or the faked scrollTop is bypassed
  // entirely: production reaches the live edge through `scrollEl.scrollTo({top, behavior})`, and in
  // a real browser that is the platform's own method writing a real scroll position — which stays 0
  // on an element with no overflow, so every pin assertion read 0.
  el.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
    const top = typeof arg === "number" ? y : arg?.top;
    if (top !== undefined) {
      state.scrollTop = top;
    }
  }) as typeof el.scrollTo;
  return state;
}

// The gesture tracker's idle bound reads `Date.now()`, so a faked Date
// lets `land` step past one without sleeping it out. Only Date: the scenes' frames, observers,
// timers and smooth flights stay real.
beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
});
afterAll(() => {
  vi.useRealTimers();
});

beforeEach(() => {
  scroll.resetScrollState();
});

describe("deferWhileReading", () => {
  it("applies immediately while Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    scroll.scrollToBottom();
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  // Content must never disappear from above the reader through no action of their own, which is
  // exactly what a turn folding mid-read would do.
  it("queues while Reading and applies on the return to Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.pressHold();
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    expect(fn).not.toHaveBeenCalled();

    scroll.scrollToBottom();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("preserves arrival order", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.pressHold();
    const order: number[] = [];
    scroll.deferWhileReading(() => order.push(1));
    scroll.deferWhileReading(() => order.push(2));
    scroll.deferWhileReading(() => order.push(3));
    scroll.scrollToBottom();
    expect(order).toEqual([1, 2, 3]);
  });

  it("does not replay a flushed queue on the next return", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.pressHold();
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    scroll.scrollToBottom();
    scroll.pressHold();
    scroll.scrollToBottom();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("drops the queue on a chat switch", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.pressHold();
    const fn = vi.fn();
    scroll.deferWhileReading(fn);
    scroll.resetScrollState();
    scroll.scrollToBottom();
    expect(fn).not.toHaveBeenCalled();
  });
});

// The reset is what a chat switch runs, so anything it leaves behind belongs to the previous chat.
describe("resetScrollState", () => {
  beforeEach(resetBetween);

  it("hides the resume control the reader left behind", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    expect(scrollBtn.classList.contains("hidden")).toBe(false);

    scroll.resetScrollState();
    expect(scroll.readingState()).toBe("following");
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("leaves no control that a return to the live edge cannot clear", () => {
    // The field-assignment version wrote Following into the field and left the control on screen,
    // which setState's unchanged-state guard then made permanent: scrolling back to the bottom is a
    // Following-to-Following no-op, so the reader could not dismiss it by any gesture.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    scroll.resetScrollState();

    s.scrollTop = 1500;
    readerScroll();
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });

  it("stops asking for the previous chat's older pages", () => {
    // The fetcher is the previous chat's closure; left wired, the history edge would page the wrong conversation.
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);

    scroll.resetScrollState();
    scroll.noteHistoryEdge(true);

    expect(load).not.toHaveBeenCalled();
  });
});

// A jump is the one way into Reading that can have nowhere to go. Pinned because the failure was
// silent and sticky: the resume control appeared over a transcript that had not moved, and since
// nothing scrolled, no scroll event arrived to put the state back.
describe("jumpTo", () => {
  // The helper below appends to the shared transcript, because `scrollFrameRect`
  // answers null for a disconnected element, so a target that is not in the DOM takes the
  // no-landing branch instead of the arithmetic.
  beforeEach(resetBetween);

  /** A target `top` px from the scroller's own top edge, `height` tall. */
  function target(top: number, height: number): HTMLElement {
    const e = document.createElement("div");
    const rectAt = (): DOMRect => {
      const wrapTop = scroll.getScrollEl().getBoundingClientRect().top;
      return new DOMRect(0, wrapTop + top, 100, height);
    };
    e.getBoundingClientRect = rectAt;
    e.getClientRects = (() => [rectAt()] as unknown as DOMRectList) as typeof e.getClientRects;
    e.scrollIntoView = () => {
      /* no layout is driven here; the state decision is what is under test */
    };
    messagesEl.appendChild(e);
    return e;
  }

  it("stays Following when the transcript cannot scroll", () => {
    fakeScroller({ scrollHeight: 800, clientHeight: 800, scrollTop: 0 });
    scroll.jumpTo(target(100, 400));
    expect(scroll.readingState()).toBe("following");
  });

  it("parks the reader when the jump leaves the live edge", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    scroll.jumpTo(target(-3000, 400));
    expect(scroll.readingState()).toBe("reading");
    expect(document.getElementById("scrollBottom")?.classList.contains("hidden")).toBe(false);
  });

  // The no-landing branch, on the same geometry as the case above — which parks the reader — so
  // this passes because the target has no box and not because the jump lands at the live edge.
  it("keeps the reader Following when the jump target has left the DOM", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    const gone = target(-3000, 400);
    gone.remove();
    scroll.jumpTo(gone);
    expect(scroll.readingState()).toBe("following");
  });

  it("returns to Following, and hides the resume control, when the jump lands at the bottom", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    scroll.jumpTo(target(-3000, 400));
    expect(scroll.readingState()).toBe("reading");

    // The last turn: its top is past the scroller's own maximum, so the landing clamps to the
    // bottom and there is nothing to resume from.
    scroll.jumpTo(target(3100, 400));
    expect(scroll.readingState()).toBe("following");
    expect(document.getElementById("scrollBottom")?.classList.contains("hidden")).toBe(true);
  });

  // find-in-chat centres its hit, which lands the reader half a viewport higher than a `start` jump
  // would. Measuring that as `start` would call a centred hit near the bottom "at the live edge"
  // and unfreeze a reader who is not.
  it("accounts for the requested block when deciding", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 0 });
    const nearBottom = target(3300, 20);
    scroll.jumpTo(nearBottom, { block: "start" });
    expect(scroll.readingState()).toBe("following");

    scroll.resetScrollState();
    // The first jump's follow write moved the fake scroller, and this target is placed on screen.
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 0 });
    scroll.jumpTo(nearBottom, { block: "center" });
    expect(scroll.readingState()).toBe("reading");
  });

  // Inside skipped `content-visibility: auto` content a target can report no box until the jump's own
  // `scrollIntoView` lays its row out.
  it("parks the reader on a far target that takes its box only when the jump lays it out", () => {
    fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 3200 });
    const late = target(-3000, 400);
    const laidOut = late.getClientRects.bind(late);
    late.getClientRects = (() => [] as unknown as DOMRectList) as typeof late.getClientRects;
    late.scrollIntoView = () => {
      late.getClientRects = laidOut;
    };
    scroll.jumpTo(late);
    expect(scroll.readingState()).toBe("reading");
  });

  // Laying a skipped row out grows it, and the owner's hold moves the scroller by that growth before a flight's first
  // frame, so a scroller that moved is not by that alone a landing.
  it("treats a jump whose scroller moved short of the landing as a flight in progress", () => {
    const state = fakeScroller({ scrollHeight: 4000, clientHeight: 800, scrollTop: 2400 });
    const far = target(-2000, 400);
    far.scrollIntoView = () => {
      state.scrollTop += 600;
    };
    scroll.jumpTo(far);
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    // A frame of the flight: inside its flight, the owner's own animation and not the reader.
    state.scrollTop -= 300;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    off();
    expect(seen).not.toHaveBeenCalled();
  });
});

describe("readingState", () => {
  it("starts Following", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 500 });
    expect(scroll.readingState()).toBe("following");
  });
  it("names the state rather than exposing a boolean", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    scroll.pressHold();
    expect(scroll.readingState()).toBe("reading");
    scroll.scrollToBottom();
    expect(scroll.readingState()).toBe("following");
  });

  it("notifies listeners on a transition, and only on a transition", () => {
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 200 });
    const seen: string[] = [];
    scroll.onReadingStateChange((s) => seen.push(s));
    scroll.pressHold();
    scroll.pressHold();
    scroll.scrollToBottom();
    expect(seen).toEqual(["reading", "following"]);
  });
});

// The rest of the module: the event paths `init()` wires up, the streaming anchor, and pagination.

const messagesEl = document.getElementById("messages")!;
const scrollBtn = document.getElementById("scrollBottom")!;

/** The reader's own move to where the fixture has put the scroller (`readerArrivedAt`). */
function readerScroll(): void {
  readerArrivedAt(scroll.getScrollEl());
}

/** The reader's own move to where the fixture has put `el`: from 50px the other side (below it, or above it at the
 *  end), the input that opens the gesture, then the `scroll` event. The owner reads the state from where the scroller
 *  went. */
function readerArrivedAt(el: HTMLElement): void {
  const to = el.scrollTop;
  const max = el.scrollHeight - el.clientHeight;
  const atEnd = to >= max - 1;
  el.scrollTop = atEnd ? Math.max(0, to - 50) : Math.min(max, to + 50);
  el.dispatchEvent(new WheelEvent("wheel", { deltaY: atEnd ? 1 : -1 }));
  el.scrollTop = to;
  el.dispatchEvent(new Event("scroll"));
  el.dispatchEvent(new Event("scrollend"));
  // The history-edge observer's report, which fake geometry cannot produce: the edge counts as reached within
  // HISTORY_LEAD_PX of the top.
  scroll.noteHistoryEdge(el.scrollTop < HISTORY_LEAD_PX);
}

const HISTORY_LEAD_PX = 100;

/** The legacy touch factories, absent from the DOM lib because they are not standard.
 *  `createTouchList` is typed as the array `TouchEventInit` declares, which is the init that
 *  consumes it — an engine answering these refuses a real array in its place. */
interface LegacyTouchDoc {
  createTouch: (
    view: Window,
    target: EventTarget,
    identifier: number,
    pageX: number,
    pageY: number,
    screenX: number,
    screenY: number,
  ) => Touch;
  createTouchList: (...touches: Touch[]) => Touch[];
}

/** A touch event built through whichever construction this engine allows: the standard
 *  constructors, the legacy factories, or — where no touch API is compiled in at all — an
 *  ordinary event carrying `touches`, the one field these listeners read. */
function touchEvent(
  type: "touchstart" | "touchmove" | "touchend",
  target: HTMLElement,
  clientY?: number,
): Event {
  const engine = globalThis as {
    readonly TouchEvent?: typeof TouchEvent;
    readonly Touch?: typeof Touch;
  };
  const doc = document as Document & Partial<LegacyTouchDoc>;
  const init = { bubbles: true, cancelable: true };
  const ys = clientY === undefined ? [] : [clientY];

  if (
    engine.TouchEvent !== undefined &&
    doc.createTouch !== undefined &&
    doc.createTouchList !== undefined
  ) {
    const make = doc.createTouch.bind(doc);
    const touches = doc.createTouchList(...ys.map((y) => make(window, target, 1, 10, y, 10, y)));
    return new engine.TouchEvent(type, { ...init, touches });
  }
  if (engine.TouchEvent !== undefined && engine.Touch !== undefined) {
    const Point = engine.Touch;
    const touches = ys.map((y) => new Point({ identifier: 1, target, clientX: 10, clientY: y }));
    return new engine.TouchEvent(type, { ...init, touches });
  }
  const ev = new Event(type, init);
  Object.defineProperty(ev, "touches", {
    value: ys.map((y) => ({ identifier: 1, target, clientX: 10, clientY: y })),
  });
  return ev;
}

/** Scripted work standing in for a fetch that resolves on a later task, counted while pending:
 *  timers and frames run in no fixed order, so a frame count cannot stand for its landing. */
let fixtureTasks = 0;
function fixtureTask(fn: () => void): void {
  fixtureTasks++;
  setTimeout(() => {
    fixtureTasks--;
    fn();
  }, 0);
}

/** A history with one older page: the first ask lands `page`, every later one lands nothing. A fake
 *  scroller's history edge never leaves the lead, so a page that lands rows is re-asked for forever;
 *  the empty page is what ends that chain before the case returns. */
function onePage(page: () => void): () => void {
  let asks = 0;
  return () => {
    asks++;
    const first = asks === 1;
    fixtureTask(() => {
      if (first) {
        page();
      }
      document.getElementById("load-more-skeleton")?.remove();
    });
  };
}

/** Two frames cover a correction's frame and the settle's re-assert frame: that frame schedule is the owner's
 *  contract, and what a positive pins. An absence reads after `drained()`. */
async function settle(): Promise<void> {
  await frames(2);
}

/** Until no frame callback is left queued, so a write the controller still owes is seen. */
async function drained(): Promise<void> {
  await framesDrained("the controller's queued frames");
}

/** Captured at load: a case spies on `requestAnimationFrame` to count the controller's own frames,
 *  and the waits below must not show up in that count. */
const nextFrame = window.requestAnimationFrame.bind(window);

/** A message task, because a faked `setTimeout` never fires on its own. */
async function taskHop(): Promise<void> {
  await new Promise<void>((r) => {
    const hop = new MessageChannel();
    hop.port1.onmessage = (): void => {
      hop.port1.close();
      r();
    };
    hop.port2.postMessage(null);
  });
}

/** Each frame resumes on a fresh task rather than inside the frame callback: code resumed there
 *  runs before that frame's ResizeObserver delivery, where no reader input ever lands, and the
 *  controller reads a scroll it has no event for yet as drift. */
async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => nextFrame(r));
    await taskHop();
  }
}

/** Every DOM change anywhere in the page, so a wait can tell an observer's late delivery (a
 *  `data-near` mark, a `--scrollbar-w` write) from a scene at rest. */
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

/** Consecutive frames with nothing moving before a scene counts as settled: an observer's
 *  delivery lands one frame after the layout it answers, so one still frame can sit between a
 *  write and its consequence. */
const STILL_FRAMES = 2;

function sceneNow(): string {
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
}

/** Frames until the scroller, the reading state and the DOM have held still for STILL_FRAMES. */
async function stillness(): Promise<void> {
  const start = performance.now();
  let seen = sceneNow();
  let still = 0;
  while (still < STILL_FRAMES) {
    if (performance.now() - start > FRAME_BUDGET_MS) {
      throw new Error(`the scene was still moving after ${String(FRAME_BUDGET_MS)}ms: ${seen}`);
    }
    await frames(1);
    const now = sceneNow();
    still = now === seen ? still + 1 : 0;
    seen = now;
  }
}

/** The singleton outlives every test and only some of its state is in resetScrollState's remit:
 *  the anchor provider, the transcript's children and the pagination furniture leak into the
 *  next test otherwise. Settling last keeps a frame queued by this cleanup out of the test that
 *  follows. */
async function resetBetween(): Promise<void> {
  if (fixtureTasks !== 0) {
    const queued = fixtureTasks;
    // Stop the loader before draining; otherwise an always-in-lead fixture immediately queues
    // another page and the drain never completes.
    scroll.setLoadMore(null, false);
    await until(() => fixtureTasks === 0, "the leaked fixture pages to drain");
    throw new Error(
      `${String(queued)} fixture page task(s) from the previous case were still queued: they would land in this one`,
    );
  }
  scroll.setAnchorProvider(null);
  messagesEl.replaceChildren();
  document.getElementById("load-more-skeleton")?.remove();
  scrollBtn.classList.remove("hidden");
  await drained();
}

describe("the scroll listener's reading model", () => {
  beforeEach(resetBetween);

  it("parks the reader in Reading when they scroll away from the bottom", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    expect(scroll.readingState()).toBe("reading");
  });

  // The defect this file's real-layout section reproduces, in its cheapest form: the SAME position,
  // arriving with no input behind it, is the platform's own clamp and may not park anyone.
  it("ignores a scroll away from the bottom that no reader input produced", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("returns to Following when the reader's scroll reaches the bottom again and ends there", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    readerScroll();
    s.scrollTop = 1500;
    readerScroll();
    scroll.getScrollEl().dispatchEvent(new Event("scrollend"));
    expect(scroll.readingState()).toBe("following");
  });
});

// WHICH INPUTS COUNT AS THE READER. Intent is decided from input rather than position, so every
// device that scrolls this box needs a listener of its own, and a device left out parks the reader
// by their own gesture.
describe("the reader's input surfaces", () => {
  beforeEach(resetBetween);

  /** A touch at one vertical position. `touchmove` carries a position rather than a delta, so a
   *  drag is two of these and the controller keeps the previous one. */
  function touchAt(el: HTMLElement, type: "touchstart" | "touchmove", clientY: number): void {
    el.dispatchEvent(touchEvent(type, el, clientY));
  }

  /** Grow the transcript by a chunk and report where the scroller ended up. 1500 is the write
   *  suppressed, 2500 is the pin running. */
  async function chunkLands(s: { scrollHeight: number; scrollTop: number }): Promise<number> {
    s.scrollHeight = 3000;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    const at = s.scrollTop;
    await drained();
    expect(s.scrollTop, "a write after the pin's frames").toBe(at);
    return at;
  }

  it("takes no licence from a pointer moving with no thumb held", async () => {
    // Without the held-thumb guard every mouse movement over the page refreshes the quiet period,
    // and the auto-scroll is suppressed for as long as the reader's hand is on the mouse.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: 10, clientY: 100 }));
    expect(await chunkLands(s)).toBe(2500);
  });

  it("parks the reader on a key that scrolls UP", () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "PageUp" }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following on a key that scrolls DOWN", () => {
    // Same displacement, opposite aim: a reader heading for the live edge is not parked by whatever
    // the layout does to them on the way.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown" }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("reads Shift+Space as a scroll UP", () => {
    // The one direction no key spelling distinguishes: Space pages down, Shift+Space pages up, and
    // both arrive as `key: " "`.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: " ", shiftKey: true }));
    s.scrollTop = 0;
    scroll.getScrollEl().dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the scroller alone for a key that does not scroll", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "k" }));
    expect(await chunkLands(s)).toBe(2500);
  });

  it("leaves the scroller alone for a scrolling key typed into a field", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const field = document.createElement("input");
    document.body.appendChild(field);
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown", bubbles: true }));
    field.remove();
    expect(await chunkLands(s)).toBe(2500);
  });

  it("parks the reader when a touch drag aims UP", () => {
    // A finger moving DOWN the screen scrolls the content UP, so the sign inverts — and the aim is
    // what parks them, which is why the momentum that follows needs no direction of its own.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    touchAt(el, "touchstart", 100);
    touchAt(el, "touchmove", 260);
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following when a touch drag aims DOWN", () => {
    // The same displacement, aimed the other way: everything below the reader is the layout's
    // business, and a fling toward the live edge must not park them.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const el = scroll.getScrollEl();
    touchAt(el, "touchstart", 260);
    touchAt(el, "touchmove", 100);
    s.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("builds a touch event carrying the position the listeners read", () => {
    const el = scroll.getScrollEl();
    const { TouchEvent: Ctor } = globalThis as { readonly TouchEvent?: typeof TouchEvent };
    const ev = touchEvent("touchmove", el, 42) as Event & {
      readonly touches: { readonly clientY: number }[];
    };
    expect({
      type: ev.type,
      count: ev.touches.length,
      y: ev.touches[0]?.clientY,
      real: Ctor === undefined || ev instanceof Ctor,
    }).toEqual({ type: "touchmove", count: 1, y: 42, real: true });
  });
});

describe("the End key", () => {
  beforeEach(resetBetween);

  function pressEnd(target: HTMLElement, init: KeyboardEventInit = {}): void {
    target.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true, ...init }));
  }

  // The live edge is `scrollHeight - clientHeight` — the largest scrollTop the box actually has.
  // Every landing is clamped to it, because the marker that tells the scroll listener a scroll was
  // the controller's own has to be the position the browser will really reach.
  it("resumes Following and pins to the live edge", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    pressEnd(document.body);
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("leaves the reader alone when they are already Following", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    scroll.scrollToBottom();
    pressEnd(document.body);
    await drained();
    expect(s.scrollTop).toBe(1500);
  });

  it("ignores End while the caret is in a text field", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const input = document.createElement("input");
    document.body.appendChild(input);
    scroll.pressHold();
    pressEnd(input);
    input.remove();
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End while the caret is in a contenteditable", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const box = document.createElement("div");
    box.contentEditable = "true";
    document.body.appendChild(box);
    scroll.pressHold();
    pressEnd(box);
    box.remove();
    expect(scroll.readingState()).toBe("reading");
  });

  // The tag test is anchored at both ends: a custom element whose name merely contains a field tag
  // is not a field.
  it("treats a custom element named around a field tag as ordinary", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const before = document.createElement("my-input");
    const after = document.createElement("input-box");
    document.body.append(before, after);
    scroll.pressHold();
    pressEnd(before);
    const resumedForSuffix = scroll.readingState();
    scroll.pressHold();
    pressEnd(after);
    const resumedForPrefix = scroll.readingState();
    before.remove();
    after.remove();
    expect([resumedForSuffix, resumedForPrefix]).toEqual(["following", "following"]);
  });

  it("ignores End with Ctrl held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    pressEnd(document.body, { ctrlKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End with Meta held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    pressEnd(document.body, { metaKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores End with Alt held", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    pressEnd(document.body, { altKey: true });
    expect(scroll.readingState()).toBe("reading");
  });

  it("ignores a key that is not End", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(scroll.readingState()).toBe("reading");
  });
});

describe("the resume control", () => {
  beforeEach(resetBetween);

  it("is shown while Reading and hidden on the return to Following", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    const shownWhileReading = scrollBtn.classList.contains("hidden");
    scroll.scrollToBottom();
    expect([shownWhileReading, scrollBtn.classList.contains("hidden")]).toEqual([false, true]);
  });

  it("returns the reader to the live edge when clicked", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    scrollBtn.click();
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("carries the label the caller sets", () => {
    scroll.setResumeLabel("3 new blocks");
    expect(scrollBtn.querySelector("span")?.textContent).toBe("3 new blocks");
  });
});

describe("scrollToBottom", () => {
  beforeEach(resetBetween);

  it("returns to Following and jumps to the live edge on the next frame", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    scroll.scrollToBottom();
    expect(scroll.readingState()).toBe("following");
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("hides the resume control once the jump has landed", async () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.pressHold();
    scroll.scrollToBottom();
    await settle();
    expect(scrollBtn.classList.contains("hidden")).toBe(true);
  });
});

describe("the streaming auto-scroll", () => {
  beforeEach(resetBetween);

  // The anchored pin is measured under REAL LAYOUT below ("the streaming pin through a
  // containment-bounded row" and "the anchor's coordinate space"): it reads the anchor's rect
  // against the scroller's, and a fake whose `scrollTop` moves no box double-counts the second pin
  // of a burst.

  it("pins to the document bottom when no anchor is offered", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect(s.scrollTop).toBe(1500);
  });

  it("does not move the reader while they are Reading", async () => {
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    scroll.pressHold();
    messagesEl.appendChild(document.createElement("div"));
    await drained();
    expect(s.scrollTop).toBe(100);
  });

  // The resume control is deliberately not asserted here: setState is its only owner, so hidden ⇔
  // Following, and the auto-scroll only ever runs while already Following.

  // One frame per burst of mutations, but the guard has to re-arm or the second chunk of a stream
  // never scrolls.
  it("re-arms the frame guard so the next chunk scrolls too", async () => {
    // Starts on the end: a reader the fake moved with no scroll event would be read as a reader scroll.
    const s = fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    s.scrollHeight = 2500;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    const afterFirst = s.scrollTop;
    s.scrollHeight = 3000;
    messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect([afterFirst, s.scrollTop]).toEqual([2000, 2500]);
  });
});

describe("pagination", () => {
  beforeEach(resetBetween);

  it("fetches older messages when the reader scrolls near the top", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    readerScroll();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("does not fetch while the history edge is out of reach", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 100 });
    scroll.setLoadMore(load, true);
    readerScroll();
    expect(load).not.toHaveBeenCalled();
  });

  it("does not fetch when the server said there is nothing older", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, false);
    readerScroll();
    expect(load).not.toHaveBeenCalled();
  });

  it("does not start a second fetch while one is in flight", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    readerScroll();
    readerScroll();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("shows the skeleton while fetching", () => {
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(() => undefined, true);
    expect(document.getElementById("load-more-skeleton")).toBeNull();
    readerScroll();
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();
  });

  it("asks again after a page lands while the history edge is still in reach", async () => {
    // A page shorter than the lead leaves the edge inside it, where the observer reports no new crossing.
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn(
      onePage(() => {
        messagesEl.prepend(document.createElement("div"));
        s.scrollHeight += 100;
      }),
    );
    scroll.setLoadMore(load, true);
    readerScroll();
    await until(
      () => load.mock.calls.length >= 2 && fixtureTasks === 0,
      "the second page to land",
      {
        detail: () =>
          `${String(load.mock.calls.length)} asks, ${String(fixtureTasks)} pages queued`,
      },
    );
    await drained();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("does not ask again after a page that landed nothing, until the edge is reached anew", async () => {
    // The same failing request would otherwise loop for as long as the edge stays in reach.
    fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn(() => {
      fixtureTask(() => {
        document.getElementById("load-more-skeleton")?.remove();
      });
    });
    scroll.setLoadMore(load, true);
    readerScroll();
    await until(() => fixtureTasks === 0, "the empty page to land");
    await drained();
    expect(load).toHaveBeenCalledTimes(1);

    scroll.noteHistoryEdge(false);
    scroll.noteHistoryEdge(true);
    expect(load).toHaveBeenCalledTimes(2);
    await until(() => fixtureTasks === 0, "the second empty page to land");
  });

  it("clears the in-flight flag once the page has landed", async () => {
    const s = fakeScroller({ scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn(() => {
      fixtureTask(() => {
        document.getElementById("load-more-skeleton")?.remove();
        s.scrollHeight = 1400;
      });
    });
    scroll.setLoadMore(load, true);
    readerScroll();
    await until(() => fixtureTasks === 0, "the page to land");
    await settle();
    s.scrollTop = 0;
    readerScroll();
    expect(load).toHaveBeenCalledTimes(2);
    await until(() => fixtureTasks === 0, "the second page to land");
  });

  // A pagination pass belongs to the chat that started it. Its completion signal does not: the
  // skeleton is a global element id, and the caller drops it the moment ITS fetch resolves,
  // whichever chat the reader has moved to by then.
  it("takes the previous chat's loading skeleton down with it", () => {
    fakeScroller({ scrollHeight: 5000, clientHeight: 500, scrollTop: 50 });
    scroll.setLoadMore(() => undefined, true);
    readerScroll();
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    scroll.resetScrollState();
    expect(document.getElementById("load-more-skeleton")).toBeNull();
  });

  it("does not compensate the next chat for the previous chat's page", async () => {
    const s = fakeScroller({ scrollHeight: 5000, clientHeight: 500, scrollTop: 50 });
    scroll.setLoadMore(() => undefined, true);
    readerScroll();

    // The reader switches chats with the fetch still in flight.
    scroll.resetScrollState();

    // The incoming chat is shorter, and the reader scrolls up into it. Reading is the state that
    // makes the damage stick: the follow correction moves a Following reader at the very next
    // mutation, and leaves this one alone.
    s.scrollHeight = 1000;
    s.scrollTop = 300;
    readerScroll();
    expect(scroll.readingState()).toBe("reading");

    // Now the OUTGOING chat's fetch resolves and drops the skeleton without asking which chat is on
    // screen. Read as this pass completing, it would charge the reader 300 + (1000 - 5000) and throw
    // them to the top of a conversation they had deliberately parked in.
    document.getElementById("load-more-skeleton")?.remove();
    await drained();
    expect(s.scrollTop).toBe(300);
  });
});

describe("fillViewport", () => {
  beforeEach(resetBetween);

  // Folding can starve pagination: a transcript shorter than its viewport fires no scroll event, so
  // nothing ever asks for the next page. The fetch is forced here, which is why a reader parked at
  // the threshold still gets one.
  it("fetches while the transcript does not overflow its viewport", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("does nothing once the transcript overflows", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    expect(load).not.toHaveBeenCalled();
  });

  it("does nothing when there is nothing older", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 400, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, false);
    scroll.fillViewport();
    expect(load).not.toHaveBeenCalled();
  });

  it("does nothing while a fetch is in flight", () => {
    const load = vi.fn();
    fakeScroller({ scrollHeight: 400, clientHeight: 500, scrollTop: 0 });
    scroll.setLoadMore(load, true);
    scroll.fillViewport();
    scroll.fillViewport();
    expect(load).toHaveBeenCalledTimes(1);
  });
});

// REAL LAYOUT. This section is the one that must not use `fakeScroller`, and the reason is
// mechanical rather than stylistic: the fake shadows `scrollTo` with a plain assignment to its own
// `scrollTop` number, which fires no `scroll` event.

function realScroller(): HTMLElement {
  const wrap = scroll.getScrollEl();
  // `Reflect.deleteProperty` rather than `delete`: the keys are computed, and the point is to drop
  // `fakeScroller`'s redefinitions so the platform's own metrics and `scrollTo` come back.
  for (const key of ["scrollHeight", "clientHeight", "scrollTop", "scrollTo"]) {
    Reflect.deleteProperty(wrap, key);
  }
  // `scrollbar-gutter: stable` is a shipped declaration (css/13-messages.css) this section
  // MEASURES rather than decorates: the gutter is what a scrollbar press aims at. `overflow-anchor:
  // none` is the owner's own inline declaration, which this `cssText` replaces.
  wrap.style.cssText =
    "height:400px;overflow-y:auto;position:relative;scrollbar-gutter:stable;overflow-anchor:none;";
  if (messagesEl.parentElement !== wrap) {
    wrap.appendChild(messagesEl);
  }
  messagesEl.replaceChildren();
  wrap.scrollTop = 0;
  return wrap;
}

function block(px: number, className = ""): HTMLElement {
  const d = document.createElement("div");
  if (className !== "") {
    d.className = className;
  }
  d.style.cssText = `height:${String(px)}px;`;
  messagesEl.appendChild(d);
  return d;
}

/** The production shape of a live top-level bubble: a `.msg-row` wrapper holding the
 *  `.message.assistant.streaming` element the block dispatcher registers as the anchor. Returns
 *  the CHILD, which is what `getLiveAnchor` hands over. The row's declarations are `.msg-row`'s
 *  own (css/13-messages.css), inline so the scene needs no stylesheet, and `content-visibility`
 *  is the load-bearing one: it implies `contain: layout paint style`, and `contain: paint` makes
 *  the row a containing block — which is where an offsetParent walk stops. */
function containedRow(px: number): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  row.style.cssText =
    "display:flex;align-items:flex-end;flex-shrink:0;" +
    "content-visibility:auto;contain-intrinsic-size:auto 3rem;";
  const child = document.createElement("div");
  child.className = "message assistant streaming";
  child.style.cssText = `height:${String(px)}px;width:100%;`;
  row.appendChild(child);
  messagesEl.appendChild(row);
  return child;
}

/** Every scrollTop this controller WRITES, in order, still performing the write. A landing
 *  assertion cannot see an INTERMEDIATE position, and the defect below is intermediate by
 *  construction: it writes 0, and the next frame — or the next block seal, which empties the
 *  anchor slot and falls the target through to the document bottom — writes the right number
 *  again. */
function recordWrites(wrap: HTMLElement): number[] {
  const writes: number[] = [];
  const platform = wrap.scrollTo.bind(wrap);
  wrap.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
    if (typeof arg === "number") {
      writes.push(y ?? 0);
      platform(arg, y ?? 0);
      return;
    }
    if (arg?.top !== undefined) {
      writes.push(arg.top);
    }
    platform(arg ?? {});
  }) as typeof wrap.scrollTo;
  return writes;
}

/** Zero is the assertion in every case below: none of these scenes has a legitimate follow target of
 *  0, so a single one is the defect. */
function zeroWrites(writes: readonly number[]): number {
  return writes.filter((top) => top === 0).length;
}

/** Let `ms` of the controller's clock pass, and every frame the scene needs with it: whatever is
 *  queued settles at the current time (a scroll event judged inside the reader's window stays
 *  inside it), the clock jumps, and whatever reads the new time settles too. A smooth flight
 *  moves the scroller until it lands, so a scene acting mid-flight waits on `flightMoves`. */
async function land(ms = 120): Promise<void> {
  await stillness();
  vi.advanceTimersByTime(ms);
  await stillness();
}

/** Arm before starting a smooth flight; resolves a task after its first `scroll` event, the
 *  earliest point the flight is under way. The caller asserts how far it has got. */
async function flightMoves(wrap: HTMLElement): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const bound = setTimeout(() => {
      reject(
        new Error(
          `the flight did not move within ${String(FRAME_BUDGET_MS)}ms (scrollTop ${String(wrap.scrollTop)})`,
        ),
      );
    }, FRAME_BUDGET_MS);
    wrap.addEventListener(
      "scroll",
      () => {
        clearTimeout(bound);
        resolve();
      },
      { once: true },
    );
  });
  await taskHop();
}

/** Resolve on the scroller's `scrollend`: a smooth flight runs on the engine's own clock, which
 *  the controller's faked one does not move. */
async function flightEnds(wrap: HTMLElement): Promise<void> {
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
}

/** Move the scroller AS THE READER: the input event that says whose scroll it is, then the
 *  position they reach. Assigning `scrollTop` alone is the shape the PLATFORM produces (a
 *  `content-visibility` clamp), which the controller deliberately refuses to read as intent. */
function readerScrollTo(wrap: HTMLElement, top: number): void {
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -1 : 1 }));
  wrap.scrollTop = top;
  scroll.noteHistoryEdge(top < HISTORY_LEAD_PX);
}

/** Both real-layout blocks start the same way: no anchor, a real overflowing box, an empty
 *  transcript, and the reader Following at the top. */
async function realLayoutReset(): Promise<void> {
  scroll.setAnchorProvider(null);
  realScroller();
  await land();
  scroll.resetScrollState();
}

/** Park the reader with a real gesture pair: down to the bottom, then back to the top. Two
 *  writes rather than one, so the state passes through Following and the park is the listener's
 *  own verdict rather than a seeded field. */
async function park(wrap: HTMLElement): Promise<void> {
  readerScrollTo(wrap, wrap.scrollHeight - wrap.clientHeight);
  await land();
  readerScrollTo(wrap, 0);
  await land();
}

/** The three facts one failure of a resume has to name: where the click landed, whether the
 *  reader is following again, and whether the control took itself down. */
function landing(wrap: HTMLElement): {
  scrollTop: number;
  state: string;
  hidden: boolean;
} {
  return {
    scrollTop: wrap.scrollTop,
    state: scroll.readingState(),
    hidden: scrollBtn.classList.contains("hidden"),
  };
}

describe("a large tool card below the streaming block", () => {
  beforeEach(realLayoutReset);

  it("keeps Following when the pin lands far from the document bottom", async () => {
    // The reported failure: the agent streams a sentence, a 900px tool card renders below it, and
    // the anchored pin lands 850px above the bottom.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    const wrap = scroll.getScrollEl();
    expect(scroll.readingState()).toBe("following");
    expect(wrap.scrollTop).toBe(1350);
    expect(wrap.scrollHeight - wrap.clientHeight).toBe(2200);
  });

  it("keeps Following when the anchor sits in a collapsed disclosure", async () => {
    // `height: 0` + `overflow: hidden` clips the block without removing it from layout, so it still
    // reports the offsets the pin arithmetic reads — offsets that overflow a document the box
    // contributes no height to.
    block(1500);
    const box = block(0);
    box.style.cssText = "height:0;overflow:hidden;";
    const streaming = document.createElement("div");
    streaming.className = "message assistant streaming";
    streaming.style.cssText = "height:300px;";
    box.appendChild(streaming);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    expect(scroll.readingState()).toBe("following");
  });

  it("still parks the reader in Reading when THEY scroll up", async () => {
    // The controller ignores a scroll no input produced, so the gesture the state exists for has to
    // keep working over the same DOM.
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    readerScrollTo(scroll.getScrollEl(), 200);
    await land();
    expect(scroll.readingState()).toBe("reading");
  });

  it("keeps Following when a pagination pass compensates its own height", async () => {
    // The completion observer gives back the height the older page added above the reader.
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    let pages = 0;
    scroll.setLoadMore(() => {
      // What the real caller does: fetch, prepend the page, drop the skeleton — all after
      // maybeLoadMore has returned and its observer is watching. One page: the history edge asks again
      // for as long as it stays in reach.
      setTimeout(() => {
        if (pages++ === 0) {
          const page = document.createElement("div");
          page.style.cssText = "height:600px;";
          messagesEl.prepend(page);
        }
        document.getElementById("load-more-skeleton")?.remove();
      }, 0);
    }, true);
    await land();

    // The history edge's report asks whatever the reader's position, which is what keeps the anchored
    // pin — the geometry the defect needs — in place.
    scroll.noteHistoryEdge(true);
    await land();

    streaming.style.height = "400px";
    streaming.appendChild(document.createTextNode("the chunk after the page landed"));
    await land();

    // 600 + 1500 + 400 - 400 + 50, short of a 3000 maximum. Parked, the reader stays where the
    // compensation left them at 1950 and the growth passes them by.
    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2150,
      state: "following",
    });
  });

  it("follows every chunk as it arrives", async () => {
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    const wrap = scroll.getScrollEl();
    streaming.style.height = "400px";
    streaming.appendChild(document.createTextNode("chunk one"));
    await land();
    const first = wrap.scrollTop;
    streaming.style.height = "600px";
    streaming.appendChild(document.createTextNode("chunk two"));
    await land();
    // Two distinct pin positions, not two landings on a clamped maximum: the 900px card below keeps
    // both pins short of the document's end.
    expect([first, wrap.scrollTop]).toEqual([1550, 1750]);
  });
});

// A gesture landing a little above the end parks the reader, and content arriving afterwards leaves them where they
// asked to be.
describe("a deliberate upward gesture that lands near the end", () => {
  beforeEach(realLayoutReset);

  /** Following at the live edge of a real overflowing box, put there by the auto-scroll rather
   *  than by any input — so the wheel below is the only gesture in the scene. 3000px of content
   *  in a 400px viewport, so the maximum is 2600. */
  async function atTheLiveEdge(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(3000);
    await land();
    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2600,
      state: "following",
    });
    return wrap;
  }

  /** Wheel up `px` in `notches`, as a device delivers it: the input event that opens the gesture,
   *  then the position it reaches. */
  async function wheelUp(wrap: HTMLElement, px: number, notches: number): Promise<void> {
    const step = px / notches;
    for (let n = 0; n < notches; n++) {
      wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -step }));
      wrap.scrollTo({ top: wrap.scrollTop - step, behavior: "instant" });
      await land();
    }
  }

  it("parks the reader on a 100px gesture and shows the resume control", async () => {
    const wrap = await atTheLiveEdge();

    await wheelUp(wrap, 100, 2);

    // 2500 against a 2600 maximum: `isAtBottom` still answers true here, which is correct for
    // auto-follow and must not decide this.
    expect(landing(wrap)).toEqual({ scrollTop: 2500, state: "reading", hidden: false });
  });

  it("holds that position across content mutations", async () => {
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 100, 2);
    // Past the gesture's idle bound, so the open gesture is not what holds the reader here: the state is.
    await land(400);

    const positions: number[] = [];
    for (let m = 0; m < 6; m++) {
      block(200);
      await land();
      positions.push(wrap.scrollTop);
    }

    // Every mutation grows the document BELOW the reader, so a reader who owns the scroller does
    // not move at all. Parked in Following instead, the pin walks them down to each new maximum and
    // ends pinned at the bottom.
    expect({ positions, ...landing(wrap) }).toEqual({
      positions: [2500, 2500, 2500, 2500, 2500, 2500],
      scrollTop: 2500,
      state: "reading",
      hidden: false,
    });
  });

  it("keeps Following when a shrink clamps the scroller with no input behind it", async () => {
    // A clamp moves the scroller up onto the new end with nothing behind it, and reading THAT as a park would latch
    // the auto-scroll off for a session.
    const wrap = await atTheLiveEdge();
    await land(400);

    (messagesEl.lastElementChild as HTMLElement).style.height = "2900px";
    await land();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2500,
      state: "following",
    });
  });

  it("keeps a parked reader parked when a scroll with no input lands near the end after the gesture ended", async () => {
    // A move with no input is the reader's, and one short of the end is not a return.
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 100, 2);
    expect(scroll.readingState()).toBe("reading");
    await land(400);

    wrap.scrollTo({ top: 2540, behavior: "instant" });
    await land();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2540,
      state: "reading",
    });
  });

  it("keeps Following when an upward gesture lands on the end the content shrank to", async () => {
    // A notch up that coincides with content folding below the reader: the platform clamps scrollTop onto the new
    // end, which reads as the clamp. Both readings leave the reader at the end, so the stream keeps carrying them.
    const wrap = realScroller();
    const content = block(3000);
    await land();
    expect(landing(wrap)).toEqual({ scrollTop: 2600, state: "following", hidden: true });

    wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -60 }));
    wrap.scrollTo({ top: 2540, behavior: "instant" });
    content.style.height = "2800px";
    await land();
    block(40);
    await land(400);
    block(40);
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2480, state: "following", hidden: true });
  });

  it("still promotes a reader who moves down onto the bottom with their upward aim standing", async () => {
    // The shapes the direction test must keep promoting: a press on the scrollbar track below the
    // thumb and Ctrl+End both move DOWN to the end without aiming anything.
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 100, 2);
    expect(scroll.readingState()).toBe("reading");

    wrap.dispatchEvent(touchEvent("touchend", wrap));
    wrap.scrollTo({ top: 2600, behavior: "instant" });
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2600, state: "following", hidden: true });
  });

  it("keeps the reader parked when the owner holds them under content arriving above", async () => {
    // The hold moves scrollTop DOWN by the height that arrived above, which is the owner holding the
    // reader and not the reader coming back.
    const wrap = await atTheLiveEdge();
    await wheelUp(wrap, 60, 1);
    expect(scroll.readingState()).toBe("reading");

    const above = document.createElement("div");
    above.style.cssText = "height:200px;";
    messagesEl.prepend(above);
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2740, state: "reading", hidden: false });
  });
});

// The reported failure: while the reader sat at the bottom of a streaming reply, the transcript
// snapped to the very top of turn 1 for one or more frames and then snapped back, over and over.
describe("the streaming pin through a containment-bounded row", () => {
  beforeEach(realLayoutReset);

  it("still declares content-visibility on .msg-row", () => {
    // A premise, not a behaviour: it stops the three cases below from
    // silently ceasing to reproduce production if the declaration ever moves. Read out of the
    // stylesheet, never copied here.
    const rule = /^\.msg-row\s*\{[^}]*\}/m.exec(messagesCss);
    expect(rule, "the .msg-row rule is missing from css/13-messages.css").not.toBeNull();
    expect(rule?.[0]).toContain("content-visibility");
  });

  it("follows the live block through a containment-bounded row", async () => {
    // 1350 is the number the uncontained sibling case already asserts ("keeps Following when the
    // pin lands far from the document bottom"), and that is the point: wrapping the anchor in its
    // production row must not move the pin.
    const wrap = realScroller();
    block(1500);
    const streaming = containedRow(200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    const writes = recordWrites(wrap);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    expect({
      scrollTop: wrap.scrollTop,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
      max: wrap.scrollHeight - wrap.clientHeight,
    }).toEqual({ scrollTop: 1350, state: "following", zeros: 0, max: 2200 });
  });

  it("never writes an intermediate 0 while a turn streams through several blocks", async () => {
    // The seal/re-register alternation the reader actually saw: anchor registered, then the slot
    // emptied at a block boundary, then a second bubble takes it. Each transition is a chance to
    // write the top of the transcript, and the bottom-anchored position has to survive all of them.
    const wrap = realScroller();
    block(1500);
    const first = containedRow(200);
    const tail = block(900);
    let live: HTMLElement | null = first;
    scroll.setAnchorProvider(() => live);
    const writes = recordWrites(wrap);
    await land();

    first.appendChild(document.createTextNode("prose"));
    await land();
    const onFirst = wrap.scrollTop;

    // The seal: no top-level bubble is live, so the target is the document bottom for as long as
    // the slot stays empty. 1500 + 200 + 1000 - 400.
    live = null;
    tail.style.height = "1000px";
    await land();
    const sealed = wrap.scrollTop;

    // The next block registers its own bubble, with tall evidence below it so the new pin is short
    // of the maximum rather than coincidentally equal to it.
    const second = containedRow(300);
    block(900);
    live = second;
    await land();
    second.appendChild(document.createTextNode("more prose"));
    await land();

    expect({
      onFirst,
      sealed,
      onSecond: wrap.scrollTop,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
    }).toEqual({
      onFirst: 1350,
      sealed: 2300,
      onSecond: 2650,
      state: "following",
      zeros: 0,
    });
  });

  it("falls back to the document bottom when the anchor has left the DOM", async () => {
    // An element with no box has no position to follow, which is the same answer as having no
    // anchor.
    const wrap = realScroller();
    block(1500);
    const streaming = containedRow(200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    const writes = recordWrites(wrap);
    await land();

    streaming.remove();
    block(100);
    await land();

    expect({
      scrollTop: wrap.scrollTop,
      max: wrap.scrollHeight - wrap.clientHeight,
      state: scroll.readingState(),
      zeros: zeroWrites(writes),
    }).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      max: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      zeros: 0,
    });
  });
});

// The anchor in its PRODUCTION wrapper.
describe("the anchor's coordinate space", () => {
  beforeEach(realLayoutReset);

  /** The shipped `.msg-row` declarations that decide this geometry; the containment is what
   *  moves the offsetParent onto the row. */
  function msgRow(): HTMLElement {
    const row = block(0);
    row.style.cssText =
      "display:flex;align-items:flex-end;flex-shrink:0;content-visibility:auto;contain-intrinsic-size:auto 3rem;";
    return row;
  }

  /** The live bubble as `mountText` seats it: inside the row, and positioned, which is the shape
   *  the shipped `.message` rule gives it. */
  function bubbleIn(row: HTMLElement, px: number): HTMLElement {
    const streaming = document.createElement("div");
    streaming.className = "message assistant streaming";
    streaming.style.cssText = `position:relative;height:${String(px)}px;width:100%;`;
    row.appendChild(streaming);
    return streaming;
  }

  // The fixture's own premise, so a layout change that hands the bubble a different offsetParent
  // turns the case below into one that cannot fail rather than leaving it green for the wrong
  // reason.
  it("seats the bubble in a row that is its offsetParent", () => {
    const row = msgRow();
    expect(bubbleIn(row, 200).offsetParent).toBe(row);
  });

  it("keeps Following when the anchor above the fold asks for a negative scrollTop", async () => {
    // The other edge of the same arithmetic, and the one no case reached: an anchor within one
    // viewport of the document's top makes the pin's target NEGATIVE. Three things the fixture has
    // to carry or it cannot fail.
    const wrap = scroll.getScrollEl();
    const streaming = bubbleIn(msgRow(), 200);
    block(3000);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    // −150 asked for (a 200px anchor bottom, a 400px viewport, 50px of air), so 0 is the reachable landing.
    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 0,
      state: "following",
    });
  });

  it("pins the anchor's own bottom, not its offset inside its row", async () => {
    block(1500);
    const streaming = bubbleIn(msgRow(), 200);
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land();

    // The contract in the reader's own units. A gap rather than a scrollTop, so the c-v render the
    // pin itself triggers cannot make the expected number a function of which frame settled last.
    const wrap = scroll.getScrollEl();
    const gap = wrap.getBoundingClientRect().bottom - streaming.getBoundingClientRect().bottom;
    expect({ gap, state: scroll.readingState() }).toEqual({ gap: 50, state: "following" });
  });
});

// The reported failure: "the scroll to bottom button showed '108 new blocks', I clicked it, and
// instead of scrolling to the bottom it scrolled to the start of the last output message." The
// click's landing is short by exactly the height that arrives AFTER the click — the fold batch the
// resume itself flushes, or the turn still streaming — and the reader is left parked with the
// control back on screen, which is why the wrong position sticks.
describe("the resume control's landing", () => {
  beforeEach(realLayoutReset);

  it("lands at the true bottom when the resume's own flush grows the transcript", async () => {
    // `applyFoldPass` wraps every fold, unfold and body mount in `deferWhileReading`, so a reader
    // who parked mid-turn has the whole batch waiting on their return — and `setState("following")`
    // flushes it on the line before the bottom is measured.
    const wrap = realScroller();
    block(3000);
    const late = block(0);
    late.style.cssText = "block-size:0;overflow:hidden;transition:block-size 300ms linear;";
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scroll.deferWhileReading(() => {
      late.style.blockSize = "2000px";
    });
    scrollBtn.click();
    await land(900);

    // The end within the one slack every at-the-end decision shares: a growth of under a pixel is not rewritten.
    expect({
      onEnd: Math.abs(wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop) <= 1,
      state: scroll.readingState(),
      hidden: scrollBtn.classList.contains("hidden"),
    }).toEqual({ onEnd: true, state: "following", hidden: true });
  });

  it("lands at the true bottom when the turn keeps streaming through the click", async () => {
    const wrap = realScroller();
    block(3000);
    const tail = block(200);
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scrollBtn.click();
    await land(60);
    tail.style.height = "2200px";
    await land(900);

    expect(landing(wrap)).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      hidden: true,
    });
  });

  it("hands the scroller back when the reader scrolls while the resume settles", async () => {
    // The resume's settle re-asserts the end for a few frames, and a gesture inside it is the reader
    // overruling their own click. It must stop the settle rather than drag them back.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);
    readerScrollTo(wrap, 200);
    await land(400);

    expect({ state: scroll.readingState(), scrollTop: wrap.scrollTop }).toEqual({
      state: "reading",
      scrollTop: 200,
    });
  });

  it("does not drag an incoming view to the bottom with the outgoing chat's pass", async () => {
    // `attach` restores the incoming view's own saved position, and a pass still running for the
    // chat being parked would overwrite it on the next frame.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);
    scroll.attach({ el: messagesEl, scrollTop: 300, readingState: "reading" });
    await land(400);

    expect(wrap.scrollTop).toBe(300);
  });
});

// While the resume settles, it holds the position Following means, not a different one.
describe("the resume's settle", () => {
  beforeEach(realLayoutReset);

  /** The reachable shape: a live text block with tall evidence BELOW it, so the anchor's pin
   *  sits far above the document bottom. Same geometry as "keeps Following when the pin lands
   *  far from the document bottom" — anchorTop is 1500 + 200 - 400 + 50 = 1350 against a maximum
   *  of 2200. */
  function pinScene(): { wrap: HTMLElement; streaming: HTMLElement } {
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    return { wrap, streaming };
  }

  it("pins the live text block, not the document bottom, while it settles", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    // What `buildTurn` does for a turn the user just sent.
    scroll.scrollToBottom();
    await land(60);
    const opened = wrap.scrollTop;

    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(60);
    const afterChunk = wrap.scrollTop;

    await land(300);
    const held = wrap.scrollTop;

    expect({ opened, afterChunk, held, max: wrap.scrollHeight - wrap.clientHeight }).toEqual({
      opened: 1350,
      afterChunk: 1350,
      held: 1350,
      max: 2200,
    });
  });

  it("lands the resume click on the live text block too", async () => {
    // A reader resuming into a live turn has an anchor registered, so the click lands where Following
    // means rather than at the document maximum.
    const { wrap } = pinScene();
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scrollBtn.click();
    await land(300);

    expect(landing(wrap)).toEqual({ scrollTop: 1350, state: "following", hidden: true });
  });

  it("does not move the reader when the settle ends", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    scroll.scrollToBottom();
    await land(300);
    const inside = wrap.scrollTop;
    // Past the settle, so the follow correction owns the next frame.
    await land(500);
    streaming.appendChild(document.createTextNode("a chunk past the deadline"));
    await land(60);
    const handedOff = wrap.scrollTop;

    // Equal is the assertion: a settle holding the maximum would read 2200 then 1350, an 850px snap.
    expect({ inside, handedOff }).toEqual({ inside: 1350, handedOff: 1350 });
  });

  it("keeps following the anchor for growth arriving while the resume settles", async () => {
    const { wrap, streaming } = pinScene();
    await land();

    scroll.scrollToBottom();

    streaming.style.height = "400px";
    await land(60);
    const grown = wrap.scrollTop;

    streaming.style.height = "600px";
    await land(60);
    const grownAgain = wrap.scrollTop;

    // Both landings are short of their own maximum (2400, then 2600): the growth arrives after the click.
    expect({ grown, grownAgain }).toEqual({ grown: 1550, grownAgain: 1750 });
  });

  it("parks the reader on an upward gesture that lands just above the end", async () => {
    // 40px up off the resume's own landing: a gesture past the slack parks the reader where they asked.
    const wrap = realScroller();
    block(3000);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);

    readerScrollTo(wrap, 2560);
    await land(400);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2560,
      state: "reading",
    });
  });

  it("leaves a jump landing at the live edge where it landed", async () => {
    // The landing clamps to the end, so the jump keeps the reader Following, and the resume's settle must not
    // overwrite it or abort the smooth scroll.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(150);
    await land();

    await park(wrap);
    scrollBtn.click();
    await land(60);

    scroll.jumpTo(target, { block: "start" });
    await land(500);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
    });
  });

  it("keeps a jump's landing on the end Following after the reader's own park", async () => {
    // The jump is the reader's new position: its own scroll event is the owner's, and a landing on the end follows.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(150);
    await land();

    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    scroll.jumpTo(target, { block: "start", behavior: "instant" });
    await land();

    expect(landing(wrap)).toEqual({
      scrollTop: wrap.scrollHeight - wrap.clientHeight,
      state: "following",
      hidden: true,
    });
  });
});

// The STREAMING follow write's licence, and that it is re-read where it is spent.

describe("the streaming follow write's licence", () => {
  beforeEach(realLayoutReset);

  /** Following at the live edge with the resume's settle over, so a write observed afterwards is
   *  the follow correction's own rather than the settle's re-assert. */
  async function followingAtEdge(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(3000);
    await land();
    scroll.scrollToBottom();
    await land(100);
    return wrap;
  }

  it("does not take the reader back when they scroll up inside its frame", async () => {
    const wrap = await followingAtEdge();
    expect(scroll.readingState()).toBe("following");

    // The mutation licenses ONE write, and awaiting a microtask is the MutationObserver's own
    // delivery — so the frame is queued and has not run.
    block(400);
    await Promise.resolve();
    readerScrollTo(wrap, 0);
    await land(300);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 0,
      state: "reading",
    });
  });

  it("does not land on a view handed over while it was queued", async () => {
    // The parked-chat case, and the one that lets `messages-parked-views.test.ts` stop waiting out
    // frames before a gesture: `attach` restores the incoming view's own Reading, so the re-read
    // refuses a write the outgoing view licensed.
    const wrap = await followingAtEdge();

    block(400);
    await Promise.resolve();
    scroll.detach();
    scroll.attach({ el: messagesEl, scrollTop: 250, readingState: "reading" });
    await land(300);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 250,
      state: "reading",
    });
  });
});

describe("a smooth jump's own flight", () => {
  beforeEach(realLayoutReset);

  it("keeps a jump up from the live edge parked while the transcript keeps resizing", async () => {
    const wrap = realScroller();
    const target = block(400);
    block(20_000);
    const tail = block(10);
    await land();
    scroll.scrollToBottom();
    await land(100);
    expect(scroll.readingState()).toBe("following");

    // A growing tail, as a streaming turn delivers it: every resize licenses a follow write.
    let growths = 0;
    const grow = setInterval(() => {
      tail.style.height = `${String(tail.offsetHeight + 10)}px`;
      growths++;
    }, 20);
    try {
      const landed = flightEnds(wrap);
      scroll.jumpTo(target, { block: "start" });
      await landed;
      // Growth keeps arriving after the landing, where a follow write would no longer be a flight's.
      const atLanding = growths;
      await until(() => growths >= atLanding + 3, "three growths after the landing");
    } finally {
      clearInterval(grow);
    }
    // The last growth's observer delivery, and the follow write it would license.
    await framesDrained("the last growth's follow frames");

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 0,
      state: "reading",
    });
  });
});

// `onReaderGesture`: the seam for "the reader said where they want to be".

describe("onReaderGesture", () => {
  beforeEach(realLayoutReset);

  it("fires for a scroll the reader performed", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 1200;
    await land();
    off();

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("stays silent for a scroll event at the position the last one reported", async () => {
    // The scroller left and came back inside one task, as a clamp the next layout undoes
    // does: the event it leaves states no new position.
    const wrap = realScroller();
    block(3000);
    await land();
    wrap.scrollTop = 1200;
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 900;
    wrap.scrollTop = 1200;
    await land();
    off();

    expect(seen).not.toHaveBeenCalled();
  });

  it("fires when the reader asks for the live edge", async () => {
    // The resume control. It is a gesture whose whole meaning is "take me to the live edge", so a
    // consumer holding a position the reader has now abandoned has to hear it — and the scroll
    // listener cannot say so, because this landing is written through `scrollSelfTo` and excused.
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);
    expect(scroll.readingState()).toBe("reading");

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scrollBtn.click();
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(2600);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("fires for a turn the reader just sent", async () => {
    // `buildTurn`'s `scrollToBottom()` for a triggered turn, and the app's own comment at that call
    // site says why it counts: the reader asked for the turn, so the pin takes them to it even if
    // they were parked further up.
    const wrap = realScroller();
    block(3000);
    await land();
    await park(wrap);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scroll.scrollToBottom();
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(2600);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("publishes a live-edge request once, not once per re-assert frame", async () => {
    // The resume's settle re-asserts its landing every frame until it holds. The GESTURE happened once, so publishing from the frame loop would hand a consumer dozens of
    // identical revocations and make the seam unusable for anything that repaints on one.
    const wrap = realScroller();
    block(3000);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    await park(wrap);

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scrollBtn.click();
    // Well past the resume's settle, with growth arriving inside it so the frames have something to
    // re-assert.
    await land(200);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(800);
    off();

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("does not fire for an instant jump", async () => {
    // A jump is a gesture whose whole meaning is "this turn", so publishing it revokes the very
    // pick that produced it — the turn map sets its pick and then jumps to that turn.
    const wrap = realScroller();
    block(3000);
    const target = block(200);
    block(2000);
    await land();

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    scroll.jumpTo(target, { block: "start", behavior: "instant" });
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(target.offsetTop);
    expect(scroll.readingState()).toBe("reading");
    expect(seen).not.toHaveBeenCalled();
  });

  it("does not fire for the owner's own follow writes while a turn streams", async () => {
    // The other half of the contract, and the case the seam was built for: a turn streaming under a
    // reader who has not moved writes a scroll position several times a second, and none of those
    // is the reader changing their mind.
    const wrap = realScroller();
    block(1500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    scroll.setAnchorProvider(() => streaming);
    await land();
    expect(scroll.readingState()).toBe("following");

    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    streaming.appendChild(document.createTextNode("a streamed chunk"));
    await land(300);
    off();

    expect(wrap.scrollTop).toBe(1350);
    expect(seen).not.toHaveBeenCalled();
  });

  it("fires for a gesture that keeps the reader Following", async () => {
    // Not `onReadingStateChange`: a move within the end's slack stays Following, so a state listener hears
    // nothing while the reader has plainly acted.
    const wrap = realScroller();
    block(3000);
    await land();
    // A pixel up from the end is within the slack: the end, and still Following.
    wrap.scrollTop = 2599;
    await land();
    expect(scroll.readingState()).toBe("following");

    const seen = vi.fn();
    const stateSeen = vi.fn();
    scroll.onReadingStateChange(stateSeen);
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 2600;
    await land();
    off();

    expect(seen).toHaveBeenCalledTimes(1);
    expect(stateSeen).not.toHaveBeenCalled();
  });

  it("stops firing once unregistered", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    wrap.scrollTop = 500;
    await land();
    off();
    wrap.scrollTop = 900;
    await land();

    expect(seen).toHaveBeenCalledTimes(1);
  });
});

// THE SCROLLBAR, which is the one input surface with no event of its own: a thumb drag produces no
// wheel and no touch, so the PRESS is the input and its position is the only thing separating it
// from a click in the transcript.

/** `scrollbar-gutter: stable` is the shipped declaration (css/13-messages.css) and it reserves
 *  nothing where the bar is an overlay, so the answer is measured on a throwaway box carrying that
 *  declaration rather than assumed from the engine. */
function reservesScrollbarGutter(): boolean {
  const probe = document.createElement("div");
  probe.style.cssText =
    "position:absolute;visibility:hidden;height:100px;width:100px;" +
    "overflow-y:auto;scrollbar-gutter:stable;";
  const tall = document.createElement("div");
  tall.style.cssText = "height:1000px;";
  probe.appendChild(tall);
  document.body.appendChild(probe);
  const gutter = probe.offsetWidth - probe.clientWidth;
  probe.remove();
  return gutter > 0;
}

describe.skipIf(!reservesScrollbarGutter())("the scrollbar as an input surface", () => {
  beforeEach(realLayoutReset);

  /** A press on the thumb: the reserved gutter's width, and the press that lands in it.
   *  `scrollbar-gutter: stable` reserves the strip whether or not a bar is drawn, which is what
   *  makes the arithmetic answerable in a test. */
  function gutterPress(wrap: HTMLElement): { gutter: number; press: () => void } {
    const gutter = wrap.offsetWidth - wrap.clientWidth;
    const x = wrap.getBoundingClientRect().right - gutter / 2;
    return {
      gutter,
      press: () => {
        // `clientY` matters: it is the origin `dragThumb` measures its travel from.
        wrap.dispatchEvent(
          new PointerEvent("pointerdown", { clientX: x, clientY: 200, bubbles: true }),
        );
      },
    };
  }

  /** A transcript that overflows, Following at its end: a demotion in any case below came from the press and the
   *  scroller's own move. */
  async function driftedOffTheEdge(): Promise<HTMLElement> {
    block(1500);
    const wrap = scroll.getScrollEl();
    await land();
    expect(scroll.readingState()).toBe("following");
    return wrap;
  }

  /** Move the held thumb by `dy`, watched on the DOCUMENT because a drag that leaves the
   *  scroller still owns the bar. The thumb travels WITH the content, so this sign does not
   *  invert the way a finger's does. */
  function dragThumb(wrap: HTMLElement, dy: number): void {
    const gutter = wrap.offsetWidth - wrap.clientWidth;
    const x = wrap.getBoundingClientRect().right - gutter / 2;
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: x, clientY: 200 + dy }));
  }

  it("parks the reader when the thumb is dragged UP", async () => {
    const wrap = await driftedOffTheEdge();
    const { gutter, press } = gutterPress(wrap);
    // The premise this surface needs, pinned rather than assumed: this platform reserves a strip to
    // aim at. An overlay scrollbar measures 0, and there the surface is deliberately absent because
    // a touch drag is how you scroll.
    expect(gutter).toBeGreaterThan(0);

    press();
    dragThumb(wrap, -40);
    wrap.scrollTop -= 40;
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
  });

  it("leaves the reader Following when the thumb is dragged DOWN", async () => {
    const wrap = await driftedOffTheEdge();
    gutterPress(wrap).press();
    dragThumb(wrap, 40);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("takes no aim from a drag that started inside the transcript", async () => {
    // A copy button, a fold header. Tracked as a thumb, the pointer's own travel would park a
    // reader who never touched the scrollbar.
    const wrap = await driftedOffTheEdge();
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", {
        clientX: wrap.getBoundingClientRect().left + 10,
        bubbles: true,
      }),
    );
    dragThumb(wrap, -40);
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });

  it("suppresses the auto-scroll for as long as the thumb is held", async () => {
    // A held thumb keeps the gesture open whatever the idle bound, so no follow write lands under it.
    await driftedOffTheEdge();
    const wrap = scroll.getScrollEl();
    gutterPress(wrap).press();
    await land(400);
    const was = wrap.scrollTop;
    block(1500);
    await land();
    expect(wrap.scrollTop).toBe(was);
  });

  it("gives the scroller back when the thumb is released", async () => {
    // A release that goes unseen latches the licence on for the session.
    await driftedOffTheEdge();
    const wrap = scroll.getScrollEl();
    gutterPress(wrap).press();
    document.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    await land(400);
    await frames(16);
    block(1500);
    await land();
    expect(wrap.scrollTop).toBe(wrap.scrollHeight - wrap.clientHeight);
  });
});

// THE OTHER PLATFORM CLASS. A scroller reserving no strip has no thumb to aim at, so a press at its
// right edge must take no aim — `inScrollbarGutter`'s width test, the one clause the block above
// cannot reach.
describe("the scrollbar surface a platform does not reserve", () => {
  beforeEach(realLayoutReset);

  it("takes no aim from a press at the scroller's right edge", async () => {
    const wrap = realScroller();
    wrap.style.setProperty("scrollbar-width", "none");
    block(1500);
    await land();
    expect({ gutter: wrap.offsetWidth - wrap.clientWidth, state: scroll.readingState() }).toEqual({
      gutter: 0,
      state: "following",
    });

    // The tightest witness the guard has: with the width test dropped, a press AT the right edge
    // satisfies the remaining comparison and the drag parks them.
    const { right } = wrap.getBoundingClientRect();
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: right, clientY: 200, bubbles: true }),
    );
    document.dispatchEvent(new PointerEvent("pointermove", { clientX: right, clientY: 160 }));
    wrap.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("following");
  });
});

describe("holding what the reader reads", () => {
  beforeEach(realLayoutReset);

  /** Twenty 100px rows in the 400px scrollport, the reader parked at 500: row 5 is at the top
   *  edge and row 6 holds the reading line, a third of the way down. */
  async function parkedAmongRows(): Promise<{ wrap: HTMLElement; rows: HTMLElement[] }> {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 500);
    await land();
    expect(scroll.readingState()).toBe("reading");
    return { wrap, rows };
  }

  it("publishes no reader gesture for the owner's own hold", async () => {
    const { wrap } = await parkedAmongRows();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    const above = document.createElement("div");
    above.style.cssText = "height:200px;";
    messagesEl.prepend(above);
    await land();
    off();

    // The premise: the hold did move the scroller.
    expect(wrap.scrollTop).toBe(700);
    expect(seen).not.toHaveBeenCalled();
  });

  it("puts the reading line back when the row above it grows", async () => {
    // The top row is the one growing, so with no hold the row the reader is on would move down by the growth.
    const { rows } = await parkedAmongRows();
    const reading = rows[6]!;
    const was = reading.getBoundingClientRect().top;
    rows[5]!.style.height = "300px";
    await land();

    expect(reading.getBoundingClientRect().top).toBe(was);
  });

  it("holds a growth that lands between its own correction and that correction's scroll event", async () => {
    // A growth the observer reports after the correction's write and before that write's scroll event.
    const { rows } = await parkedAmongRows();
    const reading = rows[6]!;
    const was = reading.getBoundingClientRect().top;
    let grown = false;
    const late = new ResizeObserver(() => {
      if (!grown) {
        grown = true;
        // A task after this delivery: past the correction's write, before its scroll event.
        fixtureTask(() => {
          rows[5]!.style.height = "500px";
        });
      }
    });
    rows[5]!.style.height = "300px";
    late.observe(rows[5]!);
    await until(() => grown && fixtureTasks === 0, "the late growth to land");
    await land();
    late.disconnect();

    expect(grown).toBe(true);
    expect(reading.getBoundingClientRect().top).toBe(was);
  });

  it("leaves a press's own change where it opened", async () => {
    // What a click opens is the reader's: the top edge holds and the content below moves for it.
    const { rows } = await parkedAmongRows();
    const reading = rows[6]!;
    const was = reading.getBoundingClientRect().top;
    rows[5]!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    rows[5]!.style.height = "300px";
    await land();

    expect(reading.getBoundingClientRect().top).toBe(was + 200);
  });

  it("re-reads the line where a correction cannot scroll, and stops correcting", async () => {
    // At offset 0 a shrink above the line has nowhere to be put back; the reader now sees the line
    // where it landed, so a later growth is held there.
    const { rows } = await parkedAmongRows();
    const wrap = scroll.getScrollEl();
    readerScrollTo(wrap, 0);
    await land();
    const reading = rows[1]!;
    rows[0]!.style.height = "50px";
    await land();
    const landed = reading.getBoundingClientRect().top;
    const frames = vi.spyOn(window, "requestAnimationFrame");
    await land();
    const idleFrames = frames.mock.calls.length;
    frames.mockRestore();
    rows[0]!.style.height = "300px";
    await land();

    expect({ idleFrames, top: reading.getBoundingClientRect().top }).toEqual({
      idleFrames: 0,
      top: landed,
    });
  });

  it("re-picks the line when its element loses its box, instead of reading that as a drift", async () => {
    const { wrap, rows } = await parkedAmongRows();
    rows[6]!.hidden = true;
    await land();

    expect(wrap.scrollTop).toBe(500);
  });

  it("keeps a jump written inside a frame callback while a row resizes in that frame", async () => {
    // The jump's scroll event is deferred past this frame's ResizeObserver delivery, so the resize
    // is seen first.
    const { wrap, rows } = await parkedAmongRows();
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        rows[18]!.style.height = "150px";
        scroll.jumpTo(rows[12]!, { block: "start", behavior: "instant" });
        resolve();
      });
    });
    await land();

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 1200,
      state: "reading",
    });
  });

  it("keeps a shift written inside a frame callback while a row resizes in that frame", async () => {
    // The shell panel's opening compensation, from its own frame callback.
    const { wrap, rows } = await parkedAmongRows();
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        rows[18]!.style.height = "150px";
        scroll.shiftScroll(200);
        resolve();
      });
    });
    await land();

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 700,
      state: "reading",
    });
  });

  /** The page writers' order around a landing: the paint re-measures and writes the page, then the skeleton's owner
   *  re-measures and drops it (messages.ts, chat.ts). */
  function landPage(px: number): void {
    const page = document.createElement("div");
    page.style.cssText = `height:${String(px)}px;`;
    document.getElementById("load-more-skeleton")?.after(page);
    document.getElementById("load-more-skeleton")?.remove();
  }

  /** A page of `px` landing above everything, the way the real caller lands one: after the fetch,
   *  with the skeleton dropped in the same task. */
  function loadsPage(px: number): () => void {
    return () => {
      setTimeout(() => {
        landPage(px);
      }, 0);
    };
  }

  it("holds the reader where the skeleton left them at offset 0, where nothing anchors, when an older page lands", async () => {
    // At offset 0 nothing anchors, so only the pass can take back what the skeleton added.
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 150);
    await land();
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    readerScrollTo(wrap, 0);
    await land();
    const was = rows[0]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[0]!.getBoundingClientRect().top).toBe(was);
  });

  it("does not count an anchored older page twice", async () => {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 150);
    await land();
    scroll.setLoadMore(loadsPage(400), true);
    await land();
    const was = rows[1]!.getBoundingClientRect().top;
    const travel = wrap.scrollTop - 50;
    readerScrollTo(wrap, 50);
    await land();

    expect(rows[1]!.getBoundingClientRect().top).toBe(was + travel);
  });

  /** A fetch the test lands by hand, so the reader can move while it is in flight. */
  function heldPage(px: number): { load: () => void; land: () => void } {
    let pending = false;
    return {
      load: () => {
        pending = true;
      },
      land: () => {
        expect(pending).toBe(true);
        landPage(px);
      },
    };
  }

  it("leaves the reader where they scrolled to at offset 0 while the older page was in flight", async () => {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 150);
    await land();
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    readerScrollTo(wrap, 80);
    await land();
    readerScrollTo(wrap, 0);
    await land();
    const was = rows[0]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[0]!.getBoundingClientRect().top).toBe(was);
  });

  it("leaves the reader where they scrolled to under an anchored page that was in flight", async () => {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 150);
    await land();
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    readerScrollTo(wrap, 90);
    await land();
    readerScrollTo(wrap, 30);
    await land();
    const was = rows[0]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[0]!.getBoundingClientRect().top).toBe(was);
  });

  /** Twenty 100px rows, the reader parked, and an older page of 400px in flight since the reader
   *  scrolled near the top. */
  async function pageInFlight(): Promise<{
    wrap: HTMLElement;
    rows: HTMLElement[];
    page: { land: () => void };
  }> {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 150);
    await land();
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    readerScrollTo(wrap, 80);
    await land();
    return { wrap, rows, page };
  }

  it("leaves the reader where a rail jump put them while the older page was in flight", async () => {
    const { rows, page } = await pageInFlight();
    await scroll.jumpToOffset(1200, "instant");
    await land();
    const was = rows[14]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[14]!.getBoundingClientRect().top).toBe(was);
  });

  it("writes nothing below the live edge a resume reached while the older page was in flight", async () => {
    const { wrap, rows, page } = await pageInFlight();
    scroll.scrollToBottom();
    // Past the resume's settle, so only the landing can write.
    await land(900);
    const was = rows[19]!.getBoundingClientRect().top;
    const edge = wrap.scrollTop;
    const writes = recordWrites(wrap);
    page.land();
    await land();

    expect({
      above: writes.filter((top) => top < edge),
      top: rows[19]!.getBoundingClientRect().top,
    }).toEqual({ above: [], top: was });
  });

  it("keeps the reading line where its own hold put it back while the older page was in flight", async () => {
    const { wrap, page } = await pageInFlight();
    const box = wrap.getBoundingClientRect();
    const x = box.left + box.width / 2;
    const edgeRow = document.elementFromPoint(x, box.top + 1) as HTMLElement;
    const reading = document.elementFromPoint(x, box.top + scroll.readingLineOffset())!;
    expect(reading).not.toBe(edgeRow);
    const was = reading.getBoundingClientRect().top;
    edgeRow.style.height = "300px";
    await land();
    const held = reading.getBoundingClientRect().top;
    page.land();
    await land();

    expect({ held, landed: reading.getBoundingClientRect().top }).toEqual({
      held: was,
      landed: was,
    });
  });

  it("keeps the hold of a growth above the view while the older page was in flight", async () => {
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    readerScrollTo(wrap, 900);
    await land();
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    scroll.noteHistoryEdge(true);
    await land();
    const was = rows[12]!.getBoundingClientRect().top;
    rows[3]!.style.height = "200px";
    await land();
    const anchored = rows[12]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect({ anchored, landed: rows[12]!.getBoundingClientRect().top }).toEqual({
      anchored: was,
      landed: was,
    });
  });

  it("leaves the reader at the top when the older page that was asked for lands there", async () => {
    // A pointer-down on the transcript drops the reading line, so only the pass can put back what landed above at
    // offset 0, and only against a measure taken before the page.
    const wrap = realScroller();
    const rows = Array.from({ length: 20 }, () => block(100));
    await land();
    await park(wrap);
    rows[0]!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    const page = heldPage(400);
    scroll.setLoadMore(page.load, true);
    await land();
    scroll.noteHistoryEdge(true);
    await land();
    const was = rows[0]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[0]!.getBoundingClientRect().top).toBe(was);
  });

  it("keeps a reader's move the page landed ahead of that move's scroll event", async () => {
    const { wrap, rows, page } = await pageInFlight();
    readerScrollTo(wrap, 30);
    // Same task: the scroller has moved and its scroll event has not run.
    const was = rows[1]!.getBoundingClientRect().top;
    page.land();
    await land();

    expect(rows[1]!.getBoundingClientRect().top).toBe(was);
  });
});

describe("laying out what is near the scrollport", () => {
  beforeEach(realLayoutReset);
  // The attach case re-roots the observers on its own view; hand them back to the transcript root.
  afterEach(() => {
    scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
  });

  /** A `.tool-call` box `px` tall, appended to `host`. */
  function skippable(host: HTMLElement, px: number): HTMLElement {
    const box = document.createElement("div");
    box.className = "tool-call";
    box.style.cssText = `height:${String(px)}px;`;
    host.appendChild(box);
    return box;
  }

  it("marks a box within one and a half scrollports of the view, and not one past that", async () => {
    // 400px scrollport parked at the top: the near span ends 600px below it, at 1000.
    const wrap = realScroller();
    block(850);
    const near = skippable(messagesEl, 100);
    block(1000);
    const far = skippable(messagesEl, 100);
    block(1000);
    await land();
    await park(wrap);
    const before = { near: near.hasAttribute("data-near"), far: far.hasAttribute("data-near") };
    wrap.scrollTop = 1500;
    await land();

    expect({ before, after: far.hasAttribute("data-near") }).toEqual({
      before: { near: true, far: false },
      after: true,
    });
  });

  it("measures the lead in scrollport heights on a tall, narrow scroller", async () => {
    // 200x800 parked at the top: the near span ends 1200px below the scrollport, at 2000.
    const wrap = realScroller();
    wrap.style.width = "200px";
    wrap.style.height = "800px";
    block(1920);
    const near = skippable(messagesEl, 100);
    block(20);
    const far = skippable(messagesEl, 100);
    block(1000);
    await land();
    await park(wrap);

    expect({ near: near.hasAttribute("data-near"), far: far.hasAttribute("data-near") }).toEqual({
      near: true,
      far: false,
    });
  });

  it("re-measures the lead when the scrollport's height changes", async () => {
    // 400px: the span ends at 1000. 800px: it ends at 2000.
    const wrap = realScroller();
    block(1300);
    const box = skippable(messagesEl, 100);
    block(1000);
    await land();
    await park(wrap);
    const before = box.hasAttribute("data-near");
    wrap.style.height = "800px";
    await land();

    expect({ before, after: box.hasAttribute("data-near") }).toEqual({
      before: false,
      after: true,
    });
  });

  it("marks the boxes a view already holds when the scroller attaches it", async () => {
    const wrap = realScroller();
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
    const shown = document.createElement("div");
    messagesEl.replaceChildren(shown);
    scroll.attach({ el: shown, scrollTop: 0, readingState: "following" });
    // Built beside the attached view, the way the multiplexer seats a chat's view.
    const incoming = document.createElement("div");
    const box = skippable(incoming, 100);
    messagesEl.prepend(incoming);
    await land();
    const before = box.hasAttribute("data-near");

    scroll.attach({ el: incoming, scrollTop: 0, readingState: "following" });
    await land();

    expect({ before, after: box.hasAttribute("data-near") }).toEqual({
      before: false,
      after: true,
    });
  });

  it("delivers nothing to a box once its view is detached", async () => {
    realScroller();
    const box = skippable(messagesEl, 100);
    await land();
    const before = box.hasAttribute("data-near");
    scroll.detach();
    // Out of the near span (no box at all) while parked: an observer still holding it would unmark it.
    box.hidden = true;
    await land();

    expect({ before, after: box.hasAttribute("data-near") }).toEqual({ before: true, after: true });
  });
});

// A row the engine has never laid out sits at its 3rem placeholder until the near lead reaches it, then takes its real
// size above the reader. The scroller anchors nothing here, as WebKit failed to in the transcript's geometry, so the
// controller alone keeps the text in place.
describe("a cold row laid out above the reader", () => {
  const sheet = document.createElement("style");
  // `.cold` stands in for an engine with no lead of its own (WebKit): only `[data-near]` lays the row out, as the
  // shipped `[id="messages-wrap"] [data-near]` rule does (css/13-messages.css).
  sheet.textContent =
    ".msg-row.cold{content-visibility:hidden;contain-intrinsic-size:auto 3rem;}" +
    '[id="messagesWrap"] [data-near]{content-visibility:visible;contain:content;}';

  beforeEach(async () => {
    document.head.appendChild(sheet);
    await realLayoutReset();
  });
  afterEach(() => {
    sheet.remove();
    scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
  });

  const frame = (): Promise<void> =>
    new Promise((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });

  /** A transcript view: twenty 100px blocks, a cold row whose content is 380px, then `after` more blocks. */
  function coldView(after = 30): { view: HTMLElement; cold: HTMLElement } {
    const view = document.createElement("div");
    const add = (n: number): void => {
      for (let i = 0; i < n; i++) {
        const b = document.createElement("div");
        b.style.cssText = "height:100px;";
        view.appendChild(b);
      }
    };
    add(20);
    const cold = document.createElement("div");
    cold.className = "msg-row cold";
    const body = document.createElement("div");
    body.style.cssText = "height:380px;";
    cold.appendChild(body);
    view.appendChild(cold);
    add(after);
    messagesEl.appendChild(view);
    return { view, cold };
  }

  /** The element on the reading line, which is the text the reader is looking at. */
  function onLine(wrap: HTMLElement): Element {
    const box = wrap.getBoundingClientRect();
    return document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 3)!;
  }

  /** Every scroll step's residual: how far the reader's text moved on screen, frame to frame, beyond what the step
   *  itself moved it. One step per frame, as momentum delivers them, so every frame carries a scroll event. */
  async function flingResiduals(wrap: HTMLElement): Promise<number[]> {
    const residuals: number[] = [];
    let ref = onLine(wrap);
    const step = async (dy: number): Promise<void> => {
      if (
        !wrap.contains(ref) ||
        ref.getBoundingClientRect().bottom < wrap.getBoundingClientRect().top
      ) {
        ref = onLine(wrap);
      }
      const top0 = ref.getBoundingClientRect().top;
      // Between frames, where a real scroll lands: ahead of its frame's scroll event, never inside the frame.
      await new Promise((r) => setTimeout(r, 0));
      const st0 = wrap.scrollTop;
      if (dy !== 0) {
        wrap.scrollTo({ top: st0 + dy, behavior: "instant" });
      }
      const moved = wrap.scrollTop - st0;
      await frame();
      residuals.push(Math.round(ref.getBoundingClientRect().top - (top0 - moved)));
    };
    const touch = (type: "touchstart" | "touchmove" | "touchend", y?: number): void => {
      wrap.dispatchEvent(touchEvent(type, wrap, y));
    };
    // The finger drags the content toward older text, then lets go into momentum, which carries no input event.
    let y = 100;
    touch("touchstart", y);
    for (let i = 0; i < 10; i++) {
      y += 40;
      touch("touchmove", y);
      await step(-40);
    }
    touch("touchend");
    for (let v = -40 * 0.85; Math.abs(v) > 1; v *= 0.85) {
      await step(v);
    }
    for (let i = 0; i < 10; i++) {
      await step(0);
    }
    return residuals;
  }

  it("keeps the reader's text in place when a row above it takes its real size after a chat switch", async () => {
    const wrap = scroll.getScrollEl();
    const { view: a, cold } = coldView();
    const b = document.createElement("div");
    b.style.cssText = "height:2000px;";
    scroll.attach({ el: a, scrollTop: 0, readingState: "following" });
    await land();
    // The reader parks with the cold row 850px above the scrollport, past the 600px lead.
    readerScrollTo(wrap, 2900);
    await land();
    // Away to another chat and back, the way the multiplexer parks a view.
    const parked = scroll.detach();
    a.hidden = true;
    messagesEl.appendChild(b);
    scroll.attach({ el: b, scrollTop: 0, readingState: "following" });
    await land();
    scroll.detach();
    b.remove();
    a.hidden = false;
    scroll.attach({ el: a, ...parked });
    await land();
    const premise = {
      state: scroll.readingState(),
      top: wrap.scrollTop,
      cold: cold.getBoundingClientRect().height,
    };

    const residuals = await flingResiduals(wrap);

    expect({
      premise,
      grown: cold.getBoundingClientRect().height,
      jumps: residuals.filter((r) => r !== 0),
    }).toEqual({
      premise: { state: "reading", top: 2900, cold: 48 },
      grown: 380,
      jumps: [],
    });
  });

  it("still follows the live edge through streamed appends with a cold row above", async () => {
    const wrap = scroll.getScrollEl();
    // Five blocks below the cold row: at the live edge it sits 100px above the scrollport, inside the 600px lead.
    const { view, cold } = coldView(5);
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    scroll.scrollToBottom();
    await land();
    const grown = cold.getBoundingClientRect().height;
    const gaps: number[] = [];
    for (let i = 0; i < 8; i++) {
      const chunk = document.createElement("div");
      chunk.style.cssText = "height:60px;";
      view.appendChild(chunk);
      await frame();
      await frame();
      gaps.push(Math.round(wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop));
    }

    expect({ grown, state: scroll.readingState(), gaps }).toEqual({
      grown: 380,
      state: "following",
      gaps: [0, 0, 0, 0, 0, 0, 0, 0],
    });
  });
});

// WebKit can reset the scroller to 0 in a layout later in the activation task that restored a parked view (a chat
// switch from the side menu), with the view already at its full height. The scenes on `switchBackUnderReset` reproduce
// that reset by collapsing the view for one forced layout right after the restore; the rest return to a parked view
// under the reader's own scroll, or a change that moves their text or replaces the element showing it.
describe("a parked view's restore under a platform reset", () => {
  beforeEach(async () => {
    await realLayoutReset();
  });
  afterEach(() => {
    skeleton.px = 0;
    scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
  });

  /** A view of forty 100px rows inside the transcript root. */
  function rowsView(): HTMLElement {
    const view = document.createElement("div");
    for (let i = 0; i < 40; i++) {
      const b = document.createElement("div");
      b.style.cssText = "height:100px;";
      view.appendChild(b);
    }
    messagesEl.appendChild(view);
    return view;
  }

  /** Park `view` with the reader at 1200 (Reading) or on the live edge (Following), switch to another view and back
   *  (running `beforeReturn` just before the restore), and let the platform reset the scroller in the restoring task.
   *  Returns every scrollTop an animation frame saw from then on. */
  async function switchBackUnderReset(
    view: HTMLElement,
    state: "reading" | "following",
    beforeReturn: () => void = () => undefined,
  ): Promise<number[]> {
    const wrap = scroll.getScrollEl();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    if (state === "reading") {
      readerScrollTo(wrap, 1200);
    } else {
      scroll.scrollToBottom();
    }
    await land(900);
    const parked = scroll.detach();
    view.hidden = true;
    const other = document.createElement("div");
    other.style.cssText = "height:2000px;";
    messagesEl.appendChild(other);
    scroll.attach({ el: other, scrollTop: 0, readingState: "following" });
    await land();
    scroll.detach();
    other.remove();
    view.hidden = false;
    beforeReturn();
    scroll.attach({ el: view, ...parked });
    view.style.display = "none";
    expect(wrap.scrollTop).toBe(0);
    view.style.display = "";
    const seen: number[] = [];
    await new Promise<void>((resolve) => {
      let n = 0;
      const tick = (): void => {
        seen.push(wrap.scrollTop);
        if (++n < 12) {
          requestAnimationFrame(tick);
        } else {
          resolve();
        }
      };
      requestAnimationFrame(tick);
    });
    await land();
    return seen;
  }

  it("puts a reader back where they were reading", async () => {
    const view = rowsView();
    await switchBackUnderReset(view, "reading");
    const wrap = scroll.getScrollEl();

    expect({
      top: wrap.scrollTop,
      state: scroll.readingState(),
      resume: !scrollBtn.classList.contains("hidden"),
    }).toEqual({ top: 1200, state: "reading", resume: true });
  });

  it("puts a reader back where they were reading in a view re-rendered while it was parked", async () => {
    const view = rowsView();
    // One page re-pointed at another run and back renders every row anew, so no element the park saw survives.
    await switchBackUnderReset(view, "reading", () => {
      view.replaceChildren(...[...view.children].map((row) => row.cloneNode(true)));
    });

    expect({ top: scroll.getScrollEl().scrollTop, state: scroll.readingState() }).toEqual({
      top: 1200,
      state: "reading",
    });
  });

  it("keeps a reader who was following on the live edge in every frame", async () => {
    const view = rowsView();
    const seen = await switchBackUnderReset(view, "following");
    const wrap = scroll.getScrollEl();
    const edge = wrap.scrollHeight - wrap.clientHeight;

    expect({
      off: seen.filter((top) => Math.abs(top - edge) >= 1),
      state: scroll.readingState(),
    }).toEqual({
      off: [],
      state: "following",
    });
  });

  it("hands the scroller to a reader who scrolls before the restore settles", async () => {
    const wrap = scroll.getScrollEl();
    const view = rowsView();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    readerScrollTo(wrap, 1200);
    await land();
    const parked = scroll.detach();
    scroll.attach({ el: view, ...parked });
    readerScrollTo(wrap, 600);
    await land();

    expect({ top: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      top: 600,
      state: "reading",
    });
  });

  /** Park `view` with the reader at `top`, switch away and back, run `after` in the restoring task, and return how far
   *  the text on the reading line (or `opts.ref`'s, re-read every frame) had moved on screen at each of the next
   *  thirteen frames (zeros dropped). `opts.beforeReturn` runs just before the restore. */
  async function movesAfterReturn(
    view: HTMLElement,
    top: number,
    after: () => void,
    opts: { ref?: () => Element; beforeReturn?: () => void } = {},
  ): Promise<{ moves: number[]; state: string }> {
    const wrap = scroll.getScrollEl();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    readerScrollTo(wrap, top);
    await land(400);
    const parked = scroll.detach();
    view.hidden = true;
    const other = document.createElement("div");
    other.style.cssText = "height:2000px;";
    messagesEl.appendChild(other);
    scroll.attach({ el: other, scrollTop: 0, readingState: "following" });
    await land();
    scroll.detach();
    other.remove();
    view.hidden = false;
    opts.beforeReturn?.();
    scroll.attach({ el: view, ...parked });
    const box = wrap.getBoundingClientRect();
    const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 3)!;
    const ref = opts.ref ?? ((): Element => hit);
    const base = ref().getBoundingClientRect().top;
    const moves: number[] = [];
    // Queued before `after`, so each frame's sample runs ahead of that frame's changes and reads what the previous
    // frame painted, never a layout its ResizeObserver delivery has yet to answer.
    const sampled = new Promise<void>((resolve) => {
      let n = 0;
      const tick = (): void => {
        moves.push(Math.round(ref().getBoundingClientRect().top - base));
        if (++n < 13) {
          requestAnimationFrame(tick);
        } else {
          resolve();
        }
      };
      requestAnimationFrame(tick);
    });
    after();
    await sampled;
    return { moves: moves.filter((m) => m !== 0), state: scroll.readingState() };
  }

  it("puts the reader's text back where they left it when they leave with an older page still loading", async () => {
    const wrap = scroll.getScrollEl();
    const view = rowsView();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    readerScrollTo(wrap, 500);
    await land();
    skeleton.px = 200;
    scroll.setLoadMore(() => undefined, true);
    scroll.noteHistoryEdge(true);
    await land();
    const box = wrap.getBoundingClientRect();
    const ref = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 3)!;
    const left = ref.getBoundingClientRect().top;
    const loading = document.getElementById("load-more-skeleton") !== null;
    const parked = scroll.detach();
    view.hidden = true;
    const other = document.createElement("div");
    other.style.cssText = "height:2000px;";
    messagesEl.appendChild(other);
    scroll.attach({ el: other, scrollTop: 0, readingState: "following" });
    await land();
    scroll.detach();
    other.remove();
    view.hidden = false;
    scroll.attach({ el: view, ...parked });
    await land();

    expect({
      loading,
      moved: Math.round(ref.getBoundingClientRect().top - left),
      state: scroll.readingState(),
    }).toEqual({ loading: true, moved: 0, state: "reading" });
  });

  it("keeps the reader's text still when an older page's skeleton lands above them on return", async () => {
    const view = rowsView();
    skeleton.px = 200;
    // The activation rewires pagination; with the reader inside the history lead the first delivery asks for a page,
    // whose skeleton lands above them while the fetch is pending, and the pass compensates its height.
    const out = await movesAfterReturn(view, 500, () => {
      scroll.setLoadMore(() => undefined, true);
    });

    expect({
      skeleton: document.getElementById("load-more-skeleton")?.getBoundingClientRect().height,
      ...out,
    }).toEqual({ skeleton: 200, moves: [], state: "reading" });
  });

  it("keeps the reader's text still when a row above them grows in the first frame after return", async () => {
    const view = rowsView();
    const row = view.children[2] as HTMLElement;
    const out = await movesAfterReturn(view, 1500, () => {
      requestAnimationFrame(() => {
        row.style.height = "300px";
      });
    });

    expect({ grown: row.getBoundingClientRect().height, ...out }).toEqual({
      grown: 300,
      moves: [],
      state: "reading",
    });
  });

  /** A full-window layer over the transcript, as the phone's side menu stands while it slides shut. */
  function coverScroller(): HTMLElement {
    const cover = document.createElement("div");
    cover.dataset["testCover"] = "";
    cover.style.cssText = "position:fixed;inset:0;z-index:100;";
    document.body.appendChild(cover);
    return cover;
  }
  afterEach(() => {
    for (const cover of document.querySelectorAll("[data-test-cover]")) {
      cover.remove();
    }
  });

  /** Stamped 100px entries of `turn` (the first `first` px tall), as the renderer marks a turn body's rows. */
  function entries(turn: string, first = 100): HTMLElement[] {
    return Array.from({ length: 10 }, (_, seq) => {
      const e = document.createElement("div");
      e.dataset["entryTurn"] = turn;
      e.dataset["entrySeq"] = String(seq);
      e.style.height = `${String(seq === 0 ? first : 100)}px`;
      return e;
    });
  }

  /** A view of four turn cards, each a body of ten stamped entries. */
  function cardsView(): HTMLElement {
    const view = document.createElement("div");
    for (let t = 0; t < 4; t++) {
      const card = document.createElement("div");
      const body = document.createElement("div");
      body.append(...entries(`t${String(t)}`));
      card.appendChild(body);
      view.appendChild(card);
    }
    messagesEl.appendChild(view);
    return view;
  }

  it("keeps the reader's entry still when the turn holding it is rebuilt taller above it as the view returns", async () => {
    const view = cardsView();
    const body = view.children[1]!.firstElementChild!;
    const entry = (): Element => view.querySelector('[data-entry-turn="t1"][data-entry-seq="6"]')!;
    const out = await movesAfterReturn(
      view,
      1500,
      () => {
        // A turn still open at park is rebuilt with what landed meanwhile: new elements for the same entries.
        body.replaceChildren(...entries("t1", 300));
      },
      { ref: entry },
    );

    expect(out).toEqual({ moves: [], state: "reading" });
  });

  it("keeps the reader's text still when the row on the reading line is replaced as the view returns and a page's skeleton lands above", async () => {
    const view = rowsView();
    const rows = [...view.children];
    skeleton.px = 200;
    const out = await movesAfterReturn(
      view,
      500,
      () => {
        rows[6]!.replaceWith(rows[6]!.cloneNode(true));
        scroll.setLoadMore(() => undefined, true);
      },
      { ref: () => rows[11]! },
    );

    expect({ replaced: !rows[6]!.isConnected, ...out }).toEqual({
      replaced: true,
      moves: [],
      state: "reading",
    });
  });

  it("keeps the reader's text still when the row on the reading line is deleted as the view returns and a page's skeleton lands above", async () => {
    const view = rowsView();
    const rows = [...view.children];
    skeleton.px = 200;
    // The text above the deleted row stays put; what followed it closes the gap.
    const out = await movesAfterReturn(
      view,
      500,
      () => {
        rows[6]!.remove();
        scroll.setLoadMore(() => undefined, true);
      },
      { ref: () => rows[5]! },
    );

    expect(out).toEqual({ moves: [], state: "reading" });
  });

  /** `n` 100px paragraphs. */
  function paras(n: number): HTMLElement[] {
    return Array.from({ length: n }, () => {
      const p = document.createElement("p");
      p.style.cssText = "height:100px;margin:0;";
      return p;
    });
  }

  /** A view of three cards: ten stamped entries, then one stamped entry 1500px tall (a 100px box, then fourteen
   *  paragraphs) holding the reading line, then ten more. */
  function tallEntryView(): { view: HTMLElement; inner: HTMLElement; above: HTMLElement } {
    const view = document.createElement("div");
    const c0 = document.createElement("div");
    c0.append(...entries("t0"));
    const e = document.createElement("div");
    e.dataset["entryTurn"] = "t1";
    e.dataset["entrySeq"] = "0";
    const inner = document.createElement("div");
    inner.style.height = "100px";
    e.append(inner, ...paras(14));
    const c1 = document.createElement("div");
    c1.appendChild(e);
    const c2 = document.createElement("div");
    c2.append(...entries("t2"));
    view.append(c0, c1, c2);
    messagesEl.appendChild(view);
    return { view, inner, above: c0.children[9] as HTMLElement };
  }

  it.each(["inside the reader's entry", "in an entry above"])(
    "keeps the reader's text still when a box %s grows above the line in the first frame after return",
    async (where) => {
      const { view, inner, above } = tallEntryView();
      const grow = where === "in an entry above" ? above : inner;
      const out = await movesAfterReturn(view, 1800, () => {
        requestAnimationFrame(() => {
          grow.style.height = "300px";
        });
      });

      expect({ grown: grow.getBoundingClientRect().height, ...out }).toEqual({
        grown: 300,
        moves: [],
        state: "reading",
      });
    },
  );

  /** The row the renderer mounts for turn t1's entry `say-9`: stamped open while it streams (no `seq` yet), or sealed
   *  at seq 6 once it has; twelve 100px paragraphs inside its bubble either way. */
  function sayRow(sealed: boolean): HTMLElement {
    const row = document.createElement("div");
    if (sealed) {
      row.dataset["entryTurn"] = "t1";
      row.dataset["entrySeq"] = "6";
      row.dataset["entryId"] = "say-9";
    } else {
      row.dataset["openTurn"] = "t1";
      row.dataset["openId"] = "say-9";
    }
    const bubble = document.createElement("div");
    bubble.append(...paras(12));
    row.appendChild(bubble);
    return row;
  }

  it.each(["still open", "sealed"])(
    "keeps the reader's text still when the open entry they read is rebuilt %s below an arrival as the view returns",
    async (rebuilt) => {
      const view = document.createElement("div");
      const c0 = document.createElement("div");
      c0.append(...entries("t0"));
      const body = document.createElement("div");
      body.append(...entries("t1").slice(0, 5), sayRow(false));
      const c1 = document.createElement("div");
      c1.appendChild(body);
      view.append(c0, c1);
      messagesEl.appendChild(view);
      const row = (): Element => body.lastElementChild!;
      const out = await movesAfterReturn(
        view,
        1800,
        () => {
          // An open turn is rebuilt on return with what arrived meanwhile: an interjection above the open row, and a
          // new element for the row itself.
          const arrival = document.createElement("div");
          arrival.dataset["entryTurn"] = "t1";
          arrival.dataset["entrySeq"] = "5";
          arrival.style.height = "200px";
          row().replaceWith(arrival, sayRow(rebuilt === "sealed"));
        },
        { ref: () => row().firstElementChild!.children[5]! },
      );

      expect(out).toEqual({ moves: [], state: "reading" });
    },
  );

  /** `sayRow(false)` with each 100px paragraph's text streamed in `pieces` inline pieces stacked down it, as the
   *  renderer's chunk spans split a paragraph by delta timing, then a `blockPx` block (a display equation). */
  function chunkedSayRow(pieces: number, blockPx = 0): HTMLElement {
    const row = sayRow(false);
    for (const p of row.querySelectorAll("p")) {
      p.style.fontSize = "0";
      p.append(
        ...Array.from({ length: pieces }, () => {
          const span = document.createElement("span");
          span.style.cssText = `display:inline-block;vertical-align:top;width:100%;height:${String((100 - blockPx) / pieces)}px;`;
          return span;
        }),
      );
      if (blockPx > 0) {
        const block = document.createElement("div");
        block.style.height = `${String(blockPx)}px`;
        p.appendChild(block);
      }
    }
    return row;
  }

  it("keeps the reader's text still when the paragraph they read is rebuilt from a different number of streamed pieces as the view returns", async () => {
    const view = document.createElement("div");
    const c0 = document.createElement("div");
    c0.append(...entries("t0"));
    const body = document.createElement("div");
    body.append(...entries("t1").slice(0, 5), chunkedSayRow(5));
    const c1 = document.createElement("div");
    c1.appendChild(body);
    view.append(c0, c1);
    messagesEl.appendChild(view);
    const row = (): Element => body.lastElementChild!;
    const out = await movesAfterReturn(
      view,
      1850,
      () => {
        const arrival = document.createElement("div");
        arrival.dataset["entryTurn"] = "t1";
        arrival.dataset["entrySeq"] = "5";
        arrival.style.height = "200px";
        row().replaceWith(arrival, chunkedSayRow(10));
      },
      { ref: () => row().firstElementChild!.children[6]! },
    );

    expect(out).toEqual({ moves: [], state: "reading" });
  });

  it("keeps the reader's text still when the block they read inside a streamed paragraph follows a different number of pieces as the view returns", async () => {
    const view = document.createElement("div");
    const c0 = document.createElement("div");
    c0.append(...entries("t0"));
    const body = document.createElement("div");
    body.append(...entries("t1").slice(0, 5), chunkedSayRow(5, 60));
    const c1 = document.createElement("div");
    c1.appendChild(body);
    view.append(c0, c1);
    messagesEl.appendChild(view);
    const row = (): Element => body.lastElementChild!;
    const out = await movesAfterReturn(
      view,
      1850,
      () => {
        const arrival = document.createElement("div");
        arrival.dataset["entryTurn"] = "t1";
        arrival.dataset["entrySeq"] = "5";
        arrival.style.height = "200px";
        row().replaceWith(arrival, chunkedSayRow(10, 60));
      },
      { ref: () => row().firstElementChild!.children[6]! },
    );

    expect(out).toEqual({ moves: [], state: "reading" });
  });

  it("keeps the reader's text still when a box above the line grows inside the entry they read while they read it", async () => {
    const wrap = scroll.getScrollEl();
    const { view, inner } = tallEntryView();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    readerScrollTo(wrap, 1800);
    await land(400);
    const box = wrap.getBoundingClientRect();
    const ref = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 3)!;
    const before = ref.getBoundingClientRect().top;
    inner.style.height = "300px";
    await land();

    expect({
      moved: Math.round(ref.getBoundingClientRect().top - before),
      state: scroll.readingState(),
    }).toEqual({ moved: 0, state: "reading" });
  });

  it("keeps the reader's text still when the side menu still covers the transcript as the view returns and a page's skeleton lands above", async () => {
    const view = rowsView();
    const rows = [...view.children];
    skeleton.px = 200;
    let cover: HTMLElement | null = null;
    const out = await movesAfterReturn(
      view,
      500,
      () => {
        requestAnimationFrame(() => {
          cover?.remove();
        });
        scroll.setLoadMore(() => undefined, true);
      },
      {
        ref: () => rows[11]!,
        beforeReturn: () => {
          cover = coverScroller();
        },
      },
    );

    expect(out).toEqual({ moves: [], state: "reading" });
  });

  it("puts the reader's text back where they left it when they leave under the side menu with an older page still loading", async () => {
    const wrap = scroll.getScrollEl();
    const view = rowsView();
    scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
    await land();
    readerScrollTo(wrap, 500);
    await land();
    skeleton.px = 200;
    scroll.setLoadMore(() => undefined, true);
    scroll.noteHistoryEdge(true);
    await land();
    const box = wrap.getBoundingClientRect();
    const ref = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 3)!;
    const left = ref.getBoundingClientRect().top;
    const loading = document.getElementById("load-more-skeleton") !== null;
    const cover = coverScroller();
    const parked = scroll.detach();
    cover.remove();
    view.hidden = true;
    const other = document.createElement("div");
    other.style.cssText = "height:2000px;";
    messagesEl.appendChild(other);
    scroll.attach({ el: other, scrollTop: 0, readingState: "following" });
    await land();
    scroll.detach();
    other.remove();
    view.hidden = false;
    scroll.attach({ el: view, ...parked });
    await land();

    expect({
      loading,
      moved: Math.round(ref.getBoundingClientRect().top - left),
      state: scroll.readingState(),
    }).toEqual({ loading: true, moved: 0, state: "reading" });
  });
});

describe("onContentResize", () => {
  beforeEach(realLayoutReset);

  it("fires from the per-child ResizeObserver and returns a working unregister", async () => {
    realScroller();
    const card = block(200);
    await land();
    const seen = vi.fn();
    const off = scroll.onContentResize(seen);

    card.style.height = "600px";
    await land();
    const fired = seen.mock.calls.length;
    expect(fired).toBeGreaterThan(0);

    off();
    card.style.height = "300px";
    await land();

    expect(seen.mock.calls.length).toBe(fired);
  });
});

describe("onAttach", () => {
  beforeEach(realLayoutReset);

  it("fires when a view takes the scroller, after the position is restored", () => {
    const wrap = realScroller();
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
    const view = document.createElement("div");
    view.className = "transcript-view";
    view.style.cssText = "height:3000px;";
    messagesEl.replaceChildren(view);
    // Read INSIDE the callback: a listener re-measuring the incoming view has to see the restored
    // offset, so firing before the write would hand it the outgoing view's position.
    const seen = vi.fn(() => wrap.scrollTop);
    const off = scroll.onAttach(seen);

    scroll.attach({ el: view, scrollTop: 250, readingState: "reading" });
    expect(seen).toHaveBeenCalledTimes(1);
    expect(seen.mock.results[0]?.value).toBe(250);

    off();
    scroll.attach({ el: view, scrollTop: 100, readingState: "reading" });

    expect(seen).toHaveBeenCalledTimes(1);
  });
});

describe("readingLineOffset", () => {
  it("is a third of the scrollport, from its top", () => {
    const s = fakeScroller({ scrollHeight: 3000, clientHeight: 900, scrollTop: 0 });
    expect(scroll.readingLineOffset()).toBe(300);
    s.clientHeight = 600;
    expect(scroll.readingLineOffset()).toBe(200);
  });
});

describe("the history edge's observer", () => {
  beforeEach(realLayoutReset);

  /** Re-roots the observers on the transcript, which seats the marker `realScroller` emptied it of. */
  async function seated(): Promise<HTMLElement> {
    const wrap = realScroller();
    block(3000);
    scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
    await land();
    return wrap;
  }

  it("asks for the next older page while the reader is still a screenful away from the top", async () => {
    // 400px scrollport: the lead is one and a half of them, 600px.
    const wrap = await seated();
    const load = vi.fn();
    scroll.setLoadMore(load, true);
    wrap.scrollTop = 2000;
    await land(60);
    expect(load).not.toHaveBeenCalled();

    wrap.scrollTop = 500;
    await land(60);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("asks nothing once the server has no older page", async () => {
    const wrap = await seated();
    const load = vi.fn();
    scroll.setLoadMore(load, false);
    wrap.scrollTop = 0;
    await land(60);

    expect(load).not.toHaveBeenCalled();
  });
});

describe("pagination furniture belongs to its own view", () => {
  /** Two sibling transcript views under the scroller, and the scroller attached to the first —
   *  the shape after one chat switch. */
  function twoViews(): { parked: HTMLElement; active: HTMLElement } {
    const wrap = realScroller();
    wrap.style.cssText = "height:400px;overflow-y:auto;position:relative;";
    const parked = document.createElement("div");
    parked.className = "transcript-view";
    const active = document.createElement("div");
    active.className = "transcript-view";
    messagesEl.replaceChildren(parked, active);
    return { parked, active };
  }

  /** The skeleton a load pass in flight leaves in a view. Built by hand because what is under
   *  test is which element a REMOVAL reaches, not how one is mounted. */
  function plantSkeleton(view: HTMLElement): HTMLElement {
    const skel = document.createElement("div");
    skel.id = "load-more-skeleton";
    view.prepend(skel);
    return skel;
  }

  it("abandons only the attached view's load pass, leaving a parked view's skeleton", () => {
    const { parked, active } = twoViews();
    const parkedSkeleton = plantSkeleton(parked);
    scroll.attach({ el: active, scrollTop: 0, readingState: "following" });
    const activeSkeleton = plantSkeleton(active);

    // `resetScrollState` ends in `abandonLoadPass`, and it is the reachable door: a chat switch
    // runs it for the INCOMING view.
    scroll.resetScrollState();

    expect({
      parkedKept: parked.contains(parkedSkeleton),
      activeGone: !active.contains(activeSkeleton),
    }).toEqual({ parkedKept: true, activeGone: true });
  });

  it("moves the history marker with the attached view", () => {
    // One marker serves every chat, as the live edge's does: a parked view's own top is not a history edge.
    const { parked, active } = twoViews();
    scroll.attach({ el: parked, scrollTop: 0, readingState: "following" });
    expect(parked.querySelector(".transcript-top")).not.toBeNull();

    scroll.attach({ el: active, scrollTop: 0, readingState: "following" });

    expect({
      parkedHas: parked.querySelector(".transcript-top") !== null,
      activeHas: active.querySelector(".transcript-top") !== null,
    }).toEqual({ parkedHas: false, activeHas: true });
  });
});

describe("onViewportChange's release", () => {
  beforeEach(realLayoutReset);

  /** ONE viewport change. The scroll listener is what dispatches it, so a plain `scrollTop`
   *  write is the trigger — no gesture is needed and none is wanted. */
  async function viewportChange(wrap: HTMLElement): Promise<void> {
    wrap.scrollTop = wrap.scrollTop === 0 ? 300 : 0;
    await land();
  }

  it("stops delivering to the listener that released, and to no other", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const first = vi.fn();
    const second = vi.fn();
    const offFirst = scroll.onViewportChange(first);
    scroll.onViewportChange(second);

    // The FIRST-registered listener on purpose: at index 0 a `> 0` test finds nothing to remove,
    // which is the off-by-one a `>= 0` boundary exists for.
    offFirst();
    await viewportChange(wrap);

    expect({ first: first.mock.calls.length, second: second.mock.calls.length > 0 }).toEqual({
      first: 0,
      second: true,
    });
  });

  it("takes nothing else down when the same release is called twice", async () => {
    const wrap = realScroller();
    block(3000);
    await land();
    const first = vi.fn();
    const second = vi.fn();
    const offFirst = scroll.onViewportChange(first);
    scroll.onViewportChange(second);

    offFirst();
    offFirst();
    await viewportChange(wrap);

    expect({ first: first.mock.calls.length, second: second.mock.calls.length > 0 }).toEqual({
      first: 0,
      second: true,
    });
  });
});

describe("jumpTo's end-aligned landing", () => {
  beforeEach(realLayoutReset);

  it("parks the reader for a target whose BOTTOM lands short of the live edge", async () => {
    // 3000px of content in the 400px scrollport, so the maximum offset is 2600 and the target's
    // bottom lands 250px short of it.
    const wrap = realScroller();
    block(2550);
    const target = block(200);
    block(250);
    await land();
    expect(wrap.scrollHeight - wrap.clientHeight).toBe(2600);

    scroll.jumpTo(target, { block: "end", behavior: "instant" });

    expect(scroll.readingState()).toBe("reading");
  });
});

// A jump's own smooth flight belongs to the owner: no reader gesture is published across it, reader input ends it,
// pagination waits for it, and a bound ends a flight whose scroll never ends.
describe("the owner's own flight", () => {
  beforeEach(realLayoutReset);

  async function flightFromTheEnd(): Promise<{ wrap: HTMLElement; landed: Promise<boolean> }> {
    const wrap = realScroller();
    block(4000);
    await land();
    scroll.scrollToBottom();
    await land();
    expect(wrap.scrollTop, "the premise: at the end").toBe(3600);
    const landed = scroll.jumpToOffset(1000, "smooth");
    await flightMoves(wrap);
    return { wrap, landed };
  }

  it("publishes no reader gesture across the flight, and lands Reading", async () => {
    const { wrap, landed } = await flightFromTheEnd();
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);

    const result = await landed;
    await land();
    off();

    expect({ result, seen: seen.mock.calls.length, ...landing(wrap) }).toEqual({
      result: true,
      seen: 0,
      scrollTop: 1000,
      state: "reading",
      hidden: false,
    });
  });

  it("hands the scroller to the reader the moment they touch it", async () => {
    const seen = vi.fn();
    const off = scroll.onReaderGesture(seen);
    const wrap = realScroller();
    block(4000);
    await land();
    scroll.scrollToBottom();
    await land();
    expect(wrap.scrollTop, "the premise: at the end").toBe(3600);
    // The reader's wheel in the flight's first moving frame.
    wrap.addEventListener(
      "scroll",
      () => {
        wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -3, deltaMode: 1 }));
      },
      { once: true },
    );
    const landed = scroll.jumpToOffset(1000, "smooth");
    const result = await landed;
    wrap.scrollTop = wrap.scrollTop - 30;
    await land();
    off();

    expect({ result, published: seen.mock.calls.length > 0 }).toEqual({
      result: false,
      published: true,
    });
  });

  it("holds the history edge's page back until the flight ends, then asks for it", async () => {
    const load = vi.fn();
    const { landed } = await flightFromTheEnd();
    scroll.setLoadMore(load, true);
    scroll.noteHistoryEdge(true);
    const during = load.mock.calls.length;

    await landed;
    await land();
    scroll.setLoadMore(null, false);
    document.getElementById("load-more-skeleton")?.remove();

    expect({ during, after: load.mock.calls.length }).toEqual({ during: 0, after: 1 });
  });

  it("ends a flight whose scroll never ends at its bound", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setTimeout", "clearTimeout"] });
    try {
      const wrap = realScroller();
      block(4000);
      // A flight to where the scroller already stands with a smooth write the engine ignores.
      wrap.scrollTo = (() => undefined) as typeof wrap.scrollTo;
      await frames(2);
      let result: boolean | null = null;
      void scroll.jumpToOffset(1000, "smooth").then((r) => {
        result = r;
      });
      vi.advanceTimersByTime(1499);
      await taskHop();
      const before = result;
      vi.advanceTimersByTime(2);
      await taskHop();
      expect({ before, after: result }).toEqual({ before: null, after: true });
    } finally {
      vi.useFakeTimers({ toFake: ["Date"] });
      Reflect.deleteProperty(scroll.getScrollEl(), "scrollTo");
    }
  });
});
