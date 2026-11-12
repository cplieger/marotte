import { describe, expect, it } from "vitest";

import { rawWorkflowID, runContentOf, runScopeOf, runTopic } from "./run-content-frame.js";

const RUN = "wf_5y00000000000001";

describe("runContentOf", () => {
  // The frames the server publishes on a run's topic: a run step's log, each naming its run in a
  // top-level `workflow_id`.
  it.each([
    "turn_opened",
    "entry_opened",
    "entry_delta",
    "entry_sealed",
    "entry_appended",
    "turn_closed",
  ])("a run's %s names its run", (type) => {
    expect(runContentOf({ type, chat_id: "", payload: { workflow_id: RUN, turn: "t1" } })).toBe(
      RUN,
    );
  });

  it.each([
    // Broadcasts that carry a workflow id: the run tree, and a chat call's delta adopting the run
    // it launched.
    { type: "run_progress", payload: { workflow_id: RUN } },
    { type: "tool_progress", chat_id: "c1", payload: { workflow_id: RUN, tool_call_id: "x" } },
    { type: "run_finished", payload: { workflow_id: RUN } },
    // A chat's log frame carries no workflow id; a launch card's id sits inside its entry.
    { type: "entry_delta", chat_id: "c1", payload: { turn: "t1" } },
    { type: "entry_appended", chat_id: "c1", payload: { entry: { workflow_id: RUN } } },
  ])("a broadcast names no run: $type", (envelope) => {
    expect(runContentOf(envelope)).toBe("");
  });

  it.each([
    null,
    "entry_delta",
    42,
    { type: "entry_delta" },
    { type: "entry_delta", payload: null },
    { type: "entry_delta", payload: { workflow_id: 7 } },
    { type: 3, payload: { workflow_id: RUN } },
  ])("a malformed envelope names no run: %j", (envelope) => {
    expect(runContentOf(envelope)).toBe("");
  });
});

describe("rawWorkflowID", () => {
  it("reads the payload's own workflow_id, nothing nested", () => {
    expect(rawWorkflowID({ workflow_id: RUN })).toBe(RUN);
    expect(rawWorkflowID({ entry: { workflow_id: RUN } })).toBe("");
    expect(rawWorkflowID({ workflow_id: ["x"] })).toBe("");
    expect(rawWorkflowID(undefined)).toBe("");
  });
});

describe("runScopeOf", () => {
  it("scopes a run tab to its run's topic", () => {
    expect(runTopic(RUN)).toBe(`run:${RUN}`);
    expect(runScopeOf(RUN)).toEqual([`run:${RUN}`]);
  });

  it("a page showing no run draws broadcasts only", () => {
    expect(runScopeOf("")).toEqual([]);
  });

  it.each(["a/b", "run id", 'q"', "x".repeat(129)])(
    "an id no run can carry draws broadcasts only: %s",
    (id) => {
      expect(runScopeOf(id)).toEqual([]);
    },
  );

  // A topic is at most 128 bytes, and the prefix takes four of them.
  it("the longest id a topic can name is scoped to it", () => {
    const id = "a".repeat(124);
    expect(runScopeOf(id)).toEqual([`run:${id}`]);
  });

  it("a run id too long for a topic draws everything", () => {
    expect(runScopeOf("a".repeat(125))).toBeNull();
  });
});
