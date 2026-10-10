import { describe, it, expect, beforeEach, vi } from "vitest";
import type * as ApiClient from "./api-client.js";
import type { RunNode, RunProgressFrame, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";
import { workNodes, type ExecNode } from "./exec-view/model.js";

let responses: RunState[] = [];
let resolvers: (() => void)[] = [];

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetOrError: vi.fn(async () => {
    await new Promise<void>((r) => resolvers.push(r));
    const state = responses.shift();
    return state === undefined
      ? { ok: false, status: 502, data: null, error: "" }
      : { ok: true, status: 200, data: { workflowId: state.workflowId, state }, error: "" };
  }),
  apiGetTyped: vi.fn(async () => null),
}));

const store = await import("./run-store.js");
const { runToExec } = await import("./run-exec-source.js");
const { composerMode } = await import("./run-composer.js");

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };
const T0 = Date.parse("2026-10-08T10:00:00Z");
const at = (ms: number): string => new Date(T0 + ms).toISOString();

type Leaf = Pick<RunNode, "status" | "startedAt" | "sessionId">;

function tree(build: Leaf): RunState {
  return {
    workflowId: "wf",
    status: "running",
    root: {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [{ nodeId: "build", type: "step", ...build }],
    },
  };
}

async function answerReads(): Promise<void> {
  while (resolvers.length > 0) {
    for (const r of resolvers.splice(0)) {
      r();
    }
    // A task boundary, so every continuation of the read, a trailing one included, has run.
    await new Promise((r) => setTimeout(r, 0));
  }
}

function land(f: Omit<RunProgressFrame, "workflow_id">): void {
  expect(store.applyRunProgress({ workflow_id: "wf", ...f }), "the frame lands").toBe(true);
}

async function raceRead(
  raced: RunState,
  during: Omit<RunProgressFrame, "workflow_id">,
  reread: RunState,
): Promise<void> {
  responses.push(raced);
  store.invalidateRun("wf");
  land(during);
  responses.push(reread);
  await answerReads();
}

function buildMode(): ReturnType<typeof composerMode> {
  const state = store.peekRunState("wf");
  expect(state?.status, "the run is cached and running").toBe("running");
  const s = state as RunState;
  const step: ExecNode | undefined = workNodes(runToExec("wf", s, undefined, NO_ASKS).nodes)[0];
  return composerMode({ workflowID: "wf", runLive: true, runPaused: false, step });
}

beforeEach(async () => {
  responses = [];
  resolvers = [];
  store.forgetRun("wf");
  responses.push(tree({ status: "pending" }));
  store.invalidateRun("wf");
  await answerReads();
});

describe("a read issued before a frame cannot undo it", () => {
  it("keeps the session a second node_start bound while an older read was out", async () => {
    land({ node_path: ["wf", "build"], status: "running", started_at: at(100) });
    await raceRead(
      tree({ status: "running", startedAt: at(100) }),
      { node_path: ["wf", "build"], status: "running", started_at: at(101), session_id: "sess-a" },
      tree({ status: "running", startedAt: at(100), sessionId: "sess-a" }),
    );
    expect(buildMode()).toMatchObject({ kind: "message", verb: "steer" });
  });

  it("keeps a step's pause that landed while an older read was out", async () => {
    land({
      node_path: ["wf", "build"],
      status: "running",
      started_at: at(100),
      session_id: "sess-a",
    });
    await raceRead(
      tree({ status: "running", startedAt: at(100), sessionId: "sess-a" }),
      { node_path: ["wf", "build"], status: "paused" },
      tree({ status: "paused", startedAt: at(100), sessionId: "sess-a" }),
    );
    expect(buildMode()).toMatchObject({ kind: "disabled" });
  });

  it("takes the answer of the read issued after the frame", async () => {
    land({
      node_path: ["wf", "build"],
      status: "running",
      started_at: at(100),
      session_id: "sess-a",
    });
    await raceRead(
      tree({ status: "running", startedAt: at(100), sessionId: "sess-a" }),
      { node_path: ["wf", "build"], status: "paused" },
      tree({ status: "running", startedAt: at(100), sessionId: "sess-a" }),
    );
    expect(responses, "the re-read was taken").toHaveLength(0);
    expect(buildMode(), "the newer read says it is running again").toMatchObject({
      kind: "message",
      verb: "steer",
    });
  });
});
