// Opening a run tab reads what the tab draws closed: the run's glance (each repeat's latest pass).
// The whole tree is read when the reader opens the panel that lists every step.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as TabsModule from "./tabs.js";
import type * as DecisionDockModule from "./decision-dock.js";

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
}));

vi.mock("./run-chat-steps.js");

import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { openRunView, refreshRun, showRun } from "./run-view.js";
import { forgetRun } from "./run-store.js";
import { showOnlyView } from "./view-swap.js";
import { openDockPanel, settledOpen } from "./__test-helpers__/exec-dock.js";

const WF = "wf_open_shape";
const PASSES = 50;

function step(id: string, status: string): unknown {
  return {
    nodeId: id,
    type: "step",
    status,
    startedAt: "2026-11-10T10:00:00Z",
    ...(status === "running" ? {} : { endedAt: "2026-11-10T10:01:00Z" }),
    children: [],
  };
}

/** A repeat of PASSES passes of build then review, the last pass building. */
function pass(i: number): unknown {
  const last = i === PASSES - 1;
  return {
    nodeId: `lane#${String(i)}`,
    type: "sequence",
    status: last ? "running" : "completed",
    children: [
      step("build", last ? "running" : "completed"),
      step("review", last ? "pending" : "completed"),
    ],
  };
}

function runOf(passes: unknown[], earlier?: unknown): unknown {
  return {
    workflowId: WF,
    rev: 7,
    state: {
      workflowId: WF,
      status: "running",
      root: {
        nodeId: WF,
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "lane",
            type: "repeat",
            status: "running",
            ...(earlier === undefined ? {} : { earlierPasses: earlier }),
            children: passes,
          },
        ],
      },
    },
  };
}

const tree = runOf(Array.from({ length: PASSES }, (_, i) => pass(i)));
const glance = runOf([pass(PASSES - 1)], {
  count: PASSES - 1,
  credits: 0,
  from: "2026-11-10T10:00:00Z",
  to: "2026-11-10T10:01:00Z",
  open: false,
});

let runView: HTMLElement;
const urls: string[] = [];

async function flush(): Promise<void> {
  await vi.dynamicImportSettled();
  for (let i = 0; i < 20; i++) {
    await Promise.resolve();
  }
}

/** The reads of the run's state the page made, by shape. */
function reads(): { glance: number; tree: number } {
  const own = urls.filter((u) => u.startsWith(`/api/runs/${WF}`) && !u.includes("/steps/"));
  return {
    glance: own.filter((u) => u.includes("view=glance")).length,
    tree: own.filter((u) => !u.includes("view=")).length,
  };
}

beforeEach(() => {
  forgetRun(WF);
  urls.length = 0;
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
  vi.mocked(apiGetOrError).mockImplementation((url: string) => {
    urls.push(url);
    const data = url.includes("view=glance") ? glance : tree;
    return Promise.resolve({ ok: true, status: 200, data, error: "" } as never);
  });
  vi.mocked(apiGetTyped).mockResolvedValue(null);
});

afterEach(() => {
  showRun("wf_none");
  forgetRun(WF);
  document.body.replaceChildren();
});

describe("opening a run tab", () => {
  it("reads the run's glance and paints the latest pass's step", async () => {
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();

    expect(reads()).toEqual({ glance: 1, tree: 0 });
    const rows = [...runView.querySelectorAll(".ev-dock-label")].map((n) => n.textContent);
    expect(rows.join(" ")).toContain("build");
  });

  it("reads the whole tree when the reader opens the steps panel, and lists every pass", async () => {
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();

    openDockPanel(runView);
    await flush();
    await settledOpen(runView);

    expect(reads()).toEqual({ glance: 1, tree: 1 });
    const text = runView.querySelector(".ev-dock-body")?.textContent ?? "";
    expect(text).toContain("pass 2");
  });

  it("lists no part of the run while the whole tree is still being read", async () => {
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();
    const answers: ((v: unknown) => void)[] = [];
    vi.mocked(apiGetOrError).mockImplementation((url: string) => {
      urls.push(url);
      return new Promise((resolve) => {
        answers.push(resolve as (v: unknown) => void);
      }) as never;
    });

    openDockPanel(runView);
    await flush();

    const steps = runView.querySelector(".ev-dock-body .ev-tree");
    const note = runView.querySelector<HTMLElement>(".ev-dock-body > .list-empty");
    expect(steps?.textContent).not.toContain("pass");
    expect(note?.hidden).toBe(false);
    for (const answer of answers) {
      answer({ ok: true, status: 200, data: tree, error: "" });
    }
    await flush();
    await settledOpen(runView);
    expect(steps?.textContent).toContain("pass 2");
    expect(note?.hidden).toBe(true);
  });

  it("lists the steps at once on the panel's open when the glance left no pass out", async () => {
    const single = runOf([pass(PASSES - 1)]);
    vi.mocked(apiGetOrError).mockImplementation((url: string) => {
      urls.push(url);
      return Promise.resolve({ ok: true, status: 200, data: single, error: "" } as never);
    });
    showOnlyView(runView);
    showRun(WF);
    refreshRun(WF);
    await flush();

    const answers: ((v: unknown) => void)[] = [];
    vi.mocked(apiGetOrError).mockImplementation((url: string) => {
      urls.push(url);
      return new Promise((resolve) => {
        answers.push(resolve as (v: unknown) => void);
      }) as never;
    });
    openDockPanel(runView);
    await flush();

    expect(runView.querySelector(".ev-dock-body .ev-tree")?.textContent).toContain("build");
    expect(runView.querySelector<HTMLElement>(".ev-dock-body > .list-empty")?.hidden).toBe(true);
    for (const answer of answers) {
      answer({ ok: true, status: 200, data: single, error: "" });
    }
    await flush();
  });

  it("reads the whole tree when a door names a step of an earlier pass", async () => {
    showOnlyView(runView);
    await openRunView(WF, "run", "", `${WF}/lane/lane#3/build`);
    showRun(WF);
    refreshRun(WF);
    await flush();

    expect(reads().tree).toBe(1);
  });
});
