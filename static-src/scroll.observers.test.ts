// The observers `init()` wires, and the module-scope boot.
import type * as Dom from "./dom.js";
import { describe, it, expect, afterEach, vi } from "vitest";
import type { MockInstance } from "vitest";
import type * as ScrollModule from "./scroll.js";
import { framesDrained } from "./__test-helpers__/frame-queue.js";

/** Cache-buster for the re-imports below. */
let bootSeq = 0;

// The module builds its singleton against $.messages / $.messagesWrap at import and reads
// $.scrollBottom in init; ids are created on demand so a cleared body yields a fresh element set
// for the next import.
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
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
vi.mock("./skeleton.js", () => ({ loadMoreSkeleton: () => document.createElement("div") }));

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = [];
  readonly targets = new Set<Element>();
  readonly observeCalls: Element[] = [];
  readonly unobserveCalls: Element[] = [];
  private readonly cb: (entries: ResizeObserverEntry[]) => void;
  constructor(cb: (entries: ResizeObserverEntry[]) => void) {
    this.cb = cb;
    FakeResizeObserver.instances.push(this);
  }
  observe(el: Element): void {
    this.targets.add(el);
    this.observeCalls.push(el);
  }
  unobserve(el: Element): void {
    this.targets.delete(el);
    this.unobserveCalls.push(el);
  }
  disconnect(): void {
    this.targets.clear();
  }
  /** What no real box change will do here: report that every watched box changed, at its current size. */
  fire(): void {
    this.cb(
      [...this.targets].map((target) => {
        const rect = target.getBoundingClientRect();
        return {
          target,
          contentRect: rect,
          borderBoxSize: [{ blockSize: rect.height, inlineSize: rect.width }],
          contentBoxSize: [{ blockSize: rect.height, inlineSize: rect.width }],
          devicePixelContentBoxSize: [],
        } as unknown as ResizeObserverEntry;
      }),
    );
  }
  observedCount(el: Element): number {
    return this.observeCalls.filter((e) => e === el).length;
  }
}

/** Faked for the same reason the ResizeObserver is: the real one reports a THRESHOLD CROSSING
 *  computed from real boxes, and this harness has no overflow and no layout, so the platform would
 *  never deliver an entry for the sentinel. */
class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  readonly targets = new Set<Element>();
  readonly options: IntersectionObserverInit | undefined;
  private readonly cb: IntersectionObserverCallback;
  constructor(cb: IntersectionObserverCallback, options?: IntersectionObserverInit) {
    this.cb = cb;
    this.options = options;
    FakeIntersectionObserver.instances.push(this);
  }
  observe(el: Element): void {
    this.targets.add(el);
  }
  unobserve(el: Element): void {
    this.targets.delete(el);
  }
  disconnect(): void {
    this.targets.clear();
  }
  fire(entries: readonly { isIntersecting: boolean }[]): void {
    this.cb(
      entries as unknown as IntersectionObserverEntry[],
      this as unknown as IntersectionObserver,
    );
  }
}

/** Inside the test body, never at module scope: `unstubGlobals` is on, so a stub installed at
 *  collection time is restored before the first test runs. */
function stubResizeObserver(): void {
  FakeResizeObserver.instances.length = 0;
  FakeIntersectionObserver.instances.length = 0;
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
}

/** The scroller's boxes are faked with writable properties — the same level scroll.test.ts
 *  chose. Real layout is not the subject here: these tests are arithmetic over three numbers,
 *  and an element sized by the harness rather than by the app's stylesheet would measure
 *  whatever the harness happened to produce. */
function fakeGeometry(
  el: HTMLElement,
  init: {
    scrollHeight: number;
    clientHeight: number;
    scrollTop: number;
    offsetWidth?: number;
    clientWidth?: number;
  },
): {
  scrollHeight: number;
  clientHeight: number;
  scrollTop: number;
  offsetWidth: number;
  clientWidth: number;
} {
  const state = { offsetWidth: 0, clientWidth: 0, ...init };
  for (const key of ["scrollHeight", "clientHeight", "offsetWidth", "clientWidth"] as const) {
    Object.defineProperty(el, key, { configurable: true, get: () => state[key] });
  }
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => state.scrollTop,
    set: (v: number) => {
      state.scrollTop = v;
    },
  });
  // `scrollTo` and `scrollBy` have to be shadowed alongside the metrics, or the faked scrollTop is bypassed
  // entirely: production reaches the live edge through `scrollEl.scrollTo({top, behavior})`, and in
  // a real browser that is the platform's own method writing a real scroll position — which stays 0
  // on an element with no overflow, so every pin assertion read 0.
  el.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
    const top = typeof arg === "number" ? y : arg?.top;
    if (top !== undefined) {
      state.scrollTop = top;
    }
  }) as typeof el.scrollTo;
  el.scrollBy = ((arg?: number | ScrollToOptions, y?: number): void => {
    const d = typeof arg === "number" ? y : arg?.top;
    if (d !== undefined) {
      state.scrollTop += d;
    }
  }) as typeof el.scrollBy;
  return state;
}

/** The reader's own move to where the fixture has put the scroller: from 50px the other side (below it, or above
 *  it at the end), the input that opens the gesture, then the `scroll` the browser delivers. The owner reads the
 *  state from where the scroller went. */
function readerScroll(el: HTMLElement): void {
  const to = el.scrollTop;
  const max = el.scrollHeight - el.clientHeight;
  const atEnd = to >= max - 1;
  el.scrollTop = atEnd ? Math.max(0, to - 50) : Math.min(max, to + 50);
  el.dispatchEvent(new WheelEvent("wheel", { deltaY: atEnd ? 1 : -1 }));
  el.scrollTop = to;
  el.dispatchEvent(new Event("scroll"));
}

/** The reader's scroll plus the history-edge observer's report, which fake geometry cannot produce: the edge counts
 *  as reached within 100px of the top. */
function readerAtHistoryEdge(h: Harness): void {
  readerScroll(h.scrollEl);
  h.scroll.noteHistoryEdge(h.scrollEl.scrollTop < 100);
}

/** Two frames, for the controller's frame-queued pin and re-assert, then a task to resume past
 *  the last one. That schedule is what a positive pins; an absence reads after `drained()`. */
async function settle(): Promise<void> {
  for (let i = 0; i < 2; i++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
  await new Promise((r) => setTimeout(r, 0));
}

/** Until no frame callback is left queued, so a write the controller still owes is seen. */
async function drained(): Promise<void> {
  await framesDrained("the controller's queued frames");
}

/** Drain the same queues without depending on `setTimeout`, which a fake-timer test has
 *  replaced. Animation frames run on the real clock captured at its own import, so they still
 *  land. */
async function settleFrames(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
}

interface Harness {
  scroll: typeof ScrollModule;
  /** The CONTENT observer: the view's children, and the reads its callback makes. It touches no
   *  document style. */
  ro: FakeResizeObserver;
  /** The GUTTER observer: the scroller's own box, and the one write that reaches a shared
   *  ancestor. */
  gutter: FakeResizeObserver;
  /** The near-view observer, which lays skippable boxes out ahead of the scrollport. */
  near: FakeIntersectionObserver;
  messagesEl: HTMLElement;
  scrollEl: HTMLElement;
  /** Every `addEventListener` on the scroller since just before the module was imported. */
  listeners: MockInstance<typeof EventTarget.prototype.addEventListener>;
  existingRow: HTMLElement | null;
}

/** A module instance whose observers are the fakes above, against a DOM no previous instance
 *  holds a reference to. */
async function freshModule(opts: { withExistingRow?: boolean } = {}): Promise<Harness> {
  stubResizeObserver();
  document.body.replaceChildren();
  document.documentElement.style.removeProperty("--scrollbar-w");
  // Built before the import so the scroller can be spied on: the mocked registry hands out whatever
  // is already in the document under that id.
  const messages = document.createElement("div");
  messages.id = "messages";
  const wrap = document.createElement("div");
  wrap.id = "messagesWrap";
  let existingRow: HTMLElement | null = null;
  if (opts.withExistingRow === true) {
    // A transcript that already has content, which is the ordinary case on a reload of an open
    // chat.
    existingRow = document.createElement("div");
    messages.appendChild(existingRow);
  }
  document.body.append(messages, wrap);
  const listeners = vi.spyOn(wrap, "addEventListener");
  vi.resetModules();
  bootSeq++;
  const scroll = (await import(
    /* @vite-ignore */ `./scroll.ts?boot=${bootSeq}`
  )) as typeof ScrollModule;
  const scrollEl = scroll.getScrollEl();
  // TWO ResizeObservers, in construction order: the content one, then the gutter one. Attributed by
  // construction site rather than by target, so a regression that moves a callback between them
  // names the observer it moved to. TWO IntersectionObservers likewise: the history edge's (built with
  // the near lead), then the near-view one.
  expect([
    FakeResizeObserver.instances.length,
    FakeIntersectionObserver.instances.length,
    scrollEl,
  ]).toEqual([2, 2, wrap]);
  return {
    scroll,
    ro: FakeResizeObserver.instances[0]!,
    gutter: FakeResizeObserver.instances[1]!,
    near: FakeIntersectionObserver.instances[1]!,
    messagesEl: messages,
    scrollEl,
    listeners,
    existingRow,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

// The GUTTER observer: the scroller's own box, and the one write that reaches a shared ancestor. It
// is a second observer because css/13-messages.css reads `--scrollbar-w` in this scroller's own
// `padding-inline`.
describe("the gutter observer", () => {
  it("watches the scroller alone", async () => {
    // The box whose reserved gutter is being published, and nothing else: an observation carrying
    // every turn card as well is what made the write undeliverable.
    const h = await freshModule();
    expect([h.gutter.targets.has(h.scrollEl), h.gutter.targets.size]).toEqual([true, 1]);
  });

  it("keeps the write out of the content observer", async () => {
    // The content observer reads and follows; it may not invalidate style for the whole document,
    // or every card it also carries is re-laid-out mid-frame.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    h.ro.fire();
    await drained();
    expect([
      h.ro.targets.has(h.scrollEl),
      document.documentElement.style.getPropertyValue("--scrollbar-w"),
    ]).toEqual([false, "0px"]);
  });

  it("re-measures the reserved gutter when the scroller's box changes", async () => {
    // The width the transcript's inline-END inset gives back. It is read off the real element, so a
    // box that changes between two frames has to be read again; a window listener would read the
    // pre-relayout value.
    const h = await freshModule();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    h.gutter.fire();
    await settle();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("15px");
  });

  it("writes nothing inside the resize delivery itself", async () => {
    // A write landing in the callback produces a new observation of a box
    // this loop has already delivered, which Chromium reports as an undelivered notification. A
    // frame later it is deliverable.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    h.gutter.fire();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
    await settle();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("15px");
  });

  it("writes the gutter once across a resize storm", async () => {
    // Same width twice must cost one style invalidation, not two: a browser-zoom or
    // overlay-scrollbar change moves this box repeatedly and every write invalidates the whole
    // document's style. The single frame slot is the first saving; the change-guard is the second.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    const setProperty = vi.spyOn(document.documentElement.style, "setProperty");
    h.gutter.fire();
    h.gutter.fire();
    h.gutter.fire();
    await settle();
    expect(setProperty.mock.calls.filter((c) => c[0] === "--scrollbar-w")).toHaveLength(1);
    await drained();
    expect(setProperty.mock.calls.filter((c) => c[0] === "--scrollbar-w")).toHaveLength(1);
  });

  it("leaves no scheduled write behind when the controller is reset", async () => {
    // A parked or disposed view may not write document style a frame after it stopped being the
    // transcript.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, {
      scrollHeight: 500,
      clientHeight: 500,
      scrollTop: 0,
      offsetWidth: 1000,
      clientWidth: 985,
    });
    h.gutter.fire();
    h.scroll.resetScrollState();
    await drained();
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
  });
});

// The CONTENT observer: the view's children, the reads its callback makes, and the follow write. It
// writes no document style at all.
describe("the content resize observer", () => {
  it("does not release a reader parked near the end when content grows below them", async () => {
    // No size change releases Reading.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    h.scrollEl.dispatchEvent(new Event("scroll"));
    h.scrollEl.dispatchEvent(new WheelEvent("wheel", { deltaY: -30 }));
    g.scrollTop = 1470;
    h.scrollEl.dispatchEvent(new Event("scroll"));
    expect(h.scroll.readingState()).toBe("reading");

    g.scrollHeight = 2020;
    now.mockReturnValue(1400);
    h.ro.fire();
    await settleFrames();

    expect([h.scroll.readingState(), g.scrollTop]).toEqual(["reading", 1470]);
  });

  it("leaves Reading alone while a gesture is still in flight", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1000 });
    vi.spyOn(Date, "now").mockReturnValue(1000);
    readerScroll(h.scrollEl);
    g.scrollHeight = 1500;
    h.ro.fire();
    // Waited out, or the assertion would be satisfied by the deferral rather than by the gesture.
    await settleFrames();
    expect(h.scroll.readingState()).toBe("reading");
  });

  it("never declares the reader Reading from a size change", async () => {
    // Following holds the ANCHOR, not the document bottom, so tall evidence rendering below it
    // legitimately leaves the scroller hundreds of pixels from the end: a demotion here would stop
    // following mid-turn.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 600, clientHeight: 500, scrollTop: 100 });
    expect(h.scroll.readingState()).toBe("following");
    g.scrollHeight = 4000;
    vi.spyOn(Date, "now").mockReturnValue(1000);
    h.ro.fire();
    await settleFrames();
    expect(h.scroll.readingState()).toBe("following");
  });

  it("puts a Following reader back on the live edge when the box shrinks under them", async () => {
    // The composer growing or the shell panel opening leaves the reader Following but no longer AT
    // the edge, and no DOM mutation says so.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    h.ro.fire();
    await settle();
    expect(g.scrollTop).toBe(1500);
  });

  it("does not release a parked reader when content grows before the deferred apply", async () => {
    // No size change releases Reading: here the transcript appends below a reader parked on the end.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    h.scroll.pressHold();
    now.mockReturnValue(1400);
    h.ro.fire();

    g.scrollHeight = 2500;
    await settleFrames();
    expect([h.scroll.readingState(), h.scroll.scrollableBy()]).toEqual(["reading", 2000]);
  });

  it("does not release a parked reader who has scrolled away, however long the frame took", async () => {
    // The reader's own scroll away keeps them Reading, however long the frame took (a throttled one is ~1016ms).
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1500 });
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    h.scroll.pressHold();
    now.mockReturnValue(1400);
    h.ro.fire();

    now.mockReturnValue(1500);
    g.scrollTop = 400;
    readerScroll(h.scrollEl);
    // Past 1500 + 300, so `readerInControl()` is false again by the time the apply runs and the
    // geometry is the only thing left refusing.
    now.mockReturnValue(2600);
    await settleFrames();
    expect(h.scroll.readingState()).toBe("reading");
  });
});

// What `init()` wires.
describe("what init wires", () => {
  it("registers the scroll listener as passive", async () => {
    // The transcript is the app's hottest scroller: a non-passive listener makes the browser wait
    // for this handler before it may scroll. Nothing in the harness models that, so the
    // registration is the observable.
    const h = await freshModule();
    const scrollRegistrations = h.listeners.mock.calls.filter((c) => c[0] === "scroll");
    expect(scrollRegistrations.map((c) => c[2])).toEqual([{ passive: true }]);
  });

  it("watches the rows the transcript already had", async () => {
    const h = await freshModule({ withExistingRow: true });
    expect(h.ro.targets.has(h.existingRow!)).toBe(true);
  });

  it("wires the End key on the document, not on the transcript", async () => {
    // The keyboard half of the resume control has to work while the focus is anywhere that is not a
    // text field, which is why it is registered on the document.
    const h = await freshModule();
    fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    h.scroll.pressHold();
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(h.scroll.readingState()).toBe("following");
  });
});

// The transcript's mutation observer. Streaming does not append rows — it grows the text inside the
// row that is already there — so the observer has to watch the whole subtree and the character
// data, not just the child list.
describe("the transcript's mutation observer", () => {
  it("scrolls for a change nested inside a row", async () => {
    const h = await freshModule();
    const row = document.createElement("div");
    h.messagesEl.appendChild(row);
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    await settle();
    // The content grows past the end with no size report the fake could send: only the mutation can queue the write.
    g.scrollHeight = 2600;
    row.appendChild(document.createElement("span"));
    await settle();
    expect(g.scrollTop).toBe(2100);
  });

  it("scrolls when a row's text grows", async () => {
    // The streaming case: a chunk extends an existing text node and no element is added or removed
    // anywhere.
    const h = await freshModule();
    const row = document.createElement("div");
    const text = document.createTextNode("half a sen");
    row.appendChild(text);
    h.messagesEl.appendChild(row);
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    await settle();
    g.scrollHeight = 2600;
    text.data += "tence";
    await settle();
    expect(g.scrollTop).toBe(2100);
  });
});

// The mutation path's LICENCE TO MEASURE, which it does not have.
describe("what the mutation callback may read", () => {
  /** Count reads of the scroller's three layout metrics while `run` executes. */
  function countMetricReads(el: HTMLElement): { reads: () => number; stop: () => void } {
    let n = 0;
    const own = ["scrollHeight", "clientHeight", "scrollTop"] as const;
    const saved = own.map((k) => Object.getOwnPropertyDescriptor(el, k));
    for (const key of own) {
      const desc = Object.getOwnPropertyDescriptor(el, key);
      const get = desc?.get;
      if (desc === undefined || get === undefined) {
        throw new Error(`fakeGeometry must have installed a ${key} getter first`);
      }
      const set = desc.set;
      Object.defineProperty(el, key, {
        configurable: true,
        get: () => {
          n++;
          return get.call(el);
        },
        ...(set === undefined ? {} : { set: set.bind(el) }),
      });
    }
    return {
      reads: () => n,
      stop: () => {
        for (const [i, key] of own.entries()) {
          const desc = saved[i];
          if (desc !== undefined) {
            Object.defineProperty(el, key, desc);
          }
        }
      },
    };
  }

  it("reads no scroll metric while the reader is Reading", async () => {
    const h = await freshModule();
    const row = document.createElement("div");
    h.messagesEl.appendChild(row);
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 1000 });
    vi.spyOn(Date, "now").mockReturnValue(1000);
    // A real gesture parks the reader — and publishes the edge state, which is what the callback
    // below is entitled to consume.
    readerScroll(h.scrollEl);
    expect(h.scroll.readingState()).toBe("reading");
    await settle();

    const counter = countMetricReads(h.scrollEl);
    // The streaming shape: a chunk extends a text node inside a resident row.
    row.appendChild(document.createTextNode("a chunk"));
    await settle();
    const reads = counter.reads();
    counter.stop();
    expect(reads).toBe(0);
    // Still Reading, so the published value was consumed rather than ignored.
    expect([h.scroll.readingState(), g.scrollTop]).toEqual(["reading", 1000]);
  });
});

describe("the near-view observer", () => {
  /** A `.tool-call` box inside a row, the depth the transcript nests one at. */
  function rowWithBox(host: HTMLElement): { row: HTMLElement; box: HTMLElement } {
    const row = document.createElement("div");
    const box = document.createElement("div");
    box.className = "tool-call";
    row.appendChild(box);
    host.appendChild(row);
    return { row, box };
  }

  it("releases the outgoing view's boxes on detach and watches only the incoming view's", async () => {
    // A box still observed after its view is parked or evicted keeps that transcript alive.
    const h = await freshModule();
    const outgoing = rowWithBox(h.messagesEl).box;
    await settle();
    expect(h.near.targets.has(outgoing)).toBe(true);

    h.scroll.detach();
    expect(h.near.targets.size).toBe(0);

    const incoming = document.createElement("div");
    const kept = rowWithBox(incoming).box;
    h.messagesEl.appendChild(incoming);
    h.scroll.attach({ el: incoming, scrollTop: 0, readingState: "following" });
    h.scroll.applyPendingRestore();
    expect([h.near.targets.size, h.near.targets.has(kept)]).toEqual([1, true]);
  });

  it("stops watching a box whose row leaves the transcript, and watches it again when it returns", async () => {
    const h = await freshModule();
    const { row, box } = rowWithBox(h.messagesEl);
    await settle();
    row.remove();
    await drained();
    const gone = h.near.targets.has(box);
    h.messagesEl.appendChild(row);
    await settle();

    expect({ gone, back: h.near.targets.has(box) }).toEqual({ gone: false, back: true });
  });
});

// The per-child observers. A child is watched because a code block expanding or an image loading
// changes ITS box without changing the scroller's, and the bookkeeping is what stops the
// registration set drifting from the transcript.
describe("the per-child resize observers", () => {
  it("observes each child of the transcript as it arrives", async () => {
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    expect(h.ro.targets.has(a)).toBe(true);
  });

  it("stops observing a child that leaves", async () => {
    // Not tidiness: an observer holding a removed turn keeps it alive for as long as the chat is
    // open.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    a.remove();
    await settle();
    expect([h.ro.targets.has(a), h.ro.unobserveCalls.includes(a)]).toEqual([false, true]);
  });

  it("keeps observing the children that stayed", async () => {
    const h = await freshModule();
    const a = document.createElement("div");
    const b = document.createElement("div");
    h.messagesEl.append(a, b);
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    expect([h.ro.targets.has(a), h.ro.targets.has(b)]).toEqual([true, true]);
  });

  it("does not re-observe a child it is already watching", async () => {
    // `observe()` on a live target re-delivers an entry for it, so re-registering every resident
    // turn on every streamed chunk is a callback storm, not a no-op.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await settle();
    h.messagesEl.appendChild(document.createElement("div"));
    await drained();
    expect(h.ro.observedCount(a)).toBe(1);
  });

  it("observes a child that comes back", async () => {
    // Reconciliation can move a row out and put the same node back; the bookkeeping has to forget
    // it in between or it is never watched again.
    const h = await freshModule();
    const a = document.createElement("div");
    h.messagesEl.appendChild(a);
    await settle();
    a.remove();
    await settle();
    h.messagesEl.appendChild(a);
    await settle();
    expect(h.ro.targets.has(a)).toBe(true);
  });
});

// One scroll per frame. Each chunk's small delta is what makes the motion read as continuous; two
// writes in one frame are the stutter the module's header comment is about.
describe("the auto-scroll frame guard", () => {
  it("coalesces two triggers in the same frame into one scroll", async () => {
    const h = await freshModule();
    fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const scrollTo = vi.spyOn(h.scrollEl, "scrollTo");
    h.ro.fire();
    h.messagesEl.appendChild(document.createElement("div"));
    h.ro.fire();
    await settle();
    expect(scrollTo).toHaveBeenCalledTimes(1);
    await drained();
    expect(scrollTo).toHaveBeenCalledTimes(1);
  });

  it("scrolls again for the next frame's trigger", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 2000, clientHeight: 500, scrollTop: 0 });
    const scrollTo = vi.spyOn(h.scrollEl, "scrollTo");
    h.ro.fire();
    await settle();
    // A trigger that moves the end; one that does not is compared away and writes nothing.
    g.scrollHeight = 2400;
    h.ro.fire();
    await settle();
    expect(scrollTo).toHaveBeenCalledTimes(2);
  });
});

// Pagination's furniture and its safety net. The load is asynchronous — the skeleton's
// disappearance is what marks it complete — so both the completion path and the timeout that has to
// exist because a fetch can simply never land are reachable only from here.
describe("pagination's skeleton", () => {
  /** Wire a fetcher and park the reader at the top so the scroll listener asks for a page. */
  function startLoad(h: Harness, load: () => void): void {
    h.scroll.setLoadMore(load, true);
    readerAtHistoryEdge(h);
  }

  it("prepends the skeleton on a second pass too", async () => {
    // Each pass places its own skeleton above the oldest turn, so a reader waiting for a second
    // older page sees it as they saw the first.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    startLoad(h, () => undefined);
    document.getElementById("load-more-skeleton")?.remove();
    await settle();
    g.scrollTop = 0;
    readerAtHistoryEdge(h);
    expect(h.messagesEl.firstElementChild?.id).toBe("load-more-skeleton");
  });
});

describe("pagination's safety timeout", () => {
  /** The load-more window is 15s; only the module's own timer is faked, so the real observers
   *  and frames keep running. */
  function useLoadTimeout(): void {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  }

  it("gives up on a fetch that never lands and lets the reader retry", async () => {
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn();
    useLoadTimeout();
    h.scroll.setLoadMore(load, true);
    readerAtHistoryEdge(h);
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    vi.advanceTimersByTime(15_000);
    await settleFrames();
    // The furniture goes, and the reader is not stuck: the next scroll to the top asks again.
    expect(document.getElementById("load-more-skeleton")).toBeNull();
    g.scrollTop = 0;
    readerAtHistoryEdge(h);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("does not move the reader when it gives up", async () => {
    // The transcript can have grown from streaming while the fetch hung, so the height delta the
    // completion path would apply is real — and applying it here would jump a reader whose page
    // never arrived.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    useLoadTimeout();
    h.scroll.setLoadMore(() => undefined, true);
    readerAtHistoryEdge(h);
    g.scrollHeight = 1500;

    vi.advanceTimersByTime(15_000);
    await settleFrames();
    expect(g.scrollTop).toBe(0);
  });

  it("does not fire after the page has landed", async () => {
    // The timer belongs to ONE load. Left armed, it expires in the middle of a LATER fetch and
    // clears its in-flight flag, which is how two fetches end up in the air at once and a page
    // lands twice.
    const h = await freshModule();
    const g = fakeGeometry(h.scrollEl, { scrollHeight: 1000, clientHeight: 500, scrollTop: 0 });
    const load = vi.fn();
    useLoadTimeout();
    h.scroll.setLoadMore(load, true);
    readerAtHistoryEdge(h);
    document.getElementById("load-more-skeleton")?.remove();
    g.scrollHeight = 1400;
    await settleFrames();

    // One second short of the first load's window, then a second fetch — so the first load's
    // deadline falls INSIDE the second load's flight.
    vi.advanceTimersByTime(14_000);
    g.scrollTop = 0;
    readerAtHistoryEdge(h);
    expect(load).toHaveBeenCalledTimes(2);
    expect(document.getElementById("load-more-skeleton")).not.toBeNull();

    vi.advanceTimersByTime(2_000);
    await settleFrames();
    // The second fetch is still the one in flight: its furniture is untouched and nothing else may
    // be started.
    g.scrollTop = 0;
    readerAtHistoryEdge(h);
    expect([
      document.getElementById("load-more-skeleton") === null,
      load.mock.calls.length,
    ]).toEqual([false, 2]);
  });
});

// The module-scope boot. Importing before the document is ready must not build the controller
// against a transcript that is not in the DOM yet.
describe("the deferred boot", () => {
  afterEach(() => {
    Reflect.deleteProperty(document, "readyState");
  });

  /** Everything freshModule does except reaching into the module afterwards: the point of these
   *  two tests is what the IMPORT alone did. */
  function stageDocument(): void {
    stubResizeObserver();
    document.body.replaceChildren();
    document.documentElement.style.removeProperty("--scrollbar-w");
    const messages = document.createElement("div");
    messages.id = "messages";
    const wrap = document.createElement("div");
    wrap.id = "messagesWrap";
    document.body.append(messages, wrap);
    vi.resetModules();
    bootSeq++;
  }

  it("wires the transcript on import when the document is already parsed", async () => {
    // The ordinary case: the bundle runs after the document has been parsed, and nothing will fire
    // DOMContentLoaded again — so deferring here would leave the transcript unwired until some
    // other module happened to call in.
    expect(document.readyState).toBe("complete");
    stageDocument();
    (await import(/* @vite-ignore */ `./scroll.ts?boot=${bootSeq}`)) as typeof ScrollModule;
    expect(FakeResizeObserver.instances).toHaveLength(2);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
  });

  it("waits for DOMContentLoaded when the document is still loading", async () => {
    Object.defineProperty(document, "readyState", {
      configurable: true,
      get: () => "loading",
    });
    stageDocument();
    (await import(/* @vite-ignore */ `./scroll.ts?boot=${bootSeq}`)) as typeof ScrollModule;
    // Nothing wired: no observers, and the gutter the layout reads is unwritten.
    expect(FakeResizeObserver.instances).toHaveLength(0);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("");

    document.dispatchEvent(new Event("DOMContentLoaded"));
    expect(FakeResizeObserver.instances).toHaveLength(2);
    expect(document.documentElement.style.getPropertyValue("--scrollbar-w")).toBe("0px");
  });
});
