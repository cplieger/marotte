// The close gate: an idle subtree closes in the same task with no dialog; each kind of live work
// opens the destructive confirm and names itself. Real store and run inventory; the dock and the
// dialog are mocked.

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Session } from "./types.js";
import type * as ApiClient from "./api-client.js";
import type * as DecisionDock from "./decision-dock.js";
import type * as Confirm from "./confirm.js";
import type { Confirmation } from "./confirm.js";
import { confirmText } from "./__test-helpers__/confirm-text.js";
import type * as Submit from "./submit.js";
import { rowEntry, runAt } from "./__test-helpers__/run-order.js";
import type { RunEntryView } from "./types.js";

/** A queued row the server took and whose turn opened as `turn`. */
function openedRow(id: string, turn: string): RunEntryView {
  return rowEntry(id, { after: "running" }, { state: "running", turn });
}

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async () => null),
  apiGetOrError: vi.fn(async () => ({ ok: false, status: 0, data: null, error: "" })),
}));

const pendingAsks = new Map<string, string[]>();
vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDock>()),
  pendingDecisionIDs: (chatID: string) => pendingAsks.get(chatID) ?? [],
}));

const inFlight = new Map<string, string[]>();
vi.mock("./submit.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Submit>()),
  inFlightMessageIDs: (chatID: string) => inFlight.get(chatID) ?? [],
}));

const mockConfirm = vi.fn(async (_q: Confirmation) => true);
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: mockConfirm,
}));

const { confirmBusyClose, deleteStopsWork, mergeImpact } = await import("./close-busy.js");
const store = await import("./store.js");
const runStore = await import("./run-store.js");
const { frameRunGone, frameRunLive } = await import("./__test-helpers__/live-runs.js");

function makeSession(id: string, extra: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
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
    turn_open: false,
    working_label: "Thinking",
    ...extra,
  };
}

const A = { ref: "a", name: "Alpha" };
const T = { ref: "t", name: "Tangent" };

/** The confirm's message, or null when the gate answered without a dialog. */
async function gate(chats = [A]): Promise<string | null> {
  mockConfirm.mockClear();
  const { verdict } = confirmBusyClose(chats[0]?.name ?? "", chats[0]?.ref ?? "", chats);
  if (verdict === true) {
    expect(mockConfirm).not.toHaveBeenCalled();
    return null;
  }
  await verdict;
  expect(mockConfirm).toHaveBeenCalledTimes(1);
  const q = mockConfirm.mock.calls[0]?.[0];
  expect([q?.confirmLabel, q?.variant]).toEqual(["Close and stop", "destructive"]);
  return confirmText(q);
}

beforeEach(() => {
  for (const id of ["wf-1", "wf-2", "wf-3"]) {
    frameRunGone(id);
    runStore.forgetRun(id);
  }
  pendingAsks.clear();
  inFlight.clear();
  mockConfirm.mockImplementation(async () => true);
  store.setSessions([makeSession("a"), makeSession("t")]);
});

describe("an idle close", () => {
  it("answers true in the same task, with no dialog", async () => {
    expect(await gate([A, T])).toBeNull();
  });

  it("ignores a finished turn and a settled run", async () => {
    store.setTurnOpen("a", true);
    store.setTurnOpen("a", false);
    frameRunLive("wf-1", "a", true);
    frameRunGone("wf-1");
    expect(await gate()).toBeNull();
  });

  it("is unmoved by another chat's work", async () => {
    store.setTurnOpen("t", true);
    frameRunLive("wf-1", "t", true);
    pendingAsks.set("t", ["7"]);
    expect(await gate([A])).toBeNull();
  });
});

describe("each kind of live work names itself", () => {
  it("a running reply", async () => {
    store.setTurnOpen("a", true);
    expect(await gate()).toBe("Close Alpha? It stops the reply.");
  });

  it("a question waiting", async () => {
    pendingAsks.set("a", ["3"]);
    expect(await gate()).toBe("Close Alpha? It cancels the question waiting for your answer.");
  });

  it("live workflow runs, a parked one included", async () => {
    frameRunLive("wf-1", "a", true);
    frameRunLive("wf-2", "a", false);
    expect(await gate()).toBe("Close Alpha? It stops 2 workflow runs.");
  });

  it("one live workflow run", async () => {
    frameRunLive("wf-1", "a", false);
    expect(await gate()).toBe("Close Alpha? It stops 1 workflow run.");
  });

  it("names each run whose name is known", async () => {
    frameRunLive("wf-1", "a", true);
    frameRunLive("wf-2", "a", false);
    runStore.noteRunLabel("wf-1", "Build");
    runStore.noteRunLabel("wf-2", "Deploy");
    expect(await gate()).toBe("Close Alpha? It stops the workflow runs Build and Deploy.");
  });

  it("names one run", async () => {
    frameRunLive("wf-1", "a", true);
    runStore.noteRunLabel("wf-1", "Build");
    expect(await gate()).toBe("Close Alpha? It stops the workflow run Build.");
  });

  it("counts the runs whose name has not loaded beside the named ones", async () => {
    frameRunLive("wf-1", "a", true);
    frameRunLive("wf-2", "a", true);
    frameRunLive("wf-3", "a", false);
    runStore.noteRunLabel("wf-1", "Build");
    expect(await gate()).toBe("Close Alpha? It stops the workflow runs Build and 2 others.");
  });

  it("unread steers and unsent follow-ups, held ones included", async () => {
    store.setSessions([
      makeSession("a", {
        steers: [{ id: "steer-1", text: "also", origin: "user", unsent: true }],
        run: runAt(1, [rowEntry("q1"), rowEntry("q2", { after: "held" }, { state: "held" })]),
      }),
    ]);
    expect(await gate()).toBe("Close Alpha? It drops 3 queued messages.");
  });

  it("not a follow-up whose turn already opened: that is the running reply", async () => {
    store.setSessions([makeSession("a", { run: runAt(1, [openedRow("q1", "t1")]) })]);
    store.openTurn("a", {
      id: "t1-open",
      turn: "t1",
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source: "prompt", n: 1, prompt: { id: "q1", text: "then" } },
    });
    expect(await gate()).toBe("Close Alpha? It stops the reply.");
  });

  it("every term together, in one sentence", async () => {
    store.setSessions([makeSession("a", { run: runAt(1, [rowEntry("q1")]) })]);
    store.setTurnOpen("a", true);
    pendingAsks.set("a", ["3"]);
    frameRunLive("wf-1", "a", true);
    expect(await gate()).toBe(
      "Close Alpha? It stops the reply, cancels the question waiting for your answer, stops 1 workflow run, and drops 1 queued message.",
    );
  });
});

describe("the subtree", () => {
  it("a busy tangent under an idle parent makes the close busy and is named", async () => {
    store.setTurnOpen("t", true);
    pendingAsks.set("t", ["7"]);
    expect(await gate([A, T])).toBe(
      "Close Alpha? It stops the reply in Tangent and cancels the question waiting for your answer in Tangent.",
    );
  });

  it("names every chat when the parent and a tangent are both replying", async () => {
    store.setTurnOpen("a", true);
    store.setTurnOpen("t", true);
    expect(await gate([A, T])).toBe("Close Alpha? It stops the replies in Alpha and Tangent.");
  });

  it("counts runs and queued messages across the subtree", async () => {
    store.setSessions([makeSession("a"), makeSession("t", { run: runAt(1, [rowEntry("q1")]) })]);
    frameRunLive("wf-1", "a", true);
    frameRunLive("wf-2", "t", true);
    expect(await gate([A, T])).toBe(
      "Close Alpha? It stops 2 workflow runs and drops 1 queued message.",
    );
  });

  it("names the chat for a tab that is not one itself", async () => {
    store.setTurnOpen("t", true);
    mockConfirm.mockClear();
    const { verdict } = confirmBusyClose("Run tab", "", [T]);
    expect(verdict).not.toBe(true);
    await verdict;
    expect(confirmText(mockConfirm.mock.calls[0]?.[0])).toBe(
      "Close Run tab? It stops the reply in Tangent.",
    );
  });
});

describe("the answer", () => {
  it("is the dialog's: Cancel keeps the tab", async () => {
    store.setTurnOpen("a", true);
    mockConfirm.mockImplementation(async () => false);
    await expect(confirmBusyClose("Alpha", "a", [A]).verdict).resolves.toBe(false);
  });

  it("is the dialog's: confirming closes", async () => {
    store.setTurnOpen("a", true);
    await expect(confirmBusyClose("Alpha", "a", [A]).verdict).resolves.toBe(true);
  });
});

function openTurn(chat: string, turn: string, promptID = `${turn}-msg`): void {
  store.openTurn(chat, {
    id: `${turn}-open`,
    turn,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: promptID, text: "go" } },
  });
}

const idle = (ref: string) => ({ ref, turns: [], asks: [], runs: [], steers: [], queued: [] });

describe("the impact the close sends", () => {
  it("names every chat the close takes, with the ids of its open turns, its own in-flight sends, its asks, its runs, its unread steers and its queued follow-ups", () => {
    store.setSessions([
      makeSession("a", {
        steers: [{ id: "steer-m1", text: "also", origin: "user", unsent: true }],
        run: runAt(1, [rowEntry("q1"), rowEntry("q2", { after: "held" }, { state: "held" })]),
      }),
      makeSession("t"),
    ]);
    openTurn("a", "t1");
    inFlight.set("a", ["m-9"]);
    pendingAsks.set("a", ["3"]);
    frameRunLive("wf-1", "a", true);
    expect(confirmBusyClose("Alpha", "a", [A, T]).impact).toEqual({
      chats: [
        {
          ref: "a",
          turns: ["t1", "m-9"],
          asks: ["3"],
          runs: ["wf-1"],
          steers: ["steer-m1"],
          queued: ["q1", "q2"],
        },
        idle("t"),
      ],
    });
  });

  it("does not name a follow-up whose turn opened, nor a closed turn", () => {
    store.setSessions([makeSession("a", { run: runAt(1, [openedRow("q1", "t1")]) })]);
    openTurn("a", "t0");
    store.appendEntry("a", {
      id: "t0-close",
      turn: "t0",
      kind: "turn_close",
      seq: 1,
      ts: 2,
      payload: { outcome: "completed" },
    });
    openTurn("a", "t1", "q1");
    const w = confirmBusyClose("Alpha", "a", [A]).impact.chats[0];
    expect(w?.queued).toEqual([]);
    expect(w?.turns).toContain("t1");
  });

  it("the dialog's counts are the lengths of those lists", async () => {
    store.setSessions([
      makeSession("a", {
        steers: [
          { id: "steer-1", text: "x", origin: "user", unsent: true },
          { id: "steer-2", text: "y", origin: "user", unsent: true },
        ],
        run: runAt(1, [rowEntry("q1")]),
      }),
    ]);
    frameRunLive("wf-1", "a", true);
    frameRunLive("wf-2", "a", false);
    expect(await gate()).toBe("Close Alpha? It stops 2 workflow runs and drops 3 queued messages.");
  });

  it("an idle subtree still sends its chats", () => {
    const asked = confirmBusyClose("Alpha", "a", [A, T]);
    expect(asked.verdict).toBe(true);
    expect(asked.impact).toEqual({ chats: [idle("a"), idle("t")] });
  });

  it("asks again on the merge of a stale close, naming a chat only the server knew", async () => {
    const sent = { chats: [{ ...idle("a"), turns: ["t1"] }] };
    const server = {
      chats: [
        { ...idle("a"), turns: ["t2"] },
        { ...idle("x"), asks: ["4"] },
      ],
    };
    mockConfirm.mockClear();
    const asked = confirmBusyClose("Alpha", "a", [A], { sent, server });
    expect(asked.impact).toEqual({
      chats: [
        { ...idle("a"), turns: ["t1", "t2"] },
        { ...idle("x"), asks: ["4"] },
      ],
    });
    await asked.verdict;
    expect(confirmText(mockConfirm.mock.calls[0]?.[0])).toBe(
      "Close Alpha? It stops the reply and cancels the question waiting for your answer in x.",
    );
  });
});

describe("mergeImpact", () => {
  it("unions each id list per chat and keeps every chat of either side", () => {
    const sent = {
      chats: [
        { ref: "a", turns: ["t1"], asks: ["1"], runs: ["r1"], steers: ["s1"], queued: ["q1"] },
      ],
    };
    const server = {
      chats: [
        {
          ref: "a",
          turns: ["t1", "res:7"],
          asks: ["2"],
          runs: ["r1"],
          steers: ["s2"],
          queued: ["q1", "q2"],
        },
        idle("b"),
      ],
    };
    expect(mergeImpact(sent, server)).toEqual({
      chats: [
        {
          ref: "a",
          turns: ["t1", "res:7"],
          asks: ["1", "2"],
          runs: ["r1"],
          steers: ["s1", "s2"],
          queued: ["q1", "q2"],
        },
        idle("b"),
      ],
    });
  });
});

describe("deleteStopsWork", () => {
  it("lists every descendant chat and never the deleted one", () => {
    store.setSessions([makeSession("a"), makeSession("t")]);
    expect(deleteStopsWork([T]).impact).toEqual({ chats: [idle("t")] });
  });

  it("its text is empty when every descendant is idle", () => {
    expect(deleteStopsWork([T]).text).toBe("");
  });

  it("names each busy chat otherwise", () => {
    openTurn("t", "t1");
    expect(deleteStopsWork([T]).text).toBe("Closing its tabs also stops the reply in Tangent.");
  });

  it("names no run, since a descendant's close leaves its runs running", () => {
    frameRunLive("wf-1", "t", true);
    const asked = deleteStopsWork([T]);
    expect(asked.text).toBe("");
    expect(asked.impact).toEqual({ chats: [idle("t")] });
  });
});
