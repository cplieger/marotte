// A run card and the run bar draw a run's glance (each repeat through its latest pass, the passes
// before it folded into `earlierPasses`) exactly as they draw its whole tree.

import { describe, it, expect } from "vitest";
import { runToExec } from "./run-exec-source.js";
import { buildRunCard } from "./fundamentals/run-card.js";
import { buildExecSummary } from "./exec-view/summary.js";
import { makeRunState } from "./__test-helpers__/model.js";
import type { EarlierPasses, RunNode, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };

/** Pass `i` of the lane: a build and a review, each with its own minutes and credits. */
function pass(i: number, status: RunNode["status"], review: RunNode["status"]): RunNode {
  const at = (min: number): string =>
    new Date(Date.UTC(2026, 0, 1, 10, i * 10 + min)).toISOString();
  return {
    nodeId: `lane#${String(i)}`,
    type: "sequence",
    status,
    iteration: i,
    startedAt: at(0),
    children: [
      {
        nodeId: "build",
        type: "step",
        status: "completed",
        startedAt: at(0),
        endedAt: at(4),
        credits: 1 + i,
      },
      {
        nodeId: "review",
        type: "step",
        status: review,
        startedAt: at(5),
        endedAt: at(8),
        credits: 0.5,
        ...(review === "failed" ? { failureReason: "the review found a regression" } : {}),
      },
    ],
  };
}

/** A settled run whose lane repeat ran four passes (the second's review failed), as its whole
 *  tree and as its glance. */
function shapes(
  runStatus: NonNullable<RunState["status"]>,
  lastReview: RunNode["status"],
): [RunState, RunState] {
  const passes = [
    pass(0, "completed", "completed"),
    pass(1, "failed", "failed"),
    pass(2, "completed", "completed"),
    pass(3, lastReview === "failed" ? "failed" : "completed", lastReview),
  ];
  const lane = (children: RunNode[], earlierPasses?: EarlierPasses): RunNode => ({
    nodeId: "lane",
    type: "repeat",
    status: lastReview === "failed" ? "failed" : "completed",
    children,
    ...(earlierPasses === undefined ? {} : { earlierPasses }),
  });
  const state = (root: RunNode): RunState => ({
    ...makeRunState({ status: runStatus }),
    displayName: "lanes",
    root: { nodeId: "wf_1", type: "sequence", status: "completed", children: [root] },
  });
  // What the server folds the first three passes into: their count, the credits of their six
  // steps, and the window from the first build's start to the third review's end.
  const folded: EarlierPasses = {
    count: 3,
    credits: 1 + 2 + 3 + 3 * 0.5,
    from: Date.UTC(2026, 0, 1, 10, 0),
    to: Date.UTC(2026, 0, 1, 10, 28),
    open: false,
  };
  return [state(lane(passes)), state(lane(passes.slice(-1), folded))];
}

function card(state: RunState): string {
  const view = buildRunCard("wf_1", "lanes", () => undefined);
  view.render(state, NO_ASKS, { plan: undefined, ends: new Map() });
  return view.root.textContent;
}

function summary(state: RunState): { text: string; described: string } {
  const view = buildExecSummary({ figure: (c) => c.toFixed(2) });
  view.paint(runToExec(state, undefined, NO_ASKS));
  return { text: view.root.textContent, described: view.describe() };
}

describe("a run's glance", () => {
  for (const [name, runStatus, lastReview] of [
    ["a completed run", "completed", "completed"],
    ["a run whose latest pass failed", "failed", "failed"],
  ] as const) {
    it(`draws ${name}'s card and run bar summary as its whole tree does`, () => {
      const [whole, glance] = shapes(runStatus, lastReview);
      expect(card(glance)).toBe(card(whole));
      expect(summary(glance)).toEqual(summary(whole));
    });
  }

  it("counts the folded passes' spend and time in the run's figures", () => {
    const [, glance] = shapes("completed", "completed");
    const run = runToExec(glance, undefined, NO_ASKS);
    expect(run.credits).toBe(1 + 2 + 3 + 4 + 4 * 0.5);
    // 10:00 to 10:38, the last review's end.
    expect(summary(glance).text).toContain("38m");
  });
});
