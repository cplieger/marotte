// A second rewind to the same turn appends a second `turn_revert` to the same surviving turn; the transcript shows
// one "Rewound to here" row, the newest's.
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

function revert(at: number, from: string, n: number): Entry {
  return sealed(
    "t1",
    at,
    "turn_revert",
    { from, from_n: n, through: from, cause: "rewind" },
    `${from}:revert`,
  );
}

const T1: Entry[] = [
  sealed("t1", 0, "turn_open", { prompt: { id: "t1-p", text: "go" }, source: "prompt", n: 1 }),
  sealed("t1", 1, "text", { text: "done" }),
  sealed("t1", 2, "turn_close", { outcome: "completed" }),
];

async function paint(chatID: string, entries: Entry[]): Promise<HTMLElement> {
  const turns = new Map<string, TurnState>([["t1", { entries, openEntries: new Map() }]]);
  const s: Session = { ...makeSession({ id: chatID, name: chatID }), turns, turn_order: ["t1"] };
  s.turn_count = 1;
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

function banners(view: HTMLElement): HTMLElement[] {
  return [...view.querySelectorAll<HTMLElement>(".boundary-rewound")];
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe("rewinding to the same turn twice", () => {
  it("draws only the newest record's row when the turn already holds two", async () => {
    const view = await paint("c-rw-cold", [...T1, revert(3, "t2", 2), revert(4, "t3", 2)]);
    expect(banners(view).map((b) => b.dataset["entryId"])).toEqual(["t3:revert"]);
  });

  it("replaces the drawn row when the second record arrives", async () => {
    const view = await paint("c-rw-live", [...T1, revert(3, "t2", 2)]);
    expect(banners(view).map((b) => b.dataset["entryId"])).toEqual(["t2:revert"]);

    store.appendEntry("c-rw-live", revert(4, "t3", 2));
    await Promise.resolve();

    expect(banners(view).map((b) => b.dataset["entryId"])).toEqual(["t3:revert"]);
    expect(banners(view)[0]?.textContent).toContain("Rewound to here");
  });
});
