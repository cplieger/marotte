// The dock's COMPACTION MARKER: a row queued before a mid-turn compaction says so, and a row queued
// after it does not.
import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import type * as ChatActions from "./actions/chat.js";

// The row's controls dispatch an action and open a confirm. The marker is what is under test and
// the real modules would pull the action framework, the transport and a native <dialog> in behind
// it.
const mocks = vi.hoisted(() => ({
  clearDispatch: vi.fn(() => Promise.resolve(true)),
  cancelDispatch: vi.fn(() => ({
    outcome: Promise.resolve<{ status: string }>({ status: "success" }),
  })),
  confirmMock: vi.fn((_message: string) => Promise.resolve(true)),
  setComposerValueMock: vi.fn(),
}));

vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  clearSteers: { dispatch: mocks.clearDispatch },
  cancelTurn: { dispatch: mocks.cancelDispatch },
  removeSteer: { dispatch: vi.fn() },
}));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirmMock }));
vi.mock("./composer-value.js", () => ({ setComposerValue: mocks.setComposerValueMock }));
// Every export is present so real-ESM linking succeeds; no case here restores a draft.
vi.mock("./composer-state.js", () => ({
  initComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  flushComposerDraft: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
  restoreFailedSend: vi.fn(),
  composerDraft: vi.fn(() => ""),
  restoreRefusedEdit: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  dropComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));
// The real module reaches actions/chat.js, mocked above; every export is present so real-ESM
// linking succeeds.
vi.mock("./attachments.js", () => ({
  addAttachmentTo: vi.fn(),
  removeAttachmentFrom: vi.fn(),
  attachmentGeneration: vi.fn(() => 0),
  addAttachment: vi.fn(),
  takeAttachments: vi.fn(() => []),
  hasAttachments: vi.fn(() => false),
  stashAttachments: vi.fn(),
  flushAttachments: vi.fn(),
  restoreAttachments: vi.fn(),
  dropAttachments: vi.fn(),
  seedAttachments: vi.fn(),
  adoptRemoteAttachments: vi.fn(),
  _resetAttachmentsForTest: vi.fn(),
}));

import {
  setSessions,
  setActive,
  recordSteerQueued,
  markSteersCompacted,
  openTurn,
  appendEntry,
} from "./store.js";
import { initPendingSteers } from "./pending-steers.js";
import type { Entry, Session } from "./types.js";

const CHAT = "chat-1";
const OTHER = "chat-2";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The seq the next entry of the fixture turn takes. Reset per case, because `appendEntry`
 *  refuses any seq that is not the turn's next one. */
let seq = 0;

function openFixtureTurn(chatID: string): void {
  const open: Entry = {
    id: "t1-open",
    turn: "t1",
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: "m-0", text: "hi" } },
  };
  openTurn(chatID, open);
  seq = 1;
}

/** The `steer` entry KAS's own frame produces for a steer the agent READ. That entry is the
 *  row's LEAVE, and `appendEntry` removes the row in the same store update that seats it. */
function landSteerEntry(chatID: string, steerID: string, text: string): void {
  const entry: Entry = {
    id: steerID,
    turn: "t1",
    kind: "steer",
    seq,
    ts: seq + 1,
    payload: { text, origin: "user", state: "read" },
  };
  seq += 1;
  appendEntry(chatID, entry);
}

function rows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("#steer-stack .steer-row"));
}

function textOf(row: HTMLElement): string {
  return row.querySelector(".steer-text")?.textContent ?? "";
}

function markedOf(row: HTMLElement): boolean {
  return row.hasAttribute("data-compacted");
}

function labelOf(row: HTMLElement): string {
  return row.querySelector(".steer-state-label")?.textContent ?? "";
}

function nameOf(row: HTMLElement): string {
  return row.getAttribute("aria-label") ?? "";
}

describe("the dock's compaction marker", () => {
  // The stack element is captured once, by the module's own idempotent init, so it has to outlive
  // every case: replacing it per test would leave the effect painting into a detached node.
  beforeAll(() => {
    document.body.innerHTML = `
      <ul id="steer-stack" class="steer-stack hidden"></ul>
      <textarea id="prompt-input"></textarea>`;
    initPendingSteers();
  });

  beforeEach(() => {
    seq = 0;
    setSessions([makeSession(CHAT), makeSession(OTHER)]);
    setActive(CHAT);
    expect(rows()).toHaveLength(0);
  });

  // BOTH rows, and the two channels a reader has: the attribute CSS keys on, and the extended word,
  // which the visible label and the accessible name both read from one spelling so they cannot
  // disagree.
  it("marks every row the dock holds when the compaction lands", () => {
    recordSteerQueued(CHAT, { id: "steer-1", text: "use tabs", origin: "user" });
    recordSteerQueued(CHAT, { id: "steer-2", text: "and rename it", origin: "user" });
    expect(rows().map(markedOf)).toEqual([false, false]);
    expect(rows().map(labelOf)).toEqual(["Sent", "Sent"]);

    markSteersCompacted(CHAT);

    expect(rows().map(markedOf)).toEqual([true, true]);
    expect(rows().map(labelOf)).toEqual([
      "Sent, context compacted since",
      "Sent, context compacted since",
    ]);
    expect(nameOf(rows()[0] as HTMLElement)).toBe(
      "Sent, context compacted since, waiting for the agent: use tabs",
    );
  });

  // A row that arrives AFTERWARDS was queued against the summarized context, so the compaction is
  // in its past on both sides and there is nothing to warn about. This is the case arrival order
  // buys, and the control that stops the marker being read as "this chat compacted at some point".
  it("leaves a row that arrives after the compaction unmarked", () => {
    recordSteerQueued(CHAT, { id: "steer-1", text: "before", origin: "user" });
    markSteersCompacted(CHAT);
    recordSteerQueued(CHAT, { id: "steer-2", text: "after", origin: "user" });

    expect(rows().map(textOf)).toEqual(["before", "after"]);
    expect(rows().map(markedOf)).toEqual([true, false]);
    expect(rows().map(labelOf)).toEqual(["Sent, context compacted since", "Sent"]);
  });

  // The store operation is what carries the scoping — the handler hands it the chat id off the
  // frame's envelope — so another chat's compaction reaches these rows through nothing.
  it("marks nothing for a compaction that landed on another chat", () => {
    recordSteerQueued(CHAT, { id: "steer-1", text: "mine", origin: "user" });

    markSteersCompacted(OTHER);

    expect(rows().map(markedOf)).toEqual([false]);
    expect(labelOf(rows()[0] as HTMLElement)).toBe("Sent");
  });

  // A LATCH: the store only ever sets it, so a second compaction over the same rows changes nothing
  // and no path clears the attribute. Without this the marker could be written as a per-compaction
  // flag and a later frame would silently unmark rows the first one was right about.
  it("keeps the mark across a second compaction", () => {
    recordSteerQueued(CHAT, { id: "steer-1", text: "one", origin: "user" });
    markSteersCompacted(CHAT);
    markSteersCompacted(CHAT);

    expect(rows().map(markedOf)).toEqual([true]);
    expect(labelOf(rows()[0] as HTMLElement)).toBe("Sent, context compacted since");
  });

  // A chat switch REBUILDS the stack from the store rather than updating rows in place, so the mark
  // has to be written on the row's first paint as well as on an update. Two write sites, and only
  // this case reaches the first one — every case above marks a row that is already on screen.
  it("rebuilds a marked row still marked after a chat switch", () => {
    recordSteerQueued(CHAT, { id: "steer-1", text: "before the switch", origin: "user" });
    markSteersCompacted(CHAT);

    setActive(OTHER);
    expect(rows()).toHaveLength(0);
    setActive(CHAT);

    expect(rows().map(textOf)).toEqual(["before the switch"]);
    expect(rows().map(markedOf)).toEqual([true]);
    expect(labelOf(rows()[0] as HTMLElement)).toBe("Sent, context compacted since");
  });

  // The marker changes what a row SAYS, never how long it lives: a marked row still leaves on its
  // own `steer` entry, which is the dock's whole invariant. Without this every case above passes
  // just as well for a marker that pinned its row in place.
  it("does not keep a marked row past its own steer entry", () => {
    openFixtureTurn(CHAT);
    recordSteerQueued(CHAT, { id: "steer-1", text: "read me", origin: "user" });
    recordSteerQueued(CHAT, { id: "steer-2", text: "still waiting", origin: "user" });
    markSteersCompacted(CHAT);
    expect(rows().map(markedOf)).toEqual([true, true]);

    landSteerEntry(CHAT, "steer-1", "read me");

    expect(rows().map(textOf)).toEqual(["still waiting"]);
    expect(rows().map(markedOf)).toEqual([true]);
  });
});
