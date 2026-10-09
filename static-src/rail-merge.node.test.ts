// THE MERGE, and the two things that make it more than a concatenation: it keys on `n` rather than
// on id, because the appender assigns `n` at open and stores it, so a paginated window and the
// session-wide index name a turn by the same number; and it validates a payload that arrives
// through an unchecked cast, because a non-finite `n` reaches `calc(NaN * …)` — an invalid
// declaration the browser drops, so the failure is a marker silently pinned to the top rather than
// a wrong one.

import { readFileSync } from "node:fs";
import { describe, it, expect, beforeEach, vi } from "vitest";

import { mergeTurnSets, validateTurnIndex, type TurnSummary } from "./rail-merge.js";
import type { Turn, TurnOutcome } from "./turns.js";
import type { EntryPrompt } from "./wire/types.gen.js";

function prompt(id: string, text: string): EntryPrompt {
  return { id, text };
}

function residentTurn(n: number, over: Partial<Turn> = {}): Turn {
  return {
    id: `t-${String(n)}`,
    n,
    trigger: prompt(`m-${String(n)}`, `prompt ${String(n)}`),
    body: [],
    openEntries: new Map(),
    ts: n * 1000,
    outcome: "completed",
    rewindTo: undefined,
    ...over,
  };
}

interface FirstLineCase {
  name: string;
  prompt: string;
  first_line: string;
}

function firstLineCases(): FirstLineCase[] {
  const raw = readFileSync(
    new URL("../internal/chat/testdata/first_line.json", import.meta.url),
    "utf8",
  );
  return (JSON.parse(raw) as { cases: FirstLineCase[] }).cases;
}

function indexRow(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return {
    id: `t-${String(n)}`,
    n,
    ts: n * 1000,
    outcome: "completed",
    first_line: `prompt ${String(n)}`,
    agent_initiated: false,
    ...over,
  };
}

describe("resident wins for anything it can answer", () => {
  it("takes the resident outcome over the index's for a turn in the window", () => {
    const index = [indexRow(1), indexRow(2, { outcome: "unknown" })];
    const resident = [residentTurn(2, { outcome: "running" })];
    const turns = mergeTurnSets(resident, index);
    expect(turns.map((t) => [t.n, t.outcome])).toEqual([
      [1, "completed"],
      [2, "running"],
    ]);
  });

  it("carries a turn the index has never seen, so the newest turn needs no fetch", () => {
    const index = [indexRow(1), indexRow(2)];
    const resident = [residentTurn(2), residentTurn(3, { outcome: "running" })];
    const turns = mergeTurnSets(resident, index);
    expect(turns.map((t) => t.n)).toEqual([1, 2, 3]);
  });

  // The server's first_line for the same turn follows the same file, so a turn's preview does not
  // change as it pages in or out of the store's window. Node: a disk read.
  it.each(firstLineCases())("derives the preview as the index does: $name", (c) => {
    const turns = mergeTurnSets([residentTurn(4, { trigger: prompt("m-4", c.prompt) })], []);
    expect(turns[0]?.first_line ?? "").toBe(c.first_line);
  });

  it("reports a turn with no trigger as agent-initiated and unlabelled", () => {
    const turns = mergeTurnSets([residentTurn(4, { trigger: undefined })], []);
    expect(turns[0]?.agent_initiated).toBe(true);
    // ABSENT rather than "": the field is `omitempty` on the wire, so an agent-initiated turn
    // carries no label there either and every reader defaults.
    expect(turns[0]?.first_line).toBeUndefined();
  });

  it("takes the resident id, ts and label over the index's row for the same n", () => {
    const index = [indexRow(8, { id: "t-8-index", ts: 500, first_line: "stale" })];
    const resident = [
      residentTurn(8, { id: "t-8", ts: 9000, trigger: prompt("m-8", "live prompt") }),
    ];
    const turns = mergeTurnSets(resident, index);
    expect(turns[0]).toEqual({
      id: "t-8",
      n: 8,
      ts: 9000,
      outcome: "completed",
      first_line: "live prompt",
      agent_initiated: false,
    });
  });
});

describe("a resident turn's duration comes from its own turn_close", () => {
  it("carries the close's elapsed_ms and wins over the index row's", () => {
    const closed = residentTurn(3, {
      body: [
        {
          id: "t-3-close",
          turn: "t-3",
          lane: "",
          kind: "turn_close",
          seq: 1,
          ts: 3500,
          payload: { outcome: "completed", elapsed_ms: 4200 },
        },
      ],
    });
    const turns = mergeTurnSets([closed], [indexRow(3, { elapsed_ms: 99 })]);
    expect(turns[0]?.elapsed_ms).toBe(4200);
  });

  it("carries no duration for a resident turn that has not closed", () => {
    const turns = mergeTurnSets(
      [residentTurn(3, { outcome: "running" })],
      [indexRow(3, { elapsed_ms: 99 })],
    );
    expect(turns[0]?.elapsed_ms).toBeUndefined();
  });
});

describe("the index extends the set backwards", () => {
  it("supplies the turns outside the window", () => {
    const index = Array.from({ length: 10 }, (_, i) => indexRow(i + 1));
    const resident = [residentTurn(8), residentTurn(9), residentTurn(10)];
    const turns = mergeTurnSets(resident, index);
    expect(turns.map((t) => t.n)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
  });

  it("orders the merged set by n whatever order either side arrived in", () => {
    const index = [indexRow(9), indexRow(1), indexRow(4)];
    const resident = [residentTurn(7), residentTurn(2)];
    const turns = mergeTurnSets(resident, index);
    expect(turns.map((t) => t.n)).toEqual([1, 2, 4, 7, 9]);
  });
});

describe("the merge keys on n, never on id", () => {
  it("counts one turn once when the two sides name it differently", () => {
    // The index holds the turn's own id; a resident row can carry a different one (a turn re-keyed
    // by a merge swap), and the position is what the rail renders.
    const index = [indexRow(7), indexRow(8, { id: "t-8-index" })];
    const resident = [residentTurn(8, { id: "t-8-resident" })];
    const turns = mergeTurnSets(resident, index);
    expect(turns.map((t) => t.n)).toEqual([7, 8]);
    expect(turns[1]?.id).toBe("t-8-resident");
  });
});

describe("validating the index", () => {
  // A malformed index warns once per fetch, so every case here silences it and the one case about
  // the warn reads the spy.
  let warn: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
  });

  it("answers an empty set for a payload that is not an array", () => {
    expect(validateTurnIndex(undefined)).toEqual({ turns: [], dropped: 0 });
    expect(validateTurnIndex({ turns: [] })).toEqual({ turns: [], dropped: 0 });
  });

  it("drops a row whose id cannot be read", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: 7, n: 1, ts: 1, outcome: "completed" },
      { id: "", n: 2, ts: 2, outcome: "completed" },
      { n: 3, ts: 3, outcome: "completed" },
      { id: "t-4", n: 4, ts: 4, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.n)).toEqual([4]);
    expect(dropped).toBe(3);
  });

  it("drops a row whose n cannot be read", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: Number.NaN, ts: 1, outcome: "completed" },
      { id: "b", n: Number.POSITIVE_INFINITY, ts: 1, outcome: "completed" },
      { id: "c", n: 0, ts: 1, outcome: "completed" },
      { id: "d", n: -3, ts: 1, outcome: "completed" },
      { id: "e", n: 1.5, ts: 1, outcome: "completed" },
      { id: "f", n: "2", ts: 1, outcome: "completed" },
      { id: "g", n: 2, ts: 1, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.id)).toEqual(["g"]);
    expect(dropped).toBe(6);
  });

  it("drops a row that is not an object at all", () => {
    const { turns, dropped } = validateTurnIndex([null, "row", 4, { id: "t-1", n: 1, ts: 1 }]);
    expect(turns).toHaveLength(1);
    expect(dropped).toBe(3);
  });

  it("keeps a row whose ts cannot be read and sits it on its predecessor", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: 1, ts: 1000, outcome: "completed" },
      { id: "b", n: 2, ts: "soon", outcome: "completed" },
      { id: "c", n: 3, ts: 3000, outcome: "completed" },
    ]);
    expect(turns.map((t) => t.id)).toEqual(["a", "b", "c"]);
    expect(dropped).toBe(0);
    // A plausible start time rather than the epoch: `ts` is the wire's field and answering for a
    // malformed one is the validator's job.
    expect(turns[1]?.ts).toBe(1000);
  });

  it("fills a LEADING unreadable ts from the row after it", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: -1, outcome: "completed" },
      { id: "b", n: 2, ts: 2000, outcome: "completed" },
    ]);
    expect(turns[0]?.ts).toBe(2000);
  });

  it("coerces an outcome the wire does not carry to unknown", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "exploded" },
      { id: "b", n: 2, ts: 2, outcome: 7 },
      { id: "c", n: 3, ts: 3 },
      { id: "d", n: 4, ts: 4, outcome: "refused" },
      // An inherited member name is not a member. `Object.hasOwn` is what makes this a miss rather
      // than the prototype's answer.
      { id: "e", n: 5, ts: 5, outcome: "constructor" },
      // The kind a turn takes when the reader asked for nothing that failed.
      { id: "f", n: 6, ts: 6, outcome: "empty" },
    ]);
    expect(turns.map((t) => t.outcome)).toEqual([
      "unknown",
      "unknown",
      "unknown",
      "refused",
      "unknown",
      "empty",
    ]);
    expect(dropped).toBe(0);
  });

  it("coerces a first_line that is not a string to no label", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed", first_line: 12 },
      { id: "b", n: 2, ts: 2, outcome: "completed", first_line: "" },
      { id: "c", n: 3, ts: 3, outcome: "completed", first_line: "real" },
    ]);
    expect(turns[0]?.first_line).toBeUndefined();
    expect(turns[1]?.first_line).toBeUndefined();
    expect(turns[2]?.first_line).toBe("real");
  });

  it("coerces an agent_initiated that is not a boolean to false", () => {
    const { turns } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed", agent_initiated: "yes" },
      { id: "b", n: 2, ts: 2, outcome: "completed", agent_initiated: 1 },
      { id: "c", n: 3, ts: 3, outcome: "completed", agent_initiated: true },
    ]);
    expect(turns.map((t) => t.agent_initiated)).toEqual([false, false, true]);
  });

  it("keeps a finite positive elapsed_ms and omits every other value without dropping the row", () => {
    const { turns, dropped } = validateTurnIndex([
      { id: "a", n: 1, ts: 1, outcome: "completed", elapsed_ms: 1234 },
      { id: "b", n: 2, ts: 2, outcome: "completed", elapsed_ms: Number.NaN },
      { id: "c", n: 3, ts: 3, outcome: "completed", elapsed_ms: -5 },
      { id: "d", n: 4, ts: 4, outcome: "completed", elapsed_ms: "1234" },
      { id: "e", n: 5, ts: 5, outcome: "completed", elapsed_ms: 0 },
      { id: "f", n: 6, ts: 6, outcome: "completed", elapsed_ms: Number.POSITIVE_INFINITY },
    ]);
    expect(dropped).toBe(0);
    expect(turns.map((t) => t.elapsed_ms)).toEqual([
      1234,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
    ]);
    expect("elapsed_ms" in (turns[1] ?? {})).toBe(false);
  });

  it("logs once per fetch and stays quiet when nothing was dropped", () => {
    validateTurnIndex([
      { id: "", n: 1, ts: 1 },
      { id: "b", n: 0, ts: 1 },
      { id: "c", n: 3, ts: 1 },
    ]);
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockClear();
    validateTurnIndex([{ id: "c", n: 3, ts: 1 }]);
    expect(warn).not.toHaveBeenCalled();
  });
});

describe("an outcome the merge cannot grade is still a real value", () => {
  it("keeps every resident outcome the wire declares", () => {
    const outcomes: TurnOutcome[] = [
      "running",
      "completed",
      "cancelled",
      "interrupted",
      "failed",
      "refused",
      "unknown",
      "empty",
    ];
    const resident = outcomes.map((outcome, i) => residentTurn(i + 1, { outcome }));
    const turns = mergeTurnSets(resident, []);
    expect(turns.map((t) => t.outcome)).toEqual(outcomes);
  });
});
