// The exports of turn-severity.ts the shared fixture does not pin. The fixture pin is
// `turn-severity.node.test.ts`.
import { describe, it, expect } from "vitest";

import { TURN_OUTCOME_VALUES, mayGradeBroken } from "./turn-severity.js";
import type { EntryTurnClose, TurnOutcome } from "./wire/types.gen.js";

/** Every member of `TurnOutcome`, SORTED. Hardcoded: `decoders.gen.ts`'s `TURN_OUTCOMES` is
 *  module-private. `satisfies` ties it to the union; sorting ignores record order. */
const EVERY_OUTCOME_SORTED = [
  "cancelled",
  "completed",
  "empty",
  "failed",
  "interrupted",
  "refused",
  "running",
  "unknown",
] as const satisfies readonly TurnOutcome[];

describe("mayGradeBroken", () => {
  // Expected values from `stepClose.mayBeBroken` (internal/agent/step_grade.go), the Go owner.
  it.each<[string, EntryTurnClose | undefined, boolean]>([
    ["a failed close", { outcome: "failed" }, true],
    ["an interrupted close", { outcome: "interrupted" }, true],
    ["a completed tool_use stop", { outcome: "completed", stop_reason_raw: "tool_use" }, true],
    [
      "a completed max_turn_requests stop",
      { outcome: "completed", stop_reason_raw: "max_turn_requests" },
      true,
    ],
    ["an unanswered completed close", { outcome: "completed", unanswered: true }, true],
    ["an unanswered empty close", { outcome: "empty", unanswered: true }, true],
    ["an unanswered failed close", { outcome: "failed", unanswered: true }, true],
    ["an unanswered cancelled close", { outcome: "cancelled", unanswered: true }, false],
    ["a completed end_turn stop", { outcome: "completed", stop_reason_raw: "end_turn" }, false],
    ["a completed max_tokens stop", { outcome: "completed", stop_reason_raw: "max_tokens" }, false],
    ["an empty close that answered", { outcome: "empty" }, false],
    ["a cancelled close", { outcome: "cancelled" }, false],
    ["no close", undefined, false],
  ])("%s", (_, close, want) => {
    expect(mayGradeBroken(close)).toBe(want);
  });
});

describe("TURN_OUTCOME_VALUES", () => {
  it("holds exactly the eight outcomes the wire can send", () => {
    // For the DERIVATION: `Object.keys` over the wrong table type-checks through the cast, and the
    // decoder would admit an out-of-vocabulary outcome.
    expect([...TURN_OUTCOME_VALUES].sort()).toEqual([...EVERY_OUTCOME_SORTED]);
  });
});
