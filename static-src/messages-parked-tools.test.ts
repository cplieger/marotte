// ---------------------------------------------------------------------------
// The tool layer under the multiplexer: composite tool identity, the parked
// terminal buffer, and the refcounted run clock. All three survived the entry
// cutover; the per-MESSAGE park half did not, so every fixture is a turn of
// ENTRIES and every writer one of the store's own operations.
//
// Tool call ids are backend-authored with no cross-chat uniqueness guarantee,
// and parked views stay RESIDENT, so two chats' identical ids share the page
// and the card registries key on toolCallSigKey(chatID, toolID).
//
// REAL store, renderer and tool layer; the scroll mock is canonical (geometry
// plays no part here) and api-client is stubbed, because the run store fetches
// run state through it and the fake payload is what makes a run "live".
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

// The renderer's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element, so the hosts exist before any import resolves.
for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// The network edge: run-store's fetch path, which is the OrError variant because the
// store spends a failed read's STATUS. A live run answers with one running node so
// `runIsLive` is true and the card's clock arms.
const apiGetOrErrorMock = vi.hoisted(() => vi.fn());
vi.mock("./api-client.js", () => ({
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: `store-load.ts` reaches these for
  // the deep-link confirmation and the window read, and this graph includes it.
  apiGet: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiGetOrError: apiGetOrErrorMock,
}));

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const turns = await import("./turns.js");
const messages = await import("./messages.js");
const blocks = await import("./messages-blocks.js");
const { appendTerminalChunk } = await import("./messages-tools.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

// --- Fixtures -------------------------------------------------------------------------

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

/** A sealed entry of any kind at `seq`. An absent lane is `""`, the transcript's own. */
function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  id?: string,
  lane?: string,
): Entry {
  const e: Entry = {
    id: id ?? `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  };
  return lane === undefined ? e : { ...e, lane };
}

function turnOpen(turnID: string, n: number, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

function toolCall(
  turnID: string,
  at: number,
  id: string,
  over: Record<string, unknown> = {},
  lane?: string,
): Entry {
  return sealed(
    turnID,
    at,
    "tool_call",
    { id, title: `run ${id}`, kind: "execute", status: "in_progress", ts: at + 1, ...over },
    id,
    lane,
  );
}

function toolResult(turnID: string, at: number, callID: string, status = "completed"): Entry {
  return sealed(turnID, at, "tool_result", { status }, `${callID}:result`);
}

function turnClose(turnID: string, at: number, outcome = "completed"): Entry {
  return sealed(turnID, at, "turn_close", { outcome });
}

/** A chat holding whole turns, the shape a page GET lands. */
function settled(
  id: string,
  turnEntries: readonly (readonly Entry[])[],
  over: Partial<Session> = {},
): Session {
  const s = session(id, over);
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  return s;
}

/** A chat of one bare prompt turn — the neighbour a park needs somewhere to go. */
function bystander(id: string): Session {
  const t = `${id}-t1`;
  return settled(id, [[turnOpen(t, 1, "x")]]);
}

/** Mount chats and activate the first, announcing the window as a REPLAY (the cause a
 *  fetched page carries), so no case inherits an arrival tail from its own seed. */
function seed(...sessions: Session[]): void {
  store.setSessions(sessions);
  const first = sessions[0];
  if (first !== undefined) {
    store.setActive(first.id);
    store.bumpMessages(first.id, "load");
  }
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** The projected turn a detached render takes. The page renders a REAL turn of the
 *  store — nothing is copied and no lane is stripped off an entry. */
function turnOf(chatID: string, turnID: string): Turn {
  const s = store.get(chatID);
  if (s === undefined) {
    throw new Error(`no session ${chatID}`);
  }
  const t = turns.projectTurn(s, turnID);
  if (t === undefined) {
    throw new Error(`turn ${turnID} is not drawn`);
  }
  return t;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope, so earlier cases' parked
  // views would otherwise count against the LRU budget of later ones.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  apiGetOrErrorMock.mockReset();
  // Status 0: no request reached the engine, which keeps the store's retry ladder where
  // a settled 404 would skip it.
  apiGetOrErrorMock.mockResolvedValue({ ok: false, status: 0, data: null, error: "" });
});

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Composite tool identity.
// ---------------------------------------------------------------------------

describe("composite tool identity", () => {
  it("two chats with IDENTICAL tool ids park and unpark without cross-corruption", async () => {
    // The exact id collision the composite key exists for: both chats' wire
    // frames name the same tool_call.id.
    const a = freshID("c-same");
    const b = freshID("c-same");
    const shared = "toolu_shared";
    const ta = `${a}-t1`;
    const tb = `${b}-t1`;
    seed(
      settled(a, [[turnOpen(ta, 1), toolCall(ta, 1, shared, { title: "chat A's call" })]]),
      settled(b, [[turnOpen(tb, 1), toolCall(tb, 1, shared, { title: "chat B's call" })]]),
    );
    await flushed();

    // Park A, mount B — both cards are on the page now, one per view.
    store.setActive(b);
    await flushed();
    // Re-QUERIED per read rather than held: both turns are still OPEN, and
    // `resumeTurnBody` REBUILDS an open turn's body, so a held node is detached from
    // the unpark onward. Identity is not the subject; which chat's data lands where is.
    const cardA = (): HTMLElement | null =>
      viewOf(a).querySelector<HTMLElement>(`[data-tool-id="${shared}"]`);
    const cardB = (): HTMLElement | null =>
      viewOf(b).querySelector<HTMLElement>(`[data-tool-id="${shared}"]`);
    expect(cardA()).not.toBeNull();
    expect(cardB()).not.toBeNull();
    // Both cards' pre-state, so every assertion below is about what a writer DID
    // rather than about a slot that was empty all along.
    expect(cardA()?.dataset["outcome"]).toBe("running");
    expect(cardB()?.dataset["outcome"]).toBe("running");

    // B's result lands on B's card only.
    store.appendEntry(b, toolResult(tb, 2, shared, "completed"));
    await flushed();
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    expect(cardA()?.dataset["outcome"]).not.toBe("ok");

    // THE KEYING ASSERTION, and it is the one that needs no freeze behind it: A's
    // own result is appended under the SAME tool id while B is the ACTIVE chat, so
    // B's card is live and subscribed. A shared signal would land `fail` on it.
    store.appendEntry(a, toolResult(ta, 2, shared, "failed"));
    await flushed();
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    // And nothing reached A's parked DOM either.
    expect(cardA()?.dataset["outcome"]).toBe("running");

    store.setActive(a);
    await flushed();
    // A's card carries A's own outcome, and B's still carries B's.
    expect(cardA()?.dataset["outcome"]).toBe("fail");
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    // Both signals exist side by side under their composite keys.
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(a, shared))).toBeDefined();
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(b, shared))).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// The parked terminal buffer.
// ---------------------------------------------------------------------------

describe("the parked terminal buffer", () => {
  it("drains once at resume and drops the oldest past 64 KB", async () => {
    const a = freshID("c-term");
    const b = freshID("c-term");
    const t = `${a}-t1`;
    const termID = freshID("term");
    // A SETTLED turn, which is the one shape this mechanism is observable in rather
    // than a convenience — see the hand-off closing this case.
    seed(
      settled(a, [
        [
          turnOpen(t, 1, "run it"),
          toolCall(t, 1, "t-term", { terminal_id: termID }),
          toolResult(t, 2, "t-term"),
          turnClose(t, 3),
        ],
      ]),
      bystander(b),
    );
    await flushed();
    // The link exists (mount claimed the terminal); live output lands in the
    // card while active.
    appendTerminalChunk(termID, "live line\n", [], 0);
    const pre = (): string =>
      viewOf(a).querySelector(".tool-call .tool-output pre")?.textContent ?? "";
    expect(pre()).toBe("live line\n");

    store.setActive(b);
    await flushed();

    // Three 30 KB chunks while parked: 90 KB > the 64 KB cap, so the OLDEST
    // drops (shell scrollback semantics — resume shows the newest output).
    const chunk = (label: string): string =>
      `${label}${"x".repeat(30 * 1024 - label.length - 1)}\n`;
    const oldest = chunk("oldest");
    const middle = chunk("middle");
    const newest = chunk("newest");
    let offset = "live line\n".length;
    for (const c of [oldest, middle, newest]) {
      appendTerminalChunk(termID, c, [], offset);
      offset += c.length;
    }
    // Nothing reached the parked DOM.
    expect(pre()).toBe("live line\n");

    store.setActive(a);
    await flushed();
    // Drained once: the two newest chunks landed, the oldest was dropped, and the
    // text already on screen when the view parked is still in front of them.
    const text = pre();
    expect(text.startsWith("live line\n")).toBe(true);
    expect(text).toContain("middle");
    expect(text).toContain("newest");
    expect(text).not.toContain("oldest");

    // A second park/unpark cycle with no new output replays nothing — the
    // buffer was consumed by the drain, not merely read.
    store.setActive(b);
    await flushed();
    store.setActive(a);
    await flushed();
    expect(pre()).toBe(text);

    // HANDED TO 4c, ROOT-CAUSED AND NOT PINNED: on an OPEN turn the buffer is
    // DESTROYED before it can drain. `resumeView` resumes each body and only then
    // calls `drainParkedTerminals`; a turn with no `turn_close` resumes through
    // `rebuildTurnBody`, whose `disposeToolEffectsForChat` reaches `removeSlot`, which
    // deletes the call's `termToTool` link AND its `parkedTermBuffers` entry. The
    // drain then finds no link and skips the terminal. Measured as an empty `pre()`
    // on the open-turn fixture this case first used, and it matters because an open
    // turn is the shape a terminal normally streams in.
  });
});

// ---------------------------------------------------------------------------
// The refcounted run clock: one interval per WORKFLOW over the cards holding
// it, so a park releasing the transcript's hold must not stop a clock another
// surface still reads. The two surfaces are two DIFFERENT calls naming one run
// — the launch in the issuer's lane, a later mention in a delegate's — which is
// the only wire shape that can produce two cards for one run.
// ---------------------------------------------------------------------------

describe("the run clock", () => {
  /** A live run: one running leaf, started in the past, never ended — the
   *  shape `runIsLive` and the card's clock both key on. */
  function liveRunPayload(wf: string): unknown {
    return {
      workflowId: wf,
      state: {
        workflowId: wf,
        status: "running",
        root: {
          nodeId: "n1",
          type: "step",
          status: "running",
          startedAt: new Date(Date.now() - 5000).toISOString(),
        },
      },
    };
  }

  it("survives a park while another surface holds the same run", async () => {
    vi.useFakeTimers();

    const a = freshID("c-clock");
    const b = freshID("c-clock");
    const wf = freshID("wf");
    const lane = "subtask-1";
    const t = `${a}-t1`;
    apiGetOrErrorMock.mockImplementation(() =>
      Promise.resolve({ ok: true, status: 200, data: liveRunPayload(wf), error: "" }),
    );
    seed(
      settled(a, [
        [
          turnOpen(t, 1, "run the workflow"),
          // The launch, in the issuer's own lane: the transcript's run card.
          toolCall(t, 1, "t-launch", { workflow_id: wf, title: "Run Workflow" }),
          // A delegate mentioning the SAME run. It renders at no position in the
          // transcript (its lane is not the transcript's root) and is the whole
          // body of that delegate's own page.
          toolCall(t, 2, "t-inspect", { workflow_id: wf, title: "Inspect Workflow" }, lane),
        ],
      ]),
      bystander(b),
    );
    await flushed();
    // Let the mocked run fetch resolve into the cell.
    await vi.advanceTimersByTimeAsync(0);

    const transcriptCard = viewOf(a).querySelector<HTMLElement>(".run-card");
    expect(transcriptCard).not.toBeNull();

    // The second surface: the delegate's own page, rendering its lane, whose
    // mention of `wf` holds its own clock ref.
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, turnOf(a, t), a, lane, false);
    await vi.advanceTimersByTimeAsync(0);
    // The run's elapsed is the FOOT's now (`renderFoot`, re-rendered by `tick`),
    // so the ledger is where a card's clock is read.
    const detachedClock = host.querySelector<HTMLElement>(".run-ledger");
    expect(detachedClock).not.toBeNull();

    // Park A: the transcript card releases ITS hold; the detached surface's
    // hold keeps the shared interval alive.
    store.setActive(b);
    await flushed();

    const before = detachedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    const after = detachedClock?.textContent ?? "";
    expect(after).not.toBe("");
    expect(after).not.toBe(before);

    // The parked transcript card's clock did NOT advance: its hold released.
    const parkedClock = viewOf(a).querySelector<HTMLElement>(".run-ledger");
    const parkedBefore = parkedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(parkedClock?.textContent ?? "").toBe(parkedBefore);

    // Unpark: the transcript card re-arms, re-reads its cell and ticks again.
    store.setActive(a);
    await flushed();
    const resumedClock = viewOf(a).querySelector<HTMLElement>(".run-ledger");
    const resumedBefore = resumedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(resumedClock?.textContent ?? "").not.toBe(resumedBefore);

    blocks.disposeDetachedBody(t, lane);
    host.remove();
  });
});

// ---------------------------------------------------------------------------
// THREE ORACLES DROPPED OUT LOUD: two mounted cards for ONE tool call. The old
// suite's last describe held three cases over that shape — a park freezing the
// transcript's card while the page's kept updating, the page's dispose leaving
// the transcript's card managed, and a chunk reaching the live page while the
// parked transcript waited. The shape is UNREACHABLE rather than untested:
// `entryRenders` refuses an entry whose lane is not the render's root lane, so
// the transcript (lane "") and a delegate's page render DISJOINT entry sets and
// one tool_call is mountable on exactly one of them. A fixture with one tool id
// in two lanes would be a wire shape nothing produces.
//
// Two lost nothing — the park freeze over a tool card is
// `messages-parked-views.test.ts`'s, the drain-once oracle this file's own.
// What is dropped with no home is the multimap: the second slot per (chatID,
// toolID) and the refcounted clearing behind it. HANDED TO 4c, not pinned:
// `mountToolCard` still comments that the transcript's card and the subagent
// page's for one call come and go independently, which the lane predicate has
// made false.
// ---------------------------------------------------------------------------
