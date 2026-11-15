// A model fallback that moves the chat's model draws exactly one line in its turn: in the open body while the turn
// runs, and on the folded face once a later turn folds it, so a reload still shows it. Fixture: chat
// c-f4e9bfb65d845891b4a62f4a9f9d8909 turn #431, KAS's re-pin (seq 33) then its fallback notice (seq 34).

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import type * as Scroll from "./scroll.js";

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

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");

messages.mountChatView();

const TURN = "t-7sy6zlbd5j6hgwsq5c4zqwuwx4";
const NEXT = "t-next";

function sealed(
  turn: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id = `${turn}:e${String(seq)}`,
): Entry {
  return { id, turn, kind, seq, ts: seq + 1, payload };
}

function turnOpen(turn: string, n: number, text: string): Entry {
  return sealed(
    turn,
    0,
    "turn_open",
    { prompt: { id: `${turn}-p`, text }, source: "prompt", n },
    turn,
  );
}

/** Turn #431's shape: prose, the user's steer, the recorded pair, the retry's prose, a tool, the answer. */
function turn431(fallback: Record<string, unknown>, repin = true): Entry[] {
  const body: Entry[] = [
    turnOpen(TURN, 431, "check your grelot inbox"),
    sealed(TURN, 1, "text", { text: "Reading the inbox." }, "a-say"),
    sealed(
      TURN,
      2,
      "steer",
      { text: "please use chat extract", origin: "user", state: "read" },
      "steer-1",
    ),
  ];
  if (repin) {
    body.push(
      sealed(TURN, 3, "model_switched", {
        from: "claude-opus-5.5",
        to: "claude-opus-4.8",
        reason: "unavailable",
      }),
    );
  }
  body.push(
    sealed(TURN, 4, "model_fallback", {
      from: "claude-opus-5.5",
      to: "claude-opus-4.8",
      ...fallback,
    }),
    sealed(TURN, 5, "text", { text: "I'll re-extract this chat." }, "b-say"),
    sealed(
      TURN,
      6,
      "tool_call",
      { id: "c1", title: "Run Command", kind: "execute", status: "completed", ts: 1 },
      "c1",
    ),
    sealed(TURN, 7, "tool_result", { status: "completed" }, "c1:result"),
    sealed(TURN, 8, "text", { text: "The answer." }, "c-say"),
    sealed(TURN, 9, "turn_close", { outcome: "completed" }),
  );
  return body;
}

const SESSION_REFUSAL = { scope: "session", trigger: "refusal" };

function nextTurn(): Entry[] {
  return [
    turnOpen(NEXT, 432, "next"),
    sealed(NEXT, 1, "text", { text: "ok" }, "next-say"),
    sealed(NEXT, 2, "turn_close", { outcome: "completed" }),
  ];
}

function load(chatID: string, turns: readonly (readonly Entry[])[]): void {
  const s: Session = makeSession({ id: chatID, name: chatID });
  for (const entries of turns) {
    const first = entries[0];
    if (first !== undefined) {
      s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
      s.turn_order.push(first.turn);
    }
  }
  s.turn_count = s.turn_order.length;
  store.setSessions([s]);
  store.setActive(chatID);
  store.setPainted(chatID);
  store.bumpMessages(chatID, "load");
}

function card(turnID: string): HTMLElement {
  const root = messages.activeTranscriptView() ?? document.getElementById("messages")!;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

/** The switch lines `turnID`'s card shows: the body's when it is open, the face's when it is folded. */
function lines(turnID: string): string[] {
  const c = card(turnID);
  const scope = c.hasAttribute("data-folded") ? ".turn-face" : ".turn-body";
  return [
    ...c.querySelectorAll<HTMLElement>(`:scope > ${scope} .boundary-switched .boundary-label`),
  ].map((b) => b.textContent ?? "");
}

const SESSION_LINE =
  "Switched to claude-opus-4.8: claude-opus-5.5 declined the request \u00b7 this chat now stays on claude-opus-4.8";

beforeEach(() => {
  messages.teardownAll();
  localStorage.clear();
  resetFoldState();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

describe("a model fallback's line", () => {
  it("draws once in the open turn while it is the newest", async () => {
    load("fb-live", [turn431(SESSION_REFUSAL)]);
    await vi.waitFor(() => {
      expect(lines(TURN)).toEqual([SESSION_LINE]);
    });
    expect(card(TURN).hasAttribute("data-folded")).toBe(false);
  });

  it("draws once while the turn streams, the re-pin and the notice arriving apart", async () => {
    const chat = "fb-stream";
    store.setSessions([makeSession({ id: chat, name: chat, thinking: true })]);
    store.setActive(chat);
    store.setPainted(chat);
    const [open, ...rest] = turn431(SESSION_REFUSAL);
    store.openTurn(chat, open!);
    for (const e of rest.slice(0, 3)) {
      store.appendEntry(chat, e);
    }
    await vi.waitFor(() => {
      expect(lines(TURN)).toHaveLength(1);
    });
    for (const e of rest.slice(3, -1)) {
      store.appendEntry(chat, e);
    }
    await vi.waitFor(() => {
      expect(lines(TURN)).toEqual([SESSION_LINE]);
    });
  });

  it("draws once on the folded face after a reload, before the retry's answer", async () => {
    load("fb-reload", [turn431(SESSION_REFUSAL), nextTurn()]);
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
      expect(lines(TURN)).toEqual([SESSION_LINE]);
    });
    const face = card(TURN).querySelector(":scope > .turn-face")!;
    expect(face.firstElementChild?.classList.contains("boundary-switched")).toBe(true);
    expect(face.textContent).toContain("The answer.");
  });

  it("draws once in the body when the reader opens the folded turn", async () => {
    setTurnOpen("fb-opened", TURN, true);
    load("fb-opened", [turn431(SESSION_REFUSAL), nextTurn()]);
    await vi.waitFor(() => {
      expect(lines(TURN)).toEqual([SESSION_LINE]);
    });
  });

  it("says a turn-scope fallback applies to this turn only", async () => {
    load("fb-turn", [turn431({ scope: "turn", trigger: "availability" }, false), nextTurn()]);
    await vi.waitFor(() => {
      expect(lines(TURN)).toEqual([
        "Switched to claude-opus-4.8: claude-opus-5.5 was unavailable \u00b7 for this turn only",
      ]);
    });
  });

  it("stays hidden for a sub-agent's fallback, open or folded", async () => {
    load("fb-sub", [turn431({ ...SESSION_REFUSAL, subagent: true }, false), nextTurn()]);
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(true);
    });
    expect(lines(TURN)).toEqual([]);
    setTurnOpen("fb-sub", TURN, true);
    store.bumpMessages("fb-sub", "load");
    await vi.waitFor(() => {
      expect(card(TURN).hasAttribute("data-folded")).toBe(false);
    });
    expect(lines(TURN)).toEqual([]);
  });
});
