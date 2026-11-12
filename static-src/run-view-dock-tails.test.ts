// The dock's line for an in-flight step the pane does not show comes from that step's tail, seeded
// by the run read and moved by its frames: no transcript is read or held for it.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as TabsModule from "./tabs.js";
import type * as DecisionDockModule from "./decision-dock.js";
import type * as ToastModule from "./toast.js";
import type * as NoticeSubjectModule from "./notice-subject.js";
import type * as NotifyModule from "./notify.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiBlobOrError: vi.fn(),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));

vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDockModule>()),
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
  runPendingAsks: vi.fn(() => ({ count: 0, asked: [], label: "" })),
  dropRunRequests: vi.fn(),
  dropTurnDecisions: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ToastModule>()),
  info: vi.fn(),
  jumpToast: vi.fn(),
  error: vi.fn(),
}));
vi.mock("./notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubjectModule>()),
  runEndJump: vi.fn(() => undefined),
}));
vi.mock("./notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NotifyModule>()),
  notifyOffScreen: vi.fn(),
  closeRunAsksExcept: vi.fn(() => Promise.resolve()),
}));

// The step stream stands in through `__mocks__/run-chat-steps.ts`.
vi.mock("./run-chat-steps.js");

import "./handlers/run.js";
import { dispatch } from "./bus.js";
import { apiGetOrError, apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { refreshRun, showRun } from "./run-view.js";
import { appendRunEntry, forgetRun, openRunTurn } from "./run-store.js";
import { hasSubject } from "./subject-versions.js";
import { showOnlyView } from "./view-swap.js";
import { rescopeOwed, setScopePresenter, type ScopeOutcome } from "./run-content-scope.js";

const WF = "wf_tails";
const A = `${WF}:a`;

function runRead(wf = WF): unknown {
  return {
    workflowId: wf,
    state: {
      workflowId: wf,
      status: "running",
      root: {
        nodeId: wf,
        type: "sequence",
        status: "running",
        children: [
          { nodeId: "a", type: "step", status: "running", startedAt: "2026-11-10T10:00:00Z" },
          { nodeId: "b", type: "step", status: "running", startedAt: "2026-11-10T10:01:00Z" },
        ],
      },
    },
    step_starts: {
      [`${wf}:a`]: {
        started_at: "2026-11-10T10:00:00Z",
        ended: false,
        turn: "t-a",
        tail: "a was here",
      },
      [`${wf}:b`]: {
        started_at: "2026-11-10T10:01:00Z",
        ended: false,
        turn: "t-b",
        tail: "b says",
      },
    },
  };
}

async function flush(): Promise<void> {
  await vi.dynamicImportSettled();
  for (let i = 0; i < 20; i++) {
    await Promise.resolve();
  }
}

let runView: HTMLElement;

beforeEach(() => {
  forgetRun(WF);
  document.body.replaceChildren();
  runView = document.createElement("div");
  runView.id = "run-view";
  runView.dataset["tabView"] = "";
  const content = document.createElement("div");
  content.className = "page-content";
  const body = document.createElement("div");
  body.id = "run-body";
  content.append(body);
  const execDock = document.createElement("div");
  execDock.id = "run-exec-dock";
  const dock = document.createElement("div");
  dock.id = "run-dock";
  runView.append(content, execDock, dock);
  document.body.append(runView);
  vi.mocked(apiGetOrError).mockResolvedValue({ ok: true, status: 200, data: runRead(), error: "" });
  vi.mocked(apiGetTyped).mockResolvedValue(null);
  vi.mocked(apiGetTypedOrError).mockResolvedValue({ ok: false, status: 0, data: null, error: "" });
});

afterEach(() => {
  document.body.replaceChildren();
});

function dockTail(label: string): string {
  const row = [...runView.querySelectorAll<HTMLButtonElement>(".ev-dock-row")].find((b) =>
    (b.getAttribute("aria-label") ?? "").startsWith(`${label},`),
  );
  return row?.querySelector(".ev-dock-tail")?.textContent ?? "";
}

describe("an in-flight step the pane does not show", () => {
  it("shows its tail from the run read and its own frames, reading and holding no transcript", async () => {
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();
    vi.mocked(apiGetTyped).mockClear();
    vi.mocked(apiGetTypedOrError).mockClear();
    expect(dockTail("a")).toBe("a was here");

    dispatch({
      type: "entry_opened",
      chat_id: "",
      payload: {
        workflow_id: WF,
        open: { turn: "t-a", id: "t-a-9", kind: "text", text: "", n: 0, lane: "" },
      },
    } as never);
    for (let n = 1; n <= 20; n++) {
      dispatch({
        type: "entry_delta",
        chat_id: "",
        payload: {
          workflow_id: WF,
          turn: "t-a",
          entry_id: "t-a-9",
          lane: "",
          n,
          delta: `w${String(n)} `,
        },
      } as never);
    }
    await flush();

    expect(dockTail("a")).toContain("w20");
    const reads = [
      ...vi.mocked(apiGetTyped).mock.calls.map(([u]) => u),
      ...vi.mocked(apiGetTypedOrError).mock.calls.map(([u]) => u),
    ].filter((u) => u.includes(`/runs/${WF}/`));
    expect(reads).toEqual([]);
    expect(
      [...runView.querySelectorAll<HTMLElement>(".ev-d-body")].map((h) => h.dataset["path"]),
    ).not.toContain(A);
  });
});

// Each case reads a run of its own: a shown run's tail cells outlive `forgetRun`.
function readWithOpenEntry(wf: string): unknown {
  const read = runRead(wf) as { step_starts: Record<string, Record<string, unknown>> };
  const row = read.step_starts[`${wf}:a`];
  if (row !== undefined) {
    row["open"] = {
      turn: "t-a",
      id: "t-a-9",
      lane: "",
      kind: "text",
      text: "## Plan\n- first point\nsecond",
      n: 3,
    };
  }
  return read;
}

function delta(wf: string, n: number, text: string): void {
  dispatch({
    type: "entry_delta",
    chat_id: "",
    payload: { workflow_id: wf, turn: "t-a", entry_id: "t-a-9", lane: "", n, delta: text },
  } as never);
}

describe("an in-flight step whose entry opened before the run read", () => {
  it("follows that entry from the read's row through its deltas and its seal", async () => {
    const wf = "wf_tails_mid";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: readWithOpenEntry(wf),
      error: "",
    });
    showOnlyView(runView);
    showRun(wf);
    refreshRun(wf);
    await flush();
    expect(dockTail("a")).toBe("second");

    delta(wf, 4, " half\nthird");
    delta(wf, 5, " line");
    await flush();
    expect(dockTail("a")).toBe("third line");

    dispatch({
      type: "entry_sealed",
      chat_id: "",
      payload: { workflow_id: wf, turn: "t-a", entry_id: "t-a-9", lane: "", seq: 7, ts: 7, n: 5 },
    } as never);
    await flush();
    expect(dockTail("a")).toBe("third line");
  });

  it("applies the deltas that arrived while the read was out", async () => {
    const wf = "wf_tails_early";
    let land: (v: unknown) => void = () => undefined;
    const out = new Promise((resolve) => {
      land = resolve;
    });
    vi.mocked(apiGetOrError).mockReturnValue(out as never);
    showOnlyView(runView);
    showRun(wf);
    refreshRun(wf);
    await flush();
    delta(wf, 3, "held by the read already");
    delta(wf, 4, " half\nthird");
    land({ ok: true, status: 200, data: readWithOpenEntry(wf), error: "" });
    await flush();
    expect(dockTail("a")).toBe("third");

    delta(wf, 5, " line");
    await flush();
    expect(dockTail("a")).toBe("third line");
  });
});

describe("a run tab leaving and re-entering the page's scope", () => {
  it("forgets its turns' stamps on leaving and catches each held open turn up on return", async () => {
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();
    openRunTurn(WF, {
      id: "t-a-0",
      turn: "t-a",
      lane: "",
      kind: "turn_open",
      payload: { source: "workflow_step", node_path: A, n: 1 },
      seq: 0,
      ts: 0,
    });
    appendRunEntry(WF, {
      id: "t-a-1",
      turn: "t-a",
      lane: "",
      kind: "text",
      payload: { text: "held" },
      seq: 1,
      ts: 1,
    });
    expect(hasSubject("run_turn", `${WF}/t-a`)).toBe(true);

    const other = document.createElement("div");
    other.dataset["tabView"] = "";
    document.body.append(other);
    showOnlyView(other);
    expect(hasSubject("run_turn", `${WF}/t-a`)).toBe(false);

    vi.mocked(apiGetTyped).mockClear();
    showOnlyView(runView);
    showRun(WF);
    await flush();
    const ranges = vi
      .mocked(apiGetTyped)
      .mock.calls.map(([u]) => u)
      .filter((u) => u.includes("/turns/"));
    expect(ranges).toEqual([`/api/runs/${WF}/turns/t-a?after=1`]);
  });
});

describe("a run tab's reads wait for the stream to deliver its run", () => {
  /** The stream the page asks for a run's topic: each ask waits for the case to answer it. */
  let asks: { run: string; answer: (outcome: ScopeOutcome) => void }[] = [];

  beforeEach(() => {
    asks = [];
    setScopePresenter(
      (run) =>
        new Promise((resolve) => {
          asks.push({ run, answer: resolve });
        }),
    );
  });

  afterEach(() => {
    setScopePresenter(() => Promise.resolve("covered"));
  });

  function runReads(wf: string): string[] {
    return [
      ...vi.mocked(apiGetTyped).mock.calls.map(([u]) => u),
      ...vi.mocked(apiGetTypedOrError).mock.calls.map(([u]) => u),
    ].filter((u) => u.includes(`/runs/${wf}/turns/`) || u.includes(`/runs/${wf}/steps/`));
  }

  /** Show `wf`'s tab with its step `a` holding an open turn, then hide it behind another view. */
  async function holdOpenTurnThenHide(wf: string): Promise<HTMLElement> {
    showOnlyView(runView);
    showRun(wf);
    asks.at(-1)?.answer("covered");
    refreshRun(wf);
    await flush();
    openRunTurn(wf, {
      id: "t-a-0",
      turn: "t-a",
      lane: "",
      kind: "turn_open",
      payload: { source: "workflow_step", node_path: `${wf}:a`, n: 1 },
      seq: 0,
      ts: 0,
    });
    appendRunEntry(wf, {
      id: "t-a-1",
      turn: "t-a",
      lane: "",
      kind: "text",
      payload: { text: "held" },
      seq: 1,
      ts: 1,
    });
    const other = document.createElement("div");
    other.dataset["tabView"] = "";
    document.body.append(other);
    showOnlyView(other);
    return other;
  }

  it("reads neither the shown step nor an open turn until the stream delivers the run", async () => {
    const wf = "wf_scope_first";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: runRead(wf),
      error: "",
    });
    showOnlyView(runView);
    showRun(wf);
    refreshRun(wf);
    await flush();
    expect(asks.map((a) => a.run)).toEqual([wf]);
    expect(runReads(wf)).toEqual([]);

    asks[0]?.answer("covered");
    await flush();
    // The shown step is in flight and holds nothing, so it is read once.
    expect(runReads(wf)).toEqual([`/api/runs/${wf}/steps/${encodeURIComponent(`${wf}:b`)}`]);
  });

  it("catches a returning run's open turn up only after the stream delivers it", async () => {
    const wf = "wf_scope_return";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: runRead(wf),
      error: "",
    });
    await holdOpenTurnThenHide(wf);
    vi.mocked(apiGetTyped).mockClear();
    vi.mocked(apiGetTypedOrError).mockClear();

    showOnlyView(runView);
    showRun(wf);
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([]);

    asks.at(-1)?.answer("covered");
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([
      `/api/runs/${wf}/turns/t-a?after=1`,
    ]);
  });

  it("reads nothing for a run that leaves while its ask waits", async () => {
    const wf = "wf_scope_left";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: runRead(wf),
      error: "",
    });
    const other = await holdOpenTurnThenHide(wf);
    vi.mocked(apiGetTyped).mockClear();
    vi.mocked(apiGetTypedOrError).mockClear();

    showOnlyView(runView);
    showRun(wf);
    const waiting = asks.at(-1);
    showOnlyView(other);
    waiting?.answer("covered");
    await flush();
    expect(runReads(wf)).toEqual([]);
  });

  it("a run that leaves and returns reads once, on its newest ask", async () => {
    const wf = "wf_scope_twice";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: runRead(wf),
      error: "",
    });
    const other = await holdOpenTurnThenHide(wf);
    vi.mocked(apiGetTyped).mockClear();
    vi.mocked(apiGetTypedOrError).mockClear();

    showOnlyView(runView);
    showRun(wf);
    const first = asks.at(-1);
    showOnlyView(other);
    showOnlyView(runView);
    showRun(wf);
    const newest = asks.at(-1);
    first?.answer("covered");
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([]);

    newest?.answer("covered");
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([
      `/api/runs/${wf}/turns/t-a?after=1`,
    ]);
  });

  /** Answer each read of `wf`'s run with the next promise the case lands, one per read made. */
  function heldRunReads(wf: string): { urls: string[]; land: (data: unknown) => void } {
    const urls: string[] = [];
    const waiting: ((v: unknown) => void)[] = [];
    vi.mocked(apiGetOrError).mockImplementation((u: string) => {
      if (u.split("?")[0] !== `/api/runs/${wf}`) {
        return Promise.resolve({ ok: true, status: 200, data: runRead(wf), error: "" }) as never;
      }
      urls.push(u);
      return new Promise((resolve) => {
        waiting.push(resolve);
      }) as never;
    });
    return {
      urls,
      land: (data) => {
        waiting.shift()?.({ ok: true, status: 200, data, error: "" });
      },
    };
  }

  /** `readWithOpenEntry`'s row one delta on: the delta a scope not yet delivering the run lost. */
  function readAfterLostDelta(wf: string): unknown {
    const read = readWithOpenEntry(wf) as {
      step_starts: Record<string, { open: { text: string; n: number } }>;
    };
    const open = read.step_starts[`${wf}:a`]?.open;
    if (open !== undefined) {
      open.text += " half\nthird";
      open.n = 4;
    }
    return read;
  }

  it("reads a run answered before its ask again, so a sibling step's line follows its deltas", async () => {
    const wf = "wf_scope_tail_before";
    const reads = heldRunReads(wf);
    showOnlyView(runView);
    showRun(wf);
    refreshRun(wf);
    await flush();
    reads.land(readWithOpenEntry(wf));
    await flush();
    expect(dockTail("a")).toBe("second");
    expect(reads.urls).toHaveLength(1);

    // Delta 4 is published before the scope delivers the run, so it never arrives.
    asks[0]?.answer("covered");
    await flush();
    expect(reads.urls).toHaveLength(2);
    delta(wf, 5, " line");
    await flush();
    reads.land(readAfterLostDelta(wf));
    await flush();
    expect(dockTail("a")).toBe("third line");

    delta(wf, 6, "s");
    await flush();
    expect(dockTail("a")).toBe("third lines");
  });

  it("reads a run whose read is out when its ask is answered again, after that read lands", async () => {
    const wf = "wf_scope_tail_out";
    const reads = heldRunReads(wf);
    showOnlyView(runView);
    showRun(wf);
    refreshRun(wf);
    await flush();
    asks[0]?.answer("covered");
    await flush();
    delta(wf, 5, " line");
    reads.land(readWithOpenEntry(wf));
    await flush();
    expect(reads.urls).toHaveLength(2);

    reads.land(readAfterLostDelta(wf));
    await flush();
    expect(dockTail("a")).toBe("third line");
  });

  it("an ask the link lost reads after the next open asks again", async () => {
    const wf = "wf_scope_owed";
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: runRead(wf),
      error: "",
    });
    await holdOpenTurnThenHide(wf);
    vi.mocked(apiGetTyped).mockClear();
    vi.mocked(apiGetTypedOrError).mockClear();

    showOnlyView(runView);
    showRun(wf);
    asks.at(-1)?.answer("owed");
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([]);

    rescopeOwed();
    expect(asks.at(-1)?.run).toBe(wf);
    asks.at(-1)?.answer("covered");
    await flush();
    expect(runReads(wf).filter((u) => u.includes("/turns/"))).toEqual([
      `/api/runs/${wf}/turns/t-a?after=1`,
    ]);
  });
});
