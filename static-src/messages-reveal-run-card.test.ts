// ---------------------------------------------------------------------------
// `revealRunCard`: what the run sub-tab's "Open the conversation" link chains onto
// once the chat's tab is open.
//
// It is BEST-EFFORT by design, and the three cases below are the three answers it
// can honestly give: the card is mounted (scroll to it, true), the card's turn is
// resident but folded to a stub so no card exists (false — the reader is in the right
// conversation and nothing more was claimed), or this chat has no resident view at
// all (false, and nothing touched).
//
// It drives the REAL transcript rather than mocking it, because the thing under test
// is which card in which view — a mocked renderer would let a document-wide lookup
// pass. The entry fixtures and the seed-whole-turns `activate` are
// `block-virtualization.test.ts`'s, which is this fence's reference harness.
//
// THE CARD'S OWNER IS AN ENTRY, and the fixture has to satisfy the derivation rather
// than a message's `tool_calls` array: `runCardOwners` gives a run's card to the FIRST
// `tool_call` entry in turn and `seq` order whose EFFECTIVE run id names it, and
// `effectiveRunID` reads `payload.workflow_id` off the call itself. So one sealed
// `tool_call` entry carrying that field is the whole launcher, and the scope is the
// RESIDENT window — which is what makes the stub case below answer false rather than
// needing a second mechanism.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// The render graph reaches the shared DOM registry, which throws on a missing app
// root. Every id has to exist before the imports below are evaluated, and the pair
// the send-state effect reads is among them. NESTED as the shipped page nests them:
// `#messages-wrap` is the scroller inside the outer wrapper.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

// scroll.ts is a self-initialising singleton over a real scroller; the canonical mock
// is what every suite in this graph uses. `jumpTo` is a spy on it, which is the
// observable for "did it scroll to the card" — the module owns both halves of that
// decision (park the reader, decide whether the jump leaves the live edge), so
// asserting the CALL rather than a scrollTop is asserting the contract.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The graph's network edge, real except for the two GETs this graph makes on paint: a
// run's state and the rail's session-wide turn index. Both answer nothing, which is
// what a card with no fetched state renders its own loading row for.
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, revealRunCard, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages, removeChat } = await import("./store.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { RESIDENT_ENTRIES } = await import("./block-window.js");
const { resetTurnRail } = await import("./turn-rail.js");
const { scrollMock } = await import("./__test-helpers__/scroll-mock.js");
const { KEY_ATTR } = await import("./reconcile.js");

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

function turnOpen(turnID: string, n: number): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text: `prompt ${turnID}` },
    source: "prompt",
    n,
  });
}

/** A prose turn: one sealed `text` entry, which is one mounted prose run. */
function plainTurn(turnID: string, n: number): Entry[] {
  return [
    turnOpen(turnID, n),
    sealed(turnID, 1, "text", { text: `reply ${turnID}` }),
    sealed(turnID, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** A turn that LAUNCHED a run: one sealed `tool_call` entry whose own
 *  `payload.workflow_id` is what `effectiveRunID` reads, so this entry owns that run's
 *  card wherever the window holds it. */
function launcherTurn(turnID: string, n: number, workflowID: string): Entry[] {
  return [
    turnOpen(turnID, n),
    sealed(turnID, 1, "tool_call", {
      id: `${turnID}-tc`,
      title: "Run Workflow",
      kind: "other",
      status: "completed",
      ts: 1,
      workflow_id: workflowID,
    }),
    sealed(turnID, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** A turn that spends the whole entry budget on its own, so every turn behind it is a
 *  stub. `thinking` entries rather than `text`: a body of sealed `text` entries is ONE
 *  prose run and would cost one ordinal, so the budget would never bind. */
function hugeTurn(turnID: string, n: number, count: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `step ${String(at)}` }));
  }
  out.push(sealed(turnID, count + 1, "turn_close", { outcome: "completed" }));
  return out;
}

/** Seed whole turns and paint them, the way a window GET lands: one `TurnState` per
 *  turn in `turn_order`, then the `load` bump the loader ends on. */
function activate(chat: string, turnEntries: readonly (readonly Entry[])[]): void {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chat, name: chat }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
}

function card(turnID: string): HTMLElement | null {
  const root = activeTranscriptView() ?? messagesEl;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  return null;
}

let seq = 0;
/** A fresh chat id per case, so fold overrides and resident views cannot bleed. */
function chatID(): string {
  seq++;
  return `c-reveal-${String(seq)}`;
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  resetFoldState();
  resetTurnRail();
  scrollMock.jumpTo.mockClear();
  setSessions([]);
});

describe("revealRunCard", () => {
  it("scrolls to a mounted run card and says so", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    const view = activeTranscriptView();
    const runCard = view?.querySelector<HTMLElement>('.run-card[data-run="wf_1"]');
    // The premise, or this case asserts nothing about scoping: the card is really
    // mounted inside THIS chat's view.
    expect(runCard).not.toBeNull();

    expect(revealRunCard(c, "wf_1")).toBe(true);
    expect(scrollMock.jumpTo).toHaveBeenCalledTimes(1);
    expect(scrollMock.jumpTo.mock.calls[0]?.[0]).toBe(runCard);
  });

  // The renderer fact this function rests on, and the reason it does not also unfold:
  // a turn pushed out of the resident ENTRY window is a header/footer STUB with no
  // `.turn-body`, so it holds no run card at all and this answers false. The collapsed
  // FACE used to mount a duplicate card, which is what made a stub answer true; that
  // duplicate is gone — the composer band's run bar is the persistent surface for a
  // live run now — and the honest answer is that the reader is in the right
  // conversation and the card is one unfold away. `runCardOwners` agrees from the other
  // side: its scope is the resident window, so a paged-out mention owns no card.
  it("answers false for a launching turn that folded to a stub", () => {
    const c = chatID();
    // The newest turn spends the whole entry budget by itself, so the launching turn
    // behind it is outside the window. The stub premise is asserted below rather than
    // assumed.
    activate(c, [launcherTurn("t-old", 1, "wf_old"), hugeTurn("t-big", 2, RESIDENT_ENTRIES + 64)]);
    const view = activeTranscriptView();
    // The premise: the turn really is a stub, which is what takes its card with it.
    expect(card("t-old")?.querySelector(":scope > .turn-body")).toBeNull();
    expect(view?.querySelector('.run-card[data-run="wf_old"]')).toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // The honest false case: the window is PAGINATED, so a run launched far enough back
  // is not resident and no client-side unfold can reach it. A step's transcript is
  // served from the RUN's own log on demand, which is the answer to that whole family.
  it("answers false when the launching turn has been paged out", () => {
    const c = chatID();
    // A resident view whose window simply does not hold the launcher.
    activate(c, [plainTurn("t1", 1), plainTurn("t2", 2)]);
    expect(activeTranscriptView()).not.toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // THE SCOPING CASE. `.run-card[data-run]` repeats once per RESIDENT view and
  // `document.querySelector` answers in document order, which can be a PARKED view's
  // card — so a document-wide lookup would scroll a reader to another conversation's
  // card and claim it landed.
  it("answers false, and touches nothing, for a chat with no resident view", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    // The card really is in the DOCUMENT, under the chat that owns it.
    expect(document.querySelector('.run-card[data-run="wf_1"]')).not.toBeNull();

    expect(revealRunCard("c-never-opened", "wf_1")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // A run this conversation never launched has no card to find, so nothing is
  // disturbed.
  it("answers false for a run this chat did not launch", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    expect(revealRunCard(c, "wf_other")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  it("answers false for an empty run id", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    expect(revealRunCard(c, "")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // ORACLE CORRECTED, because the implementation moved under it: this case's comment
  // used to say "the store read is what stops the walk rather than throwing on an
  // absent session", and `revealRunCard` makes no store read at all — it resolves the
  // VIEW and queries inside it. So what it pins is that dropping a chat's window
  // disposes that chat's view: with the view left standing the card would still be
  // there and this would answer true.
  it("answers false when the chat's window is gone", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    setTurnOpen(c, "t1", false);
    removeChat(c);
    expect(revealRunCard(c, "wf_1")).toBe(false);
  });
});
