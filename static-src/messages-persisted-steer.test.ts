// A persisted steer is a `steer` entry rendered at its `seq` through the live primitive. This suite defends the
// note's vocabulary over recorded facts (whose words, read or not, why a drop went unread, acknowledged or not),
// since a wrong one makes a false claim about the reader's own message.

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
const { steerAckID } = await import("./entry-ids.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

/** A sealed entry at `at`. The transcript's own lane is `""`, which an absent one is. */
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

/** The persisted shape a landed steer takes. `state` is required on the wire. */
function steer(
  turnID: string,
  at: number,
  text: string,
  over: Record<string, unknown> = {},
): Entry {
  return sealed(
    turnID,
    at,
    "steer",
    { text, origin: "user", state: "read", ...over },
    `steer-${turnID}-${String(at)}`,
  );
}

/** KAS's acknowledgement of a steer, paired to it by the id the appender mints. */
function steerAck(turnID: string, at: number, steerEntryID: string): Entry {
  return sealed(
    turnID,
    at,
    "steer_ack",
    { steer_id: steerEntryID, text: "" },
    steerAckID(steerEntryID),
  );
}

function reply(turnID: string, at: number, text: string): Entry {
  return sealed(turnID, at, "text", { text });
}

function turnClose(turnID: string, at: number): Entry {
  return sealed(turnID, at, "turn_close", { outcome: "completed" });
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** Mount `entries` as `chatID`'s whole transcript (one turn, announced as a replay) and paint. */
async function paint(chatID: string, turnID: string, entries: Entry[]): Promise<HTMLElement> {
  const turns = new Map<string, TurnState>([[turnID, { entries, openEntries: new Map() }]]);
  store.setSessions([session(chatID, { turns, turn_order: [turnID], turn_count: 1 })]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
  await flushed();
  return viewOf(chatID);
}

function notes(view: HTMLElement): HTMLElement[] {
  return [...view.querySelectorAll<HTMLElement>(".steer-note")];
}

function labelOf(note: HTMLElement | undefined): string | null | undefined {
  return note?.querySelector(".steer-note-label")?.textContent;
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope; an earlier case's parked view would count against the LRU.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe("a persisted steer the agent read", () => {
  it("mounts .steer-note with data-origin=user, data-state=read and no control", async () => {
    const c = freshID("c-steer");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "use tabs"),
      reply(t, 2, "done"),
      turnClose(t, 3),
    ]);

    const found = notes(view);
    expect(found).toHaveLength(1);
    const note = found[0];
    expect(note?.dataset["origin"]).toBe("user");
    expect(note?.dataset["state"]).toBe("read");
    expect(labelOf(note)).toBe("Mid-turn message");
    expect(note?.querySelector(".steer-note-text")?.textContent).toBe("use tabs");
    // The control for the acknowledged case below, which an unconditional mark would otherwise satisfy.
    expect(note?.dataset["acknowledged"]).toBeUndefined();
    // The note carries no control: the acknowledgement is its own entry and a drop's resend is its own turn.
    expect(note?.querySelector(".steer-note-restore")).toBeNull();
  });

  it("renders the note rather than a grey system row", async () => {
    const c = freshID("c-steer-not-system");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "use tabs"),
      reply(t, 2, "done"),
      turnClose(t, 3),
    ]);

    expect(notes(view)).toHaveLength(1);
    expect(view.querySelector(".message.system")).toBeNull();
  });

  it("carries the origin, so a workflow's report is not titled as the reader's words", async () => {
    const c = freshID("c-steer-origin");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "A workflow you launched completed.", { origin: "agent" }),
      reply(t, 2, "done"),
      turnClose(t, 3),
    ]);

    const note = notes(view)[0];
    expect(note?.dataset["origin"]).toBe("agent");
    expect(labelOf(note)).toBe("Workflow result");
  });

  // The ack is its own entry, so a reload must read it out of the turn's body rather than lose it.
  it("marks the note acknowledged when the turn holds its steer_ack", async () => {
    const c = freshID("c-steer-ack");
    const t = `${c}-t1`;
    const s = steer(t, 1, "use tabs");
    const view = await paint(c, t, [
      turnOpen(t),
      s,
      steerAck(t, 2, s.id),
      reply(t, 3, "done"),
      turnClose(t, 4),
    ]);

    const note = notes(view)[0];
    expect(note?.dataset["acknowledged"]).toBe("true");
    expect(labelOf(note)).toBe("Mid-turn message · acknowledged");
  });
});

// A correction the agent never read must not render like a delivered one: that is a false claim about the
// reader's message.
describe("a persisted UNDELIVERED steer says so", () => {
  it("mounts data-state=dropped with the not-read label", async () => {
    const c = freshID("c-steer-dropped");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "actually target main", { state: "dropped" }),
      reply(t, 2, "done"),
      turnClose(t, 3),
    ]);

    const note = notes(view)[0];
    expect(note?.dataset["state"]).toBe("dropped");
    expect(note?.dataset["origin"]).toBe("user");
    expect(labelOf(note)).toBe("Not read");
    expect(note?.querySelector(".steer-note-text")?.textContent).toBe("actually target main");
  });

  // A drop value the note has no wording for adds no clause, which keeps upstream text off this surface.
  it("states a recorded drop reason and ignores one it has no wording for", async () => {
    const c = freshID("c-steer-reason");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "restarted under me", { state: "dropped", reason: "restart" }),
      steer(t, 2, "unknown reason", { state: "dropped", reason: "who knows" }),
      reply(t, 3, "done"),
      turnClose(t, 4),
    ]);

    const labels = notes(view).map((n) => labelOf(n));
    expect(labels).toEqual(["Not read · the session restarted", "Not read"]);
  });

  it("reads the two states apart from one transcript", async () => {
    const c = freshID("c-steer-both");
    const t = `${c}-t1`;
    const view = await paint(c, t, [
      turnOpen(t),
      steer(t, 1, "use tabs"),
      steer(t, 2, "actually target main", { state: "dropped" }),
      reply(t, 3, "done"),
      turnClose(t, 4),
    ]);

    expect(notes(view).map((n) => n.dataset["state"])).toEqual(["read", "dropped"]);
  });
});
