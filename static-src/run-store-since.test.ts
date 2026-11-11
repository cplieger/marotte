// A held run moves by what changed: a new iteration's step start reads only the changed nodes
// (`?since`), a recorded grade arrives the same way, and a surface that draws only the run's
// status reads its header, never the node tree.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type * as TabsModule from "./tabs.js";
import type * as ToastModule from "./toast.js";
import type * as NoticeSubjectModule from "./notice-subject.js";
import type * as NotifyModule from "./notify.js";
import { signal } from "@cplieger/ui";

const m = vi.hoisted(() => ({
  answers: [] as ((url: string) => unknown)[],
  /** When set, answers every read itself, in place of `answers`. */
  route: undefined as ((url: string) => unknown) | undefined,
  urls: [] as string[],
  painted: [] as { id: string; status: string }[],
  runTabs: [] as string[],
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGetTypedOrError: vi.fn(),
  apiPost: vi.fn(),
  apiGetOrError: vi.fn((url: string) => {
    m.urls.push(url);
    const answer = m.route === undefined ? m.answers.shift()?.(url) : m.route(url);
    return Promise.resolve(
      answer === undefined
        ? { ok: false, status: 0, data: null, error: "" }
        : { ok: true, status: 200, data: answer, error: "" },
    );
  }),
}));

const tabsVersion = signal(0);
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsModule>()),
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
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ToastModule>()),
  info: vi.fn(),
  jumpToast: vi.fn(),
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

await import("./handlers/run.js");
const bus = await import("./bus.js");

/** One SSE frame through the bus, as the adapter delivers it. */
function send(type: string, payload: Record<string, unknown>): void {
  bus.dispatch({ type, chat_id: "", payload } as never);
}
const {
  invalidateRun,
  registerRunStateDemand,
  rereadHeldRuns,
  runState,
  runStateAs,
  runStepEnds,
  widenRun,
} = await import("./run-store.js");
const { installRunDotSubscriber } = await import("./run-dots.js");

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
  m.route = undefined;
  m.urls.length = 0;
  m.painted.length = 0;
  m.runTabs.length = 0;
});

describe("a held run's tree moves by what changed", () => {
  registerRunStateDemand((id) => id === WF, "tree");

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

describe("a run tab's dot", () => {
  it("reads the run's header, never its tree, at boot and on a revalidation", async () => {
    const id = "wf_dot";
    m.runTabs.push(id);
    registerRunStateDemand((w) => w === id, "header");
    const own = `/api/runs/${id}`;
    m.route = (url) =>
      url.startsWith(own)
        ? {
            workflowId: id,
            rev: 3,
            display_name: "nightly",
            state: { workflowId: id, status: "running" },
          }
        : undefined;
    const reads = (): string[] => m.urls.filter((u) => u.startsWith(own));

    installRunDotSubscriber();
    await flush();
    rereadHeldRuns();
    await flush();

    expect(reads()).toEqual([`${own}?view=header`, `${own}?view=header`]);
    expect(m.painted.at(-1)).toEqual({ id: `tab-${id}`, status: "working" });

    m.urls.length = 0;
    send("run_rev", { workflow_id: id, rev: 4, status: "paused" });
    await flush();

    expect(reads()).toEqual([]);
    expect(m.painted.at(-1)?.status).not.toBe("working");
  });
});

describe("a run read in the shape its readers draw", () => {
  /** A header answer for `id` at `rev`, with the run `status`. */
  const header = (id: string, rev: number, status = "running") => ({
    workflowId: id,
    rev,
    display_name: "nightly",
    state: { workflowId: id, status },
  });
  /** A whole answer for `id` at `rev`: one step under the root. */
  const tree = (id: string, rev: number) => ({
    workflowId: id,
    rev,
    state: {
      workflowId: id,
      status: "running",
      root: {
        nodeId: id,
        type: "sequence",
        status: "running",
        children: [{ nodeId: "build", type: "step", status: "running" }],
      },
    },
  });

  it("reads a run no reader holds as its header", async () => {
    const id = "wf_nobody";
    m.route = (url) => (url.startsWith(`/api/runs/${id}`) ? header(id, 2) : undefined);

    invalidateRun(id);
    await flush();

    expect(m.urls).toEqual([`/api/runs/${id}?view=header`]);
    expect(runState(id)?.status).toBe("running");
  });

  it("reads the tree once a reader draws it, and shows none of it until then", async () => {
    const id = "wf_widen";
    let drawsTree = false;
    registerRunStateDemand((w) => w === id, "header");
    registerRunStateDemand((w) => w === id && drawsTree, "tree");
    m.route = (url) => (url.includes("view=header") ? header(id, 2) : tree(id, 3));
    invalidateRun(id);
    await flush();
    expect(runState(id)).toBeDefined();
    expect(runStateAs(id, "tree")).toBeUndefined();

    drawsTree = true;
    widenRun(id);
    await flush();

    expect(m.urls).toEqual([`/api/runs/${id}?view=header`, `/api/runs/${id}`]);
    expect(runStateAs(id, "tree")?.root?.children?.[0]?.nodeId).toBe("build");
  });

  it("follows a header read out when the tree is drawn with one read of the tree", async () => {
    const id = "wf_widen_out";
    let drawsTree = false;
    registerRunStateDemand((w) => w === id, "header");
    registerRunStateDemand((w) => w === id && drawsTree, "tree");
    m.route = (url) => (url.includes("view=header") ? header(id, 2) : tree(id, 3));

    invalidateRun(id);
    drawsTree = true;
    widenRun(id);
    widenRun(id);
    await flush();

    expect(m.urls).toEqual([`/api/runs/${id}?view=header`, `/api/runs/${id}`]);
    expect(runStateAs(id, "tree")).toBeDefined();
  });

  it("keeps a held tree moving by what changed once only its header is drawn", async () => {
    const id = "wf_hidden_tab";
    let drawsTree = true;
    registerRunStateDemand((w) => w === id, "header");
    registerRunStateDemand((w) => w === id && drawsTree, "tree");
    m.route = (url) =>
      url.includes("since=")
        ? { workflowId: id, rev: 4, head: { status: "paused" }, nodes: [], removed: [] }
        : tree(id, 3);
    invalidateRun(id);
    await flush();
    drawsTree = false;
    m.urls.length = 0;

    send("run_rev", { workflow_id: id, rev: 4, status: "paused" });
    await flush();

    expect(m.urls).toEqual([`/api/runs/${id}?since=3`]);
    expect(runStateAs(id, "tree")?.status).toBe("paused");
  });

  it("re-reads a glance on a new revision rather than asking what changed", async () => {
    const id = "wf_glance";
    registerRunStateDemand((w) => w === id, "glance");
    m.route = () => tree(id, 5);
    invalidateRun(id);
    await flush();

    send("run_rev", { workflow_id: id, rev: 5 });
    send("run_rev", { workflow_id: id, rev: 6 });
    await flush();

    expect(m.urls).toEqual([`/api/runs/${id}?view=glance`, `/api/runs/${id}?view=glance`]);
    expect(runStateAs(id, "glance")).toBeDefined();
    expect(runStateAs(id, "tree")).toBeUndefined();
  });
});
