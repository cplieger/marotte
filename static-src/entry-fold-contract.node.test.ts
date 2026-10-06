// Client half of the shared fold oracle: testdata/entry_fold.json is the contract, TestEntryFoldContract the Go reader.

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { entryRenders, firstPlanSeq } from "./block-window.js";
import { payloadOf, projectTurns } from "./turns.js";
import type { Entry, EntryKind } from "./wire/types.gen.js";
import type { TurnState } from "./types.js";

const FIXTURE_PATH = "../internal/chat/testdata/entry_fold.json";

/** One entry's row of the fixture: the export's answer, plus whether this half must agree. */
interface FoldRow {
  id: string;
  seq: number;
  kind: EntryKind;
  renders: boolean;
  compared: boolean;
  edge?: string;
}

interface FoldTurn {
  turn: string;
  n: number;
  entries: FoldRow[];
}

interface FoldFixture {
  entries: Entry[];
  turns: FoldTurn[];
}

function loadFixture(): FoldFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as FoldFixture;
}

/** The store's partition, ordered by `turn_open.n`, never by first appearance (the export's rule being compared). */
function storeTurns(entries: readonly Entry[]): {
  turns: Map<string, TurnState>;
  turn_order: string[];
} {
  const turns = new Map<string, TurnState>();
  for (const e of entries) {
    const held = turns.get(e.turn);
    if (held === undefined) {
      turns.set(e.turn, { entries: [e], openEntries: new Map() });
      continue;
    }
    held.entries.push(e);
  }
  for (const state of turns.values()) {
    state.entries.sort((a, b) => a.seq - b.seq);
  }
  const ordinalOf = (id: string): number => {
    const first = turns.get(id)?.entries[0];
    return first === undefined ? 0 : (payloadOf(first, "turn_open")?.n ?? 0);
  };
  const turn_order = [...turns.keys()].sort((a, b) => ordinalOf(a) - ordinalOf(b));
  return { turns, turn_order };
}

describe("the fold contract shared with the Go implementation", () => {
  const fx = loadFixture();
  const source = storeTurns(fx.entries);

  it("reads a fixture that carries the shape it exists for", () => {
    expect(fx.turns.length).toBeGreaterThanOrEqual(2);
    // Interleaved, or file order alone would satisfy the grouping assertion.
    const owners = fx.entries.map((e) => e.turn);
    const spread = fx.turns.map(
      (t) => owners.lastIndexOf(t.turn) - owners.indexOf(t.turn) + 1 - t.entries.length,
    );
    expect(Math.max(...spread)).toBeGreaterThan(0);
    // Both verdicts occur, or a one-answer fixture would read as agreement.
    const compared = fx.turns.flatMap((t) => t.entries).filter((r) => r.compared);
    expect(new Set(compared.map((r) => r.renders))).toEqual(new Set([true, false]));
  });

  it("partitions the log into the same turns, in the same order", () => {
    expect(source.turn_order).toEqual(fx.turns.map((t) => t.turn));
    for (const t of fx.turns) {
      const held = source.turns.get(t.turn);
      expect(held?.entries.map((e) => e.id)).toEqual(t.entries.map((r) => r.id));
      expect(held?.entries.map((e) => e.seq)).toEqual(t.entries.map((r) => r.seq));
    }
  });

  const projected = projectTurns(source);

  for (const t of fx.turns) {
    describe(t.turn, () => {
      const turn = projected.find((p) => p.id === t.turn);
      const rows = t.entries.filter((r) => r.compared);

      it("is drawn and projected", () => {
        expect(turn).toBeDefined();
        expect(turn?.n).toBe(t.n);
        expect(rows.length).toBeGreaterThan(0);
      });

      for (const row of rows) {
        it(`${row.kind} at seq ${row.seq} renders at its own position: ${row.renders}`, () => {
          if (turn === undefined) {
            throw new Error("the turn was not projected; the case above names it");
          }
          const entry = turn.body.find((e) => e.id === row.id);
          if (entry === undefined) {
            throw new Error(`entry ${row.id} is not in the turn's body`);
          }
          expect(entryRenders(entry, "", firstPlanSeq(turn, ""))).toBe(row.renders);
        });
      }
    });
  }
});
