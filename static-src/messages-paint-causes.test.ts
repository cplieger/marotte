// paint() skips the work the declared render cause makes unnecessary: `chunk` (a mounted entry's signal painted
// it, or a laned entry renders nowhere), `tool` (the owning turn's keyed refresh), `fact` (a flip the log cannot
// show). A skip is asserted as spy deltas plus element identity, since a rebuild mints new nodes for equal markup.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { SessionOverrides } from "./__test-helpers__/model.js";
// The streaming describes wait on real frames (the reveal cursor spreads growth), so they declare frame bounds.
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

// The graph reads the DOM registry at module scope and `byId` throws on a missing element.
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

// Scroll is inert through the shared helper; everything else is real, spied where a skip must be proven.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// Spied, not replaced: `setResidentTurns` is a seam a skipped pass must not reach.
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

/** Mount chats and activate the first, announcing a replay so no case inherits an arrival tail from its seed. */
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
  // Per element: `toEqual` compares nodes structurally and would pass a rebuilt element.
  for (const [i, el] of after.entries()) {
    expect(el).toBe(before[i]);
  }
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

/** Two frames: the cold body build drains per frame, and a `content-visibility: auto` row needs a relevance pass. */
async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope; an earlier case's parked view would count against the LRU.
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
      // The precondition: the delta is a `chunk` only because a mounted bubble's signal carries it.
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

      // The reveal holds the live edge's tail back, so close the turn to drain it.
      store.appendEntry(a, turnClose(t, 1));
      await vi.waitFor(
        () => {
          expect(viewOf(a).querySelector(".message.assistant")?.textContent).toContain("world");
        },
        { timeout: FRAME_BUDGET_MS },
      );
    });

    it("a delta with no signal cell classifies shape, so the full pass paints it", async () => {
      // A page GET's open tail has no signal cell; the chat is deliberately not the active one.
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
      // The newest turn's body is mounted, so its per-call signal exists and the cause is `tool`.
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
      // An absent render falls through to the full pass, because only the full pass mounts.
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
      // A laned entry renders at no position in the transcript's own lane, which makes the skip free.
      expect(viewOf(a).textContent).not.toContain("delegate prose");
    });

    it("a fact flip runs the full pass and repaints its cards in place", async () => {
      const a = freshID("c-fact");
      const t = `${a}-t1`;
      seed(settledChat(a, [[turnOpen(t, 1), textEntry(t, 1, "done"), turnClose(t, 2)]]));
      await settledFrames();
      const kids = cardsOf(a);
      const before = seamCounts();

      // A fact the log's shape cannot state.
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

// `.is-bodyless` mirrors "the card ends with an empty body" for CSS, so it tracks both the body's children and
// whether a footer follows, after every build and update pass.

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
    // The msg-row half: an empty settled text entry paints a hidden `.msg-row.is-empty`; a live bubble stays visible
    // for its caret.
    const a = freshID("c-blrow");
    const t = `${a}-t1`;
    seed(settledChat(a, [[turnOpen(t, 1, "go"), textEntry(t, 1, ""), turnClose(t, 2)]]));
    await vi.waitFor(() => {
      expect(viewOf(a).querySelector(".msg-row")).not.toBeNull();
    });
    expect(viewOf(a).querySelector(".msg-row")?.classList.contains("is-empty")).toBe(true);
  });

  it("clears it when a FOOTER arrives under a still-empty body", async () => {
    // A closed empty turn earns its own footer, so the body is no longer the card's last child.
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

// `turn_close` absence is `outcome: "running"`, so a turn the log leaves open carries no settled mark.

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
    expect(card?.querySelector(":scope > .turn-footer .turn-ledger-glyph")).toBeNull();
  });

  it("paints the neutral mark when the log closed the turn with no readable outcome", async () => {
    // After a restart nothing closed the turn readably, so `unknown` states itself.
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
