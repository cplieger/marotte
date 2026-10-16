// The exec model's derived reads: what counts as work, the step counter, the failure an alert
// names and the clocks, which the run card, the run bar and the run page all read.
import { describe, it, expect } from "vitest";
import {
  counters,
  elapsed,
  failureOwner,
  compactText,
  miniBar,
  progressPhrase,
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

describe("the progress phrase states where the run stands", () => {
  const running = (nodes: ExecNode[]): ProgressSource => ({ nodes, state: "running" });
  const passOf = (n: number, kids: ExecNode[], id = "loop"): ExecNode => ({
    ...group(`pass ${String(n)}`, "sequence", kids),
    path: `${id}#${String(n)}`,
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
  const compact = (run: ProgressSource): string => compactText(progressPhrase(run));

  it("reads x/y with no loop, the words carrying the step", () => {
    const nodes = [work("a", "ok"), work("b", "running"), work("c", "pending")];
    expect(progressPhrase(running(nodes))).toEqual({
      pass: "",
      inner: "",
      steps: "2/3",
      words: "step 2 of 3",
      tip: "step 2 of 3",
    });
  });

  it("prefixes one loop's pass and budget, its id in the words", () => {
    const nodes = [
      loop([earlier(1), earlier(2), third()], { maxPasses: 8 }),
      work("land", "pending"),
    ];
    expect(progressPhrase(running(nodes))).toEqual({
      pass: "3/8",
      inner: "",
      steps: "2/5",
      words: "loop pass 3 of 8, step 2 of 5",
      tip: "loop pass 3 of 8, step 2 of 5",
    });
    expect(compact(running(nodes))).toBe("\u21bb3/8 \u00b7 2/5");
  });

  it("names the pass alone for a loop with no budget", () => {
    const nodes = [loop([earlier(1), earlier(2), third()]), work("land", "pending")];
    expect(compact(running(nodes))).toBe("\u21bb3 \u00b7 2/5");
  });

  it("shows no pass once the step in flight is outside the loop", () => {
    const done = passOf(3, [work("code", "ok"), work("review", "ok")]);
    const nodes = [loop([earlier(1), earlier(2), done], { maxPasses: 8 }), work("land", "running")];
    expect(compact(running(nodes))).toBe("3/3");
  });

  describe("nested loops: the outermost pass is the headline", () => {
    const inner = (pass: number): ExecNode =>
      loop([passOf(pass, [work("build", "running")], "build-loop")], {
        path: "build-loop",
        label: "build-loop",
        maxPasses: 4,
      });
    const outer = (kid: ExecNode, over: Partial<ExecNode> = {}): ExecNode =>
      loop([passOf(15, [kid, work("check", "pending")], "tasks")], {
        path: "tasks",
        label: "tasks",
        maxPasses: 50,
        ...over,
      });

    it("leaves an inner loop at its first pass out of the headline", () => {
      const run = running([outer(inner(1))]);
      expect(compact(run)).toBe("\u21bb15/50 \u00b7 1/2");
      expect(progressPhrase(run).words).toBe(
        "tasks pass 15 of 50, build-loop pass 1 of 4, step 1 of 2",
      );
    });

    it("adds an inner loop past its first pass after a chevron", () => {
      expect(compact(running([outer(inner(2))]))).toBe("\u21bb15/50 \u203a 2/4 \u00b7 1/2");
    });

    it("shows only the innermost of several inner loops past their first pass", () => {
      const deepest = loop([passOf(3, [work("try", "running")], "retry")], {
        path: "retry",
        label: "retry",
        maxPasses: 5,
      });
      const middle = loop([passOf(2, [deepest], "build-loop")], {
        path: "build-loop",
        label: "build-loop",
        maxPasses: 4,
      });
      expect(compact(running([outer(middle)]))).toBe("\u21bb15/50 \u203a 3/5 \u00b7 1/2");
    });

    it("reads the extended budget the loop carries", () => {
      expect(compact(running([outer(inner(1), { maxPasses: 60 })]))).toBe("\u21bb15/60 \u00b7 1/2");
    });

    it("keeps the outer pass that parallel branches share though their inner loops differ", () => {
      const retry = (id: string, pass: number, step: string): ExecNode =>
        loop([passOf(pass, [work(step, "running")], id)], { path: id, label: id, maxPasses: 4 });
      const branches = (b: ExecNode): ExecNode =>
        group("fan", "parallel", [
          { ...group("track-a", "sequence", [retry("build-loop", 2, "build")]), path: "track-a" },
          { ...group("track-b", "sequence", [b]), path: "track-b" },
        ]);
      // One branch inside an inner loop, the other in none; then both inside inner loops at
      // different passes.
      const noInner = running([outer(branches(work("lint", "running")))]);
      expect(compact(noInner)).toBe("\u21bb15/50 \u00b7 0/3");
      expect(progressPhrase(noInner).words).toBe("tasks pass 15 of 50, 0 of 3 steps done");
      expect(miniBar(noInner).continuous).toBe(true);
      const otherInner = running([outer(branches(retry("lint-loop", 3, "lint")))]);
      expect(compact(otherInner)).toBe("\u21bb15/50 \u00b7 0/3");
    });

    it("names an outer loop with no budget by its pass alone", () => {
      const { maxPasses: _budget, ...unbounded } = outer(inner(1));
      const run = running([unbounded]);
      expect(compact(run)).toBe("\u21bb15 \u00b7 1/2");
      expect(progressPhrase(run).words.startsWith("tasks pass 15, ")).toBe(true);
    });
  });

  it("follows the newest started step when none is in flight", () => {
    const at = (min: number): Partial<ExecNode> => ({ startGen: min });
    const paused = passOf(2, [work("code", "ok", at(3)), work("review", "pending")]);
    const nodes = [
      work("plan", "ok", at(1)),
      loop([passOf(1, [work("code1", "ok", at(2))]), paused], { maxPasses: 4 }),
    ];
    expect(compact(running(nodes))).toBe("\u21bb2/4 \u00b7 2/3");
  });

  it("shows no pass once every step has settled", () => {
    const last = passOf(2, [
      work("code", "ok", { startGen: 1 }),
      work("review", "ok", { startGen: 2 }),
    ]);
    expect(compact(running([loop([earlier(1), last], { maxPasses: 2 })]))).toBe("2/2");
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
      expect(compact(run)).toBe("1/3");
    });
  }

  it("keeps the pass of a one-step loop, whose step count the summary withholds", () => {
    const nodes = [loop([earlier(1), passOf(2, [work("code", "running")])], { maxPasses: 5 })];
    expect(summaryProgress(running(nodes))).toMatchObject({ pass: "2/5", steps: "" });
  });

  describe("a parallel run", () => {
    // The reported shape: three tracks, track A back in pass 2 of its loop while B finished and C
    // reached its land step. Plan order: A has 6 work nodes, B 1, C 7; the bar's 7 green segments
    // are the 7 done.
    const trackA = (): ExecNode => ({
      ...group("track-a", "sequence", [
        work("a-setup", "ok"),
        loop(
          [
            passOf(1, [work("a-fix1", "ok"), work("a-r1", "ok"), work("a-agg1", "ok")], "a-loop"),
            passOf(
              2,
              [
                work("a-fix", "running"),
                work("a-review-1", "pending"),
                work("a-review-2", "pending"),
                work("a-agg", "pending"),
              ],
              "a-loop",
            ),
          ],
          { path: "a-loop", label: "fix-loop", maxPasses: 3 },
        ),
        work("a-land", "pending"),
      ]),
      path: "track-a",
    });
    const trackC = (landState: ExecState, loopPass?: number): ExecNode => ({
      ...group("track-c", "sequence", [
        work("c-setup", "ok"),
        loop(
          [
            passOf(
              loopPass ?? 1,
              [
                work("c-fix", loopPass === undefined ? "ok" : "running"),
                work("c-review-1", loopPass === undefined ? "ok" : "pending"),
                work("c-review-2", loopPass === undefined ? "ok" : "pending"),
                work("c-agg", loopPass === undefined ? "ok" : "pending"),
              ],
              "c-loop",
            ),
          ],
          { path: "c-loop", label: "fix-loop", maxPasses: 3 },
        ),
        work("c-land", landState),
        work("c-audit", "pending"),
      ]),
      path: "track-c",
    });
    const fan = (c: ExecNode): ExecNode[] => [
      group("tracks", "parallel", [trackA(), { ...work("track-b", "ok"), path: "track-b" }, c]),
    ];

    it("counts done/total, not the first branch's position", () => {
      const run = running(fan(trackC("running")));
      expect(progressPhrase(run)).toMatchObject({ pass: "", steps: "7/14" });
      expect(progressPhrase(run).words).toBe("track-a fix-loop pass 2 of 3, 7 of 14 steps done");
      expect(progressPhrase(run).tip).toBe("track-a \u21bb2/3 \u00b7 7/14");
    });

    it("shows no pass glyph when two branches stand in different loop passes", () => {
      const run = running(fan(trackC("pending", 1)));
      const p = progressPhrase(run);
      expect(p.pass).toBe("");
      expect(p.tip).toBe("track-a \u21bb2/3, track-c \u21bb1/3 \u00b7 3/14");
    });

    it("draws the bar in plan order with a double gap before each sibling branch", () => {
      const bar = miniBar(running(fan(trackC("running"))));
      expect(bar.continuous).toBe(false);
      expect(bar.segs).toHaveLength(14);
      expect(bar.segs.flatMap((s, i) => (s.branch ? [i] : []))).toEqual([6, 7]);
    });

    it("keeps the pass when every branch in flight shares one loop pass", () => {
      const pass = passOf(2, [
        group("review", "parallel", [work("gpt", "running"), work("claude", "running")]),
        work("agg", "pending"),
      ]);
      const run = running([loop([earlier(1), pass], { label: "review-loop", maxPasses: 3 })]);
      expect(compact(run)).toBe("\u21bb2/3 \u00b7 0/3");
    });
  });

  it("keeps the in-flight position for a single-branch run", () => {
    expect(counters([work("a", "ok"), work("b", "ok"), work("c", "running")]).current).toBe(3);
    expect(compact(running([work("a", "ok"), work("b", "running"), work("c", "pending")]))).toBe(
      "2/3",
    );
  });
});

describe("miniBar follows the headline", () => {
  const running = (nodes: ExecNode[]): ProgressSource => ({ nodes, state: "running" });
  const loopAt = (pass: number, max: number): ExecNode => ({
    ...group("tasks", "repeat", [
      ...Array.from({ length: pass - 1 }, (_, i) => ({
        ...group(`pass ${String(i + 1)}`, "sequence", [work(`t${String(i + 1)}`, "ok")]),
        path: `tasks#${String(i + 1)}`,
        state: "ok" as const,
        pass: i + 1,
      })),
      {
        ...group(`pass ${String(pass)}`, "sequence", [work("now", "running")]),
        path: `tasks#${String(pass)}`,
        pass,
      },
    ]),
    maxPasses: max,
  });

  it("draws the steps with no loop", () => {
    const bar = miniBar(running([work("a", "ok"), work("b", "running")]));
    expect(bar.segs.map((s) => s.state)).toEqual(["ok", "running"]);
  });

  it("draws one segment per pass up to 12", () => {
    const bar = miniBar(running([loopAt(3, 12)]));
    expect(bar.continuous).toBe(false);
    expect(bar.segs).toHaveLength(12);
    expect(bar.segs.slice(0, 4).map((s) => s.state)).toEqual(["ok", "ok", "running", "pending"]);
  });

  it("draws one continuous fill past 12 passes, weighted done, now and left", () => {
    const bar = miniBar(running([loopAt(15, 50)]));
    expect(bar.continuous).toBe(true);
    expect(bar.segs.map((s) => [s.state, s.weight])).toEqual([
      ["ok", 14],
      ["running", 1],
      ["pending", 35],
    ]);
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
