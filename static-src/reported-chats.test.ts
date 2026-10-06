// The two REPORTED chats, replayed through the real projection.

import { describe, it, expect, beforeEach } from "vitest";

import fixtureRaw from "./testdata/reported-chats.json?raw";
import { projectTurns, turnFailureText, type Turn, type TurnSource } from "./turns.js";
import { severityOf, defaultFailureReason, TURN_OUTCOME_VALUES } from "./turn-severity.js";
import { outcomeLatch } from "./store.js";
import { isTurnOpen, resetFoldState } from "./fold-state.js";
import type { TurnState } from "./types.js";
import type { Entry, EntryKind, EntryTurnClose, TurnOutcome } from "./wire/types.gen.js";

interface FixtureMessage {
  readonly id: string;
  readonly role: string;
  readonly ts: number;
  readonly content?: string;
  readonly event_kind?: string;
  readonly turn_outcome?: string;
  readonly turn_stop_reason_raw?: string;
  readonly turn_failure_reason?: string;
  readonly block_count?: number;
  readonly tool_call_count?: number;
}

interface FixtureChat {
  readonly chat_id: string;
  readonly messages: readonly FixtureMessage[];
}

const fixture = JSON.parse(fixtureRaw) as Record<string, FixtureChat>;

/** One recorded turn, before its entries are built. `outcome` is the turn's own
 *  `turn_close.outcome` and NOT a derivation: under the entry model the appender writes it, so a
 *  turn with no carrier at all is `running` rather than guessed at. */
interface FoldedTurn {
  readonly prompt: FixtureMessage;
  carrier: FixtureMessage | undefined;
  outcome: TurnOutcome | undefined;
  stopReason: string;
  reason: string;
}

/** Narrow a recorded outcome to the wire's own vocabulary, throwing on anything else — a RUNTIME
 *  check where the deleted `rehydrate` cast, so a value the wire cannot send fails the fixture
 *  here rather than reaching a rule that grades it. */
function outcomeOf(raw: string, where: string): TurnOutcome {
  const found = TURN_OUTCOME_VALUES.find((v) => v === raw);
  if (found === undefined) {
    throw new Error(`${where}: ${raw} is not a TurnOutcome the wire can send`);
  }
  return found;
}

function entryAt(
  turnID: string,
  seq: number,
  kind: EntryKind,
  payload: unknown,
  id: string,
): Entry {
  return { id, turn: turnID, kind, seq, ts: seq + 1, payload };
}

/** Group the recorded rows into turns. FOUR rules, and each is the entry model's own: */
function groupTurns(chat: FixtureChat): FoldedTurn[] {
  const out: FoldedTurn[] = [];
  for (const m of chat.messages) {
    if (m.role === "user") {
      out.push({
        prompt: m,
        carrier: undefined,
        outcome: undefined,
        stopReason: "",
        reason: m.turn_failure_reason ?? "",
      });
      continue;
    }
    const cur = out[out.length - 1];
    if (cur === undefined) {
      throw new Error(`${chat.chat_id}: ${m.role} row ${m.id} precedes every prompt`);
    }
    if (m.role === "assistant") {
      cur.carrier = m;
    }
    if (m.turn_outcome !== undefined) {
      cur.outcome = outcomeOf(m.turn_outcome, `${chat.chat_id}/${m.id}`);
      cur.stopReason = m.turn_stop_reason_raw ?? "";
    }
    const prose = (m.turn_failure_reason ?? m.content ?? "").trim();
    if (prose !== "") {
      cur.reason = prose;
    }
  }
  return out;
}

/** Build one turn's entries. `block_count` and `tool_call_count` become that many body entries,
 *  because the fold and face rules read the SHAPE of a turn's body — the deleted `rehydrate`'s
 *  own reason, and it survives the model change. */
function entriesFor(turnID: string, t: FoldedTurn): { entries: Entry[]; closeAt?: number } {
  const entries: Entry[] = [
    entryAt(
      turnID,
      0,
      "turn_open",
      {
        source: "prompt",
        n: Number(turnID.slice(turnID.lastIndexOf("-") + 1)),
        prompt: { id: t.prompt.id, text: t.prompt.content ?? "" },
      },
      `${turnID}-open`,
    ),
  ];
  const carrier = t.carrier;
  if (carrier !== undefined) {
    const texts = carrier.block_count ?? 0;
    for (let i = 0; i < texts; i++) {
      entries.push(
        entryAt(
          turnID,
          entries.length,
          "text",
          { text: i === 0 ? (carrier.content ?? "") : "" },
          `${carrier.id}-t${String(i)}`,
        ),
      );
    }
    const calls = carrier.tool_call_count ?? 0;
    for (let i = 0; i < calls; i++) {
      const id = `tc-${carrier.id}-${String(i)}`;
      entries.push(
        entryAt(
          turnID,
          entries.length,
          "tool_call",
          { id, title: "Work", kind: "other", status: "completed", ts: carrier.ts },
          id,
        ),
      );
    }
  }
  if (t.outcome === undefined) {
    return { entries };
  }
  const payload: EntryTurnClose = { outcome: t.outcome };
  if (t.stopReason !== "") {
    payload.stop_reason_raw = t.stopReason;
  }
  if (t.reason !== "") {
    payload.failure_reason = t.reason;
  }
  const closeAt = entries.length;
  entries.push(entryAt(turnID, closeAt, "turn_close", payload, `${turnID}-close`));
  return { entries, closeAt };
}

/** Fold one fixture chat into the `TurnSource` the projection reads. `closeAt` is SET for every
 *  settled turn and left absent only for a turn nothing closed, because `hasOpenTurn` is a
 *  `closeAt === undefined` scan: a fold that omitted it would make every turn of both records
 *  paint `working`. */
function foldChat(chat: FixtureChat): TurnSource {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const [i, t] of groupTurns(chat).entries()) {
    const turnID = `${chat.chat_id}-turn-${String(i + 1)}`;
    const built = entriesFor(turnID, t);
    const state: TurnState = { entries: built.entries, openEntries: new Map() };
    if (built.closeAt !== undefined) {
      state.closeAt = built.closeAt;
    }
    turns.set(turnID, state);
    order.push(turnID);
  }
  return { turns, turn_order: order };
}

function chatNamed(name: string): FixtureChat {
  const chat = fixture[name];
  if (chat === undefined) {
    throw new Error(`the fixture has no chat named ${name}`);
  }
  return chat;
}

function turnsOf(name: string): Turn[] {
  return projectTurns(foldChat(chatNamed(name)));
}

beforeEach(() => {
  localStorage.clear();
  resetFoldState();
});

describe("the reported chats project into the turns the report described", () => {
  it("reads chat 1 as failed, failed, completed", () => {
    expect(turnsOf("reported-failed-no-reason").map((t) => t.outcome)).toEqual([
      "failed",
      "failed",
      "completed",
    ]);
  });

  it("reads chat 2 as interrupted, cancelled", () => {
    // Two turns, not three: the `interrupted` event row carries prose for the turn it follows and
    // opens nothing, which under the entry model is the file's own shape rather than a boundary
    // rule — only a `turn_open` opens a turn.
    expect(turnsOf("reported-interrupted-hollow-dot").map((t) => t.outcome)).toEqual([
      "interrupted",
      "cancelled",
    ]);
  });
});

describe("symptom 1: every failed turn now SAYS something", () => {
  it("gives chat 1 turn 1 a reason, where the record holds none at all", () => {
    // THE REPORTED TURN. On disk: a settled `failed`, 26 blocks, 17 tool calls, 3 changed files,
    // and no prose anywhere — no text block, no event row, no `turn_failure_reason` (that field did
    // not exist when this was written).
    const turn = turnsOf("reported-failed-no-reason")[0];
    expect(turn?.outcome).toBe("failed");
    expect(turn === undefined ? "" : turnFailureText(turn)).toBe(
      "The agent reported an error and the turn stopped.",
    );
  });

  it("gives chat 1 turn 2 a reason, where the only trace is a skipped marker", () => {
    // The second reported turn: an empty turn whose sole persisted row is a `turn_outcome` event
    // with empty content, which folds to a `turn_open` and a `turn_close` with NOTHING between them
    // — so the body renders literally nothing and the notice is the turn's whole account of itself.
    const turn = turnsOf("reported-failed-no-reason")[1];
    expect(turn?.outcome).toBe("failed");
    expect(turn?.body.filter((e) => e.kind !== "turn_close")).toEqual([]);
    expect(turn === undefined ? "" : turnFailureText(turn)).not.toBe("");
  });

  it("keeps chat 2's own upstream sentence rather than replacing it", () => {
    // The one reason that WAS durable, and the fallback must not outrank it. Its carrier moved with
    // the model — the divider's prose is `turn_close.failure_reason` now — and what it says is
    // KAS's own text, more specific than anything marotte can say.
    const turn = turnsOf("reported-interrupted-hollow-dot")[0];
    expect(turn === undefined ? "" : turnFailureText(turn)).toBe(
      "A network error occurred. Please check your connection and try again.",
    );
  });

  it("says nothing for the turn that ended cleanly", () => {
    const turn = turnsOf("reported-failed-no-reason")[2];
    expect(turn?.outcome).toBe("completed");
    expect(turn === undefined ? "x" : turnFailureText(turn)).toBe("");
  });
});

describe("symptom 2: the tab dot", () => {
  it("latches chat 2's interrupted turn as a FAILURE, not as nothing", () => {
    // The newest outcome in this record is `interrupted`; mapped to nothing, `tabStatusFor` would
    // fall through to `idle`, which 12-tabs.css paints as a transparent disc with a hairline ring.
    const turns = turnsOf("reported-interrupted-hollow-dot");
    expect(outcomeLatch(turns[0]?.outcome)).toBe("failed");
  });

  it("latches chat 1's failed turns as failures and its clean turn as done", () => {
    const [a, b, c] = turnsOf("reported-failed-no-reason");
    expect([outcomeLatch(a?.outcome), outcomeLatch(b?.outcome), outcomeLatch(c?.outcome)]).toEqual([
      "failed",
      "failed",
      "done",
    ]);
  });

  it("latches chat 2's cancelled turn as DONE, not as a failure and not as nothing", () => {
    // The control, and it has to say two things now. A cancel the user asked for is not a FAILURE —
    // that is why the interrupted turn above had to be tested on its own outcome rather than on the
    // chat's.
    const turns = turnsOf("reported-interrupted-hollow-dot");
    expect(outcomeLatch(turns[1]?.outcome)).toBe("done");
  });
});

describe("the four surfaces agree, per turn, across both records", () => {
  it("never shows a failure a reader could mistake for nothing happening", () => {
    // The join, stated as one property over every turn in both real transcripts: whenever the
    // severity is `broken`, ALL FOUR surfaces must say so — the turn has text to show, it refuses
    // to auto-fold, and the tab latches a failure.
    for (const name of Object.keys(fixture)) {
      const turns = turnsOf(name);
      for (const [i, t] of turns.entries()) {
        const severity = severityOf(t.outcome);
        const where = `${name} turn ${String(i + 1)} (${t.outcome})`;
        if (severity !== "broken") {
          continue;
        }
        expect(turnFailureText(t), `${where}: has inline text`).not.toBe("");
        expect(outcomeLatch(t.outcome), `${where}: latches a failure`).toBe("failed");
        expect(isTurnOpen("c1", t, i, turns.length), `${where}: does not auto-fold`).toBe(true);
      }
    }
  });

  it("does not overstate the turns that merely stopped, and does not erase them either", () => {
    // The other direction, and it is what keeps the property above from being satisfiable by
    // painting everything red: a `cancelled` or `unknown` turn is never latched as a FAILURE and
    // folds like any other.
    for (const name of Object.keys(fixture)) {
      const turns = turnsOf(name);
      for (const [i, t] of turns.entries()) {
        if (severityOf(t.outcome) !== "stopped") {
          continue;
        }
        const where = `${name} turn ${String(i + 1)} (${t.outcome})`;
        expect(outcomeLatch(t.outcome), `${where}: not a failure`).not.toBe("failed");
        expect(outcomeLatch(t.outcome), `${where}: not the hollow ring either`).toBe("done");
        if (t.outcome === "cancelled") {
          expect(turnFailureText(t), `${where}: the footer carries it`).toBe("");
        } else {
          expect(turnFailureText(t), `${where}: still says something`).not.toBe("");
        }
      }
    }
  });

  it("covers at least one broken and one stopped turn, or the two above are vacuous", () => {
    // Both properties are `for` loops with a `continue`, so an empty match set passes them. This is
    // the guard that makes them mean something, and it is what would fail if the fixture were ever
    // reduced to clean turns.
    const severities = Object.keys(fixture)
      .flatMap((name) => turnsOf(name))
      .map((t) => severityOf(t.outcome));
    expect(severities).toContain("broken");
    expect(severities).toContain("stopped");
    expect(severities).toContain("clean");
  });
});

// The four later items, over the same two real records.

describe("a turn whose end marotte could not read still says something", () => {
  /** Chat 1's own first turn closed on `outcome` — derived from the real record rather than
   *  authored, so the ids, roles and timestamps are a live chat file's. */
  function firstTurnAs(name: string, outcome: TurnOutcome): Turn {
    const chat = chatNamed(name);
    const first = chat.messages[0];
    if (first?.role !== "user") {
      throw new Error(`${name} does not open on a user message, so this shape is not derivable`);
    }
    const src = foldChat({ chat_id: chat.chat_id, messages: [first] });
    const [turnID] = src.turn_order;
    const state = turnID === undefined ? undefined : src.turns.get(turnID);
    if (turnID === undefined || state === undefined) {
      throw new Error(`the fold produced no turn for ${name}`);
    }
    state.closeAt = state.entries.length;
    state.entries.push({
      id: `${turnID}-close`,
      turn: turnID,
      kind: "turn_close",
      seq: state.closeAt,
      ts: first.ts,
      payload: { outcome } satisfies EntryTurnClose,
    });
    const turns = projectTurns(src);
    const turn = turns[0];
    if (turns.length !== 1 || turn === undefined) {
      throw new Error(`projected ${String(turns.length)} turns, want 1`);
    }
    return turn;
  }

  it("carries inline text, so the turn is not a mark over an empty body", () => {
    expect(turnFailureText(firstTurnAs("reported-failed-no-reason", "unknown"))).toBe(
      "The turn ended for a reason marotte could not read.",
    );
  });

  it("latches DONE rather than a failure or the hollow ring", () => {
    // `unknown` is graded `stopped`, so it must not paint red — the wire never reported a failure —
    // and must not paint the hollow ring either, because the chat did run a turn.
    expect(outcomeLatch(firstTurnAs("reported-failed-no-reason", "unknown").outcome)).toBe("done");
  });

  it("does not read the same for a turn that DID answer", () => {
    // The carve-out that stops the clause widening: chat 1's real first turn holds an assistant
    // carrier, so it keeps its own settled outcome. Without this the cases above would pass for a
    // rule that graded every turn `unknown`.
    expect(turnsOf("reported-failed-no-reason")[0]?.outcome).toBe("failed");
  });

  it("reads RUNNING while nothing has closed it, and grades it as no outcome at all", () => {
    // The entry model's own answer for the shape the deleted derivation guessed at: a turn with no
    // `turn_close` is not an outcome, it is a turn still going.
    const chat = chatNamed("reported-failed-no-reason");
    const first = chat.messages[0];
    if (first === undefined) {
      throw new Error("the fixture chat is empty");
    }
    const [open] = projectTurns(foldChat({ chat_id: chat.chat_id, messages: [first] }));
    expect(open?.outcome).toBe("running");
    expect(open === undefined ? "x" : turnFailureText(open)).toBe("");
    expect(outcomeLatch(open?.outcome)).toBe("");
  });
});

describe("a DISCARDED turn is a stop, not a failure and not a silence", () => {
  it("latches done and is never broken", () => {
    const cancelled = turnsOf("reported-interrupted-hollow-dot")[1];
    expect(cancelled?.outcome).toBe("cancelled");
    expect(severityOf(cancelled?.outcome)).not.toBe("broken");
    expect(outcomeLatch(cancelled?.outcome)).toBe("done");
  });
});

describe("the push gate has words for every turn it speaks for", () => {
  it("never has to push an empty sentence", () => {
    // Here rather than in the handler's own suite: both push gates build their body from
    // `defaultFailureReason`, so a `broken` outcome with no sentence would notify with nothing.
    for (const name of Object.keys(fixture)) {
      for (const [i, t] of turnsOf(name).entries()) {
        if (severityOf(t.outcome) !== "broken") {
          continue;
        }
        expect(
          defaultFailureReason(t.outcome),
          `${name} turn ${String(i + 1)} (${t.outcome}) has no sentence to push`,
        ).not.toBe("");
      }
    }
  });

  it("says nothing for a turn that ended cleanly", () => {
    // The other direction: "" is how both gates spell "notify nothing", so a clean turn gaining a
    // sentence would make the empty string stop meaning that.
    const clean = turnsOf("reported-failed-no-reason")[2];
    expect(clean?.outcome).toBe("completed");
    expect(defaultFailureReason(clean?.outcome)).toBe("");
  });
});

describe("the fixture is the real record, not a hand-written one", () => {
  it("carries both reported chat ids", () => {
    expect(fixture["reported-failed-no-reason"]?.chat_id).toBe(
      "c-c4b7910007414d0e0759fc263cbb2146",
    );
    expect(fixture["reported-interrupted-hollow-dot"]?.chat_id).toBe(
      "c-a7f83c9ff14d180d6cf44e9caea44f45",
    );
  });

  it("still holds the two properties that made the report reproducible", () => {
    // If either of these stops being true the fixture has been edited into something that no longer
    // reproduces the bug, and every assertion above is measuring a different transcript.
    const chat1 = fixture["reported-failed-no-reason"]?.messages ?? [];
    const failed = chat1.find((m) => m.turn_outcome === "failed" && m.role === "assistant");
    expect(failed, "chat 1 has a failed assistant carrier").toBeDefined();
    expect(failed?.turn_failure_reason, "and it records NO reason").toBeUndefined();
    expect(failed?.content, "and no prose either").toBeUndefined();

    const chat2 = fixture["reported-interrupted-hollow-dot"]?.messages ?? [];
    const interrupted = chat2.find((m) => m.turn_outcome === "interrupted");
    expect(interrupted, "chat 2 has an interrupted carrier").toBeDefined();
    expect(
      chat2.find((m) => m.event_kind === "interrupted")?.content,
      "and its reason is on the divider, durably",
    ).toContain("network error");
  });
});
