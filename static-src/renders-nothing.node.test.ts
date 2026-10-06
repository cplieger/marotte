// `rendersNothing` is messages.ts's bodyless test: the question is EXISTENCE, and a
// count answers it by pricing every entry. The oracle is equality with
// `turnCost(t).entries === 0`, which holds while the predicate consults `entryRenders`
// and breaks the moment it hardcodes a kind list of its own. The kind SET is part of it
// (a `Record<EntryKind, true>` fails `typecheck:tests` when the wire gains a kind), and
// two arms are pinned WITHOUT `turnCost`, because a predicate and a price can agree on a
// wrong answer.
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { rendersNothing, turnCost } from "./block-window.js";
import { makeTurn } from "./__test-helpers__/model.js";
import type { Turn } from "./turns.js";
import type { Entry, EntryKind } from "./wire/types.gen.js";

const KIND_SET: Record<EntryKind, true> = {
  turn_open: true,
  turn_bind: true,
  text: true,
  thinking: true,
  tool_call: true,
  tool_result: true,
  steer: true,
  steer_ack: true,
  plan: true,
  compaction: true,
  compaction_failed: true,
  safety_blocked: true,
  model_switched: true,
  mode_switched: true,
  turn_revert: true,
  reconciled: true,
  turn_close: true,
};

const KINDS = Object.keys(KIND_SET) as readonly EntryKind[];
const LANES = ["", "sub-a"] as const;

const bodyArb = fc.array(
  fc.record({ kind: fc.constantFrom(...KINDS), lane: fc.constantFrom(...LANES) }),
  { maxLength: 8 },
);

/** `body` whose entries past the first THROW when read, so a scan that does not stop at
 *  the first rendering entry fails rather than merely costing more. */
function poisonedAfterFirst(entries: readonly Entry[]): Entry[] {
  return new Proxy([...entries], {
    get(target, prop, recv): unknown {
      if (typeof prop === "string" && /^[0-9]+$/.test(prop) && Number(prop) > 0) {
        throw new Error(`read body[${prop}] past a rendering entry`);
      }
      return Reflect.get(target, prop, recv);
    },
  });
}

describe("rendersNothing", () => {
  it("agrees with the count it replaced, over every kind and both lanes", () => {
    fc.assert(
      fc.property(bodyArb, fc.constantFrom(...LANES), (body, lane) => {
        const t = makeTurn({ body });
        expect(rendersNothing(t, lane)).toBe(turnCost(t, lane).entries === 0);
      }),
    );
  });

  it("answers TRUE for a turn whose only body entry is a reconciled", () => {
    // Pinned independently of the equality: a `reconciled` is a fact about the RECORD
    // and draws nowhere. A failure here is phase 2's `entryRenders` edit missing, which
    // the equality property would pass over.
    expect(rendersNothing(makeTurn({ body: [{ kind: "reconciled" }] }))).toBe(true);
  });

  it("answers FALSE for a turn whose only body entry is a turn_revert", () => {
    // Decision 3: a revert is a visible boundary row.
    expect(rendersNothing(makeTurn({ body: [{ kind: "turn_revert" }] }))).toBe(false);
  });

  it("stops at the first rendering entry", () => {
    const t = makeTurn({ body: [{ kind: "text" }, { kind: "text" }] });
    const guarded: Turn = { ...t, body: poisonedAfterFirst(t.body) };
    expect(rendersNothing(guarded)).toBe(false);
    // The fixture can fail: the count this predicate replaced reads the whole body.
    expect(() => turnCost(guarded)).toThrow("read body[1]");
  });
});
