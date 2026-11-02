// A reader at the live edge stays there when the last turn grows under their own click (its footer's
// details opening), and a reader scrolled up is not moved. Real layout, the shipped stylesheet and
// the real scroller: a fake one emits no scroll event and no resize delivery.
import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import { page as browser, userEvent } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeSession } from "./__test-helpers__/model.js";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Entry } from "./wire/types.gen.js";
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:420px;overflow-y:auto;position:relative;";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}

vi.mock("./api-client.js", async (importOriginal) => {
  const failed = (): Promise<unknown> =>
    Promise.resolve({ ok: false, status: 0, data: null, error: "" });
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGet: vi.fn(failed),
    apiGetOrError: vi.fn(failed),
  };
});
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoad>()),
  loadMessages: vi.fn(),
  loadOlderTurns: vi.fn(),
  loadList: vi.fn(),
}));

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

const BUDGET = framesBudgetMs(24);

let style: HTMLStyleElement;
beforeAll(() => {
  style = mountAppCSS();
});
afterAll(async () => {
  style.remove();
  await browser.viewport(1280, 720);
});

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

/** A settled turn with a long reply, so the transcript scrolls and every footer has details. */
function turnEntries(n: number): Entry[] {
  const id = `t${String(n)}`;
  return [
    sealed(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: `prompt ${String(n)}` },
      source: "prompt",
      n,
    }),
    sealed(id, 1, "text", { text: `reply ${String(n)} `.repeat(60) }),
    sealed(id, 2, "turn_close", { outcome: "completed", credits: 1.5, tokens: 1200 }),
  ];
}

let seq = 0;
async function openChat(): Promise<void> {
  seq += 1;
  const id = `c-follow-${String(seq)}`;
  const s = makeSession({ id, name: id });
  for (const n of [1, 2, 3, 4, 5]) {
    s.turns.set(`t${String(n)}`, { entries: turnEntries(n), openEntries: new Map() });
    s.turn_order.push(`t${String(n)}`);
  }
  s.turn_count = 5;
  store.setSessions([s]);
  store.setActive(id);
  await until(() => lastFooter() !== null, "the last turn's footer");
  await settled();
}

/** Wait until the transcript's height holds still across a few frames: deferred parts and skipped
 *  boxes take their real size after the first paint. */
async function settled(): Promise<void> {
  let last = -1;
  let still = 0;
  await until(() => {
    const h = wrap.scrollHeight;
    still = h === last ? still + 1 : 0;
    last = h;
    return still >= 5;
  }, "the transcript to settle");
}

function lastFooter(): HTMLElement | null {
  const turns = messages.activeTranscriptView()?.querySelectorAll<HTMLElement>(":scope > .turn");
  const last = turns?.[turns.length - 1];
  return last?.querySelector<HTMLElement>(".turn-footer") ?? null;
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + BUDGET;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 16));
  }
}

const maxTop = (): number => wrap.scrollHeight - wrap.clientHeight;

// The controller's reader window reads `Date.now()`. A Date that runs with real time and can also
// jump lets `wheelTo` step past the window without sleeping it out, while every window the scene
// opens still closes no later than it would on the wall clock.
beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"], shouldAdvanceTime: true });
});
afterAll(() => {
  vi.useRealTimers();
});

const twoFrames = (): Promise<unknown> =>
  new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

/** Resolve on the scroller's `scrollend`: the shipped stylesheet scrolls it smoothly, so a
 *  `scrollTop` write lands over several frames. */
function scrollEnds(): Promise<void> {
  return new Promise((resolve, reject) => {
    const bound = setTimeout(() => {
      reject(
        new Error(`no scrollend within ${String(BUDGET)}ms (scrollTop ${String(wrap.scrollTop)})`),
      );
    }, BUDGET);
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

/** The reader's own wheel to `top`, so the controller sees a reader stating a position, then the
 *  reader's window passing: the scroll lands inside it, and the clock moves past it. */
async function wheelTo(top: number): Promise<void> {
  const moves = Math.abs(wrap.scrollTop - Math.max(0, Math.min(top, maxTop()))) >= 1;
  const landed = moves ? scrollEnds() : Promise.resolve();
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -100 : 100 }));
  wrap.scrollTop = top;
  await landed;
  vi.advanceTimersByTime(400);
  await twoFrames();
}

type Press = "click" | "tap" | "wobble" | "space";

/** A tap the way a phone delivers one: the touch events bubble through the scroller, then the click.
 *  `drift` is how far the finger moves inside the tap, which WebKit reports as `touchmove`s. */
function tap(target: HTMLElement, drift = 0): void {
  const touch = (type: string, touches: { clientX: number; clientY: number }[]): Event =>
    Object.assign(new Event(type, { bubbles: true }), { touches });
  const r = target.getBoundingClientRect();
  const x = r.left + 4;
  const y = r.top + 4;
  target.dispatchEvent(touch("touchstart", [{ clientX: x, clientY: y }]));
  if (drift > 0) {
    target.dispatchEvent(touch("touchmove", [{ clientX: x + drift / 2, clientY: y + drift / 2 }]));
    target.dispatchEvent(touch("touchmove", [{ clientX: x + drift, clientY: y - drift }]));
  }
  target.dispatchEvent(touch("touchend", []));
  target.click();
}

/** Open the footer's details the way `how` presses, and wait for its slide to land. */
async function openDetails(footer: HTMLElement, how: Press): Promise<void> {
  const btn = footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")!;
  if (how === "click") {
    btn.click();
  } else if (how === "tap") {
    tap(btn);
  } else if (how === "wobble") {
    tap(btn, 4);
  } else {
    btn.focus({ preventScroll: true });
    await userEvent.keyboard(" ");
  }
  expect(btn.getAttribute("aria-expanded"), "the premise: the details opened").toBe("true");
  await Promise.all(footer.getAnimations().map((a) => a.finished));
  await settled();
}

for (const [w, h] of [
  [390, 844],
  [1280, 900],
] as const) {
  describe(`at ${String(w)}px`, { timeout: testTimeoutFor(BUDGET * 4) }, () => {
    for (const how of ["click", "tap", "wobble", "space"] as const) {
      it(`keeps a reader at the live edge on it when a ${how} opens the last turn's details`, async () => {
        await browser.viewport(w, h);
        await openChat();
        await wheelTo(maxTop());
        await settled();
        expect(maxTop() - wrap.scrollTop, "the premise: at the live edge").toBeLessThanOrEqual(1);
        const footer = lastFooter()!;
        const before = wrap.scrollHeight;
        await openDetails(footer, how);
        expect(wrap.scrollHeight, "the premise: the turn grew").toBeGreaterThan(before + 20);
        expect(maxTop() - wrap.scrollTop, "still at the live edge").toBeLessThanOrEqual(1);
        expect(
          footer.getBoundingClientRect().bottom,
          "the details are on screen",
        ).toBeLessThanOrEqual(wrap.getBoundingClientRect().bottom + 1);
      });
    }

    for (const how of ["click", "tap", "wobble"] as const) {
      it(`leaves a reader scrolled up where they are when a ${how} opens a footer`, async () => {
        await browser.viewport(w, h);
        await openChat();
        await wheelTo(maxTop());
        await settled();
        await wheelTo(Math.round(maxTop() / 3));
        await settled();
        const top = wrap.scrollTop;
        const box = wrap.getBoundingClientRect();
        const footer = [
          ...messages.activeTranscriptView()!.querySelectorAll<HTMLElement>(".turn-footer"),
        ].find((f) => {
          const r = f.getBoundingClientRect();
          return r.top >= box.top && r.bottom <= box.bottom;
        });
        expect(footer, "the premise: a footer in view").toBeDefined();
        await openDetails(footer!, how);
        expect(Math.abs(wrap.scrollTop - top)).toBeLessThanOrEqual(4);
      });
    }
  });
}
