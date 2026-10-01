// `clearTurnState`, against the REAL store: the outcome-independent half of "a turn is no
// longer running", reached by three doors that each apply whatever else they know. Only the
// two surfaces it reaches OUT to are replaced, because they carry the two facts the store
// cannot: which TAB a chat's subject is open in, and whether a question is waiting.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const mocks = vi.hoisted(() => ({
  setTabStatus: vi.fn(),
  tabIdFor: vi.fn((kind: string, ref: string) => `tab-${kind}-${ref}`),
  hasPendingDecision: vi.fn(() => false),
}));

vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  setTabStatus: mocks.setTabStatus,
  tabIdFor: mocks.tabIdFor,
}));
vi.mock("./decision-dock.js", () => ({ hasPendingDecision: mocks.hasPendingDecision }));

import { clearTurnState } from "./turn-teardown.js";
import {
  get,
  defaultUsage,
  openTurn,
  appendEntry,
  removeChat,
  setSessions,
  setThinking,
} from "./store.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

const CHAT = "c-teardown";
const TURN = "turn-1";

function session(): Session {
  return {
    id: CHAT,
    name: "teardown",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function entry(seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${TURN}-e${String(seq)}`, turn: TURN, kind, seq, ts: seq + 1, payload };
}

/** A chat whose newest turn is CLOSED in the log while this client still believes a turn is
 *  running: the state every door is reached in. Liveness is the log, so a turn with its
 *  `turn_close` is what lets the painted dot state anything but `working`. */
function settledButThinking(outcome = "failed"): void {
  setSessions([{ ...session(), last_turn_outcome: "failed" }]);
  openTurn(CHAT, entry(0, "turn_open", { source: "prompt", n: 1 }));
  appendEntry(CHAT, entry(1, "turn_close", { outcome }));
  setThinking(CHAT, true);
}

beforeEach(() => {
  mocks.hasPendingDecision.mockReturnValue(false);
  mocks.tabIdFor.mockImplementation((kind: string, ref: string) => `tab-${kind}-${ref}`);
});

afterEach(() => {
  removeChat(CHAT);
});

describe("clearTurnState", () => {
  it("brings this client's own belief that a turn is running to rest", () => {
    settledButThinking();

    clearTurnState(CHAT);

    expect(get(CHAT)?.thinking).toBe(false);
  });

  it("paints the strip through the TAB the subject names, never the chat id", () => {
    settledButThinking();

    clearTurnState(CHAT);

    // `setTabStatus` takes the opaque server-minted tab id, so the subject is resolved
    // rather than passed through: a chat id reaching that seam names no tab at all.
    expect(mocks.tabIdFor).toHaveBeenCalledWith("chat", CHAT);
    expect(mocks.setTabStatus).toHaveBeenCalledWith(`tab-chat-${CHAT}`, "failed");
  });

  it("hands the dock its own answer, so a waiting question outranks the settled verdict", () => {
    settledButThinking();
    mocks.hasPendingDecision.mockReturnValue(true);

    clearTurnState(CHAT);

    // The ask is the one input that lives outside the store, so a teardown grading the log
    // alone would repaint a chat green over a question nobody has answered.
    expect(mocks.setTabStatus).toHaveBeenCalledWith(`tab-chat-${CHAT}`, "input");
  });

  it("mints no row for a chat the store does not hold", () => {
    setSessions([]);

    expect(() => {
      clearTurnState("c-absent");
    }).not.toThrow();

    expect(get("c-absent"), "no row was minted").toBeUndefined();
    // The empty verdict is what `tabStatusFor` answers for a chat it cannot see, and the
    // strip's own writer refuses it, so this door needs no guard agreeing with that rule.
    expect(mocks.setTabStatus).toHaveBeenCalledWith("tab-chat-c-absent", "");
  });
});
