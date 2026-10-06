// The summarized-message count this module hands the renderer (status.ts is replaced; its captured argument is the
// surface). An absent `summarizedCount` means "this window cannot say", and the renderer prints nothing for it.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Entry, EntryKind, Session, TurnState } from "./types.js";

const mocks = vi.hoisted(() => ({
  updateContextBar: vi.fn(),
}));

vi.mock("./status.js", () => ({ updateContextBar: mocks.updateContextBar }));
vi.mock("./effort.js", () => ({ effortPillLabel: () => "" }));
vi.mock("./picker.js", () => ({ getCachedModels: () => [] }));
vi.mock("./session-context.js", () => ({ getLastEffortFor: () => "" }));

import { refreshContextUI } from "./context-ui.js";
import { compactionPolicy } from "./context-ring.js";
import { effect } from "@cplieger/reactive";

/** `seq` feeds `entryRenders`' plan rule; `id` is what a compaction watermark names. */
function entry(turn: string, seq: number, kind: EntryKind, id: string): Entry {
  return { id, turn, lane: "", kind, payload: {}, seq, ts: 0 };
}

/** Entry ids are the case's own; the `turn_open` is `<turn>-open`, which renders for nobody and is in neither total. */
function turn(id: string, ...rows: readonly [EntryKind, string][]): TurnState {
  const entries = [
    entry(id, 0, "turn_open", `${id}-open`),
    ...rows.map(([kind, entryID], i) => entry(id, i + 1, kind, entryID)),
  ];
  return { entries, openEntries: new Map() };
}

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
    // The count is the header's: the pill reads `s.turn_count`, so a fixture carrying it on `usage` paints undefined.
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

  // The watermark rides the chat header while the window is a paged slice, so it can name an entry not resident here.
  it("withholds the count when the watermark is not in the loaded window", () => {
    const b = bar({ compaction_watermark: "m0-paged-out" });
    expect(b).not.toHaveProperty("summarizedCount");
    expect(b["entryCount"]).toBe(3);
  });

  // Withheld, not zero: zero says "nothing was summarized" on a chat that was compacted.
  it("does not report the withheld count as zero", () => {
    expect(bar({ compaction_watermark: "m0-paged-out" })["summarizedCount"]).not.toBe(0);
  });

  // A rewind leaves the watermark naming a message that no longer exists, for the life of the chat.
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

  // `turn_open`, `turn_close` and `tool_result` are reached by the boundary scan but counted by neither total, so the
  // watermark is found on such a row and the count stays the rows a reader sees.
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

  // A mid-turn `compaction` is an entry appended where it happened; the text before it and the row itself count as
  // summarized, the text after does not.
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

describe("the compaction point handed to the ring", () => {
  beforeEach(() => {
    mocks.updateContextBar.mockClear();
    compactionPolicy.value = { enabled: true, pct: 80 };
  });

  function handed(): unknown {
    return (mocks.updateContextBar.mock.lastCall?.[0] as { compaction: unknown }).compaction;
  }

  it("takes KAS's reported threshold at the default value", () => {
    refreshContextUI(
      session({
        usage: { ...session().usage, summarization_threshold_pct: 75 },
      }),
    );
    expect(handed()).toEqual({ band: 75, t: 75 });
  });

  it("takes the slider's value anywhere else", () => {
    compactionPolicy.value = { enabled: true, pct: 60 };
    refreshContextUI(session());
    expect(handed()).toEqual({ band: 60, t: 60 });
  });

  it("withholds the band and keys the ramp on 100 when switched off", () => {
    compactionPolicy.value = { enabled: false, pct: 60 };
    refreshContextUI(session());
    expect(handed()).toEqual({ band: null, t: 100 });
  });

  it("repaints inside an effect when the setting changes, with no new usage", () => {
    const s = session();
    const stop = effect(() => {
      refreshContextUI(s);
    });
    expect(handed()).toEqual({ band: 80, t: 80 });
    compactionPolicy.value = { enabled: true, pct: 90 };
    expect(handed()).toEqual({ band: 90, t: 90 });
    stop();
  });
});
