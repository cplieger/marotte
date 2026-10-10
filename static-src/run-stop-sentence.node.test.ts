import { describe, it, expect } from "vitest";
import {
  noteRunResumed,
  pausePendingSentence,
  resumeSentence,
  stopSentence,
  type RunState,
} from "./run-store.js";

function state(extra: Partial<RunState>): RunState {
  return { workflowId: "wf_1", ...extra };
}

describe("stopSentence", () => {
  it.each([
    { name: "a cancel", s: { status: "aborted", stopInitiator: "user" }, want: "Stopped by user" },
    {
      name: "a tab close",
      s: { status: "aborted", stopInitiator: "user", stopReason: "tab closed" },
      want: "Stopped by user: tab closed",
    },
    { name: "a pause", s: { status: "paused", stopInitiator: "user" }, want: "Paused by user" },
    {
      name: "a step marked complete",
      s: { status: "completed", stopInitiator: "user" },
      want: "Marked complete by user",
    },
    {
      name: "an orchestrator's pause, KAS's initiator and sentence as given",
      s: {
        status: "paused",
        stopInitiator: "user",
        stopReason: "Pause requested by owning parent/orchestrator; human intent not verified.",
      },
      want: "Paused by user: Pause requested by owning parent/orchestrator; human intent not verified.",
    },
    {
      name: "an initiator other than a user",
      s: { status: "aborted", stopInitiator: "parent" },
      want: "Stopped by parent",
    },
    { name: "a stop nobody asked for", s: { status: "aborted" }, want: undefined },
  ] as const)("words $name", ({ s, want }) => {
    expect(stopSentence(state(s))).toBe(want);
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

describe("resumeSentence", () => {
  const why = "Resume requested by owning parent/orchestrator; human intent not verified.";

  it("shows KAS's resume initiator and sentence as given while the run runs", () => {
    noteRunResumed("wf_1", "user", why);
    expect(resumeSentence(state({ status: "running" }))).toBe(`Resumed by user: ${why}`);
    expect(resumeSentence(state({ status: "paused" }))).toBeUndefined();
  });

  it("is cleared by a start that names no initiator", () => {
    noteRunResumed("wf_1", "user", why);
    noteRunResumed("wf_1", "", "");
    expect(resumeSentence(state({ status: "running" }))).toBeUndefined();
  });
});
