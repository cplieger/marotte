// Composite tool identity, the parked terminal buffer, and the refcounted run clock. Tool call ids have no
// cross-chat uniqueness and parked views stay resident, so card registries key on toolCallSigKey(chatID, toolID).

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

// The graph reads the DOM registry at module scope and `byId` throws on a missing element.
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

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// The run store's fetch uses the OrError variant; a live run's running node arms the card's clock.
const apiGetOrErrorMock = vi.hoisted(() => vi.fn());
vi.mock("./api-client.js", () => ({
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiGetOrError: apiGetOrErrorMock,
}));

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const turns = await import("./turns.js");
const messages = await import("./messages.js");
const blocks = await import("./messages-blocks.js");
const { appendTerminalChunk } = await import("./messages-tools.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

// --- Fixtures -------------------------------------------------------------------------

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  id?: string,
  lane?: string,
): Entry {
  const e: Entry = {
    id: id ?? `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  };
  return lane === undefined ? e : { ...e, lane };
}

function turnOpen(turnID: string, n: number, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

function toolCall(
  turnID: string,
  at: number,
  id: string,
  over: Record<string, unknown> = {},
  lane?: string,
): Entry {
  return sealed(
    turnID,
    at,
    "tool_call",
    { id, title: `run ${id}`, kind: "execute", status: "in_progress", ts: at + 1, ...over },
    id,
    lane,
  );
}

function toolResult(turnID: string, at: number, callID: string, status = "completed"): Entry {
  return sealed(turnID, at, "tool_result", { status }, `${callID}:result`);
}

/** A chat holding whole turns, the shape a page GET lands. */
function settled(
  id: string,
  turnEntries: readonly (readonly Entry[])[],
  over: Partial<Session> = {},
): Session {
  const s = session(id, over);
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  return s;
}

/** A chat of one bare prompt turn — the neighbour a park needs somewhere to go. */
function bystander(id: string): Session {
  const t = `${id}-t1`;
  return settled(id, [[turnOpen(t, 1, "x")]]);
}

/** Mount chats and activate the first, announcing a replay so no case inherits an arrival tail from its seed. */
function seed(...sessions: Session[]): void {
  store.setSessions(sessions);
  const first = sessions[0];
  if (first !== undefined) {
    store.setActive(first.id);
    store.bumpMessages(first.id, "load");
  }
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** The projected turn a detached render takes: a real store turn, nothing copied. */
function turnOf(chatID: string, turnID: string): Turn {
  const s = store.get(chatID);
  if (s === undefined) {
    throw new Error(`no session ${chatID}`);
  }
  const t = turns.projectTurn(s, turnID);
  if (t === undefined) {
    throw new Error(`turn ${turnID} is not drawn`);
  }
  return t;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope; earlier parked views would count against the LRU.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  apiGetOrErrorMock.mockReset();
  // Status 0 keeps the store's retry ladder, where a settled 404 would skip it.
  apiGetOrErrorMock.mockResolvedValue({ ok: false, status: 0, data: null, error: "" });
});

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Composite tool identity.
// ---------------------------------------------------------------------------

describe("composite tool identity", () => {
  it("two chats with IDENTICAL tool ids park and unpark without cross-corruption", async () => {
    // Both chats' frames name the same tool_call.id.
    const a = freshID("c-same");
    const b = freshID("c-same");
    const shared = "toolu_shared";
    const ta = `${a}-t1`;
    const tb = `${b}-t1`;
    seed(
      settled(a, [[turnOpen(ta, 1), toolCall(ta, 1, shared, { title: "chat A's call" })]]),
      settled(b, [[turnOpen(tb, 1), toolCall(tb, 1, shared, { title: "chat B's call" })]]),
    );
    await flushed();

    store.setActive(b);
    await flushed();
    // Re-queried per read: `resumeTurnBody` rebuilds an open turn's body, so a held node detaches at unpark.
    const cardA = (): HTMLElement | null =>
      viewOf(a).querySelector<HTMLElement>(`[data-tool-id="${shared}"]`);
    const cardB = (): HTMLElement | null =>
      viewOf(b).querySelector<HTMLElement>(`[data-tool-id="${shared}"]`);
    expect(cardA()).not.toBeNull();
    expect(cardB()).not.toBeNull();
    // The pre-state, so the assertions below are about what a writer did.
    expect(cardA()?.dataset["outcome"]).toBe("running");
    expect(cardB()?.dataset["outcome"]).toBe("running");

    store.appendEntry(b, toolResult(tb, 2, shared, "completed"));
    await flushed();
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    expect(cardA()?.dataset["outcome"]).not.toBe("ok");

    // A's result arrives under the same id while B is active and subscribed; a shared signal would land `fail` on B.
    store.appendEntry(a, toolResult(ta, 2, shared, "failed"));
    await flushed();
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    expect(cardA()?.dataset["outcome"]).toBe("running");

    store.setActive(a);
    await flushed();
    expect(cardA()?.dataset["outcome"]).toBe("fail");
    expect(cardB()?.dataset["outcome"]).toBe("ok");
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(a, shared))).toBeDefined();
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(b, shared))).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// The parked terminal buffer.
// ---------------------------------------------------------------------------

describe("the parked terminal buffer", () => {
  it("drains once at resume and drops the oldest past 64 KB", async () => {
    const a = freshID("c-term");
    const b = freshID("c-term");
    const t = `${a}-t1`;
    const termID = freshID("term");
    // An open turn with the command running, whose body is rebuilt at unpark.
    seed(
      settled(a, [[turnOpen(t, 1, "run it"), toolCall(t, 1, "t-term", { terminal_id: termID })]]),
      bystander(b),
    );
    await flushed();
    appendTerminalChunk(termID, "live line\n", [], 0);
    const pre = (): string =>
      viewOf(a).querySelector(".tool-call .tool-output pre")?.textContent ?? "";
    expect(pre()).toBe("live line\n");

    store.setActive(b);
    await flushed();

    // 90 KB parked exceeds the 64 KB cap, so the oldest drops, as shell scrollback does.
    const chunk = (label: string): string =>
      `${label}${"x".repeat(30 * 1024 - label.length - 1)}\n`;
    const oldest = chunk("oldest");
    const middle = chunk("middle");
    const newest = chunk("newest");
    let offset = "live line\n".length;
    for (const c of [oldest, middle, newest]) {
      appendTerminalChunk(termID, c, [], offset);
      offset += c.length;
    }
    expect(pre()).toBe("live line\n");

    store.setActive(a);
    await flushed();
    // Drained once: the two newest chunks land after the text already on screen.
    const text = pre();
    expect(text.startsWith("live line\n")).toBe(true);
    expect(text).toContain("middle");
    expect(text).toContain("newest");
    expect(text).not.toContain("oldest");

    // The buffer was consumed by the drain, so a second cycle replays nothing.
    store.setActive(b);
    await flushed();
    store.setActive(a);
    await flushed();
    expect(pre()).toBe(text);

    // The rebuilt card still owns the terminal.
    appendTerminalChunk(termID, "after\n", [], offset);
    expect(pre().endsWith("after\n")).toBe(true);
  });
});

// One refcounted interval per workflow, so a park releasing the transcript's hold must not stop a clock another
// surface still reads. Two cards for one run come only from two calls naming it in different lanes.

describe("the run clock", () => {
  /** A live run: one running leaf, started, never ended. */
  function liveRunPayload(wf: string): unknown {
    return {
      workflowId: wf,
      state: {
        workflowId: wf,
        status: "running",
        root: {
          nodeId: "n1",
          type: "step",
          status: "running",
          startedAt: new Date(Date.now() - 5000).toISOString(),
        },
      },
    };
  }

  it("survives a park while another surface holds the same run", async () => {
    vi.useFakeTimers();

    const a = freshID("c-clock");
    const b = freshID("c-clock");
    const wf = freshID("wf");
    const lane = "subtask-1";
    const t = `${a}-t1`;
    apiGetOrErrorMock.mockImplementation(() =>
      Promise.resolve({ ok: true, status: 200, data: liveRunPayload(wf), error: "" }),
    );
    seed(
      settled(a, [
        [
          turnOpen(t, 1, "run the workflow"),
          toolCall(t, 1, "t-launch", { workflow_id: wf, title: "Run Workflow" }),
          // A delegate mentioning the same run: it renders nowhere in the transcript and is the body of its own page.
          toolCall(t, 2, "t-inspect", { workflow_id: wf, title: "Inspect Workflow" }, lane),
        ],
      ]),
      bystander(b),
    );
    await flushed();
    // Let the mocked run fetch resolve into the cell.
    await vi.advanceTimersByTimeAsync(0);

    const transcriptCard = viewOf(a).querySelector<HTMLElement>(".run-card");
    expect(transcriptCard).not.toBeNull();

    // The delegate's page holds its own clock ref.
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, turnOf(a, t), a, lane, false);
    await vi.advanceTimersByTimeAsync(0);
    // The run's elapsed is rendered in the foot, so the ledger is where a clock is read.
    const detachedClock = host.querySelector<HTMLElement>(".run-ledger");
    expect(detachedClock).not.toBeNull();

    // Park A: the detached surface's hold keeps the shared interval alive.
    store.setActive(b);
    await flushed();

    const before = detachedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    const after = detachedClock?.textContent ?? "";
    expect(after).not.toBe("");
    expect(after).not.toBe(before);

    const parkedClock = viewOf(a).querySelector<HTMLElement>(".run-ledger");
    const parkedBefore = parkedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(parkedClock?.textContent ?? "").toBe(parkedBefore);

    store.setActive(a);
    await flushed();
    const resumedClock = viewOf(a).querySelector<HTMLElement>(".run-ledger");
    const resumedBefore = resumedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(resumedClock?.textContent ?? "").not.toBe(resumedBefore);

    blocks.disposeDetachedBody(t, lane);
    host.remove();
  });
});
