// Client half of the delegate DOT oracle: testdata/delegate_dot.json is the contract, TestDelegateDotContract the Go
// reader. Three surfaces render one status differently on purpose (wire `aborted`, card "cancelled").

import { describe, it, expect, beforeAll } from "vitest";
import goldenRaw from "../internal/chat/testdata/delegate_dot.json?raw";
import type { SubagentProjection } from "./subagent-slice.js";
import type { ExecState } from "./exec-view/status.js";
import type { ExecRun } from "./exec-view/model.js";
import type { SubagentCard } from "./fundamentals/subagent-block.js";
import type { ToolCall, ToolStatus } from "./wire/types.gen.js";

interface DotRow {
  status: string;
  dot: string;
  card_word: string;
  page_state: string;
  page_word: string;
  terminal: boolean;
  sites: string[];
}

/** The stale-spinner row: an in-flight invocation in a chat holding no live turn of its
 *  own, with the status the client folds it onto and what each surface then says. */
interface StaleRow {
  status: string;
  turn_live: boolean;
  folds_to: string;
  dot: string;
  card_word: string;
  page_state: string;
  page_word: string;
}

interface DotFixture {
  statuses: string[];
  rows: DotRow[];
  stale: StaleRow[];
  readers: { column: string; reader: string }[];
}

const fixture = JSON.parse(goldenRaw) as DotFixture;

/** One delegate at different statuses is enough. */
const SUBTASK = "sa-1";

let subagentStatusFor: (status: ToolStatus | undefined, turnLive?: boolean) => string;
let delegateStatusFor: (status: ToolStatus, turnLive: boolean) => ToolStatus;
let buildSubagentCard: (name: string, status: ToolStatus) => SubagentCard;
let subagentToExec: (
  subtaskID: string,
  projection: SubagentProjection,
  turnLive?: boolean,
) => ExecRun;
let stateWordOf: Readonly<Record<ExecState, string>>;
let isToolActive: (s: ToolStatus) => boolean;
let isToolDone: (s: ToolStatus) => boolean;

beforeAll(async () => {
  for (const id of ["messages", "messages-wrap", "scroll-bottom"]) {
    if (document.getElementById(id) === null) {
      const el =
        id === "scroll-bottom" ? document.createElement("button") : document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
  ({ subagentStatusFor, delegateStatusFor } = await import("./store.js"));
  ({ buildSubagentCard } = await import("./fundamentals/subagent-block.js"));
  ({ subagentToExec } = await import("./subagent-exec-source.js"));
  ({ STATE_WORD: stateWordOf } = await import("./exec-view/status.js"));
  ({ isToolActive, isToolDone } = await import("./tool-schema.js"));
});

function invocationAt(status: ToolStatus): ToolCall {
  return {
    id: "tc-1",
    title: "Sub-agent: context-gatherer",
    kind: "other",
    status,
    ts: 0,
    agent_subtask_id: SUBTASK,
  };
}

/** Built as a value: under test is the status fold, not the lane walk. */
function projectionAt(status: ToolStatus): SubagentProjection {
  return {
    group: { pipeline: "", driver: undefined, members: [] },
    slices: new Map([
      [
        SUBTASK,
        {
          invocation: invocationAt(status),
          turn: undefined,
          entries: [],
          open: false,
          live: isToolActive(status),
        },
      ],
    ]),
  };
}

/** Read off the card's state element, which `refreshName` writes when the head is not a link. */
function cardWordAt(status: ToolStatus): string {
  const card = buildSubagentCard("introspect", status);
  const state = card.root.querySelector<HTMLElement>(".subagent-header > .sr-only");
  if (state === null) {
    throw new Error("the card carries no .subagent-header > .sr-only state element");
  }
  return state.textContent ?? "";
}

describe("the delegate dot contract", () => {
  it("names a row for every status the fixture declares", () => {
    // A truncated fixture would pass every row vacuously.
    expect(fixture.rows.map((r) => r.status).sort()).toEqual([...fixture.statuses].sort());
    expect(fixture.rows.length).toBeGreaterThan(0);
  });

  it("carries the readers it is asserted through, so a failure names a function", () => {
    expect(fixture.readers.map((r) => r.column)).toContain("card_word");
    expect(fixture.readers.map((r) => r.column)).toContain("page_state");
  });

  for (const row of fixture.rows) {
    const status = row.status as ToolStatus;

    it(`${row.status}: the tab dot is ${row.dot}`, () => {
      expect(subagentStatusFor(status)).toBe(row.dot);
    });

    it(`${row.status}: the card announces ${row.card_word}`, () => {
      expect(cardWordAt(status)).toBe(row.card_word);
    });

    it(`${row.status}: the page is ${row.page_state}, announced as ${row.page_word}`, () => {
      const run = subagentToExec(SUBTASK, projectionAt(status));
      expect(run.nodes).toHaveLength(1);
      const leaf = run.nodes[0];
      if (leaf === undefined) {
        throw new Error("subagentToExec produced no leaf for the delegate");
      }
      expect(leaf.state).toBe(row.page_state);
      expect(stateWordOf[row.page_state as ExecState]).toBe(row.page_word);
    });

    it(`${row.status}: the fold is the identity while the chat's turn is live`, () => {
      // The second input may only narrow: a live chat's answer must be the status itself.
      expect(delegateStatusFor(status, true)).toBe(row.status);
    });

    it(`${row.status}: settled is ${row.terminal} on both sides`, () => {
      // Go answers with ToolStatus.Terminal(); a status only one language reads as over spins a card forever.
      expect(isToolDone(status)).toBe(row.terminal);
      expect(isToolActive(status)).toBe(!row.terminal);
    });
  }

  it("carries a stale row, so the fold is not asserted against an absent block", () => {
    expect(fixture.stale.length).toBeGreaterThan(0);
    expect(fixture.readers.map((r) => r.column)).toContain("stale.folds_to");
  });

  for (const row of fixture.stale) {
    const status = row.status as ToolStatus;
    const folded = row.folds_to as ToolStatus;

    it(`${row.status} with turnLive=${String(row.turn_live)}: folds to ${row.folds_to}`, () => {
      expect(delegateStatusFor(status, row.turn_live)).toBe(folded);
    });

    it(`${row.status} with turnLive=${String(row.turn_live)}: the three surfaces say what ${row.folds_to} says`, () => {
      expect(subagentStatusFor(status, row.turn_live)).toBe(row.dot);
      expect(cardWordAt(folded)).toBe(row.card_word);
      // Raw status and liveness, as `subagent-view.ts` passes them: the page makes the fold.
      const run = subagentToExec(SUBTASK, projectionAt(status), row.turn_live);
      const leaf = run.nodes[0];
      if (leaf === undefined) {
        throw new Error("subagentToExec produced no leaf for the folded status");
      }
      expect(leaf.state).toBe(row.page_state);
      expect(stateWordOf[row.page_state as ExecState]).toBe(row.page_word);
    });
  }
});
