// The activity dot on a SUBAGENT tab's row. Each rule fails silently: every open subagent tab gets
// a dot from its own invocation; a `tool_call_update` repaints it with NO tab mutation; a tab
// landing AFTER its chat's messages paints on the tab-set bump; a delegate with nothing resident
// paints ""; an unchanged status still REACHES the writer (`recordDotStatus` suppresses the bump).
// The REAL store, seeded through `openTurn`/`appendEntry`, so production's version bump is tested.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { appendEntry, defaultUsage, get, openTurn, setSessions } from "./store.js";
import { toolResultID } from "./entry-ids.js";
import { subagentRef } from "./tab-materialize.js";
import { makeToolCall } from "./__test-helpers__/model.js";
import type { Session, ToolStatus } from "./types.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";

const m = {
  painted: [] as { id: string; status: string }[],
  /** The subagent refs the projection holds, in order, as `openSubagentRefs`
   *  answers them. */
  refs: [] as string[],
};

/** The tab SET's version, bumped by the fake exactly where a committed tab
 *  mutation bumps the real one — the dependency that paints a row landing after
 *  its chat's messages. */
const tabsVersion = signal(0);

vi.mock("./tabs.js", () => ({
  // A TRACKED read, like production's: the effect's re-run on a tab mutation is
  // half of what is under test.
  openSubagentRefs: vi.fn(() => {
    void tabsVersion.value;
    return [...m.refs];
  }),
  // The projection's own lookup. The fake keeps a readable id so the assertions
  // below name something a reader can follow, and it answers ONLY for a ref the
  // set holds — which is what makes "a closed tab is not painted" observable.
  tabIdFor: vi.fn((_kind: string, ref: string) => (m.refs.includes(ref) ? `sub:${ref}` : "")),
  setTabStatus: vi.fn((id: string, status: string) => {
    m.painted.push({ id, status });
  }),
}));

const { installSubagentDotSubscriber } = await import("./subagent-dots.js");

/** Bump the mocked tab set the way a committed tab mutation does. */
function tabsChanged(): void {
  tabsVersion.value = tabsVersion.value + 1;
}

/** Let the store's microtask coalescer flush. A macrotask, so every pending
 *  microtask has drained by the time it resolves. */
function tick(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function invocation(subtaskID: string, status: ToolStatus, id = `tc-${subtaskID}`): EntryToolCall {
  return makeToolCall({
    id,
    // One of the four titles `tool-schema.ts` `isSubagentInvocation` accepts. The dot
    // resolves through that predicate, so a nested call of the same delegate must
    // not be mistaken for its invocation.
    title: "Sub-agent: introspect",
    kind: "other",
    status,
    agent_subtask_id: subtaskID,
    ts: 1,
  });
}

/** The turn of `chatID`, whose `turn_open` is always `seq` 0. */
function turnOpen(chatID: string): Entry {
  return {
    id: `${chatID}-open`,
    turn: `t-${chatID}`,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: `m-${chatID}`, text: "delegate this" } },
  };
}

/** An empty session for `chatID`. The turn is seeded separately, so a chat with no
 *  window at all is expressible — which is what a boot-restored tab has. */
function emptyChat(chatID: string): Session {
  return {
    id: chatID,
    name: chatID,
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

/** One empty session per chat, then each one's turn: a chat with no window is a real state, and
 *  only the store's operations produce a turn it accepts. */
function seedChats(specs: readonly (readonly [string, readonly EntryToolCall[]])[]): void {
  setSessions(specs.map(([chatID]) => emptyChat(chatID)));
  for (const [chatID, calls] of specs) {
    seedTurn(chatID, calls);
  }
}

/** Seed `chatID` with one turn holding `calls` as `tool_call` entries in the ISSUER's
 *  lane, which is where an invocation lives. */
function seedTurn(chatID: string, calls: readonly EntryToolCall[]): void {
  openTurn(chatID, turnOpen(chatID));
  calls.forEach((call, i) => {
    appendEntry(chatID, {
      id: call.id,
      turn: `t-${chatID}`,
      kind: "tool_call",
      seq: i + 1,
      ts: i + 2,
      payload: call,
    });
  });
}

/** The `tool_result` that SETTLES `call`, paired to it by `<tool_call id>:result`. The
 *  create frame's status is a starting state; this is the verdict, and it is what a
 *  running delegate's dot has to follow. */
function settle(chatID: string, call: EntryToolCall, status: ToolStatus, seq: number): void {
  appendEntry(chatID, {
    id: toolResultID(call.id),
    turn: `t-${chatID}`,
    kind: "tool_result",
    seq,
    ts: seq + 2,
    payload: { status },
  });
}

let installed = false;

beforeEach(() => {
  m.painted.length = 0;
  m.refs.length = 0;
  setSessions([]);
  if (!installed) {
    installSubagentDotSubscriber();
    installed = true;
  }
  tabsChanged();
  m.painted.length = 0;
});

describe("a delegate's tab paints its own invocation's state", () => {
  it.each([
    ["in_progress", "working"],
    ["pending", "working"],
    ["completed", "done"],
    ["failed", "failed"],
  ])("paints %s as %s", (status, want) => {
    seedChats([["c1", [invocation("task-1", status as ToolStatus)]]]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({ id: `sub:${subagentRef("c1", "task-1")}`, status: want });
  });

  it("paints nothing at all when no invocation is resident", () => {
    // No invocation in the window (unfetched or evicted): "" is honest; not knowing is not idle.
    seedChats([["c1", []]]);
    m.refs.push(subagentRef("c1", "task-missing"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "task-missing")}`,
      status: "",
    });
  });

  it("ignores a nested call of the same delegate, so a running tool cannot mask a finished one", () => {
    // Every call the delegate MADE shares its subtask id; only the invocation
    // carries an invocation title. Without the predicate the dot would report
    // whichever call happened to come first in the array.
    const nested = makeToolCall({
      id: "tc-nested",
      title: "Read file",
      kind: "read",
      status: "in_progress",
      agent_subtask_id: "task-1",
      ts: 1,
    });
    seedChats([["c1", [nested, invocation("task-1", "completed")]]]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("done");
  });
});

describe("the launching chat's transcript is the repaint dependency", () => {
  it("repaints in_progress to failed when the tool_result lands, with no tab mutation", async () => {
    seedChats([["c1", [invocation("task-1", "in_progress")]]]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("working");
    const before = m.painted.length;

    // The verdict arrives as a `tool_result` ENTRY with no tab change, so only the per-chat version
    // read can repaint the dot.
    settle("c1", invocation("task-1", "in_progress"), "failed", 2);
    await tick();

    expect(m.painted.length).toBeGreaterThan(before);
    expect(m.painted.at(-1)?.status).toBe("failed");
  });

  it("repaints a delegate in a chat the reader is not looking at", async () => {
    // The property the whole feature rests on: an entry frame is ingested for any
    // chat with a store row, with no active-chat gate, so a background delegate's dot
    // is correct. Two chats, and the one that moves is not the one painted first.
    seedChats([
      ["c1", [invocation("task-1", "completed")]],
      ["c2", [invocation("task-2", "in_progress")]],
    ]);
    m.refs.push(subagentRef("c1", "task-1"), subagentRef("c2", "task-2"));
    tabsChanged();
    m.painted.length = 0;

    settle("c2", invocation("task-2", "in_progress"), "completed", 2);
    await tick();

    expect(m.painted).toContainEqual({ id: `sub:${subagentRef("c2", "task-2")}`, status: "done" });
  });

  it("paints a tab that landed AFTER its chat's messages, on the tab-set bump alone", () => {
    // The footer link exists only once the delegate's blocks are resident, so the tab arrives later and
    // only the tab-set dependency can paint the row.
    seedChats([["c1", [invocation("task-1", "completed")]]]);
    expect(m.painted).toEqual([]);

    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "task-1")}`,
      status: "done",
    });
  });

  it("re-applies an unchanged status, because a rebuilt row has to be repainted", async () => {
    // `setTabStatus` must be REACHED every pass, or a rebuilt row keeps the factory's paint.
    seedChats([["c1", [invocation("task-1", "in_progress")]]]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    const after = m.painted.length;

    // A transcript change that leaves the delegate's own status alone.
    appendEntry("c1", {
      id: "t-c1-e2",
      turn: "t-c1",
      kind: "text",
      seq: 2,
      ts: 3,
      payload: { text: "the chat carried on" },
    });
    await tick();

    expect(m.painted.length).toBeGreaterThan(after);
    expect(m.painted.at(-1)?.status).toBe("working");
  });
});

describe("the tab set bounds the work, so nothing has to be swept", () => {
  it("stops painting a closed tab", () => {
    seedChats([["c1", [invocation("task-1", "in_progress")]]]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("working");

    m.refs.length = 0;
    tabsChanged();
    m.painted.length = 0;
    // A later transcript change for the same chat must reach no row at all: the
    // pass enumerates the tab set, so a closed tab is not visited and there is no
    // tracked set left holding it.
    tabsChanged();
    expect(m.painted).toEqual([]);
  });

  it("paints every open tab of one chat, not just the first", () => {
    seedChats([["c1", [invocation("task-1", "in_progress"), invocation("task-2", "failed")]]]);
    m.refs.push(subagentRef("c1", "task-1"), subagentRef("c1", "task-2"));
    tabsChanged();
    expect(m.painted).toContainEqual({
      id: `sub:${subagentRef("c1", "task-1")}`,
      status: "working",
    });
    expect(m.painted).toContainEqual({
      id: `sub:${subagentRef("c1", "task-2")}`,
      status: "failed",
    });
  });
});

describe("a ref this client cannot resolve degrades rather than throwing", () => {
  it("paints nothing and throws nothing for a malformed ref", () => {
    // A ref arrives from the PERSISTED collection, so a bad one can reach any
    // device. `parseSubagentRef` answers two empty halves and the pass skips it —
    // the row keeps the factory's fallback name and its invisible reserved slot.
    seedChats([["c1", [invocation("task-1", "completed")]]]);
    m.refs.push("no-separator-here");
    expect(() => tabsChanged()).not.toThrow();
    expect(m.painted).toEqual([]);
  });

  it("paints nothing for a WORKFLOW STEP's subtask id", () => {
    // A deep link can name `wf:<workflowId>:<nodePath>`; `isSubagentInvocation` rejects `run_workflow`,
    // so the dot degrades to "".
    const step = makeToolCall({
      id: "tc-run",
      title: "run_workflow",
      kind: "other",
      status: "in_progress",
      agent_subtask_id: "wf:wf_1:root/iter-1",
      ts: 1,
    });
    seedChats([["c1", [step]]]);
    m.refs.push(subagentRef("c1", "wf:wf_1:root/iter-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "wf:wf_1:root/iter-1")}`,
      status: "",
    });
  });

  it("paints nothing for a chat this client holds no row for", () => {
    // A deep link to a delegate in a conversation that is not open here. The
    // effect must still visit the ref, or the row would never paint when that
    // chat's messages arrive.
    expect(get("c-unknown")).toBeUndefined();
    m.refs.push(subagentRef("c-unknown", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c-unknown", "task-1")}`,
      status: "",
    });
  });
});
