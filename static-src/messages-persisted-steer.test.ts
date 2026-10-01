// ---------------------------------------------------------------------------
// A PERSISTED steer renders at the `seq` where it landed, through the one primitive a
// live one renders through.
//
// A steer is a `steer` ENTRY in its turn's body, so there is no live-versus-persisted pair
// to keep consistent and no promotion: the note IS the entry at its own position. What this
// suite defends is the note's VOCABULARY over recorded facts — whose words these are, whether
// the agent read them, why a drop went unread, and whether it acknowledged them — because a
// note that states the wrong one makes a false claim about the reader's own message.
//
// REAL store, REAL renderer, REAL layout (Browser Mode). DOM hosts before the imports, the
// shipped transcript stylesheet, `messages.teardownAll()` per case.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";
import type { Entry, Session, TurnState } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element, so every host exists before an import resolves.
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

/** The persisted shape a landed steer takes. `state` is REQUIRED on the wire, so there is no
 *  unknown-state population to render neutrally — see the dropped oracle at the tail. */
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

/** Mount `entries` as `chatID`'s whole transcript — one turn, the shape a page GET lands —
 *  and paint. The window is announced as a REPLAY, which is what a fetched page carries. */
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
  // The multiplexer's registry persists at module scope, so an earlier case's
  // parked view would otherwise count against this one's LRU budget.
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
    // The control for the acknowledged case below: without it a note marked acknowledged
    // unconditionally satisfies that case and nothing here notices.
    expect(note?.dataset["acknowledged"]).toBeUndefined();
    // The note holds the reader's words and carries no control in either state: the
    // acknowledgement is its own entry and a drop's resend is its own turn.
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

  // The ORACLE THE ENTRY MODEL RESTORED, and the reason it is worth its own case: under the
  // message model the persisted note lost the acknowledgement (only the live mark carried
  // one), so a reload turned an acknowledged correction into a bare one. The ack is its own
  // entry now, so the note reads it out of the turn's own body.
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

// The half that matters most, and the reason the state is on the entry at all: a correction
// the agent NEVER READ. Rendering it identically to a delivered one is a false claim about
// whether the reader's own message landed, which is worse than the note being absent.
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

  // WHY it went unread is a recorded fact and the label states it. A value the note has no
  // wording for adds no clause, which is what keeps upstream text off this surface.
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
