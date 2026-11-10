// The run view paints only while its tab is the visible view: a hidden run tab does no work for the
// run's frames or its clock, and showing it again catches the shown step up before one paint.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type * as ExecPage from "./exec-view/page.js";
import type { Entry } from "./types.js";

const m = vi.hoisted(() => ({ renders: 0, applied: [] as string[][] }));

vi.mock("./api-client.js", () => ({
  apiBlobOrError: vi.fn(),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
  runPendingAsks: vi.fn(() => ({ count: 0, asked: [], label: "" })),
}));

// The step transcript's renderer is out of scope here; a stand-in that writes into its host keeps
// a paint's DOM work visible to the observer below.
vi.mock("./run-chat-steps.js", () => ({
  createRunStepStream: vi.fn((hostFor: (path: string) => HTMLElement) => ({
    apply: (paints: ReadonlyMap<string, readonly { turn: { body: Entry[] } }[]>) => {
      m.applied.push([...paints.keys()]);
      for (const [path, list] of paints) {
        const p = document.createElement("p");
        p.textContent = String(list.reduce((n, x) => n + x.turn.body.length, 0));
        hostFor(path).replaceChildren(p);
      }
    },
    dispose: vi.fn(),
  })),
}));

// Counts the page's paints: the one observable that separates "the view drew the run" from any
// other write the module may make.
vi.mock("./exec-view/page.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ExecPage>();
  return {
    ...actual,
    buildExecPage: (opts: Parameters<typeof actual.buildExecPage>[0]) => {
      const view = actual.buildExecPage(opts);
      const render = view.render.bind(view);
      return {
        ...view,
        render: (run: Parameters<typeof render>[0]) => {
          m.renders++;
          render(run);
        },
      };
    },
  };
});

import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { refreshRun, showRun } from "./run-view.js";
import {
  applyRunDelta,
  appendRunEntry,
  forgetRun,
  openRunEntry,
  openRunTurn,
} from "./run-store.js";
import { showOnlyView } from "./view-swap.js";

const WF = "wf_shown";
const STEP = `${WF}:coder`;
const TURN = "t-coder";

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
          {
            nodeId: "coder",
            type: "step",
            status: "running",
            startedAt: "2026-11-10T10:00:00Z",
            children: [],
          },
        ],
      },
    },
  };
}

function entry(seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${TURN}-${String(seq)}`, turn: TURN, lane: "", kind, payload, seq, ts: 0 };
}

async function flush(): Promise<void> {
  await vi.dynamicImportSettled();
  for (let i = 0; i < 20; i++) {
    await Promise.resolve();
  }
}

let runView: HTMLElement;
let chatView: HTMLElement;

function mountViews(): void {
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
  chatView = document.createElement("div");
  chatView.id = "chat-view";
  chatView.dataset["tabView"] = "";
  document.body.append(runView, chatView);
}

/** The run tab activating: the view swap shows its view, then its `onShow` and `refresh` run. */
async function activateRunTab(): Promise<void> {
  showOnlyView(runView);
  showRun(WF);
  refreshRun(WF);
  await flush();
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout"] });
  m.renders = 0;
  m.applied.length = 0;
  forgetRun(WF);
  document.body.replaceChildren();
  mountViews();
  vi.mocked(apiGetOrError).mockResolvedValue({ ok: true, status: 200, data: runRead(), error: "" });
  vi.mocked(apiGetTyped).mockResolvedValue(null);
  openRunTurn(WF, entry(0, "turn_open", { source: "workflow_step", node_path: STEP, n: 1 }));
  appendRunEntry(WF, entry(1, "text", { text: "hello" }));
  openRunEntry(WF, { turn: TURN, id: `${TURN}-2`, kind: "text", text: "", n: 0, lane: "" });
});

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("a hidden run tab", () => {
  it("paints nothing for the run's frames or its clock", async () => {
    await activateRunTab();
    expect(runView.querySelector(".ev-page")).not.toBeNull();
    expect(m.renders).toBeGreaterThan(0);

    showOnlyView(chatView);
    const before = m.renders;
    const records: MutationRecord[] = [];
    const observer = new MutationObserver((r) => records.push(...r));
    observer.observe(runView, {
      subtree: true,
      childList: true,
      attributes: true,
      characterData: true,
    });
    for (let n = 1; n <= 100; n++) {
      applyRunDelta(WF, TURN, `${TURN}-2`, "", n, "x");
    }
    await vi.advanceTimersByTimeAsync(5000);
    records.push(...observer.takeRecords());
    observer.disconnect();

    expect(m.renders - before).toBe(0);
    expect(records).toHaveLength(0);
  });

  it("catches the shown step up with one range read, then paints once", async () => {
    await activateRunTab();
    showOnlyView(chatView);
    for (let n = 1; n <= 20; n++) {
      applyRunDelta(WF, TURN, `${TURN}-2`, "", n, "x");
    }
    vi.mocked(apiGetTyped).mockClear();
    let answer: (v: unknown) => void = () => undefined;
    vi.mocked(apiGetTyped).mockImplementation(
      (url: string) =>
        new Promise((resolve) => {
          if (url.includes("/turns/")) {
            answer = resolve;
          } else {
            resolve(null);
          }
        }),
    );

    showOnlyView(runView);
    showRun(WF);
    await flush();
    const ranges = vi
      .mocked(apiGetTyped)
      .mock.calls.map(([url]) => url)
      .filter((u) => u.includes("/turns/"));
    expect(ranges).toEqual([`/api/runs/${WF}/turns/${TURN}?after=1`]);

    const before = m.renders;
    answer({
      entries: [],
      open_entries: [
        { turn: TURN, id: `${TURN}-2`, kind: "text", text: "x".repeat(20), n: 20, lane: "" },
      ],
    });
    await flush();
    expect(m.renders - before).toBe(1);
  });
});
