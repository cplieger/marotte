// ---------------------------------------------------------------------------
// The context breakdown's summarized-message count.
//
// The subject is what this module HANDS the renderer, so status.ts is replaced
// and the captured argument is the assertion surface. That is also the contract
// under test: `summarizedCount` absent means "this window cannot say", and the
// renderer prints nothing for it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Entry, EntryKind, Session, TurnState } from "./types.js";

const mocks = vi.hoisted(() => ({
  updateContextBar: vi.fn(),
  getActiveId: vi.fn(() => "other-chat"),
}));

vi.mock("./status.js", () => ({ updateContextBar: mocks.updateContextBar }));
vi.mock("./store.js", () => ({ getActiveId: mocks.getActiveId }));
vi.mock("./prompt-input.js", () => ({ contextFull: { value: false } }));
vi.mock("./effort.js", () => ({ effortPillLabel: () => "" }));
vi.mock("./picker.js", () => ({ getCachedModels: () => [] }));
vi.mock("./session-context.js", () => ({ getLastEffortFor: () => "" }));

import { refreshContextUI } from "./context-ui.js";

/** One sealed entry of a turn. `seq` is its position, which `entryRenders` reads for
 *  the plan rule, and `id` is what a compaction watermark names. */
function entry(turn: string, seq: number, kind: EntryKind, id: string): Entry {
  return { id, turn, lane: "", kind, payload: {}, seq, ts: 0 };
}

/** One turn built from its kinds, `turn_open` first, so a fixture reads as the shape
 *  the appender wrote. The entry ids are the case's own (`m1`, `m2`, …) and the
 *  `turn_open` is `<turn>-open`, because no case names it: it renders for nobody and is
 *  in neither total. */
function turn(id: string, ...rows: readonly [EntryKind, string][]): TurnState {
  const entries = [
    entry(id, 0, "turn_open", `${id}-open`),
    ...rows.map(([kind, entryID], i) => entry(id, i + 1, kind, entryID)),
  ];
  return { entries, openEntries: new Map() };
}

/** A window of THREE reachable text entries in one turn, which is what every case
 *  that does not name its own window is measured against. */
function oneTurnWindow(): Pick<Session, "turns" | "turn_order"> {
  return {
    turns: new Map([["t1", turn("t1", ["text", "m1"], ["text", "m2"], ["text", "m3"])]]),
    turn_order: ["t1"],
  };
}

function session(over: Partial<Session> = {}): Session {
  return {
    id: "c-1",
    model: "",
    // The count is the header's; the pill reads `s.turn_count` and nothing writes
    // one onto `usage`, so a fixture carrying it there paints String(undefined).
    turn_count: 1,
    ...oneTurnWindow(),
    usage: {
      context_pct: 10,
      context_size: 200_000,
      credits: 0,
      last_turn_ms: 0,
    },
    ...over,
  } as Session;
}

/** The one `updateContextBar` argument this refresh produced. */
function bar(over: Partial<Session> = {}): Record<string, unknown> {
  refreshContextUI(session(over));
  expect(mocks.updateContextBar).toHaveBeenCalledTimes(1);
  return mocks.updateContextBar.mock.calls[0]?.[0] as Record<string, unknown>;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("the summarized-message count", () => {
  it("counts up to and including the watermark message", () => {
    expect(bar({ compaction_watermark: "m2" })["summarizedCount"]).toBe(2);
  });

  it("counts every message when the watermark is the last one", () => {
    expect(bar({ compaction_watermark: "m3" })["summarizedCount"]).toBe(3);
  });

  it("reports zero when the chat carries no watermark", () => {
    expect(bar()["summarizedCount"]).toBe(0);
  });

  // THE DEFECT: the watermark rides the chat header while the window is a PAGED
  // slice of the log, so a chat compacted before its oldest resident entry names one
  // that is not here. Counting while scanning reported all three.
  it("withholds the count when the watermark is not in the loaded window", () => {
    const b = bar({ compaction_watermark: "m0-paged-out" });
    expect(b).not.toHaveProperty("summarizedCount");
    expect(b["entryCount"]).toBe(3);
  });

  // Withholding, not zero: zero says "nothing was summarized" on a chat that was
  // compacted, which is the same wrong readout with the sign flipped.
  it("does not report the withheld count as zero", () => {
    expect(bar({ compaction_watermark: "m0-paged-out" })["summarizedCount"]).not.toBe(0);
  });

  // A rewind truncates the messages and leaves the watermark naming a message
  // that no longer exists anywhere, for the life of the chat.
  it("withholds the count after a rewind past the compaction point", () => {
    expect(
      bar({
        compaction_watermark: "m2",
        turns: new Map([["t1", turn("t1", ["text", "m1"])]]),
        turn_order: ["t1"],
      }),
    ).not.toHaveProperty("summarizedCount");
  });

  it("counts nothing summarized in an empty window with no watermark", () => {
    expect(bar({ turns: new Map(), turn_order: [] })["summarizedCount"]).toBe(0);
  });

  // A `turn_open`, a `turn_close` and a `tool_result` are REACHED by the boundary scan
  // and counted by neither total, which is what keeps the pair status.ts renders as one
  // string honest: the watermark is FOUND on such a row rather than lost, and the count
  // it reports is the rows a reader can see.
  it("finds a watermark on a row nothing draws without counting it", () => {
    const b = bar({
      compaction_watermark: "res1",
      turns: new Map([
        [
          "t1",
          turn("t1", ["text", "m1"], ["tool_call", "tc1"], ["tool_result", "res1"], ["text", "m2"]),
        ],
      ]),
      turn_order: ["t1"],
    });
    expect(b["summarizedCount"]).toBe(2);
    expect(b["entryCount"]).toBe(3);
  });

  // A compaction that landed MID-TURN. It no longer splits the turn — a `compaction`
  // is an ENTRY appended where it happened, so one turn holds
  // [turn_open, text, compaction, text] — and the watermark names that entry. The text
  // before it and the compaction row itself count as summarized, which is true: they
  // are what the summary replaced. The text after it does not.
  it("counts the entries up to and including a mid-turn compaction", () => {
    const b = bar({
      compaction_watermark: "cmp",
      turns: new Map([
        ["t1", turn("t1", ["text", "seg1"], ["compaction", "cmp"], ["text", "seg2"])],
      ]),
      turn_order: ["t1"],
    });
    expect(b["summarizedCount"]).toBe(2);
    expect(b["entryCount"]).toBe(3);
  });
});
