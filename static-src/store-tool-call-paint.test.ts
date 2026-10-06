// WHAT MOVES A MOUNTED TOOL CARD, and what a write that changes nothing must not move.
import { describe, it, expect, beforeEach } from "vitest";
import {
  setSessions,
  setActive,
  defaultUsage,
  openTurn,
  appendEntry,
  applyToolProgress,
  foldToolCallDelta,
  settledToolCall,
  republishWindowToolCalls,
  renderCauseOf,
  setWorkingLabel,
} from "./store.js";
import { ensureToolCallSig, peekToolCallSig, toolCallSigs } from "./store-signals.js";
import type { Session, ToolCall, ToolProgressPayload } from "./types.js";
import type { Entry, EntryToolCall, EntryToolResult } from "./wire/types.gen.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
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

function resetStore(chatID: string): void {
  setSessions([makeSession(chatID)]);
  setActive(chatID);
}

function turnOpenEntry(turnID: string, n: number): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "wire_turn_start", n },
  };
}

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown, id: string) {
  return { id, turn: turnID, kind, seq, ts: seq + 1, payload } satisfies Entry;
}

// `Object.assign` rather than a spread: under `exactOptionalPropertyTypes` a spread of a `Partial`
// widens every required field to include `undefined`, which the target refuses.
function toolCall(id: string, over: Partial<EntryToolCall> = {}): EntryToolCall {
  const base: EntryToolCall = {
    id,
    title: "Run Command",
    status: "in_progress",
    kind: "execute",
    ts: 1,
  };
  return Object.assign(base, over);
}

function toolResult(over: Partial<EntryToolResult> = {}): EntryToolResult {
  return Object.assign({ status: "completed" } as EntryToolResult, over);
}

function openTurnIn(chatID: string, turnID: string, n = 1): string {
  openTurn(chatID, turnOpenEntry(turnID, n));
  return turnID;
}

/** A chat holding one `tool_call` entry, with a card MOUNTED against `shown`. Returns the turn
 *  id, so a case reads as one line of arrange. */
function mountedCall(chatID: string, entry: EntryToolCall, shown: ToolCall): string {
  resetStore(chatID);
  const turn = openTurnIn(chatID, "t1");
  appendEntry(chatID, sealed(turn, 1, "tool_call", entry, entry.id));
  ensureToolCallSig(chatID, entry.id, shown);
  return turn;
}

/** Did the fetched window reach the card? Identity, because that is what the card's own effect
 *  compares and therefore the only thing that decides whether it repaints. */
function republished(chatID: string, turn: string, callID: string): boolean {
  const before = peekToolCallSig(chatID, callID);
  republishWindowToolCalls(chatID, [turn]);
  return peekToolCallSig(chatID, callID) !== before;
}

beforeEach(() => {
  toolCallSigs.clearAll();
});

describe("republishWindowToolCalls leaves a card the fetch agrees with alone", () => {
  // Every field `applyToolCallUpdate` READS, each as its own case, because a guard that compares
  // six of seven is indistinguishable from one that compares all seven until the seventh moves.
  const fields: [string, Partial<EntryToolCall>][] = [
    ["the title", { title: "Edit File" }],
    ["the status", { status: "completed" }],
    ["the duration", { duration_ms: 42 }],
    ["the output", { output: "second" }],
    ["the terminal id", { terminal_id: "term-2" }],
  ];

  const shownBase: Partial<EntryToolCall> = {
    title: "Run Command",
    status: "in_progress",
    duration_ms: 7,
    output: "first",
    terminal_id: "term-1",
  };

  it("publishes nothing when every field it reads already matches", () => {
    const turn = mountedCall("a", toolCall("c1", shownBase), toolCall("c1", shownBase));
    expect(republished("a", turn, "c1")).toBe(false);
  });

  for (const [what, over] of fields) {
    it(`publishes when ${what} moved`, () => {
      const turn = mountedCall(
        "a",
        toolCall("c1", { ...shownBase, ...over }),
        toolCall("c1", shownBase),
      );
      expect(republished("a", turn, "c1")).toBe(true);
    });
  }

  it("publishes nothing for a field the card cannot paint from", () => {
    // `kind`, `locations`, `checkpoint` and the subtask and workflow ids are not read by the update
    // path, so a difference there is not a reason to repaint a card.
    const turn = mountedCall(
      "a",
      toolCall("c1", { ...shownBase, kind: "read", agent_subtask_id: "sub-9" }),
      toolCall("c1", shownBase),
    );
    expect(republished("a", turn, "c1")).toBe(false);
  });
});

describe("republishWindowToolCalls compares output spans element by element", () => {
  // The spans are what style the output, so a card showing last window's colours over this window's
  // text is the defect the per-field comparison exists to prevent. Both lists are freshly decoded
  // objects on the fetch path, so identity answers nothing here.
  const span = { start: 0, end: 4, fg: 1, bg: 2, attrs: 3 };
  const spanFields: [string, Partial<typeof span>][] = [
    ["start", { start: 1 }],
    ["end", { end: 5 }],
    ["fg", { fg: 9 }],
    ["bg", { bg: 9 }],
    ["attrs", { attrs: 9 }],
  ];

  it("publishes nothing for two equal span lists that are different objects", () => {
    const turn = mountedCall(
      "a",
      toolCall("c1", { output: "abcd", output_spans: [{ ...span }] }),
      toolCall("c1", { output: "abcd", output_spans: [{ ...span }] }),
    );
    expect(republished("a", turn, "c1")).toBe(false);
  });

  for (const [field, over] of spanFields) {
    it(`publishes when a span's ${field} moved`, () => {
      const turn = mountedCall(
        "a",
        toolCall("c1", { output: "abcd", output_spans: [{ ...span, ...over }] }),
        toolCall("c1", { output: "abcd", output_spans: [{ ...span }] }),
      );
      expect(republished("a", turn, "c1")).toBe(true);
    });
  }

  it("publishes when the fetch carries a different NUMBER of spans", () => {
    const turn = mountedCall(
      "a",
      toolCall("c1", { output: "abcd", output_spans: [{ ...span }, { ...span, start: 2 }] }),
      toolCall("c1", { output: "abcd", output_spans: [{ ...span }] }),
    );
    expect(republished("a", turn, "c1")).toBe(true);
  });
});

describe("republishWindowToolCalls compares diffs element by element", () => {
  const diff = { path: "main.go", old_text: "a", new_text: "b" };
  const diffFields: [string, Partial<typeof diff>][] = [
    ["path", { path: "other.go" }],
    ["old_text", { old_text: "z" }],
    ["new_text", { new_text: "z" }],
  ];

  it("publishes nothing for two equal diff lists that are different objects", () => {
    const turn = mountedCall(
      "a",
      toolCall("c1", { diffs: [{ ...diff }] }),
      toolCall("c1", { diffs: [{ ...diff }] }),
    );
    expect(republished("a", turn, "c1")).toBe(false);
  });

  for (const [field, over] of diffFields) {
    it(`publishes when a diff's ${field} moved`, () => {
      const turn = mountedCall(
        "a",
        toolCall("c1", { diffs: [{ ...diff, ...over }] }),
        toolCall("c1", { diffs: [{ ...diff }] }),
      );
      expect(republished("a", turn, "c1")).toBe(true);
    });
  }

  it("publishes when the fetch carries a different NUMBER of diffs", () => {
    const turn = mountedCall(
      "a",
      toolCall("c1", { diffs: [{ ...diff }, { ...diff, path: "two.go" }] }),
      toolCall("c1", { diffs: [{ ...diff }] }),
    );
    expect(republished("a", turn, "c1")).toBe(true);
  });
});

describe("settledToolCall carries every member the result sends", () => {
  // Each member is its own conditional spread, so a dropped one is a field that silently stops
  // reaching the card: no type error, no test failure anywhere else, and a tool row that has lost
  // its diff, its denial or its refusal mark.
  it("takes the whole result over the call", () => {
    const merged = settledToolCall(
      toolCall("c1", { title: "Run Command", kind: "execute" }),
      toolResult({
        title: "Edited main.go",
        kind: "edit",
        output: "done",
        output_spans: [{ start: 0, end: 4, fg: 1, bg: 2, attrs: 3 }],
        diffs: [{ path: "main.go", old_text: "a", new_text: "b" }],
        locations: [{ path: "main.go", line: 12 }],
        duration_ms: 42,
        terminal_id: "term-1",
        workflow_id: "wf-1",
        checkpoint: { original: "o", modified: "m", local: "l" },
        disclosed: { type: "steering", display_name: "steering.md", uri: "file:///steering.md" },
        denial: {
          capability: "shell",
          resource: "rm -rf /",
          scope: "user",
          source: "permissions.yaml",
          rule: { capability: "shell", effect: "deny" },
        },
        declined: true,
      }),
    );
    expect(merged).toMatchObject({
      title: "Edited main.go",
      kind: "edit",
      output: "done",
      output_spans: [{ start: 0, end: 4, fg: 1, bg: 2, attrs: 3 }],
      diffs: [{ path: "main.go", old_text: "a", new_text: "b" }],
      locations: [{ path: "main.go", line: 12 }],
      duration_ms: 42,
      terminal_id: "term-1",
      workflow_id: "wf-1",
      checkpoint: { original: "o", modified: "m", local: "l" },
      disclosed: { type: "steering", display_name: "steering.md", uri: "file:///steering.md" },
      denial: {
        capability: "shell",
        resource: "rm -rf /",
        scope: "user",
        source: "permissions.yaml",
        rule: { capability: "shell", effect: "deny" },
      },
      declined: true,
    });
  });

  it("leaves a member the result does not send on the call's own value", () => {
    const merged = settledToolCall(
      toolCall("c1", { title: "Run Command", terminal_id: "term-1" }),
      toolResult({ output: "done" }),
    );
    expect({ title: merged.title, terminal_id: merged.terminal_id }).toEqual({
      title: "Run Command",
      terminal_id: "term-1",
    });
  });

  it("never writes declined false, because the wire only ever sends true", () => {
    // The field is a ONE-WAY latch: `declined: false` on the result would be absent on the wire, so
    // a spread of it would be the one way a later frame could clear the mark.
    const merged = settledToolCall(toolCall("c1"), toolResult({ declined: false }));
    expect("declined" in merged).toBe(false);
  });
});

describe("foldToolCallDelta carries every member a progress frame sends", () => {
  function delta(over: Partial<ToolProgressPayload> = {}): ToolProgressPayload {
    return { turn: "t1", tool_call_id: "c1", ...over };
  }

  it("takes the delegate and run ids, the disclosure list and the denial", () => {
    // A delegate's own card is keyed by `agent_subtask_id` and a run card by `workflow_id`, so a
    // frame that drops either reaches the transcript as a plain tool row; `disclosed` and `denial`
    // are the two the card renders as its own rows.
    const next = foldToolCallDelta(
      toolCall("c1"),
      delta({
        agent_subtask_id: "sub-1",
        workflow_id: "wf-1",
        disclosed: { type: "steering", display_name: "steering.md", uri: "file:///steering.md" },
        denial: {
          capability: "shell",
          resource: "rm -rf /",
          scope: "user",
          source: "permissions.yaml",
          rule: { capability: "shell", effect: "deny" },
        },
      }),
    );
    expect(next).toMatchObject({
      agent_subtask_id: "sub-1",
      workflow_id: "wf-1",
      disclosed: { type: "steering", display_name: "steering.md", uri: "file:///steering.md" },
      denial: {
        capability: "shell",
        resource: "rm -rf /",
        scope: "user",
        source: "permissions.yaml",
        rule: { capability: "shell", effect: "deny" },
      },
    });
  });

  it("leaves each of them on the held value when the frame carries none", () => {
    const next = foldToolCallDelta(
      toolCall("c1", { agent_subtask_id: "sub-1", workflow_id: "wf-1" }),
      delta({ output_delta: "x" }),
    );
    expect({ sub: next.agent_subtask_id, wf: next.workflow_id }).toEqual({
      sub: "sub-1",
      wf: "wf-1",
    });
  });
});

describe("two tool updates in one tick", () => {
  /** Mount a card for `callID` on `turnID` and fold one output delta into it, which is the
   *  production path that earns the `tool` cause. */
  function progress(chatID: string, turnID: string, callID: string, text: string): void {
    applyToolProgress(chatID, turnID, { turn: turnID, tool_call_id: callID, output_delta: text });
  }

  function twoTurnsWithCards(chatID: string): void {
    resetStore(chatID);
    for (const [turnID, callID, n] of [
      ["t1", "c1", 1],
      ["t2", "c2", 2],
    ] as const) {
      openTurn(chatID, turnOpenEntry(turnID, n));
      appendEntry(chatID, sealed(turnID, 1, "tool_call", toolCall(callID), callID));
      ensureToolCallSig(chatID, callID, toolCall(callID));
    }
  }

  it("stays a keyed update of ONE turn when both land on that turn", async () => {
    twoTurnsWithCards("a");
    progress("a", "t1", "c1", "x");
    progress("a", "t1", "c1", "y");
    await Promise.resolve();
    expect(renderCauseOf("a")).toEqual({ cause: "tool", turnID: "t1" });
  });

  it("escalates to the full pass when they land on DIFFERENT turns", async () => {
    // One keyed update cannot refresh two turns, so the merged cause has to widen — and it carries
    // no turn id, because there is no single turn for the renderer to address.
    twoTurnsWithCards("a");
    progress("a", "t1", "c1", "x");
    progress("a", "t2", "c2", "y");
    await Promise.resolve();
    expect(renderCauseOf("a")).toEqual({ cause: "shape" });
  });

  it("lets a lower-ranked cause through rather than swallowing it", async () => {
    // The tool arm RETURNS, so it has to be entered only for a tool cause. A `fact` behind one
    // still outranks it and must survive — read as a tool cause it would be swallowed, and read as
    // two turns it would widen to the full pass neither update asked for.
    twoTurnsWithCards("a");
    progress("a", "t1", "c1", "x");
    setWorkingLabel("a", "Compacting");
    await Promise.resolve();
    expect(renderCauseOf("a")).toEqual({ cause: "fact" });
  });
});
