// The owner's corrections: Following lands on the end in the frame a size changes, Reading keeps the text on the
// reading line still, every write lands on the engine's grid, and every owner move re-takes what the reader sees.
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as Skeleton from "./skeleton.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { restoreDeviceScale, setDeviceScale } from "./__test-helpers__/device-scale.js";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import {
  frames,
  platformScroll,
  scene,
  scrollEnds,
  taskHop,
  touchEvent,
  wheel,
} from "./__test-helpers__/scroll-scene.js";

vi.mock("./dom.js", async (importOriginal) => {
  const { domProxy: proxy } = await import("./__test-helpers__/scroll-scene.js");
  return { ...(await importOriginal<typeof Dom>()), $: proxy() };
});
const skeleton = vi.hoisted(() => ({ px: 0 }));
vi.mock("./skeleton.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Skeleton>()),
  loadMoreSkeleton: () => {
    const d = document.createElement("div");
    d.style.cssText = `height:${String(skeleton.px)}px;flex-shrink:0;`;
    return d;
  },
}));

const scroll = await import("./scroll.js");
const { holdTarget } = await import("./scroll-controller.js");
const { messagesEl, land, block, recordWrites, landing, closeGesture, reset, screenTop } =
  scene(scroll);

beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
});
afterAll(() => {
  vi.useRealTimers();
});

const slack = (): number => Math.max(1, 1 / devicePixelRatio);

/** A `ResizeObserver` created after the owner's, on `target`: its callback runs at the end of the owner's delivery,
 *  the state the frame paints. Skips the delivery `observe` makes for itself. */
function deliverySampler(target: Element, read: () => void): ResizeObserver {
  let first = true;
  const ro = new ResizeObserver(() => {
    if (first) {
      first = false;
      return;
    }
    read();
  });
  ro.observe(target);
  return ro;
}

/** Reading at `top` by the reader's own wheel tick, the tick's gesture over. */
async function readAt(wrap: HTMLElement, top: number): Promise<void> {
  wheel(wrap, top < wrap.scrollTop ? -3 : 3);
  platformScroll(wrap, top);
  await land(400);
  expect(scroll.readingState(), "the premise: Reading").toBe("reading");
}

/** The element on the reading line (a third of the way down the scrollport). */
function onReadingLine(wrap: HTMLElement): Element {
  const box = wrap.getBoundingClientRect();
  const hit = document.elementFromPoint(box.left + 20, box.top + wrap.clientHeight / 3);
  if (hit === null) {
    throw new Error("nothing on the reading line");
  }
  return hit;
}

describe("the follow write on fractional geometry", () => {
  afterEach(async () => {
    await restoreDeviceScale();
  });

  it.each([
    [400.25, 1],
    [400.5, 1.5],
    [400.75, 2],
    [400.5, 1],
    [400.25, 2],
  ])(
    "writes at most once per growth at a %fpx scrollport and DPR %f, and never while idle",
    async (port, dpr) => {
      await setDeviceScale(dpr);
      const wrap = await reset();
      wrap.style.height = `${String(port)}px`;
      block(1000);
      block(1979);
      const last = block(20.375);
      await land();
      const gap = (): number =>
        last.getBoundingClientRect().bottom - wrap.getBoundingClientRect().bottom;
      expect(
        { fractional: gap() !== Math.round(gap()), small: Math.abs(gap()) < 1 },
        "the premise: a fractional gap under 1px at the end",
      ).toEqual({ fractional: true, small: true });
      const writes = recordWrites(wrap);
      const tops: number[] = [];
      let sampling = true;
      const sample = (): void => {
        tops.push(wrap.scrollTop);
        if (sampling) {
          requestAnimationFrame(sample);
        }
      };
      requestAnimationFrame(sample);

      last.style.height = "20.875px";
      await frames(100);
      const afterFirst = writes.length;
      last.style.height = "21.875px";
      await frames(100);
      const afterSecond = writes.length;
      await frames(100);
      sampling = false;
      const deltas = tops
        .slice(1)
        .map((t, i) => t - (tops[i] ?? t))
        .filter((d) => d !== 0);
      const flips = deltas
        .slice(1)
        .filter((d, i) => Math.sign(d) !== Math.sign(deltas[i] ?? d)).length;

      expect({
        first: afterFirst <= 1,
        second: afterSecond - afterFirst <= 1,
        idle: writes.length - afterSecond,
        flips,
      }).toEqual({ first: true, second: true, idle: 0, flips: 0 });
    },
  );
});

describe("placeholders taking their real size under a follower", () => {
  let css: HTMLStyleElement;
  beforeAll(() => {
    css = mountAppCSS();
  });
  afterAll(() => {
    css.remove();
  });

  it("keeps the end on the scrollport's bottom at the end of every delivery", async () => {
    const wrap = await reset();
    wrap.id = "messages-wrap";
    const view = document.createElement("div");
    view.className = "transcript-view";
    messagesEl.append(view);
    const rows: HTMLElement[] = [];
    for (let i = 0; i < 40; i++) {
      const row = document.createElement("div");
      row.className = "msg-row";
      const body = document.createElement("div");
      body.style.cssText = `height:${String(60 + ((i * 37) % 140))}px;width:100%;`;
      row.append(body);
      rows.push(row);
    }
    const gaps: number[] = [];
    let sampler: ResizeObserver | null = null;
    try {
      scroll.attach({ el: view, scrollTop: 0, readingState: "following" });
      scroll.applyPendingRestore();
      view.append(...rows);
      sampler = new ResizeObserver(() => {
        gaps.push(wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop);
      });
      await taskHop();
      for (const row of rows) {
        sampler.observe(row);
      }
      await land();
      await land();

      expect({
        state: scroll.readingState(),
        off: gaps.filter((g) => Math.abs(g) > slack()),
      }).toEqual({ state: "following", off: [] });
    } finally {
      sampler?.disconnect();
      scroll.detach();
      scroll.attach({ el: messagesEl, scrollTop: 0, readingState: "following" });
      scroll.applyPendingRestore();
      wrap.id = "messagesWrap";
    }
  });
});

describe("an older page landing during a fling", () => {
  beforeEach(() => {
    skeleton.px = 40;
  });
  afterEach(() => {
    skeleton.px = 0;
    scroll.setLoadMore(null, false);
  });

  it("holds the reading-line text in every frame while the page and its skeleton land", async () => {
    const wrap = await reset();
    block(400);
    const held = block(2000);
    block(600);
    await land();
    await readAt(wrap, 200);
    const writes = recordWrites(wrap);
    const start = { screen: screenTop(wrap, held), top: wrap.scrollTop };
    const residuals: number[] = [];
    let sampling = true;
    const sample = (): void => {
      const owner = writes.reduce((sum, w) => sum + w.delta, 0);
      const platform = wrap.scrollTop - start.top - owner;
      residuals.push(Math.round(screenTop(wrap, held) - (start.screen - platform)));
      if (sampling) {
        requestAnimationFrame(sample);
      }
    };
    requestAnimationFrame(sample);
    let landed = false;

    wrap.dispatchEvent(touchEvent("touchstart", wrap, 100));
    for (let i = 1; i <= 4; i++) {
      wrap.dispatchEvent(touchEvent("touchmove", wrap, 100 + 10 * i));
      platformScroll(wrap, 200 - 10 * i);
    }
    wrap.dispatchEvent(touchEvent("touchend", wrap));
    platformScroll(wrap, 0, "smooth");
    scroll.setLoadMore(() => {
      setTimeout(() => {
        const page = document.createElement("div");
        page.style.cssText = "height:600px;flex-shrink:0;";
        messagesEl.querySelector("#load-more-skeleton")?.after(page);
        document.getElementById("load-more-skeleton")?.remove();
        // The history has one older page.
        scroll.setLoadMore(null, false);
        landed = true;
      }, 30);
    }, true);
    scroll.noteHistoryEdge(true);
    await scrollEnds(wrap);
    await frames(4);
    await closeGesture();
    const before = writes.length;
    await frames(2);
    sampling = false;

    expect({
      landed,
      jumps: residuals.filter((r) => r !== 0),
      state: scroll.readingState(),
      idleWrites: writes.length - before,
    }).toEqual({ landed: true, jumps: [], state: "reading", idleWrites: 0 });
  });
});

describe("a follow target that changes with no size change", () => {
  beforeEach(async () => {
    await reset();
  });

  /** Following a live block with a 300px card below it, on its pin. */
  async function onLivePin(): Promise<{
    wrap: HTMLElement;
    live: HTMLElement;
    clock: HTMLElement;
  }> {
    const wrap = await reset();
    block(2000);
    const live = block(200, "message assistant streaming");
    const clock = block(300);
    clock.textContent = "0:00";
    let anchor: HTMLElement | null = live;
    scroll.setAnchorProvider(() => anchor);
    live.textContent = "a token";
    await land();
    expect(wrap.scrollTop, "the premise: on the pin").toBe(2200 - 400 + 50);
    (live as HTMLElement & { seal?: () => void }).seal = () => {
      anchor = null;
    };
    return { wrap, live, clock };
  }

  it("lands on the end the frame after the owner hears the target changed", async () => {
    const { wrap, live } = await onLivePin();

    (live as HTMLElement & { seal: () => void }).seal();
    scroll.followTargetChanged();
    await frames(1);

    expect({
      onEnd: Math.abs(wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop) <= slack(),
      state: scroll.readingState(),
    }).toEqual({ onEnd: true, state: "following" });
  });

  it("writes nothing when the clock rewrites its text and the target stays", async () => {
    const { wrap, clock } = await onLivePin();
    const writes = recordWrites(wrap);

    for (let i = 1; i <= 3; i++) {
      clock.textContent = `0:0${String(i)}`;
      await land(1000);
    }

    expect(writes).toEqual([]);
  });
});

describe("a clamp from the scrollport growing", () => {
  beforeEach(async () => {
    await reset();
  });

  let n = 0;
  const off = scroll.onReaderGesture(() => {
    n++;
  });
  afterAll(off);

  async function atEnd(): Promise<{
    wrap: HTMLElement;
    last: HTMLElement;
    gestures: () => number;
  }> {
    const wrap = await reset();
    block(1000);
    block(1980);
    const last = block(20);
    await land();
    expect(wrap.scrollTop, "the premise").toBe(2600);
    n = 0;
    return { wrap, last, gestures: () => n };
  }

  it("keeps Following and publishes no gesture when the scrollport grows and clamps", async () => {
    const { wrap, gestures } = await atEnd();

    wrap.style.height = "500px";
    await land();

    expect({ ...landing(wrap), gestures: gestures() }).toEqual({
      scrollTop: 2500,
      state: "following",
      hidden: true,
      gestures: 0,
    });
  });

  it("keeps Following when the last block shrinks by the slack", async () => {
    const { wrap, last, gestures } = await atEnd();

    last.style.height = `${String(20 - slack())}px`;
    await land();

    expect({
      state: scroll.readingState(),
      gestures: gestures(),
      onEnd: wrap.scrollTop >= 2600 - 2 * slack(),
    }).toEqual({
      state: "following",
      gestures: 0,
      onEnd: true,
    });
  });

  it("leaves a reader 300px up Reading, the text in place, with no write", async () => {
    const { wrap } = await atEnd();
    await readAt(wrap, 2300);
    const line = onReadingLine(wrap);
    const before = screenTop(wrap, line);
    const writes = recordWrites(wrap);

    wrap.style.height = "500px";
    await land();

    expect({
      state: scroll.readingState(),
      moved: Math.round(screenTop(wrap, line) - before),
      writes,
    }).toEqual({ state: "reading", moved: 0, writes: [] });
  });
});

describe("holds on the engine's grid", () => {
  afterEach(async () => {
    await restoreDeviceScale();
  });

  it.each([
    [0, 0, 3000, "none"],
    [1300.5, 0, 3000, "none"],
    [1300.5, 0.3, 3000, "none"],
    [1300, -0.5, 3000, "none"],
    [0, -2, 3000, "clamped"],
    [3000, 2, 3000, "clamped"],
  ] as const)("holdTarget(%f, %f, %f) is %s", (before, drift, max, kind) => {
    expect(holdTarget(before, drift, max)).toEqual({ kind });
  });

  it.each([
    [1300.5, 0.6, 3000, 1301, false],
    [1300.5, -0.6, 3000, 1300, false],
    [2999, 3, 3000, 3000, true],
    [10, -22.4, 3000, 0, true],
  ] as const)(
    "holdTarget(%f, %f, %f) writes %f, clamped %s",
    (before, drift, max, top, clamped) => {
      expect(holdTarget(before, drift, max)).toEqual({ kind: "write", top, clamped });
    },
  );

  /** Reading at 1300 in 3000/400: `above` sits over the reading line, `last` under it. */
  async function readingMiddle(): Promise<{
    wrap: HTMLElement;
    above: HTMLElement;
    held: HTMLElement;
    last: HTMLElement;
  }> {
    const wrap = await reset();
    const above = block(1000);
    const held = block(1000);
    const last = block(1000);
    await land();
    await readAt(wrap, 1300);
    return { wrap, above, held, last };
  }

  it.each([1, 1.5, 2])(
    "answers each 1px growth above with one 1px write at DPR %f",
    async (dpr) => {
      await setDeviceScale(dpr);
      const { wrap, above, held } = await readingMiddle();
      const start = screenTop(wrap, held);
      const writes = recordWrites(wrap);

      above.style.height = "1001px";
      await land();
      const first = Math.abs(screenTop(wrap, held) - start);
      above.style.height = "1002px";
      await land();

      expect({
        deltas: writes.map((w) => w.delta),
        first: first <= 0.5,
        second: Math.abs(screenTop(wrap, held) - start) <= 0.5,
      }).toEqual({ deltas: [1, 1], first: true, second: true });
    },
  );

  it.each([1, 1.5, 2])(
    "keeps the text within half a pixel through ten 22.4px growths at DPR %f",
    async (dpr) => {
      await setDeviceScale(dpr);
      const { wrap, above, held } = await readingMiddle();
      const start = screenTop(wrap, held);
      const offs: number[] = [];
      for (let i = 1; i <= 10; i++) {
        above.style.height = `${String(1000 + 22.4 * i)}px`;
        await land();
        offs.push(Math.abs(screenTop(wrap, held) - start));
      }
      const writes = recordWrites(wrap);
      await frames(2);

      expect({ over: offs.filter((o) => o > 0.5), idle: writes }).toEqual({ over: [], idle: [] });
    },
  );

  it.each([0.67, 0.8])(
    "writes nothing for growth below the reader or for their own scrolling at DPR %f",
    async (dpr) => {
      await setDeviceScale(dpr);
      const { wrap, held, last } = await readingMiddle();
      const writes = recordWrites(wrap);
      const start = screenTop(wrap, held);
      const offs: number[] = [];
      for (let i = 1; i <= 10; i++) {
        last.style.height = `${String(1000 + 8 * i)}px`;
        await land();
        offs.push(Math.round(screenTop(wrap, held) - start));
      }
      for (let i = 0; i < 10; i++) {
        const dir = i % 2 === 0 ? -1 : 1;
        wheel(wrap, 3 * dir);
        platformScroll(wrap, wrap.scrollTop + 20 * dir);
        last.style.height = `${String(1080 + 8 * (i + 1))}px`;
        await land(300);
      }

      expect({ writes, offs: offs.filter((o) => o !== 0) }).toEqual({ writes: [], offs: [] });
    },
  );

  it("pays two half-pixel shrinks above with one 1px write", async () => {
    await setDeviceScale(1);
    const { wrap, above, held } = await readingMiddle();
    const start = screenTop(wrap, held);
    const writes = recordWrites(wrap);

    above.style.height = "999.5px";
    await land();
    const first = { writes: writes.length, off: Math.abs(screenTop(wrap, held) - start) <= 0.5 };
    above.style.height = "999px";
    await land();

    expect({
      first,
      deltas: writes.map((w) => w.delta),
      off: Math.abs(screenTop(wrap, held) - start) <= 0.5,
    }).toEqual({ first: { writes: 0, off: true }, deltas: [-1], off: true });
  });

  it("writes nothing when the engine reports its position more coarsely than layout", async () => {
    await setDeviceScale(1);
    const { wrap, last } = await readingMiddle();
    const proto = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop")!;
    Object.defineProperty(wrap, "scrollTop", {
      configurable: true,
      get: () => Math.floor((proto.get!.call(wrap) as number) / 2) * 2,
      set: (v: number) => {
        proto.set!.call(wrap, v);
      },
    });
    try {
      const writes = recordWrites(wrap);
      for (let i = 1; i <= 10; i++) {
        last.style.height = `${String(1000 + 8 * i)}px`;
        await land();
      }
      for (let i = 0; i < 10; i++) {
        const dir = i % 2 === 0 ? -1 : 1;
        wheel(wrap, 3 * dir);
        platformScroll(wrap, (proto.get!.call(wrap) as number) + 21 * dir);
        await land(300);
      }
      expect(writes).toEqual([]);
    } finally {
      Reflect.deleteProperty(wrap, "scrollTop");
    }
  });
});

describe("a scrollport shrink under a follower", () => {
  it("lands the end on the bottom edge at the end of the shrink's delivery", async () => {
    const wrap = await reset();
    block(3000);
    await land();
    let gap: number | null = null;
    const ro = deliverySampler(wrap, () => {
      gap ??= wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop;
    });
    await frames(1);

    setTimeout(() => {
      wrap.style.height = "336px";
    }, 0);
    await land();
    ro.disconnect();

    expect({ gap: gap !== null && Math.abs(gap) <= slack(), state: scroll.readingState() }).toEqual(
      {
        gap: true,
        state: "following",
      },
    );
  });
});

describe("growth inside the owner's own observer callbacks", () => {
  let css: HTMLStyleElement;
  beforeAll(() => {
    css = mountAppCSS();
  });
  afterAll(() => {
    css.remove();
    skeleton.px = 0;
    scroll.setLoadMore(null, false);
  });

  it("holds the text at the next animation frame when a row laid out ahead takes its size", async () => {
    const wrap = await reset();
    wrap.id = "messages-wrap";
    try {
      block(200);
      const row = document.createElement("div");
      row.className = "msg-row";
      row.style.containIntrinsicSize = "auto 20px";
      const body = document.createElement("div");
      body.style.cssText = "height:400px;width:100%;";
      row.append(body);
      messagesEl.append(row);
      const held = block(3000);
      await land();
      await readAt(wrap, 1800);
      expect(row.hasAttribute("data-near"), "the premise: the row is outside the lead").toBe(false);
      const drifts: number[] = [];
      const start = (): number => held.getBoundingClientRect().top;
      let before = 0;
      const io = new IntersectionObserver(
        (entries) => {
          if (entries.some((e) => e.isIntersecting)) {
            requestAnimationFrame(() => {
              drifts.push(Math.round(start() - before));
            });
          }
        },
        { root: wrap, rootMargin: `${String(Math.round(wrap.clientHeight * 1.5))}px 0px` },
      );
      io.observe(row);
      await frames(2);

      before = start() + 1000;
      wheel(wrap, -3);
      platformScroll(wrap, 800);
      await land(400);
      io.disconnect();

      expect({ near: row.hasAttribute("data-near"), drifts }).toEqual({ near: true, drifts: [0] });
    } finally {
      wrap.id = "messagesWrap";
    }
  });

  it("holds the text at the next animation frame when the history edge prepends the skeleton", async () => {
    skeleton.px = 120;
    const wrap = await reset();
    const held = block(3000);
    await land();
    await readAt(wrap, 200);
    const before = screenTop(wrap, held);
    let atFrame: number | null = null;

    scroll.setLoadMore(() => {
      requestAnimationFrame(() => {
        atFrame = screenTop(wrap, held);
      });
    }, true);
    scroll.noteHistoryEdge(true);
    await frames(2);
    scroll.setLoadMore(null, false);
    document.getElementById("load-more-skeleton")?.remove();
    await land();

    expect(atFrame === null ? null : Math.round(atFrame - before)).toBe(0);
  });
});

describe("a reading line on a turn's spacer", () => {
  /** A turn card: rows, then the spacer standing in for rows the window has not mounted, then its footer. */
  function card(rows: number): { card: HTMLElement; last: HTMLElement; spacer: HTMLElement } {
    const c = document.createElement("div");
    c.style.cssText = "display:flex;flex-direction:column;flex-shrink:0;";
    const body = document.createElement("div");
    body.className = "turn-body";
    let last = body;
    for (let i = 0; i < rows; i++) {
      last = document.createElement("div");
      last.style.cssText = "height:100px;flex-shrink:0;";
      body.append(last);
    }
    const spacer = document.createElement("div");
    spacer.className = "turn-space";
    spacer.style.cssText = "height:150px;flex-shrink:0;";
    const footer = document.createElement("div");
    footer.style.cssText = "height:40px;flex-shrink:0;";
    c.append(body, spacer, footer);
    messagesEl.append(c);
    return { card: c, last, spacer };
  }

  it("holds the rows above the spacer while the window fills it, not the footer below", async () => {
    const wrap = await reset();
    block(1000);
    const t = card(10);
    block(2000);
    await land();
    const line = wrap.getBoundingClientRect().top + wrap.clientTop + wrap.clientHeight / 3;
    // The spacer's top 100px above the reading line: the last row sits just above it on screen.
    await readAt(
      wrap,
      Math.round(wrap.scrollTop + t.spacer.getBoundingClientRect().top - (line - 100)),
    );
    const before = screenTop(wrap, t.last);

    // Rows mounted in the spacer's place came out taller than it priced them.
    t.spacer.style.height = "450px";
    await land();

    expect(Math.round(screenTop(wrap, t.last) - before)).toBe(0);
  });
});

describe("a reading line inside a spacer while the window moves", () => {
  /** A turn card as residency builds it: header, a body opening with its head spacer, the body's rows, then the
   *  card's tail spacer and footer. */
  function spacedCard(): {
    card: HTMLElement;
    body: HTMLElement;
    head: HTMLElement;
    tail: HTMLElement;
  } {
    const div = (css: string, cls = ""): HTMLElement => {
      const d = document.createElement("div");
      d.className = cls;
      d.style.cssText = `${css};flex-shrink:0;`;
      return d;
    };
    const card = div("display:flex;flex-direction:column");
    const body = div("display:flex;flex-direction:column", "turn-body");
    const head = div("height:300px", "turn-space");
    body.append(head);
    for (let i = 0; i < 6; i++) {
      body.append(div("height:100px"));
    }
    const tail = div("height:300px", "turn-space");
    card.append(div("height:40px"), body, tail, div("height:40px"));
    messagesEl.append(card);
    return { card, body, head, tail };
  }

  /** Rows priced at 100px that mount at 250px, in the spacer's place. */
  const realRows = (): HTMLElement[] =>
    [0, 1].map(() => {
      const d = document.createElement("div");
      d.style.cssText = "height:250px;flex-shrink:0;";
      return d;
    });

  it("holds the mounted row nearest the line while the window mounts at either end", async () => {
    const wrap = await reset();
    const above = block(1000);
    const { body, head, tail } = spacedCard();
    block(2000);
    await land();
    const line = (): number => wrap.getBoundingClientRect().top + wrap.clientHeight / 3;
    const into = (sp: HTMLElement): number =>
      Math.round(wrap.scrollTop + sp.getBoundingClientRect().top - line() + 50);
    const moved: Record<string, number> = {};
    const step = async (name: string, row: () => Element, change: () => void): Promise<void> => {
      const held = row();
      const at = screenTop(wrap, held);
      change();
      await land();
      moved[name] = Math.round(screenTop(wrap, held) - at);
    };
    const firstRow = (): Element => head.nextElementSibling!;
    const lastRow = (): Element => body.lastElementChild!;

    // The line 50px into the head spacer: the first mounted row is the one nearest it.
    await readAt(wrap, into(head));
    await step("head spacer, head mount", firstRow, () => {
      head.style.height = "100px";
      head.after(...realRows());
    });
    await step("head spacer, growth above", firstRow, () => {
      above.style.height = "1300px";
    });

    // The line 50px into the tail spacer: the last mounted row is the one nearest it.
    await readAt(wrap, into(tail));
    await step("tail spacer, head mount", lastRow, () => {
      head.style.height = "0px";
      head.after(...realRows());
    });
    await step("tail spacer, tail mount", lastRow, () => {
      tail.style.height = "100px";
      body.append(...realRows());
    });

    expect(moved).toEqual({
      "head spacer, head mount": 0,
      "head spacer, growth above": 0,
      "tail spacer, head mount": 0,
      "tail spacer, tail mount": 0,
    });
  });
});

describe("every owner move re-takes the text the reader sees", () => {
  afterEach(async () => {
    await resetA11yMedia();
  });

  /** 4000px in 400, Reading 40px above the end, a 200px row at 2000 between the old viewport and a jump to 1000. */
  async function readingNearEnd(): Promise<{
    wrap: HTMLElement;
    mid: HTMLElement;
    target: HTMLElement;
  }> {
    const wrap = await reset();
    block(1000);
    const target = block(1000);
    const mid = block(200);
    block(1800);
    await land();
    await readAt(wrap, 3560);
    return { wrap, mid, target };
  }

  async function heldAfter(
    wrap: HTMLElement,
    grow: () => void,
  ): Promise<{ writes: number; moved: number }> {
    const line = onReadingLine(wrap);
    const before = screenTop(wrap, line);
    const writes = recordWrites(wrap);
    setTimeout(grow, 0);
    await land();
    return { writes: writes.length, moved: Math.round(screenTop(wrap, line) - before) };
  }

  it("holds the landed text after an instant jumpToOffset, not the text it left", async () => {
    const { wrap, mid } = await readingNearEnd();
    await scroll.jumpToOffset(1000, "instant");
    await land();

    expect(await heldAfter(wrap, () => (mid.style.height = "400px"))).toEqual({
      writes: 0,
      moved: 0,
    });
  });

  it("holds the landed text after a jumpTo that flies no flight", async () => {
    const { wrap, mid, target } = await readingNearEnd();
    await emulateA11yMedia({ reducedMotion: "reduce" });
    scroll.jumpTo(target, { behavior: "smooth" });
    await land();

    expect(await heldAfter(wrap, () => (mid.style.height = "400px"))).toEqual({
      writes: 0,
      moved: 0,
    });
  });

  it("holds the shifted-to text after shiftScroll", async () => {
    const { wrap, mid } = await readingNearEnd();
    scroll.shiftScroll(-2450);
    await land();

    expect(await heldAfter(wrap, () => (mid.style.height = "400px"))).toEqual({
      writes: 0,
      moved: 0,
    });
  });

  it("takes an anchor at a jump from Following, and holds it with one write", async () => {
    const wrap = await reset();
    const above = block(1000);
    block(3000);
    await land();
    await scroll.jumpToOffset(1300, "instant");
    await land();
    expect(scroll.readingState(), "the premise").toBe("reading");

    expect(await heldAfter(wrap, () => (above.style.height = "1040px"))).toEqual({
      writes: 1,
      moved: 0,
    });
  });

  it("takes an anchor once a press's layout settles, and holds it with one write", async () => {
    const wrap = await reset();
    const above = block(300);
    block(700);
    const card = block(200);
    block(3000);
    await land();
    await readAt(wrap, 500);
    card.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 30, clientY: 30, bubbles: true }),
    );
    scroll.pressHold();
    for (let i = 1; i <= 3; i++) {
      card.style.height = `${String(200 + 100 * i)}px`;
      await frames(1);
    }
    await land();

    expect(await heldAfter(wrap, () => (above.style.height = "1040px"))).toEqual({
      writes: 1,
      moved: 0,
    });
  });
});

describe("a size change above in the frame a restored view settles", () => {
  it("holds the text once, so no frame paints it moved", async () => {
    const wrap = await reset();
    const above = block(1000);
    const held = block(3000);
    await land();
    await readAt(wrap, 2000);
    const at = screenTop(wrap, held);
    const parked = scroll.detach();
    scroll.attach({ el: messagesEl, ...parked });
    scroll.applyPendingRestore();
    above.style.height = "1189px";
    const moved: number[] = [];
    // Created after the owner's observer, so each callback reads what that delivery's frame paints.
    const ro = new ResizeObserver(() => {
      moved.push(Math.round(screenTop(wrap, held) - at));
    });
    ro.observe(above);
    await frames(1);
    await land();
    ro.disconnect();

    expect({ painted: moved, landed: Math.round(screenTop(wrap, held) - at) }).toEqual({
      painted: [0],
      landed: 0,
    });
  });
});

describe("a press whose slide starts late", () => {
  it("leaves the reader where they are while what they pressed opens above the reading line", async () => {
    const wrap = await reset();
    block(300);
    const card = block(100);
    block(3000);
    await land();
    await readAt(wrap, 280);
    const top = wrap.scrollTop;
    card.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 30, clientY: 30, bubbles: true }),
    );
    scroll.pressHold();

    // A slide whose first frames move nothing, as a disclosure's does while its start is pending.
    const slide = card.animate([{ height: "100px" }, { height: "300px" }], {
      duration: 200,
      delay: 150,
      fill: "forwards",
    });
    await slide.finished;
    card.style.height = "300px";
    slide.cancel();
    await land();

    expect(wrap.scrollTop - top).toBe(0);
  });
});

describe("a growth above the viewport during a touch fling", () => {
  let css: HTMLStyleElement;
  beforeAll(() => {
    css = mountAppCSS();
  });
  afterAll(() => {
    css.remove();
  });

  const scrollTopOf = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop")!;

  /** iOS WebKit while the UI process flings the scroller: until the owner's next write lands, the page reads a
   *  position `lag` px behind the platform's, the last one the UI reported
   *  (https://github.com/WebKit/WebKit/commit/e8697834ad4b3d036032823f29b7d4329ffb45ec), while layout and the
   *  platform's own position are current.
   *  `landed` is where `el` sat on screen as that write returned. */
  function staleUntilWrite(wrap: HTMLElement, lag: number, el: Element): { landed: number | null } {
    const seen: { landed: number | null } = { landed: null };
    const fresh = (): void => {
      for (const key of ["scrollTop", "scrollTo", "scrollBy"]) {
        Reflect.deleteProperty(wrap, key);
      }
      seen.landed = screenTop(wrap, el);
    };
    Object.defineProperty(wrap, "scrollTop", {
      configurable: true,
      get: () => (scrollTopOf.get!.call(wrap) as number) + lag,
      set: (v: number) => {
        scrollTopOf.set!.call(wrap, v);
      },
    });
    for (const method of ["scrollTo", "scrollBy"] as const) {
      const platform = HTMLElement.prototype[method] as (
        this: HTMLElement,
        o: ScrollToOptions,
      ) => void;
      Object.defineProperty(wrap, method, {
        configurable: true,
        value: (o: ScrollToOptions): void => {
          platform.call(wrap, o);
          fresh();
        },
      });
    }
    return seen;
  }

  /** A finger's drag up the content from `from`, `steps` platform moves of 10px, the finger still down. */
  async function dragUp(wrap: HTMLElement, from: number, steps: number): Promise<void> {
    wrap.dispatchEvent(touchEvent("touchstart", wrap, 100));
    for (let i = 1; i <= steps; i++) {
      wrap.dispatchEvent(touchEvent("touchmove", wrap, 100 + 10 * i));
      platformScroll(wrap, from - 10 * i);
    }
    await frames(2);
    expect(
      { state: scroll.readingState(), gesture: scroll.readerGestureOpen() },
      "the premise: Reading, mid-gesture",
    ).toEqual({ state: "reading", gesture: true });
  }

  afterEach(async () => {
    const wrap = scroll.getScrollEl();
    for (const key of ["scrollTop", "scrollTo", "scrollBy"]) {
      Reflect.deleteProperty(wrap, key);
    }
    wrap.dispatchEvent(touchEvent("touchend", wrap));
    await frames(2);
    await closeGesture();
  });

  it("lands the text where it was when a residency change mounts rows above while the page reads a stale position", async () => {
    const wrap = await reset();
    const above = block(1000);
    const held = block(3000);
    await land();
    await readAt(wrap, 2000);
    await dragUp(wrap, 2000, 4);
    const at = screenTop(wrap, held);

    const seen = staleUntilWrite(wrap, 30, held);
    scroll.heldMutation(() => {
      above.style.height = "1021px";
    });

    expect(seen.landed === null ? null : Math.round(seen.landed - at)).toBe(0);
  });

  it("lands the text where it was when a box laid out ahead takes its real size while the page reads a stale position", async () => {
    const wrap = await reset();
    wrap.id = "messages-wrap";
    try {
      const above = block(200);
      const held = block(3000);
      await land();
      await readAt(wrap, 2000);
      await dragUp(wrap, 2000, 4);
      // Mounted past the engine's own lead, so it has never been laid out at its real size.
      const box = document.createElement("div");
      box.className = "tool-call";
      box.style.cssText = "contain-intrinsic-size:auto 44px;border:0;";
      const body = document.createElement("div");
      body.style.cssText = "height:65px;width:100%;";
      box.append(body);
      scroll.heldMutation(() => {
        above.after(box);
      });
      await frames(2);
      expect(
        { near: box.hasAttribute("data-near"), height: box.offsetHeight },
        "the premise: the box sits outside the lead at its estimate",
      ).toEqual({ near: false, height: 44 });
      const want = screenTop(wrap, held) + (wrap.scrollTop - 800);

      // Into the lead: the near mark lays the box out at 65px against its 44px estimate.
      const seen = staleUntilWrite(wrap, 30, held);
      wrap.dispatchEvent(touchEvent("touchmove", wrap, 250));
      platformScroll(wrap, 800);
      await frames(2);

      expect({
        near: box.hasAttribute("data-near"),
        moved: seen.landed === null ? null : Math.round(seen.landed - want),
      }).toEqual({ near: true, moved: 0 });
    } finally {
      wrap.id = "messagesWrap";
    }
  });

  /** A finger landing on the transcript, not yet moved: a tap, a long-press or a drag to be. */
  function touchDown(wrap: HTMLElement): void {
    wrap.dispatchEvent(
      new PointerEvent("pointerdown", { pointerType: "touch", clientY: 100, bubbles: true }),
    );
    wrap.dispatchEvent(touchEvent("touchstart", wrap, 100));
  }

  it.each([
    [
      "a residency change",
      (above: HTMLElement): void => {
        scroll.heldMutation(() => {
          above.style.height = "1021px";
        });
      },
    ],
    [
      "a resize the owner hears in its delivery",
      (above: HTMLElement): void => {
        above.style.height = "1021px";
      },
    ],
  ])(
    "holds the text through %s above between a finger's touchdown and its move out of the tap slop",
    async (_, grow) => {
      const wrap = await reset();
      const above = block(1000);
      const held = block(3000);
      await land();
      await readAt(wrap, 2000);
      const at = screenTop(wrap, held);

      touchDown(wrap);
      grow(above);
      await frames(2);
      const down = Math.round(screenTop(wrap, held) - at);
      wrap.dispatchEvent(touchEvent("touchmove", wrap, 120));
      await frames(2);

      expect({ down, moving: Math.round(screenTop(wrap, held) - at) }).toEqual({
        down: 0,
        moving: 0,
      });
    },
  );

  it("holds the text when a box the last fling step brought into the lead takes its real size after touchdown", async () => {
    const wrap = await reset();
    wrap.id = "messages-wrap";
    // WebKit's own lead: none, so only the owner's mark lays a box out ahead of the scrollport.
    const noEngineLead = document.createElement("style");
    noEngineLead.textContent =
      '[id="messages-wrap"] .tool-call:not([data-near]) { content-visibility: hidden; }';
    document.head.append(noEngineLead);
    try {
      const above = block(200);
      const held = block(3000);
      await land();
      await readAt(wrap, 2000);
      const box = document.createElement("div");
      box.className = "tool-call";
      box.style.cssText = "contain-intrinsic-size:auto 44px;border:0;";
      const body = document.createElement("div");
      body.style.cssText = "height:65px;width:100%;";
      box.append(body);
      scroll.heldMutation(() => {
        above.after(box);
      });
      await frames(2);
      // The owner's lead: 1.5 scrollports past each edge.
      const edge =
        box.getBoundingClientRect().bottom -
        wrap.getBoundingClientRect().top +
        wrap.scrollTop +
        Math.round(wrap.clientHeight * 1.5);
      await readAt(wrap, edge + 20);
      expect(
        { near: box.hasAttribute("data-near"), height: box.offsetHeight },
        "the premise: the box sits just outside the lead at its estimate",
      ).toEqual({ near: false, height: 44 });
      let at: number | null = null;
      // The finger lands after the step's scroll event, before the near mark is delivered.
      wrap.addEventListener(
        "scroll",
        () => {
          at = screenTop(wrap, held);
          touchDown(wrap);
        },
        { once: true },
      );

      // A fling's last step, too short for the owner to mark the band itself.
      platformScroll(wrap, edge - 20);
      await frames(2);

      expect({
        near: box.hasAttribute("data-near"),
        moved: at === null ? null : Math.round(screenTop(wrap, held) - at),
      }).toEqual({ near: true, moved: 0 });
    } finally {
      noEngineLead.remove();
      wrap.id = "messagesWrap";
    }
  });

  it("holds the text through a growth above once a drag opens inside a tap's press", async () => {
    const wrap = await reset();
    const above = block(1000);
    const held = block(3000);
    await land();
    await readAt(wrap, 2000);
    const at = screenTop(wrap, held);
    held.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    touchDown(wrap);
    wrap.dispatchEvent(touchEvent("touchmove", wrap, 120));
    above.style.height = "1021px";
    await frames(2);

    expect(Math.round(screenTop(wrap, held) - at)).toBe(0);
  });
});

describe("reduced motion", () => {
  afterEach(async () => {
    await resetA11yMedia();
  });

  it.each(["jumpTo", "jumpToOffset"])(
    "lands a smooth %s in its own task when the reader asks for less motion",
    async (how) => {
      const wrap = await reset();
      block(1000);
      const target = block(400);
      block(2000);
      await land();
      await emulateA11yMedia({ reducedMotion: "reduce" });
      let later = 0;

      if (how === "jumpTo") {
        scroll.jumpTo(target, { behavior: "smooth" });
      } else {
        void scroll.jumpToOffset(1000, "smooth");
      }
      const landed = wrap.scrollTop;
      await frames(1);
      const on = (): void => {
        later++;
      };
      wrap.addEventListener("scroll", on);
      await frames(3);
      wrap.removeEventListener("scroll", on);

      expect({ landed, later, state: scroll.readingState() }).toEqual({
        landed: 1000,
        later: 0,
        state: "reading",
      });
    },
  );
});
