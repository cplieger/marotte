// The reading state follows the reader's own motion: an input only opens a gesture, the state changes by where the
// scroller went, and nothing the platform or a timer does moves the reader between Following and Reading.
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { server } from "vitest/browser";
import type * as Dom from "./dom.js";
import type * as Skeleton from "./skeleton.js";
import {
  frames,
  platformScroll,
  scene,
  scrollEnds,
  scrollLog,
  touchEvent,
  wheel,
} from "./__test-helpers__/scroll-scene.js";

vi.mock("./dom.js", async (importOriginal) => {
  const { domProxy: proxy } = await import("./__test-helpers__/scroll-scene.js");
  return { ...(await importOriginal<typeof Dom>()), $: proxy() };
});
vi.mock("./skeleton.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Skeleton>()),
  loadMoreSkeleton: () => document.createElement("div"),
}));

const scroll = await import("./scroll.js");
const { land, block, recordWrites, landing, closeGesture, reset, screenTop, scrollBtn } =
  scene(scroll);

beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
});
afterAll(() => {
  vi.useRealTimers();
});

/** The slack at this page's DPR, the one tolerance every at-the-end decision shares. */
const slack = (): number => Math.max(1, 1 / devicePixelRatio);

/** Following at the end of a 3000px transcript in a 400px scrollport (2600), whose last row is a run clock that
 *  rewrites its text without changing size. `above` is the first row, above every reading line these scenes hold. */
async function followingWithClock(): Promise<{
  wrap: HTMLElement;
  above: HTMLElement;
  mid: HTMLElement;
  clock: HTMLElement;
  tick: () => void;
}> {
  const wrap = await reset();
  const above = block(1000);
  const mid = block(1980);
  const clock = block(20);
  clock.style.overflow = "hidden";
  clock.textContent = "0:00";
  await land();
  expect({ at: wrap.scrollTop, state: scroll.readingState() }, "the premise").toEqual({
    at: 2600,
    state: "following",
  });
  let s = 0;
  const tick = (): void => {
    s = (s + 1) % 10;
    clock.firstChild!.nodeValue = `0:0${String(s)}`;
  };
  return { wrap, above, mid, clock, tick };
}

async function clockFor(tick: () => void, seconds: number): Promise<void> {
  for (let i = 0; i < seconds; i++) {
    tick();
    await land(1000);
  }
}

/** Grow `el` by `px` every frame until the returned stop; the scene never stills meanwhile, so waits are frames. */
function grower(el: HTMLElement, px: number): () => void {
  let on = true;
  const grow = (): void => {
    if (!on) {
      return;
    }
    el.style.height = `${String(el.offsetHeight + px)}px`;
    requestAnimationFrame(grow);
  };
  requestAnimationFrame(grow);
  return () => {
    on = false;
  };
}

/** `ms` of the owner's clock, then a few frames, for a scene that does not hold still. */
async function advance(ms: number): Promise<void> {
  vi.advanceTimersByTime(ms);
  await frames(4);
}

/** The states seen at every scroll event that moved more than the slack from `from`. */
function statesPast(wrap: HTMLElement, from: number): { states: string[]; off: () => void } {
  const states: string[] = [];
  const on = (): void => {
    if (Math.abs(wrap.scrollTop - from) > slack()) {
      states.push(scroll.readingState());
    }
  };
  wrap.addEventListener("scroll", on);
  return { states, off: () => wrap.removeEventListener("scroll", on) };
}

/** A platform smooth scroll standing in for an engine's own tick animation or momentum: asserts it passed through
 *  at least three positions with no `scrollend` before the last. */
async function platformGlide(wrap: HTMLElement, to: number): Promise<void> {
  const log = scrollLog(wrap);
  // Past the 300ms input window of a timer-based owner once the animation is under way, as an engine's own tick
  // outlives it: stepped once the scroller has left the end's slack, because unthrottled test frames can outrun an
  // animation's slow start.
  const from = wrap.scrollTop;
  const step = (): void => {
    if (Math.abs(wrap.scrollTop - from) > 2) {
      wrap.removeEventListener("scroll", step);
      vi.advanceTimersByTime(350);
    }
  };
  wrap.addEventListener("scroll", step);
  platformScroll(wrap, to, "smooth");
  await scrollEnds(wrap);
  log.off();
  expect(
    { moves: log.tops.length >= 3, earlyEnd: log.ends.some((at) => at < log.tops.length) },
    "the premise: one animated platform scroll",
  ).toEqual({ moves: true, earlyEnd: false });
}

/** A `ResizeObserver` created after the owner's, on `target`: its callback runs at the end of the owner's delivery,
 *  the state the frame paints. */
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

describe("a wheel tick's own animation, after the reader's input stops", () => {
  beforeEach(async () => {
    await reset();
  });

  it.each([8, 30, 57, 90])(
    "leaves a reader whose tick carried them %ipx up Reading there, with no owner write",
    async (px) => {
      const { wrap, tick } = await followingWithClock();
      const writes = recordWrites(wrap);
      const seen = statesPast(wrap, 2600);

      wheel(wrap, -3);
      await platformGlide(wrap, 2600 - px);
      await clockFor(tick, 5);
      seen.off();

      expect({
        everyMoveReading: seen.states.every((s) => s === "reading") && seen.states.length > 0,
        writes,
        near: Math.abs(wrap.scrollTop - (2600 - px)) <= slack(),
        state: scroll.readingState(),
      }).toEqual({ everyMoveReading: true, writes: [], near: true, state: "reading" });
    },
  );

  it("holds the reading-line text through a growth above it during the tick, with one write of the growth", async () => {
    const { wrap, above, mid, tick } = await followingWithClock();
    const writes = recordWrites(wrap);
    const contentTop = (): number => mid.parentElement!.getBoundingClientRect().top;
    const start = { screen: screenTop(wrap, mid), content: contentTop() };
    const residual = (): number => {
      const owner = writes.reduce((sum, w) => sum + w.delta, 0);
      const platform = start.content - contentTop() - owner;
      return Math.round(screenTop(wrap, mid) - (start.screen - platform));
    };
    const residuals: number[] = [];
    // The growth is a resize-delivery mover: the frame it lands in is sampled where the frame paints, at the end of
    // the delivery; every other frame at its animation-frame callbacks.
    let grown = false;
    const ro = new ResizeObserver(() => {
      if (grown) {
        grown = false;
        residuals.push(residual());
      }
    });
    ro.observe(above);
    let sampling = true;
    const sample = (): void => {
      if (!grown) {
        residuals.push(residual());
      }
      if (sampling) {
        requestAnimationFrame(sample);
      }
    };
    requestAnimationFrame(sample);

    wheel(wrap, -3);
    const reading = new Promise<void>((r) => {
      const on = (): void => {
        if (scroll.readingState() === "reading") {
          wrap.removeEventListener("scroll", on);
          r();
        }
      };
      wrap.addEventListener("scroll", on);
    });
    platformScroll(wrap, 2543, "smooth");
    await reading;
    vi.advanceTimersByTime(350);
    grown = true;
    above.style.height = "1040px";
    await land();
    await closeGesture();
    sampling = false;
    ro.disconnect();
    const afterGesture = writes.length;
    await clockFor(tick, 5);

    expect({
      writes: writes.slice(0, afterGesture).map((w) => Math.round(w.delta)),
      later: writes.length - afterGesture,
      jumps: residuals.filter((r) => r !== 0),
      state: scroll.readingState(),
    }).toEqual({ writes: [40], later: 0, jumps: [], state: "reading" });
  });

  it("leaves a touch fling's momentum where it took the reader, with no owner write", async () => {
    const { wrap, tick } = await followingWithClock();
    const writes = recordWrites(wrap);

    wrap.dispatchEvent(touchEvent("touchstart", wrap, 100));
    for (let i = 1; i <= 4; i++) {
      wrap.dispatchEvent(touchEvent("touchmove", wrap, 100 + 10 * i));
      platformScroll(wrap, 2600 - 10 * i);
    }
    wrap.dispatchEvent(touchEvent("touchend", wrap));
    await frames(2);
    await platformGlide(wrap, 2480);
    await clockFor(tick, 5);

    expect({ writes, ...landing(wrap) }).toEqual({
      writes: [],
      scrollTop: 2480,
      state: "reading",
      hidden: false,
    });
  });
});

describe("an input that does not move the transcript", () => {
  beforeEach(async () => {
    await reset();
  });

  /** Whether the transcript sits on its end in the frame `grow` is delivered, and the state then. */
  async function pinnedInGrowthFrame(wrap: HTMLElement, grow: HTMLElement): Promise<unknown> {
    let inFrame: number | null = null;
    const ro = deliverySampler(grow, () => {
      inFrame ??= wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop;
    });
    await frames(1);
    grow.style.height = `${String(grow.offsetHeight + 40)}px`;
    await land();
    ro.disconnect();
    return {
      inFrame: inFrame !== null && Math.abs(inFrame) <= slack(),
      state: scroll.readingState(),
    };
  }

  it("keeps a wheel a nested scroller takes from holding the follow write off", async () => {
    const { wrap, clock } = await followingWithClock();
    const nested = document.createElement("div");
    nested.style.cssText = "height:300px;overflow-y:auto;";
    const tall = document.createElement("div");
    tall.style.cssText = "height:1000px;";
    nested.append(tall);
    clock.before(nested);
    await land();
    nested.scrollTop = 350;
    await land();

    wheel(tall, -3);
    nested.scrollTop = 300;

    expect(await pinnedInGrowthFrame(wrap, clock)).toEqual({ inFrame: true, state: "following" });
  });

  it("keeps a key another control handled from holding the follow write off", async () => {
    const { wrap, clock } = await followingWithClock();
    const tab = document.createElement("button");
    tab.addEventListener("keydown", (e) => {
      e.preventDefault();
    });
    document.body.append(tab);

    tab.dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true }),
    );
    const result = await pinnedInGrowthFrame(wrap, clock);
    tab.remove();

    expect(result).toEqual({ inFrame: true, state: "following" });
  });

  it("keeps a Ctrl+wheel zoom from holding the follow write off", async () => {
    const { wrap, clock } = await followingWithClock();

    wheel(wrap, -3, { ctrlKey: true });

    expect(await pinnedInGrowthFrame(wrap, clock)).toEqual({ inFrame: true, state: "following" });
  });

  it("keeps a wheel down at the end from holding the follow write off", async () => {
    const { wrap, clock } = await followingWithClock();

    wheel(wrap, 3);

    expect(await pinnedInGrowthFrame(wrap, clock)).toEqual({ inFrame: true, state: "following" });
  });

  it("keeps a follower Following on the end when a wheel at the end moved nothing and its window passed", async () => {
    const { wrap, clock } = await followingWithClock();

    wheel(wrap, 3);
    vi.advanceTimersByTime(350);
    clock.style.height = "60px";
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2640, state: "following", hidden: true });
  });

  it("keeps a finger aimed down at the end from holding the follow write off", async () => {
    const { wrap, clock } = await followingWithClock();

    wrap.dispatchEvent(touchEvent("touchstart", wrap, 300));
    wrap.dispatchEvent(touchEvent("touchmove", wrap, 280));
    wrap.dispatchEvent(touchEvent("touchmove", wrap, 260));
    const result = await pinnedInGrowthFrame(wrap, clock);
    wrap.dispatchEvent(touchEvent("touchend", wrap));

    expect(result).toEqual({ inFrame: true, state: "following" });
  });
});

describe("rejoining the end", () => {
  beforeEach(async () => {
    await reset();
  });

  it("rejoins a reader whose move down reached the end while a stream grew it", async () => {
    const { wrap, clock } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2540);
    await land(400);
    expect(scroll.readingState(), "the premise").toBe("reading");
    const stop = grower(clock, 8);
    try {
      wheel(wrap, 3);
      platformScroll(wrap, wrap.scrollHeight - wrap.clientHeight, "smooth");
      await scrollEnds(wrap);
      await advance(300);
      let gap: number | null = null;
      const ro = deliverySampler(clock, () => {
        gap = wrap.scrollHeight - wrap.clientHeight - wrap.scrollTop;
      });
      await frames(3);
      ro.disconnect();
      expect({
        state: scroll.readingState(),
        onEnd: gap !== null && Math.abs(gap) <= slack(),
      }).toEqual({
        state: "following",
        onEnd: true,
      });
    } finally {
      stop();
    }
  });

  it("leaves a reader who scrolled below a live pin to the true end Reading, with no write after", async () => {
    const wrap = await reset();
    block(2000);
    const live = block(200, "message assistant streaming");
    block(300);
    scroll.setAnchorProvider(() => live);
    live.textContent = "a token";
    await land();
    const pin = 2200 - 400 + 50;
    expect(wrap.scrollTop, "the premise: on the live pin").toBe(pin);
    const writes = recordWrites(wrap);

    wheel(wrap, 3);
    platformScroll(wrap, 2100, "smooth");
    await scrollEnds(wrap);
    await closeGesture();
    live.textContent = "a token and more";
    await land();

    expect({ writes, ...landing(wrap) }).toEqual({
      writes: [],
      scrollTop: 2100,
      state: "reading",
      hidden: false,
    });
  });

  it("parks a reader the content clamped to the end, and one wheel tick down rejoins", async () => {
    const { wrap, mid } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2560);
    await land(400);
    const writes = recordWrites(wrap);

    mid.style.height = "1780px";
    await land();
    const parked = { ...landing(wrap), writes: writes.filter((w) => w.top !== 2400).length };
    wheel(wrap, 3);
    await land();

    expect({ parked, after: landing(wrap) }).toEqual({
      parked: { scrollTop: 2400, state: "reading", hidden: false, writes: 0 },
      after: { scrollTop: 2400, state: "following", hidden: true },
    });
  });
});

describe("the reader's own scroll event, whatever came with it", () => {
  beforeEach(async () => {
    await reset();
  });

  it("parks a follower whose tick starts in the frame after the owner's own write", async () => {
    const { wrap, clock } = await followingWithClock();
    const stop = grower(clock, 4);
    await frames(4);
    // The owner wrote in the frame just gone; its own scrollend is still to come when the reader's tick starts.
    stop();
    const writes = recordWrites(wrap);
    const start = wrap.scrollTop;
    const ends: number[] = [];
    let moved = false;
    const onEnd = (): void => {
      if (!moved) {
        ends.push(wrap.scrollTop);
      }
    };
    const onScroll = (): void => {
      moved ||= wrap.scrollTop < start;
    };
    wrap.addEventListener("scrollend", onEnd);
    wrap.addEventListener("scroll", onScroll);

    wheel(wrap, -3);
    // The owner's write of the frame just gone fires its scroll and scrollend in the next frame.
    await frames(1);
    platformScroll(wrap, start - 57, "smooth");
    vi.advanceTimersByTime(350);
    await scrollEnds(wrap);
    wrap.removeEventListener("scrollend", onEnd);
    wrap.removeEventListener("scroll", onScroll);
    await closeGesture();

    expect({
      premise: ends.length > 0,
      writes,
      state: scroll.readingState(),
      near: Math.abs(wrap.scrollTop - (start - 57)) <= slack(),
    }).toEqual({ premise: true, writes: [], state: "reading", near: true });
  });

  it("parks a follower whose wheel the engine scrolled before it dispatched the wheel event", async () => {
    // A threaded scroller applies the tick first: the wheel listener already reads the moved position.
    const { wrap } = await followingWithClock();
    const start = wrap.scrollTop;
    const writes = recordWrites(wrap);
    platformScroll(wrap, start - 57);
    wheel(wrap, -3);
    await scrollEnds(wrap);
    await closeGesture();

    expect({ writes, state: scroll.readingState(), top: wrap.scrollTop }).toEqual({
      writes: [],
      state: "reading",
      top: start - 57,
    });
  });

  it("leaves a scroll up with no input behind it where it took the reader, while a stream grows", async () => {
    const { wrap, clock } = await followingWithClock();
    const stop = grower(clock, 8);
    try {
      await frames(3);
      const moved = wrap.scrollTop - 200;
      platformScroll(wrap, moved);
      const positions: number[] = [];
      for (let i = 0; i < 6; i++) {
        await advance(160);
        positions.push(wrap.scrollTop);
      }
      expect({ state: scroll.readingState(), positions }).toEqual({
        state: "reading",
        positions: Array<number>(6).fill(moved),
      });
    } finally {
      stop();
    }
  });

  it("holds the text the reader moved to when a growth above lands in the same frame as their move", async () => {
    const { wrap, above, mid } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2300);
    await land(400);
    expect(scroll.readingState(), "the premise").toBe("reading");

    platformScroll(wrap, 2100);
    above.style.height = "1040px";
    // Read after both: the growth has pushed the text the reader moved to 40px down.
    const before = screenTop(wrap, mid) - 40;
    await land();

    expect({
      held: Math.round(screenTop(wrap, mid) - before),
      state: scroll.readingState(),
    }).toEqual({
      held: 0,
      state: "reading",
    });
  });

  it("parks a follower at the reader's touch that reverses, by each move's own direction", async () => {
    const { wrap } = await followingWithClock();
    const writes = recordWrites(wrap);

    wrap.dispatchEvent(touchEvent("touchstart", wrap, 400));
    wrap.dispatchEvent(touchEvent("touchmove", wrap, 340));
    wrap.dispatchEvent(touchEvent("touchmove", wrap, 370));
    platformScroll(wrap, 2570);
    wrap.dispatchEvent(touchEvent("touchend", wrap));
    for (let i = 0; i < 3; i++) {
      await land(300);
    }

    expect({ writes, ...landing(wrap) }).toEqual({
      writes: [],
      scrollTop: 2570,
      state: "reading",
      hidden: false,
    });
  });

  it("leaves a reader's no-input move up where it took them while a turn streams below", async () => {
    const { wrap, clock } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2560);
    await land(400);
    const stop = grower(clock, 8);
    try {
      platformScroll(wrap, 2530);
      await advance(400);
      await advance(400);
      expect(landing(wrap)).toEqual({ scrollTop: 2530, state: "reading", hidden: false });
    } finally {
      stop();
    }
  });
});

describe("whose move it is, and which way", () => {
  type Kind = "wheel" | "touch" | "sideways touch" | "key" | "bar" | "none";

  /** The gutter a scroller styled as the scene's reserves: an engine with overlay scrollbars reserves none, and a
   *  thumb press has nowhere to land. */
  function sceneGutter(): number {
    const probe = document.createElement("div");
    probe.style.cssText =
      "position:absolute;width:100px;height:100px;overflow-y:auto;scrollbar-gutter:stable;";
    document.body.append(probe);
    const gutter = probe.offsetWidth - probe.clientWidth;
    probe.remove();
    return gutter;
  }
  const hasGutter = sceneGutter() > 0;

  /** 3000px in a 400px scrollport, Following at the end (2600), or parked `at` px above it by the reader's own wheel
   *  tick. Parked at the end (0): a tick up 60px, then a collapse below that brings the end to them. */
  async function start(at: "following" | number): Promise<HTMLElement> {
    const { wrap, mid } = await followingWithClock();
    if (at === "following") {
      return wrap;
    }
    wheel(wrap, -3);
    platformScroll(wrap, 2600 - (at === 0 ? 60 : at));
    await land(400);
    if (at === 0) {
      mid.style.height = "1920px";
      await land(400);
    }
    expect(landing(wrap), "the premise").toEqual({
      scrollTop: 2600 - (at === 0 ? 60 : at),
      state: "reading",
      hidden: false,
    });
    return wrap;
  }

  /** The input of `kind`, aimed `dir` (1: down), as its device delivers it, before the scroll it produces; returns
   *  its release. */
  function input(wrap: HTMLElement, kind: Kind, dir: -1 | 1): () => void {
    const gutterX = wrap.getBoundingClientRect().right - (wrap.offsetWidth - wrap.clientWidth) / 2;
    switch (kind) {
      case "wheel":
        wheel(wrap, 3 * dir);
        return () => undefined;
      case "touch":
        // A finger moving up the screen scrolls the content down.
        wrap.dispatchEvent(touchEvent("touchstart", wrap, 200));
        wrap.dispatchEvent(touchEvent("touchmove", wrap, 200 - 20 * dir));
        wrap.dispatchEvent(touchEvent("touchend", wrap));
        return () => undefined;
      case "sideways touch":
        wrap.dispatchEvent(touchEvent("touchstart", wrap, 200, 100));
        wrap.dispatchEvent(touchEvent("touchmove", wrap, 200, 130));
        wrap.dispatchEvent(touchEvent("touchend", wrap));
        return () => undefined;
      case "key":
        document.body.dispatchEvent(
          new KeyboardEvent("keydown", { key: dir > 0 ? "ArrowDown" : "ArrowUp", bubbles: true }),
        );
        return () => undefined;
      case "bar":
        wrap.dispatchEvent(
          new PointerEvent("pointerdown", { clientX: gutterX, clientY: 200, bubbles: true }),
        );
        document.dispatchEvent(
          new PointerEvent("pointermove", { clientX: gutterX, clientY: 200 + 20 * dir }),
        );
        // An engine fires a thumb drag's `scrollend` at the release
        // (https://developer.chrome.com/blog/scrollend-a-new-javascript-event); the glide's instant steps fired theirs
        // while it was held.
        return () => {
          document.dispatchEvent(new PointerEvent("pointerup", { clientX: gutterX }));
          wrap.dispatchEvent(new Event("scrollend"));
        };
      case "none":
        return () => undefined;
    }
  }

  /** The platform carrying the scroller to `to`, the last 2px one frame each. */
  async function glide(wrap: HTMLElement, to: number): Promise<void> {
    const from = wrap.scrollTop;
    const s = Math.sign(to - from);
    for (const top of [to - 2 * s, to - s, to].filter((t) => (t - from) * s > 0)) {
      platformScroll(wrap, top);
      await land();
    }
  }

  /** A gesture of `px` over `ms`, eased out, a frame every 50ms. */
  async function easeOut(
    wrap: HTMLElement,
    kind: "wheel" | "touch",
    dir: -1 | 1,
    px: number,
    ms: number,
  ): Promise<void> {
    const from = wrap.scrollTop;
    input(wrap, kind, dir)();
    for (let t = 50; t <= ms; t += 50) {
      platformScroll(wrap, from + dir * px * (1 - (1 - t / ms) ** 3));
      await land(50);
    }
  }

  interface Row {
    name: string;
    at: "following" | number;
    act: (wrap: HTMLElement) => Promise<void>;
    state: "following" | "reading";
  }
  /** `kind`'s input aimed by the sign of `to`, then the platform's move: `to` px, or onto the end. */
  const move =
    (kind: Kind, to: number | "end") =>
    async (wrap: HTMLElement): Promise<void> => {
      const end = wrap.scrollHeight - wrap.clientHeight;
      const release = input(wrap, kind, to === "end" || to > 0 ? 1 : -1);
      await glide(wrap, to === "end" ? end : wrap.scrollTop + to);
      release();
      await land(400);
    };
  const kinds: Kind[] = ["wheel", "touch", "key", "bar", "none"];
  const rows: Row[] = [
    ...kinds.flatMap((kind): Row[] => [
      {
        name: `${kind} up from the end`,
        at: "following",
        act: move(kind, -30),
        state: "reading",
      },
      // 2px: 1px above the end is within the slack, so it is the end (the smallest Reading park).
      {
        name: `${kind} down onto the end from 2px`,
        at: 2,
        act: move(kind, "end"),
        state: "following",
      },
      {
        name: `${kind} down onto the end from 57px`,
        at: 57,
        act: move(kind, "end"),
        state: "following",
      },
      { name: `${kind} down 20px of 57px`, at: 57, act: move(kind, 20), state: "reading" },
      {
        name: `${kind} down onto the end from 400px`,
        at: 400,
        act: move(kind, "end"),
        state: "following",
      },
      { name: `${kind} up from 400px`, at: 400, act: move(kind, -30), state: "reading" },
      ...(kind === "none"
        ? []
        : [
            {
              name: `${kind} down at the end`,
              at: 0,
              act: move(kind, "end"),
              state: "following",
            } as const,
          ]),
    ]),
    {
      name: "a sideways touch at the end",
      at: 0,
      act: move("sideways touch", 0),
      state: "reading",
    },
    {
      name: "a sideways touch while Following",
      at: "following",
      act: move("sideways touch", 0),
      state: "following",
    },
    {
      name: "a wheel 57px up, then 57px down in eased steps",
      at: "following",
      act: async (wrap) => {
        await easeOut(wrap, "wheel", -1, 57, 400);
        await land(1000);
        await easeOut(wrap, "wheel", 1, 57, 400);
        await land(400);
      },
      state: "following",
    },
    {
      name: "a touch 90px up, then 90px down with momentum",
      at: "following",
      act: async (wrap) => {
        await easeOut(wrap, "touch", -1, 90, 900);
        await land(1000);
        await easeOut(wrap, "touch", 1, 90, 900);
        await land(400);
      },
      state: "following",
    },
  ];

  async function leaves({ at, act, state }: Row): Promise<void> {
    const wrap = await start(at);

    await act(wrap);

    expect({
      state: scroll.readingState(),
      hidden: scrollBtn.classList.contains("hidden"),
    }).toEqual({
      state,
      hidden: state === "following",
    });
  }

  const isBar = (row: Row): boolean => row.name.startsWith("bar ");
  it.each(rows.filter((row) => !isBar(row)))("$name leaves the reader $state", leaves);
  it.skipIf(!hasGutter).each(rows.filter(isBar))("$name leaves the reader $state", leaves);

  it.runIf(server.browser === "chromium")(
    "runs the thumb rows in Chromium, whose scene scroller reserves a gutter",
    async () => {
      const wrap = await start("following");

      const gutter = wrap.offsetWidth - wrap.clientWidth;

      expect(gutter, "the scene's scroller reserves a gutter").toBeGreaterThan(0);
      expect(sceneGutter(), "the probe the bar rows skip on").toBe(gutter);
    },
  );

  it.each<[string, (target: HTMLElement) => Promise<void>]>([
    [
      "jumpTo",
      async (target) => {
        scroll.jumpTo(target, { behavior: "instant" });
        await Promise.resolve();
      },
    ],
    [
      "jumpToOffset",
      async () => {
        await scroll.jumpToOffset(1000, "instant");
      },
    ],
  ])(
    "keeps a %s park Reading when a collapse brings the end to the reader, and one tick down rejoins",
    async (_, jump) => {
      const { wrap, mid } = await followingWithClock();
      wheel(wrap, -3);
      platformScroll(wrap, 2540);
      await land(400);

      await jump(mid);
      await land(400);
      expect(landing(wrap), "the jump parks").toEqual({
        scrollTop: 1000,
        state: "reading",
        hidden: false,
      });
      // 1000 + 380 + 20 in 400: the end comes to 1000, where the reader sits.
      mid.style.height = "380px";
      await land();
      const collapsed = landing(wrap);
      wheel(wrap, 3);
      await land();

      expect({ collapsed, after: landing(wrap) }).toEqual({
        collapsed: { scrollTop: 1000, state: "reading", hidden: false },
        after: { scrollTop: 1000, state: "following", hidden: true },
      });
    },
  );

  it("keeps a Following reader on the end when the scrollport shrinks under a taller composer", async () => {
    const wrap = await start("following");

    wrap.style.height = "336px";
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2664, state: "following", hidden: true });
  });
});

describe("the reader's park, after their input stops", () => {
  beforeEach(async () => {
    await reset();
  });

  it.each([2, 30, 57])(
    "keeps a reader %ipx up held through a growth above once their tick has ended",
    async (px) => {
      const { wrap, above, tick } = await followingWithClock();
      wheel(wrap, -3);
      platformScroll(wrap, 2600 - px);
      await land(400);
      expect(scroll.readingState(), "the premise").toBe("reading");

      // A row above the reading line takes 4px more (a placeholder laid out).
      above.style.height = "1004px";
      await land();
      await clockFor(tick, 3);

      expect(landing(wrap)).toEqual({ scrollTop: 2604 - px, state: "reading", hidden: false });
    },
  );

  it("keeps a reader 1px above the end Following, and a growth there costs one follow write", async () => {
    const { wrap, clock, tick } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2599);
    await land(400);
    const writes = recordWrites(wrap);

    clock.style.height = "21px";
    await land();
    clock.style.height = "22px";
    await land();
    await clockFor(tick, 3);

    expect({ state: scroll.readingState(), at: wrap.scrollTop, writes: writes.length }).toEqual({
      state: "following",
      // The first 1px makes the gap 2, past the slack: one write to the end. The second leaves a gap of 1.
      at: 2601,
      writes: 1,
    });
  });

  it("keeps the reader's own park through a press on a card", async () => {
    const { wrap, tick } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2570);
    await land();
    // What a tool card's disclosure does on the reader's press.
    scroll.pressHold();
    await land(400);

    platformScroll(wrap, 2560);
    await land();
    await clockFor(tick, 3);

    expect(landing(wrap)).toEqual({ scrollTop: 2560, state: "reading", hidden: false });
  });

  it("writes nothing while Following at the end and the clock rewrites its text", async () => {
    const { wrap, tick } = await followingWithClock();
    const writes = recordWrites(wrap);

    await clockFor(tick, 5);

    expect({ writes, at: wrap.scrollTop }).toEqual({ writes: [], at: 2600 });
  });

  it.each([
    [8, 2551, "reading"],
    [57, 2600, "following"],
  ])("after parking 57px up, a %ipx move down lands at %i in %s", async (down, at, state) => {
    const { wrap, tick } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2543);
    await land(400);

    wheel(wrap, 3);
    platformScroll(wrap, 2543 + down);
    await land(400);
    await clockFor(tick, 3);

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({ at, state });
  });

  it("stays where an upward scroll with no input event behind it left the reader", async () => {
    const { wrap, tick } = await followingWithClock();
    const writes = recordWrites(wrap);

    // An overlay scrollbar's thumb, middle-click autoscroll or find-in-page: the scroller moves, no input announces it.
    platformScroll(wrap, 2550);
    await land();
    await clockFor(tick, 3);

    expect({ writes, ...landing(wrap) }).toEqual({
      writes: [],
      scrollTop: 2550,
      state: "reading",
      hidden: false,
    });
  });

  it("releases a parked reader who moves back down to the end with no input behind it", async () => {
    const { wrap } = await followingWithClock();
    wheel(wrap, -3);
    platformScroll(wrap, 2500);
    await land(400);

    platformScroll(wrap, 2540);
    await land();
    const short = scroll.readingState();
    platformScroll(wrap, 2600);
    await land();

    expect({ short, ...landing(wrap) }).toEqual({
      short: "reading",
      scrollTop: 2600,
      state: "following",
      hidden: true,
    });
  });

  it("leaves a platform move after a resume alone", async () => {
    const { wrap } = await followingWithClock();
    scroll.scrollToBottom();
    await land(1000);

    platformScroll(wrap, 2550);
    await land(150);

    expect({ scrollTop: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      scrollTop: 2550,
      state: "reading",
    });
  });

  it("keeps Following when a shrink in view clamps the scroller onto the new end", async () => {
    const { wrap, mid, tick } = await followingWithClock();

    mid.style.height = "1880px";
    await land();
    await clockFor(tick, 2);

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 2500,
      state: "following",
    });
  });

  /** Parked by the reader at the end of a 2800px transcript after a collapse below them; the last 100px are a
   *  scroller of its own at its top, and a button handles its own ArrowDown. */
  async function parkedAtEndOverNested(): Promise<{
    wrap: HTMLElement;
    inner: HTMLElement;
    key: HTMLElement;
  }> {
    const wrap = await reset();
    const content = block(2900);
    const nested = block(100);
    nested.style.overflowY = "auto";
    const inner = document.createElement("div");
    inner.style.cssText = "height:300px;";
    nested.append(inner);
    const key = document.createElement("button");
    key.addEventListener("keydown", (e) => {
      e.preventDefault();
    });
    content.append(key);
    await land();
    wheel(wrap, -3);
    platformScroll(wrap, 2540);
    await land(400);
    content.style.height = "2700px";
    await land(400);
    expect(landing(wrap), "the premise").toEqual({
      scrollTop: 2400,
      state: "reading",
      hidden: false,
    });
    return { wrap, inner, key };
  }

  type Nested = Awaited<ReturnType<typeof parkedAtEndOverNested>>;
  it.each<[string, (s: Nested) => void]>([
    ["a horizontal wheel", (s) => s.wrap.dispatchEvent(new WheelEvent("wheel", { deltaX: 3 }))],
    ["a Ctrl+wheel", (s) => wheel(s.wrap, 3, { ctrlKey: true })],
    ["a wheel down a nested scroller takes", (s) => wheel(s.inner, 3)],
    [
      "an ArrowDown a control handled",
      (s) =>
        s.key.dispatchEvent(
          new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }),
        ),
    ],
  ])("keeps the reader parked at the end Reading through %s", async (_, act) => {
    const s = await parkedAtEndOverNested();

    act(s);
    await land();

    expect(landing(s.wrap)).toEqual({ scrollTop: 2400, state: "reading", hidden: false });
  });

  it("rejoins at the end with a wheel down once the nested scroller is at its own end", async () => {
    const { wrap, inner } = await parkedAtEndOverNested();
    inner.parentElement!.scrollTop = 200;

    wheel(inner, 3);
    await land();

    expect(landing(wrap)).toEqual({ scrollTop: 2400, state: "following", hidden: true });
  });
});

describe("an upward move the reader did not make", () => {
  beforeEach(async () => {
    await reset();
  });

  /** Following a live block pinned above the end: 1000px, 500px, the 200px streaming block, then 900px below it. */
  async function pinnedAboveTheEnd(): Promise<{ wrap: HTMLElement; first: HTMLElement }> {
    const wrap = await reset();
    const first = block(1000);
    block(500);
    const streaming = block(200, "message assistant streaming");
    block(900);
    await land();
    expect(wrap.scrollTop, "the premise: Following at the end").toBe(2200);
    scroll.setAnchorProvider(() => streaming);
    streaming.textContent = "a token";
    await land();
    return { wrap, first };
  }

  it("keeps Following through the owner's own pin write up to the live block", async () => {
    const { wrap } = await pinnedAboveTheEnd();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: 1700 - 400 + 50,
      state: "following",
    });
  });

  it("keeps Following, by the owner's write, when a block above the viewport shrinks", async () => {
    const { wrap, first } = await pinnedAboveTheEnd();
    const pinned = wrap.scrollTop;

    first.style.height = "900px";
    await land();

    expect({ at: wrap.scrollTop, state: scroll.readingState() }).toEqual({
      at: pinned - 100,
      state: "following",
    });
  });
});
