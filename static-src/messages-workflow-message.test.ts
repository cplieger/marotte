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
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
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
    const toggles = view.querySelectorAll(".steer-group-toggle");
    expect(toggles).toHaveLength(1);
    expect(toggles[0]?.textContent).toBe("3 updates from 1 workflow");
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

    const toggle = view.querySelector(".steer-group-toggle");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    const updates = [...view.querySelectorAll<HTMLElement>('.steer-note[data-origin="step"]')];
    expect(updates).toHaveLength(2);
    expect(updates.every((r) => r.hidden)).toBe(true);
  });
});
