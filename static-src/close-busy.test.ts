// The close gate: an idle subtree closes in the same task with no dialog; each kind of live work
// opens the destructive confirm and names itself. Real store and run inventory; the dock and the
// dialog are mocked.

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Session } from "./types.js";
import type * as ApiClient from "./api-client.js";
import type * as DecisionDock from "./decision-dock.js";
import type * as Confirm from "./confirm.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async () => null),
  apiGetOrError: vi.fn(async () => ({ ok: false, status: 0, data: null, error: "" })),
}));

const pendingChats = new Set<string>();
vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDock>()),
  hasPendingDecision: (chatID: string) => pendingChats.has(chatID),
}));

const mockConfirm = vi.fn(async (_message: string, _label?: string, _variant?: string) => true);
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: mockConfirm,
}));

const { confirmBusyClose } = await import("./close-busy.js");
const store = await import("./store.js");
const runStore = await import("./run-store.js");

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
  const verdict = confirmBusyClose(chats[0]?.name ?? "", chats[0]?.ref ?? "", chats);
  if (verdict === true) {
    expect(mockConfirm).not.toHaveBeenCalled();
    return null;
  }
  await verdict;
  expect(mockConfirm).toHaveBeenCalledTimes(1);
  const [message, label, variant] = mockConfirm.mock.calls[0] ?? [];
  expect([label, variant]).toEqual(["Close and stop", "destructive"]);
  return message ?? "";
}

beforeEach(() => {
  for (const id of ["wf-1", "wf-2", "wf-3"]) {
    runStore.noteRunSettled(id);
    runStore.forgetRun(id);
  }
  pendingChats.clear();
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
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunSettled("wf-1");
    expect(await gate()).toBeNull();
  });

  it("is unmoved by another chat's work", async () => {
    store.setTurnOpen("t", true);
    runStore.noteRunLive("wf-1", "t", true);
    pendingChats.add("t");
    expect(await gate([A])).toBeNull();
  });
});

describe("each kind of live work names itself", () => {
  it("a running reply", async () => {
    store.setTurnOpen("a", true);
    expect(await gate()).toBe("Closing Alpha stops the reply.");
  });

  it("a question waiting", async () => {
    pendingChats.add("a");
    expect(await gate()).toBe("Closing Alpha cancels the question waiting for your answer.");
  });

  it("live workflow runs, a parked one included", async () => {
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunLive("wf-2", "a", false);
    expect(await gate()).toBe("Closing Alpha stops 2 workflow runs.");
  });

  it("one live workflow run", async () => {
    runStore.noteRunLive("wf-1", "a", false);
    expect(await gate()).toBe("Closing Alpha stops 1 workflow run.");
  });

  it("names each run whose name is known", async () => {
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunLive("wf-2", "a", false);
    runStore.noteRunLabel("wf-1", "Build");
    runStore.noteRunLabel("wf-2", "Deploy");
    expect(await gate()).toBe("Closing Alpha stops the workflow runs Build and Deploy.");
  });

  it("names one run", async () => {
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunLabel("wf-1", "Build");
    expect(await gate()).toBe("Closing Alpha stops the workflow run Build.");
  });

  it("counts the runs whose name has not loaded beside the named ones", async () => {
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunLive("wf-2", "a", true);
    runStore.noteRunLive("wf-3", "a", false);
    runStore.noteRunLabel("wf-1", "Build");
    expect(await gate()).toBe("Closing Alpha stops the workflow runs Build and 2 others.");
  });

  it("unread steers and unsent follow-ups, held ones included", async () => {
    store.setSessions([
      makeSession("a", {
        steers: [{ id: "steer-1", text: "also", origin: "user", unsent: true }],
        queued: [
          { id: "q1", text: "then" },
          { id: "q2", text: "later", held: true },
        ],
      }),
    ]);
    expect(await gate()).toBe("Closing Alpha drops 3 queued messages.");
  });

  it("not a follow-up whose turn already opened: that is the running reply", async () => {
    store.setSessions([makeSession("a", { queued: [{ id: "q1", text: "then" }] })]);
    store.openTurn("a", {
      id: "t1-open",
      turn: "t1",
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source: "prompt", n: 1, prompt: { id: "q1", text: "then" } },
    });
    expect(await gate()).toBe("Closing Alpha stops the reply.");
  });

  it("every term together, in one sentence", async () => {
    store.setSessions([makeSession("a", { queued: [{ id: "q1", text: "then" }] })]);
    store.setTurnOpen("a", true);
    pendingChats.add("a");
    runStore.noteRunLive("wf-1", "a", true);
    expect(await gate()).toBe(
      "Closing Alpha stops the reply, cancels the question waiting for your answer, stops 1 workflow run, and drops 1 queued message.",
    );
  });
});

describe("the subtree", () => {
  it("a busy tangent under an idle parent makes the close busy and is named", async () => {
    store.setTurnOpen("t", true);
    pendingChats.add("t");
    expect(await gate([A, T])).toBe(
      "Closing Alpha stops the reply in Tangent and cancels the question waiting for your answer in Tangent.",
    );
  });

  it("names every chat when the parent and a tangent are both replying", async () => {
    store.setTurnOpen("a", true);
    store.setTurnOpen("t", true);
    expect(await gate([A, T])).toBe("Closing Alpha stops the replies in Alpha and Tangent.");
  });

  it("counts runs and queued messages across the subtree", async () => {
    store.setSessions([
      makeSession("a"),
      makeSession("t", { queued: [{ id: "q1", text: "then" }] }),
    ]);
    runStore.noteRunLive("wf-1", "a", true);
    runStore.noteRunLive("wf-2", "t", true);
    expect(await gate([A, T])).toBe(
      "Closing Alpha stops 2 workflow runs and drops 1 queued message.",
    );
  });

  it("names the chat for a tab that is not one itself", async () => {
    store.setTurnOpen("t", true);
    mockConfirm.mockClear();
    const verdict = confirmBusyClose("Run tab", "", [T]);
    expect(verdict).not.toBe(true);
    await verdict;
    expect(mockConfirm.mock.calls[0]?.[0]).toBe("Closing Run tab stops the reply in Tangent.");
  });
});

describe("the answer", () => {
  it("is the dialog's: Cancel keeps the tab", async () => {
    store.setTurnOpen("a", true);
    mockConfirm.mockImplementation(async () => false);
    await expect(confirmBusyClose("Alpha", "a", [A])).resolves.toBe(false);
  });

  it("is the dialog's: confirming closes", async () => {
    store.setTurnOpen("a", true);
    await expect(confirmBusyClose("Alpha", "a", [A])).resolves.toBe(true);
  });
});
