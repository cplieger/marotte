// The resume control counts entries the reader can reach, through the renderer's own `entryRenders`. Two populations
// answer false: another lane's entries, and kinds with no position of their own (`turn_open`, `turn_close`,
// `tool_result`, `turn_bind`). Flows run production's order: park, then entries land via `openTurn`/`appendEntry`.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import type { ReadingState } from "./scroll.js";

// The DOM registry throws on a missing element, so the hosts exist before any import resolves.
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

// The shared scroll mock, not a copy: an inline factory missing `scrollableBy` fails only in a full-suite run, with
// no file named.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
import { scrollMock } from "./__test-helpers__/scroll-mock.js";

const store = await import("./store.js");
const messages = await import("./messages.js");

/** Captured once: `mountChatView` is idempotent. */
let onReading: (s: ReadingState) => void;

messages.mountChatView();
{
  const call = scrollMock.onReadingStateChange.mock.calls[0];
  if (call === undefined) {
    throw new Error("mountChatView registered no reading-state listener");
  }
  onReading = call[0] as (s: ReadingState) => void;
}

function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  lane?: string,
): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    ...(lane === undefined ? {} : { lane }),
    payload,
  } as Entry;
}

/** Optionally in a delegate's lane; lane alone tells a delegate's entries apart in the shared `seq` space. */
function text(turnID: string, at: number, body: string, lane?: string): Entry {
  return sealed(turnID, at, "text", { text: body }, lane);
}

/** Minted per flow, so every mount is a fresh chat switch. */
let chatSeq = 0;

function session(id: string): Session {
  return makeSession({ id, name: id });
}

/** Park on an empty chat, then land one turn through the store's own operations, so each append recounts. */
function parkThenLand(body: (turnID: string) => Entry[]): string {
  const chat = `c-${String(++chatSeq)}`;
  const turnID = `t-${String(chatSeq)}`;
  store.setSessions([session(chat)]);
  store.setActive(chat);
  scrollMock.readingState.mockReturnValue("reading");
  onReading("reading"); // Baseline: nothing reachable yet.
  store.openTurn(
    chat,
    sealed(turnID, 0, "turn_open", {
      prompt: { id: `${turnID}-p`, text: "go" },
      source: "prompt",
      n: 1,
    }),
  );
  for (const e of body(turnID)) {
    store.appendEntry(chat, e);
  }
  return chat;
}

/** The label after the last full pass. */
function lastLabel(): string {
  const call = scrollMock.setResumeLabel.mock.calls.at(-1);
  return call === undefined ? "<no label>" : (call[0] as string);
}

beforeEach(() => {
  scrollMock.setResumeLabel.mockClear();
});

describe("the resume label counts only entries the reader can reach", () => {
  it("does NOT count a delegate's entries", () => {
    parkThenLand((t) => [
      text(t, 1, "parent one"),
      text(t, 2, "parent two"),
      text(t, 3, "delegate a", "sa-1"),
      text(t, 4, "delegate b", "sa-1"),
      text(t, 5, "delegate c", "sa-1"),
    ]);
    // The view draws no entry outside its root lane, so the distance is the two parent entries, not five.
    expect(lastLabel()).toBe("2 new blocks");
  });

  it("counts parent-lane entries whatever else the turn carries", () => {
    parkThenLand((t) => [text(t, 1, "only parent"), text(t, 2, "delegate", "sa-1")]);
    // The singular, a different branch of the label.
    expect(lastLabel()).toBe("1 new block");
  });

  // A settled tool call is one reachable entry: its `tool_result` folds onto the card, and `turn_open`/`turn_close` are
  // not counted.
  it("counts a settled tool call once, not once per entry", () => {
    parkThenLand((t) => [
      text(t, 1, "prose"),
      sealed(t, 2, "tool_call", {
        id: `${t}-tc`,
        title: "Run Command",
        kind: "execute",
        status: "completed",
        ts: 2,
      }),
      sealed(t, 3, "tool_result", { id: `${t}-tc:result`, status: "completed", output: "ok" }),
      sealed(t, 4, "turn_close", { outcome: "completed" }),
    ]);
    expect(lastLabel()).toBe("2 new blocks");
  });
});
