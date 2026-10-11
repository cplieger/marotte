// The transcript's resize observers, over REAL layout: no undeliverable observation, whatever the
// scrollport is.

import { describe, it, expect, afterAll, beforeAll, beforeEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Entry, TurnState } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";

// NESTED as the shipped page nests them (static/index.html): `#messages-wrap` is `position:
// absolute; inset: 0` inside the outer wrapper, so it is the `offsetParent` of the whole transcript
// AND the scroller.
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
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

interface Tally {
  calls: number;
  entries: number;
}

const tallies = new Map<string, Tally>();

function tallyFor(site: string): Tally {
  let t = tallies.get(site);
  if (t === undefined) {
    t = { calls: 0, entries: 0 };
    tallies.set(site, t);
  }
  return t;
}

/** The first frame BELOW this file: the module and line that constructed the observer. A count
 *  says a loop happened; a site says whose. */
function constructionSite(): string {
  const stack = new Error("ro").stack ?? "";
  for (const line of stack.split("\n").slice(1)) {
    if (line.includes("scroll-observer.test.ts")) {
      continue;
    }
    const m = /([A-Za-z0-9._-]+\.ts)\??[^/]*?:(\d+):/.exec(line);
    if (m !== null) {
      return `${m[1] ?? "?"}:${m[2] ?? "?"}`;
    }
  }
  return "unknown";
}

const NativeResizeObserver = window.ResizeObserver;

/** Whether the engine is currently INSIDE a resize delivery. The invariant the last case asserts
 *  is about a moment rather than a count: a mutation of an observed child is safe in any other
 *  task and undeliverable here. */
let inDelivery = false;

/** A delegating wrapper rather than a subclass: the tally has to be reachable from the callback,
 *  and a derived constructor cannot touch anything of its own before `super()`. */
class ProbeResizeObserver implements ResizeObserver {
  private readonly inner: ResizeObserver;
  constructor(cb: ResizeObserverCallback) {
    const tally = tallyFor(constructionSite());
    this.inner = new NativeResizeObserver((entries, observer) => {
      tally.calls += 1;
      tally.entries += entries.length;
      inDelivery = true;
      try {
        cb(entries, observer);
      } finally {
        // Restored in a `finally`, so a callback that throws cannot leave the flag standing and
        // make every later case read as a violation.
        inDelivery = false;
      }
    });
  }
  observe(target: Element, options?: ResizeObserverOptions): void {
    this.inner.observe(target, options);
  }
  unobserve(target: Element): void {
    this.inner.unobserve(target);
  }
  disconnect(): void {
    this.inner.disconnect();
  }
}

// Installed BEFORE `scroll.ts` is imported, because that module builds its observers at import.
// Assigned rather than `vi.stubGlobal`'d: `unstubGlobals` is on, so a stub installed at collection
// time is restored before the first test.
window.ResizeObserver = ProbeResizeObserver as unknown as typeof ResizeObserver;

/** Every `ResizeObserver loop` the engine reported, whichever pass produced it. */
const loopErrors: string[] = [];
window.addEventListener("error", (e) => {
  if (e.message.includes("ResizeObserver loop")) {
    loopErrors.push(e.message);
  }
});

/** How many times `--scrollbar-w` was actually written. Zero means the fixture never reached the
 *  write, and the loop assertions below would be vacuous. */
let gutterWrites = 0;
const rootStyle = document.documentElement.style;
const nativeSetProperty = rootStyle.setProperty.bind(rootStyle);
rootStyle.setProperty = (name: string, value: string | null, priority?: string): void => {
  if (name === "--scrollbar-w") {
    gutterWrites += 1;
  }
  nativeSetProperty(name, value, priority);
};

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView } = await import("./messages.js");
const scroll = await import("./scroll.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

/** Turns and blocks per turn: the resting shape the loop reproduces on. Sixty cards is past the
 *  point where one observer carrying all of them makes the deferred observation set large. */
const TURNS = 60;
const BLOCKS = 12;

/** A GROSS-REGRESSION GUARD, not evidence about the invariant: measured tallies are 1-2 calls
 *  per site, and the 42-callback amplitude it is sized against comes from the
 *  streaming and momentum scenarios THIS fixture does not reproduce, so it sits 4-8x above the
 *  observed floor and far below the regression it names. */
const MAX_CALLS_PER_SITE = 8;

let seq = 0;

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    lane: "",
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

/** One prompt-opened turn: its `turn_open` header, `BLOCKS` paragraphs of prose, and its close.
 *  Consecutive `text` entries of one lane are ONE prose run, so the paragraphs land in a single
 *  bubble — the same wrapped-text volume per card the loop is measured over. */
function turnEntries(t: number): Entry[] {
  const id = `t${String(t)}`;
  const out: Entry[] = [
    sealed(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: `prompt ${String(t)}` },
      source: "prompt",
      n: t + 1,
    }),
  ];
  for (let i = 0; i < BLOCKS; i++) {
    out.push(
      sealed(id, i + 1, "text", {
        text: `turn ${String(t)} block ${String(i)} of some prose long enough to wrap on a narrow measure\n\n`,
      }),
    );
  }
  out.push(sealed(id, BLOCKS + 1, "turn_close", { outcome: "completed" }));
  return out;
}

function paint(): void {
  seq++;
  const id = `c-ro-${String(seq)}`;
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (let t = 0; t < TURNS; t++) {
    const entries = turnEntries(t);
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries, openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id, name: "c" }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(id);
  bumpMessages(id, "load");
}

/** Frames the settle waits for: the deferred write lands on the next one, the resize it causes
 *  is delivered on the one after, and the rest are margin. */
const SETTLE_FRAMES = 6;

/** Resolved from inside the callback, so the awaiting code runs in that same phase, ahead of any
 *  callback registered after this one. */
async function frame(): Promise<void> {
  await new Promise<void>((r) => {
    requestAnimationFrame(() => {
      r();
    });
  });
}

/** Let the engine finish delivering: several frames plus a macrotask, which is where a deferred
 *  observation lands and where the loop error would arrive. */
async function settle(): Promise<void> {
  for (let f = 0; f < SETTLE_FRAMES; f++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
  await new Promise<void>((r) => {
    setTimeout(r, 100);
  });
}

/** Per-site counts as they stood when the case started. A DELTA rather than a reset, because
 *  `scroll.ts` builds its two observers once, at import — clearing the map would drop them for
 *  the rest of the file and leave the bound below checking only the observers a paint happens to
 *  construct. */
let baseline = new Map<string, Tally>();

function snapshot(): Map<string, Tally> {
  return new Map([...tallies].map(([site, t]) => [site, { ...t }]));
}

function since(): Map<string, Tally> {
  const out = new Map<string, Tally>();
  for (const [site, t] of tallies) {
    const was = baseline.get(site) ?? { calls: 0, entries: 0 };
    out.set(site, { calls: t.calls - was.calls, entries: t.entries - was.entries });
  }
  return out;
}

function report(): string {
  return [...since()]
    .map(([site, t]) => `${site} ${String(t.calls)} calls / ${String(t.entries)} entries`)
    .join("; ");
}

// The subject IS frames, and a browser running the whole suite delivers them at 1Hz partway through
// (`__test-helpers__/frame-budget.ts`), so the per-test timeout has to be sized in seconds per
// frame waited for.
describe(
  "the transcript's resize observers over real layout",
  { timeout: testTimeoutFor(framesBudgetMs(SETTLE_FRAMES)) },
  () => {
    let style: HTMLStyleElement;

    beforeAll(() => {
      style = mountAppCSS();
    });

    afterAll(() => {
      style.remove();
      window.ResizeObserver = NativeResizeObserver;
      rootStyle.setProperty = nativeSetProperty;
    });

    beforeEach(() => {
      mountChatView();
      loopErrors.length = 0;
      baseline = snapshot();
    });

    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build, so without an
    // explicit height the absolutely-positioned scroller inside it is 0 tall and nothing overflows
    // — which is the one state that cannot reserve a gutter and therefore cannot reproduce the
    // write.
    for (const viewport of [600, 720]) {
      it(`delivers every observation at a ${String(viewport)}px scrollport`, async () => {
        const outer = document.getElementById("messages-wrap-outer")!;
        outer.style.height = `${String(viewport)}px`;
        paint();
        await settle();

        expect(loopErrors, report()).toEqual([]);
        const observed = since();
        // TWO observers from the chat's `ScrollController` (scroll-controller.ts) — the content one and
        // the gutter one — and between them at least one delivered entry, or the bound below is a loop
        // over nothing.
        const fromScroll = [...observed].filter(([site]) =>
          site.startsWith("scroll-controller.ts"),
        );
        expect(
          [fromScroll.length, fromScroll.reduce((n, [, t]) => n + t.entries, 0) > 0],
          report(),
        ).toEqual([2, true]);
        for (const [site, t] of observed) {
          expect(t.calls, `${site} — ${report()}`).toBeLessThanOrEqual(MAX_CALLS_PER_SITE);
        }
      });
    }

    // The resize path releases only when the end comes CLOSER, and a clamping shrink, a sentinel
    // crossing and a style mutation all reach the same re-derivation by other callers.
    it(
      "releases the deferred batch OUTSIDE the resize delivery",
      { timeout: testTimeoutFor(framesBudgetMs(SETTLE_FRAMES * 3)) },
      async () => {
        const outer = document.getElementById("messages-wrap-outer")!;
        outer.style.height = "600px";
        paint();
        const view = document.querySelector<HTMLElement>(".transcript-view.is-active")!;
        // An observed child whose height the case owns: `childObserver` re-observes on every
        // childList change, so appending one puts it in the ResizeObserver's set.
        const spacer = document.createElement("div");
        spacer.style.blockSize = "400px";
        spacer.style.flexShrink = "0";
        view.appendChild(spacer);
        await settle();

        // 50px above the end, inside the band, then parked there: `setUserScrolledUp` enters
        // Reading through `setState` and marks no input, so the reader does not own the scroller
        // and the promotion path below is reachable.
        scrollerEl.scrollTop = scrollerEl.scrollHeight - scrollerEl.clientHeight - 50;
        await settle();
        scroll.setUserScrolledUp(true);

        let deliveryAtFlush: boolean | null = null;
        scroll.deferWhileReading(() => {
          deliveryAtFlush = inDelivery;
          // What a released fold does: change the box of a child this observer watches.
          spacer.style.blockSize = "10px";
        });

        // 20px off the end: 50px of room becomes 30px, so the end came closer and the state is
        // released.
        spacer.style.blockSize = "380px";
        await settle();

        // `null` would mean the batch never ran, which is the vacuous pass this assertion has to
        // exclude as well.
        expect([deliveryAtFlush, loopErrors], report()).toEqual([false, []]);
      },
    );

    // The other half of that release: content appended BETWEEN the delivery and the frame that
    // applies it takes the reader off the edge with no input, no scroll event and no sentinel
    // crossing, so a measurement carried across the frame releases a reader who is 450px above the
    // end.
    it(
      "keeps the batch held when the content grows before the deferred apply",
      { timeout: testTimeoutFor(framesBudgetMs(SETTLE_FRAMES * 3)) },
      async () => {
        const outer = document.getElementById("messages-wrap-outer")!;
        outer.style.height = "600px";
        paint();
        const view = document.querySelector<HTMLElement>(".transcript-view.is-active")!;
        const spacer = document.createElement("div");
        spacer.style.blockSize = "400px";
        spacer.style.flexShrink = "0";
        view.appendChild(spacer);
        await settle();

        scrollerEl.scrollTop = scrollerEl.scrollHeight;
        await settle();
        scroll.setUserScrolledUp(true);

        let flushed = false;
        scroll.deferWhileReading(() => {
          flushed = true;
        });

        // 50px, inside BOTTOM_TOLERANCE_PX (100), so this delivery measures the reader at the edge
        // — the release the case above asserts.
        spacer.style.blockSize = "450px";
        await frame();
        await frame();
        // 450px more, well outside it. Nothing else can notice: no gesture, and the sentinel's own
        // crossing is not delivered until after this frame's apply.
        spacer.style.blockSize = "900px";
        await settle();

        expect([flushed, scroll.readingState(), loopErrors], report()).toEqual([
          false,
          "reading",
          [],
        ]);
      },
    );

    it("still reserves the gutter, so the pass above is not vacuous", () => {
      // The error only ever occurred on a pass that WROTE `--scrollbar-w`, so a run that never
      // wrote one would pass the two cases above having reproduced nothing.
      expect([gutterWrites > 0, rootStyle.getPropertyValue("--scrollbar-w")]).toEqual([
        true,
        expect.stringMatching(/^([1-9]\d*)px$/),
      ]);
    });
  },
);
