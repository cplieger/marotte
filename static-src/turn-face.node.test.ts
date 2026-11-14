// turn-face.ts: the collapsed turn's face (the turn's last message) and whether folding hides anything. Pure, hence
// `.node`.

import { describe, it, expect } from "vitest";
import { turnFace, turnFoldHides } from "./turn-face.js";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

/** The face's prose, the turn's last message. */
function lastMessage(t: Turn): string[] {
  return turnFace(t).flatMap((p) => (p.kind === "prose" ? [p.text] : []));
}

/** A turn whose body is `body`, seqs stamped by position. */
function oneTurn(body: readonly Entry[]): Turn {
  return {
    id: "t1",
    n: 1,
    trigger: { id: "m-1", text: "do a thing" },
    body: body.map((e, i) => ({ ...e, seq: i + 1 })),
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

function sealed(
  kind: Entry["kind"],
  payload: unknown = {},
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  const base: Entry = { id: opts.id ?? `x-${kind}`, turn: "t1", kind, seq: 0, ts: 1, payload };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

/** A text entry under say `say` (a KAS message id, or `<say>#k` for a later segment of it). */
function text(say: string, s: string, lane?: string): Entry {
  return sealed("text", { text: s }, lane === undefined ? { id: say } : { id: say, lane });
}

function call(id: string): Entry {
  return sealed(
    "tool_call",
    { id, title: "Run Command", kind: "execute", status: "completed", ts: 1 },
    { id },
  );
}

const STEER = sealed("steer", { text: "use tabs", origin: "user", state: "read" }, { id: "s-1" });

/** KAS's acknowledgement of `STEER`, which the body draws as its own prose bubble. */
function ack(s: string): Entry {
  return sealed("steer_ack", { steer_id: "s-1", text: s }, { id: "s-1:ack" });
}

// --- The last message -------------------------------------------------------

describe("the face's last-message prose", () => {
  it("takes the prose after the last tool call", () => {
    const t = oneTurn([
      text("a-say", "working on it"),
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      text("b-say", "the final answer"),
    ]);
    expect(lastMessage(t)).toEqual(["the final answer"]);
  });

  it("takes EVERY message back to back at the end, one string each", () => {
    // Turn #797: a reply, then the reply a workflow notification woke the agent for, nothing drawn between.
    const t = oneTurn([
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      text("a-say", "Open behind this one: plus 2 other calls."),
      sealed("steer_delivered", { steer_id: "w-1", read_ts: 1 }, { id: "w-1:delivered" }),
      text("b-say", "No change: still waiting."),
    ]);
    expect(lastMessage(t)).toEqual([
      "Open behind this one: plus 2 other calls.",
      "No change: still waiting.",
    ]);
  });

  it("joins one message's segments, which a seal split, into one string", () => {
    const t = oneTurn([
      call("c1"),
      text("a-say", "one **tw"),
      sealed("tool_result", {}, { id: "c1:result" }),
      text("a-say#2", "o** three"),
    ]);
    expect(lastMessage(t)).toEqual(["one **two** three"]);
  });

  it("stops at a steer note, which the body draws between the two", () => {
    const t = oneTurn([text("a-say", "before"), STEER, text("b-say", "after")]);
    expect(lastMessage(t)).toEqual(["after"]);
  });

  it("skips a delegate's prose — a delegate's report is not the turn's answer", () => {
    const t = oneTurn([text("a-say", "the parent's answer"), text("d-say", "report", "sub-A")]);
    expect(lastMessage(t)).toEqual(["the parent's answer"]);
  });

  it("ends at the last NON-EMPTY text, dropping a whitespace-only message", () => {
    const t = oneTurn([text("a-say", "the answer"), call("c1"), text("b-say", "   ")]);
    expect(lastMessage(t)).toEqual(["the answer"]);
  });

  it("answers empty for a turn with no prose at all", () => {
    expect(lastMessage(oneTurn([call("c1")]))).toEqual([]);
  });

  it("reaches back over a steer_ack inside the message, which is its own block", () => {
    // A real shape: KAS's ack of a steer lands mid-say, so the say's head precedes it and its tail follows it.
    const t = oneTurn([
      call("c1"),
      text("x-say", "LONG ANSWER part one.\n\n["),
      ack("Folded in as the discovery check."),
      text("x-say#2", "\n\nSTEERING s2: tail.]"),
      call("hook-1"),
    ]);
    expect(lastMessage(t)).toEqual([
      "LONG ANSWER part one.\n\n[",
      "Folded in as the discovery check.",
      "STEERING s2: tail.]",
    ]);
  });

  it("ends at a trailing steer_ack, the last words the agent wrote", () => {
    const t = oneTurn([call("c1"), text("a-say", "the answer"), ack("Noted.")]);
    expect(lastMessage(t)).toEqual(["the answer", "Noted."]);
  });

  it("reaches back over a steer note inside ONE message", () => {
    const t = oneTurn([
      call("c1"),
      text("y-say", "First half."),
      STEER,
      text("y-say#2", "Second half."),
    ]);
    expect(lastMessage(t)).toEqual(["First half.", "Second half."]);
  });

  it("reaches back over a tool call inside ONE message", () => {
    const t = oneTurn([
      text("a-say", "before"),
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      text("a-say#2", "after"),
    ]);
    expect(lastMessage(t)).toEqual(["before", "after"]);
  });

  it("reaches back over SEVERAL drawn entries inside one message, to its first segment", () => {
    const t = oneTurn([
      text("z-say", "earlier message"),
      call("c0"),
      text("a-say", "one"),
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      sealed("thinking", { text: "hmm" }, { id: "r-say" }),
      call("c2"),
      text("a-say#2", "two"),
      call("c3"),
      text("a-say#3", "three"),
    ]);
    expect(lastMessage(t)).toEqual(["one", "two", "three"]);
  });

  it("does not reach over a tool call to a text under a MINTED say, which names no message", () => {
    const t = oneTurn([text("t1:e1", "unknown"), call("c1"), text("a-say", "the answer")]);
    expect(lastMessage(t)).toEqual(["the answer"]);
  });

  it("reaches over a tool call and a delegate's prose to the same say", () => {
    // The nearest TOP-LEVEL text decides: a delegate's report between them is not the parent's message.
    const t = oneTurn([
      text("a-say", "head"),
      call("c1"),
      text("d-say", "report", "sub-A"),
      text("a-say#2", "tail"),
    ]);
    expect(lastMessage(t)).toEqual(["head", "tail"]);
  });

  const applied = sealed("model_fallback", {
    from: "model-a-5",
    to: "model-b-5",
    scope: "session",
    trigger: "refusal",
  });

  it("carries the refused text and its continuation across an applied fallback", () => {
    const t = oneTurn([
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      text("a-say", "Here is the first half"),
      applied,
      text("b-say", " and the second half."),
    ]);
    expect(lastMessage(t)).toEqual(["Here is the first half", "and the second half."]);
  });

  it.each([
    ["no target", { from: "model-a-5", trigger: "availability", reason: "noServedTarget" }],
    ["neither a target nor a reason", { from: "model-a-5", trigger: "refusal" }],
    [
      "a target",
      { from: "model-a-5", to: "model-b-5", trigger: "refusal", reason: "chainExhausted" },
    ],
  ])("stops at a declined fallback naming %s, after which nothing continues", (_, payload) => {
    const t = oneTurn([text("a-say", "a"), sealed("model_fallback", payload), text("b-say", "b")]);
    expect(lastMessage(t)).toEqual(["b"]);
  });

  it("reaches over a sub-agent's model_fallback, which the body does not draw", () => {
    const t = oneTurn([
      text("a-say", "before"),
      sealed("model_fallback", {
        from: "model-a-5",
        trigger: "refusal",
        reason: "chainExhausted",
        subagent: true,
      }),
      text("b-say", "after"),
    ]);
    expect(lastMessage(t)).toEqual(["before", "after"]);
  });

  it("stops at the tool call when the refused attempt wrote no prose", () => {
    const t = oneTurn([
      text("a-say", "a"),
      call("c1"),
      sealed("tool_result", {}, { id: "c1:result" }),
      applied,
      text("b-say", "b"),
    ]);
    expect(lastMessage(t)).toEqual(["b"]);
  });
});

// --- The rows a face keeps -------------------------------------------------

/** The face's parts as `row:<kind>` or the prose, in order. */
function faceOf(t: Turn): string[] {
  return turnFace(t).map((p) => (p.kind === "row" ? `row:${p.entry.kind}` : p.text));
}

const REPIN = sealed("model_switched", { from: "a", to: "b", reason: "unavailable" }, { id: "sw" });
const FALLBACK = sealed(
  "model_fallback",
  { from: "a", to: "b", scope: "session", trigger: "refusal" },
  { id: "fb" },
);

describe("turnFace", () => {
  it("keeps a fallback's one line before the answer it was retried for, and drops the re-pin it explains", () => {
    const t = oneTurn([
      text("a-say", "working"),
      STEER,
      REPIN,
      FALLBACK,
      call("c1"),
      text("b-say", "the answer"),
    ]);
    expect(faceOf(t)).toEqual(["row:model_fallback", "the answer"]);
  });

  it("keeps the line between the refused text and its continuation, where it happened", () => {
    const t = oneTurn([text("a-say", "half an"), REPIN, FALLBACK, text("a-say#2", "answer")]);
    expect(faceOf(t)).toEqual(["half an", "row:model_fallback", "answer"]);
  });

  it("keeps a turn-scope fallback's line, a fallback that moved the turn's model", () => {
    const turnScope = sealed("model_fallback", {
      from: "a",
      to: "b",
      scope: "turn",
      trigger: "availability",
    });
    expect(faceOf(oneTurn([turnScope, text("a-say", "done")]))).toEqual([
      "row:model_fallback",
      "done",
    ]);
  });

  it.each([
    ["model_switched", { from: "a", to: "b", effort: "high" }],
    ["mode_switched", { from: "spec", to: "vibe", source: "agent" }],
    ["context_reset", { cause: "load_failed" }],
  ] as const)("leaves out a %s line, which the body keeps", (kind, payload) => {
    expect(faceOf(oneTurn([sealed(kind, payload), text("a-say", "done")]))).toEqual(["done"]);
  });

  it.each([
    [
      "a sub-agent's fallback",
      { from: "a", to: "b", scope: "session", trigger: "refusal", subagent: true },
    ],
    ["a decline", { from: "a", trigger: "availability", reason: "chainExhausted" }],
    ["a same-model retry", { from: "auto", to: "auto", scope: "turn", trigger: "refusal" }],
  ] as const)("leaves out %s, which moved no model", (_why, payload) => {
    expect(faceOf(oneTurn([sealed("model_fallback", payload), text("a-say", "done")]))).toEqual([
      "done",
    ]);
  });

  it("leaves out a delegate's lines", () => {
    const t = oneTurn([
      sealed("model_switched", { from: "a", to: "b" }, { lane: "sub-A" }),
      text("a-say", "done"),
    ]);
    expect(faceOf(t)).toEqual(["done"]);
  });

  it("keeps a line on a turn with no prose", () => {
    expect(faceOf(oneTurn([call("c1"), FALLBACK]))).toEqual(["row:model_fallback"]);
  });
});

// --- Whether the fold would hide anything ---------------------------------

describe("turnFoldHides", () => {
  it("hides nothing on a prose-only turn — its face IS its body", () => {
    expect(turnFoldHides(oneTurn([text("a-say", "the answer")]))).toBe(false);
  });

  it("hides nothing on a turn of two messages back to back, since the face shows both", () => {
    const t = oneTurn([text("a-say", "first reply"), text("b-say", "second reply")]);
    expect(turnFoldHides(t)).toBe(false);
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
    ["model_routed", { message: "" }],
    ["mode_switched", { from: "spec", to: "vibe", source: "user" }],
    ["context_reset", { cause: "session_missing" }],
    ["model_fallback", { from: "a", trigger: "availability", reason: "chainExhausted" }],
    ["profile_switched", { from: "trusted", to: "read-only" }],
  ] as const)("hides a %s entry", (kind, payload) => {
    const t = oneTurn([sealed(kind, payload), text("a-say", "done")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides nothing behind a model fallback and the re-pin it explains, which the face shows as one line", () => {
    expect(turnFoldHides(oneTurn([REPIN, FALLBACK, text("a-say", "done")]))).toBe(false);
  });

  it("hides nothing behind a sub-agent's model_fallback, which the body does not draw", () => {
    const t = oneTurn([
      text("a-say", "the answer"),
      sealed("model_fallback", { from: "a", to: "b", trigger: "refusal", subagent: true }),
    ]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it("hides a delegate's output, and the LANE is what reports it", () => {
    // A delegate's nested tool result is no counted kind, so only the lane clause answers for it.
    const t = oneTurn([
      sealed("tool_result", { status: "completed" }, { id: "c1:result", lane: "sub-A" }),
      text("a-say", "the answer"),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides prose before the last message — the face shows only the last message", () => {
    const t = oneTurn([text("a-say", "working on it"), STEER, text("b-say", "the answer")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("does not count an empty text entry as earlier prose", () => {
    const t = oneTurn([text("a-say", "  "), STEER, text("b-say", "the answer")]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it.each(["turn_bind", "tool_result", "turn_close"] as const)(
    "hides nothing behind a %s, which the face does not draw either",
    (kind) => {
      const t = oneTurn([text("a-say", "the answer"), sealed(kind, {})]);
      expect(turnFoldHides(t)).toBe(false);
    },
  );

  it("hides nothing behind a steer_ack, which the face shows as its own block", () => {
    const t = oneTurn([text("a-say", "the answer"), ack("Noted."), text("a-say#2", "more")]);
    expect(lastMessage(t)).toEqual(["the answer", "Noted.", "more"]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it("hides a steer_ack before the last message, which the face leaves out", () => {
    const t = oneTurn([ack("Noted."), STEER, text("b-say", "the answer")]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("hides the steer note inside ONE message, which the face reaches over", () => {
    const t = oneTurn([text("y-say", "First half."), STEER, text("y-say#2", "Second half.")]);
    expect(lastMessage(t)).toEqual(["First half.", "Second half."]);
    expect(turnFoldHides(t)).toBe(true);
  });
});
