// Unit tests for send-state.ts — verifies the SendState computed derives the send-button state
// reactively from the SSE-status / agent-down signals plus the reactive store (activeSession),
// pushing to prompt-input via a single effect with NO manual recompute call.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { SendState } from "./prompt-input.js";
import { setSendState } from "./prompt-input.js";
import { setSSEStatus, setAgentDown, clearAgentDown, reportSendRefused } from "./send-state.js";
import { setSessions, setActive, applyRun, recordSteerQueued } from "./store.js";
import type { Session } from "./types.js";
import { compactionEntry, runAt, turnEntry } from "./__test-helpers__/run-order.js";

// Capture every SendState pushed to prompt-input without loading its DOM deps.
vi.mock("./prompt-input.js", () => ({ setSendState: vi.fn() }));

const pushed = vi.mocked(setSendState);

function lastPushed(): SendState | undefined {
  return pushed.mock.calls.at(-1)?.[0];
}

let version = 0;

/** The server's run order now names a running turn that Cancel stops, or (`false`) nothing. */
function turnRuns(id: string, runs: boolean): void {
  version += 1;
  applyRun(
    id,
    runs ? runAt(version, [turnEntry("t1")], { cancel: "t1", send: "steer" }) : runAt(version, []),
  );
}

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

beforeEach(() => {
  // Reset the shared module-level reactive graph to a known idle baseline.
  setActive("");
  setSessions([]);
  clearAgentDown();
  setSSEStatus("connecting");
  pushed.mockClear();
});

describe("send-state precedence", () => {
  it("orders disconnected > agentDown > streaming > idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    // Make every lower-priority condition true at once.
    turnRuns(id, true);
    setAgentDown("The agent could not be started for this chat.");
    setSSEStatus("disconnected");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });

    // Drop disconnected via "connecting" (not "connected", which would clear
    setSSEStatus("connecting");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "The agent could not be started for this chat.",
    });

    clearAgentDown();
    expect(lastPushed()).toEqual({ kind: "streaming" });

    turnRuns(id, false);
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  // Both reachability rungs report `error`, and neither locks the composer: the next Send respawns
  // the bridge, and a dropped SSE stream says nothing about the command POST.
  it("reports both unreachable states as the advisory error state", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");

    setAgentDown("The agent could not be started for this chat.");
    expect(lastPushed()?.kind).toBe("error");

    clearAgentDown();
    setSSEStatus("disconnected");
    expect(lastPushed()?.kind).toBe("error");
  });

  // A failed ATTEMPT must leave the button alone. Nothing on the prompt-failure path calls setAgentDown, so a throttled turn on a
  // connected client settles back to idle and the send icon stays a send icon.
  it("leaves the button idle when a turn fails on a reachable agent", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    turnRuns(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    // The failed turn's close leaves the list idle; the prose is reported somewhere that is not this module.
    turnRuns(id, false);
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  // Outstanding steers must NOT reach the button. They are server state about what the agent has
  // read, shown on the chip row; putting them here would give the one control two jobs, and during
  // a turn it would stop offering Cancel.
  it("ignores pending steers entirely", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    recordSteerQueued(id, { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued(id, { id: "steer-2", text: "two", origin: "user" });
    expect(lastPushed()).toEqual({ kind: "idle" });

    turnRuns(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

// Cancel names what the server would stop (`RunView.cancel`): a compaction alone is never stopped by it.
describe("send-state from the run order", () => {
  it("shows Cancel exactly while the server names something to stop", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    expect(lastPushed()).toEqual({ kind: "idle" });

    version += 1;
    applyRun(
      id,
      runAt(version, [compactionEntry("compact-1", { after: "running" })], { send: "queue" }),
    );
    expect(lastPushed()).toEqual({ kind: "idle" });

    turnRuns(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    turnRuns(id, false);
    expect(lastPushed()).toEqual({ kind: "idle" });
  });
});

describe("send-state auto-tracking", () => {
  it("a run order change flips the pushed SendState with no explicit recompute", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    expect(lastPushed()).toEqual({ kind: "idle" });
    pushed.mockClear();

    // Only a store mutation + flush — the computed re-derives on its own.
    turnRuns(id, true);
    expect(pushed).toHaveBeenCalledTimes(1);
    expect(lastPushed()).toEqual({ kind: "streaming" });
  });
});

// The third face state: a refused send (409 reason:"starting"). submit.ts owns the copy; this
// module only renders it, on the same rung and with the same lifecycle as the unreachable-agent
// state — the next attempt clears it via clearAgentDown, and a chat switch drops it because the
// refusal was one chat's.
describe("send-state refused-send face", () => {
  it("renders the caller's copy through the error surface", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");

    reportSendRefused("The chat is busy right now. Send again to retry");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "The chat is busy right now. Send again to retry",
    });
  });

  it("outranks streaming, exactly like the agent-down rung", () => {
    // The refusal face must win over a running turn, or the reader never learns their send was refused.
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    turnRuns(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    reportSendRefused("The chat is busy right now. Send again to retry");
    expect(lastPushed()?.kind).toBe("error");
  });

  it("clears on the next attempt (clearAgentDown), settling back to idle", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    reportSendRefused("The chat is busy right now. Send again to retry");

    // What submitPrompt does at the top of every attempt.
    clearAgentDown();
    expect(lastPushed()).toEqual({ kind: "idle" });
  });

  it("does not survive a chat switch — the refusal belonged to one chat", () => {
    setSessions([makeSession("c1"), makeSession("c2")]);
    setActive("c1");
    setSSEStatus("connected");
    reportSendRefused("The chat is busy right now. Send again to retry");
    expect(lastPushed()?.kind).toBe("error");

    setActive("c2");
    expect(lastPushed()).toEqual({ kind: "idle" });
  });
});

describe("send-state disconnected override", () => {
  it("setSSEStatus('disconnected') wins over a live turn", () => {
    const id = "c1";
    setSessions([makeSession(id)]);
    setActive(id);
    setSSEStatus("connected");
    turnRuns(id, true);
    expect(lastPushed()).toEqual({ kind: "streaming" });

    setSSEStatus("disconnected");
    expect(lastPushed()).toEqual({
      kind: "error",
      reason: "Disconnected from the server. Reconnecting…",
    });
  });
});
