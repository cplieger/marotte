// The clamp floor on the shipped transcript view: a shrink in any task cannot clamp the scroller or move the text
// above it before the owner answers it, and the owner lowers the floor without moving what the reader reads.
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as Skeleton from "./skeleton.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { frames, platformScroll, scene, wheel } from "./__test-helpers__/scroll-scene.js";

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
const { messagesEl, land, recordWrites, reset, screenTop, closeGesture } = scene(scroll);

let css: HTMLStyleElement;
let gestures = 0;
const offGestures = scroll.onReaderGesture(() => {
  gestures++;
});
beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  css = mountAppCSS();
});
afterAll(() => {
  offGestures();
  css.remove();
  vi.useRealTimers();
});
let view: HTMLElement | null = null;
afterEach(() => {
  skeleton.px = 0;
  scroll.setLoadMore(null, false);
  if (view !== null) {
    scroll.detach();
    view.remove();
    view = null;
  }
});

const slack = (): number => Math.max(1, 1 / devicePixelRatio);

/** An attached `.transcript-view` in the 400px scroller holding cards of `heights`, Following at its end. */
async function floorScene(
  heights: number[],
): Promise<{ wrap: HTMLElement; v: HTMLElement; cards: HTMLElement[]; gap: number }> {
  const wrap = await reset();
  const v = document.createElement("div");
  v.className = "transcript-view is-active";
  messagesEl.append(v);
  view = v;
  const cards = heights.map((h) => {
    const c = document.createElement("div");
    c.style.cssText = `height:${String(h)}px;flex-shrink:0;`;
    return c;
  });
  scroll.attach({ el: v, scrollTop: 0, readingState: "following" });
  v.append(...cards);
  await land();
  const gap = parseFloat(getComputedStyle(v).rowGap);
  return { wrap, v, cards, gap };
}

async function readAt(wrap: HTMLElement, top: number): Promise<void> {
  wheel(wrap, top < wrap.scrollTop ? -3 : 3);
  platformScroll(wrap, top);
  await land(400);
  expect(scroll.readingState(), "the premise: Reading").toBe("reading");
}

const end = (wrap: HTMLElement): number => wrap.scrollHeight - wrap.clientHeight;

/** A `ResizeObserver` created after the owner's, on one of its targets: its callback runs at the end of the owner's
 *  delivery. Never on the view, whose own box the floor changes. */
function afterDelivery(target: Element, read: () => void): ResizeObserver {
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

describe("a shrink read before the owner answers it", () => {
  it("keeps the text and scrollTop where they were, and packs the freed space below", async () => {
    const { wrap, v, cards } = await floorScene([1000, 1000, 1000, 1000]);
    await readAt(wrap, 1200);
    const held = cards[1]!;
    const before = { text: screenTop(wrap, held), top: wrap.scrollTop };
    const writes = recordWrites(wrap);
    let atDelivery: { text: number; writes: number } | null = null;
    const ro = afterDelivery(cards[3]!, () => {
      atDelivery ??= {
        text: Math.round(screenTop(wrap, held) - before.text),
        writes: writes.length,
      };
    });
    await frames(1);

    cards[3]!.style.height = "700px";
    const forced = {
      text: Math.round(screenTop(wrap, held) - before.text),
      top: wrap.scrollTop - before.top,
      floor: v.hasAttribute("data-scroll-floor"),
      packing: getComputedStyle(v).justifyContent,
    };
    await frames(3);
    ro.disconnect();
    const last = cards[3]!.getBoundingClientRect().bottom;

    expect({
      forced,
      atDelivery,
      lowered: Math.abs(v.getBoundingClientRect().bottom - (last + 16)) < 0.5,
      floor: v.hasAttribute("data-scroll-floor"),
    }).toEqual({
      forced: { text: 0, top: 0, floor: true, packing: "flex-start" },
      atDelivery: { text: 0, writes: 0 },
      lowered: true,
      floor: true,
    });
  });
});

describe("a card removed inside an animation-frame callback", () => {
  it("holds the text for a later callback of the same frame, with one write of the card and its gap", async () => {
    const { wrap, cards, gap } = await floorScene([300, 1000, 1000, 1000]);
    await readAt(wrap, 1500);
    const held = cards[2]!;
    const before = screenTop(wrap, held);
    const writes = recordWrites(wrap);
    let seen: number | null = null;

    requestAnimationFrame(() => {
      cards[0]!.remove();
    });
    requestAnimationFrame(() => {
      seen = Math.round(screenTop(wrap, held) - before);
    });
    await frames(2);

    expect({ seen, writes: writes.map((w) => Math.round(w.delta)) }).toEqual({
      seen: 0,
      writes: [-(300 + gap)],
    });
  });

  it("writes nothing for a card removed below the reader", async () => {
    const { wrap, cards } = await floorScene([1000, 1000, 1000, 1000]);
    await readAt(wrap, 1000);
    const held = cards[1]!;
    const before = screenTop(wrap, held);
    const writes = recordWrites(wrap);

    requestAnimationFrame(() => {
      cards[3]!.remove();
    });
    await frames(2);

    expect({ moved: Math.round(screenTop(wrap, held) - before), writes }).toEqual({
      moved: 0,
      writes: [],
    });
  });
});

describe("a view that fits its scrollport", () => {
  it("carries no floor and keeps its last turn 16px above the scroller's bottom", async () => {
    const { wrap, v, cards } = await floorScene([100, 100]);

    expect({
      floor: v.hasAttribute("data-scroll-floor"),
      min: v.style.minBlockSize,
      air: Math.round(
        wrap.getBoundingClientRect().bottom - cards[1]!.getBoundingClientRect().bottom,
      ),
    }).toEqual({ floor: false, min: "", air: 16 });
  });

  it("drops the floor when the scrollport grows past the view, in the scroller's own delivery", async () => {
    const { wrap, v, cards } = await floorScene([300, 300]);
    expect(v.hasAttribute("data-scroll-floor"), "the premise: floored while it overflows").toBe(
      true,
    );
    let atDelivery: { floor: boolean; air: number } | null = null;
    const ro = afterDelivery(wrap, () => {
      atDelivery ??= {
        floor: v.hasAttribute("data-scroll-floor") || v.style.minBlockSize !== "",
        air: Math.round(
          wrap.getBoundingClientRect().bottom - cards[1]!.getBoundingClientRect().bottom,
        ),
      };
    });
    await frames(1);

    setTimeout(() => {
      wrap.style.height = "900px";
    }, 0);
    await land();
    ro.disconnect();

    expect(atDelivery).toEqual({ floor: false, air: 16 });
  });
});

describe("content above a Reading reader near the end goes away", () => {
  it("holds the text when a card above is removed, with one write and no clamp", async () => {
    const { wrap, cards } = await floorScene([300, 1000, 1000]);
    await readAt(wrap, end(wrap) - 40);
    const held = cards[2]!;
    const before = screenTop(wrap, held);
    const writes = recordWrites(wrap);

    cards[0]!.remove();
    await frames(1);
    const seen = Math.abs(screenTop(wrap, held) - before);

    expect({
      held: seen <= 0.5,
      writes: writes.length,
      inRange: wrap.scrollTop <= end(wrap),
    }).toEqual({
      held: true,
      writes: 1,
      inRange: true,
    });
  });

  it("holds the text when a block above shrinks 300px, at the end of the shrink's delivery", async () => {
    const { wrap, cards } = await floorScene([600, 1000, 1000]);
    await readAt(wrap, end(wrap) - 40);
    const held = cards[2]!;
    const before = screenTop(wrap, held);
    const writes = recordWrites(wrap);
    let atDelivery: { held: boolean; writes: number } | null = null;
    const ro = afterDelivery(cards[0]!, () => {
      atDelivery ??= {
        held: Math.abs(screenTop(wrap, held) - before) <= 0.5,
        writes: writes.length,
      };
    });
    await frames(1);

    setTimeout(() => {
      cards[0]!.style.height = "300px";
    }, 0);
    await land();
    ro.disconnect();

    expect({ atDelivery, inRange: wrap.scrollTop <= end(wrap) }).toEqual({
      atDelivery: { held: true, writes: 1 },
      inRange: true,
    });
  });

  it("lets a card removed below clamp the reader onto the new end, and never pays that move back", async () => {
    const { wrap, cards } = await floorScene([1000, 1000, 300, 200]);
    await readAt(wrap, end(wrap) - 40);
    const writes = recordWrites(wrap);

    cards[2]!.remove();
    await frames(2);
    const clamped = {
      writes: writes.length,
      onEnd: Math.abs(wrap.scrollTop - end(wrap)) <= slack(),
    };
    const line = document.elementFromPoint(
      wrap.getBoundingClientRect().left + 20,
      wrap.getBoundingClientRect().top + wrap.clientHeight / 3,
    )!;
    const before = screenTop(wrap, line);
    cards[0]!.style.height = "1040px";
    await land();
    const grown = {
      deltas: writes.map((w) => Math.round(w.delta)),
      held: Math.round(screenTop(wrap, line) - before),
    };
    await frames(2);
    cards[3]!.style.height = "208px";
    await land();

    expect({ clamped, grown, later: writes.length }).toEqual({
      clamped: { writes: 0, onEnd: true },
      grown: { deltas: [40], held: 0 },
      later: 1,
    });
  });
});

describe("a sub-pixel shrink", () => {
  it("leaves the view floored at its new size, so a later shrink in another task clamps nothing", async () => {
    const { wrap, v, cards } = await floorScene([1000, 1000, 1000]);
    await readAt(wrap, end(wrap) - 100);
    gestures = 0;

    cards[0]!.style.height = "999.6px";
    await frames(3);
    const floored = {
      attr: v.hasAttribute("data-scroll-floor"),
      size: Math.abs(parseFloat(v.style.minBlockSize) - v.getBoundingClientRect().height) < 0.01,
    };
    const top = wrap.scrollTop;
    let forced = Number.NaN;
    setTimeout(() => {
      cards[2]!.style.height = "700px";
      forced = wrap.scrollTop;
    }, 0);
    await land();

    expect({ floored, forced: forced - top, gestures, state: scroll.readingState() }).toEqual({
      floored: { attr: true, size: true },
      forced: 0,
      gestures: 0,
      state: "reading",
    });
  });
});

describe("the older-page skeleton leaving with no page", () => {
  it.each(["an empty page", "the pass's 15s safety"])(
    "holds the text when %s removes it, with one write of its height and gap",
    async (how) => {
      skeleton.px = 120;
      const { wrap, cards, gap } = await floorScene([1000, 1000]);
      await readAt(wrap, 200);
      if (how !== "an empty page") {
        vi.useFakeTimers({ toFake: ["setTimeout", "Date"] });
      }
      try {
        scroll.setLoadMore(() => undefined, true);
        scroll.noteHistoryEdge(true);
        await frames(2);
        expect(
          document.getElementById("load-more-skeleton"),
          "the premise: the pass is open",
        ).not.toBeNull();
        const held = cards[0]!;
        const before = screenTop(wrap, held);
        const writes = recordWrites(wrap);
        let seen: number | null = null;

        if (how === "an empty page") {
          setTimeout(() => {
            document.getElementById("load-more-skeleton")?.remove();
            requestAnimationFrame(() => {
              seen = Math.round(screenTop(wrap, held) - before);
            });
          }, 0);
        } else {
          vi.advanceTimersByTime(15_000);
          requestAnimationFrame(() => {
            seen = Math.round(screenTop(wrap, held) - before);
          });
        }
        await frames(3);

        expect({ seen, writes: writes.map((w) => Math.round(w.delta)) }).toEqual({
          seen: 0,
          writes: [-(120 + gap)],
        });
      } finally {
        vi.useFakeTimers({ toFake: ["Date"] });
      }
    },
  );
});

describe("content above the reader changing during their gesture", () => {
  /** Following on a live block whose pin sits 50px above the end (its air), with a down-aimed wheel gesture open:
   *  the shape where lowering the floor before a hold clamps the scroller by 262px and moves the text 50px. */
  async function followingGesture(): Promise<{
    wrap: HTMLElement;
    cards: HTMLElement[];
    live: HTMLElement;
  }> {
    const { wrap, cards } = await floorScene([300, 1000, 1000, 72]);
    const live = cards[2]!;
    scroll.setAnchorProvider(() => live);
    live.textContent = "a token";
    await land();
    expect(wrap.scrollTop, "the premise: on the pin, 50px above the end").toBe(end(wrap) - 50);
    gestures = 0;
    wheel(wrap, 3);
    expect(scroll.readerGestureOpen(), "the premise: the wheel opened a gesture").toBe(true);
    return { wrap, cards, live };
  }

  it.each(["removed", "grown"])(
    "keeps the text still while Following when a card above is %s, then lands on the pin",
    async (how) => {
      const { wrap, cards, live } = await followingGesture();
      const before = screenTop(wrap, live);
      const top = wrap.scrollTop;
      let seen = Number.NaN;
      // A removal is corrected in the child observer's microtask, before the next animation frame; a growth in the
      // resize delivery, which is where the frame paints.
      if (how === "removed") {
        cards[0]!.remove();
        requestAnimationFrame(() => {
          seen = screenTop(wrap, live) - before;
        });
        await frames(2);
      } else {
        const ro = afterDelivery(cards[0]!, () => {
          if (Number.isNaN(seen)) {
            seen = screenTop(wrap, live) - before;
          }
        });
        await frames(1);
        cards[0]!.style.height = "600px";
        await frames(2);
        ro.disconnect();
      }
      const moved = wrap.scrollTop - top;
      const writes = recordWrites(wrap);
      await closeGesture();
      await frames(2);

      expect({
        moved: Math.round(moved) === (how === "removed" ? -312 : 300),
        held: Math.abs(seen) <= 0.5,
        gestures,
        settle: writes.every((w) => Math.abs(w.delta) <= slack()),
        state: scroll.readingState(),
      }).toEqual({ moved: true, held: true, gestures: 0, settle: true, state: "following" });
    },
  );

  it("holds the text of a Reading reader in a gesture when a card above is removed", async () => {
    const { wrap, cards } = await floorScene([300, 1000, 1000]);
    await readAt(wrap, end(wrap) - 40);
    wheel(wrap, -3);
    const held = cards[2]!;
    const before = screenTop(wrap, held);

    cards[0]!.remove();
    await frames(1);

    expect(Math.abs(screenTop(wrap, held) - before) <= 0.5).toBe(true);
  });
});

describe("a size change inside the owner's own observer callback", () => {
  it("raises the floor before the hold writes, so a shrink in the next frame clamps nothing", async () => {
    const wrap = await reset();
    wrap.id = "messages-wrap";
    try {
      const v = document.createElement("div");
      v.className = "transcript-view is-active";
      messagesEl.append(v);
      view = v;
      const top = document.createElement("div");
      top.style.cssText = "height:200px;flex-shrink:0;";
      const row = document.createElement("div");
      row.className = "msg-row";
      row.style.containIntrinsicSize = "auto 20px";
      const body = document.createElement("div");
      body.style.cssText = "height:400px;width:100%;";
      row.append(body);
      const gapper = document.createElement("div");
      gapper.style.cssText = "height:300px;flex-shrink:0;";
      const shrinker = document.createElement("div");
      shrinker.style.cssText = "height:700px;flex-shrink:0;";
      scroll.attach({ el: v, scrollTop: 0, readingState: "following" });
      v.append(top, row, gapper, shrinker);
      await land();
      await readAt(wrap, end(wrap) - 20);
      expect(row.hasAttribute("data-near"), "the premise: the row is outside the lead").toBe(false);
      let after = Number.NaN;
      let at = Number.NaN;
      const io = new IntersectionObserver(
        (entries) => {
          if (entries.some((e) => e.isIntersecting)) {
            requestAnimationFrame(() => {
              at = wrap.scrollTop;
              shrinker.style.height = "100px";
              after = wrap.scrollTop;
            });
          }
        },
        { root: wrap, rootMargin: `${String(Math.round(wrap.clientHeight * 1.5))}px 0px` },
      );
      io.observe(row);
      await frames(2);

      wheel(wrap, -3);
      platformScroll(wrap, 800);
      await land(400);
      io.disconnect();

      expect({ near: row.hasAttribute("data-near"), clamped: after - at }).toEqual({
        near: true,
        clamped: 0,
      });
    } finally {
      wrap.id = "messagesWrap";
    }
  });

  it("raises the floor before the skeleton's hold writes, so a shrink in the next frame clamps nothing", async () => {
    skeleton.px = 120;
    const { wrap, cards } = await floorScene([300, 400]);
    await readAt(wrap, end(wrap) - 20);
    let after = Number.NaN;
    let at = Number.NaN;

    scroll.setLoadMore(() => undefined, true);
    requestAnimationFrame(() => {
      at = wrap.scrollTop;
      cards[1]!.style.height = "100px";
      after = wrap.scrollTop;
    });
    await frames(3);

    expect({
      prepended: document.getElementById("load-more-skeleton") !== null,
      held: at > 0,
      clamped: after - at,
    }).toEqual({ prepended: true, held: true, clamped: 0 });
  });
});
