// The workflow adapter: KAS's `inspect` reply folded into the exec view's model.

import { describe, it, expect } from "vitest";
import { runToExec, indexPlan } from "./run-exec-source.js";
import { nodePathKey } from "./run-node-key.js";
import { flatten, workNodes, counters, type ExecNode } from "./exec-view/model.js";
import extendedLoopRaw from "./testdata/run-extended-loop.json?raw";
import { makeRunState } from "./__test-helpers__/model.js";
import type { RunNode, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };

// `root` stays `unknown`: the builders spell a node's status as the raw string KAS sends, which
// `RunNode`'s classified status would refuse.
function stateWith(root: unknown, extra: Record<string, unknown> = {}): RunState {
  return { ...makeRunState({ status: "running" }), root: root as RunNode, ...extra };
}

const step = (nodeId: string, status: string, extra: Record<string, unknown> = {}) => ({
  nodeId,
  type: "step",
  status,
  children: [],
  ...extra,
});

describe("runToExec structure", () => {
  // The root is a container KAS names after the workflow itself, so keeping it would put one group
  // around everything — an indent carrying no information.
  it("unwraps the synthetic root and keeps the real top level", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [step("a", "completed"), step("b", "running")],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes.map((n) => n.label)).toEqual(["a", "b"]);
    // The path still carries the root, because it is the address the server stamps on a step frame
    // and the two sides of that join must agree.
    expect(run.nodes[0]?.path).toBe("wf_1:a");
  });

  it("keys two steps whose ids spell alike under a slash join apart", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          { nodeId: "a/b", type: "sequence", status: "running", children: [step("c", "running")] },
          { nodeId: "a", type: "sequence", status: "running", children: [step("b/c", "running")] },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(workNodes(run.nodes).map((n) => n.path)).toEqual(["wf_1:a/b:c", "wf_1:a:b/c"]);
  });

  // Flattening the tree to its leaves would make a loop, a parallel and a watch invisible.
  it("keeps control-flow containers as nodes of their own", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "loop",
            type: "repeat",
            status: "running",
            children: [
              {
                nodeId: "loop#0",
                type: "sequence",
                status: "running",
                iteration: 0,
                children: [step("work", "running")],
              },
            ],
          },
          { nodeId: "watch", type: "watch", status: "pending", children: [] },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(flatten(run.nodes).map((n) => `${n.kind}:${n.label}`)).toEqual([
      "repeat:loop",
      "sequence:pass 1",
      "step:work",
      "watch:watch",
    ]);
    // The iteration container is labelled as the PASS it is (`loop#0` is KAS's id for it, not a
    // name) and contributes KAS's frame spelling to the PATH: the detail pane addresses a step's
    // live transcript by path, so a tree keyed on `loop#0` selects a row nothing streams into.
    expect(flatten(run.nodes).map((n) => n.path)).toEqual([
      "wf_1:loop",
      "wf_1:loop:iter-0",
      "wf_1:loop:iter-0:work",
      "wf_1:watch",
    ]);
    // Only steps and watches count: a container's span is its children's, so counting it would
    // inflate the total and double-count the time.
    expect(counters(run.nodes).total).toBe(2);
    expect(workNodes(run.nodes).map((n) => n.label)).toEqual(["work", "watch"]);
  });

  // A node type this build has never seen must land on a kind the CSS has a rule for, or the row
  // renders with no glyph and no treatment.
  it("maps an unknown node type onto group rather than passing it through", () => {
    const run = runToExec(
      stateWith({ nodeId: "wf_1", type: "fanout", status: "running", children: [] }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.kind).toBe("group");
  });
});

// A node's role is its TYPE: KAS writes a parallel as `children: []` until its branches start and a
// repeat with no children until its first pass opens, and neither is work in the meantime.
describe("runToExec decides work by kind and fills an unexpanded container from its plan", () => {
  const PLAN = [
    {
      nodeId: "loop",
      type: "repeat",
      maxIterations: 3,
      steps: [
        { nodeId: "code", type: "step", agentName: "wf-coder", modelId: "claude-opus-5.5" },
        {
          nodeId: "reviews",
          type: "parallel",
          branches: [
            { nodeId: "review-a", type: "step", agentName: "reviewer-a" },
            { nodeId: "review-b", type: "step", agentName: "reviewer-b" },
          ],
        },
      ],
    },
  ];

  function shape(root: unknown, plan: unknown): string[] {
    return flatten(runToExec(stateWith(root), plan, NO_ASKS).nodes).map(
      (n) => `${n.kind}:${n.label}:${n.path}:${n.transcript === true ? "t" : "-"}`,
    );
  }

  it("lists a pending parallel's branches from the plan, at the paths KAS gives them", () => {
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [{ nodeId: "reviews", type: "parallel", status: "pending", children: [] }],
    };
    expect(shape(root, PLAN)).toEqual([
      "parallel:reviews:wf_1:reviews:-",
      "step:review-a:wf_1:reviews:review-a:t",
      "step:review-b:wf_1:reviews:review-b:t",
    ]);
  });

  it("opens a not-yet-started repeat on its first pass, spelled as KAS will spell it", () => {
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [{ nodeId: "loop", type: "repeat", status: "running" }],
    };
    const run = runToExec(stateWith(root), PLAN, NO_ASKS);
    expect(shape(root, PLAN)).toEqual([
      "repeat:loop:wf_1:loop:-",
      "sequence:pass 1:wf_1:loop:iter-0:-",
      "step:code:wf_1:loop:iter-0:code:t",
      "parallel:reviews:wf_1:loop:iter-0:reviews:-",
      "step:review-a:wf_1:loop:iter-0:reviews:review-a:t",
      "step:review-b:wf_1:loop:iter-0:reviews:review-b:t",
    ]);
    expect(run.nodes[0]?.maxPasses).toBe(3);
    expect(run.nodes[0]?.children[0]?.pass).toBe(1);
    // Planned work has not started, and the step counter counts it.
    expect(workNodes(run.nodes).map((n) => n.state)).toEqual(["pending", "pending", "pending"]);
    expect(counters(run.nodes)).toEqual({ total: 3, done: 0, failed: 0, current: 0 });
  });

  it("never counts a childless container as a step, plan or no plan", () => {
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [
        step("a", "running"),
        { nodeId: "fan", type: "parallel", status: "pending", children: [] },
      ],
    };
    const run = runToExec(stateWith(root), undefined, NO_ASKS);
    expect(workNodes(run.nodes).map((n) => n.label)).toEqual(["a"]);
    expect(run.nodes[1]?.transcript).toBeUndefined();
  });

  // Node types are an open upstream vocabulary, and the state path already maps a type this build
  // has never seen onto `group`; the plan path has to agree, or a parallel loses that branch.
  it("keeps a planned branch of an unknown type, as a group in plan order", () => {
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [{ nodeId: "reviews", type: "parallel", status: "pending", children: [] }],
    };
    const plan = [
      {
        nodeId: "reviews",
        type: "parallel",
        branches: [
          { nodeId: "review-a", type: "step" },
          { nodeId: "future-review", type: "delegate" },
        ],
      },
    ];
    expect(shape(root, plan)).toEqual([
      "parallel:reviews:wf_1:reviews:-",
      "step:review-a:wf_1:reviews:review-a:t",
      "group:future-review:wf_1:reviews:future-review:-",
    ]);
  });

  it("fills a root KAS has not expanded from the plan's top level", () => {
    const root = { nodeId: "wf_1", type: "sequence", status: "running" };
    expect(shape(root, PLAN).slice(0, 3)).toEqual([
      "repeat:loop:wf_1:loop:-",
      "sequence:pass 1:wf_1:loop:iter-0:-",
      "step:code:wf_1:loop:iter-0:code:t",
    ]);
    expect(runToExec(stateWith(root), undefined, NO_ASKS).nodes).toEqual([]);
  });

  it("keeps the state's children over the plan's once KAS has expanded a container", () => {
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [
        {
          nodeId: "reviews",
          type: "parallel",
          status: "running",
          children: [step("review-a", "running", { branchId: "review-a" })],
        },
      ],
    };
    expect(shape(root, PLAN)).toEqual([
      "parallel:reviews:wf_1:reviews:-",
      "step:review-a:wf_1:reviews:review-a:t",
    ]);
  });

  it("counts the latest pass, so the pass number stays the loop's", () => {
    const pass = (n: number, status: string) => ({
      nodeId: `loop#${String(n)}`,
      type: "sequence",
      status,
      iteration: n,
      children: [step("code", status), step("review", status === "completed" ? status : "pending")],
    });
    const root = {
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [
        {
          nodeId: "loop",
          type: "repeat",
          status: "running",
          children: [pass(0, "completed"), pass(1, "running")],
        },
      ],
    };
    const run = runToExec(stateWith(root), PLAN, NO_ASKS);
    expect(counters(run.nodes)).toEqual({ total: 2, done: 0, failed: 0, current: 1 });
    // The run tab still holds every pass.
    expect(run.nodes[0]?.children.map((p) => `${p.label}:${String(p.pass)}`)).toEqual([
      "pass 1:1",
      "pass 2:2",
    ]);
    // A step inside a pass does not restate it.
    expect(run.nodes[0]?.children[1]?.children[0]?.subtitle).toBeUndefined();
  });
});

describe("runToExec container state", () => {
  // A container reads `running` for as long as anything inside it is open, which tells a reader
  // nothing they cannot already see. The worst outcome beneath it is what a collapsed group has to
  // be able to say.
  it("rolls the worst child outcome up to its container", () => {
    for (const [child, want] of [
      ["failed", "fail"],
      ["aborted", "warn"],
      ["running", "running"],
      ["paused", "waiting"],
    ] as const) {
      const run = runToExec(
        stateWith({
          nodeId: "wf_1",
          type: "sequence",
          status: "running",
          children: [
            {
              nodeId: "g",
              type: "parallel",
              status: "running",
              children: [step("ok", "completed"), step("other", child)],
            },
          ],
        }),
        undefined,
        NO_ASKS,
      );
      expect(run.nodes[0]?.state).toBe(want);
    }
  });

  // All children done means the container is done, even while its own status still says running —
  // which it does until KAS settles it.
  it("settles a container once every child has", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "g",
            type: "sequence",
            status: "running",
            children: [step("a", "completed"), step("b", "skipped")],
          },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.state).toBe("ok");
  });

  // KAS's inspect reply carries no end on a completed repeat, so without one its duration ran on.
  it("ends a finished container with no end of its own at its last child's end", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          {
            nodeId: "loop",
            type: "repeat",
            status: "completed",
            startedAt: "2026-10-07T22:02:46.471Z",
            children: [
              step("a", "completed", {
                startedAt: "2026-10-07T22:02:46.474Z",
                endedAt: "2026-10-07T22:03:13.060Z",
              }),
              step("b", "completed", {
                startedAt: "2026-10-07T22:02:46.474Z",
                endedAt: "2026-10-07T22:03:00.595Z",
              }),
            ],
          },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.end).toBe("2026-10-07T22:03:13.060Z");
  });

  it("leaves a running container's end open", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "loop",
            type: "repeat",
            status: "running",
            startedAt: "2026-10-07T22:02:46.471Z",
            children: [
              step("a", "completed", { endedAt: "2026-10-07T22:03:00.595Z" }),
              step("b", "running"),
            ],
          },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.end).toBeUndefined();
  });

  /** A loop whose pass 1 `a` failed and whose pass 2 `a` carries `last`. */
  function loopAfterFailure(last: string): RunState {
    const pass = (i: number, status: string): Record<string, unknown> => ({
      nodeId: `loop#${String(i)}`,
      type: "sequence",
      status,
      iteration: i,
      children: [step("a", status, { failureReason: i === 0 ? "the first try broke" : undefined })],
    });
    return stateWith({
      nodeId: "wf_1",
      type: "sequence",
      status: "running",
      children: [
        {
          nodeId: "loop",
          type: "repeat",
          status: "running",
          children: [pass(0, "failed"), pass(1, last)],
        },
      ],
    });
  }

  // A loop is where its latest pass is: a failure an earlier pass moved past is that pass's.
  it("rolls a repeat up from its latest pass alone", () => {
    for (const [last, want] of [
      ["running", "running"],
      ["completed", "ok"],
      ["failed", "fail"],
    ] as const) {
      const run = runToExec(loopAfterFailure(last), undefined, NO_ASKS);
      expect(run.nodes[0]?.state, last).toBe(want);
      expect(run.nodes[0]?.children[0]?.state, "pass 1 keeps its own failure").toBe("fail");
    }
  });
});

describe("runToExec reads what nothing read before", () => {
  // `state.inputs` had zero readers on any surface: what the run was ASKED to do was displayed
  // nowhere in the app.
  it("carries the run's inputs", () => {
    const run = runToExec(
      stateWith(step("a", "running"), { inputs: { repo: "marotte" } }),
      undefined,
      NO_ASKS,
    );
    expect(run.inputs).toEqual({ repo: "marotte" });
  });

  // `nodePlan` had zero readers: passed through verbatim by GET /api/runs/{id} and decoded by
  // nothing, so a loop's bound and its exit condition were on the wire and had never been on
  // screen.
  it("states a repeat's bound and stop condition from the plan", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          { nodeId: "loop", type: "repeat", status: "running", children: [step("w", "running")] },
        ],
      }),
      [
        {
          nodeId: "loop",
          type: "repeat",
          maxIterations: 5,
          onMaxIterations: "pause",
          stopCondition: "w.output contains PASS",
          steps: [{ nodeId: "w", type: "step" }],
        },
      ],
      NO_ASKS,
    );
    const loop = run.nodes[0];
    expect(loop?.subtitle).toContain("up to 5 passes");
    expect(loop?.subtitle).toContain("w.output contains PASS");
    const labels = (loop?.facts ?? []).map((f) => f.label);
    expect(labels).toContain("Max passes");
    expect(labels).toContain("At the cap");
  });

  it("names a watch's handler from KAS's nodePlan shape", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "build", type: "watch", status: "running", children: [] }],
      }),
      [{ nodeId: "build", type: "watch", agentName: "background-process" }],
      NO_ASKS,
    );
    const watch = run.nodes[0];
    expect(watch?.subtitle).toBe("polls background-process");
    expect((watch?.facts ?? []).find((f) => f.label === "Handler")?.value).toBe(
      "background-process",
    );
  });

  it("reads a settled background-process watch's exit record", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          {
            nodeId: "build",
            type: "watch",
            status: "completed",
            children: [],
            capturedOutput: JSON.stringify({
              terminalId: "t1",
              status: "exited",
              exitCode: 1,
              signal: null,
              startedAt: "2026-10-06T00:00:00.000Z",
              outputFile: "/tmp/build.log",
              outputTail: "ok\nFAIL x",
            }),
          },
        ],
      }),
      [{ nodeId: "build", type: "watch", agentName: "background-process" }],
      NO_ASKS,
    );
    const watch = run.nodes[0];
    expect(watch?.subtitle).toBe("exited 1");
    const facts = Object.fromEntries((watch?.facts ?? []).map((f) => [f.label, f.value]));
    expect(facts["Handler"]).toBe("background-process");
    expect(facts["Exit"]).toBe("1");
    expect(facts["Output file"]).toBe("/tmp/build.log");
    expect(watch?.output).toBe("```text\nok\nFAIL x\n```");
  });

  it("keeps a watch capture that is not KAS's record verbatim", () => {
    const run = runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          {
            nodeId: "pr",
            type: "watch",
            status: "completed",
            children: [],
            capturedOutput: "merged",
          },
        ],
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.output).toBe("merged");
    expect(run.nodes[0]?.subtitle).toBe("polls");
  });

  // Four more fields with zero render sites before the exec view.
  it("surfaces the per-step facts nothing rendered", () => {
    const run = runToExec(
      stateWith(
        step("a", "completed", {
          agentName: "wf-coder",
          modelId: "claude-opus-5",
          effortLevel: "max",
          completionSignal: "success",
          sessionId: "sess_1",
          continuationAttempts: 2,
          watchTerminal: true,
        }),
      ),
      undefined,
      NO_ASKS,
    );
    const facts = new Map((run.nodes[0]?.facts ?? []).map((f) => [f.label, f.value]));
    expect(facts.get("Effort")).toBe("max");
    expect(facts.get("Signal")).toBe("success");
    expect(facts.get("Session")).toBe("sess_1");
    expect(facts.get("Retries")).toBe("2");
    expect(facts.get("Watch")).toBe("reached a terminal state");
  });

  // `auto` is the ABSENCE of a model choice, so naming it as one implies a pin that never happened.
  it("does not report auto as a model", () => {
    const run = runToExec(
      stateWith(step("a", "running", { agentName: "wf-coder", modelId: "auto" })),
      undefined,
      NO_ASKS,
    );
    expect((run.nodes[0]?.facts ?? []).map((f) => f.label)).not.toContain("Model");
    expect(run.nodes[0]?.subtitle).toBe("wf-coder");
  });

  it("counts a step's retries in the subtitle, one retry singular", () => {
    const subtitle = (n: number) =>
      runToExec(
        stateWith(step("a", "running", { agentName: "wf-coder", continuationAttempts: n })),
        undefined,
        NO_ASKS,
      ).nodes[0]?.subtitle;
    expect(subtitle(1)).toBe("wf-coder \u00b7 1 retry");
    expect(subtitle(2)).toBe("wf-coder \u00b7 2 retries");
  });
});

describe("indexPlan tolerance", () => {
  // A foreign shape whose members grow between kiro-cli releases. The page's other half renders
  // fine whether or not this walk understood all of it, so nothing here may throw.
  it("survives a plan that is not the shape it expects", () => {
    for (const input of [undefined, null, 42, "nope", {}, [], [null, 7, "x"]]) {
      expect(() => indexPlan(input)).not.toThrow();
      expect(indexPlan(input).size).toBe(0);
    }
  });

  it("descends every container spelling, including one it does not know", () => {
    const idx = indexPlan([
      {
        nodeId: "top",
        steps: [{ nodeId: "mid", branches: [{ nodeId: "deep", maxIterations: 3 }] }],
      },
      { nodeId: "other", nodes: [{ nodeId: "nested", stopCondition: "done" }] },
    ]);
    expect(idx.get("deep")?.maxIterations).toBe(3);
    expect(idx.get("nested")?.stopCondition).toBe("done");
  });

  // `stopWhen` is the other spelling; the engine rejects a node declaring both, so folding them
  // into one field cannot lose one.
  it("takes stopWhen as a stop condition", () => {
    expect(indexPlan([{ nodeId: "n", stopWhen: "watch.terminal" }]).get("n")?.stopCondition).toBe(
      "watch.terminal",
    );
  });

  // KAS's nodePlan names a watch's handler in `agentName`; on any other node that field is not one.
  it("reads a watch's handler from agentName on a watch node only", () => {
    const idx = indexPlan([
      { nodeId: "build", type: "watch", agentName: "background-process" },
      { nodeId: "code", type: "step", agentName: "wf-coder" },
    ]);
    expect(idx.get("build")?.watch).toBe("background-process");
    expect(idx.has("code")).toBe(false);
  });

  // A node the plan mentions with nothing interesting on it earns no entry, so a caller can treat
  // "present" as "has a fact".
  it("indexes only nodes that carry a fact", () => {
    expect(indexPlan([{ nodeId: "bare", type: "step" }]).size).toBe(0);
  });
});

describe("runToExec alert precedence", () => {
  // An unanswered ask outranks the run's own status, because the run genuinely still reads
  // `running` while a step's ask blocks it — so the status reports nothing wrong and would leave
  // the one actionable state unsaid.
  it("puts an unanswered ask ahead of everything", () => {
    const run = runToExec(stateWith(step("a", "running"), { status: "failed" }), undefined, {
      count: 2,
      asked: [{ nodeID: "a", sessionID: "", answer: false }],
      label: "Run tests?",
    });
    expect(run.alert?.kind).toBe("input");
    expect(run.alert?.text).toContain("Run tests?");
    expect(run.alert?.text).toContain("2 asks waiting");
    // And it reclassifies the run itself, so the header says "needs input".
    expect(run.state).toBe("input");
    expect(run.nodes[0]?.state).toBe("input");
  });

  // A deliberate stop is not a failure, and telling them apart is the whole reason `stopInitiator`
  // is on the wire.
  it("separates a user stop from a failure", () => {
    const stopped = runToExec(
      stateWith(step("a", "aborted"), {
        status: "aborted",
        stopInitiator: "user",
        stopReason: "changed my mind",
      }),
      undefined,
      NO_ASKS,
    );
    expect(stopped.alert?.kind).toBe("stopped");
    expect(stopped.alert?.text).toBe("Stopped by user: changed my mind");

    const paused = runToExec(
      stateWith(step("a", "paused"), { status: "paused", stopInitiator: "user" }),
      undefined,
      NO_ASKS,
    );
    expect(paused.alert?.text).toBe("Paused by user");

    const pending = runToExec(
      stateWith(step("a", "running"), { status: "running", pausePending: { initiator: "user" } }),
      undefined,
      NO_ASKS,
    );
    expect(pending.alert).toEqual({ kind: "paused", text: "Pausing after the current step" });

    const failed = runToExec(
      stateWith(step("a", "failed", { failureReason: "exit 1" }), { status: "failed" }),
      undefined,
      NO_ASKS,
    );
    expect(failed.alert?.kind).toBe("failed");
    // It names the step, because "the run failed" sends a reader looking for which.
    expect(failed.alert?.text).toContain("a failed: exit 1");
  });

  it("names the transient-error code behind a pause", () => {
    const run = runToExec(
      stateWith(step("a", "paused"), {
        status: "paused",
        pauseReason: "retrying",
        pauseDetail: { code: "ThrottlingException" },
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert?.kind).toBe("paused");
    expect(run.alert?.text).toContain("ThrottlingException");
    expect(run.alert?.text).toContain("transient");
  });

  // `pauseDetail.class` gained a second member upstream in 2.21.1. An exhausted continuation budget
  // is not a transient failure, and the reason sentence already carries upstream's own explanation.
  it("does not call an exhausted continuation budget a transient error", () => {
    const run = runToExec(
      stateWith(step("a", "paused"), {
        status: "paused",
        pauseReason: "Step could not be continued after 3 consecutive attempts.",
        pauseDetail: { class: "continuation-exhausted", code: "MaxContinuationAttempts" },
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert?.kind).toBe("paused");
    expect(run.alert?.text).toContain("MaxContinuationAttempts");
    expect(run.alert?.text).not.toContain("transient");
  });

  // Only the run-asks store says a step waits on a person; a need-input park it holds no ask for (the
  // run was not read yet, or the question was answered) is KAS's pause, quoted like any other.
  it("leaves a need-input park the store holds no ask for to KAS's own sentence", () => {
    const run = runToExec(
      stateWith(
        {
          nodeId: "phase1",
          type: "parallel",
          status: "paused",
          children: [step("verify", "paused", { completionSignal: "need_input" })],
        },
        { status: "paused", pauseReason: "Step 'verify' is waiting for user input." },
      ),
      undefined,
      NO_ASKS,
    );
    expect(run.alert?.kind).toBe("paused");
    expect(run.alert?.text).toBe("Waiting: Step 'verify' is waiting for user input.");
  });

  it("quotes any other pause reason verbatim", () => {
    const run = runToExec(
      stateWith(step("a", "paused"), { status: "paused", pauseReason: "watching for a file" }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert?.text).toBe("Waiting: watching for a file");
  });

  it("raises no alert on a run that wants nothing", () => {
    const run = runToExec(
      stateWith(step("a", "completed"), { status: "completed" }),
      undefined,
      NO_ASKS,
    );
    expect(run.alert).toBeUndefined();
    expect(run.live).toBe(false);
  });
});

// Both fields stay on `RunState`, which is a documented verbatim passthrough of KAS's own schema.

describe("runToExec question targeting", () => {
  // The run tab answers where the server would: a repeat's iterations share a node id, so only the
  // execution the ask resolves to (by session, else by path) is the one the box's words answer.
  const loop = {
    nodeId: "loop",
    type: "repeat",
    status: "paused",
    children: [
      { ...step("review", "completed", { sessionId: "sess_0" }), iteration: 0 },
      { ...step("review", "paused", { sessionId: "sess_1" }), iteration: 1 },
    ],
  };

  function answered(address: RunAsks["asked"][number]): { path: string; ask?: string }[] {
    const run = runToExec(stateWith(loop, { status: "paused" }), undefined, {
      count: 1,
      asked: [address],
      label: "Which colour?",
    });
    return workNodes(run.nodes)
      .filter((n) => n.verb === "answer")
      .map((n) => (n.ask === undefined ? { path: n.path } : { path: n.path, ask: n.ask }));
  }

  it("answers the execution a question's path names, under that question's id", () => {
    expect(
      answered({
        nodeID: "review",
        sessionID: "",
        answer: true,
        path: nodePathKey(["loop", "iter-1"]),
        askID: "ask-1",
      }),
    ).toEqual([{ path: nodePathKey(["loop", "iter-1"]), ask: "ask-1" }]);
  });

  it("answers the execution a question's session names", () => {
    expect(
      answered({ nodeID: "review", sessionID: "sess_1", answer: true, askID: "ask-1" }),
    ).toEqual([{ path: nodePathKey(["loop", "iter-1"]), ask: "ask-1" }]);
  });

  it("answers nowhere for a question addressed by a node id alone", () => {
    expect(answered({ nodeID: "review", sessionID: "", answer: true })).toEqual([]);
  });
});

describe("a step's detail line", () => {
  it("names a retry wait on a running step and a pause on a paused one, verbatim", () => {
    const run = runToExec(
      stateWith(step("coder", "running", { retryReason: "Waiting 30s to retry." })),
      undefined,
      NO_ASKS,
    );
    expect(run.nodes[0]?.state).toBe("running");
    expect(run.nodes[0]?.subtitle).toBe("Retrying: Waiting 30s to retry.");

    const paused = runToExec(
      stateWith(step("coder", "paused", { pauseReason: "Step paused." })),
      undefined,
      NO_ASKS,
    );
    expect(paused.nodes[0]?.subtitle).toBe("Paused: Step paused.");
  });
});

describe("the plan revision notice", () => {
  it.each([
    [
      { outcome: "queued", pending: 3, after: "build" },
      "Plan revised: 3 steps queued after build",
      false,
    ],
    [
      { outcome: "applied", pending: 1, after: "build" },
      "Plan revised: 1 step queued after build \u00b7 applied",
      false,
    ],
    [
      { outcome: "rejected", pending: 2, reason: "unknown agent" },
      "Plan revised: 2 steps queued \u00b7 rejected: unknown agent",
      true,
    ],
    [
      { outcome: "dropped", pending: 2 },
      "Plan revised: 2 steps queued \u00b7 dropped, the run ended first",
      false,
    ],
    [
      { outcome: "superseded", pending: 2 },
      "Plan revised: 2 steps queued \u00b7 superseded",
      false,
    ],
  ] as const)("words %o", (u, text, failed) => {
    const run = runToExec(stateWith(step("a", "running"), { planUpdate: u }), undefined, NO_ASKS);
    expect(run.notice).toEqual({ text, failed });
  });

  it("reaches the exec model from the run read", () => {
    const run = runToExec(
      stateWith(step("a", "running"), {
        planUpdate: { outcome: "queued", pending: 2, after: "a" },
      }),
      undefined,
      NO_ASKS,
    );
    expect(run.notice?.text).toBe("Plan revised: 2 steps queued after a");
  });
});

// KAS grades a step `completed` when the model refused or kiro-cli stopped it at its model-call
// limit; only the run log's close knows, and it arrives as the reply's `step_ends`.
describe("runToExec reads the run log's step ends", () => {
  const CAPPED =
    "kiro-cli stopped this step after 300 model calls in one turn, so its work may be unfinished. Rerun the step, or split its work into smaller steps.";
  const tree = (...children: unknown[]) =>
    stateWith({ nodeId: "wf_1", type: "sequence", status: "completed", children });

  it("marks a completed step whose turn kiro-cli stopped as failed, with the reason", () => {
    const run = runToExec(
      tree(step("build", "completed")),
      undefined,
      NO_ASKS,
      "",
      new Map([
        [
          nodePathKey(["wf_1", "build"]),
          { outcome: "failed", failure_kind: "model_call_limit", failure_reason: CAPPED },
        ],
      ]),
    );
    expect(run.nodes[0]?.state).toBe("fail");
    expect(run.nodes[0]?.failure).toBe(CAPPED);
  });

  it("falls back to the outcome's sentence for an end with no reason", () => {
    const run = runToExec(
      tree(step("build", "completed")),
      undefined,
      NO_ASKS,
      "",
      new Map([[nodePathKey(["wf_1", "build"]), { outcome: "refused" }]]),
    );
    expect(run.nodes[0]?.state).toBe("fail");
    expect(run.nodes[0]?.failure).toBe("The model declined to continue.");
  });

  it.each(["", "  \n"])("falls back to the outcome's sentence for a blank reason %j", (reason) => {
    const run = runToExec(
      tree(step("build", "completed")),
      undefined,
      NO_ASKS,
      "",
      new Map([[nodePathKey(["wf_1", "build"]), { outcome: "refused", failure_reason: reason }]]),
    );
    expect(run.nodes[0]?.failure).toBe("The model declined to continue.");
  });

  it("does not name a blank account of KAS's own failure", () => {
    const run = runToExec(
      stateWith(
        {
          nodeId: "wf_1",
          type: "sequence",
          status: "failed",
          children: [step("build", "failed", { failureReason: "  \n" })],
        },
        { status: "failed" },
      ),
      undefined,
      NO_ASKS,
      "",
    );
    expect(run.nodes[0]?.state).toBe("fail");
    expect(run.nodes[0]?.failure).toBeUndefined();
    expect(run.alert?.text).toBe("The run failed");
  });

  it("keeps KAS's own account for a step it did not grade completed", () => {
    const run = runToExec(
      tree(step("build", "failed", { failureReason: "exit 1" })),
      undefined,
      NO_ASKS,
      "",
      new Map([[nodePathKey(["wf_1", "build"]), { outcome: "failed", failure_reason: CAPPED }]]),
    );
    expect(run.nodes[0]?.state).toBe("fail");
    expect(run.nodes[0]?.failure).toBe("exit 1");
  });

  it("leaves a completed step with no end, and an end at another path, alone", () => {
    const run = runToExec(
      tree(step("build", "completed")),
      undefined,
      NO_ASKS,
      "",
      new Map([[nodePathKey(["wf_1", "other"]), { outcome: "failed", failure_reason: CAPPED }]]),
    );
    expect(run.nodes[0]?.state).toBe("ok");
    expect(run.nodes[0]?.failure).toBeUndefined();
  });
});

// KAS expands an `include` into a sequence whose plan entry carries `includedFrom` and prefixes
// every id inside with `<include id>/`; the state tree's sequence drops `includedFrom`.
describe("runToExec shows where an included group came from", () => {
  const seat = (id: string, agent: string, model: string) => ({
    nodeId: `review/${id}`,
    type: "step",
    agentName: agent,
    modelId: model,
  });
  const plan = [
    { nodeId: "implement", type: "step", agentName: "wf-coder" },
    {
      nodeId: "review",
      type: "sequence",
      steps: [
        {
          nodeId: "review/seats",
          type: "parallel",
          branches: [
            seat("advisor", "advisor", "gpt-5.6-sol"),
            seat("code-adversary-claude", "code-adversary-claude", "claude-opus-5.5"),
          ],
        },
      ],
      includedFrom: { name: "review-seats", path: "./review-seats.workflow.json" },
    },
    { nodeId: "finalize", type: "step", agentName: "wf-coder" },
  ];
  const root = (review: Record<string, unknown>) => ({
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [step("implement", "completed"), review, step("finalize", "pending")],
  });
  const started = {
    nodeId: "review",
    type: "sequence",
    status: "running",
    children: [
      {
        nodeId: "review/seats",
        type: "parallel",
        status: "running",
        children: [
          step("review/advisor", "running", { agentName: "advisor" }),
          step("review/code-adversary-claude", "running"),
        ],
      },
    ],
  };
  const fold = (planIn: unknown, review: Record<string, unknown> = started) =>
    flatten(runToExec(stateWith(root(review)), planIn, NO_ASKS).nodes);
  const byLabel = (nodes: ReturnType<typeof fold>, label: string) => {
    const n = nodes.find((x) => x.label === label);
    if (n === undefined) {
      throw new Error(`no ${label}`);
    }
    return n;
  };

  it("subtitles the group with its recipe and gives it the recipe's name and file", () => {
    const group = byLabel(fold(plan), "review");
    expect(group.subtitle).toBe("from review-seats");
    expect(group.qualifier).toBeUndefined();
    expect(group.facts).toEqual([
      { label: "Recipe", value: "review-seats" },
      { label: "Recipe file", value: "./review-seats.workflow.json", mono: true },
    ]);
  });

  it("qualifies every id inside with the include's prefix, keeping the label verbatim", () => {
    const nodes = fold(plan);
    for (const label of ["review/seats", "review/advisor", "review/code-adversary-claude"]) {
      expect(byLabel(nodes, label).qualifier, label).toBe("review/");
    }
    expect(byLabel(nodes, "implement").qualifier).toBeUndefined();
    expect(byLabel(nodes, "finalize").qualifier).toBeUndefined();
  });

  it("names the recipe among a step's facts, after its structural ones", () => {
    const facts = byLabel(fold(plan), "review/advisor").facts ?? [];
    expect(facts.map((f) => f.label)).toEqual(["Agent", "Recipe"]);
    expect(facts[1]?.value).toBe("review-seats");
    expect(byLabel(fold(plan), "implement").facts).toBeUndefined();
  });

  it("shows the same before the include has started, from its plan", () => {
    const nodes = fold(plan, { nodeId: "review", type: "sequence", status: "pending" });
    expect(byLabel(nodes, "review").subtitle).toBe("from review-seats");
    expect(byLabel(nodes, "review/code-adversary-claude").qualifier).toBe("review/");
  });

  it("reads nothing from an includedFrom it cannot trust", () => {
    for (const bad of [undefined, "review-seats", { name: 3 }, { name: "" }, ["review-seats"]]) {
      const planIn = plan.map((n) => (n.nodeId === "review" ? { ...n, includedFrom: bad } : n));
      const nodes = fold(planIn);
      const group = byLabel(nodes, "review");
      expect(group.subtitle, JSON.stringify(bad)).toBeUndefined();
      expect(group.facts, JSON.stringify(bad)).toBeUndefined();
      expect(byLabel(nodes, "review/advisor").qualifier, JSON.stringify(bad)).toBeUndefined();
    }
  });

  it("keeps a recipe with no path to its name alone", () => {
    const planIn = plan.map((n) =>
      n.nodeId === "review" ? { ...n, includedFrom: { name: "review-seats", path: 7 } } : n,
    );
    expect(byLabel(fold(planIn), "review").facts).toEqual([
      { label: "Recipe", value: "review-seats" },
    ]);
  });

  it("gives a hand-written id holding a slash no qualifier", () => {
    const nodes = flatten(
      runToExec(
        stateWith({
          nodeId: "wf_1",
          type: "sequence",
          status: "running",
          children: [
            {
              nodeId: "a",
              type: "sequence",
              status: "running",
              children: [step("a/b", "running")],
            },
          ],
        }),
        undefined,
        NO_ASKS,
      ).nodes,
    );
    expect(byLabel(nodes, "a/b").qualifier).toBeUndefined();
  });

  it("degrades to the plain group with no plan", () => {
    const nodes = fold(undefined);
    expect(byLabel(nodes, "review").subtitle).toBeUndefined();
    expect(byLabel(nodes, "review/advisor").qualifier).toBeUndefined();
  });

  it("says a parallel runs in parallel and no more, since KAS's plan names no join", () => {
    expect(byLabel(fold(plan), "review/seats").subtitle).toBe("in parallel");
  });
});

// KAS's loop executor reads `extendedMaxIterations ?? maxIterations`, and an extension stores the
// new total on the repeat's state node (`testdata/run-extended-loop.json`, a run extended from 3
// passes to 8 that ran 7).
describe("runToExec reads a loop's budget as the engine does", () => {
  const extended = JSON.parse(extendedLoopRaw) as { state: unknown; nodePlan: unknown };

  function fold(edit: (loop: Record<string, unknown>) => void = () => undefined): ExecNode {
    const state = structuredClone(extended.state) as RunState;
    const loop = state.root?.children?.find((n) => n.nodeId === "build-loop");
    if (loop === undefined) {
      throw new Error("the fixture lost its loop");
    }
    edit(loop as unknown as Record<string, unknown>);
    const node = runToExec(state, extended.nodePlan, NO_ASKS).nodes.find(
      (n) => n.label === "build-loop",
    );
    if (node === undefined) {
      throw new Error("no build-loop node");
    }
    return node;
  }

  const fact = (n: ExecNode, label: string): string | undefined =>
    n.facts?.find((f) => f.label === label)?.value;

  it("takes an extension's total as the budget and states what it added", () => {
    const loop = fold();
    expect([loop.maxPasses, loop.addedPasses]).toEqual([8, 5]);
    expect(loop.subtitle).toBe(
      "up to 8 passes (5 added) \u00b7 until gate.json verdict is APPROVED",
    );
    expect(fact(loop, "Max passes")).toBe("8 (3 planned, 5 added)");
    expect(fact(loop, "Stops when")).toBe(
      "/repo/.agents/tasks/build/gate.json verdict is APPROVED",
    );
  });

  it("keeps the plan's bound with no extension", () => {
    const loop = fold((l) => {
      delete l["extendedMaxIterations"];
    });
    expect([loop.maxPasses, loop.addedPasses]).toEqual([3, undefined]);
    expect(loop.subtitle).toBe("up to 3 passes \u00b7 until gate.json verdict is APPROVED");
    expect(fact(loop, "Max passes")).toBe("3");
  });
});

describe("runToExec words a stop condition in KAS's object form", () => {
  const subtitle = (stopCondition: unknown): string | undefined =>
    runToExec(
      stateWith({
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "loop", type: "repeat", status: "running", children: [] }],
      }),
      [{ nodeId: "loop", type: "repeat", stopCondition, steps: [{ nodeId: "w", type: "step" }] }],
      NO_ASKS,
    ).nodes[0]?.subtitle;

  it("names each part it sets", () => {
    expect(subtitle({ containsText: "PASS" })).toBe('repeats \u00b7 until output contains "PASS"');
    expect(subtitle({ completionSignal: "success" })).toBe(
      "repeats \u00b7 until the step signals success",
    );
    expect(subtitle({ fileCheck: { path: "/t/done.json", jsonPath: "done", value: true } })).toBe(
      "repeats \u00b7 until done.json done is true",
    );
    expect(subtitle({ fileCheck: { path: "/t/v.json", value: ["A", "B"] } })).toBe(
      "repeats \u00b7 until v.json is one of A, B",
    );
  });

  it("says nothing of a shape it cannot read", () => {
    expect(subtitle({ fileCheck: { jsonPath: "done", value: true } })).toBe("repeats");
    expect(subtitle(42)).toBe("repeats");
  });
});

// KAS names the loop a cap pause stopped at in `state.pause` (`{kind: "maxIterations", nodeId,
// maxIterations, iterationsRun}`), and `pauseReason` is its sentence.
describe("the alert of a run paused at a loop's cap", () => {
  const pass = (n: number, status: string, children?: unknown[]) => ({
    nodeId: `loop#${String(n)}`,
    type: "sequence",
    status,
    iteration: n,
    children: children ?? [
      step("code", status),
      step("review", status === "running" ? "paused" : status),
    ],
  });
  const loop = (status: string, passes: unknown[], nodeId = "loop") => ({
    nodeId,
    type: "repeat",
    status,
    children: passes,
  });
  const capPause = (nodeId: string, max: number, ran = max) => ({
    kind: "maxIterations",
    nodeId,
    maxIterations: max,
    iterationsRun: ran,
  });
  const PLAN = [
    {
      nodeId: "loop",
      type: "repeat",
      maxIterations: 3,
      onMaxIterations: "pause",
      steps: [{ nodeId: "code", type: "step" }],
    },
  ];
  const alertFor = (
    children: unknown[],
    pause: Record<string, unknown>,
    plan: unknown = PLAN,
  ): string | undefined =>
    runToExec(
      stateWith(
        { nodeId: "wf_1", type: "sequence", status: "paused", children },
        { status: "paused", ...pause },
      ),
      plan,
      NO_ASKS,
    ).alert?.text;
  const spent = [pass(0, "completed"), pass(1, "completed"), pass(2, "completed")];

  it("keeps the pass count beside KAS's sentence", () => {
    expect(
      alertFor([loop("paused", spent), step("land", "pending")], {
        pauseReason: "Repeat 'loop' reached maxIterations.",
        pause: capPause("loop", 3),
      }),
    ).toBe("Waiting: Repeat 'loop' reached maxIterations. \u00b7 3 of 3 passes");
  });

  it("counts an extended budget as KAS ran it", () => {
    const passes = Array.from({ length: 8 }, (_, i) => pass(i, "completed"));
    expect(
      alertFor([loop("paused", passes), step("land", "pending")], {
        pauseReason: "Repeat 'loop' reached maxIterations.",
        pause: capPause("loop", 8),
      }),
    ).toBe("Waiting: Repeat 'loop' reached maxIterations. \u00b7 8 of 8 passes");
  });

  it("counts nothing for a pause inside a pass", () => {
    const passes = [pass(0, "completed"), pass(1, "completed"), pass(2, "running")];
    expect(
      alertFor([loop("paused", passes), step("land", "pending")], {
        pauseReason: "Interrupted by agent restart",
      }),
    ).toBe("Waiting: Interrupted by agent restart");
  });

  it("counts nothing for a pause of another kind", () => {
    expect(
      alertFor([loop("paused", spent), step("land", "pending")], {
        pauseReason: "Waiting on a step",
        pause: { ...capPause("loop", 3), kind: "stepInput" },
      }),
    ).toBe("Waiting: Waiting on a step");
  });

  it("counts nothing for a pause after a loop that stopped on its last pass", () => {
    expect(
      alertFor([loop("completed", spent), step("land", "paused")], {
        pauseReason: "Step 'land' failed: exit 1",
      }),
    ).toBe("Waiting: Step 'land' failed: exit 1");
  });

  it("counts nothing for a pause after a loop whose cap outcome was continue", () => {
    const plan = [{ ...PLAN[0], onMaxIterations: "continue" }];
    expect(
      alertFor(
        [loop("completed", spent), step("land", "paused")],
        { pauseReason: "Interrupted by agent restart" },
        plan,
      ),
    ).toBe("Waiting: Interrupted by agent restart");
  });

  it("counts the loop KAS paused, not a nested one that spent its own budget", () => {
    const inner = loop("completed", spent, "inner");
    const outer = loop(
      "paused",
      [pass(0, "completed", [inner]), pass(1, "completed", [structuredClone(inner)])],
      "outer",
    );
    const plan = [
      {
        nodeId: "outer",
        type: "repeat",
        maxIterations: 2,
        onMaxIterations: "pause",
        steps: [
          {
            nodeId: "inner",
            type: "repeat",
            maxIterations: 3,
            onMaxIterations: "continue",
            steps: [{ nodeId: "code", type: "step" }],
          },
        ],
      },
    ];
    expect(
      alertFor(
        [outer, step("land", "pending")],
        { pauseReason: "Repeat 'outer' reached maxIterations.", pause: capPause("outer", 2) },
        plan,
      ),
    ).toBe("Waiting: Repeat 'outer' reached maxIterations. \u00b7 2 of 2 passes");
  });
});
