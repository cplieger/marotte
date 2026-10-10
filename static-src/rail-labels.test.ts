import { describe, it, expect } from "vitest";

import { binLabel, markerLabel, type MarkerSubject } from "./rail-labels.js";
import type { TurnOutcome } from "./turns.js";

/** Every member of the wire union, spelled out here: importing a list from the module under test
 *  proves nothing about its completeness. */
const ALL_OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
  "empty",
];

/** The word each outcome contributes to the preview, hardcoded. */
const PREVIEW_WORD: Record<TurnOutcome, string> = {
  running: "Running",
  completed: "Completed",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  refused: "Refused",
  unknown: "Unknown",
  failed: "Failed",
  empty: "Empty",
};

const QUIET = { pending: false, hit: false };

function subject(over: Partial<MarkerSubject> = {}): MarkerSubject {
  return { n: 14, outcome: "completed", first_line: "fix the login redirect", ...over };
}

describe("a turn's preview", () => {
  it("leads with the number, the outcome and the duration, then the prompt", () => {
    const label = markerLabel(subject({ outcome: "failed", elapsed_ms: 192_000 }), QUIET);
    expect(label.preview).toBe("#14 \u00b7 Failed \u00b7 3m 12s\nfix the login redirect");
  });

  it("names the outcome word for every outcome, completed included", () => {
    for (const outcome of ALL_OUTCOMES) {
      const first = markerLabel(subject({ outcome }), QUIET).preview.split("\n")[0];
      expect(first, outcome).toBe(`#14 \u00b7 ${PREVIEW_WORD[outcome]}`);
    }
  });

  it("states a duration only when the turn has one", () => {
    expect(markerLabel(subject({ elapsed_ms: 0 }), QUIET).preview).toBe(
      "#14 \u00b7 Completed\nfix the login redirect",
    );
    expect(markerLabel(subject({ elapsed_ms: 4_500 }), QUIET).preview).toBe(
      "#14 \u00b7 Completed \u00b7 4.5s\nfix the login redirect",
    );
  });

  it("cuts a long prompt at 90 code points, counting an astral character once", () => {
    const long = "\u{1F600}" + "a".repeat(120);
    const second = markerLabel(subject({ first_line: long }), QUIET).preview.split("\n")[1] ?? "";
    expect(Array.from(second)).toHaveLength(91);
    expect(second.startsWith("\u{1F600}a")).toBe(true);
    expect(second.endsWith("\u2026")).toBe(true);
  });

  it("keeps a prompt of exactly 90 code points whole", () => {
    const exact = "b".repeat(90);
    expect(markerLabel(subject({ first_line: exact }), QUIET).preview.split("\n")[1]).toBe(exact);
  });

  it("says what an agent-initiated turn is instead of inventing a prompt", () => {
    const label = markerLabel({ n: 3, outcome: "completed", agent_initiated: true }, QUIET);
    expect(label.preview).toBe("#3 \u00b7 Completed\nAgent-initiated turn");
  });

  it("is one line for a user turn with no readable prompt", () => {
    expect(markerLabel({ n: 3, outcome: "completed", first_line: "  " }, QUIET).preview).toBe(
      "#3 \u00b7 Completed",
    );
  });

  it("keeps the transient facts on the first line, loading before the match", () => {
    expect(markerLabel(subject(), { pending: true, hit: true }).preview).toBe(
      "#14 \u00b7 Completed \u00b7 Loading\u2026 \u00b7 Search match\nfix the login redirect",
    );
    expect(markerLabel(subject(), { pending: false, hit: true }).preview).toBe(
      "#14 \u00b7 Completed \u00b7 Search match\nfix the login redirect",
    );
  });
});

describe("a turn's accessible name", () => {
  it("is the number, the outcome and the prompt", () => {
    expect(markerLabel(subject({ outcome: "failed" }), QUIET).ariaLabel).toBe(
      "Turn 14, failed: fix the login redirect",
    );
  });

  it("omits completed, and names every other outcome in lower case", () => {
    for (const outcome of ALL_OUTCOMES) {
      const name = markerLabel(subject({ outcome }), QUIET).ariaLabel;
      const expected =
        outcome === "completed"
          ? "Turn 14: fix the login redirect"
          : `Turn 14, ${PREVIEW_WORD[outcome].toLowerCase()}: fix the login redirect`;
      expect(name, outcome).toBe(expected);
    }
  });

  it("marks an agent-initiated turn and carries no prompt for it", () => {
    expect(markerLabel({ n: 3, outcome: "failed", agent_initiated: true }, QUIET).ariaLabel).toBe(
      "Turn 3, failed, agent-initiated",
    );
  });

  it("keeps the transient facts and the duration out of the name", () => {
    expect(
      markerLabel(subject({ elapsed_ms: 9_000 }), { pending: true, hit: true }).ariaLabel,
    ).toBe("Turn 14: fix the login redirect");
  });
});

describe("a binned row's labels", () => {
  it("names the range and the worst outcome", () => {
    const label = binLabel({ first: 120, last: 124, worst: "failed" }, QUIET);
    expect(label.preview).toBe("Turns 120-124 \u00b7 Failed");
    expect(label.ariaLabel).toBe("Turns 120 to 124, worst failed");
  });

  it("names a clean range by its range alone", () => {
    const label = binLabel({ first: 1, last: 5, worst: "completed" }, QUIET);
    expect(label.preview).toBe("Turns 1-5 \u00b7 Completed");
    expect(label.ariaLabel).toBe("Turns 1 to 5");
  });

  it("says a member holds a search match, in the preview only", () => {
    const label = binLabel(
      { first: 6, last: 10, worst: "completed" },
      { pending: false, hit: true },
    );
    expect(label.preview).toBe("Turns 6-10 \u00b7 Completed \u00b7 Search match");
    expect(label.ariaLabel).toBe("Turns 6 to 10");
  });
});
