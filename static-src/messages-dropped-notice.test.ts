// KAS's boundary clear names the agent notes the turn injected, so an old log holds a text-less dropped twin of a
// workflow result the agent read. The transcript draws no "not delivered" row for it, cold or live, and still draws
// one for a note the turn never read. Entries from chat c-8c158c28 turn 2, ids kept.
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

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

const T1 = "t-plotzzxpbd7vypn7yaajpornse";
const T2 = "t-uw5nzhg42na346yb4nwnnct76u";
const ROW = "wfmsg-01a12d1d-aa3f-75a2-8566-073e5566ddc6";
const KAS = "notify-feedda60-b84d-4648-95b4-78a4e4971394";

function entry(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id: string,
): Entry {
  return { id, turn, kind, seq, ts: 1791758302180 + seq, payload } as Entry;
}

/** The dropped twin, as line 89 of the chat's log holds it. */
const DROPPED = entry(
  T2,
  5,
  "steer",
  { text: "", origin: "agent", state: "dropped", reason: "boundary" },
  KAS,
);

function turns(withDrop: boolean): [string, Entry[]][] {
  return [
    [
      T1,
      [
        entry(
          T1,
          0,
          "turn_open",
          { prompt: { id: "m-1", text: "go" }, source: "prompt", n: 1 },
          T1,
        ),
        entry(T1, 1, "turn_close", { outcome: "completed" }, `${T1}:e1`),
        entry(
          T1,
          2,
          "steer",
          {
            text: "Report written to report.md.",
            origin: "step",
            step: "investigate",
            origin_run: "wf_1",
            produced_ts: 1791758302000,
          },
          ROW,
        ),
      ],
    ],
    [
      T2,
      [
        entry(T2, 0, "turn_open", { source: "wire_turn_start", n: 2 }, T2),
        entry(
          T2,
          1,
          "steer_delivered",
          { steer_id: ROW, kas_id: KAS, read_ts: 1791758302180 },
          `${ROW}:delivered`,
        ),
        entry(T2, 2, "thinking", { text: "Summarise the report." }, `${T2}:think`),
        entry(T2, 3, "text", { text: "The report is in." }, `${T2}:say`),
        entry(T2, 4, "turn_close", { outcome: "completed" }, `${T2}:e1`),
        ...(withDrop ? [DROPPED] : []),
      ],
    ],
  ];
}

async function paint(chatID: string, held: [string, Entry[]][]): Promise<HTMLElement> {
  const map = new Map<string, TurnState>(
    held.map(([id, entries]) => [id, { entries, openEntries: new Map() }]),
  );
  const s: Session = {
    ...makeSession({ id: chatID, name: chatID }),
    turns: map,
    turn_order: held.map(([id]) => id),
    turn_count: held.length,
  };
  store.setSessions([s]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
  await Promise.resolve();
  const view = messages.transcriptViewFor(chatID);
  if (view === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return view;
}

function shown(view: HTMLElement): { fallbacks: number; drops: number; labels: string[] } {
  return {
    fallbacks: view.querySelectorAll(".entry-fallback").length,
    drops: view.querySelectorAll(`[data-entry-id="${KAS}"]`).length,
    labels: [...view.querySelectorAll(".steer-note-label")].map((l) => l.textContent ?? ""),
  };
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe("a dropped twin of a workflow result the turn read", () => {
  it("draws nothing on a log that already holds it", async () => {
    const view = await paint("c-drop-cold", turns(true));
    expect(shown(view)).toEqual({ fallbacks: 0, drops: 0, labels: ["investigate"] });
  });

  it("draws nothing when it arrives after the turn closed", async () => {
    const view = await paint("c-drop-live", turns(false));
    store.appendEntry("c-drop-live", DROPPED);
    await Promise.resolve();
    expect(shown(view)).toEqual({ fallbacks: 0, drops: 0, labels: ["investigate"] });
  });

  it("still draws a note the turn never read as not delivered", async () => {
    const held = turns(true);
    const t2 = held[1]?.[1];
    t2?.splice(1, 1, entry(T2, 1, "text", { text: "Working." }, `${T2}:pre`));
    const view = await paint("c-drop-unread", held);
    expect(shown(view)).toEqual({
      fallbacks: 0,
      drops: 1,
      labels: ["investigate", "Workflow result not delivered \u00b7 the turn ended first"],
    });
  });
});
