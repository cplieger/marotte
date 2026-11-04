// The owner's corrections: Following lands on the end in the frame a size changes, Reading keeps the text on the
// reading line still, every write lands on the engine's grid, and every owner move re-takes what the reader sees.
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as Skeleton from "./skeleton.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { clearDeviceScale, setDeviceScale } from "./__test-helpers__/device-scale.js";
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
    await clearDeviceScale();
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
    await clearDeviceScale();
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
