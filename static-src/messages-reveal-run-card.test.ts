// `revealRunCard` is best-effort: true when the card is mounted, false when its turn is a stub or the chat has no
// resident view. The real transcript runs, since a mocked renderer would let a document-wide lookup pass. A run's
// card belongs to the first `tool_call` entry whose `payload.workflow_id` names it, within the resident window.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// The render graph reads the DOM registry, which throws on a missing id; nested as the shipped page nests them.
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

// The canonical scroll mock; `jumpTo` is a spy, and asserting the call is asserting the contract.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Real except the two GETs made on paint (run state, turn index), which answer nothing.
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

/** A turn that launched a run: its `tool_call` entry's `payload.workflow_id` owns that run's card. */
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

/**
 * A turn that spends the whole entry budget, so every turn behind it is a stub. `thinking`, not `text`: a sealed
 * text body is one prose run and would cost one ordinal.
 */
function hugeTurn(turnID: string, n: number, count: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `step ${String(at)}` }));
  }
  out.push(sealed(turnID, count + 1, "turn_close", { outcome: "completed" }));
  return out;
}

/** Seed whole turns and paint them, as a window GET lands: `TurnState`s in `turn_order`, then the `load` bump. */
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
    // The premise: the card is mounted inside this chat's view.
    expect(runCard).not.toBeNull();

    expect(revealRunCard(c, "wf_1")).toBe(true);
    expect(scrollMock.jumpTo).toHaveBeenCalledTimes(1);
    expect(scrollMock.jumpTo.mock.calls[0]?.[0]).toBe(runCard);
  });

  // A turn outside the resident entry window is a stub with no `.turn-body`, so it holds no run card.
  it("answers false for a launching turn that folded to a stub", () => {
    const c = chatID();
    // The newest turn spends the whole budget, so the launching turn is outside the window.
    activate(c, [launcherTurn("t-old", 1, "wf_old"), hugeTurn("t-big", 2, RESIDENT_ENTRIES + 64)]);
    const view = activeTranscriptView();
    // The premise: the turn really is a stub.
    expect(card("t-old")?.querySelector(":scope > .turn-body")).toBeNull();
    expect(view?.querySelector('.run-card[data-run="wf_old"]')).toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // The window is paginated, so a run launched far enough back is not resident and no unfold can reach it.
  it("answers false when the launching turn has been paged out", () => {
    const c = chatID();
    // A resident view whose window does not hold the launcher.
    activate(c, [plainTurn("t1", 1), plainTurn("t2", 2)]);
    expect(activeTranscriptView()).not.toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // `.run-card[data-run]` repeats once per resident view, so a document-wide lookup could find a parked view's card.
  it("answers false, and touches nothing, for a chat with no resident view", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    // The card is in the document, under the chat that owns it.
    expect(document.querySelector('.run-card[data-run="wf_1"]')).not.toBeNull();

    expect(revealRunCard("c-never-opened", "wf_1")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

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

  // Dropping a chat's window disposes its view; with the view left standing the card would answer true.
  it("answers false when the chat's window is gone", () => {
    const c = chatID();
    activate(c, [launcherTurn("t1", 1, "wf_1")]);
    setTurnOpen(c, "t1", false);
    removeChat(c);
    expect(revealRunCard(c, "wf_1")).toBe(false);
  });
});
