// The TypeScript half of the turn-severity cross-language pin; the Go half is
// `TestTurnSeverityContract` (internal/marotte/turns_test.go), over ONE fixture, so a rule changed
// in one language fails in the other. `.node`: a disk read.
import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";

import { severityOf, isBroken, defaultFailureReason } from "./turn-severity.js";
import type { TurnOutcome } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/marotte/testdata/turn_severity.json";

interface SeverityCase {
  readonly outcome: string;
  readonly severity: string;
  readonly default_reason: string;
}

function loadCases(): readonly SeverityCase[] {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const parsed: unknown = JSON.parse(raw);
  if (typeof parsed !== "object" || parsed === null) {
    throw new Error(`${FIXTURE_PATH} is not an object`);
  }
  const cases = (parsed as Record<string, unknown>)["cases"];
  if (!Array.isArray(cases) || cases.length === 0) {
    throw new Error(`${FIXTURE_PATH} has no cases`);
  }
  return cases as readonly SeverityCase[];
}

/** Every value the generated `TurnOutcome` union can hold, hand-listed (a union is not enumerable),
 *  so list, union and fixture must agree. `satisfies` ties it to the generated type. */
const EVERY_OUTCOME = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "failed",
  "refused",
  "unknown",
  "empty",
] as const satisfies readonly TurnOutcome[];

describe("the severity table agrees with the Go implementation", () => {
  it("grades every fixture row the same way", () => {
    for (const c of loadCases()) {
      const outcome = c.outcome as TurnOutcome;
      expect(severityOf(outcome), `severityOf(${c.outcome})`).toBe(c.severity);
      expect(defaultFailureReason(outcome), `defaultFailureReason(${c.outcome})`).toBe(
        c.default_reason,
      );
    }
  });

  it("covers every outcome the wire can send", () => {
    // The other direction, and it is what makes the table a contract rather than a
    // sample: a ninth outcome with no row reaches five surfaces that must be
    // total over the severity, so it fails here instead of there.
    const rows = new Set(loadCases().map((c) => c.outcome));
    for (const outcome of EVERY_OUTCOME) {
      expect(rows.has(outcome), `${outcome} has a fixture row`).toBe(true);
      rows.delete(outcome);
    }
    expect([...rows], "fixture rows naming no declared outcome").toEqual([]);
  });
});

describe("severityOf", () => {
  it("never grades anything but a completed turn as clean", () => {
    // The one direction a status mark must not fail in, and the defect the whole
    // module exists to remove: `interrupted` graded as nothing, so a broken turn
    // painted the hollow ring that means nothing is happening here.
    for (const outcome of EVERY_OUTCOME) {
      if (outcome === "completed") {
        continue;
      }
      expect(severityOf(outcome), `${outcome} must not read clean`).not.toBe("clean");
    }
    expect(severityOf("completed")).toBe("clean");
  });

  it("grades an absent outcome as stopped, not clean", () => {
    // A turn projected from a transcript with no durable outcome (every record
    // written before the field existed) reaches here as undefined. Stopped is the
    // safe answer for `unknown`'s reason; clean would claim it worked.
    expect(severityOf(undefined)).toBe("stopped");
  });

  it("reads a value the wire adds later as stopped rather than clean", () => {
    // The decoder makes this unreachable; the arm keeps the failure DIRECTION safe. The cast expresses
    // an input the type forbids.
    expect(severityOf("teleported" as TurnOutcome)).toBe("stopped");
  });
});

describe("isBroken", () => {
  it("answers true for exactly the three broken outcomes", () => {
    const broken = EVERY_OUTCOME.filter((o) => isBroken(o));
    expect(broken).toEqual(["interrupted", "failed", "refused"]);
  });
});

describe("defaultFailureReason", () => {
  // Keyed on the OUTCOME rather than the severity, because `severityOf` grades
  // `cancelled` and `unknown` alike and only one of them still speaks.
  const SPEAKS = new Set<TurnOutcome>(["interrupted", "failed", "refused", "unknown"]);

  it("gives every turn that ended badly something to say", () => {
    // This is the property symptom 1 turned on: a card with a red mark and an
    // empty body is what a reader of the reported chat actually got.
    for (const outcome of EVERY_OUTCOME) {
      if (SPEAKS.has(outcome)) {
        expect(defaultFailureReason(outcome), `${outcome} says something`).not.toBe("");
      } else {
        expect(defaultFailureReason(outcome), `${outcome} says nothing`).toBe("");
      }
    }
  });

  // A cancel is the reader's own gesture and the footer's outcome word already
  // reads "Cancelled" a row away, so a sentence here is one fact rendered twice.
  it("says nothing for a cancelled turn", () => {
    expect(defaultFailureReason("cancelled")).toBe("");
  });
});
