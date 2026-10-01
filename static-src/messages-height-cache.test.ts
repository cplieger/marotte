// When the transcript FORGETS what it measured.
//
// `block-heights.ts` is the one per-TURN store an unmount deliberately keeps: its numbers
// are what price the spacers standing in for entries nobody has mounted, so dropping them
// at the unmount would defeat the cache. That leaves the view's dispose as the moment they
// stop standing for anything, and `disposeChatView` reaches it through the chat's own
// `turn_order` — which is the key space, since a height is keyed `(turnID, seq)`.
import { describe, it, expect, vi, beforeEach } from "vitest";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every other suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: { dispatch: () => Promise.resolve() },
  explainError: { dispatch: () => Promise.resolve(null) },
}));
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0 })),
}));

const { mountChatView, disposeChatView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { recordRowHeight, spacerHeight } = await import("./block-heights.js");
const { projectTurns } = await import("./turns.js");
const { resetFoldState } = await import("./fold-state.js");

import { makeSession } from "./__test-helpers__/model.js";
import type { Turn } from "./turns.js";
import type { Entry, TurnState } from "./types.js";

const CHAT = "c-height";
const TURN = "t1";

/** The whole turn's cold price: eight `text` entries in one lane are ONE prose run, so the
 *  spacer stands for one row (`ENTRY_ESTIMATE_PX.text`, 48 at both tiers) plus the boundary
 *  gap `.turn-body` no longer supplies (`ROW_GAP_PX`, 12). */
const COLD_PX = 60;

/** A height no estimate can produce, so a read answering it can only have come from the
 *  cache — plus that same boundary gap. */
const MEASURED_PX = 999;
const MEASURED_TOTAL_PX = 1011;

/** The run the eight text entries form: `seq` 1 through 8, so `sliceTurn` answers
 *  `{from: 1, to: 9}` and that is the range a row records against. */
const RUN = { from: 1, to: 9 } as const;

function sealed(at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${TURN}-e${String(at)}`, turn: TURN, kind, seq: at, ts: at + 1, payload } as Entry;
}

/** One turn: a prompt, eight sealed text entries, a close. */
function entries(): Entry[] {
  return [
    sealed(0, "turn_open", { prompt: { id: `${TURN}-p`, text: "go" }, source: "prompt", n: 1 }),
    ...Array.from({ length: 8 }, (_, i) => sealed(i + 1, "text", { text: `chunk ${String(i)}` })),
    sealed(9, "turn_close", { outcome: "completed" }),
  ];
}

function activate(): void {
  const turns = new Map<string, TurnState>([
    [TURN, { entries: entries(), openEntries: new Map() }],
  ]);
  setSessions([
    { ...makeSession({ id: CHAT, name: CHAT }), turns, turn_order: [TURN], turn_count: 1 },
  ]);
  setActive(CHAT);
  bumpMessages(CHAT, "load");
}

/** The projection `spacerHeight` prices, taken from production rather than hand-built, so a
 *  reshape of `Turn` cannot leave this suite pricing a shape the renderer never sees. */
function turn(): Turn {
  const built = projectTurns({
    turns: new Map<string, TurnState>([[TURN, { entries: entries(), openEntries: new Map() }]]),
    turn_order: [TURN],
  });
  const t = built[0];
  if (t === undefined) {
    throw new Error("the projection produced no turn");
  }
  return t;
}

/** The whole turn, priced: nothing mounted, so the tail spacer stands for every entry. */
function wholeTurn(): number {
  return spacerHeight(turn(), { from: 0, to: 0 }, "tail", "");
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  resetFoldState();
  setSessions([]);
  setActive("");
});

describe("the measurement cache over a view's life", () => {
  it("answers from the measurement while the view lives", () => {
    // The control. Without it the case below passes for the wrong reason: a
    // `spacerHeight` that never consulted the cache also returns the estimate.
    activate();
    expect(wholeTurn()).toBe(COLD_PX);
    recordRowHeight(TURN, RUN, MEASURED_PX);
    expect(wholeTurn()).toBe(MEASURED_TOTAL_PX);
    bumpMessages(CHAT, "shape");
    expect(wholeTurn()).toBe(MEASURED_TOTAL_PX);
  });

  it("forgets the turn's measurements when the chat's view is disposed", () => {
    activate();
    recordRowHeight(TURN, RUN, MEASURED_PX);
    expect(wholeTurn()).toBe(MEASURED_TOTAL_PX);

    // Tab close, LRU eviction and teardown all run this, and the chat's `turn_order` is
    // what it forgets by — so the record has to still be in the store when it runs.
    disposeChatView(CHAT);
    expect(wholeTurn()).toBe(COLD_PX);
  });
});
