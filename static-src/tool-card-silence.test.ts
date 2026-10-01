// The tool card's SILENCE MARKER: when it renders, what it says, and when it leaves.
//
// Its companion is `tool-silence.test.ts`, which owns the value's own two properties
// (Date.now()-derived, reads no entry `ts`). This file owns the card: the marker is
// an in-flight card's alone, it clears on the next PROGRESS frame, and a status
// change with no progress behind it leaves it standing.
import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";

import { clearToolSilence, noteToolActivity, silenceMsFor } from "./tool-silence.js";
import { buildToolCard, syncSilenceMarker } from "./tool-card.js";
import {
  setSessions,
  setActive,
  openTurn,
  appendEntry,
  applyToolProgress,
  republishWindowToolCalls,
} from "./store.js";
import { toolCallSigs } from "./store-signals.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import type * as ScrollModule from "./scroll.js";
import type * as OpenersModule from "./editor-openers.js";

// Mock scroll.ts for its eager `$.messages` read at module level, and
// editor-openers.ts for its transitive DOM, exactly as `tool-card.test.ts` does:
// this file's subject is one span in a card's header, not the transcript around it.
// The spread below evaluates the real scroll.ts, whose load builds its controller
// over these ids, so they exist before any import of this file is linked.
vi.hoisted(() => {
  for (const [tag, id] of [
    ["div", "messages"],
    ["div", "messages-wrap"],
    ["button", "scroll-bottom"],
  ] as const) {
    if (document.getElementById(id) === null) {
      const host = document.createElement(tag);
      host.id = id;
      document.body.appendChild(host);
    }
  }
});
vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ScrollModule>()),
  ...(await import("./__test-helpers__/scroll-mock.js")).scrollMock,
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof OpenersModule>()),
  openFile: vi.fn(),
  openFileDiff: vi.fn(),
  openFileGitDiff: vi.fn(),
}));

// The ids the remaining transitive imports read at module level.
beforeAll(() => {
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (document.getElementById(id) === null) {
      const host = document.createElement("div");
      host.id = id;
      document.body.appendChild(host);
    }
  }
});

const CHAT = "c-silence";
const TURN = "t-1";
const CALL = "tc-1";

function turnOpen(): Entry {
  return {
    id: `${TURN}-open`,
    turn: TURN,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: "m-1", text: "hi" } },
  };
}

function call(over: Partial<EntryToolCall> = {}): EntryToolCall {
  return Object.assign(
    { id: CALL, title: "Run Command", status: "in_progress", kind: "execute", ts: 1 },
    over,
  ) as EntryToolCall;
}

/** A chat holding one open turn with one in-flight tool call. */
function seedCall(tc: EntryToolCall = call()): void {
  setSessions([makeSession({ id: CHAT })]);
  setActive(CHAT);
  openTurn(CHAT, turnOpen());
  appendEntry(CHAT, { id: CALL, turn: TURN, kind: "tool_call", seq: 1, ts: 2, payload: tc });
}

/** A live, in-flight card, which is the only card that may carry a marker. */
function liveCard(): HTMLElement {
  return buildToolCard({
    id: CALL,
    title: "Run Command",
    kind: "execute",
    status: "in_progress",
    live: true,
  });
}

beforeEach(() => {
  clearToolSilence();
  toolCallSigs.clearAll();
});

afterEach(() => {
  clearToolSilence();
});

describe("the card's marker", () => {
  it("renders past the threshold and states the silence", () => {
    const card = liveCard();
    noteToolActivity(CHAT, CALL, Date.now() - 3 * 60_000);
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")?.textContent).toBe("no output for 3 minutes");
  });

  it("renders nothing under the threshold", () => {
    const card = liveCard();
    noteToolActivity(CHAT, CALL, Date.now() - 30_000);
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).toBeNull();
  });

  it("renders nothing for a call this client has applied no frame for", () => {
    const card = liveCard();
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).toBeNull();
  });

  it("clears on the next progress frame", () => {
    seedCall();
    const card = liveCard();
    noteToolActivity(CHAT, CALL, Date.now() - 4 * 60_000);
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).not.toBeNull();

    applyToolProgress(CHAT, TURN, { turn: TURN, tool_call_id: CALL, output_delta: "spoke" });
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).toBeNull();
  });

  it("does NOT clear on a status change alone", () => {
    seedCall(call({ status: "pending" }));
    const card = liveCard();
    const quiet = Date.now() - 4 * 60_000;
    noteToolActivity(CHAT, CALL, quiet);
    syncSilenceMarker(card, CHAT, CALL);
    const before = card.querySelector(".tool-silence")?.textContent;
    expect(before).toBe("no output for 4 minutes");

    // A republish of the same call with a different status: no `tool_progress` was
    // applied, so nothing said the call spoke and the marker stands.
    republishWindowToolCalls(CHAT, [TURN]);
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")?.textContent).toBe(before);
    expect(silenceMsFor(CHAT, CALL, quiet + 4 * 60_000)).toBe(4 * 60_000);
  });

  it("leaves when the card settles, because a settled card is not silent", () => {
    const card = liveCard();
    noteToolActivity(CHAT, CALL, Date.now() - 3 * 60_000);
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).not.toBeNull();

    // `data-start-ms` MEANS the card is in flight and is dropped on every settle.
    delete card.dataset["startMs"];
    syncSilenceMarker(card, CHAT, CALL);
    expect(card.querySelector(".tool-silence")).toBeNull();
  });
});
