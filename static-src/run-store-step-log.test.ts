// A step's reader of the run log is told of its own step's frames and of no other step's, and reads
// that step's turns in the order they opened.

import { describe, it, expect, afterEach } from "vitest";
import { effect } from "@cplieger/reactive";
import type { Entry } from "./types.js";
import {
  applyRunDelta,
  appendRunEntry,
  forgetRun,
  openRunEntry,
  openRunTurn,
  runStepHoles,
  runStepTurns,
} from "./run-store.js";

const WF = "wf_step_log";
const A = "wf:lane-a:build";
const B = "wf:lane-b:build";

function turnOpen(turn: string, path: string, n: number): Entry {
  return {
    id: `${turn}-0`,
    turn,
    lane: "",
    kind: "turn_open",
    seq: 0,
    ts: 0,
    payload: { source: "workflow_step", node_path: path, n },
  };
}

function text(turn: string, seq: number): Entry {
  return { id: `${turn}-${String(seq)}`, turn, lane: "", kind: "text", seq, ts: 0, payload: {} };
}

/** How many times an effect reading `path`'s turns runs while `act` writes the log. */
function passesOver(path: string, act: () => void): number {
  let passes = 0;
  const stop = effect(() => {
    runStepTurns(WF, path);
    passes++;
  });
  passes = 0;
  act();
  stop();
  return passes;
}

afterEach(() => {
  forgetRun(WF);
});

describe("a step's view of the run log", () => {
  it("runs for its own step's frames and not for another step's", () => {
    openRunTurn(WF, turnOpen("ta", A, 1));
    openRunTurn(WF, turnOpen("tb", B, 2));
    openRunEntry(WF, { turn: "tb", id: "tb-1", kind: "text", text: "", n: 0, lane: "" });

    expect(
      passesOver(A, () => {
        for (let n = 1; n <= 50; n++) {
          applyRunDelta(WF, "tb", "tb-1", "", n, "x");
        }
        appendRunEntry(WF, text("tb", 1));
      }),
    ).toBe(0);
    expect(
      passesOver(B, () => {
        appendRunEntry(WF, text("tb", 2));
      }),
    ).toBe(1);
  });

  it("holds the step's turns in their opened order, whatever order they arrive in", () => {
    openRunTurn(WF, turnOpen("t3", A, 3));
    openRunTurn(WF, turnOpen("t2", B, 2));
    openRunTurn(WF, turnOpen("t1", A, 1));

    expect(runStepTurns(WF, A).map(([id]) => id)).toEqual(["t1", "t3"]);
    expect(runStepTurns(WF, B).map(([id]) => id)).toEqual(["t2"]);
  });

  it("names a hole in its own turns, telling its reader", () => {
    openRunTurn(WF, turnOpen("ta", A, 1));
    openRunTurn(WF, turnOpen("tb", B, 2));

    expect(
      passesOver(A, () => {
        appendRunEntry(WF, text("ta", 5));
      }),
    ).toBe(1);
    expect(runStepHoles(WF, A)).toEqual(["ta"]);
    expect(runStepHoles(WF, B)).toEqual([]);
  });
});
