// The run read carries a step's capture summary, not its whole output: the collapsed rows draw
// the summary, and a watch keeps its structured record, so every row reads as it did.

import { describe, it, expect } from "vitest";
import { runToExec } from "./run-exec-source.js";
import { makeRunState } from "./__test-helpers__/model.js";
import type { RunNode, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };

function stateWith(children: unknown[]): RunState {
  return {
    ...makeRunState({ status: "running" }),
    root: { nodeId: "wf_1", type: "sequence", status: "running", children } as unknown as RunNode,
  };
}

describe("a step in the summary shape", () => {
  it("carries its capture summary as the row's capture line, its output left to the capture read", () => {
    const run = runToExec(
      stateWith([
        {
          nodeId: "build",
          type: "step",
          status: "completed",
          children: [],
          hasCapture: true,
          captureSummary: "Built 12 packages.\nAll tests passed.",
        },
      ]),
      undefined,
      NO_ASKS,
    );

    expect(run.nodes[0]?.capture).toBe("Built 12 packages.\nAll tests passed.");
    expect(run.nodes[0]?.output).toBeUndefined();
    expect(run.nodes[0]?.deferredOutput).toBe(true);
  });
});
