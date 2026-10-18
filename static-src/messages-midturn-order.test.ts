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

function open(id: string, text: string, kind: OpenEntry["kind"] = "text"): OpenEntry {
  return { turn: TURN, id, kind, text, n: 1 };
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

/** Mount a chat mid-stream: one open turn whose `kind` entry `say-1` holds `text` so far. */
async function streamingAs(chatID: string, kind: OpenEntry["kind"], text: string): Promise<void> {
  store.setSessions([session(chatID)]);
  store.setActive(chatID);
  store.openTurn(chatID, turnOpen());
  store.openEntry(chatID, open("say-1", text, kind));
  await vi.waitFor(() => {
    expect(bodyRows()).toEqual(["open"]);
  });
}

/** An interjection: the entry takes the next seq while `say-1` stays open and keeps streaming. */
function interject(chatID: string, e: Entry, seq: number): void {
  store.appendEntry(chatID, { ...e, seq });
}

/** A reload of a stream an interjection landed in: the entry, then the whole say, then the close. */
function reloadedWhole(
  chatID: string,
  kind: Entry["kind"],
  e: Entry,
  text: string,
  lead: Entry[] = [],
): void {
  const s = session(chatID);
  s.thinking = false;
  const at = 1 + lead.length;
  s.turns.set(TURN, {
    entries: [
      turnOpen(),
      ...lead,
      { ...e, seq: at },
      sealed(at + 1, kind, { text }, "say-1"),
      sealed(at + 2, "turn_close", { outcome: "completed" }),
    ],
    openEntries: new Map(),
  });
  s.turn_order = [TURN];
  s.turn_count = 1;
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
}

function reasoningTexts(): string[] {
  return [...viewRoot().querySelectorAll<HTMLElement>(".turn-body > .reasoning-block")].map(
    (b) => b.querySelector(".reasoning-body")?.textContent ?? b.textContent ?? "",
  );
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
// A step's `send_message` received by the launching chat, not yet taken up.
const STEP_MESSAGE: Entry = sealed(
  1,
  "steer",
  {
    text: "Task 12 round 3, Claude seat: APPROVE.",
    origin: "step",
    step: "review-claude",
    produced_ts: 5,
  },
  "wfmsg-1",
);
const TAKE_UP: Entry = sealed(1, "steer_delivered", { steer_id: "wfmsg-0", read_ts: 9 });

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

  it("puts a mid-turn steer above the reply it arrived inside, and reloads it there", async () => {
    // Both paths in one test: the claim is that the two orders are the same order.
    const chat = "mto-steer";
    await streamingAs(chat, "text", "the reply so f");
    interject(chat, { ...STEER, seq: 1 }, 1);
    store.applyDelta(chat, TURN, "say-1", "", 2, "ar");

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["e-2", "open"]);
    });
    expect(viewRoot().querySelectorAll(".turn").length).toBe(1);
    // A steer opens no turn either, so a mid-turn correction cannot be hoisted out of the reply.
    const note = viewRoot().querySelector<HTMLElement>(".turn-body > .steer-note");
    expect(note).not.toBeNull();
    expect(note?.dataset["state"]).toBe("read");
    expect(note?.querySelector(".steer-note-label")?.textContent).toBe("Mid-turn message");
    // The caret stays on the reply, which is still one bubble.
    expect([...new Set(streamingRows())]).toEqual(["open"]);
    expect(
      viewRoot().querySelectorAll(
        ".turn-body > .message.assistant, .turn-body > * .message.assistant",
      ).length,
    ).toBe(1);

    store.sealEntry(chat, TURN, "say-1", "", 2, 3, 2);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["e-2", "say-1"]);
    });

    reloadedWhole(chat, "text", STEER, "the reply so far");
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["e-2", "say-1"]);
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

describe("an open entry's surface carries the entry's identity", () => {
  it.each(["text", "thinking"] as const)(
    "stamps the %s surface with its turn and entry while it streams, and with its seq once it seals there",
    async (kind) => {
      const chat = `mto-open-stamp-${kind}`;
      await streamingAs(chat, kind, "the reply so far");
      const surface = viewRoot().querySelector<HTMLElement>(".turn > .turn-body > :not(.spacer)");
      const streaming = { ...surface?.dataset };

      store.sealEntry(chat, TURN, "say-1", "", 1, 2, 1);
      await vi.waitFor(() => {
        expect(bodyRows()).toEqual(["say-1"]);
      });
      const now = viewRoot().querySelector<HTMLElement>(".turn > .turn-body > :not(.spacer)");

      expect({
        streaming: {
          turn: streaming["openTurn"],
          id: streaming["openId"],
          seq: streaming["entrySeq"],
        },
        sealed: {
          same: now === surface,
          open: now?.dataset["openId"],
          turn: now?.dataset["entryTurn"],
          seq: now?.dataset["entrySeq"],
        },
      }).toEqual({
        streaming: { turn: TURN, id: "say-1", seq: undefined },
        sealed: { same: true, open: undefined, turn: TURN, seq: "1" },
      });
    },
  );
});

describe("an entry arriving mid-stream does not cut the stream", () => {
  it("keeps one reasoning block around a step message, live and on reload", async () => {
    const chat = "mto-think-card";
    await streamingAs(chat, "thinking", "I need to locate the arrapi agent on Ag");
    interject(chat, STEP_MESSAGE, 1);
    store.applyDelta(chat, TURN, "say-1", "", 2, "ent Mail, register");

    // The card goes above the trace, which keeps streaming as the container's last element.
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "open"]);
    });
    const trace = viewRoot().querySelector<HTMLElement>(".turn-body > .reasoning-block");
    expect(trace).not.toBeNull();
    await vi.waitFor(() => {
      expect(trace?.textContent).toContain("arrapi agent on Agent Mail, register");
    });

    store.sealEntry(chat, TURN, "say-1", "", 2, 3, 2);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "say-1"]);
    });
    // The open trace sealed in place: one block, the same element.
    expect(viewRoot().querySelectorAll(".turn-body > .reasoning-block").length).toBe(1);
    expect(viewRoot().querySelector(".turn-body > .reasoning-block")).toBe(trace);

    reloadedWhole(
      chat,
      "thinking",
      STEP_MESSAGE,
      "I need to locate the arrapi agent on Agent Mail, register",
    );
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "say-1"]);
    });
    expect(viewRoot().querySelectorAll(".turn-body > .reasoning-block").length).toBe(1);
    expect(reasoningTexts()[0]).toContain("arrapi agent on Agent Mail, register");
  });

  it("keeps one reply bubble around a step message, with the caret on it, live and on reload", async () => {
    const chat = "mto-say-card";
    await streamingAs(chat, "text", "I need to locate the arrapi agent on Ag");
    interject(chat, STEP_MESSAGE, 1);
    store.applyDelta(chat, TURN, "say-1", "", 2, "ent Mail, register");

    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "open"]);
    });
    expect([...new Set(streamingRows())]).toEqual(["open"]);
    // One bubble holds both halves; the reveal may still hold back the last characters.
    expect(viewRoot().querySelectorAll(".turn-body .message.assistant").length).toBe(1);
    await vi.waitFor(() => {
      expect(viewRoot().querySelector(".streaming")?.textContent).toContain("agent on Agent Mail");
    });

    store.sealEntry(chat, TURN, "say-1", "", 2, 3, 2);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "say-1"]);
    });

    reloadedWhole(
      chat,
      "text",
      STEP_MESSAGE,
      "I need to locate the arrapi agent on Agent Mail, register",
    );
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["wfmsg-1", "say-1"]);
    });
    expect(streamingRows()).toEqual([]);
  });

  it("keeps an open reply apart from the message before it across a step message, as the reload draws it", async () => {
    // Two says back to back are two messages, so two bubbles; the card lands between them live and on reload.
    const chat = "mto-run-card";
    const first = sealed(1, "text", { text: "Done with the first part. " }, "say-0");
    store.setSessions([session(chat)]);
    store.setActive(chat);
    store.openTurn(chat, turnOpen());
    store.openEntry(chat, open("say-0", "Done with the first part. "));
    store.sealEntry(chat, TURN, "say-0", "", 1, 2, 1);
    store.openEntry(chat, open("say-1", "Next I"));
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-0", "open"]);
      expect(
        viewRoot().querySelectorAll(".turn-body .message.assistant")[1]?.textContent,
      ).toContain("Next");
    });

    interject(chat, { ...STEP_MESSAGE, seq: 2 }, 2);
    store.applyDelta(chat, TURN, "say-1", "", 2, " will register");
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-0", "wfmsg-1", "open"]);
    });
    const bubbles = [...viewRoot().querySelectorAll<HTMLElement>(".turn-body .message.assistant")];
    expect(bubbles.length).toBe(2);
    expect(bubbles[0]?.textContent).not.toContain("Next I");
    expect(bubbles[0]?.textContent).toContain("Done with the first part.");
    await vi.waitFor(() => {
      expect(bubbles[1]?.textContent).toContain("Next I will");
    });
    expect([...new Set(streamingRows())]).toEqual(["open"]);

    store.sealEntry(chat, TURN, "say-1", "", 3, 4, 2);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-0", "wfmsg-1", "say-1"]);
    });

    reloadedWhole(chat, "text", STEP_MESSAGE, "Next I will register", [first]);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-0", "wfmsg-1", "say-1"]);
    });
  });

  it("keeps one reasoning block across a take-up, which draws nothing", async () => {
    const chat = "mto-think-takeup";
    await streamingAs(chat, "thinking", "The checkpoint rule of counting after every 5");
    interject(chat, TAKE_UP, 1);
    store.applyDelta(chat, TURN, "say-1", "", 2, " merged landings looks right.");
    await vi.waitFor(() => {
      expect(viewRoot().querySelector(".turn-body > .reasoning-block")?.textContent).toContain(
        "every 5 merged landings",
      );
    });

    store.sealEntry(chat, TURN, "say-1", "", 2, 3, 2);
    await vi.waitFor(() => {
      expect(bodyRows()).toEqual(["say-1"]);
    });
    expect(viewRoot().querySelectorAll(".turn-body > .reasoning-block").length).toBe(1);
  });
});
