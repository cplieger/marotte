// `buildTurn` pins to the live edge only for a turn the paint recorded as an arrival, since the pin publishes a
// reader gesture that revokes the rail's pick; `renderCauseOf` tells a fetched window from a fresh chat's first
// prompt. Real scroll, layout and rail: a fake scroller emits no scroll event, so every case would pass ungated.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { SessionOverrides } from "./__test-helpers__/model.js";
import {
  FRAME_BUDGET_MS,
  framesBudgetMs,
  testTimeoutFor,
} from "./__test-helpers__/frame-budget.js";
import type { Session, TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type { TurnSummary } from "./rail-merge.js";

// These waits span several rounds of pin, settle and re-measure (16 frames measured at the 1Hz throttle), so the
// describes take `testTimeoutFor` of this budget or the deadline preempts the wait as a bare timeout.
const PIN_CONTEST_BUDGET_MS = framesBudgetMs(16);

// The DOM the import graph resolves at load, nested as the page nests it.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
// Production's `overflow-anchor: none`: without it Chromium's scroll anchoring moves scrollTop when older turns
// land above, which is the number these cases read.
wrap.style.cssText = "height:300px;overflow-y:auto;overflow-anchor:none;position:relative;";
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
// Deterministic boxes; `t3` is short so an offset-derived mark would name its taller neighbour.
const style = document.createElement("style");
style.textContent =
  `.turn{block-size:200px}[data-reconcile-key="t3"]{block-size:40px}` +
  // The rail's marker capacity comes from its measured height, so a boxless rail holds one marker.
  `.turn-rail{position:absolute;inset-block-start:0;block-size:400px}`;
document.head.appendChild(style);

// The rail's turn index and the pagination door are staged network reads.
const { served, apiGetMock } = vi.hoisted(() => ({
  served: { turns: [] as unknown[] },
  apiGetMock: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: apiGetMock,
  // Present-but-inert so real-ESM linking succeeds.
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const rail = await import("./turn-rail.js");
const { loadMessages } = await import("./store-load.js");

messages.mountChatView();

const MINUTE = 60_000;

// --- Fixtures -------------------------------------------------------------------------

function session(id: string, over: SessionOverrides = {}): Session {
  return makeSession({ id, name: id, ...over });
}

/** A sealed entry of any kind at `seq`. */
function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  };
}

/** A reader-sent turn open (`source: "prompt"`): the trigger is one half of the pin's gate, arrivals the other. */
function turnOpen(turnID: string, n: number): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text: `prompt ${turnID}` },
    source: "prompt",
    n,
  });
}

/** Turn `n` as the entries that make one: its open, one sealed reply, its close. */
function turnEntries(n: number): Entry[] {
  const id = `t${String(n)}`;
  return [
    turnOpen(id, n),
    sealed(id, 1, "text", { text: `reply ${String(n)}` }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** Seat whole turns on a session, the shape a page GET lands. */
function seat(s: Session, ns: readonly number[], at: "front" | "back"): void {
  const ids: string[] = [];
  for (const n of ns) {
    const entries = turnEntries(n);
    const id = `t${String(n)}`;
    s.turns.set(id, { entries, openEntries: new Map() });
    ids.push(id);
  }
  if (at === "front") {
    s.turn_order.unshift(...ids);
  } else {
    s.turn_order.push(...ids);
  }
}

function chatWith(id: string, ns: readonly number[], over: SessionOverrides = {}): Session {
  const s = session(id, over);
  seat(s, ns, "back");
  s.turn_count = Math.max(s.turn_count, ...ns, 0);
  return s;
}

function summary(n: number): TurnSummary {
  return { id: `t${String(n)}`, n, outcome: "completed", ts: n * MINUTE };
}

/** Poll `pred` until it holds; `what` is the timeout's sentence. Bounded by frames, since rAF throttles mid-run. */
async function until(pred: () => boolean, what: string, budget = FRAME_BUDGET_MS): Promise<void> {
  const deadline = Date.now() + budget;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(budget)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

/** A fresh chat id: `setActive` is a no-op for the active id, and the rail keys per-chat index records on it. */
let seq = 0;
function nextChat(): string {
  seq += 1;
  return `c${String(seq)}`;
}

/** The active view's turn cards, in document order. */
function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

function cardFor(turnID: string): HTMLElement | null {
  return (
    messages
      .activeTranscriptView()
      ?.querySelector<HTMLElement>(`:scope > [data-reconcile-key="${turnID}"]`) ?? null
  );
}

function markers(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".turn-rail > .rail-marker")];
}

function markerFor(n: number): HTMLElement {
  const hit = markers().find((m) => m.firstChild?.textContent === String(n));
  if (hit === undefined) {
    throw new Error(`no rail marker for turn ${String(n)}`);
  }
  return hit;
}

/** The markers the rail claims a position with; one of `data-current` / `data-selected` is written per render. */
function marked(): HTMLElement[] {
  return markers().filter(
    (m) => m.dataset["selected"] !== undefined || m.dataset["current"] !== undefined,
  );
}

function atLiveEdge(): boolean {
  return wrap.scrollTop === wrap.scrollHeight - wrap.clientHeight;
}

/**
 * Wait until the scroller stops moving on its own: a queued pin frame overwrites a scroll written before it.
 * Three identical reads is one frame with no write.
 */
async function quiet(): Promise<void> {
  let last = -1;
  let stable = 0;
  await until(
    () => {
      if (wrap.scrollTop === last) {
        stable += 1;
      } else {
        stable = 0;
        last = wrap.scrollTop;
      }
      return stable >= 3;
    },
    "the scroller to stop moving",
    PIN_CONTEST_BUDGET_MS,
  );
}

/**
 * Move the scroller as the reader: an input event, then the position. `scroll.ts` reads intent off the input, and
 * only an upward aim enters Reading.
 */
function readerScrollTo(top: number): void {
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -1 : 1 }));
  wrap.scrollTop = top;
}

/** Park the reader at `top` with a real gesture, confirming both the listener's verdict and the position. */
async function park(top: number): Promise<void> {
  await quiet();
  readerScrollTo(top);
  await until(
    () => {
      // The pin re-writes the same position every rAF, so positional stability cannot prove no frame is queued.
      if (wrap.scrollTop !== top) {
        readerScrollTo(top);
        return false;
      }
      return scroll.readingState() === "reading";
    },
    `the reader parked at ${String(top)}`,
    PIN_CONTEST_BUDGET_MS,
  );
}

/** Mount the active chat and wait for a card per turn; the wait covers observers and the paint's rAF. */
async function mount(s: Session): Promise<void> {
  store.setSessions([s]);
  store.setActive(s.id);
  const want = s.turn_order.length;
  await until(
    () => messages.activeTranscriptView() !== null && cards().length === want,
    `${s.id}'s first paint to mount ${String(want)} card(s)`,
  );
}

function requireSession(chatID: string): Session {
  const s = store.get(chatID);
  if (s === undefined) {
    throw new Error(`no session ${chatID}`);
  }
  return s;
}

/** The older-page prepend as `loadMessages` applies it: fetched turns in front, one `load` bump. */
function prepend(chatID: string, ns: readonly number[], hasMore = false): void {
  const s = requireSession(chatID);
  seat(s, ns, "front");
  s.has_more = hasMore;
  store.bumpMessages(chatID, "load");
}

/**
 * The newest-page fetch as `loadMessages` applies it, one `load` bump. The cause is restated, not driven, since
 * `store-load.js` is mocked here.
 */
function loadWindow(chatID: string, ns: readonly number[], hasMore = false): void {
  const s = requireSession(chatID);
  s.turns = new Map<string, TurnState>();
  s.turn_order = [];
  seat(s, ns, "back");
  s.turn_count = Math.max(0, ...ns);
  s.has_more = hasMore;
  store.bumpMessages(chatID, "load");
}

beforeEach(() => {
  messages.teardownAll();
  rail.resetTurnRail();
  store.setActive("");
  store.setSessions([]);
  served.turns = [];
  apiGetMock.mockImplementation((path: string) =>
    Promise.resolve(path.endsWith("/turns") ? ({ turns: served.turns } as never) : null),
  );
  vi.mocked(loadMessages).mockReset();
});

describe(
  "the live-edge pin a turn mount asks for",
  { timeout: testTimeoutFor(PIN_CONTEST_BUDGET_MS) },
  () => {
    it("publishes a reader gesture for a turn the reader just sent", async () => {
      // The reader asked for this turn, so the pin takes them there even from a parked position.
      const chat = nextChat();
      await mount(chatWith(chat, [1, 2, 3]));
      await park(120);
      expect(scroll.readingState()).toBe("reading");

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      store.openTurn(chat, turnOpen("t4", 4));
      await until(() => cardFor("t4") !== null, "the sent turn's card to mount");
      // Settle before unsubscribing so a second publish is caught; a live-edge wait could not fail here.
      await quiet();
      off();

      expect(seen).toHaveBeenCalledTimes(1);
    });

    it("publishes for the first turn of a chat that had nothing in it", async () => {
      // A fresh chat's first prompt: a one-turn transcript cannot scroll, so only the pin can publish a gesture.
      const chat = nextChat();
      await mount(chatWith(chat, []));
      expect(cards()).toHaveLength(0);

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      store.openTurn(chat, turnOpen("t1", 1));
      await until(() => seen.mock.calls.length > 0, "the pin to publish its gesture");
      off();

      expect(cardFor("t1")).not.toBeNull();
      expect(seen).toHaveBeenCalledTimes(1);
    });

    it("stays silent for a turn card a pagination prepend built", async () => {
      // The live-edge assertion is the control that stops this passing because nothing mounted.
      const chat = nextChat();
      await mount(chatWith(chat, [4, 5, 6], { turn_count: 6, has_more: true }));
      await park(120);

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      prepend(chat, [1, 2, 3]);
      await until(() => cards().length === 6, "the prepended page's cards to mount");
      off();

      expect(cardFor("t1")).not.toBeNull();
      // Not the exact park offset: a prepend runs inside `preserveReadingPosition`, which moves the number by design.
      expect(atLiveEdge()).toBe(false);
      expect(seen).toHaveBeenCalledTimes(0);
    });

    it("stays silent for a turn card a chat-switch replay built", async () => {
      // The incoming chat's turns are a replay, not turns the reader just sent.
      const first = nextChat();
      const second = nextChat();
      await mount(chatWith(first, [1, 2]));
      store.setSessions([chatWith(first, [1, 2]), chatWith(second, [1, 2, 3])]);

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      store.setActive(second);
      await until(() => cards().length === 3, "the switched-to chat's cards to mount");
      // An opened chat still lands at the live edge through the streaming auto-scroll, not this mount.
      await until(atLiveEdge, "the opened chat to land at the live edge");
      off();

      expect(seen).toHaveBeenCalledTimes(0);
    });

    it("stays silent for the window a cold open's own fetch filled", async () => {
      // A non-resident chat paints empty first, so its fetched window looks like a fresh chat; the paint's cause
      // separates them for both the pin and the animation.
      const chat = nextChat();
      await mount(chatWith(chat, []));

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      loadWindow(chat, [1, 2, 3]);
      await until(() => cards().length === 3, "the fetched window's cards to mount");
      await until(atLiveEdge, "the opened chat to land at the live edge");
      off();

      expect(seen).toHaveBeenCalledTimes(0);
      // A replay animates nothing, or every row of a reopened conversation would fade in together.
      expect(cards().filter((c) => c.hasAttribute("data-chat-entry"))).toHaveLength(0);
    });
  },
);

describe(
  "a rail jump onto a non-resident turn",
  { timeout: testTimeoutFor(PIN_CONTEST_BUDGET_MS) },
  () => {
    /**
     * Turns 4..6 resident of 6, the index fetched, and a door answering 1..3. No window base: a card's ordinal is the
     * session-absolute `turn_open.n`.
     */
    async function pagedScene(): Promise<string> {
      const chat = nextChat();
      await mount(chatWith(chat, [4, 5, 6], { turn_count: 6, has_more: true }));
      served.turns = [1, 2, 3, 4, 5, 6].map(summary);
      await rail.loadTurnRail(chat);
      await until(() => markers().length === 6, "the rail's own index to render its markers");
      vi.mocked(loadMessages).mockImplementation((chatID: string) => {
        prepend(chatID, [1, 2, 3]);
        return Promise.resolve(true);
      });
      return chat;
    }

    it("keeps the pick the click set, and marks the clicked turn rather than its neighbour", async () => {
      // An ungated prepend pin would revoke the pick before the jump resolved; `t3` is the short card.
      await pagedScene();

      markerFor(3).click();
      // The target card is resident and the jump's `finally` has cleared its pending state.
      await until(
        () => cardFor("t3") !== null && markers().every((m) => m.dataset["pending"] === undefined),
        "the paged jump to resolve",
      );
      // The landing's own scroll event arrives a frame later, so wait for it.
      await quiet();

      expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
      expect(marked().map((m) => m.firstChild?.textContent)).toEqual(["3"]);
      expect(markerFor(3).getAttribute("aria-current")).toBe("true");
      // `data-selected` marks the pick, `data-current` the offset-derived turn; with the gate deleted only this moves.
      expect(markerFor(3).dataset["selected"]).toBe("");
      expect(markerFor(3).dataset["current"]).toBeUndefined();
    });

    it("leaves the clicked turn's top on the reading line once the landing settles", async () => {
      // The turn sits on the scroller's reading line after the correction loop, not just the first scroll.
      await pagedScene();

      markerFor(3).click();
      // Wait for the correction loop's exit: under load the scroller pauses between the two scrolls.
      await until(
        () => cardFor("t3") !== null && markers().every((m) => m.dataset["pending"] === undefined),
        "the paged jump's correction loop to finish",
      );
      await quiet();

      const card = cardFor("t3");
      expect(card).not.toBeNull();
      // Rects in the scrollport's frame: a turn card's offsetParent is its own row.
      const fromTop = (card?.getBoundingClientRect().top ?? 0) - wrap.getBoundingClientRect().top;
      // LANDING_TOLERANCE_PX hardcoded, so the assertion does not agree with whatever the module believes.
      expect(Math.abs(fromTop - scroll.readingLineOffset())).toBeLessThanOrEqual(8);
    });
  },
);
