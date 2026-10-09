// The site audit as an assertion, and the guard that keeps its list CLOSED.
import { describe, it, expect, vi } from "vitest";

import { manifestSheets } from "./__test-helpers__/css-rules.js";

vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: Browser Mode links for real rather than
  // reading properties off a namespace object, and no path here opens a diff.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: undefined,
}));

const { buildTurnFooter } = await import("./fundamentals/turn-footer.js");

describe("the durable channel", () => {
  // The turn card's own panel carries the value on every device, beside the map's preview.
  it("carries a delegate's own duration in the delegate footer's panel", () => {
    // One shared builder, so the delegate footer gains the rows with no second mechanism — which
    // matters because a leaf delegate's head is a link, so a hover-gated readout there would have
    // no non-navigating way in.
    const footer = buildTurnFooter({ elapsedMs: 12_000 });
    const rows = [...footer.querySelectorAll(".turn-info-row")].map((r) => r.textContent);
    expect(rows).toContain("Wall clock12.0s");
  });
});

/** A class name shaped like a duration slot. Deliberately wider than the audit's own vocabulary,
 *  because the guard's job is catching the NEXT slot rather than the ones the audit already
 *  names. */
const DURATION_SHAPED = /elapsed|duration|timings|dur|time|gap/iu;

/** The audited list, CLOSED. Every member carries the row that rules on it, so adding a
 *  class here without a row is visibly the wrong move. */
const RULED = new Map<string, string>([
  // No turn-map class: the map reports no pause, and a turn's duration is its preview text.
  // `duration_ms` still travels — the turn ledger and the delegate footer sum it — and no surface
  // paints it per call. Named OUT of scope: each is a property of a run or a workflow step, and the
  // reader opened that card to read exactly it.
  ["run-step-dur", "out of scope — the run card"],
  ["ev-dur", "out of scope — the exec view"],
  ["ev-d-dur", "out of scope — the exec view"],
  ["ev-tl-dur", "out of scope — the exec view"],
  // Row 8's rule applied elsewhere: an absolute timestamp is not a duration.
  ["sched-time", "wall clock, not a duration"],
  ["entry-time", "wall clock, not a duration — and painted at rest on every device"],
]);

describe("the closed list stays closed", () => {
  it("sweeps every stylesheet the MANIFEST declares", () => {
    // The manifest is READ, never restated: a count asserted here would be wrong the next time a
    // slice is added, and the sweep would silently stop covering it.
    const sheets = manifestSheets();
    expect(sheets.length).toBeGreaterThan(1);
    expect(sheets.filter((s) => s.css !== "")).toHaveLength(sheets.length);
  });

  it("finds no duration-bearing selector outside the list", () => {
    const unruled: string[] = [];
    for (const { name, css } of manifestSheets()) {
      const text = css.replace(/\/\*[\s\S]*?\*\//gu, " ");
      for (const [, cls] of text.matchAll(/\.(-?[A-Za-z_][\w-]*)/gu)) {
        if (cls !== undefined && DURATION_SHAPED.test(cls) && !RULED.has(cls)) {
          unruled.push(`${name}: .${cls}`);
        }
      }
    }
    expect([...new Set(unruled)].sort()).toEqual([]);
  });

  it("keeps every ruled class in the bundle, so the list cannot rot", () => {
    const all = manifestSheets()
      .map((s) => s.css.replace(/\/\*[\s\S]*?\*\//gu, " "))
      .join("\n");
    const declared = new Set([...all.matchAll(/\.(-?[A-Za-z_][\w-]*)/gu)].map(([, c]) => c));
    expect([...RULED.keys()].filter((c) => !declared.has(c))).toEqual([]);
  });
});
