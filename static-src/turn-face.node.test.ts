// turn-face.ts: the collapsed turn's face and whether folding hides anything. Pure, hence `.node`.

import { describe, it, expect } from "vitest";
import { turnFaceProse, turnFoldHides } from "./turn-face.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

/** A turn whose body is `body`. */
function oneTurn(body: readonly Entry[]): Turn {
  return {
    id: "t1",
    n: 1,
    trigger: { id: "m-1", text: "do a thing" },
    body: [...body],
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown = {},
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

function textEntry(turnID: string, seq: number, text = "hi", lane?: string): Entry {
  return sealed(turnID, seq, "text", { text }, lane === undefined ? {} : { lane });
}

function callEntry(turnID: string, seq: number, callID: string): Entry {
  return sealed(
    turnID,
    seq,
    "tool_call",
    { id: callID, title: "Run Command", kind: "execute", status: "completed", ts: 1 },
    { id: callID },
  );
}

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
    ["model_fallback", { from: "a", to: "b", trigger: "refusal" }],
    ["mode_switched", { from: "spec", to: "vibe", source: "user" }],
  ] as const)("hides a %s entry", (kind, payload) => {
    const t = oneTurn([sealed("t1", 1, kind, payload), textEntry("t1", 2, "done")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides nothing behind a sub-agent's model_fallback, which the body does not draw", () => {
    const t = oneTurn([
      textEntry("t1", 1, "the answer"),
      sealed("t1", 2, "model_fallback", { from: "a", to: "b", trigger: "refusal", subagent: true }),
    ]);
    expect(turnFoldHides(t)).toBe(false);
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
