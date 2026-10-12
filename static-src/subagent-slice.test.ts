// The delegate projection as pure values: which lane an entry lands in, which turn a lane lives in,
// and whose liveness a delegate reports.

import { describe, it, expect } from "vitest";
import {
  blockShape,
  findSubagentInvocation,
  readLane,
  shapeExtends,
  sliceSubagent,
  turnHoldingLane,
} from "./subagent-slice.js";
import { toolResultID } from "./entry-ids.js";
import { makeToolCall } from "./__test-helpers__/model.js";
import type { TurnSource } from "./turns.js";
import type { OpenEntry, TurnState } from "./types.js";
import type { Entry, EntryToolCall, EntryToolResult } from "./wire/types.gen.js";

/** The `turn_open` of `turnID` at ordinal `n`, reader-prompted so the turn draws. */
function turnOpen(turnID: string, n: number): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n, prompt: { id: `m-${turnID}`, text: "delegate this" } },
  };
}

/** A sealed entry at `seq`. `lane` is absent unless named: the chat's own lane. */
function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  const base: Entry = {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    payload,
  };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

function text(turnID: string, seq: number, body: string, lane?: string): Entry {
  return sealed(turnID, seq, "text", { text: body }, lane === undefined ? {} : { lane });
}

function invocation(id: string, subtask: string, status: EntryToolCall["status"] = "completed") {
  return makeToolCall({
    id,
    title: "Sub-agent: context-gatherer",
    kind: "other",
    status,
    agent_subtask_id: subtask,
    input: { name: "context-gatherer" },
  });
}

function driver(id: string) {
  return makeToolCall({
    id,
    title: "Orchestrate Sub-agent",
    kind: "other",
    status: "in_progress",
  });
}

function work(id: string) {
  return makeToolCall({
    id,
    title: "Read File",
    kind: "read",
  });
}

function toolCall(turnID: string, seq: number, call: EntryToolCall, lane?: string): Entry {
  return sealed(turnID, seq, "tool_call", call, {
    id: call.id,
    ...(lane === undefined ? {} : { lane }),
  });
}

function toolResult(
  turnID: string,
  seq: number,
  callID: string,
  status: EntryToolResult["status"],
  lane?: string,
): Entry {
  return sealed(
    turnID,
    seq,
    "tool_result",
    { status },
    {
      id: toolResultID(callID),
      ...(lane === undefined ? {} : { lane }),
    },
  );
}

function source(
  turns: readonly (readonly [string, readonly Entry[], ReadonlyMap<string, OpenEntry>?])[],
): TurnSource {
  const map = new Map<string, TurnState>();
  for (const [id, entries, open] of turns) {
    map.set(id, { entries: [...entries], openEntries: new Map(open ?? []) });
  }
  return { turns: map, turn_order: turns.map(([id]) => id) };
}

function openEntry(turnID: string, lane: string, body: string): OpenEntry {
  return { turn: turnID, id: `${turnID}-open-${lane}`, lane, kind: "text", text: body, n: 1 };
}

const STAGE_A = "invoke_subagent_orc_1_stage_plan";
const STAGE_B = "invoke_subagent_orc_1_stage_review";

/** What an older engine's two-stage pipeline left in one turn: the driver and both stage-shaped
 *  invocations in the chat's lane, each stage's work in its own. Stage A settled through its
 *  result while its create frame still reads `in_progress`; stage B is still running. */
function pipeline(): TurnSource {
  return source([
    [
      "t1",
      [
        turnOpen("t1", 1),
        text("t1", 1, "parent prose"),
        toolCall("t1", 2, driver("orc_1")),
        toolCall("t1", 3, invocation(STAGE_A, "sub_a", "in_progress")),
        text("t1", 4, "stage A wrote this", "sub_a"),
        toolCall("t1", 5, work("read_1"), "sub_a"),
        toolResult("t1", 6, "read_1", "completed", "sub_a"),
        toolCall("t1", 7, invocation(STAGE_B, "sub_b", "in_progress")),
        text("t1", 8, "stage B wrote this", "sub_b"),
        toolResult("t1", 9, STAGE_A, "completed"),
      ],
    ],
  ]);
}

function prose(entries: readonly Entry[] | undefined): string[] {
  return (entries ?? [])
    .filter((e) => e.kind === "text")
    .map((e) => (e.payload as { text: string }).text);
}

describe("a stage-shaped delegate is an ordinary delegate", () => {
  // Replay of an older engine's pipeline: each former stage is its own delegate, and the id
  // shape pulls in neither a sibling nor the driver.
  it("projects the asked stage alone", () => {
    const slice = sliceSubagent(pipeline(), "sub_b", false);
    expect(slice.invocation?.id).toBe(STAGE_B);
    expect(prose(slice.entries)).toEqual(["stage B wrote this"]);
    expect(slice.entries.some((e) => (e.lane ?? "") !== "sub_b")).toBe(false);
  });
});

describe("a delegate's entries are its lane's, uncopied", () => {
  // The chat's own prose is lane "" and another delegate's is that delegate's; either leaking
  // renders one agent's work inside another's page.
  it("keeps the chat's entries and another delegate's out", () => {
    const a = sliceSubagent(pipeline(), "sub_a", false);
    expect(a.entries.map((e) => e.seq)).toEqual([4, 5, 6]);
    expect(prose(a.entries)).toEqual(["stage A wrote this"]);
    expect(sliceSubagent(pipeline(), "sub_b", false).entries.map((e) => e.seq)).toEqual([8]);
  });

  // Nothing is copied and no lane is cleared: a view renders the REAL turn with the
  // delegate's uuid as its root, so the slice hands back the store's own objects with
  // their lane intact.
  it("hands back the store's own entries with their lane intact", () => {
    const src = pipeline();
    const stored = src.turns.get("t1")?.entries[4];
    const projected = sliceSubagent(src, "sub_a", false).entries[0];
    expect(projected).toBe(stored);
    expect(projected?.lane).toBe("sub_a");
  });

  it("files a tool call and its result under the delegate whose lane holds them", () => {
    expect(sliceSubagent(pipeline(), "sub_a", false).entries.map((e) => e.id)).toEqual([
      "t1-e4",
      "read_1",
      toolResultID("read_1"),
    ]);
    expect(sliceSubagent(pipeline(), "sub_b", false).entries.some((e) => e.id === "read_1")).toBe(
      false,
    );
  });
});

describe("the invocation is the issuer's tool_call naming the lane", () => {
  it("finds the invocation in the issuer's lane and keeps it out of the entries", () => {
    for (const [id, call] of [
      ["sub_a", STAGE_A],
      ["sub_b", STAGE_B],
    ] as const) {
      const slice = sliceSubagent(pipeline(), id, false);
      expect(slice.invocation?.id).toBe(call);
      expect(slice.entries.some((e) => e.id === call)).toBe(false);
    }
  });

  // The create frame's status is a starting state; the settled verdict rides the
  // `tool_result`. Without the join a finished delegate's dot spins forever.
  it("joins the invocation with its tool_result, so the settled status wins", () => {
    expect(findSubagentInvocation(pipeline(), "sub_a")?.status).toBe("completed");
    expect(findSubagentInvocation(pipeline(), "sub_b")?.status).toBe("in_progress");
  });

  // A delegate's own work carries the same subtask id in its lane; a call that is not
  // an invocation must not be mistaken for one because it names the uuid.
  it("requires an invocation title, not just a matching subtask id", () => {
    const stray = { ...work("read_9"), agent_subtask_id: "sub_x" } as EntryToolCall;
    const src = source([["t1", [turnOpen("t1", 1), toolCall("t1", 1, stray)]]]);
    expect(findSubagentInvocation(src, "sub_x")).toBeUndefined();
  });

  it("walks the resident turns newest first", () => {
    const src = source([
      ["t1", [turnOpen("t1", 1), toolCall("t1", 1, invocation("old", "sub_dup", "completed"))]],
      ["t2", [turnOpen("t2", 2), toolCall("t2", 1, invocation("new", "sub_dup", "in_progress"))]],
    ]);
    expect(findSubagentInvocation(src, "sub_dup")?.id).toBe("new");
  });
});

describe("the turn a lane lives in", () => {
  // The tail subscribes on `laneSig(turn, lane)`, so the slice has to name the turn the
  // projection actually rendered: the one the invocation was issued in.
  it("projects the turn holding the lane", () => {
    const slice = sliceSubagent(pipeline(), "sub_a", false);
    expect(slice.turn?.id).toBe("t1");
    expect(slice.turn?.n).toBe(1);
  });

  // A window can hold a delegate's output with its dispatching call paged out.
  it("falls back to the turn whose lane holds entries when the invocation is not resident", () => {
    const src = source([["t7", [turnOpen("t7", 7), text("t7", 1, "orphan work", "sub_gone")]]]);
    const slice = sliceSubagent(src, "sub_gone", false);
    expect(slice?.invocation).toBeUndefined();
    expect(slice?.turn?.id).toBe("t7");
    expect(prose(slice?.entries)).toEqual(["orphan work"]);
  });

  // Text arriving now has no `seq` and no position: it is reported as `open`, never
  // in `entries`. A delegate's first streamed words are all a page has to mount a body
  // from, so a slice that reported them as nothing renders an empty note over live text.
  it("reports an open entry as open and never as an entry", () => {
    const open = new Map([["sub_live", openEntry("t3", "sub_live", "typing…")]]);
    const src = source([
      [
        "t3",
        [turnOpen("t3", 3), toolCall("t3", 1, invocation("inv_live", "sub_live", "in_progress"))],
        open,
      ],
    ]);
    const slice = sliceSubagent(src, "sub_live", false);
    expect(slice?.open).toBe(true);
    expect(slice?.entries).toEqual([]);
    expect(slice?.live).toBe(true);
  });

  // An open entry carries no `seq`, so a lane whose sealed entries are all paged out is
  // located by it alone — which is the state a delegate is in for its first frames.
  it("locates a lane by its open entry alone", () => {
    const open = new Map([["sub_live", openEntry("t3", "sub_live", "typing…")]]);
    const src = source([
      ["t2", [turnOpen("t2", 2), text("t2", 1, "parent prose")]],
      ["t3", [turnOpen("t3", 3)], open],
    ]);
    expect(turnHoldingLane(src, "sub_live")).toBe("t3");
    const lane = readLane(src, "sub_live");
    expect(lane.turnID).toBe("t3");
    expect(lane.open?.text).toBe("typing…");
    expect(lane.entries).toEqual([]);
    const slice = sliceSubagent(src, "sub_live", false);
    expect(slice?.turn?.id).toBe("t3");
    expect(slice?.open).toBe(true);
  });

  it("reads one lane's sealed entries and its open tail from the newest turn holding it", () => {
    const open = new Map([["sub_a", openEntry("t1", "sub_a", "more")]]);
    const src = source([
      ["t0", [turnOpen("t0", 1), text("t0", 1, "earlier", "sub_a")]],
      ["t1", [turnOpen("t1", 2), text("t1", 1, "later", "sub_a")], open],
    ]);
    const lane = readLane(src, "sub_a");
    expect(lane.turnID).toBe("t1");
    expect(prose(lane.entries)).toEqual(["later"]);
    expect(lane.open?.text).toBe("more");
    expect(turnHoldingLane(src, "")).toBeUndefined();
    expect(readLane(src, "nobody")).toEqual({ turnID: undefined, entries: [], open: undefined });
  });
});

describe("live is the delegate's own", () => {
  // A delegate can finish while the conversation carries on. Reading the chat's flag would leave a
  // settled delegate under a streaming caret.
  it("reads the delegate's own settled status, not the chat's", () => {
    expect(sliceSubagent(pipeline(), "sub_a", true).live).toBe(false);
    expect(sliceSubagent(pipeline(), "sub_b", true).live).toBe(true);
  });

  it("falls back to the chat while the invocation is not resident", () => {
    const src = source([["t1", [turnOpen("t1", 1), text("t1", 1, "orphan work", "sub_gone")]]]);
    for (const chatLive of [true, false]) {
      expect(sliceSubagent(src, "sub_gone", chatLive).live).toBe(chatLive);
    }
  });
});

describe("a delegate with little resident", () => {
  it("projects its invocation and its lane", () => {
    const src = source([
      [
        "t1",
        [
          turnOpen("t1", 1),
          text("t1", 1, "parent"),
          toolCall("t1", 2, invocation("tooluse_plain", "sub_solo")),
          text("t1", 3, "solo work", "sub_solo"),
        ],
      ],
    ]);
    const slice = sliceSubagent(src, "sub_solo", false);
    expect(prose(slice.entries)).toEqual(["solo work"]);
    expect(slice.invocation?.id).toBe("tooluse_plain");
  });

  // "Not projected" and "projected and empty" are different answers and the page says different
  // things for each.
  it("still answers for a delegate with nothing resident", () => {
    const src = source([["t1", [turnOpen("t1", 1), text("t1", 1, "parent")]]]);
    expect(sliceSubagent(src, "sub_missing", false)).toEqual({
      invocation: undefined,
      turn: undefined,
      entries: [],
      open: false,
      live: false,
    });
  });
});

describe("blockShape decides update against rebuild", () => {
  // Growth at the tail keeps the mounted prefix; a range read that FILLED A HOLE
  // inserts a same-kind entry mid-lane, which a kind-only compare cannot see. The
  // `seq` in the signature is what tells the two apart.
  it("reads tail growth as an extension", () => {
    const before = blockShape([text("t1", 4, "a", "s"), toolCall("t1", 5, work("r"), "s")]);
    const after = blockShape([
      text("t1", 4, "a", "s"),
      toolCall("t1", 5, work("r"), "s"),
      text("t1", 9, "b", "s"),
    ]);
    expect(shapeExtends(before, after)).toBe(true);
    expect(shapeExtends(after, before)).toBe(false);
  });

  it("reads a filled hole as a rebuild even when the kinds match", () => {
    const before = blockShape([text("t1", 4, "a", "s"), text("t1", 9, "c", "s")]);
    const after = blockShape([
      text("t1", 4, "a", "s"),
      text("t1", 6, "b", "s"),
      text("t1", 9, "c", "s"),
    ]);
    expect(shapeExtends(before, after)).toBe(false);
  });
});
