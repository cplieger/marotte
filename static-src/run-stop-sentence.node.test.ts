import { readFileSync } from "node:fs";
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

// KAS stamps its parent-run controls `user` with these reasons, and its own classifier names the
// pause the orchestrator's (internal/kascap/parent_control_reason_census_test.go pins both).
const parentReasons = JSON.parse(
  readFileSync(
    new URL("../internal/kascap/testdata/parent_control_reasons.json", import.meta.url),
    "utf8",
  ),
) as { pause: string; resume: string };

describe("the run's chat as the actor, shared with the bundle census", () => {
  it("names the chat, not the reader, for the pause its chat asked for", () => {
    expect(
      stopSentence(
        state({ status: "paused", stopInitiator: "user", stopReason: parentReasons.pause }),
      ),
    ).toBe("Paused by its chat");
  });

  it("names the chat for a stop that kept the chat's pause attribution", () => {
    expect(
      stopSentence(
        state({ status: "aborted", stopInitiator: "user", stopReason: parentReasons.pause }),
      ),
    ).toBe("Stopped by its chat");
  });

  it("names the chat for the resume its chat asked for", () => {
    noteRunResumed("wf_1", "user", parentReasons.resume);
    expect(resumeSentence(state({ status: "running" }))).toBe("Resumed by its chat");
  });

  it.each([
    {
      name: "pause",
      s: { status: "paused", stopReason: parentReasons.pause },
      want: "Paused by bot",
    },
    {
      name: "stop",
      s: { status: "aborted", stopReason: parentReasons.pause },
      want: "Stopped by bot",
    },
  ] as const)(
    "needs KAS's user initiator beside the parent's reason, and never shows it, on a $name",
    ({ s, want }) => {
      expect(stopSentence(state({ ...s, stopInitiator: "bot" }))).toBe(want);
    },
  );

  it("needs KAS's user initiator beside the parent's reason on a resume", () => {
    noteRunResumed("wf_1", "bot", parentReasons.resume);
    expect(resumeSentence(state({ status: "running" }))).toBe("Resumed by bot");
  });

  it("names the reader for a pause with any other reason", () => {
    expect(
      stopSentence(
        state({ status: "paused", stopInitiator: "user", stopReason: "Pause requested by me" }),
      ),
    ).toBe("Paused by you: Pause requested by me");
  });
});

const marotteFixture = JSON.parse(
  readFileSync(new URL("../internal/agent/testdata/pause_reasons.json", import.meta.url), "utf8"),
) as { reasons: string[]; apiCaller: string };
const marotteReasons = marotteFixture.reasons;

// marotte stamps this reason beside `user` on a control its REST API took from outside its page.
describe("an agent as the actor, shared with Go", () => {
  it.each([
    { name: "pause", status: "paused", want: "Paused by an agent" },
    { name: "stop", status: "aborted", want: "Stopped by an agent" },
  ] as const)("names an agent, not the reader, for its $name", ({ status, want }) => {
    expect(
      stopSentence(state({ status, stopInitiator: "user", stopReason: marotteFixture.apiCaller })),
    ).toBe(want);
  });

  it("names an agent for its resume", () => {
    noteRunResumed("wf_1", "user", marotteFixture.apiCaller);
    expect(resumeSentence(state({ status: "running" }))).toBe("Resumed by an agent");
  });

  it("never shows the reason under another initiator", () => {
    expect(
      stopSentence(
        state({ status: "paused", stopInitiator: "bot", stopReason: marotteFixture.apiCaller }),
      ),
    ).toBe("Paused by bot");
  });
});

describe("stopSentence on marotte's own pause, shared with Go", () => {
  it.each(marotteReasons)("names no initiator for %s", (reason) => {
    expect(
      stopSentence(state({ status: "paused", stopInitiator: "user", stopReason: reason })),
    ).toBe(`Paused: ${reason}`);
  });

  // KAS replaces the attribution on an attributed stop, so a stop that kept marotte's reason was
  // nobody's the run can name.
  it.each(marotteReasons)("names nobody for a stop that kept it: %s", (reason) => {
    expect(
      stopSentence(state({ status: "aborted", stopInitiator: "user", stopReason: reason })),
    ).toBeUndefined();
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
  it("names who resumed the run while it runs", () => {
    noteRunResumed("wf_1", "user", "picked back up");
    expect(resumeSentence(state({ status: "running" }))).toBe("Resumed by you: picked back up");
    expect(resumeSentence(state({ status: "paused" }))).toBeUndefined();
  });

  it("is cleared by a start that names no initiator", () => {
    noteRunResumed("wf_1", "user", parentReasons.resume);
    noteRunResumed("wf_1", "", "");
    expect(resumeSentence(state({ status: "running" }))).toBeUndefined();
  });
});
