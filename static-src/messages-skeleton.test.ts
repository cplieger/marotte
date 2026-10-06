// The placeholder and the transcript never share the container. Reconcile inserts the newest turn after any unkeyed
// sibling, so a skeleton still mounted when content lands would sit above the conversation.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// The render graph reads the DOM registry, which throws on a missing app root, so every id exists before the imports.
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

// scroll.ts is a self-initialising singleton; the canonical mock is what every suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const { mountChatView, activeTranscriptView, teardownAll } = await import("./messages.js");
const { CHAT_SKELETON_ID, chatSkeleton } = await import("./skeleton.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");

const messagesEl = document.getElementById("messages") as HTMLElement;

/** One `TurnState` per turn, with the ids in `turn_order`, which the paint reads for file order. */
function activate(turnIDs: readonly string[]): void {
  const turns = new Map<string, TurnState>();
  turnIDs.forEach((id, i) => {
    turns.set(id, { entries: drawnTurn(id, i + 1), openEntries: new Map() });
  });
  setSessions([
    {
      id: "c-1",
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      supervised_mode: false,
      effort: "",
      effort_levels: [],
      effort_active: "",
      usage: { context_size: 0 },
      turns,
      turn_order: [...turnIDs],
      turn_count: turnIDs.length,
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    },
  ] as never);
  setActive("c-1");
}

/** The cheapest drawn turn: an agent-initiated `turn_open`, one text entry, its close. */
function drawnTurn(id: string, n: number): Entry[] {
  const at = (seq: number, kind: Entry["kind"], payload: unknown): Entry =>
    ({ id: `${id}-e${String(seq)}`, turn: id, kind, seq, ts: seq + 1, payload }) as Entry;
  return [
    at(0, "turn_open", { source: "agent", n }),
    at(1, "text", { text: `reply ${String(n)}` }),
    at(2, "turn_close", { outcome: "completed" }),
  ];
}

beforeEach(() => {
  mountChatView();
  // The real teardown: the multiplexer keeps a view registry, and a bare replaceChildren would leave the next activation
  // painting into a detached view.
  teardownAll();
  activate([]);
  // The active id survives across tests, so setActive alone is a no-op after the first.
  bumpMessages("c-1");
});

describe("the transcript's loading placeholder", () => {
  it("is dropped by the paint that brings in the first turn", () => {
    // Into the active view, the placeholder's one home (chat.ts mounts it there on a cold activation).
    const view = activeTranscriptView();
    expect(view).not.toBeNull();
    view?.appendChild(chatSkeleton());
    expect(document.getElementById(CHAT_SKELETON_ID)).not.toBeNull();

    activate(["m1"]);
    bumpMessages("c-1");

    expect(document.getElementById(CHAT_SKELETON_ID)).toBeNull();
    expect(messagesEl.querySelector(".turn")).not.toBeNull();
  });

  it("never ends up above the turns, which is where reconcile would leave it", () => {
    // Positional, not just co-presence: reconcile appends the newest turn after every unkeyed sibling in the active view.
    const view = activeTranscriptView();
    expect(view).not.toBeNull();
    view?.appendChild(chatSkeleton());
    activate(["m1", "m2"]);
    bumpMessages("c-1");

    const first = view?.firstElementChild;
    expect(first?.id).not.toBe(CHAT_SKELETON_ID);
    expect(first?.classList.contains("turn")).toBe(true);
  });

  it("survives a paint that produces no turns, because that is what it is for", () => {
    // An empty turn list is a chat still loading; dropping the placeholder then would show nothing.
    activeTranscriptView()?.appendChild(chatSkeleton());
    activate([]);
    bumpMessages("c-1");

    expect(document.getElementById(CHAT_SKELETON_ID)).not.toBeNull();
  });
});
