import { afterEach, describe, it, expect } from "vitest";
import { join } from "@cplieger/keyenc";
import {
  _resetRunAsksForTest,
  allRunAsks,
  applyRunAsks,
  askedSteps,
  dropAllRunAsks,
  runAskPosition,
  runAsksOf,
  type RunAskAddress,
  type RunAsks,
} from "./run-asks.js";
import type { RunNode } from "./run-store.js";
import type { RunAsk, RunAsksPayload } from "./wire/types.gen.js";

function step(nodeId: string, status: RunNode["status"], sessionId?: string): RunNode {
  return { nodeId, type: "step", status, ...(sessionId === undefined ? {} : { sessionId }) };
}

function tree(...children: RunNode[]): RunNode {
  return { nodeId: "wf_1", type: "parallel", status: "paused", children };
}

function asks(...asked: RunAskAddress[]): RunAsks {
  return { count: asked.length, asked, label: "" };
}

function question(path: readonly string[], sessionID = "", askID = "ask-1"): RunAskAddress {
  return { nodeID: path.at(-1) ?? "", sessionID, answer: true, path: join(...path), askID };
}

function ids(nodes: Iterable<RunNode>): string[] {
  return [...nodes].map((n) => `${n.nodeId}@${n.sessionId ?? ""}`).sort();
}

describe("askedSteps", () => {
  it("answers a question at the step running its session, under that question's id", () => {
    const root = tree(step("a", "paused", "sess_a"), step("b", "paused", "sess_b"));
    const got = askedSteps(root, asks(question(["wf_1", "b"], "sess_b", "ask-b")));
    expect(ids(got.answerable.keys())).toEqual(["b@sess_b"]);
    expect([...got.answerable.values()]).toEqual(["ask-b"]);
  });

  it("finds a question by its path when the tree carries no session for it", () => {
    const iterations: RunNode = {
      nodeId: "loop",
      type: "repeat",
      status: "paused",
      children: [
        {
          ...step("pass", "completed"),
          type: "sequence",
          iteration: 0,
          children: [step("review", "completed")],
        },
        {
          ...step("pass", "paused"),
          type: "sequence",
          iteration: 1,
          children: [step("review", "paused")],
        },
      ],
    };
    const got = askedSteps(tree(iterations), asks(question(["wf_1", "loop", "iter-1", "review"])));
    expect(got.waiting.size).toBe(1);
    const [hit] = [...got.waiting];
    expect(hit).toBe(iterations.children?.[1]?.children?.[0]);
  });

  it("names no step for a path two executions share", () => {
    const root = tree(step("a", "paused"), step("a", "paused"));
    const got = askedSteps(root, asks(question(["wf_1", "a"])));
    expect(got.waiting.size).toBe(0);
  });

  it("never matches a step question by its node id alone", () => {
    const root = tree(step("a", "paused", "sess_a"));
    const got = askedSteps(root, asks({ nodeID: "a", sessionID: "", answer: true }));
    expect(got.waiting.size).toBe(0);
  });

  it("shows a running step's permission ask as waiting, never as a question to answer", () => {
    const root = tree(step("a", "running", "sess_a"));
    const got = askedSteps(root, asks({ nodeID: "a", sessionID: "", answer: false }));
    expect(ids(got.waiting)).toEqual(["a@sess_a"]);
    expect(got.answerable.size).toBe(0);
  });

  it("leaves an ask naming neither a node nor a session on no step", () => {
    const root = tree(step("a", "running", "sess_a"));
    const got = askedSteps(root, asks({ nodeID: "", sessionID: "", answer: false }));
    expect(got.waiting.size).toBe(0);
  });

  it("does not answer at a step that is running again", () => {
    const root = tree(step("a", "running", "sess_a"));
    const got = askedSteps(root, asks(question(["wf_1", "a"], "sess_a")));
    expect(ids(got.waiting)).toEqual(["a@sess_a"]);
    expect(got.answerable.size).toBe(0);
  });
});

function ask(id: string, path: string[] = ["wf_1", "a"], session = "sess_a"): RunAsk {
  return {
    ask_id: id,
    node_id: path.at(-1) ?? "",
    node_path: path,
    step_session_id: session,
    agent_name: "",
    text: "Which branch?",
    text_source: "asked",
    can_continue: false,
  };
}

function frame(seq: number, list: RunAsk[], over: Partial<RunAsksPayload> = {}): RunAsksPayload {
  return {
    workflow_id: "wf_1",
    chat_id: "c1",
    epoch: "e1",
    seq,
    reachable: true,
    asks: list,
    ...over,
  };
}

describe("the run-asks store", () => {
  afterEach(() => {
    _resetRunAsksForTest();
  });

  it("replaces a run's snapshot whole with a newer seq and drops an older or equal one", () => {
    expect(applyRunAsks(frame(2, [ask("A")]))).toBe(true);
    expect(applyRunAsks(frame(2, [ask("B")]))).toBe(false);
    expect(applyRunAsks(frame(1, []))).toBe(false);
    expect(runAsksOf("wf_1")?.asks.map((a) => a.ask_id)).toEqual(["A"]);
    expect(applyRunAsks(frame(3, [ask("B")]))).toBe(true);
    expect(runAsksOf("wf_1")?.asks.map((a) => a.ask_id)).toEqual(["B"]);
  });

  it("takes any seq from another epoch", () => {
    applyRunAsks(frame(9, [ask("A")]));
    expect(applyRunAsks(frame(1, [ask("B")], { epoch: "e2" }))).toBe(true);
    expect(runAsksOf("wf_1")?.asks.map((a) => a.ask_id)).toEqual(["B"]);
  });

  it("forgets a run on its final frame, and a later frame of it starts it again", () => {
    applyRunAsks(frame(1, [ask("A")]));
    expect(applyRunAsks(frame(2, []))).toBe(true);
    expect(runAsksOf("wf_1")).toBeUndefined();
    expect(applyRunAsks(frame(3, [ask("B")]))).toBe(true);
    expect(runAsksOf("wf_1")?.asks.map((a) => a.ask_id)).toEqual(["B"]);
  });

  it("keeps a run marotte could not read, so its banners stay", () => {
    applyRunAsks(frame(1, [], { reachable: false }));
    expect(runAsksOf("wf_1")?.reachable).toBe(false);
  });

  it("drops every run for a pending_snapshot", () => {
    applyRunAsks(frame(1, [ask("A")]));
    applyRunAsks(frame(2, [ask("B")], { workflow_id: "wf_2" }));
    dropAllRunAsks();
    expect(allRunAsks()).toEqual([]);
  });

  it("spells a wait's position as the server's workflow.AskPosition", () => {
    const a = ask("A", ["wf_1", "loop", "iter-0", "a:b"], "sess:1");
    expect(runAskPosition("wf_1", a)).toBe(
      join("wf_1", join("wf_1", "loop", "iter-0", "a:b"), "sess:1"),
    );
  });
});
