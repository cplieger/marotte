// THE MARKER SET, and the one property that makes it worth having: it is a function
// of its four arguments and of nothing else. A set that moves with the reader is
// presence churn — a marker beside the reading position winking out and back on every
// scroll, with the accessible name's dropped count moving with it.
//
// POSITION IS A FUNCTION OF THE SLOT, not of the turn's number: `k` shown markers sit
// at `k - 1` EQUAL gaps, so a turn the rank dropped leaves no hole where it would have
// sat. The separation invariant is WCAG 2.5.8's, so the properties run over BOTH
// pointer tiers. Node environment: no DOM is reached, which is why `railMetrics` is NOT here —
// it resolves a token against a real track, and a stubbed `getComputedStyle` answering
// a constant is what let it read `1.5rem` as the number 1.5 unnoticed.
// `rail-position-css.test.ts` measures it over real layout instead.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import type { TurnSummary } from "./rail-merge.js";
import {
  maxMarkers,
  railAt,
  railSpan,
  relaxedPitch,
  selectMarkers,
  slotPosition,
} from "./rail-select.js";
import type { TurnOutcome } from "./turns.js";

/** The two tier pitches, from `--hit-floor` plus the 4px clear. */
const FINE = 28;
const COARSE = 48;
const TRACK = 800;
const NO_HITS: ReadonlySet<number> = new Set();

function turn(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return { id: `m-${String(n)}`, n, ts: n * 1000, outcome: "completed", ...over };
}

function turns(count: number, outcome: TurnOutcome = "completed"): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => turn(i + 1, { outcome }));
}

function ns(rows: readonly TurnSummary[]): number[] {
  return rows.map((r) => r.n);
}

/** The marker box the pitch was derived from, which is what the rendered `top`
 *  travels within. */
function markerOf(pitchPx: number): number {
  return pitchPx - 4;
}

describe("position is a function of the marker's slot", () => {
  it("puts the first slot at the top and the last at the foot of the travel", () => {
    // 28 markers want more than an 800px track gives them at the relaxed pitch, so
    // the span is 1 and the set occupies the whole travel.
    expect(railSpan(28, 800, 24)).toBe(1);
    expect(railAt(0, 28, 1)).toBe(0);
    expect(railAt(27, 28, 1)).toBe(1);
    expect(slotPosition(0, 28, 800, 24)).toBe(0);
    expect(slotPosition(27, 28, 800, 24)).toBe(776);
  });

  it("puts a one-marker set at the top rather than dividing by zero", () => {
    expect(railAt(0, 1, railSpan(1, 800, 24))).toBe(0);
    expect(slotPosition(0, 1, 800, 24)).toBe(0);
  });

  for (const tier of [
    { name: "fine", pitchPx: FINE },
    { name: "coarse", pitchPx: COARSE },
  ] as const) {
    it(`lays k slots out at k - 1 equal gaps, each at least the ${tier.name} pitch`, () => {
      // THE EQUIDISTANCE PROPERTY: a position keyed on the turn's own number puts a
      // 3-turn step 1.5 gaps under a 2-turn step, and a dropped turn leaves its slot
      // empty. Keyed on the slot, neither can happen.
      const markerPx = markerOf(tier.pitchPx);
      fc.assert(
        fc.property(
          fc.integer({ min: 200, max: 1200 }),
          fc.integer({ min: 2, max: 60 }),
          (trackPx, want) => {
            const cap = maxMarkers(trackPx, tier.pitchPx);
            const k = Math.max(2, Math.min(want, cap));
            const tops = Array.from({ length: k }, (_, i) => slotPosition(i, k, trackPx, markerPx));
            const first = (tops[1] ?? 0) - (tops[0] ?? 0);
            for (let i = 1; i < k; i++) {
              expect((tops[i] ?? 0) - (tops[i - 1] ?? 0)).toBeCloseTo(first, 9);
            }
            if (cap > 1) {
              expect(first).toBeGreaterThanOrEqual(tier.pitchPx - 1e-9);
            }
          },
        ),
      );
    });
  }
});

// A SET SHORTER THAN THE TRACK is spread from the top at the relaxed pitch, and only
// a set that cannot fit at it takes the whole travel. Before the span the set always
// took the whole travel, so the second of two turns sat at the foot of the column
// beside the resume control with the entire axis empty between them.
describe("the set is spread from the top until it no longer fits", () => {
  /** Float dust between the two regimes' ONE expression. A position is published as a
   *  FRACTION and multiplied back by the travel, so the relaxed regime's
   *  `slot * pitch` is reached as `slot / (slots - 1) * span * travel` and lands a
   *  part in 1e14 either side of it. Five orders of magnitude under a sub-pixel, so it
   *  cannot absorb a real change — and the alternative is a second expression for one
   *  quantity, which is what keeps the arithmetic and the render in step. */
  const DUST = 1e-9;

  /** The gap between two adjacent markers of a `slots`-marker set. */
  function gap(slots: number, trackPx = TRACK, markerPx = 24): number {
    return slotPosition(1, slots, trackPx, markerPx) - slotPosition(0, slots, trackPx, markerPx);
  }

  it("puts the second marker one relaxed pitch under the first, not at the foot of the track", () => {
    expect(relaxedPitch(24)).toBe(48);
    expect(slotPosition(0, 2, TRACK, 24)).toBe(0);
    expect(slotPosition(1, 2, TRACK, 24)).toBe(48);
    // The travel it would have taken with the span pinned at 1, which is where the
    // marker used to land: the whole column below the first turn.
    expect(TRACK - 24).toBe(776);
  });

  it("holds that pitch for every marker of a set that fits", () => {
    const tops = [0, 1, 2, 3, 4].map((i) => slotPosition(i, 5, TRACK, 24));
    expect(tops).toEqual([0, 48, 96, 144, 192]);
  });

  it("tightens the gap once the markers no longer fit, and never past the travel", () => {
    // 17 markers still fit at 48px on a 776px travel; 18 do not, so the set takes the
    // whole travel and the gap goes under the relaxed pitch for the first time.
    expect(gap(17)).toBe(48);
    expect(gap(18)).toBeLessThan(48);
    expect(gap(30)).toBeLessThan(gap(18));
    expect(slotPosition(29, 30, TRACK, 24)).toBe(776);
  });

  it("puts a one-marker set at the top with nothing spread at all", () => {
    expect(railSpan(1, TRACK, 24)).toBe(0);
    expect(slotPosition(0, 1, TRACK, 24)).toBe(0);
  });

  it("answers a track with no travel without dividing by zero", () => {
    // A pre-layout or hidden rail. The fraction is unused there, so it may not be
    // NaN: `calc(NaN * …)` is an invalid declaration the browser drops.
    expect(railSpan(4, 24, 24)).toBe(1);
    expect(slotPosition(3, 4, 24, 24)).toBe(0);
  });

  for (const tier of [
    { name: "fine", pitchPx: FINE },
    { name: "coarse", pitchPx: COARSE },
  ] as const) {
    it(`clears the ${tier.name} tier's own separation floor while relaxed`, () => {
      // The relaxed pitch is the WIDER of the two, so spreading a young session can
      // never put two hit targets closer than WCAG 2.5.8 allows.
      expect(relaxedPitch(markerOf(tier.pitchPx))).toBeGreaterThanOrEqual(tier.pitchPx);
    });

    it(`never opens a gap wider than the relaxed pitch on a ${tier.name} pointer`, () => {
      const markerPx = markerOf(tier.pitchPx);
      fc.assert(
        fc.property(
          fc.integer({ min: 2, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            expect(gap(count, trackPx, markerPx)).toBeLessThanOrEqual(
              relaxedPitch(markerPx) + DUST,
            );
          },
        ),
      );
    });

    it(`never widens a gap as markers arrive on a ${tier.name} pointer`, () => {
      // The reader's own requirement: the gaps tighten as the set grows, so a marker
      // never travels back down the track.
      const markerPx = markerOf(tier.pitchPx);
      fc.assert(
        fc.property(
          fc.integer({ min: 2, max: 400 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            expect(gap(count + 1, trackPx, markerPx)).toBeLessThanOrEqual(
              gap(count, trackPx, markerPx) + DUST,
            );
          },
        ),
      );
    });

    it(`keeps the last marker inside the travel on a ${tier.name} pointer`, () => {
      const markerPx = markerOf(tier.pitchPx);
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            expect(slotPosition(count - 1, count, trackPx, markerPx)).toBeLessThanOrEqual(
              trackPx - markerPx,
            );
          },
        ),
      );
    });
  }
});

describe("the first and last turn always have a marker", () => {
  it("keeps both ends on a session far past the track's capacity", () => {
    const shown = ns(selectMarkers(turns(400), TRACK, FINE, NO_HITS));
    expect(shown[0]).toBe(1);
    expect(shown[shown.length - 1]).toBe(400);
  });

  it("keeps both ends on a track with room for one marker", () => {
    // The one case where two markers can sit closer than the pitch: both ends are
    // kept whatever the capacity says.
    const shown = ns(selectMarkers(turns(9), 60, COARSE, NO_HITS));
    expect(maxMarkers(60, COARSE)).toBe(1);
    expect(shown).toEqual([1, 9]);
  });

  it("answers a one-turn session with that turn, once", () => {
    expect(ns(selectMarkers(turns(1), TRACK, FINE, NO_HITS))).toEqual([1]);
  });

  it("answers an empty set with an empty set", () => {
    expect(selectMarkers([], TRACK, FINE, NO_HITS)).toEqual([]);
  });
});

describe("rank decides which turns take the track's slots", () => {
  it("keeps a turn that did not end clean where the spread had dropped it", () => {
    const clean = turns(400);
    const shown = ns(selectMarkers(clean, TRACK, FINE, NO_HITS));
    expect(shown).not.toContain(150);

    const withFailure = clean.map((t) => (t.n === 150 ? turn(150, { outcome: "failed" }) : t));
    expect(ns(selectMarkers(withFailure, TRACK, FINE, NO_HITS))).toContain(150);
  });

  it("keeps a turn holding a live search hit, and drops it again when the query moves", () => {
    const rows = turns(400);
    expect(ns(selectMarkers(rows, TRACK, FINE, NO_HITS))).not.toContain(150);
    expect(ns(selectMarkers(rows, TRACK, FINE, new Set([150])))).toContain(150);
  });

  it("fills the track's capacity and no more", () => {
    // A 200px track holds four coarse markers, so four of five turns are shown and
    // laid out at one pitch. Judged at the position its turn NUMBER gives it, no pair
    // of these five clears the pitch and only the two ends would survive.
    expect(maxMarkers(200, COARSE)).toBe(4);
    expect(ns(selectMarkers(turns(5), 200, COARSE, NO_HITS))).toEqual([1, 2, 4, 5]);
    // 800px at the fine pitch holds 28, and every one of them is spent.
    expect(maxMarkers(TRACK, FINE)).toBe(28);
    expect(selectMarkers(turns(400), TRACK, FINE, NO_HITS)).toHaveLength(28);
  });

  it("spends the one free slot on rank rather than on the spread", () => {
    // A 150px coarse track holds three markers: both ends and one more, which the
    // spread gives to the middle turn unless a turn outranks it.
    const rows = turns(5);
    expect(maxMarkers(150, COARSE)).toBe(3);
    expect(ns(selectMarkers(rows, 150, COARSE, NO_HITS))).toEqual([1, 3, 5]);
    expect(ns(selectMarkers(rows, 150, COARSE, new Set([2])))).toEqual([1, 2, 5]);
    const failed = rows.map((t) => (t.n === 4 ? turn(4, { outcome: "failed" }) : t));
    expect(ns(selectMarkers(failed, 150, COARSE, NO_HITS))).toEqual([1, 4, 5]);
  });

  it("reports a dropped count on a downsampled session and none on a short one", () => {
    const long = turns(400);
    const shown = selectMarkers(long, TRACK, FINE, NO_HITS);
    expect(long.length - shown.length).toBeGreaterThan(300);

    const short = turns(6);
    expect(selectMarkers(short, TRACK, FINE, NO_HITS)).toHaveLength(6);
  });
});

describe("the SET is independent of scroll position, by signature", () => {
  it("takes exactly four arguments, none of them scroll state", () => {
    // The invariant read off the function itself. A variant that ranked the active
    // turn first would carry a fifth parameter and fail here.
    expect(selectMarkers.length).toBe(4);
  });

  it("answers identically whatever the rail's active turn is", () => {
    // Reached through a signature the production function deliberately does not
    // have, so a variant that DID accept a protected turn would answer differently
    // per active turn and this case would go red.
    const call = selectMarkers as unknown as (
      t: readonly TurnSummary[],
      trackPx: number,
      pitchPx: number,
      hits: ReadonlySet<number>,
      protect?: number,
    ) => TurnSummary[];
    const rows = turns(400);
    const baseline = ns(call(rows, TRACK, FINE, NO_HITS));
    for (const activeN of [1, 7, 150, 151, 200, 399, 400]) {
      expect(ns(call(rows, TRACK, FINE, NO_HITS, activeN))).toEqual(baseline);
    }
  });
});

describe("properties, over both pointer tiers", () => {
  const tiers = [
    { name: "fine", pitchPx: FINE },
    { name: "coarse", pitchPx: COARSE },
  ] as const;

  for (const tier of tiers) {
    it(`selects strictly increasing turns on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            const shown = ns(selectMarkers(turns(count), trackPx, tier.pitchPx, NO_HITS));
            for (let i = 1; i < shown.length; i++) {
              expect(shown[i] ?? 0).toBeGreaterThan(shown[i - 1] ?? 0);
            }
          },
        ),
      );
    });

    it(`lays the shown set out at equal gaps of at least a pitch on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          fc.array(fc.integer({ min: 1, max: 500 }), { maxLength: 12 }),
          (count, trackPx, hitNs) => {
            const shown = selectMarkers(turns(count), trackPx, tier.pitchPx, new Set(hitNs));
            const markerPx = markerOf(tier.pitchPx);
            const tops = shown.map((_, i) => slotPosition(i, shown.length, trackPx, markerPx));
            const first = (tops[1] ?? 0) - (tops[0] ?? 0);
            for (let i = 1; i < tops.length; i++) {
              const gap = (tops[i] ?? 0) - (tops[i - 1] ?? 0);
              expect(gap).toBeCloseTo(first, 9);
              // The one exempt shape: a track with room for a single marker still
              // shows both ends, closer than the pitch.
              expect(gap >= tier.pitchPx || maxMarkers(trackPx, tier.pitchPx) === 1).toBe(true);
            }
          },
        ),
      );
    });

    it(`stays within what the pitch can fit on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          fc.array(fc.integer({ min: 1, max: 500 }), { maxLength: 12 }),
          (count, trackPx, hitNs) => {
            const shown = selectMarkers(turns(count), trackPx, tier.pitchPx, new Set(hitNs));
            // Both ends are guaranteed, so 2 is the floor whatever the capacity.
            expect(shown.length).toBeLessThanOrEqual(
              Math.min(count, Math.max(2, maxMarkers(trackPx, tier.pitchPx))),
            );
          },
        ),
      );
    });

    it(`selects only real turns, in order, on a ${tier.name} pointer`, () => {
      fc.assert(
        fc.property(
          fc.integer({ min: 1, max: 500 }),
          fc.integer({ min: 200, max: 1200 }),
          (count, trackPx) => {
            const rows = turns(count);
            const shown = selectMarkers(rows, trackPx, tier.pitchPx, NO_HITS);
            for (const row of shown) {
              expect(rows).toContain(row);
            }
          },
        ),
      );
    });
  }
});
