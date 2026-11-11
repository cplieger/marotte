// A held run moves by what changed: a new iteration's step start reads only the changed nodes
// (`?since`), and a recorded grade arrives the same way.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { signal } from "@cplieger/ui";

const m = vi.hoisted(() => ({
  answers: [] as ((url: string) => unknown)[],
  urls: [] as string[],
  painted: [] as { id: string; status: string }[],
  runTabs: [] as string[],
}));

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGetTypedOrError: vi.fn(),
  apiPost: vi.fn(),
  apiGetOrError: vi.fn((url: string) => {
    m.urls.push(url);
    const answer = m.answers.shift()?.(url);
    return Promise.resolve(
      answer === undefined
        ? { ok: false, status: 0, data: null, error: "" }
        : { ok: true, status: 200, data: answer, error: "" },
    );
  }),
}));

const tabsVersion = signal(0);
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  tabIdFor: vi.fn((_kind: string, ref: string) => (m.runTabs.includes(ref) ? `tab-${ref}` : "")),
  hasTab: vi.fn((kind: string, ref: string) => kind === "run" && m.runTabs.includes(ref)),
  tabSetVersion: vi.fn(() => tabsVersion.value),
  openRunRefs: vi.fn(() => {
    void tabsVersion.value;
    return [...m.runTabs];
  }),
  setTabStatus: vi.fn((id: string, status: string) => {
    m.painted.push({ id, status });
  }),
  renameTab: vi.fn(),
}));
vi.mock("./toast.js", () => ({ info: vi.fn(), jumpToast: vi.fn() }));
vi.mock("./notice-subject.js", () => ({ runEndJump: vi.fn(() => undefined) }));
vi.mock("./notify.js", () => ({
  notifyOffScreen: vi.fn(),
  closeRunAsksExcept: vi.fn(() => Promise.resolve()),
}));

await import("./handlers/run.js");
const bus = await import("./bus.js");

/** One SSE frame through the bus, as the adapter delivers it. */
function send(type: string, payload: Record<string, unknown>): void {
  bus.dispatch({ type, chat_id: "", payload } as never);
}
const { invalidateRun, registerRunStateDemand, runState, runStepEnds } =
  await import("./run-store.js");

const WF = "wf_lanes";

/** A run of one lane whose repeat holds iteration 0 of an inner loop with one step. */
function lanes(): unknown {
  return {
    workflowId: WF,
    rev: 7,
    display_name: "lanes",
    state: {
      workflowId: WF,
      status: "running",
      root: {
        nodeId: WF,
        type: "parallel",
        status: "running",
        children: [
          {
            nodeId: "lane-a",
            type: "repeat",
            status: "running",
            children: [
              {
                nodeId: "lane-a#0",
                type: "sequence",
                status: "running",
                iteration: 0,
                children: [{ nodeId: "build", type: "step", status: "running", children: [] }],
              },
            ],
          },
        ],
      },
    },
  };
}

async function flush(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve();
  }
}

/** Every path the held tree holds, as the run's node paths spell them. */
function heldNodeIds(): string[] {
  const out: string[] = [];
  const walk = (n: { nodeId: string; children?: unknown[] } | undefined): void => {
    if (n === undefined) {
      return;
    }
    out.push(n.nodeId);
    for (const c of n.children ?? []) {
      walk(c as { nodeId: string; children?: unknown[] });
    }
  };
  walk(runState(WF)?.root as { nodeId: string; children?: unknown[] } | undefined);
  return out;
}

beforeEach(() => {
  m.answers.length = 0;
  m.urls.length = 0;
  m.painted.length = 0;
  m.runTabs.length = 0;
});

describe("a held run's tree moves by what changed", () => {
  registerRunStateDemand((id) => id === WF);

  it("reads only the changed nodes when a new iteration's step starts", async () => {
    m.answers.push(() => lanes());
    invalidateRun(WF);
    await flush();
    m.urls.length = 0;

    send("run_progress", {
      workflow_id: WF,
      kind: "node_start",
      node_path: [WF, "lane-a", "iter-1", "build"],
    });
    m.answers.push(() => ({
      workflowId: WF,
      rev: 8,
      nodes: [
        {
          path: WF,
          parent: "",
          index: 0,
          node: { nodeId: WF, type: "parallel", status: "running" },
        },
        {
          path: `${WF}:lane-a`,
          parent: WF,
          index: 0,
          node: { nodeId: "lane-a", type: "repeat", status: "running" },
        },
        {
          path: `${WF}:lane-a:iter-1`,
          parent: `${WF}:lane-a`,
          index: 1,
          node: { nodeId: "lane-a#1", type: "sequence", status: "running", iteration: 1 },
        },
        {
          path: `${WF}:lane-a:iter-1:build`,
          parent: `${WF}:lane-a:iter-1`,
          index: 0,
          node: { nodeId: "build", type: "step", status: "running" },
        },
      ],
      removed: [],
    }));
    send("run_rev", { workflow_id: WF, rev: 8 });
    await flush();

    expect(m.urls).toEqual([`/api/runs/${WF}?since=7`]);
    expect(heldNodeIds()).toEqual([WF, "lane-a", "lane-a#0", "build", "lane-a#1", "build"]);
  });

  it("keeps every unchanged node by identity and drops a removed one", async () => {
    m.answers.push(() => lanes());
    invalidateRun(WF);
    await flush();
    const before = runState(WF)?.root;
    const lane = before?.children?.[0];
    m.answers.push(() => ({
      workflowId: WF,
      rev: 10,
      head: { status: "paused" },
      nodes: [
        {
          path: WF,
          parent: "",
          index: 0,
          node: { nodeId: WF, type: "parallel", status: "running" },
        },
        {
          path: `${WF}:lane-b`,
          parent: WF,
          index: 0,
          node: { nodeId: "lane-b", type: "step", status: "pending" },
        },
      ],
      removed: [],
    }));
    send("run_rev", { workflow_id: WF, rev: 10 });
    await flush();

    const after = runState(WF);
    expect(after?.status).toBe("paused");
    expect(after?.root?.children?.map((k) => k.nodeId)).toEqual(["lane-b", "lane-a"]);
    expect(after?.root?.children?.[1]).toBe(lane);

    m.answers.push(() => ({
      workflowId: WF,
      rev: 11,
      nodes: [
        {
          path: WF,
          parent: "",
          index: 0,
          node: { nodeId: WF, type: "parallel", status: "running" },
        },
      ],
      removed: [`${WF}:lane-b`],
    }));
    send("run_rev", { workflow_id: WF, rev: 11 });
    await flush();

    expect(runState(WF)?.root?.children?.map((k) => k.nodeId)).toEqual(["lane-a"]);
    expect(runState(WF)?.status).toBe("paused");
  });

  it("applies a whole answer to a revision the server no longer holds", async () => {
    m.answers.push(() => lanes());
    invalidateRun(WF);
    await flush();
    m.urls.length = 0;
    m.answers.push(() => ({ ...(lanes() as object), rev: 30 }));

    send("run_rev", { workflow_id: WF, rev: 30 });
    await flush();
    send("run_rev", { workflow_id: WF, rev: 30 });
    await flush();

    expect(m.urls).toEqual([`/api/runs/${WF}?since=7`]);
    expect(heldNodeIds()).toEqual([WF, "lane-a", "lane-a#0", "build"]);
  });

  it("carries a recorded grade with no read of the whole run", async () => {
    m.answers.push(() => lanes());
    invalidateRun(WF);
    await flush();
    m.urls.length = 0;
    const build = `${WF}:lane-a:iter-0:build`;

    send("entry_appended", {
      workflow_id: WF,
      entry: { id: "g1", turn: "t-b", kind: "step_graded", seq: 9, ts: 1, payload: {} },
    });
    m.answers.push(() => ({
      workflowId: WF,
      rev: 9,
      nodes: [],
      removed: [],
      step_ends: { [build]: { outcome: "failed", failure_kind: "empty_answer" } },
    }));
    send("run_rev", { workflow_id: WF, rev: 9 });
    await flush();

    expect(m.urls).toEqual([`/api/runs/${WF}?since=7`]);
    expect(runStepEnds(WF).get(build)?.outcome).toBe("failed");
  });
});
