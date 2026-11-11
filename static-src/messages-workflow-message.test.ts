import { describe, it, expect, beforeEach } from "vitest";
import type { Entry, Session, TurnState } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";

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

import { loadCSS } from "./__test-helpers__/css-rules.js";
import { formatClock } from "./locale-time.js";

const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown, id?: string) {
  return {
    id: id ?? `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

function turnOpen(turnID: string, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n: 1,
  });
}

function turnClose(turnID: string, at: number): Entry {
  return sealed(turnID, at, "turn_close", { outcome: "completed" });
}

const SENT = Date.UTC(2026, 9, 9, 12, 0, 5);
const READ = SENT + 3 * 60_000;

function clock(ms: number): string {
  return formatClock(ms, { seconds: true });
}

function update(turnID: string, at: number, n: number, readTs?: number): Entry {
  return sealed(
    turnID,
    at,
    "steer",
    {
      text: `part ${String(n)} done`,
      origin: "step",
      step: "review",
      origin_run: "wf_1",
      produced_ts: SENT,
      ...(readTs === undefined ? {} : { state: "read", read_ts: readTs }),
    },
    `wfmsg-${String(n)}`,
  );
}

function delivered(turnID: string, at: number, n: number): Entry {
  return sealed(
    turnID,
    at,
    "steer_delivered",
    { steer_id: `wfmsg-${String(n)}`, kas_id: `notify-${String(n)}`, read_ts: READ },
    `wfmsg-${String(n)}:delivered`,
  );
}

async function paint(chatID: string, turns: [string, Entry[]][]): Promise<HTMLElement> {
  const map = new Map<string, TurnState>(
    turns.map(([id, entries]) => [id, { entries, openEntries: new Map() }]),
  );
  store.setSessions([
    session(chatID, { turns: map, turn_order: turns.map(([id]) => id), turn_count: turns.length }),
  ]);
  store.setActive(chatID);
  store.setPainted(chatID);
  store.bumpMessages(chatID, "load");
  await Promise.resolve();
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

describe("a step's notice KAS logged when it read it", () => {
  it("states when it was sent and nothing about delivery, however long it stays in the log", async () => {
    const c = "c-wf-notice";
    const view = await paint(c, [
      [
        "t1",
        [
          turnOpen("t1"),
          turnClose("t1", 1),
          sealed(
            "t1",
            2,
            "steer",
            { text: "Design tightened.", origin: "step", severity: "success", produced_ts: SENT },
            "notify-269c88e4",
          ),
        ],
      ],
    ]);

    expect(view.querySelector(".steer-note-times")?.textContent).toBe(`Sent ${clock(SENT)}`);
  });
});

describe("a parent's buffered workflow updates", () => {
  it("are one row each, delivered as the read settled them, the take-ups drawing nothing, and grouped", async () => {
    const c = "c-wf-barrage";
    const view = await paint(c, [
      [
        "t1",
        [
          turnOpen("t1"),
          turnClose("t1", 1),
          update("t1", 2, 1, READ),
          update("t1", 3, 2, READ),
          update("t1", 4, 3, READ),
        ],
      ],
      [
        "t2",
        [
          turnOpen("t2", "next"),
          delivered("t2", 1, 1),
          delivered("t2", 2, 2),
          delivered("t2", 3, 3),
          turnClose("t2", 4),
        ],
      ],
    ]);

    const rows = [...view.querySelectorAll<HTMLElement>(".steer-note")];
    expect(rows).toHaveLength(3);
    for (const r of rows) {
      expect(r.querySelector(".steer-note-label")?.textContent).toBe("review");
      expect(r.querySelector(".steer-note-times")?.textContent).toBe(
        `Sent ${clock(SENT)} \u00b7 delivered ${clock(READ)}`,
      );
    }
    const boxes = view.querySelectorAll(".steer-group");
    expect(boxes).toHaveLength(1);
    expect(boxes[0]?.querySelector(".steer-group-header")?.textContent).toBe("3 updates from wf_1");
    expect(rows.every((r) => r.parentElement?.parentElement === boxes[0])).toBe(true);
  });

  it("redraws a drawn row delivered when its take-up arrives later", async () => {
    const c = "c-wf-live";
    const view = await paint(c, [
      ["t1", [turnOpen("t1"), turnClose("t1", 1), update("t1", 2, 1)]],
      ["t2", [turnOpen("t2", "next")]],
    ]);
    expect(view.querySelector(".steer-note-times")?.textContent).toBe(
      `Sent ${clock(SENT)} \u00b7 not delivered yet`,
    );

    store.appendEntry(c, delivered("t2", 1, 1));
    await Promise.resolve();

    const rows = [...view.querySelectorAll<HTMLElement>(".steer-note")];
    expect(rows).toHaveLength(1);
    expect(rows[0]?.querySelector(".steer-note-times")?.textContent).toBe(
      `Sent ${clock(SENT)} \u00b7 delivered ${clock(READ)}`,
    );
  });

  it("redraws a delivered row not delivered once a rewind takes its take-up's turn", async () => {
    const c = "c-wf-rewind";
    const view = await paint(c, [
      ["t1", [turnOpen("t1"), turnClose("t1", 1), update("t1", 2, 1, READ)]],
      [
        "t2",
        [
          sealed("t2", 0, "turn_open", {
            prompt: { id: "t2-p", text: "next" },
            source: "prompt",
            n: 2,
          }),
          delivered("t2", 1, 1),
          turnClose("t2", 2),
        ],
      ],
    ]);
    expect(view.querySelector(".steer-note-times")?.textContent).toBe(
      `Sent ${clock(SENT)} \u00b7 delivered ${clock(READ)}`,
    );

    store.appendEntry(
      c,
      sealed("t1", 3, "turn_revert", { from: "t2", from_n: 2, through: "t2" }, "t2:revert"),
    );
    await Promise.resolve();

    const rows = [...view.querySelectorAll<HTMLElement>(".steer-note")];
    expect(rows).toHaveLength(1);
    expect(rows[0]?.querySelector(".steer-note-times")?.textContent).toBe(
      `Sent ${clock(SENT)} \u00b7 not delivered yet`,
    );
  });

  it("close once an ordinary row follows them in the same pass", async () => {
    const c = "c-wf-tail";
    const view = await paint(c, [
      [
        "t1",
        [
          turnOpen("t1"),
          turnClose("t1", 1),
          update("t1", 2, 1),
          update("t1", 3, 2),
          sealed("t1", 4, "steer", { text: "and the docs", origin: "user", state: "read" }),
        ],
      ],
    ]);

    const head = view.querySelector(".steer-group > .steer-group-header");
    expect(head?.getAttribute("aria-expanded")).toBe("false");
    const updates = [...view.querySelectorAll<HTMLElement>('.steer-note[data-origin="step"]')];
    expect(updates).toHaveLength(2);
    expect(updates.every((r) => r.closest('.steer-group-body[aria-hidden="true"]') !== null)).toBe(
      true,
    );
  });
});

describe("a message between two chats", () => {
  function peer(turnID: string, at: number, n: number, over: Record<string, unknown>): Entry {
    return sealed(
      turnID,
      at,
      "steer",
      { text: `note ${String(n)}`, origin: "peer", severity: "info", produced_ts: SENT, ...over },
      `wfmsg-p${String(n)}`,
    );
  }

  it("names the other chat in both directions, tracks delivery, and opens it from the label", async () => {
    const c = "c-peer-view";
    const turns = new Map<string, TurnState>([
      [
        "t1",
        {
          entries: [
            turnOpen("t1"),
            turnClose("t1", 1),
            peer("t1", 2, 1, { chat: "c-billing", to_session: "sess_view" }),
            peer("t1", 3, 2, { chat: "c-billing" }),
            peer("t1", 4, 3, { chat: "c-gone", to_session: "sess_view" }),
          ],
          openEntries: new Map(),
        },
      ],
    ]);
    store.setSessions([
      session(c, { turns, turn_order: ["t1"], turn_count: 1 }),
      session("c-billing", { name: "Billing" }),
    ]);
    store.setActive(c);
    store.setPainted(c);
    store.bumpMessages(c, "load");
    await Promise.resolve();
    const view = messages.transcriptViewFor(c);
    const notes = Array.from(view?.querySelectorAll<HTMLElement>(".steer-note") ?? []);
    const labels = notes.map((n) => n.querySelector(".steer-note-label")?.textContent);

    expect(labels).toEqual(["From Billing", "To Billing", "Message from another chat"]);
    expect(notes[0]?.querySelector("button.steer-note-chat")).not.toBeNull();
    expect(notes[2]?.querySelector("button.steer-note-chat")).toBeNull();
    expect(notes[0]?.querySelector(".steer-note-times")?.textContent).toBe(
      `Sent ${clock(SENT)} \u00b7 not delivered yet`,
    );
  });
});
