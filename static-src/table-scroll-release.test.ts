// A markdown table in a transcript is watched by the shared observer in `table-scroll.ts`; the
// transcript's teardown paths must let go of it, or the discarded DOM stays reachable through the
// observer. Driven through a mounted chat view, the one place every close path runs.
import { describe, it, expect, vi } from "vitest";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";
import { tableObservationCount } from "./table-scroll.js";

// Built before `messages.js` is imported: `scroll.ts` reads the scroller from the DOM registry at import.
for (const id of [
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
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const transcriptEl = document.createElement("div");
transcriptEl.id = "messages";
scrollerEl.appendChild(transcriptEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, disposeChatView } = await import("./messages.js");
const { KEY_ATTR } = await import("./reconcile.js");

const TABLE = "| a | b |\n|---|---|\n| 1 | 2 |\n";

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

function turnWithTable(id: string, n: number): Entry[] {
  return [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: "show me" }, source: "prompt", n }),
    sealed(id, 1, "text", { text: TABLE }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** A turn that ran a tool, so its fold hides something and residency may unmount its body. */
function toolTurnWithTable(id: string, n: number, table = TABLE): Entry[] {
  return [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: "show me" }, source: "prompt", n }),
    sealed(id, 1, "tool_call", {
      id: `${id}-tc`,
      title: "Read File",
      kind: "read",
      status: "completed",
      ts: 1,
    }),
    sealed(id, 2, "text", { text: table }),
    sealed(id, 3, "turn_close", { outcome: "completed" }),
  ];
}

/** A turn still open, so parking its chat marks the body for a rebuild on return. The prose after the
 *  table completes the table's block, which a live stream otherwise holds back. */
function openTurnWithTable(id: string, n: number): Entry[] {
  return [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: "show me" }, source: "prompt", n }),
    sealed(id, 1, "text", { text: `${TABLE}\nafter the table\n` }),
  ];
}

function paint(chat: string, turnRows: Entry[][]): void {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnRows) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chat, name: "c" }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
}

/** Tear every resident view down, so a case counts from the empty transcript. */
function reset(): void {
  setSessions([]);
  setActive("");
}

describe("a transcript's markdown tables", () => {
  it("are released when the chat view is disposed", () => {
    reset();
    const before = tableObservationCount();
    mountChatView();
    const chat = "c-table-dispose";
    paint(chat, [turnWithTable("t0", 1), turnWithTable("t1", 2), turnWithTable("t2", 3)]);
    expect(transcriptEl.querySelectorAll("table").length, "tables rendered").toBe(3);
    expect(tableObservationCount() - before, "one watched per table").toBe(3);

    disposeChatView(chat);

    expect(tableObservationCount() - before).toBe(0);
  });

  it("are released for a turn card a rewind drops while the view lives", () => {
    reset();
    const before = tableObservationCount();
    mountChatView();
    const chat = "c-table-rewind";
    paint(chat, [turnWithTable("t0", 1), turnWithTable("t1", 2), turnWithTable("t2", 3)]);
    expect(tableObservationCount() - before).toBe(3);

    paint(chat, [turnWithTable("t0", 1)]);

    expect(tableObservationCount() - before, "only the surviving card's table").toBe(1);
    disposeChatView(chat);
    expect(tableObservationCount() - before).toBe(0);
  });

  it("are released for a body residency unmounts while the card stays", async () => {
    reset();
    const before = tableObservationCount();
    mountChatView();
    const chat = "c-table-unmount";
    const target = toolTurnWithTable("t0", 1);
    paint(chat, [target]);
    const bodyTable = transcriptEl.querySelector("table");
    expect(bodyTable, "the resident body's table").not.toBeNull();
    expect(tableObservationCount() - before).toBe(1);

    // Three more turns fold the older tool turn, and a folded turn is not kept resident.
    paint(chat, [target, turnWithTable("t1", 2), turnWithTable("t2", 3), turnWithTable("t3", 4)]);
    await vi.waitFor(() => {
      const card = transcriptEl.querySelector(`[${KEY_ATTR}="t0"]`);
      expect(card, "the card stays").not.toBeNull();
      expect(card?.querySelector(":scope > .turn-body"), "the body is gone").toBeNull();
    });

    // The folded card's face renders the reply again, so the DOM holds a fresh table in its place.
    expect(bodyTable?.isConnected, "the body's table left the page").toBe(false);
    expect(
      tableObservationCount() - before,
      "one watched per table still in the page, none for the unmounted one",
    ).toBe(transcriptEl.querySelectorAll("table").length);
    disposeChatView(chat);
    expect(tableObservationCount() - before).toBe(0);
  });

  it("are released for a body rebuilt in place on unpark", async () => {
    reset();
    const before = tableObservationCount();
    mountChatView();
    const chat = "c-table-rebuild";
    const other = "c-table-rebuild-other";
    setSessions([
      {
        ...makeSession({ id: chat, name: "c", thinking: true }),
        turns: new Map([["t0", { entries: openTurnWithTable("t0", 1), openEntries: new Map() }]]),
        turn_order: ["t0"],
        turn_count: 1,
      },
      makeSession({ id: other, name: "o" }),
    ]);
    setActive(chat);
    bumpMessages(chat, "load");
    const first = transcriptEl.querySelector("table");
    expect(first, "the open turn's table").not.toBeNull();
    expect(tableObservationCount() - before).toBe(1);

    // Parking marks the open turn for a rebuild; coming back replaces its body.
    setActive(other);
    await Promise.resolve();
    setActive(chat);
    await vi.waitFor(() => {
      expect(first?.isConnected, "the old body is replaced").toBe(false);
    });

    expect(transcriptEl.querySelectorAll("table").length, "one table, not two").toBe(1);
    expect(tableObservationCount() - before, "the replaced table is let go").toBe(1);
    disposeChatView(chat);
    disposeChatView(other);
    expect(tableObservationCount() - before).toBe(0);
  });

  it("are released for a folded turn's face the card replaces while it stays", async () => {
    reset();
    const before = tableObservationCount();
    mountChatView();
    const chat = "c-table-face";
    const target = toolTurnWithTable("t0", 1);
    paint(chat, [target, turnWithTable("t1", 2), turnWithTable("t2", 3), turnWithTable("t3", 4)]);
    await vi.waitFor(() => {
      const card = transcriptEl.querySelector(`[${KEY_ATTR}="t0"]`);
      expect(
        card?.querySelector(":scope > .turn-face table"),
        "the folded face's table",
      ).not.toBeNull();
    });
    const face = transcriptEl.querySelector(`[${KEY_ATTR}="t0"] > .turn-face`);
    expect(tableObservationCount() - before, "one watched per table in the page").toBe(
      transcriptEl.querySelectorAll("table").length,
    );

    // A longer final answer changes the face's key, so the card drops the face and renders another.
    // No frame is awaited: only the card's own release can lower the count before the next report.
    paint(chat, [
      toolTurnWithTable("t0", 1, `${TABLE}\nmore prose in the answer\n`),
      turnWithTable("t1", 2),
      turnWithTable("t2", 3),
      turnWithTable("t3", 4),
    ]);

    expect(face?.isConnected, "the old face left the page").toBe(false);
    expect(
      tableObservationCount() - before,
      "one watched per table still in the page, none for the replaced face",
    ).toBe(transcriptEl.querySelectorAll("table").length);
    disposeChatView(chat);
    expect(tableObservationCount() - before).toBe(0);
  });
});
