// An entry appended mid-turn renders at its `seq` on the live and reload paths alike, so both orders are the same
// order; the real renderer runs through the real store.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

// The import graph reaches the DOM registry, which throws on a missing app root.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? document.getElementById("messages")!;
}

// --- Fixtures -------------------------------------------------------------------------

const TURN = "t-1";

function session(id: string): Session {
  return makeSession({ id, name: id, thinking: true });
}

function sealed(
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id = `e-${String(seq)}`,
): Entry {
  return { id, turn: TURN, kind, seq, ts: seq + 1, payload };
}

function turnOpen(): Entry {
  return sealed(0, "turn_open", {
    prompt: { id: "m-1", text: "do the thing" },
    source: "prompt",
    n: 1,
  });
}

function open(id: string, text: string): OpenEntry {
  return { turn: TURN, id, kind: "text", text, n: 1 };
}

/**
 * The body's rows in DOM order, named by entry. A prose run is stamped for its first entry (rest in
 * `data-entries`); an open run reads as `open`.
 */
function bodyRows(): string[] {
  const body = viewRoot().querySelector<HTMLElement>(".turn > .turn-body");
  expect(body).not.toBeNull();
  return [...body!.children]
    .filter(
      (row): row is HTMLElement => row instanceof HTMLElement && !row.classList.contains("spacer"),
    )
    .map((row) => row.dataset["entryId"] ?? "open");
}

function streamingRows(): string[] {
  return [...viewRoot().querySelectorAll<HTMLElement>(".streaming")].map(
    (elm) => elm.closest<HTMLElement>(".turn-body > *")?.dataset["entryId"] ?? "open",
  );
}

/** Mount a chat mid-reply: one open turn whose first text entry is still streaming. */
async function streamingChat(chatID: string): Promise<void> {
  store.setSessions([session(chatID)]);
  store.setActive(chatID);
  store.openTurn(chatID, turnOpen());
  store.openEntry(chatID, open("say-1", "the reply so far"));
  await vi.waitFor(() => {
    expect(viewRoot().querySelector(".message.assistant.streaming")).not.toBeNull();
  });
}

/**
 * The mid-turn append shape: KAS seals the open say before any non-text frame, the entry takes the next seq, and
 * the reply continues as a new open entry.
 */
function interrupt(chatID: string, e: Entry): void {
  store.sealEntry(chatID, TURN, "say-1", "", 1, 2, 1);
  store.appendEntry(chatID, { ...e, seq: 2 });
  store.openEntry(chatID, open("say-1#2", "and the rest"));
}

/** A reload: a fresh session holding the same SEALED entries and no open tail. */
function reloaded(chatID: string, e: Entry): void {
  const s = session(chatID);
  s.thinking = false;
  s.turns.set(TURN, {
    entries: [
      turnOpen(),
      sealed(1, "text", { text: "the reply so far" }, "say-1"),
      { ...e, seq: 2 },
      sealed(3, "text", { text: "and the rest" }, "say-1#2"),
      sealed(4, "turn_close", { outcome: "completed" }),
    ],
    openEntries: new Map(),
  });
  s.turn_order = [TURN];
  s.turn_count = 1;
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

const PLAN: Entry = sealed(2, "plan", {
  entries: [{ content: "step one", priority: "high", status: "pending" }],
});
const FAILED: Entry = sealed(2, "compaction_failed", {
  reason: "the context could not be summarised",
});
const COMPACTED: Entry = sealed(2, "compaction", { summary: "the summary" });
// A steer renders through `mountSteerNote`, whose placement is its own path, so a plan cannot stand in for it.
const STEER: Entry = sealed(2, "steer", {
  text: "actually, use the other file",
  origin: "user",
  state: "read",
});

describe("an entry appended mid-turn renders at its own seq", () => {
  it("puts the plan between the prose it split, in one card, with the caret on the open tail", async () => {
    const chat = "mto-plan";
    await streamingChat(chat);
    interrupt(chat, PLAN);

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "open"]);
    });
    // One card: only a `turn_open` opens a turn.
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    expect(viewRoot().querySelector(".plan-message")).not.toBeNull();
    // One caret, on the entry being written; the prose above the plan is a separate run now.
    expect([...new Set(streamingRows())]).toEqual(["open"]);
  });

  it("puts a compaction failure at its seq too", async () => {
    const chat = "mto-evt";
    await streamingChat(chat);
    interrupt(chat, FAILED);

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "open"]);
    });
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    // The label is asserted beside the class: a class alone cannot tell this row from a turn that ended badly.
    const row = viewRoot().querySelector<HTMLElement>(".boundary.boundary-failed");
    expect(row).not.toBeNull();
    expect(row?.querySelector(".boundary-label")?.textContent).toBe(
      "Compaction failed: the context could not be summarised",
    );
  });

  it("puts a compaction between the prose it split, summary row and all", async () => {
    const chat = "mto-compaction";
    await streamingChat(chat);
    interrupt(chat, COMPACTED);

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "open"]);
    });
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    // A compaction with a summary is the disclosing row; the label is asserted for the reason above.
    const row = viewRoot().querySelector<HTMLElement>("details.compaction");
    expect(row).not.toBeNull();
    expect(row?.querySelector(".compaction-label")?.textContent).toBe("Conversation compacted");
    expect([...new Set(streamingRows())]).toEqual(["open"]);
  });

  it("puts a mid-turn steer between the prose it split, and reloads it there", async () => {
    // Both paths in one test: the claim is that the two orders are the same order.
    const chat = "mto-steer";
    await streamingChat(chat);
    interrupt(chat, STEER);

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "open"]);
    });
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    // A steer opens no turn either, so a mid-turn correction cannot be hoisted out of the reply.
    const note = viewRoot().querySelector<HTMLElement>(".turn-body > .steer-note");
    expect(note).not.toBeNull();
    expect(note?.dataset["state"]).toBe("read");
    expect(note?.querySelector(".steer-note-label")?.textContent).toBe("Mid-turn message");
    // The caret is on the open tail, never on the prose above the note.
    expect([...new Set(streamingRows())]).toEqual(["open"]);

    reloaded(chat, STEER);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "say-1#2"]);
    });
    expect(viewRoot().querySelector(".turn-body > .steer-note")).not.toBeNull();
    expect(streamingRows()).toEqual([]);
  });

  it("renders the reload in the same order the live path did", async () => {
    const chat = "mto-reload";
    await streamingChat(chat);
    interrupt(chat, PLAN);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "open"]);
    });

    reloaded(chat, PLAN);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1", "e-2", "say-1#2"]);
    });
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    expect(streamingRows()).toEqual([]);
  });
});
