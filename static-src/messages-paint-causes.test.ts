// ---------------------------------------------------------------------------
// paint() branches on the DECLARED render cause: the store says what a bump was
// FOR, and the renderer skips exactly the work that cause makes unnecessary.
//
//   chunk — a MOUNTED entry's own signal painted the text, or the entry is in a
//           delegate's lane and renders at no position at all.
//   tool  — one call's update: the owning TURN's keyed refresh, never a mount.
//   fact  — a fact flipped with the log unchanged, which no inference over the
//           array can see; the store's declaration is the only signal.
// ---------------------------------------------------------------------------
// A skip is asserted as a spy delta on the seams paint drives, plus element
// identity over the view's children, because a reconcile that rebuilt a card
// would mint new nodes even for equal markup.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { SessionOverrides } from "./__test-helpers__/model.js";
// The two streaming describes wait on REAL frames (a delta reaches a bubble
// through the reveal cursor, which spreads growth across frames), so both
// declare their bound in the unit that harness charges.
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

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

// The scroller is mocked through the shared helper so its surface stays total;
// the paint path only needs its calls inert. Everything else is REAL, spied
// where a skip has to be proven.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// The rail is SPIED, not replaced: `setResidentTurns` is one of the seams a skipped pass
// must not reach, and the count is the only thing this file reads off it. `{ spy: true }`
// keeps the real module, so the rail this suite paints through is the shipped one.
vi.mock("./turn-rail.js", { spy: true });
vi.mock("./turns.js", { spy: true });
vi.mock("./reconcile.js", { spy: true });
vi.mock("./fold-state.js", { spy: true });
vi.mock("./messages-blocks.js", { spy: true });

const store = await import("./store.js");
const turnsMod = await import("./turns.js");
const reconcileMod = await import("./reconcile.js");
const foldMod = await import("./fold-state.js");
const railMod = await import("./turn-rail.js");
const blocksMod = await import("./messages-blocks.js");
const messages = await import("./messages.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

// --- Fixtures -------------------------------------------------------------------------

function session(id: string, over: SessionOverrides = {}): Session {
  return makeSession({ id, name: id, ...over });
}

/** A sealed entry of any kind at `seq`. An absent lane is `""`, the transcript's own. */
function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  id?: string,
): Entry {
  return { id: id ?? `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function turnOpen(turnID: string, n: number, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

function textEntry(turnID: string, at: number, s: string, id?: string): Entry {
  return sealed(turnID, at, "text", { text: s }, id);
}

function toolCall(turnID: string, at: number, id: string): Entry {
  return sealed(
    turnID,
    at,
    "tool_call",
    { id, title: "Run command", kind: "execute", status: "in_progress", ts: at + 1 },
    id,
  );
}

function turnClose(turnID: string, at: number, outcome = "completed"): Entry {
  return sealed(turnID, at, "turn_close", { outcome });
}

function open(turnID: string, id: string, text: string): OpenEntry {
  return { turn: turnID, id, kind: "text", text, n: 1 };
}

/** A chat holding whole SEALED turns, the shape a page GET lands. */
function settledChat(
  id: string,
  turns: readonly (readonly Entry[])[],
  over: SessionOverrides = {},
): Session {
  const s = session(id, over);
  for (const entries of turns) {
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

function cardsOf(chatID: string): HTMLElement[] {
  return [...viewOf(chatID).querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** Call counts on every seam a skipped pass must not touch. */
function seamCounts(): Record<string, number> {
  return {
    projectTurns: vi.mocked(turnsMod.projectTurns).mock.calls.length,
    reconcile: vi.mocked(reconcileMod.reconcile).mock.calls.length,
    isTurnOpen: vi.mocked(foldMod.isTurnOpen).mock.calls.length,
    setResidentTurns: vi.mocked(railMod.setResidentTurns).mock.calls.length,
    buildAssistantBody: vi.mocked(blocksMod.buildAssistantBody).mock.calls.length,
    updateAssistantBody: vi.mocked(blocksMod.updateAssistantBody).mock.calls.length,
  };
}

function sameNodes(after: readonly HTMLElement[], before: readonly HTMLElement[]): void {
  expect(after).toHaveLength(before.length);
  // Per element, because `toEqual` over nodes compares them STRUCTURALLY and so passes for
  // a rebuilt element holding the same markup — the one thing this assertion is for.
  for (const [i, el] of after.entries()) {
    expect(el).toBe(before[i]);
  }
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

/** Two frames, for the cold body build the window drains per frame and for a
 *  `content-visibility: auto` row's first relevance pass. */
async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope, so an earlier case's parked view
  // would otherwise count against the LRU budget of a later one.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe(
  "paint branches on the flushed render cause",
  { timeout: testTimeoutFor(FRAME_BUDGET_MS) },
  () => {
    it("a chunk flush runs zero projection, reconcile, fold or per-turn work", async () => {
      const a = freshID("c-chunk");
      const t = `${a}-t1`;
      const s = session(a, { thinking: true });
      s.turns.set(t, { entries: [turnOpen(t, 1)], openEntries: new Map() });
      s.turn_order.push(t);
      s.turn_count = 1;
      seed(s);
      store.openEntry(a, open(t, "say-1", "hello"));
      // The precondition, asserted rather than assumed: the delta below is only a `chunk`
      // because a MOUNTED bubble's own signal carries it.
      await vi.waitFor(() => {
        expect(viewOf(a).querySelector(".message.assistant")).not.toBeNull();
      });
      const kids = [...viewOf(a).children] as HTMLElement[];
      const before = seamCounts();

      store.applyDelta(a, t, "say-1", "", 2, " world");
      await flushed();

      expect(store.renderCauseOf(a).cause).toBe("chunk");
      expect(seamCounts()).toEqual(before);
      sameNodes([...viewOf(a).children] as HTMLElement[], kids);

      // The skip lost nothing: the entry's signal puts the text on screen. The reveal holds
      // the live edge's tail back, so CLOSE the turn — the pass that finalizes drains it.
      store.appendEntry(a, turnClose(t, 1));
      await vi.waitFor(
        () => {
          expect(viewOf(a).querySelector(".message.assistant")?.textContent).toContain("world");
        },
        { timeout: FRAME_BUDGET_MS },
      );
    });

    it("a delta with no signal cell classifies shape, so the full pass paints it", async () => {
      // The open tail of a page GET: seated with the window rather than by a live
      // `entry_opened`, so no signal cell exists and nothing is subscribed to it. The
      // classification is the subject, so the chat is deliberately not the active one.
      const bg = freshID("c-nosig");
      const t = `${bg}-t1`;
      const s = session(bg);
      s.turns.set(t, {
        entries: [turnOpen(t, 1)],
        openEntries: new Map([["", { turn: t, id: "say-1", kind: "text", text: "hello", n: 1 }]]),
      });
      s.turn_order.push(t);
      s.turn_count = 1;
      store.setSessions([s]);
      await flushed();

      store.applyDelta(bg, t, "say-1", "", 2, " world");
      await flushed();

      expect(store.renderCauseOf(bg).cause).toBe("shape");
    });

    it("a tool flush refreshes only the owning turn's card", async () => {
      const a = freshID("c-tool");
      const t1 = `${a}-t1`;
      const t2 = `${a}-t2`;
      // The tool call is in the NEWEST turn: the fold policy wants that turn open, so its
      // body is mounted and its per-call signal exists — which is what makes the cause
      // `tool` rather than the signal-absent fallback.
      seed(
        settledChat(a, [
          [turnOpen(t1, 1), textEntry(t1, 1, "done"), turnClose(t1, 2)],
          [turnOpen(t2, 2), toolCall(t2, 1, "tc-1")],
        ]),
      );
      await vi.waitFor(() => {
        expect(viewOf(a).querySelector(".tool-call")).not.toBeNull();
      });
      const kids = [...viewOf(a).children] as HTMLElement[];
      const before = seamCounts();
      const refreshBefore = vi.mocked(blocksMod.refreshMessageCard).mock.calls.length;

      store.applyToolProgress(a, t2, { turn: t2, tool_call_id: "tc-1", status: "completed" });
      await flushed();

      expect(store.renderCauseOf(a)).toEqual({ cause: "tool", turnID: t2 });
      const refresh = vi.mocked(blocksMod.refreshMessageCard).mock;
      expect(refresh.calls.length).toBe(refreshBefore + 1);
      expect(refresh.calls.at(-1)?.[0]?.id).toBe(t2);
      // It found its render, which is what lets paint return: an absent one falls through
      // to the full pass instead, because only the full pass mounts.
      expect(refresh.results.at(-1)?.value).toBe(true);
      expect(seamCounts()).toEqual(before);
      sameNodes([...viewOf(a).children] as HTMLElement[], kids);
    });

    it("a delta in a delegate's lane is bookkeeping-only for the transcript", async () => {
      const a = freshID("c-lane");
      const t = `${a}-t1`;
      seed(settledChat(a, [[turnOpen(t, 1), textEntry(t, 1, "done"), turnClose(t, 2)]]));
      await settledFrames();
      const kids = [...viewOf(a).children] as HTMLElement[];
      const before = seamCounts();

      store.appendEntry(a, {
        id: "sub-e1",
        turn: t,
        lane: "sub-1",
        kind: "text",
        seq: 3,
        ts: 9,
        payload: { text: "delegate prose" },
      });
      await flushed();

      expect(store.renderCauseOf(a).cause).toBe("chunk");
      expect(seamCounts()).toEqual(before);
      sameNodes([...viewOf(a).children] as HTMLElement[], kids);
      // And nothing of it is on screen, which is what makes the skip free: a laned entry
      // renders at no position in the transcript's own lane.
      expect(viewOf(a).textContent).not.toContain("delegate prose");
    });

    it("a fact flip runs the full pass and repaints its cards in place", async () => {
      const a = freshID("c-fact");
      const t = `${a}-t1`;
      seed(settledChat(a, [[turnOpen(t, 1), textEntry(t, 1, "done"), turnClose(t, 2)]]));
      await settledFrames();
      const kids = cardsOf(a);
      const before = seamCounts();

      // A transcript fact the ARRAY cannot state: no entry was appended, no entry changed
      // length, so nothing about the log's shape says a repaint is owed.
      store.setThinking(a, true);
      await flushed();

      expect(store.renderCauseOf(a).cause).toBe("fact");
      expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBeGreaterThan(
        before["projectTurns"] ?? 0,
      );
      sameNodes(cardsOf(a), kids);
    });
  },
);

// ---------------------------------------------------------------------------
// `.is-bodyless` on the turn card mirrors "the card ends with an empty body" for
// CSS (29-turns.css keys the header's bottom edge on it). It is stamped after
// every build and update pass, so it must track both facts it encodes: the
// body's children AND whether a footer follows.
// ---------------------------------------------------------------------------

describe("the bodyless turn card is marked .is-bodyless", () => {
  it("marks a prompt-only turn and clears it when the reply lands", async () => {
    const a = freshID("c-bl");
    const t = `${a}-t1`;
    const s = session(a, { thinking: true });
    s.turns.set(t, { entries: [turnOpen(t, 1, "hi")], openEntries: new Map() });
    s.turn_order.push(t);
    s.turn_count = 1;
    seed(s);
    await settledFrames();
    const card = cardsOf(a)[0];
    expect(card?.classList.contains("is-bodyless")).toBe(true);

    store.appendEntry(a, textEntry(t, 1, "hello"));
    await settledFrames();
    const after = cardsOf(a)[0];
    expect(after?.querySelector(":scope > .turn-body")?.childElementCount).toBe(1);
    expect(after?.classList.contains("is-bodyless")).toBe(false);
  });

  it("paints a reserved slot as a marked row, end to end", async () => {
    // The msg-row half of the same CSS contract, through the REAL block callbacks: a text
    // entry carrying no text paints a `.msg-row.is-empty` the stylesheet hides. The turn is
    // SETTLED because blank is `!live && nothing rendered` — a live bubble stays visible
    // for its caret, so a reserved slot is only ever marked on a bubble that is not
    // streaming (`fundamentals/text-bubble.ts`).
    const a = freshID("c-blrow");
    const t = `${a}-t1`;
    seed(settledChat(a, [[turnOpen(t, 1, "go"), textEntry(t, 1, ""), turnClose(t, 2)]]));
    await vi.waitFor(() => {
      expect(viewOf(a).querySelector(".msg-row")).not.toBeNull();
    });
    expect(viewOf(a).querySelector(".msg-row")?.classList.contains("is-empty")).toBe(true);
  });

  it("clears it when a FOOTER arrives under a still-empty body", async () => {
    // A closed turn with nothing in its body earns a footer of its own (an outcome to
    // state, and the next turn's prompt as a rewind target), so the body is no longer the
    // card's last child: marking here would erase the only line between the two bands.
    const a = freshID("c-blfoot");
    const t1 = `${a}-t1`;
    const t2 = `${a}-t2`;
    const s = settledChat(
      a,
      [[turnOpen(t1, 1, "one"), turnClose(t1, 1, "unknown")], [turnOpen(t2, 2, "two")]],
      { thinking: true },
    );
    seed(s);
    await settledFrames();

    const cards = cardsOf(a);
    expect(cards).toHaveLength(2);
    expect(cards[0]?.querySelector(":scope > .turn-footer")).not.toBeNull();
    expect(cards[0]?.querySelector(":scope > .turn-body")?.childElementCount).toBe(0);
    expect(cards[0]?.classList.contains("is-bodyless")).toBe(false);
    // The prompt-only turn takes the mark instead.
    expect(cards[1]?.classList.contains("is-bodyless")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// A turn's liveness is the LOG's statement now: `turn_close` absence IS
// `outcome: "running"`, so nothing composes a client flag against a server one
// and the defect the composition existed for is unreachable rather than
// guarded. What survives is the rendering consequence, which is what the reader
// saw: a turn the log leaves open carries no settled mark.
// ---------------------------------------------------------------------------

describe("a turn's outcome mark follows turn_close, not a liveness flag", () => {
  it("paints NO settled mark for a turn the log leaves open", async () => {
    const a = freshID("c-live");
    const t = `${a}-t1`;
    const s = session(a);
    s.turns.set(t, { entries: [turnOpen(t, 1, "do the thing")], openEntries: new Map() });
    s.turn_order.push(t);
    s.turn_count = 1;
    seed(s);
    await settledFrames();

    const card = cardsOf(a)[0];
    expect(card, "the turn painted").not.toBeUndefined();
    // `running`'s treatment already is "no settled mark", which is why the fix was the
    // derivation rather than a suppression rule in the renderer.
    expect(card?.querySelector(":scope > .turn-footer .turn-ledger-glyph")).toBeNull();
  });

  it("paints the neutral mark when the log closed the turn with no readable outcome", async () => {
    // The direction the derivation must not erase: after a restart nothing closed the turn
    // readably, so `unknown` is honest and states itself.
    const a = freshID("c-closed");
    const t = `${a}-t1`;
    seed(settledChat(a, [[turnOpen(t, 1, "do the thing"), turnClose(t, 1, "unknown")]]));
    await settledFrames();

    const card = cardsOf(a)[0];
    const footer = card?.querySelector<HTMLElement>(":scope > .turn-footer");
    expect(footer, "an unreadable end still says so").not.toBeNull();
    expect(footer?.querySelector(".turn-ledger-glyph")).not.toBeNull();
    expect(footer?.dataset["severity"]).not.toBe("");
  });
});
