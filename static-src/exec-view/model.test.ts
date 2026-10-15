// The exec model's derived reads: what counts as work, the step counter, the failure an alert
// names and the clocks, which the run card, the run bar and the run page all read.
import { describe, it, expect } from "vitest";
import {
  counters,
  elapsed,
  failureOwner,
  progressText,
  summaryProgress,
  window as execWindow,
  workNodes,
  type ExecNode,
  type ProgressSource,
} from "./model.js";
import type { ExecState } from "./status.js";

function work(label: string, state: ExecState, over: Partial<ExecNode> = {}): ExecNode {
  return { path: label, label, kind: "step", state, children: [], ...over };
}

function group(label: string, kind: ExecNode["kind"], children: ExecNode[]): ExecNode {
  return { path: label, label, kind, state: "running", children };
}

describe("workNodes decides work by kind, never by shape", () => {
  it("skips a container KAS has not expanded, though it has no children", () => {
    const nodes = [work("a", "running"), group("fan", "parallel", []), group("loop", "repeat", [])];
    expect(workNodes(nodes).map((n) => n.label)).toEqual(["a"]);
  });

  it("keeps a watch, which polls and is work", () => {
    const nodes = [group("seq", "sequence", [work("w", "pending", { kind: "watch" })])];
    expect(workNodes(nodes).map((n) => n.label)).toEqual(["w"]);
  });
});

describe("counters answers the header's step counter", () => {
  it("names the RUNNING position, not done + 1", () => {
    // A skipped step would shift a `done + 1` counter, and a parallel has several in flight.
    const c = counters([
      work("a", "ok"),
      work("b", "skipped"),
      work("c", "running"),
      work("d", "pending"),
      work("e", "pending"),
    ]);
    expect(c).toEqual({ total: 5, done: 2, failed: 0, current: 3 });
  });

  it("counts a paused or unknown step as the current one: it is where the run is", () => {
    expect(counters([work("a", "ok"), work("b", "waiting")]).current).toBe(2);
    expect(counters([work("a", "unknown")])).toEqual({ total: 1, done: 0, failed: 0, current: 1 });
  });

  it("counts a failure, and not a stop, as failed", () => {
    const c = counters([work("a", "ok"), work("b", "fail"), work("c", "warn")]);
    expect(c).toEqual({ total: 3, done: 1, failed: 1, current: 0 });
  });

  it("counts a repeat through its latest pass only", () => {
    const pass = (n: number, state: ExecState): ExecNode => ({
      ...group(`pass ${String(n)}`, "sequence", [work(`code${String(n)}`, state)]),
      pass: n,
    });
    const c = counters([group("loop", "repeat", [pass(1, "ok"), pass(2, "running")])]);
    expect(c).toEqual({ total: 1, done: 0, failed: 0, current: 1 });
  });

  it("is all zeros with nothing to count", () => {
    expect(counters([])).toEqual({ total: 0, done: 0, failed: 0, current: 0 });
  });
});

describe("progressText states the loop pass the run stands in", () => {
  const running = (nodes: ExecNode[]): ProgressSource => ({ nodes, state: "running" });
  const passOf = (n: number, kids: ExecNode[]): ExecNode => ({
    ...group(`pass ${String(n)}`, "sequence", kids),
    path: `loop#${String(n)}`,
    pass: n,
  });
  const loop = (passes: ExecNode[], over: Partial<ExecNode> = {}): ExecNode => ({
    ...group("loop", "repeat", passes),
    ...over,
  });
  const earlier = (n: number): ExecNode =>
    passOf(n, [work(`code${String(n)}`, "ok"), work(`review${String(n)}`, "ok")]);
  // The fixture's third pass: 1 done, the 2nd step running, 2 more in the pass and `land` after it.
  const third = (): ExecNode =>
    passOf(3, [
      work("code", "ok"),
      work("review", "running"),
      work("fix", "pending"),
      work("verify", "pending"),
    ]);

  it("prefixes the pass and its budget", () => {
    const nodes = [
      loop([earlier(1), earlier(2), third()], { maxPasses: 8 }),
      work("land", "pending"),
    ];
    expect(progressText(running(nodes))).toBe("pass 3 of 8 \u00b7 step 2 of 5");
    expect(summaryProgress(running(nodes))).toEqual({
      pass: "pass 3 of 8",
      steps: "step 2 of 5",
      long: "pass 3 of 8 \u00b7 step 2 of 5",
      short: "3/8 \u00b7 2 of 5",
    });
  });

  it("names the pass alone for a loop with no bound", () => {
    const nodes = [loop([earlier(1), earlier(2), third()]), work("land", "pending")];
    expect(progressText(running(nodes))).toBe("pass 3 \u00b7 step 2 of 5");
    expect(summaryProgress(running(nodes)).short).toBe("3 \u00b7 2 of 5");
  });

  it("shows no pass once the step in flight is outside the loop", () => {
    const done = passOf(3, [work("code", "ok"), work("review", "ok")]);
    const nodes = [loop([earlier(1), earlier(2), done], { maxPasses: 8 }), work("land", "running")];
    expect(progressText(running(nodes))).toBe("step 3 of 3");
    expect(summaryProgress(running(nodes)).short).toBe("");
  });

  it("shows the innermost of two nested loops", () => {
    const inner = loop([passOf(2, [work("try", "running")])], { path: "inner", maxPasses: 4 });
    const outer = loop([passOf(5, [inner, work("check", "pending")])], { maxPasses: 6 });
    expect(progressText(running([outer]))).toBe("pass 2 of 4 \u00b7 step 1 of 2");
  });

  it("follows the first step in flight across a parallel holding two loops", () => {
    const a = loop([passOf(1, [work("a1", "ok")]), passOf(2, [work("a2", "running")])], {
      path: "a",
      maxPasses: 3,
    });
    const b = loop([passOf(4, [work("b4", "running")])], { path: "b", maxPasses: 9 });
    expect(progressText(running([group("fan", "parallel", [a, b])]))).toBe(
      "pass 2 of 3 \u00b7 step 1 of 2",
    );
  });

  it("follows the newest started step when none is in flight", () => {
    const at = (min: number): Partial<ExecNode> => ({ startGen: min });
    const paused = passOf(2, [work("code", "ok", at(3)), work("review", "pending")]);
    const nodes = [
      work("plan", "ok", at(1)),
      loop([passOf(1, [work("code1", "ok", at(2))]), paused], { maxPasses: 4 }),
    ];
    expect(progressText(running(nodes))).toBe("pass 2 of 4 \u00b7 2 of 3");
  });

  it("shows no pass once every step has settled", () => {
    const last = passOf(2, [
      work("code", "ok", { startGen: 1 }),
      work("review", "ok", { startGen: 2 }),
    ]);
    expect(progressText(running([loop([earlier(1), last], { maxPasses: 2 })]))).toBe("2 of 2");
  });

  for (const state of ["fail", "warn"] as const) {
    it(`shows no pass for a run that ended (${state}) with steps still pending after its loop`, () => {
      const ended = passOf(2, [
        work("code", "ok", { startGen: 1 }),
        work("review", state, { startGen: 2 }),
      ]);
      const run: ProgressSource = {
        nodes: [loop([earlier(1), ended], { maxPasses: 4 }), work("land", "pending")],
        state,
      };
      expect(progressText(run)).toBe("1 of 3");
      expect(summaryProgress(run).short).toBe("");
    });
  }

  it("keeps the pass of a one-step loop, whose step count the summary withholds", () => {
    const nodes = [loop([earlier(1), passOf(2, [work("code", "running")])], { maxPasses: 5 })];
    expect(summaryProgress(running(nodes))).toEqual({
      pass: "pass 2 of 5",
      steps: "",
      long: "pass 2 of 5",
      short: "2/5",
    });
  });
});

describe("failureOwner names what a failed run's alert says", () => {
  const failed = (label: string, kind: ExecNode["kind"], children: ExecNode[] = []): ExecNode => ({
    ...group(label, kind, children),
    state: "fail",
    failure: `${label} broke`,
  });

  it("prefers the failed step over the container it failed", () => {
    const step = work("a", "fail", { failure: "a broke" });
    expect(failureOwner([failed("fan", "parallel", [step])])?.label).toBe("a");
  });

  it("names a failed container when no step in it carries a reason", () => {
    expect(failureOwner([failed("fan", "parallel", [work("a", "ok")])])?.label).toBe("fan");
  });

  it("ignores a failure without a reason", () => {
    expect(failureOwner([work("a", "fail"), work("b", "fail")])).toBeUndefined();
  });
});

describe("the clocks", () => {
  it("measures a finished span between its own stamps", () => {
    expect(elapsed("2026-01-01T00:00:00Z", "2026-01-01T00:00:12Z")).toBe(12_000);
  });

  it("reads a pending step as nothing, not as the epoch", () => {
    // Date.parse(undefined) and Date.parse("") are NaN; either arriving as a number would render a
    // step that never ran as having taken 56 years.
    expect(elapsed(undefined, undefined)).toBe(0);
    expect(elapsed("", "")).toBe(0);
    expect(elapsed("not a date", undefined)).toBe(0);
  });

  it("runs a live span to now", () => {
    const started = new Date(Date.now() - 5_000).toISOString();
    expect(elapsed(started, undefined)).toBeGreaterThanOrEqual(4_900);
  });

  it("spans the run from its first step's start to its last step's end", () => {
    const nodes = [
      work("a", "ok", { start: "2026-01-01T00:00:00Z", end: "2026-01-01T00:00:30Z" }),
      work("b", "ok", { start: "2026-01-01T00:00:30Z", end: "2026-01-01T00:02:00Z" }),
    ];
    expect(execWindow(nodes, false)?.span).toBe(120_000);
  });

  it("ignores a container's own stamps, which a run that has not opened its first pass carries", () => {
    const loop = { ...group("loop", "repeat", []), start: "2026-01-01T00:00:00Z" };
    expect(execWindow([loop], true)).toBeUndefined();
  });
});
