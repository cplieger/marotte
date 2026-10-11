import { describe, it, expect } from "vitest";
import fc from "fast-check";

import type { TurnSummary } from "./rail-merge.js";
import { binTurns, turnFraction } from "./rail-select.js";
import type { TurnOutcome } from "./turns.js";

function turn(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return { id: `m-${String(n)}`, n, ts: n * 1000, outcome: "completed", ...over };
}

function turns(count: number): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => turn(i + 1));
}

describe("turnFraction", () => {
  it("puts the first slot at 0, the last at 1 and the middle at the half", () => {
    expect(turnFraction(0, 5)).toBe(0);
    expect(turnFraction(4, 5)).toBe(1);
    expect(turnFraction(2, 5)).toBe(0.5);
  });

  it("puts a one-slot map at 0 rather than dividing by zero", () => {
    expect(turnFraction(0, 1)).toBe(0);
  });
});

describe("one row per turn while the track can draw it", () => {
  it("gives each of 5 turns its own row at (n-1)/(total-1)", () => {
    const { slots, bins } = binTurns(turns(5), 400);
    expect(slots).toBe(5);
    expect(bins.map((b) => [b.first.n, b.last.n, b.at])).toEqual([
      [1, 1, 0],
      [2, 2, 0.25],
      [3, 3, 0.5],
      [4, 4, 0.75],
      [5, 5, 1],
    ]);
  });

  it("puts a one-turn session at 0", () => {
    const { slots, bins } = binTurns(turns(1), 400);
    expect(slots).toBe(1);
    expect(bins.map((b) => b.at)).toEqual([0]);
  });

  it("puts a two-turn session at the two ends", () => {
    const { slots, bins } = binTurns(turns(2), 400);
    expect(slots).toBe(2);
    expect(bins.map((b) => [b.first.n, b.at])).toEqual([
      [1, 0],
      [2, 1],
    ]);
  });

  it("gives each of 30 turns its own row at (n-1)/29", () => {
    const { slots, bins } = binTurns(turns(30), 400);
    expect(slots).toBe(30);
    expect(bins).toHaveLength(30);
    expect(bins.every((b) => b.members.length === 1)).toBe(true);
    expect([bins[0]?.at, bins[1]?.at, bins[14]?.at, bins[29]?.at]).toEqual([0, 1 / 29, 14 / 29, 1]);
  });

  it("keeps one row per turn up to exactly two pixels a turn", () => {
    const { bins } = binTurns(turns(200), 400);
    expect(bins).toHaveLength(200);
  });

  it("lays no rows before the track has been measured", () => {
    expect(binTurns(turns(500), 0)).toEqual({ total: 0, slots: 0, bins: [] });
  });

  it("reports the session's last turn number, gaps included", () => {
    expect(binTurns([turn(1), turn(2), turn(9)], 400).total).toBe(9);
    expect(binTurns(turns(500), 600).total).toBe(500);
  });

  it("leaves a fraction gap for a turn number the set does not carry", () => {
    const { slots, bins } = binTurns([turn(1), turn(2), turn(5)], 400);
    expect(slots).toBe(5);
    expect(bins.map((b) => [b.first.n, b.at])).toEqual([
      [1, 0],
      [2, 0.25],
      [5, 1],
    ]);
  });

  it("answers an empty layout for no turns", () => {
    expect(binTurns([], 400)).toEqual({ total: 0, slots: 0, bins: [] });
  });
});

describe("past density, consecutive turns share a row", () => {
  it("bins 1000 turns on a 400px track into rows of 5", () => {
    const { slots, bins } = binTurns(turns(1000), 400);
    expect(slots).toBe(200);
    expect(bins).toHaveLength(200);
    expect([bins[0]?.first.n, bins[0]?.last.n]).toEqual([1, 5]);
    expect([bins[24]?.first.n, bins[24]?.last.n]).toEqual([121, 125]);
  });

  it("grades a bin by its worst member, so one failure in a clean run shows", () => {
    const set = turns(1000);
    set[121] = turn(122, { outcome: "failed" });
    const bin = binTurns(set, 400).bins[24];
    expect(bin?.severity).toBe("broken");
    expect(bin?.target.n).toBe(122);
  });

  it("ranks stopped above running", () => {
    const set = turns(1000);
    set[0] = turn(1, { outcome: "running" });
    set[2] = turn(3, { outcome: "cancelled" });
    const bin = binTurns(set, 400).bins[0];
    expect(bin?.severity).toBe("stopped");
    expect(bin?.target.n).toBe(3);
  });

  it("lands the first member carrying the worst severity", () => {
    const set = turns(1000);
    set[1] = turn(2, { outcome: "refused" });
    set[3] = turn(4, { outcome: "failed" });
    expect(binTurns(set, 400).bins[0]?.target.n).toBe(2);
  });

  it("bins 500 turns on a 600px track into 250 pairs that hold every turn", () => {
    const set = turns(500);
    const { slots, bins } = binTurns(set, 600);
    expect(slots).toBe(250);
    expect(bins).toHaveLength(250);
    expect([bins[0]?.first.n, bins[0]?.last.n, bins[0]?.at]).toEqual([1, 2, 0]);
    expect([bins[249]?.first.n, bins[249]?.last.n, bins[249]?.at]).toEqual([499, 500, 1]);
    expect(bins.flatMap((b) => b.members)).toEqual(set);
  });

  it("lands the first member of a clean bin", () => {
    expect(binTurns(turns(1000), 400).bins[3]?.target.n).toBe(16);
  });
});

describe("the layout's invariants", () => {
  const outcomes: TurnOutcome[] = ["completed", "failed", "running", "cancelled", "unknown"];
  const set = fc
    .uniqueArray(fc.integer({ min: 1, max: 3000 }), { minLength: 1, maxLength: 1200 })
    .chain((nums) =>
      fc.tuple(
        fc.constant([...nums].sort((a, b) => a - b)),
        fc.array(fc.constantFrom(...outcomes), { minLength: nums.length, maxLength: nums.length }),
      ),
    )
    .map(([nums, outs]) => nums.map((n, i) => turn(n, { outcome: outs[i] ?? "completed" })));

  it("drops no turn and draws no row thinner than two pixels", () => {
    fc.assert(
      fc.property(set, fc.integer({ min: 2, max: 2000 }), (input, track) => {
        const { slots, bins } = binTurns(input, track);
        expect(bins.flatMap((b) => b.members.map((m) => m.n))).toEqual(input.map((t) => t.n));
        expect(slots).toBeLessThanOrEqual(Math.max(1, Math.floor(track / 2)));
        expect(new Set(bins.map((b) => b.key)).size).toBe(bins.length);
      }),
    );
  });

  it("keeps every row's position inside 0..1 and ascending", () => {
    fc.assert(
      fc.property(set, fc.integer({ min: 2, max: 2000 }), (input, track) => {
        const ats = binTurns(input, track).bins.map((b) => b.at);
        for (let i = 0; i < ats.length; i++) {
          expect(ats[i]).toBeGreaterThanOrEqual(0);
          expect(ats[i]).toBeLessThanOrEqual(1);
          if (i > 0) {
            expect(ats[i]).toBeGreaterThan(ats[i - 1] ?? -1);
          }
        }
      }),
    );
  });
});
