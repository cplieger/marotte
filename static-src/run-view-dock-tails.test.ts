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

const WF = "wf_tails";
const A = `${WF}:a`;
const B = `${WF}:b`;

function runRead(): unknown {
  return {
    workflowId: WF,
    state: {
      workflowId: WF,
      status: "running",
      root: {
        nodeId: WF,
        type: "sequence",
        status: "running",
        children: [
          { nodeId: "a", type: "step", status: "running", startedAt: "2026-11-10T10:00:00Z" },
          { nodeId: "b", type: "step", status: "running", startedAt: "2026-11-10T10:01:00Z" },
        ],
      },
    },
    step_starts: {
      [A]: { started_at: "2026-11-10T10:00:00Z", ended: false, turn: "t-a", tail: "a was here" },
      [B]: { started_at: "2026-11-10T10:01:00Z", ended: false, turn: "t-b", tail: "b says" },
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
