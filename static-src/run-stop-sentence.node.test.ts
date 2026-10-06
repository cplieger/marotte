import { describe, it, expect } from "vitest";
import { pausePendingSentence, userStopSentence, type RunState } from "./run-store.js";

function state(extra: Partial<RunState>): RunState {
  return { workflowId: "wf_1", ...extra };
}

describe("userStopSentence", () => {
  it.each([
    { name: "a cancel", s: { status: "aborted", stopInitiator: "user" }, want: "Stopped by you" },
    {
      name: "a tab close",
      s: { status: "aborted", stopInitiator: "user", stopReason: "tab closed" },
      want: "Stopped by you: tab closed",
    },
    { name: "a pause", s: { status: "paused", stopInitiator: "user" }, want: "Paused by you" },
    {
      name: "a step marked complete",
      s: { status: "completed", stopInitiator: "user" },
      want: "Marked complete by you",
    },
    { name: "a stop nobody asked for", s: { status: "aborted" }, want: undefined },
  ] as const)("words $name", ({ s, want }) => {
    expect(userStopSentence(state(s))).toBe(want);
  });
});

describe("pausePendingSentence", () => {
  it("names a pause the run has not reached yet", () => {
    expect(
      pausePendingSentence(state({ status: "running", pausePending: { initiator: "user" } })),
    ).toBe("Pausing after the current step");
  });

  it("is silent once the pause has landed or none was asked for", () => {
    expect(
      pausePendingSentence(state({ status: "paused", pausePending: { initiator: "user" } })),
    ).toBeUndefined();
    expect(pausePendingSentence(state({ status: "running" }))).toBeUndefined();
  });
});
