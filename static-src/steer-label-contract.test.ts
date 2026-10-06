// The client half of the shared steer LABEL oracle: testdata/steer_label.json is the contract and
// TestSteerLabelContract is the other reader.

import { describe, it, expect } from "vitest";
import goldenRaw from "../internal/chat/testdata/steer_label.json?raw";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import type { SteerNoteData } from "./fundamentals/steer-note.js";
import type { SteerOrigin, SteerReason } from "./wire/types.gen.js";

/** One producible triple, plus the words the server says this side renders for it. */
interface LabelRow {
  origin: string;
  state: string;
  reason: string;
  label: string;
  compared: boolean;
  edge?: string;
  sites: string[];
}

interface LabelFixture {
  origins: string[];
  states: string[];
  triples: LabelRow[];
}

const fixture = JSON.parse(goldenRaw) as LabelFixture;

/** The separator `buildSteerNote` joins a label's clauses with (`parts.join(" · ")` in
 *  fundamentals/steer-note.ts, and `steerLabelFor`'s own join on the Go side). */
const SEPARATOR = " \u00b7 ";

/** The note the server's triple describes, built the way mountSteerNote builds one: `dropped` is
 *  `state === "dropped"` and `reason` is present only when the entry has one. */
function noteFor(row: LabelRow): HTMLElement {
  const data: SteerNoteData = {
    text: "the words of the steer",
    origin: row.origin as SteerOrigin,
    dropped: row.state === "dropped",
  };
  if (row.reason !== "") {
    // The fixture's row is a wire STRING (the JSON carries no enum), and the note's field is the
    // enum, so this is the one cast the shared reader owes — the same one `origin` already takes
    // two lines up.
    data.reason = row.reason as SteerReason;
  }
  return buildSteerNote(data);
}

function labelOf(note: HTMLElement): string {
  const el = note.querySelector(".steer-note-label");
  if (el === null) {
    throw new Error("the note carries no .steer-note-label");
  }
  return el.textContent ?? "";
}

describe("the steer label contract shared with the Go implementation", () => {
  const compared = fixture.triples.filter((r) => r.compared);

  it("reads a fixture with both origins, both states and at least one declared edge", () => {
    expect(fixture.origins).toEqual(["agent", "user"]);
    expect(fixture.states).toEqual(["dropped", "read"]);
    expect(compared.length).toBeGreaterThan(0);
    expect(fixture.triples.length).toBeGreaterThan(compared.length);
  });

  it("covers every (origin, state) pair the server's own enums admit", () => {
    const pairs = new Set(compared.map((r) => `${r.origin}|${r.state}`));
    for (const origin of fixture.origins) {
      for (const state of fixture.states) {
        expect(pairs.has(`${origin}|${state}`)).toBe(true);
      }
    }
  });

  // EVERY dropped row carries a reason now, so there is no bare dropped row left to diff a clause
  // against.
  it("exercises the drop-reason clause, so the wording table is pinned rather than unread", () => {
    const reasoned = compared.filter((r) => r.reason !== "");
    expect(reasoned.length).toBeGreaterThan(0);
    const clauses = new Map<string, Set<string>>();
    for (const row of reasoned) {
      const sep = row.label.indexOf(SEPARATOR);
      if (sep < 0) {
        throw new Error(
          `(${row.origin}, ${row.state}, ${row.reason}) carries no clause: ${row.label}`,
        );
      }
      const clause = row.label.slice(sep + SEPARATOR.length);
      const pair = `${row.origin}|${row.state}`;
      const seen = clauses.get(pair) ?? new Set<string>();
      if (seen.has(clause)) {
        throw new Error(`(${row.origin}, ${row.state}) words two reasons alike: ${clause}`);
      }
      seen.add(clause);
      clauses.set(pair, seen);
    }
  });

  for (const row of fixture.triples) {
    if (!row.compared) {
      it(`declares (${row.origin}, ${row.state || "unset"}) out of the comparison with a reason`, () => {
        expect(row.edge).toBeTruthy();
        expect(row.label).toBe("");
      });
      continue;
    }
    it(`(${row.origin}, ${row.state}, reason ${row.reason || "none"}) words as ${row.label}`, () => {
      const note = noteFor(row);
      expect(labelOf(note)).toBe(row.label);
      expect(note.getAttribute("data-origin")).toBe(row.origin);
      expect(note.getAttribute("data-state")).toBe(row.state);
      expect(note.getAttribute("aria-label")).toBe(`${row.label}: the words of the steer`);
    });
  }
});
