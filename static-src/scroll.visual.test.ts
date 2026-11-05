// What a reader sees of the chat transcript near its end, frame by frame: the real chat view under the app's
// stylesheet and live icons, the reader's inputs as a device delivers them, and the engine's own motion. CI runs it in
// Chromium at three device pixel ratios; `SCROLL_ENGINES=1 npx vitest --run --project browser-engines` runs it in
// Firefox and WebKit at the ratios and widths of the user's devices (`vitest.config.ts`).

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { server, userEvent } from "vitest/browser";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import "./__test-helpers__/device-scale.js";
import { scrollEnds, taskHop, touchEvent } from "./__test-helpers__/scroll-scene.js";

for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}
const outerEl = document.getElementById("messages-wrap-outer")!;
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
outerEl.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView } = await import("./messages.js");
const { scrollToBottom, readingState, readerGestureOpen } = await import("./scroll.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { initIconCrisp } = await import("./icon-crisp.js");
const { edgeSlack } = await import("./scroll-gesture.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

const protoScrollTo = HTMLElement.prototype.scrollTo as (
  this: HTMLElement,
  o: ScrollToOptions,
) => void;

const TURNS = 12;
const LONG = "the quick brown fox jumps over the lazy dog and keeps going ".repeat(5);
/** A clock row rewrites its text about once a second while a turn runs; frames are not throttled here, so the
 *  stand-in ticks by frame count. */
const CLOCK_FRAMES = 30;

function entry(turn: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turn}-e${String(at)}`,
    turn,
    kind,
    seq: at,
    ts: at + 1,
    lane: "",
    payload,
  } as Entry;
}

function turnEntries(turn: string, n: number): Entry[] {
  const out: Entry[] = [
    entry(turn, 0, "turn_open", {
      prompt: { id: `${turn}-p`, text: `prompt ${turn}` },
      source: "prompt",
      n,
    }),
  ];
  let at = 1;
  for (let r = 0; r < 3; r++) {
    out.push(entry(turn, at++, "text", { text: `part ${String(r)}: ${LONG}` }));
    out.push(entry(turn, at++, "thinking", { text: `step ${String(r)}` }));
    for (let c = 0; c < 2; c++) {
      out.push(
        entry(turn, at++, "tool_call", {
          id: `${turn}-tc${String(at)}`,
          title: `Run step ${String(r)}.${String(c)}`,
          kind: "execute",
          status: "completed",
          ts: at,
        }),
      );
    }
  }
  out.push(entry(turn, at, "turn_close", { outcome: "completed" }));
  return out;
}

/** Resumes on a task after the next frame: input and the engine's motion arrive in tasks, so a move made here is
 *  dispatched in the next frame's scroll steps, before any correction of that frame reads it. */
async function frame(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(r));
  await taskHop();
}

/** `n` frames and at least `n` frames' time at 60Hz: frames are not rate-limited here, and the owner's idle bound is
 *  both a frame count and a time. */
async function idle(n: number): Promise<void> {
  const until = performance.now() + n * (1000 / 60);
  for (let i = 0; i < n || performance.now() < until; i++) {
    await frame();
  }
}

const gap = (): number => scrollerEl.scrollHeight - scrollerEl.clientHeight - scrollerEl.scrollTop;
const end = (): number => scrollerEl.scrollHeight - scrollerEl.clientHeight;
/** The engine moving the scroller: the prototype's `scrollTo`, which the owner-write record does not see. */
const platformTo = (top: number): void => {
  protoScrollTo.call(scrollerEl, { top, behavior: "instant" });
};
const wheelTick = (deltaY: number): void => {
  scrollerEl.dispatchEvent(
    new WheelEvent("wheel", { deltaY, deltaMode: 1, bubbles: true, cancelable: true }),
  );
};

interface Sample {
  readonly st: number;
  readonly state: string;
  readonly texts: readonly (number | null)[];
  readonly icons: readonly (number | null)[];
}

/** One scenario's record: a sample per frame, the owner's writes, and the icon pass's style writes. */
class Recording {
  readonly samples: Sample[] = [];
  readonly writes: number[] = [];
  iconWrites = 0;
  idleFrom = 0;
  private on = true;
  private readonly texts: Element[];
  private readonly icons: { ic: Element; host: Element }[];
  private readonly icObs: MutationObserver;
  private clockAt = 0;

  constructor() {
    const box = scrollerEl.getBoundingClientRect();
    this.texts = [0.1, 0.5, 0.9]
      .map((f) => document.elementFromPoint(box.left + box.width / 2, box.top + box.height * f))
      .map((e) => e?.closest("p, .tool-call, .turn-footer, .turn-header, .turn") ?? null)
      .filter((e): e is Element => e !== null && scrollerEl.contains(e));
    this.icons = [...scrollerEl.querySelectorAll("svg.ic-ui")]
      .filter((ic) => {
        const r = ic.getBoundingClientRect();
        return r.width > 0 && r.bottom > box.top && r.top < box.bottom;
      })
      .slice(0, 24)
      .map((ic) => ({
        ic,
        host:
          ic.closest(".tool-call, .turn-footer, .turn-header, button, summary") ??
          ic.parentElement ??
          ic,
      }));
    // Only icons in view: the owner snaps a row's icons as the row nears the viewport, before anyone sees them.
    const tracked = new Set<Node>(this.icons.map(({ ic }) => ic));
    this.icObs = new MutationObserver((recs) => {
      this.iconWrites += recs.filter((r) => tracked.has(r.target)).length;
    });
    this.icObs.observe(scrollerEl, { subtree: true, attributes: true, attributeFilter: ["style"] });
    scrollerEl.scrollTo = ((arg?: number | ScrollToOptions, y?: number): void => {
      const top = typeof arg === "number" ? (y ?? 0) : (arg?.top ?? scrollerEl.scrollTop);
      this.writes.push(top);
      protoScrollTo.call(scrollerEl, typeof arg === "number" ? { left: arg, top } : (arg ?? {}));
    }) as typeof scrollerEl.scrollTo;
    scrollerEl.scrollBy = ((arg?: number | ScrollToOptions, y?: number): void => {
      const by = typeof arg === "number" ? (y ?? 0) : (arg?.top ?? 0);
      this.writes.push(scrollerEl.scrollTop + by);
      protoScrollTo.call(scrollerEl, { top: scrollerEl.scrollTop + by, behavior: "instant" });
    }) as typeof scrollerEl.scrollBy;
    const loop = (): void => {
      if (!this.on) {
        return;
      }
      this.sample();
      if (++this.clockAt % CLOCK_FRAMES === 0) {
        clockTick();
      }
      requestAnimationFrame(loop);
    };
    this.sample();
    requestAnimationFrame(loop);
  }

  private sample(): void {
    this.samples.push({
      st: scrollerEl.scrollTop,
      state: readingState(),
      texts: this.texts.map((e) => (e.isConnected ? e.getBoundingClientRect().top : null)),
      icons: this.icons.map(({ ic, host }) =>
        ic.isConnected && host.isConnected
          ? ic.getBoundingClientRect().top - host.getBoundingClientRect().top
          : null,
      ),
    });
  }

  markIdle(): void {
    this.idleFrom = this.samples.length;
  }

  stop(): void {
    this.on = false;
    this.icObs.disconnect();
    Reflect.deleteProperty(scrollerEl, "scrollTo");
    Reflect.deleteProperty(scrollerEl, "scrollBy");
  }

  get idleSamples(): Sample[] {
    return this.samples.slice(this.idleFrom);
  }

  /** `scrollTop` changes between idle frames. */
  idleMoves(): number {
    const s = this.idleSamples;
    return s.slice(1).filter((x, i) => x.st !== s[i]!.st).length;
  }

  /** The states the reader passed through, repeats collapsed: `["following", "reading"]` reads as `fr`. */
  stateSeq(): string {
    return this.samples
      .map((s) => s.state[0] ?? "")
      .join("")
      .replace(/(.)\1+/g, "$1");
  }

  /** Per tracked text, how far its content-space position (screen top plus `scrollTop`) ranged while idle. */
  textRange(): number[] {
    return this.texts.map((_, k) => range(this.idleSamples.map((s) => s.texts[k]! + s.st)));
  }

  /** Per tracked icon, how far its offset inside its host ranged over the whole scenario. */
  iconRange(): number[] {
    return this.icons.map((_, k) =>
      range(this.samples.map((s) => s.icons[k]).filter((v): v is number => v !== null)),
    );
  }

  /** The tracked icons whose offset inside their host moved, named by host. */
  iconsMoved(): string[] {
    return this.iconRange().flatMap((r, k) => {
      const o = this.icons[k]!;
      return r > 0.01
        ? [
            `${o.host.className.toString().split(" ")[0] ?? ""} ${o.ic.getAttribute("class") ?? ""} ${r.toFixed(3)}`,
          ]
        : [];
    });
  }

  plantIconShift(): () => void {
    const ic = this.icons[0]?.ic as SVGSVGElement | undefined;
    expect(ic, "the premise: an icon is in view").toBeDefined();
    const was = ic!.style.translate;
    const [x = 0, y = 0] = was.split(" ").map((v) => parseFloat(v) || 0);
    ic!.style.translate = `${String(x)}px ${String(y + 1)}px`;
    return () => {
      ic!.style.translate = was;
    };
  }
}

function range(v: readonly number[]): number {
  const n = v.filter((x) => Number.isFinite(x));
  return n.length === 0 ? 0 : Math.max(...n) - Math.min(...n);
}

/** The same-text rewrite of a running turn's clock: a mutation with no size change. */
function clockTick(): void {
  const hdr = scrollerEl.querySelector(".turn .turn-header");
  const tn = hdr === null ? null : document.createTreeWalker(hdr, NodeFilter.SHOW_TEXT).nextNode();
  if (tn instanceof Text) {
    tn.data = String(tn.data);
  }
}

/** The engine's animation of one gesture after its input: one smooth scroll, a single `scrollend` at its end, as a
 *  wheel tick's animation and touch momentum are. Per-frame instant writes would each end the gesture. */
async function animate(dist: number): Promise<void> {
  const ended = scrollEnds(scrollerEl);
  protoScrollTo.call(scrollerEl, { top: scrollerEl.scrollTop + dist, behavior: "smooth" });
  await ended;
}

/** A wheel tick up (`dist` < 0) or down, with the engine's tick animation. */
async function wheelGesture(dist: number): Promise<void> {
  wheelTick(Math.sign(dist) * 3);
  await animate(dist);
}

/** A finger drag up (`dist` < 0, the finger moving down the screen) or down for half the distance, each move
 *  scrolling the content, then momentum for the rest. */
async function touchGesture(dist: number): Promise<void> {
  const box = scrollerEl.getBoundingClientRect();
  const y0 = box.top + box.height / 2;
  const step = Math.trunc(dist / 8);
  scrollerEl.dispatchEvent(touchEvent("touchstart", scrollerEl, y0));
  for (let k = 1; k <= 4; k++) {
    scrollerEl.dispatchEvent(touchEvent("touchmove", scrollerEl, y0 - k * step));
    platformTo(scrollerEl.scrollTop + step);
    await frame();
  }
  scrollerEl.dispatchEvent(touchEvent("touchend", scrollerEl));
  await animate(dist - 4 * step);
}

/** A small element fully in view with no scroller between it and the transcript's: the engine's own wheel there
 *  scrolls the transcript, and the pointer reaches it with no scroll into view. */
function plainTarget(): Element {
  const box = scrollerEl.getBoundingClientRect();
  for (const y of [0.5, 0.4, 0.6, 0.3, 0.7, 0.2, 0.8]) {
    const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height * y);
    if (hit === null || !scrollerEl.contains(hit)) {
      continue;
    }
    const r = hit.getBoundingClientRect();
    let nested = false;
    for (let e: Element | null = hit; e !== null && e !== scrollerEl; e = e.parentElement) {
      const oy = getComputedStyle(e).overflowY;
      nested ||= (oy === "auto" || oy === "scroll") && e.scrollHeight > e.clientHeight;
    }
    if (!nested && r.top >= box.top && r.bottom <= box.bottom) {
      return hit;
    }
  }
  throw new Error("no plain element in view to wheel over");
}

/** A reader parked `d` px above the end with one wheel tick. */
async function parkAt(d: number): Promise<void> {
  wheelTick(-3);
  platformTo(end() - d);
  await idle(2);
}

function growLast(px: number): HTMLElement {
  const last = [...scrollerEl.querySelectorAll(".turn")].at(-1)!;
  const g = document.createElement("div");
  g.className = "v-grow";
  g.style.cssText = `height:${String(px)}px;margin:0;padding:0`;
  last.appendChild(g);
  return g;
}

/** Back on the end, Following, with nothing moving. */
async function atEnd(): Promise<void> {
  for (const g of scrollerEl.querySelectorAll(".v-grow")) {
    g.remove();
  }
  outerEl.style.height = `${String(viewportPx())}px`;
  scrollToBottom();
  // The icon pass settles too: a ratio change or a resize re-snaps every icon once.
  let iconWrites = 0;
  const icons = new MutationObserver((recs) => {
    iconWrites += recs.filter((r) => r.target instanceof SVGSVGElement).length;
  });
  icons.observe(scrollerEl, { subtree: true, attributes: true, attributeFilter: ["style"] });
  let still = 0;
  let last = "";
  while (still < 20) {
    await frame();
    const now = `${String(scrollerEl.scrollTop)}|${String(scrollerEl.scrollHeight)}|${readingState()}|${String(iconWrites)}`;
    still = now === last && !readerGestureOpen() ? still + 1 : 0;
    last = now;
  }
  icons.disconnect();
  expect(readingState(), "the premise: Following at the end").toBe("following");
  expect(gap(), "the premise: on the end").toBeLessThanOrEqual(edgeSlack());
}

async function scenario(
  act: (rec: Recording) => Promise<void>,
  idleFrames = 150,
): Promise<Recording> {
  await atEnd();
  const rec = new Recording();
  try {
    await act(rec);
    rec.markIdle();
    await idle(idleFrames);
  } finally {
    rec.stop();
  }
  return rec;
}

/** The checks every scenario owes: no icon moved inside its host, no style write from the icon pass, and the text in
 *  view held still in content space once input stopped. */
function stillAtRest(rec: Recording): void {
  expect({
    iconsMoved: rec.iconsMoved(),
    iconWrites: rec.iconWrites,
    textMoved: rec.textRange().filter((r) => r > 0.01),
    idleMoves: rec.idleMoves(),
  }).toEqual({ iconsMoved: [], iconWrites: 0, textMoved: [], idleMoves: 0 });
}

const engine = server.browser;
const initialDpr = window.devicePixelRatio;

/** Chromium's ratio override, from a hook: `expect.poll` is test-only. */
async function deviceScale(dpr: number | null): Promise<void> {
  await server.commands.setDeviceScaleFactor(dpr);
  const want = dpr ?? initialDpr;
  for (let f = 0; Math.abs(window.devicePixelRatio - want) > 1e-4; f++) {
    if (f > 120) {
      throw new Error(
        `the device pixel ratio stayed ${String(window.devicePixelRatio)}, wanted ${String(want)}`,
      );
    }
    await frame();
  }
}
const viewportPx = (): number => Math.min(720, window.innerHeight);
const DPRS = engine === "chromium" ? [1, 1.5, 3] : [window.devicePixelRatio];

beforeAll(async () => {
  mountAppCSS();
  outerEl.style.height = `${String(viewportPx())}px`;
  mountChatView();
  localStorage.clear();
  resetFoldState();
  const chat = "c-visual";
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (let i = 1; i <= TURNS; i++) {
    const id = `t${String(i)}`;
    turns.set(id, { entries: turnEntries(id, i), openEntries: new Map() });
    order.push(id);
  }
  setSessions([
    { ...makeSession({ id: chat, name: "c" }), turns, turn_order: order, turn_count: TURNS },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
  for (let i = TURNS - 3; i < TURNS; i++) {
    setTurnOpen(chat, `t${String(i)}`, true);
  }
  bumpMessages(chat, "shape");
  initIconCrisp();
  // Web fonts swapping in move icons inside their hosts once, before any scenario.
  await document.fonts.ready;
  await atEnd();
  await idle(60);
  await atEnd();
  expect(end(), "the premise: the transcript overflows several scrollports").toBeGreaterThan(
    3 * viewportPx(),
  );
  expect(
    scrollerEl.querySelectorAll("svg.ic-ui").length,
    "the premise: icons render",
  ).toBeGreaterThan(0);
}, 60000);

afterAll(async () => {
  if (engine === "chromium") {
    await deviceScale(null);
  }
});

describe("the transcript near its end", { timeout: 60000 }, () => {
  describe.each(DPRS)("the transcript's end at DPR %s", (dpr) => {
    beforeAll(async () => {
      if (engine === "chromium") {
        await deviceScale(dpr);
      }
      await atEnd();
    }, 30000);

    it("writes nothing and does not move while a Following reader sits idle on the end", async () => {
      const rec = await scenario(() => idle(1), 240);

      expect(rec.writes).toEqual([]);
      expect(rec.samples.filter((s) => s.st !== rec.samples[0]!.st)).toEqual([]);
      expect(rec.stateSeq()).toBe("f");
      stillAtRest(rec);
    });

    it("keeps a reader 1px above the end Following, with the gap kept and no write", async () => {
      const rec = await scenario(() => parkAt(1), 240);

      expect(rec.writes).toEqual([]);
      expect(readingState()).toBe("following");
      expect(Math.abs(gap() - 1)).toBeLessThanOrEqual(0.5);
      stillAtRest(rec);
    });

    it.each([30, 57])("leaves a reader %spx above the end Reading where they parked", async (d) => {
      let parked = 0;
      const rec = await scenario(async () => {
        await parkAt(d);
        parked = scrollerEl.scrollTop;
      }, 240);

      expect(rec.writes).toEqual([]);
      expect(readingState()).toBe("reading");
      expect(scrollerEl.scrollTop).toBe(parked);
      stillAtRest(rec);
    });

    it("absorbs a 1px growth at the end with no write, and follows the next one past the slack", async () => {
      const rec = await scenario(async (r) => {
        growLast(1);
        await idle(40);
        expect(r.writes, "the 1px growth is within the slack").toEqual([]);
        expect(Math.abs(gap() - 1)).toBeLessThanOrEqual(0.5);
        growLast(1);
        await idle(40);
      });

      expect(rec.writes).toHaveLength(1);
      expect(gap()).toBeLessThanOrEqual(0.5);
      expect(readingState()).toBe("following");
      stillAtRest(rec);
    });

    it("follows a 1px growth under a reader 1px above the end with one write", async () => {
      const rec = await scenario(async () => {
        await parkAt(1);
        growLast(1);
        await idle(40);
      });

      expect(rec.writes).toHaveLength(1);
      expect(gap()).toBeLessThanOrEqual(0.5);
      stillAtRest(rec);
    });

    it.each([100, 53])(
      "keeps a reader who wheeled %spx up with the engine's own wheel where they stopped",
      async (dy) => {
        let landed = 0;
        const rec = await scenario(async () => {
          const from = scrollerEl.scrollTop;
          await userEvent.wheel(plainTarget(), { delta: { y: -dy } });
          for (let f = 0; f < 120 && scrollerEl.scrollTop === from; f++) {
            await frame();
          }
          await idle(10);
          landed = scrollerEl.scrollTop;
        }, 220);

        expect(
          end() - landed,
          `the premise: the wheel moved the reader up (owner writes ${rec.writes.join(",")}, ${rec.stateSeq()})`,
        ).toBeGreaterThan(edgeSlack());
        expect(rec.writes).toEqual([]);
        expect(rec.stateSeq()).toBe("fr");
        expect(scrollerEl.scrollTop).toBe(landed);
        stillAtRest(rec);
      },
    );

    it.each([8, 30, 57, 90])(
      "keeps a reader whose wheel tick animated %spx up where it stopped",
      async (dist) => {
        const rec = await scenario(() => wheelGesture(-dist), 220);

        expect(rec.writes).toEqual([]);
        expect(Math.abs(gap() - dist)).toBeLessThanOrEqual(1);
        expect(readingState()).toBe("reading");
        stillAtRest(rec);
      },
    );

    it.each([30, 57, 90])(
      "keeps a reader whose drag and momentum went %spx up where it stopped",
      async (dist) => {
        const rec = await scenario(() => touchGesture(-dist), 220);

        expect(rec.writes).toEqual([]);
        expect(Math.abs(gap() - dist)).toBeLessThanOrEqual(1);
        expect(readingState()).toBe("reading");
        stillAtRest(rec);
      },
    );

    it.each([
      ["wheel", wheelGesture, 57],
      ["touch", touchGesture, 90],
    ] as const)(
      "rejoins the end when the reader's %s comes back down to it",
      async (_, move, dist) => {
        const rec = await scenario(async () => {
          await move(-dist);
          await idle(60);
          await move(dist);
        }, 200);

        expect(rec.writes).toEqual([]);
        expect(rec.stateSeq()).toBe("frf");
        expect(gap()).toBeLessThanOrEqual(edgeSlack());
        stillAtRest(rec);
      },
    );

    it.each([
      ["wheel", wheelGesture, 57, 8],
      ["touch", touchGesture, 57, 20],
    ] as const)(
      "leaves a reader whose %s came back down short of the end Reading where it stopped",
      async (_, move, up, down) => {
        const rec = await scenario(async () => {
          await move(-up);
          await idle(60);
          await move(down);
        }, 220);

        expect(rec.writes).toEqual([]);
        expect(Math.abs(gap() - (up - down))).toBeLessThanOrEqual(1);
        expect(readingState()).toBe("reading");
        stillAtRest(rec);
      },
    );

    it("leaves a scroll with no input (find in page) 50px up where it put the reader", async () => {
      const rec = await scenario(async () => {
        platformTo(scrollerEl.scrollTop - 50);
        await idle(2);
      }, 220);

      expect(rec.writes).toEqual([]);
      expect(Math.abs(gap() - 50)).toBeLessThanOrEqual(1);
      stillAtRest(rec);
    });

    it.each([1, 3])("rewrites no icon while the reader steps up %spx at a time", async (px) => {
      // From a Reading position: steps inside the slack at the end are not a reader leaving it.
      const rec = await scenario(async () => {
        await parkAt(100);
        for (let i = 0; i < 8; i++) {
          wheelTick(-3);
          protoScrollTo.call(scrollerEl, { top: scrollerEl.scrollTop - px, behavior: "instant" });
          await idle(12);
        }
      }, 240);

      expect(rec.writes).toEqual([]);
      stillAtRest(rec);
    });

    it("reports a planted 1px icon shift", async () => {
      const rec = await scenario(async (r) => {
        const undo = r.plantIconShift();
        await idle(6);
        undo();
      }, 30);

      // An engine paints the move on its device grid: at DPR 1.5, 1 or 2 device pixels.
      expect(Math.max(...rec.iconRange()), "the detector caught the plant").toBeGreaterThanOrEqual(
        0.6,
      );
    });

    it("keeps a Following reader's end on the bottom edge in every frame when a notice shrinks the scrollport", async () => {
      await atEnd();
      const gaps: number[] = [];
      const sample = (): void => {
        gaps.push(gap());
      };
      // At the end of the scroller's own resize delivery, the state the frame paints: the owner corrects a scrollport
      // change there, after animation-frame callbacks have run.
      const ro = new ResizeObserver(sample);
      ro.observe(scrollerEl);
      await idle(2);
      gaps.length = 0;
      await new Promise<void>((r) =>
        setTimeout(() => {
          outerEl.style.height = `${String(viewportPx() - 64)}px`;
          r();
        }, 0),
      );
      await idle(30);
      ro.disconnect();

      expect(gaps.length, "the premise: the scrollport resized").toBeGreaterThanOrEqual(1);
      expect(gaps.filter((g) => g > edgeSlack())).toEqual([]);
      expect(readingState()).toBe("following");
    });

    it("leaves a Reading reader untouched when a notice shrinks the scrollport", async () => {
      const rec = await scenario(async () => {
        await parkAt(57);
        const parked = scrollerEl.scrollTop;
        outerEl.style.height = `${String(viewportPx() - 64)}px`;
        await idle(30);
        expect(scrollerEl.scrollTop).toBe(parked);
      }, 60);

      expect(rec.writes).toEqual([]);
      expect(readingState()).toBe("reading");
    });
  });
});
