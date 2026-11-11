// A streamed frame on a large run costs what it changed: the run's tree is not re-projected and no
// tree row or timeline lane is written for text that only the shown step's pane draws.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type { Entry } from "./types.js";

const m = vi.hoisted(() => ({ applied: [] as string[][] }));

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

vi.mock("./run-chat-steps.js", () => ({
  createRunStepStream: vi.fn((hostFor: (path: string) => HTMLElement) => ({
    apply: (paints: ReadonlyMap<string, readonly { turn: { body: Entry[] } }[]>) => {
      m.applied.push([...paints.keys()]);
      for (const path of paints.keys()) {
        hostFor(path);
      }
    },
    dispose: vi.fn(),
  })),
}));

vi.mock("./run-exec-source.js", { spy: true });

import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { runToExec } from "./run-exec-source.js";
import { refreshRun, showRun } from "./run-view.js";
import {
  applyRunDelta,
  appendRunEntry,
  forgetRun,
  openRunEntry,
  openRunTurn,
} from "./run-store.js";
import { showOnlyView } from "./view-swap.js";
import { openDockPanel, settledOpen } from "./__test-helpers__/exec-dock.js";

const WF = "wf_scale";
const GROUPS = 100;
const PER_GROUP = 100;
const LIVE = `${WF}:live`;
const TURN = "t-live";

/** 10,000 finished steps in 100 groups, then one running step: the shape of a long multi-lane run
 *  whose one live step streams. */
function bigRun(): unknown {
  const groups = Array.from({ length: GROUPS }, (_, g) => ({
    nodeId: `g${String(g)}`,
    type: "sequence",
    status: "completed",
    children: Array.from({ length: PER_GROUP }, (_, s) => ({
      nodeId: `s${String(s)}`,
      type: "step",
      status: "completed",
      startedAt: "2026-11-10T10:00:00Z",
      endedAt: "2026-11-10T10:01:00Z",
      children: [],
    })),
  }));
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
          ...groups,
          {
            nodeId: "live",
            type: "step",
            status: "running",
            startedAt: "2026-11-10T11:00:00Z",
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

beforeEach(() => {
  m.applied.length = 0;
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
  vi.mocked(apiGetOrError).mockResolvedValue({ ok: true, status: 200, data: bigRun(), error: "" });
  vi.mocked(apiGetTyped).mockResolvedValue(null);
  openRunTurn(WF, entry(0, "turn_open", { source: "workflow_step", node_path: LIVE, n: 1 }));
  appendRunEntry(WF, entry(1, "text", { text: "hello" }));
  openRunEntry(WF, { turn: TURN, id: `${TURN}-2`, kind: "text", text: "", n: 0, lane: "" });
});

afterEach(() => {
  document.body.replaceChildren();
});

describe("a streamed frame on a 10,000-step run", () => {
  it(
    "re-projects no tree and writes no tree row or timeline lane",
    { timeout: 300_000 },
    async () => {
      showOnlyView(runView);
      showRun(WF);
      refreshRun(WF);
      await flush();
      openDockPanel(runView);
      await settledOpen(runView);
      vi.mocked(runToExec).mockClear();
      m.applied.length = 0;
      const records: MutationRecord[] = [];
      const observer = new MutationObserver((r) => records.push(...r));
      for (const root of runView.querySelectorAll(".ev-tree, .ev-tl")) {
        observer.observe(root, {
          subtree: true,
          childList: true,
          attributes: true,
          characterData: true,
        });
      }

      for (let n = 1; n <= 200; n++) {
        applyRunDelta(WF, TURN, `${TURN}-2`, "", n, "x");
      }
      await flush();
      records.push(...observer.takeRecords());
      observer.disconnect();

      expect(vi.mocked(runToExec)).not.toHaveBeenCalled();
      expect(records).toHaveLength(0);
      expect(new Set(m.applied.flat())).toEqual(new Set([LIVE]));
    },
  );
});
