// The exec model's derived reads: what counts as work, the step counter, the failure an alert
// names and the clocks, which the run card, the run bar and the run page all read.
import { describe, it, expect } from "vitest";
import {
  counters,
  elapsed,
  failureOwner,
  window as execWindow,
  workNodes,
  type ExecNode,
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
