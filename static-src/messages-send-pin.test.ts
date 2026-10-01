// ---------------------------------------------------------------------------
// WHICH turn mounts ask for the live edge.
//
// `buildTurn` pins for a turn with a prompt trigger the paint recorded as an
// ARRIVAL, and that pin publishes a READER GESTURE (`onReaderGesture`), which
// revokes the rail's pick. So the gate answers "did the reader just send it",
// not "does this turn have a trigger" — a replay, a refetched window and a
// prepend all mount triggered turns. `appendNewIds` is the answer and
// `renderCauseOf` is what tells a FETCHED window from a fresh chat's first
// prompt: both paint with no tail behind them and want opposite answers. The
// paged rail jump is where that meets the live defect, an ungated pin revoking
// the pick its own click had just set.
//
// REAL scroll over REAL layout, and a REAL rail: `scroll.test.ts`'s fake emits no
// scroll event, so under it every case here passes with the gate deleted. A
// negative assertion waits for the POSITIVE precondition that would have produced
// the publish and asserts afterwards; every wait polls an observable, see `until`.
// ---------------------------------------------------------------------------
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

// This file needs MORE than the suite's shared frame budget, so it states its own.
// Its waits do not poll a single settled value: `quiet` waits for the scroller to
// stop moving and `park` contests the controller's own re-assert window, so each
// one spans several rounds of pin, settle and re-measure rather than one delivery.
// 16 frames is the measured span of that contest at the 1Hz throttle. Both describes
// below take `testTimeoutFor` of THIS budget rather than the default, or the
// per-test deadline preempts the wait and the failure reads as a bare timeout
// naming no assertion — the exact defect `frame-budget.ts` warns about.
const PIN_CONTEST_BUDGET_MS = framesBudgetMs(16);

// The DOM the renderer's import graph resolves at load, nested the way the page
// nests it: the rail mounts in the positioned OUTER wrapper, the scroller is the
// wrapper, and `#messages` holds one `.transcript-view` per resident chat.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
// `overflow-anchor: none` is production's (css/13-messages.css) and it is
// load-bearing here: without it Chromium's own scroll anchoring adjusts scrollTop
// when a page of older turns lands above the reader, which is the number these
// cases read to decide whether a pin happened.
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
// Turn cards get their height from a stylesheet in production; these cases need
// deterministic boxes, so the scene declares them. `t3` is deliberately SHORT: the
// paged case asserts that the mark lands on the turn that was CLICKED rather than
// on the taller neighbour an offset-derived mark would name.
const style = document.createElement("style");
style.textContent =
  `.turn{block-size:200px}[data-reconcile-key="t3"]{block-size:40px}` +
  // The rail's marker capacity is computed from its own measured height
  // (`maxMarkers`), so a rail with no box holds one marker and no per-turn
  // marker exists to click.
  `.turn-rail{position:absolute;inset-block-start:0;block-size:400px}`;
document.head.appendChild(style);

// The rail's session-wide index is its own fetch (`GET /api/chats/{id}/turns`), and
// the pagination door is a network read. Both are staged: what is under test is the
// sequencing around them.
const { served, apiGetMock } = vi.hoisted(() => ({
  served: { turns: [] as unknown[] },
  apiGetMock: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: apiGetMock,
  // Present-but-inert so real-ESM linking succeeds: this graph reaches the run
  // store and the window read, neither of which is this file's subject.
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

/** The entry that OPENS a turn the reader sent: `source: "prompt"`, so the
 *  projection gives the turn a trigger and the card a header. That trigger is one
 *  half of the pin's gate; the paint's arrivals set is the other. */
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

/** Poll `pred` until it holds; `what` is the sentence a timeout reads as. Bounded by
 *  the suite's FRAME budget rather than a wall-clock guess, because this browser
 *  throttles rAF partway through a full run (`__test-helpers__/frame-budget.ts`). */
async function until(pred: () => boolean, what: string, budget = FRAME_BUDGET_MS): Promise<void> {
  const deadline = Date.now() + budget;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(budget)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

/** A chat id no earlier case has used. Two module states make this necessary
 *  rather than tidy: `setActive` is a no-op for the id already active (so a reused
 *  id paints nothing and the case runs against an empty view), and the rail's own
 *  per-chat index records are keyed by it. */
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

/** The markers the rail is CLAIMING a position with. Exactly one of `data-current`
 *  and `data-selected` is written per render, so this answers one element once any
 *  turn has been placed. */
function marked(): HTMLElement[] {
  return markers().filter(
    (m) => m.dataset["selected"] !== undefined || m.dataset["current"] !== undefined,
  );
}

function atLiveEdge(): boolean {
  return wrap.scrollTop === wrap.scrollHeight - wrap.clientHeight;
}

/** Wait until the scroller has stopped moving on its own. Required before a gesture:
 *  a paint's pin reaches the live edge through a rAF, so a scroll written while that
 *  frame is queued is overwritten by it (measured: a park at 120 came back as the
 *  live edge). Three identical reads is one frame with no write in it. */
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

/** Move the scroller AS THE READER: the input event that says whose scroll it is,
 *  then the position they reach.
 *
 *  Both halves are load-bearing. `scroll.ts` reads intent off the INPUT, so a bare
 *  `scrollTop` assignment is the shape the PLATFORM produces and is deliberately
 *  not read as the reader stating a position; the wheel's direction has to be the
 *  one that would have brought them there, because only an UPWARD aim enters
 *  Reading. */
function readerScrollTo(top: number): void {
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -1 : 1 }));
  wrap.scrollTop = top;
}

/** Park the reader at `top` with a real gesture, so the state is the listener's
 *  own verdict rather than a seeded field — and CONFIRMED, both halves: the
 *  verdict the listener wrote on the event, and the position surviving it. */
async function park(top: number): Promise<void> {
  await quiet();
  readerScrollTo(top);
  await until(
    () => {
      // Re-assert until it sticks: the pin re-writes the SAME position every rAF,
      // so `quiet()`'s positional stability cannot prove no frame is still queued.
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

/** Mount `chatID` as the active chat and wait for its first paint to have produced
 *  a card per turn. `setActive` paints synchronously; what the wait covers is the
 *  observers and the rAF the paint hands work to. */
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

/** The older-page prepend, exactly as `loadMessages`' own older-page branch applies
 *  it: the fetched turns in front of the resident window, one `load` bump. */
function prepend(chatID: string, ns: readonly number[], hasMore = false): void {
  const s = requireSession(chatID);
  seat(s, ns, "front");
  s.has_more = hasMore;
  store.bumpMessages(chatID, "load");
}

/** The newest-page fetch, as `loadMessages`' own newest-page branch applies it: the
 *  server's window in place of the resident one, one `load` bump.
 *
 *  The CAUSE is the one thing this helper restates rather than drives, because
 *  `store-load.js` is mocked out of this file's graph; that `loadMessages` really
 *  announces a fetched window as `load` is pinned at the production call site, in
 *  store-load.test.ts. */
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
      // The genuine case, and the one the gate must not cost: the reader asked for
      // this turn, so the pin takes them to it even though they were parked further
      // up, and anything holding a position they have now abandoned has to hear it.
      const chat = nextChat();
      await mount(chatWith(chat, [1, 2, 3]));
      await park(120);
      expect(scroll.readingState()).toBe("reading");

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      store.openTurn(chat, turnOpen("t4", 4));
      await until(() => cardFor("t4") !== null, "the sent turn's card to mount");
      // Settle before unsubscribing so a second publish is caught. Not a live-edge
      // wait: `pinToLiveEdge` also sets Following, so `autoScrollIfAnchored` reaches
      // the bottom without the pin and that wait cannot fail.
      await quiet();
      off();

      expect(seen).toHaveBeenCalledTimes(1);
    });

    it("publishes for the first turn of a chat that had nothing in it", async () => {
      // The paint before this one had no TAIL to append past, which is not the same
      // as nothing having arrived: this is the reader's first prompt in a fresh chat,
      // the one appended-tail paint with no tail behind it. A one-turn transcript
      // cannot scroll, so the gesture is the observable — and with no scroll event
      // possible here, the pin is the only thing that can publish one.
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
      // The reader is reading old history; the turns a page of it mounts are not
      // theirs. The live-edge assertion is the control that stops this passing
      // because nothing mounted: a pin would take them to the live edge.
      const chat = nextChat();
      await mount(chatWith(chat, [4, 5, 6], { turn_count: 6, has_more: true }));
      await park(120);

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      prepend(chat, [1, 2, 3]);
      await until(() => cards().length === 6, "the prepended page's cards to mount");
      off();

      expect(cardFor("t1")).not.toBeNull();
      // NOT the exact offset the reader parked at, which the old message-model
      // suite asserted: a prepend runs inside `preserveReadingPosition`, so the
      // controller adds the height it inserted above the reader and the number
      // moves BY DESIGN. What a pin would do is land them at the live edge, and
      // that is what this denies.
      expect(atLiveEdge()).toBe(false);
      expect(seen).toHaveBeenCalledTimes(0);
    });

    it("stays silent for a turn card a chat-switch replay built", async () => {
      // Every turn of the incoming chat mounts at once, and none of them is a turn
      // the reader just sent — they are a conversation being replayed.
      const first = nextChat();
      const second = nextChat();
      await mount(chatWith(first, [1, 2]));
      store.setSessions([chatWith(first, [1, 2]), chatWith(second, [1, 2, 3])]);

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      store.setActive(second);
      await until(() => cards().length === 3, "the switched-to chat's cards to mount");
      // The control that stops the gate reading as a regression: an opened chat
      // still lands at the live edge, because a Following reader is pinned there by
      // the streaming auto-scroll rather than by this mount.
      await until(atLiveEdge, "the opened chat to land at the live edge");
      off();

      expect(seen).toHaveBeenCalledTimes(0);
    });

    it("stays silent for the window a cold open's own fetch filled", async () => {
      // The case the arrivals rule cannot answer from the turn order: a chat whose
      // transcript is not resident paints EMPTY first, so the fetched window is not a
      // chat switch and its predecessor recorded no tail — the same state the
      // fresh-chat case above is in, and the opposite answer. The paint's CAUSE is
      // what separates them, and both consumers read it: the pin and the animation.
      const chat = nextChat();
      await mount(chatWith(chat, []));

      const seen = vi.fn();
      const off = scroll.onReaderGesture(seen);
      loadWindow(chat, [1, 2, 3]);
      await until(() => cards().length === 3, "the fetched window's cards to mount");
      await until(atLiveEdge, "the opened chat to land at the live edge");
      off();

      expect(seen).toHaveBeenCalledTimes(0);
      // The other consumer, and the reason this window is not merely unpinned: a
      // replay animates nothing, or every row of a reopened conversation fades in
      // together — the contract the paint states three lines above the arrivals scan.
      expect(cards().filter((c) => c.hasAttribute("data-chat-entry"))).toHaveLength(0);
    });
  },
);

describe(
  "a rail jump onto a non-resident turn",
  { timeout: testTimeoutFor(PIN_CONTEST_BUDGET_MS) },
  () => {
    /** The paged scene: turns 4..6 resident out of a session of 6, the index fetched,
     *  and a pagination door answering with turns 1..3. NOTHING SEEDS A WINDOW BASE,
     *  and the two lines that used to are dropped: a card's ordinal is `turn_open.n`,
     *  session-absolute in every window, so the numbering collision they existed for
     *  is unreachable and the paged path is the window not holding the turn. */
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
      // The end-to-end path: the click sets the pick, the jump pages history in, and
      // every prepended turn used to fire the pin — which revoked the pick before the
      // jump had resolved its target. `t3` is the SHORT card, so an offset-derived
      // mark would name `t4` rather than agreeing with the pick by accident.
      await pagedScene();

      markerFor(3).click();
      // The whole path, read from its two ends: the target card is resident, and the
      // jump's own pending state has been cleared by the `finally` that renders the
      // rail one last time.
      await until(
        () => cardFor("t3") !== null && markers().every((m) => m.dataset["pending"] === undefined),
        "the paged jump to resolve",
      );
      // AFTER the jump's own scroll has settled, which is the second half of what
      // "survives the jump" means: the landing's own scroll event arrives a frame
      // later, so a pick a gesture would revoke is revoked after the jump has
      // otherwise resolved.
      await quiet();

      expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
      expect(marked().map((m) => m.firstChild?.textContent)).toEqual(["3"]);
      expect(markerFor(3).getAttribute("aria-current")).toBe("true");
      // WHICH of the two marks it is, and this is the assertion the case's own
      // oracle needs: the rail writes `data-selected` for the reader's PICK and
      // `data-current` for the offset-derived turn, exactly one per render. Measured
      // — with the pin's arrivals gate deleted, the prepend's gesture revokes the
      // pick and the mark falls to `data-current` on this same marker, so every
      // assertion above still passes and only this one moves.
      expect(markerFor(3).dataset["selected"]).toBe("");
      expect(markerFor(3).dataset["current"]).toBeUndefined();
    });

    it("leaves the clicked turn's top on the reading line once the landing settles", async () => {
      // The other half of "the click took me there": the turn is MARKED above, and
      // here it is where the reader reads. A page landing in front of the target moves
      // it after the scroll was aimed, so this is the correction loop's answer rather
      // than the first scroll's, measured against the scroller's OWN reading line —
      // the same line activation reads.
      await pagedScene();

      markerFor(3).click();
      // The correction loop's own EXIT, not a still scroller: `pending` clears in the
      // `finally` spanning that loop, and under full-suite load the scroller sits
      // still between the first scroll and the correction, which read as settled.
      await until(
        () => cardFor("t3") !== null && markers().every((m) => m.dataset["pending"] === undefined),
        "the paged jump's correction loop to finish",
      );
      await quiet();

      const card = cardFor("t3");
      expect(card).not.toBeNull();
      // Rects in the scrollport's own frame, for `scroll.ts`'s reason: a turn card's
      // offsetParent is its own row, so an offset-relative read answers about a
      // different box.
      const fromTop = (card?.getBoundingClientRect().top ?? 0) - wrap.getBoundingClientRect().top;
      // LANDING_TOLERANCE_PX, hardcoded: the module's own tolerance is what the
      // correction loop stops at, and deriving it from the module would make this
      // assertion agree with whatever the module believes.
      expect(Math.abs(fromTop - scroll.readingLineOffset())).toBeLessThanOrEqual(8);
    });
  },
);
