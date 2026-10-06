// The client half of the drawn-predicate oracle (testdata/turn_drawn.json; the server half is
// TestTurnDrawnContract_ServerHalf, through RailRows). Each half builds rows from a different shape,
// which is the divergence the fixture catches: a drawn turn the rail refuses has no jump target.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { turnIsDrawn } from "./turns.js";
import type {
  Entry,
  EntryKind,
  EntryPrompt,
  OpenEntry,
  TurnOpenSourceName,
} from "./wire/types.gen.js";
import type { TurnState } from "./types.js";

const FIXTURE_PATH = "../internal/chat/testdata/turn_drawn.json";
const TURN = "t-1";

interface DrawnCase {
  name: string;
  owner: "both" | "client";
  drawn: boolean;
  open: { source: TurnOpenSourceName; prompt?: EntryPrompt };
  body: { kind: EntryKind; lane: string; agent_subtask_id?: string }[];
  open_entries?: string[];
}

function loadCases(): DrawnCase[] {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return (JSON.parse(raw) as { cases: DrawnCase[] }).cases;
}

function entryOf(kind: EntryKind, lane: string, seq: number, payload: unknown): Entry {
  return { id: `${kind}-${seq}`, turn: TURN, lane, kind, payload, seq, ts: 0 };
}

function openEntryOf(lane: string): OpenEntry {
  return { turn: TURN, id: `open-${lane}`, lane, kind: "text", text: "streaming", n: 1 };
}

/** The client's own shape, built from the case rather than from the server's rows. */
function stateOf(c: DrawnCase): TurnState {
  const entries: Entry[] = [
    entryOf("turn_open", "", 0, { source: c.open.source, n: 1, prompt: c.open.prompt }),
  ];
  for (const e of c.body) {
    const payload =
      e.kind === "tool_call" ? { id: "act-1", agent_subtask_id: e.agent_subtask_id } : {};
    entries.push(entryOf(e.kind, e.lane, entries.length, payload));
  }
  const openEntries = new Map<string, OpenEntry>();
  for (const lane of c.open_entries ?? []) {
    openEntries.set(lane, openEntryOf(lane));
  }
  return { entries, openEntries };
}

describe("the drawn predicate shared with the Go implementation", () => {
  const cases = loadCases();

  it("reads every case of the shared fixture", () => {
    expect(cases.length).toBeGreaterThanOrEqual(11);
    // Both verdicts and both owners are present, or a reader agreeing with a fixture
    // that asserts one answer would read as agreement.
    expect(new Set(cases.map((c) => c.drawn))).toEqual(new Set([true, false]));
    expect(cases.filter((c) => c.owner === "client")).toHaveLength(1);
  });

  for (const c of cases) {
    it(c.name, () => {
      expect(turnIsDrawn(stateOf(c))).toBe(c.drawn);
    });
  }
});
