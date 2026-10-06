// The runtime VOCABULARY half of turn-severity.ts: the exports with no Go twin. The fixture pin is
// `turn-severity.node.test.ts`.
import { describe, it, expect } from "vitest";

import { TURN_OUTCOME_VALUES } from "./turn-severity.js";
import type { TurnOutcome } from "./wire/types.gen.js";

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

describe("TURN_OUTCOME_VALUES", () => {
  it("holds exactly the eight outcomes the wire can send", () => {
    // For the DERIVATION: `Object.keys` over the wrong table type-checks through the cast, and the
    // decoder would admit an out-of-vocabulary outcome.
    expect([...TURN_OUTCOME_VALUES].sort()).toEqual([...EVERY_OUTCOME_SORTED]);
  });
});
