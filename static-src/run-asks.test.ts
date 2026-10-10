import { describe, it, expect } from "vitest";
import { askedSteps, type RunAskAddress, type RunAsks } from "./run-asks.js";
import type { RunNode } from "./run-store.js";

function step(nodeId: string, status: RunNode["status"], sessionId?: string): RunNode {
  return { nodeId, type: "step", status, ...(sessionId === undefined ? {} : { sessionId }) };
}

function tree(...children: RunNode[]): RunNode {
  return { nodeId: "wf_1", type: "parallel", status: "paused", children };
}

function asks(...asked: RunAskAddress[]): RunAsks {
  return { count: asked.length, asked, label: "" };
}

function question(nodeID = "", sessionID = ""): RunAskAddress {
  return { nodeID, sessionID, answer: true };
}

function ids(set: ReadonlySet<RunNode>): string[] {
  return [...set].map((n) => `${n.nodeId}@${n.sessionId ?? ""}`).sort();
}

describe("askedSteps", () => {
  it("answers a question at the step running its session", () => {
    const root = tree(step("a", "paused", "sess_a"), step("b", "paused", "sess_b"));
    const got = askedSteps(root, asks(question("", "sess_b")));
    expect(ids(got.answerable)).toEqual(["b@sess_b"]);
  });

  it("answers a question naming its node at the one paused execution of it", () => {
    const iterations: RunNode = {
      nodeId: "loop",
      type: "repeat",
      status: "paused",
      children: [step("review", "completed", "sess_0"), step("review", "paused", "sess_1")],
    };
    const got = askedSteps(tree(iterations), asks(question("review")));
    expect(ids(got.answerable)).toEqual(["review@sess_1"]);
    expect(ids(got.waiting)).toEqual(["review@sess_1"]);
  });

  it("answers a question naming no node at the run's only paused step", () => {
    const root = tree(step("a", "completed", "sess_a"), step("b", "paused", "sess_b"));
    expect(ids(askedSteps(root, asks(question())).answerable)).toEqual(["b@sess_b"]);
  });

  it("names no step for a question two paused executions could own", () => {
    const root = tree(step("a", "paused", "sess_a"), step("b", "paused", "sess_b"));
    const got = askedSteps(root, asks(question()));
    expect(got.answerable.size).toBe(0);
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
    const got = askedSteps(root, asks(question("", "sess_a")));
    expect(ids(got.waiting)).toEqual(["a@sess_a"]);
    expect(got.answerable.size).toBe(0);
  });
});
