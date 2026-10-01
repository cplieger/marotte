// The client half of the shared delegate DOT oracle: testdata/delegate_dot.json is the
// contract and TestDelegateDotContract is the other reader. That one SCANS every producer
// of a ToolStatus under internal/ — classifying each reference by the position it sits in,
// so a comparison is read as a reader and not as a stamp — and states, per producible
// status, what each of this side's three surfaces has to say about it. This one asks the
// real functions.
//
// THREE SURFACES, ONE VALUE, and the divergence is deliberate (.kiro/steering/marotte.md
// "A delegate has THREE terminal states"): the wire spells it `aborted`, a delegate's CARD
// announces "cancelled" because the enclosing turn card's footer says that about the same
// stop, and the subagent PAGE says "stopped". Nothing in the app asserted any pair of the
// three against another before this fixture, so the divergence could drift into a
// disagreement nobody would see.
//
// Browser placement, like the steer-label contract and unlike the fold's node test:
// `stateWord` is unexported, so its only reachable channel is the card's own accessible
// state element, which needs a DOM. The fixture arrives through the `?raw` import Vite
// already allows for ../internal (vitest.config.ts's server.fs.allow).
//
// EVERY PRODUCTION IMPORT IS DYNAMIC, and that is what keeps this file out of the vi.mock
// ratchet's population: `fundamentals/subagent-block.ts` reaches `scroll.ts`, a singleton
// that builds itself against a real `#messages` at import, and the canonical answer
// elsewhere is a whole-module `vi.mock` of it — which is exactly the shape
// __test-helpers__/vi-mock-original.node.test.ts exists to stop new files adopting. Seeding
// the three elements it reads and importing afterwards needs no mock at all, and it drives
// the real scroll controller rather than a fake.
//
// The ROW surface's word is NOT compared here: `tool-card.ts`'s outcomeWord is unexported
// and reaches no seam a test can drive, so the fixture carries the card and the page, and
// the wire's own spelling stands for the row.

import { describe, it, expect, beforeAll } from "vitest";
import goldenRaw from "../internal/chat/testdata/delegate_dot.json?raw";
import type { SubagentProjection } from "./subagent-slice.js";
import type { ExecState } from "./exec-view/status.js";
import type { ExecRun } from "./exec-view/model.js";
import type { SubagentCard } from "./fundamentals/subagent-block.js";
import type { ToolCall, ToolStatus } from "./wire/types.gen.js";

/** One producible status, with what the server says each surface renders for it. */
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

/** The delegate's own subtask id. One is enough: every row is the same delegate at a
 *  different status, which is what the three surfaces disagree about. */
const SUBTASK = "sa-1";

/** The production functions, resolved once the transcript host exists. */
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

/** The invocation tool call that dispatched the delegate, stated at the row's status.
 *  A typed literal rather than a cast, which is this phase's own rule. */
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

/** The projection the page is built from: one delegate, no pipeline, so `subagentToExec`
 *  takes its single-delegate shape and `nodes[0]` is the delegate itself. Built as a value
 *  rather than through `sliceSubagentGroup`, because what is under test is the STATUS fold
 *  and not the walk that finds the lane. */
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

/** The word the card announces, off the card's own state element — the channel
 *  `refreshName` writes when the head is not a link. */
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
    // A fixture the Go half truncated would otherwise pass every row below vacuously.
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
      // The second input may only narrow, so a live chat's answer has to be the status
      // itself for every row — otherwise the fold changes what a working delegate says.
      expect(delegateStatusFor(status, true)).toBe(row.status);
    });

    it(`${row.status}: settled is ${row.terminal} on both sides`, () => {
      // The one DERIVED column: Go answers with ToolStatus.Terminal(), the predicate the
      // close paths settle a turn's calls by, and this side answers with its own pair. A
      // status only one language reads as over is how a card spins forever.
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
      // The RAW status and the turn's liveness, as `subagent-view.ts` hands them over:
      // the page has to make the fold itself, not be given its answer.
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
