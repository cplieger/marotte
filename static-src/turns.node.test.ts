// turns.ts, the TURN PROJECTION over the entry log. The store already partitioned it: ordinal is
// `turn_open.n`, verdict `turn_close.outcome`, and a turn with no close IS running (the store closes
// crash-orphaned turns before serving). Pure, hence `.node`.

import { describe, it, expect } from "vitest";
import { toolResultID } from "./entry-ids.js";
import {
  COMMAND_KINDS,
  closeOfBody,
  entryUnion,
  isEntryKind,
  payloadOf,
  projectTurn,
  projectTurns,
  turnAnchorID,
  turnCloseOf,
  turnFaceProse,
  turnFailureText,
  turnFoldHides,
  turnIsDrawn,
  turnLedger,
  turnOpenOf,
  type Turn,
  type TurnSource,
} from "./turns.js";
import type { OpenEntry, ToolInteraction, TurnState } from "./types.js";
import type { Entry, EntryToolCall, EntryTurnClose } from "./wire/types.gen.js";

// --- Fixtures ---------------------------------------------------------------
//
// One builder per real log shape, matching store.test.ts's. `Object.assign` for optional members:
// under `exactOptionalPropertyTypes` a `Partial` spread widens required fields to `undefined`.

/** The `turn_open` that opens `turnID` at session-absolute ordinal `n`. A prompt
 *  makes it reader-opened, which is the first clause of `turnIsDrawn`. */
function turnOpen(
  turnID: string,
  n: number,
  opts: { readonly source?: string; readonly prompt?: string; readonly ts?: number } = {},
): Entry {
  const source = opts.source ?? (opts.prompt === undefined ? "wire_turn_start" : "prompt");
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: opts.ts ?? 1,
    payload: {
      source,
      n,
      ...(opts.prompt !== undefined && { prompt: { id: opts.prompt, text: "do a thing" } }),
    },
  };
}

/** A sealed entry of any kind at `seq`. `lane` is absent unless named, which is the
 *  transcript's own lane. */
function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown = {},
  opts: { readonly id?: string; readonly lane?: string; readonly ts?: number } = {},
): Entry {
  const base: Entry = {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: opts.ts ?? seq + 1,
    payload,
  };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

function textEntry(turnID: string, seq: number, text = "hi", lane?: string): Entry {
  return sealed(turnID, seq, "text", { text }, lane === undefined ? {} : { lane });
}

function closeEntry(turnID: string, seq: number, over: Partial<EntryTurnClose> = {}): Entry {
  const payload: EntryTurnClose = Object.assign({ outcome: "completed" } as EntryTurnClose, over);
  return sealed(turnID, seq, "turn_close", payload);
}

/** A tool call ENTRY. The entry id IS the payload id, because the appender
 *  round-trips KAS's `toolCallId` as both — which is what makes `toolResultID`
 *  below a join with no table behind it. */
function callEntry(
  turnID: string,
  seq: number,
  callID: string,
  over: Partial<EntryToolCall> = {},
): Entry {
  const payload: EntryToolCall = Object.assign(
    {
      id: callID,
      title: "Run Command",
      kind: "execute",
      status: "completed",
      ts: 1,
    } as EntryToolCall,
    over,
  );
  return sealed(turnID, seq, "tool_call", payload, { id: callID });
}

/** The settled value of `callID`, keyed `<call>:result` so the ledger can pair it. */
function resultEntry(
  turnID: string,
  seq: number,
  callID: string,
  over: { readonly duration_ms?: number; readonly interaction?: ToolInteraction } = {},
): Entry {
  return sealed(turnID, seq, "tool_result", Object.assign({ status: "completed" }, over), {
    id: toolResultID(callID),
  });
}

function state(
  entries: readonly Entry[],
  opts: { readonly open?: readonly OpenEntry[]; readonly closeAt?: number } = {},
): TurnState {
  const openEntries = new Map<string, OpenEntry>();
  for (const o of opts.open ?? []) {
    openEntries.set(o.lane ?? "", o);
  }
  const base: TurnState = { entries: [...entries], openEntries };
  return opts.closeAt === undefined ? base : Object.assign(base, { closeAt: opts.closeAt });
}

/** The two store fields a projection reads, in file order. */
function src(...pairs: readonly (readonly [string, TurnState])[]): TurnSource {
  return { turns: new Map(pairs), turn_order: pairs.map(([id]) => id) };
}

/** One reader-opened turn holding `body`, projected. Throws rather than answering a
 *  `Turn | undefined`, so a fixture that stopped producing a turn fails loudly at
 *  the arrange step instead of silently passing an assertion on `undefined`. */
function oneTurn(body: readonly Entry[], opts: { readonly n?: number } = {}): Turn {
  const t = projectTurns(
    src(["t1", state([turnOpen("t1", opts.n ?? 1, { prompt: "m-1" }), ...body])]),
  )[0];
  if (t === undefined) {
    throw new Error("the fixture produced no turn");
  }
  return t;
}

/** An open tail, one per `(turn, lane)`. It has no `seq` and never reaches the log. */
function openTail(turnID: string, lane = ""): OpenEntry {
  const base: OpenEntry = {
    turn: turnID,
    id: `${turnID}-open-tail`,
    kind: "text",
    text: "so far",
    n: 1,
  };
  return lane === "" ? base : Object.assign(base, { lane });
}

// --- Reading an entry -------------------------------------------------------

describe("narrowing an entry to its kind", () => {
  it("payloadOf answers the payload for a matching kind and nothing for another", () => {
    const e = textEntry("t1", 1, "the answer");
    expect(payloadOf(e, "text")?.text).toBe("the answer");
    expect(payloadOf(e, "tool_call")).toBeUndefined();
  });

  it("isEntryKind narrows, so a caller reads a typed payload off the entry itself", () => {
    const e = closeEntry("t1", 1, { credits: 0.5 });
    expect(isEntryKind(e, "turn_close")).toBe(true);
    expect(isEntryKind(e, "text")).toBe(false);
    if (isEntryKind(e, "turn_close")) {
      expect(e.payload.credits).toBe(0.5);
    }
  });

  it("entryUnion hands back the same object, so a dispatcher switches without a decode", () => {
    // Identity is the assertion: a copy would mean something re-decoded it.
    const e = textEntry("t1", 1);
    expect(entryUnion(e)).toBe(e);
  });
});

describe("turnOpenOf", () => {
  it("reads the opening entry's payload", () => {
    const t = state([turnOpen("t1", 7, { prompt: "m-1" })]);
    expect(turnOpenOf(t)?.n).toBe(7);
    expect(turnOpenOf(t)?.prompt?.id).toBe("m-1");
  });

  it("answers nothing for a turn whose open the caller never saw", () => {
    // A HOLE. The store answers one with a range read rather than a repair, so the
    // projection's job is only to refuse to render it.
    expect(turnOpenOf(state([]))).toBeUndefined();
    expect(turnOpenOf(state([textEntry("t1", 1)]))).toBeUndefined();
  });
});

describe("turnCloseOf", () => {
  it("reads the close off the store's cached index", () => {
    const t = state([turnOpen("t1", 1), textEntry("t1", 1), closeEntry("t1", 2, { credits: 3 })], {
      closeAt: 2,
    });
    expect(turnCloseOf(t)?.credits).toBe(3);
  });

  it("scans backward when the store recorded no index", () => {
    // The store records `closeAt` at the append that closes the turn; a turn read
    // back from a range read may arrive without one, and a `turn_close` is an
    // ordinary entry that is almost always last.
    const t = state([turnOpen("t1", 1), textEntry("t1", 1), closeEntry("t1", 2, { credits: 3 })]);
    expect(turnCloseOf(t)?.credits).toBe(3);
  });

  it("answers nothing for an OPEN turn, which is what makes `running` exact", () => {
    expect(turnCloseOf(state([turnOpen("t1", 1), textEntry("t1", 1)]))).toBeUndefined();
  });
});

describe("closeOfBody", () => {
  it("finds the close wherever it sits in a projected body", () => {
    // A turn's entries are not a contiguous byte range and a later append can land
    // after the close is written, so the scan is backward rather than a tail read.
    expect(
      closeOfBody([textEntry("t1", 1), closeEntry("t1", 2), textEntry("t1", 3)])?.outcome,
    ).toBe("completed");
  });

  it("answers nothing for a body carrying none", () => {
    expect(closeOfBody([textEntry("t1", 1)])).toBeUndefined();
  });
});

// --- Whether a turn is DRAWN ------------------------------------------------

describe("turnIsDrawn", () => {
  it("refuses a turn with no opening entry", () => {
    expect(turnIsDrawn(state([textEntry("t1", 1)]))).toBe(false);
  });

  it.each(["prompt", "local_shell", "empty_retry"])(
    "draws a %s turn on its header alone, with no body at all",
    (source) => {
      expect(turnIsDrawn(state([turnOpen("t1", 1, { source })]))).toBe(true);
    },
  );

  it.each(["wire_turn_start", "event", "workflow_step"])(
    "does not draw an empty %s turn — there is no header to render",
    (source) => {
      expect(turnIsDrawn(state([turnOpen("t1", 1, { source })]))).toBe(false);
    },
  );

  it("draws an agent-initiated turn on a body entry that renders at its own position", () => {
    expect(turnIsDrawn(state([turnOpen("t1", 1), textEntry("t1", 1)]))).toBe(true);
  });

  it.each(["turn_bind", "tool_result", "turn_close"] as const)(
    "does not draw an agent-initiated turn whose only body entry is a %s",
    (kind) => {
      // These three render ELSEWHERE — a bind renders nothing, a result renders as its
      // call's outcome, a close feeds the footer — so none of them is content at a
      // position of its own.
      expect(turnIsDrawn(state([turnOpen("t1", 1), sealed("t1", 1, kind)]))).toBe(false);
    },
  );

  it("draws an agent-initiated turn whose only body entry is a steer_ack", () => {
    // An ack is AGENT content at its own position rather than a fold into the steer's
    // note, so a turn carrying nothing else still has a card to render it in.
    expect(turnIsDrawn(state([turnOpen("t1", 1), sealed("t1", 1, "steer_ack")]))).toBe(true);
  });

  it("does not draw a turn whose only body entry is a delegate's", () => {
    // A non-empty lane is a delegate's work, and this predicate answers for the
    // transcript, whose root lane is "".
    expect(turnIsDrawn(state([turnOpen("t1", 1), textEntry("t1", 1, "report", "sub-A")]))).toBe(
      false,
    );
  });

  it("draws an agent-initiated turn on its FIRST plan", () => {
    // The plan clause cannot flip the verdict — the first plan reached returns true —
    // so there is no case for a SECOND plan here: it could not fail. The clause is
    // written literally in the module so the predicate reads as its own statement.
    expect(turnIsDrawn(state([turnOpen("t1", 1), sealed("t1", 1, "plan", { entries: [] })]))).toBe(
      true,
    );
  });

  it('draws a headerless turn on an OPEN entry in lane "", which the server cannot', () => {
    // THE CLIENT-ONLY CLAUSE. A headerless turn's first text streams as an open entry
    // and never reaches the log, so without this the card its bubble mounts into would
    // not exist until the turn ended.
    expect(turnIsDrawn(state([turnOpen("t1", 1)], { open: [openTail("t1")] }))).toBe(true);
  });

  it("is not drawn by a DELEGATE's open entry", () => {
    // The open clause is lane-scoped for the body clause's reason: a delegate's
    // stream is not the transcript's content.
    expect(turnIsDrawn(state([turnOpen("t1", 1)], { open: [openTail("t1", "sub-A")] }))).toBe(
      false,
    );
  });

  it("stays drawn after the open entry seals, because the sealed entry then carries it", () => {
    const t = state([turnOpen("t1", 1), textEntry("t1", 1)]);
    expect(turnIsDrawn(t)).toBe(true);
  });
});

// --- The projection ---------------------------------------------------------

describe("projectTurns", () => {
  it("reads the log in file order, one turn per `turn_order` entry", () => {
    const turns = projectTurns(
      src(
        ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
        ["t2", state([turnOpen("t2", 2, { prompt: "m-2" })])],
      ),
    );
    expect(turns.map((t) => t.id)).toEqual(["t1", "t2"]);
  });

  it("takes the ordinal from `turn_open.n` rather than the window's position", () => {
    // Session-absolute by construction: the appender assigned it at open, so a
    // paginated window and the rail's index agree without a join and nothing shifts
    // when an older page loads.
    const turns = projectTurns(
      src(
        ["t9", state([turnOpen("t9", 9, { prompt: "m-9" })])],
        ["t10", state([turnOpen("t10", 10, { prompt: "m-10" })])],
      ),
    );
    expect(turns.map((t) => t.n)).toEqual([9, 10]);
  });

  it("promotes the open entry's prompt to the trigger and keeps the rest as body", () => {
    const t = oneTurn([textEntry("t1", 1), closeEntry("t1", 2)]);
    expect(t.trigger?.id).toBe("m-1");
    expect(t.body.map((e) => e.seq)).toEqual([1, 2]);
  });

  it("leaves the trigger absent on a turn nobody prompted", () => {
    // The header then renders a typed trigger line rather than putting words in the
    // reader's mouth.
    const t = projectTurns(src(["t1", state([turnOpen("t1", 1), textEntry("t1", 1)])]))[0];
    expect(t?.trigger).toBeUndefined();
  });

  it("takes the turn's start stamp from its opening entry", () => {
    const t = projectTurns(
      src(["t1", state([turnOpen("t1", 1, { prompt: "m-1", ts: 500 }), textEntry("t1", 1)])]),
    )[0];
    expect(t?.ts).toBe(500);
  });

  it("carries each lane's open entry BESIDE the body, never in it", () => {
    // An open entry has no `seq` and no position, so nothing can render
    // it above a sealed entry that arrived later.
    const t = projectTurns(
      src(["t1", state([turnOpen("t1", 1, { prompt: "m-1" })], { open: [openTail("t1")] })]),
    )[0];
    expect(t?.body).toEqual([]);
    expect(t?.openEntries.get("")?.text).toBe("so far");
  });

  it("drops a turn the store holds no state for", () => {
    const source: TurnSource = { turns: new Map(), turn_order: ["t1"] };
    expect(projectTurns(source)).toEqual([]);
  });

  it("drops a turn whose opening entry is missing", () => {
    expect(projectTurns(src(["t1", state([textEntry("t1", 1)])]))).toEqual([]);
  });

  it("drops an undrawn turn and keeps its neighbours' ordinals intact", () => {
    // A bind-only agent turn renders nothing, and the ordinals either side are the
    // appender's, so dropping it cannot renumber anything.
    const turns = projectTurns(
      src(
        ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
        ["t2", state([turnOpen("t2", 2), sealed("t2", 1, "turn_bind", { kas_message_id: "k" })])],
        ["t3", state([turnOpen("t3", 3, { prompt: "m-3" })])],
      ),
    );
    expect(turns.map((t) => [t.id, t.n])).toEqual([
      ["t1", 1],
      ["t3", 3],
    ]);
  });

  it("returns nothing for an empty log", () => {
    expect(projectTurns(src())).toEqual([]);
  });

  describe("outcome", () => {
    it("is the close's own verdict, read verbatim", () => {
      // Nothing derives it and nothing infers precedence: one turn has one close, and
      // the server already decided.
      for (const outcome of [
        "completed",
        "cancelled",
        "interrupted",
        "failed",
        "refused",
      ] as const) {
        const t = oneTurn([textEntry("t1", 1), closeEntry("t1", 2, { outcome })]);
        expect(t.outcome, outcome).toBe(outcome);
      }
    });

    it("is running for a turn with no close, whatever the turn holds", () => {
      expect(oneTurn([]).outcome).toBe("running");
      expect(oneTurn([textEntry("t1", 1)]).outcome).toBe("running");
      expect(oneTurn([callEntry("t1", 1, "c1")]).outcome).toBe("running");
    });

    it("is not moved by an entry that would once have implied one", () => {
      // A model switch, a safety block and a failed compaction are entries at their own
      // positions now. None of them ends a turn, so none of them is an outcome.
      const t = oneTurn([
        sealed("t1", 1, "model_switched", { from: "a", to: "b" }),
        sealed("t1", 2, "safety_blocked", {}),
        sealed("t1", 3, "compaction_failed", {}),
        closeEntry("t1", 4, { outcome: "completed" }),
      ]);
      expect(t.outcome).toBe("completed");
    });

    it("reads a close that is not the turn's last entry", () => {
      const t = oneTurn([closeEntry("t1", 1, { outcome: "failed" }), textEntry("t1", 2)]);
      expect(t.outcome).toBe("failed");
    });
  });

  describe("rewindTo", () => {
    it("addresses the NEXT turn's prompt, because rewind reverts to after this turn", () => {
      // KAS drops the message it is given plus everything following, so keeping turn N
      // means addressing turn N+1's prompt.
      const turns = projectTurns(
        src(
          ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
          ["t2", state([turnOpen("t2", 2, { prompt: "m-2" })])],
        ),
      );
      expect(turns[0]?.rewindTo?.id).toBe("m-2");
    });

    it("is absent on the last turn — there is nothing after it to discard", () => {
      const turns = projectTurns(
        src(
          ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
          ["t2", state([turnOpen("t2", 2, { prompt: "m-2" })])],
        ),
      );
      expect(turns[1]?.rewindTo).toBeUndefined();
    });

    it("is absent when the next turn carries no prompt, which KAS refuses to revert to", () => {
      const turns = projectTurns(
        src(
          ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
          ["t2", state([turnOpen("t2", 2), textEntry("t2", 1)])],
        ),
      );
      expect(turns).toHaveLength(2);
      expect(turns[0]?.rewindTo).toBeUndefined();
    });

    it("skips an UNDRAWN neighbour, because a rewind may only address a rendered turn", () => {
      const turns = projectTurns(
        src(
          ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
          ["t2", state([turnOpen("t2", 2), sealed("t2", 1, "turn_bind", {})])],
          ["t3", state([turnOpen("t3", 3, { prompt: "m-3" })])],
        ),
      );
      expect(turns[0]?.rewindTo?.id).toBe("m-3");
    });
  });
});

// --- ONE turn of the source -------------------------------------------------

describe("projectTurn", () => {
  /** Two turns, the first failed and holding a body, the second a bare prompt. */
  function pair(): TurnSource {
    return src(
      [
        "t1",
        state([
          turnOpen("t1", 1, { prompt: "m-1", ts: 700 }),
          textEntry("t1", 1),
          closeEntry("t1", 2, { outcome: "failed" }),
        ]),
      ],
      ["t2", state([turnOpen("t2", 2, { prompt: "m-2" })])],
    );
  }

  it("projects the one turn a keyed pass names", () => {
    // The whole projection allocates a `Turn` and slices a body per resident turn,
    // which a per-frame tool update must not do.
    const t = projectTurn(pair(), "t1");
    expect(t?.id).toBe("t1");
    expect(t?.n).toBe(1);
    expect(t?.trigger?.id).toBe("m-1");
    expect(t?.body.map((e) => e.seq)).toEqual([1, 2]);
    expect(t?.ts).toBe(700);
    expect(t?.outcome).toBe("failed");
  });

  it("answers exactly what the whole projection answers for the same turn", () => {
    // The two are one rule with two entry points, so the differential is what stops
    // them drifting — a complete `Turn` rather than one with a field left empty.
    const source = pair();
    expect(projectTurn(source, "t1")).toEqual(projectTurns(source)[0]);
    expect(projectTurn(source, "t2")).toEqual(projectTurns(source)[1]);
  });

  it("carries each lane's open entry beside the body here too", () => {
    const source = src([
      "t1",
      state([turnOpen("t1", 1, { prompt: "m-1" })], { open: [openTail("t1")] }),
    ]);
    const t = projectTurn(source, "t1");
    expect(t?.body).toEqual([]);
    expect(t?.openEntries.get("")?.text).toBe("so far");
  });

  it("answers nothing for a turn `turn_order` does not name", () => {
    // File order is the projection's only order, so a turn the log has not placed is
    // not a turn a reader can be shown — even when the store holds its state.
    const source: TurnSource = {
      turns: new Map([["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])]]),
      turn_order: [],
    };
    expect(projectTurn(source, "t1")).toBeUndefined();
  });

  it("answers nothing when the store holds no state for the turn", () => {
    const source: TurnSource = { turns: new Map(), turn_order: ["t1"] };
    expect(projectTurn(source, "t1")).toBeUndefined();
  });

  it("answers nothing for a turn whose opening entry is missing", () => {
    expect(projectTurn(src(["t1", state([textEntry("t1", 1)])]), "t1")).toBeUndefined();
  });

  it("answers nothing for a turn `turnIsDrawn` refuses", () => {
    // A bind-only agent turn renders nothing, so the keyed pass must refuse it exactly
    // as the whole projection drops it.
    const source = src([
      "t1",
      state([turnOpen("t1", 1), sealed("t1", 1, "turn_bind", { kas_message_id: "k" })]),
    ]);
    expect(projectTurn(source, "t1")).toBeUndefined();
    expect(projectTurns(source)).toEqual([]);
  });

  it("resolves rewindTo through the NEXT turn in file order", () => {
    expect(projectTurn(pair(), "t1")?.rewindTo?.id).toBe("m-2");
  });

  it("skips an UNDRAWN neighbour when resolving rewindTo", () => {
    // The same undrawn-neighbour walk `projectTurns` performs: a rewind may only
    // address a turn the reader can see.
    const source = src(
      ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
      ["t2", state([turnOpen("t2", 2), sealed("t2", 1, "turn_bind", {})])],
      ["t3", state([turnOpen("t3", 3, { prompt: "m-3" })])],
    );
    expect(projectTurn(source, "t1")?.rewindTo?.id).toBe("m-3");
  });

  it("leaves rewindTo absent on the last turn, and on a next turn with no prompt", () => {
    expect(projectTurn(pair(), "t2")?.rewindTo).toBeUndefined();
    const source = src(
      ["t1", state([turnOpen("t1", 1, { prompt: "m-1" })])],
      ["t2", state([turnOpen("t2", 2), textEntry("t2", 1)])],
    );
    expect(projectTurn(source, "t1")?.rewindTo).toBeUndefined();
  });
});

// --- The ledger -------------------------------------------------------------

describe("turnLedger", () => {
  it("takes the aggregate off the one close entry and SUMS nothing", () => {
    // One turn has one close carrying the whole aggregate, so there is no second
    // carrier to add to and no split to read through.
    const t = oneTurn([
      textEntry("t1", 1),
      closeEntry("t1", 2, {
        credits: 0.75,
        elapsed_ms: 2000,
        stop_reason_raw: "max_tokens",
        truncated: true,
        changed_files: { "a.ts": { lines_added: 3, lines_removed: 1 } },
      }),
    ]);
    const led = turnLedger(t);
    expect(led.credits).toBeCloseTo(0.75);
    expect(led.elapsedMs).toBe(2000);
    expect(led.stopReasonRaw).toBe("max_tokens");
    expect(led.truncated).toBe(true);
    expect(led.changedFiles).toEqual({ "a.ts": { lines_added: 3, lines_removed: 1 } });
  });

  it("carries the close's changed-file map VERBATIM", () => {
    // It is a cumulative per-turn snapshot rather than a delta, and one turn has
    // exactly one close, so nothing merges and nothing is added.
    const files = {
      "a.ts": { lines_added: 5, lines_removed: 2 },
      "b.ts": { lines_added: 1, lines_removed: 0, is_new_file: true },
    };
    const led = turnLedger(oneTurn([closeEntry("t1", 1, { changed_files: files })]));
    expect(led.changedFiles).toEqual(files);
  });

  it("tallies each settled call's answered ask by its class", () => {
    const allow = { type: "tool_approval", outcome: "selected", choice: "allow_once" };
    const led = turnLedger(
      oneTurn([
        callEntry("t1", 1, "c1"),
        resultEntry("t1", 2, "c1", { interaction: allow }),
        callEntry("t1", 3, "c2"),
        resultEntry("t1", 4, "c2", { interaction: allow }),
        callEntry("t1", 5, "c3"),
        resultEntry("t1", 6, "c3", {
          interaction: { type: "user_input", outcome: "dismissed" },
        }),
        callEntry("t1", 7, "c4"),
        resultEntry("t1", 8, "c4"),
      ]),
    );
    expect(led.asks).toEqual({ allowed: 2, skipped: 1 });
  });

  it("carries the close's KAS facts verbatim", () => {
    const led = turnLedger(
      oneTurn([
        closeEntry("t1", 1, {
          request_ids: ["r1"],
          throughput: { estimated_tokens: 10, active_streaming_ms: 5 },
          recoveries: ["empty"],
          steering: ["file:///a.md"],
          engine_error_class: "ModelOverloaded",
        }),
      ]),
    );
    expect(led.requestIds).toEqual(["r1"]);
    expect(led.throughput).toEqual({ estimated_tokens: 10, active_streaming_ms: 5 });
    expect(led.recoveries).toEqual(["empty"]);
    expect(led.steering).toEqual(["file:///a.md"]);
    expect(led.engineErrorClass).toBe("ModelOverloaded");
  });

  it("reports a zeroed ledger for a turn that stamped nothing", () => {
    const led = turnLedger(oneTurn([textEntry("t1", 1)]));
    expect(led.credits).toBe(0);
    expect(led.elapsedMs).toBe(0);
    expect(led.changedFiles).toEqual({});
    expect(led.stopReasonRaw).toBe("");
    expect(led.truncated).toBe(false);
  });

  describe("models", () => {
    it("names the model that finished the turn", () => {
      const led = turnLedger(oneTurn([closeEntry("t1", 1, { model: "sonnet-4" })]));
      expect(led.models).toEqual(["sonnet-4"]);
    });

    it("carries every model in emission order, from the switch entries", () => {
      // `turn_close.model` records only the model that FINISHED, so a mid-turn switch
      // is legible solely from the entries it appended.
      const led = turnLedger(
        oneTurn([
          sealed("t1", 1, "model_switched", { from: "sonnet-4", to: "opus-5" }),
          closeEntry("t1", 2, { model: "opus-5" }),
        ]),
      );
      expect(led.models).toEqual(["sonnet-4", "opus-5"]);
    });

    it("does not repeat a model named more than once", () => {
      const led = turnLedger(
        oneTurn([
          sealed("t1", 1, "model_switched", { from: "sonnet-4", to: "opus-5" }),
          sealed("t1", 2, "model_switched", { from: "opus-5", to: "sonnet-4" }),
          closeEntry("t1", 3, { model: "sonnet-4" }),
        ]),
      );
      expect(led.models).toEqual(["sonnet-4", "opus-5"]);
    });

    it('reports no model at all rather than "unknown"', () => {
      expect(turnLedger(oneTurn([textEntry("t1", 1)])).models).toEqual([]);
    });

    it("ignores an empty model string, which is what nothing-stamped-it looks like", () => {
      expect(turnLedger(oneTurn([closeEntry("t1", 1, { model: "" })])).models).toEqual([]);
    });
  });

  describe("tool work", () => {
    it("counts each kind that made a call, and gives a kind with none NO entry", () => {
      // PARTIAL over `ToolKind`: an entry reading zero is a measurement, and nobody
      // made one.
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1", { kind: "execute" }),
          callEntry("t1", 2, "c2", { kind: "read" }),
          callEntry("t1", 3, "c3", { kind: "read" }),
          callEntry("t1", 4, "c4", { kind: "edit" }),
        ]),
      );
      expect(led.kindCounts).toEqual({ execute: 1, read: 2, edit: 1 });
    });

    it("sums tool time off the RESULTS, and counts an unstamped one as nothing", () => {
      // ZERO MEANS NOBODY STAMPED ONE, so a result that never settled a duration must
      // not read as a third measurement of zero.
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1"),
          callEntry("t1", 2, "c2"),
          resultEntry("t1", 3, "c1", { duration_ms: 500 }),
          resultEntry("t1", 4, "c2"),
        ]),
      );
      expect(led.toolMs).toBe(500);
    });

    it("is not bounded by the turn's elapsed time, because calls overlap", () => {
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1"),
          resultEntry("t1", 2, "c1", { duration_ms: 9000 }),
          closeEntry("t1", 3, { elapsed_ms: 1000 }),
        ]),
      );
      expect(led.toolMs).toBe(9000);
      expect(led.elapsedMs).toBe(1000);
    });

    it("counts the calls that OPEN a delegate, and their time, by pairing on the id", () => {
      // Counted off the invocation's `agent_subtask_id`, so it counts the call that
      // dispatched the delegate and not the nested calls the delegate then made. The
      // pairing is the entry id — `<call>:result` — with no join table behind it.
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1", { kind: "other", agent_subtask_id: "sub-A" }),
          callEntry("t1", 2, "c2", { kind: "read" }),
          resultEntry("t1", 3, "c1", { duration_ms: 9000 }),
          resultEntry("t1", 4, "c2", { duration_ms: 25 }),
        ]),
      );
      expect(led.delegateCount).toBe(1);
      expect(led.delegateMs).toBe(9000);
      expect(led.toolMs).toBe(9025);
    });

    it("ignores an empty subtask id, which is what a non-delegate call carries", () => {
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1", { agent_subtask_id: "" }),
          resultEntry("t1", 2, "c1", { duration_ms: 40 }),
        ]),
      );
      expect(led.delegateCount).toBe(0);
      expect(led.delegateMs).toBe(0);
      expect(led.toolMs).toBe(40);
    });

    it("charges a DELEGATE's own nested call to tool time and not to delegate time", () => {
      // The delegate's lane is where its nested work lives, and the pairing is on the
      // invocation's id, so nesting cannot double-count.
      const led = turnLedger(
        oneTurn([
          callEntry("t1", 1, "c1", { agent_subtask_id: "sub-A" }),
          sealed(
            "t1",
            2,
            "tool_call",
            { id: "c2", title: "Read File", kind: "read", status: "completed", ts: 1 },
            { id: "c2", lane: "sub-A" },
          ),
          sealed(
            "t1",
            3,
            "tool_result",
            { status: "completed", duration_ms: 30 },
            {
              id: toolResultID("c2"),
              lane: "sub-A",
            },
          ),
          resultEntry("t1", 4, "c1", { duration_ms: 100 }),
        ]),
      );
      expect(led.delegateCount).toBe(1);
      expect(led.delegateMs).toBe(100);
      expect(led.toolMs).toBe(130);
    });
  });

  describe("stamps", () => {
    it("takes both ends from append stamps, never from startedAt plus elapsed", () => {
      // `elapsed_ms` is the agent's own duration and nothing on the wire carries a
      // turn end, so the two are different measurements of different things.
      const t = projectTurns(
        src([
          "t1",
          state([
            turnOpen("t1", 1, { prompt: "m-1", ts: 1000 }),
            textEntry("t1", 1),
            sealed(
              "t1",
              2,
              "turn_close",
              { outcome: "completed", elapsed_ms: 50 } as EntryTurnClose,
              {
                ts: 9000,
              },
            ),
          ]),
        ]),
      )[0];
      const led = turnLedger(t as Turn);
      expect(led.startedAt).toBe(1000);
      expect(led.endedAt).toBe(9000);
      expect(led.elapsedMs).toBe(50);
    });

    it("reports no end stamp for a turn that sealed nothing", () => {
      const t = projectTurns(src(["t1", state([turnOpen("t1", 1, { prompt: "m-1", ts: 42 })])]))[0];
      const led = turnLedger(t as Turn);
      expect(led.startedAt).toBe(42);
      expect(led.endedAt).toBe(0);
    });
  });
});

describe("COMMAND_KINDS", () => {
  it("names the three kinds that mean a command ran", () => {
    // `command` is in the wire enum and is counted for completeness rather than
    // because it has been observed; `execute` and `shell` are what KAS emits.
    expect([...COMMAND_KINDS].sort()).toEqual(["command", "execute", "shell"]);
  });
});

describe("turnAnchorID", () => {
  it("builds the anchor target from the SESSION-absolute ordinal", () => {
    expect(turnAnchorID(14)).toBe("turn-14");
  });
});

// --- The collapsed turn's face ---------------------------------------------

describe("turnFaceProse", () => {
  it("takes the last non-empty top-level text entry", () => {
    const t = oneTurn([
      textEntry("t1", 1, "working on it"),
      callEntry("t1", 2, "c1"),
      textEntry("t1", 3, "the final answer"),
    ]);
    expect(turnFaceProse(t)).toBe("the final answer");
  });

  it("skips a delegate's prose — a delegate's report is not the turn's answer", () => {
    const t = oneTurn([
      textEntry("t1", 1, "the parent's answer"),
      textEntry("t1", 2, "delegate report", "sub-A"),
    ]);
    expect(turnFaceProse(t)).toBe("the parent's answer");
  });

  it("skips a whitespace-only entry", () => {
    const t = oneTurn([textEntry("t1", 1, "the answer"), textEntry("t1", 2, "   ")]);
    expect(turnFaceProse(t)).toBe("the answer");
  });

  it("answers empty for a turn with no prose at all", () => {
    expect(turnFaceProse(oneTurn([callEntry("t1", 1, "c1")]))).toBe("");
  });
});

// --- Whether the fold would hide anything ---------------------------------

describe("turnFoldHides", () => {
  it("hides nothing on a prose-only turn — its face IS its body", () => {
    expect(turnFoldHides(oneTurn([textEntry("t1", 1, "the answer")]))).toBe(false);
  });

  it("hides nothing on a bodyless turn", () => {
    expect(turnFoldHides(oneTurn([]))).toBe(false);
  });

  it.each([
    ["tool_call", { id: "c1", title: "t", kind: "read", status: "completed", ts: 1 }],
    ["thinking", { text: "hmm" }],
    ["plan", { entries: [] }],
    ["compaction", {}],
    ["compaction_failed", {}],
    ["safety_blocked", {}],
    ["model_switched", { from: "a", to: "b" }],
    ["mode_switched", { from: "spec", to: "vibe", source: "user" }],
  ] as const)("hides a %s entry", (kind, payload) => {
    const t = oneTurn([sealed("t1", 1, kind, payload), textEntry("t1", 2, "done")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides a delegate's output, and the LANE is what reports it", () => {
    // A delegate's nested tool result is no counted kind, so only the lane clause answers for it (a
    // delegate TEXT passes with the clause deleted, measured).
    const t = oneTurn([
      sealed("t1", 1, "tool_result", { status: "completed" }, { id: "c1:result", lane: "sub-A" }),
      textEntry("t1", 2, "the answer"),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides intermediate prose — the face shows only the final answer", () => {
    const t = oneTurn([textEntry("t1", 1, "working on it"), textEntry("t1", 2, "the answer")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("does not count an empty text entry as intermediate prose", () => {
    const t = oneTurn([textEntry("t1", 1, "  "), textEntry("t1", 2, "the answer")]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it.each(["turn_bind", "tool_result", "steer_ack", "turn_close"] as const)(
    "hides nothing behind a %s, which the face does not draw either",
    (kind) => {
      const t = oneTurn([textEntry("t1", 1, "the answer"), sealed("t1", 2, kind, {})]);
      expect(turnFoldHides(t)).toBe(false);
    },
  );
});

// --- What a turn that did not end cleanly SAYS ----------------------------

describe("turnFailureText", () => {
  function textOf(close: Partial<EntryTurnClose>): string {
    return turnFailureText(oneTurn([textEntry("t1", 1), closeEntry("t1", 2, close)]));
  }

  it("prefers the close's own persisted reason", () => {
    // The specific source, written by the closer that finalized the turn.
    expect(textOf({ outcome: "failed", failure_reason: "The upstream connection dropped." })).toBe(
      "The upstream connection dropped.",
    );
  });

  it("ignores a whitespace-only reason and speaks the default instead", () => {
    expect(textOf({ outcome: "failed", failure_reason: "   " })).toBe(
      "The agent reported an error and the turn stopped.",
    );
  });

  it("speaks for a failed turn that recorded no reason at all", () => {
    // Otherwise a red footer mark sits over an empty body with no account anywhere.
    expect(textOf({ outcome: "failed" })).toBe("The agent reported an error and the turn stopped.");
  });

  it("distinguishes a refusal from a dropped connection", () => {
    // Both are `broken`, and the default is keyed per OUTCOME precisely so the
    // severity's own coarseness does not reach the reader.
    const refused = textOf({ outcome: "refused" });
    const interrupted = textOf({ outcome: "interrupted" });
    expect(refused).toBe("The model declined to continue.");
    expect(refused).not.toBe(interrupted);
  });

  it("still speaks for an UNKNOWN turn, which is what the footer glyph cannot", () => {
    // `unknown` is `stopped` rather than broken, and the NEGATIVE CONTROL for the two
    // cancelled cases below: the gate that silences a cancel is keyed on the OUTCOME
    // precisely because keying it on the severity would silence this one too.
    expect(textOf({ outcome: "unknown" })).toBe(
      "The turn ended for a reason marotte could not read.",
    );
  });

  it("says nothing for a CANCELLED turn — the footer's own word carries it", () => {
    expect(textOf({ outcome: "cancelled" })).toBe("");
  });

  it("silences a sentence already persisted on a cancelled turn", () => {
    // The gate sits AHEAD of the source rather than being a change to the default
    // alone, because a cancelled turn's own record can carry a sentence that would
    // otherwise be handed straight back.
    expect(textOf({ outcome: "cancelled", failure_reason: "The turn was cancelled." })).toBe("");
  });

  it("answers empty for a clean turn — the card shows the answer instead", () => {
    expect(textOf({ outcome: "completed" })).toBe("");
  });

  it("answers empty for a turn still running", () => {
    const t = oneTurn([textEntry("t1", 1)]);
    expect(t.outcome).toBe("running");
    expect(turnFailureText(t)).toBe("");
  });
});
