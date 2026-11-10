// A memoized fold answers what a fresh fold answers, and keeps the object of every node no input
// of which moved: a frame costs the nodes it changed.

import { describe, it, expect } from "vitest";
import { runToExec, type ExecFoldMemo } from "./run-exec-source.js";
import { nodePathKey } from "./run-node-key.js";
import { flatten, type ExecNode } from "./exec-view/model.js";
import { makeRunState } from "./__test-helpers__/model.js";
import type { RunNode, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";
import type { RunStepEnd } from "./wire/types.gen.js";

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };

const step = (nodeId: string, status: RunNode["status"]): RunNode => ({
  nodeId,
  type: "step",
  status,
  startedAt: "2026-11-10T10:00:00Z",
  ...(status === "completed" ? { endedAt: "2026-11-10T10:01:00Z" } : {}),
  children: [],
});

function lane(id: string, statuses: RunNode["status"][]): RunNode {
  return {
    nodeId: id,
    type: "sequence",
    status: statuses.includes("running") ? "running" : "completed",
    children: statuses.map((s, i) => step(`${id}-s${String(i)}`, s)),
  };
}

function stateOf(root: RunNode, status: RunState["status"] = "running"): RunState {
  return { ...makeRunState({ status }), root };
}

function tree(): RunNode {
  return {
    nodeId: "wf",
    type: "sequence",
    status: "running",
    children: [lane("a", ["completed", "completed"]), lane("b", ["completed", "running"])],
  };
}

/** `root` with lane `b`'s running step completed, copied along its spine as the store patches. */
function completeB(root: RunNode): RunNode {
  const kids = root.children ?? [];
  const b = kids[1];
  if (b === undefined) {
    throw new Error("no lane b");
  }
  const steps = b.children ?? [];
  const done = steps.with(1, {
    ...steps[1]!,
    status: "completed",
    endedAt: "2026-11-10T10:02:00Z",
  });
  return { ...root, children: kids.with(1, { ...b, status: "completed", children: done }) };
}

const byPath = (nodes: readonly ExecNode[]): Map<string, ExecNode> =>
  new Map(flatten(nodes).map((n) => [n.path, n]));

const STEP_B1 = nodePathKey(["wf", "b", "b-s1"]);
const STEP_A0 = nodePathKey(["wf", "a", "a-s0"]);

describe("a memoized fold", () => {
  it("keeps every node off the patched spine and equals a fresh fold", () => {
    const memo: ExecFoldMemo = {};
    const root = tree();
    const first = byPath(runToExec(stateOf(root), undefined, NO_ASKS, "", new Map(), memo).nodes);
    const next = completeB(root);
    const run = runToExec(stateOf(next), undefined, NO_ASKS, "", new Map(), memo);
    const second = byPath(run.nodes);

    expect(run).toEqual(runToExec(stateOf(next), undefined, NO_ASKS));
    expect(second.get(nodePathKey(["wf", "a"]))).toBe(first.get(nodePathKey(["wf", "a"])));
    expect(second.get(STEP_A0)).toBe(first.get(STEP_A0));
    expect(second.get(nodePathKey(["wf", "b", "b-s0"]))).toBe(
      first.get(nodePathKey(["wf", "b", "b-s0"])),
    );
    expect(second.get(STEP_B1)).not.toBe(first.get(STEP_B1));
    expect(second.get(STEP_B1)?.state).toBe("ok");
    expect(second.get(nodePathKey(["wf", "b"]))?.state).toBe("ok");
  });

  it("re-folds a step whose recorded grade moved, and its containers, and nothing else", () => {
    const memo: ExecFoldMemo = {};
    const root = completeB(tree());
    const first = byPath(runToExec(stateOf(root), undefined, NO_ASKS, "", new Map(), memo).nodes);
    const ends = new Map<string, RunStepEnd>([[STEP_B1, { outcome: "refused" }]]);
    const run = runToExec(stateOf(root), undefined, NO_ASKS, "", ends, memo);
    const second = byPath(run.nodes);

    expect(run).toEqual(runToExec(stateOf(root), undefined, NO_ASKS, "", ends));
    expect(second.get(STEP_B1)?.state).toBe("fail");
    expect(second.get(nodePathKey(["wf", "b"]))?.state).toBe("fail");
    expect(second.get(nodePathKey(["wf", "a"]))).toBe(first.get(nodePathKey(["wf", "a"])));
    expect(second.get(nodePathKey(["wf", "b", "b-s0"]))).toBe(
      first.get(nodePathKey(["wf", "b", "b-s0"])),
    );
  });

  it("re-folds every node when the run's own status moves", () => {
    const memo: ExecFoldMemo = {};
    const root = tree();
    const first = byPath(runToExec(stateOf(root), undefined, NO_ASKS, "", new Map(), memo).nodes);
    const run = runToExec(stateOf(root, "completed"), undefined, NO_ASKS, "", new Map(), memo);

    expect(run).toEqual(runToExec(stateOf(root, "completed"), undefined, NO_ASKS));
    expect(byPath(run.nodes).get(STEP_A0)).not.toBe(first.get(STEP_A0));
  });
});
