// model.ts's own guard, and it is the `tabs-mock.test.ts` obligation applied to
// the shape this module owns: not an export list, but the STORE INVARIANT the
// factories derive. A fixture that violates `entries[i].seq === i` asserts nothing
// about production, so the invariant is the factory's to keep and this is what says
// it still does.
import { describe, expect, it } from "vitest";

import { turnSpan } from "../block-window.js";
import {
  makeEntry,
  makeOpenEntry,
  makeRunState,
  makeServerEvent,
  makeSession,
  makeToolCall,
  makeTurn,
} from "./model.js";

describe("makeTurn derives the store's coordinate space", () => {
  it("stamps every body entry with the turn's own id and seq === index + 1", () => {
    const t = makeTurn({
      id: "t-7",
      body: [{ kind: "text" }, { kind: "tool_call" }, { kind: "turn_close" }],
    });

    expect(t.body.map((e) => e.turn)).toEqual(["t-7", "t-7", "t-7"]);
    expect(t.body.map((e) => e.seq)).toEqual([1, 2, 3]);
  });

  // turnSpan is block-window.ts's own reader of that space, so the oracle is
  // production's function rather than a second copy of its arithmetic.
  it.each([0, 1, 5])("spans %i body entries plus the turn_open at 0", (k) => {
    const body = Array.from({ length: k }, () => ({ kind: "text" as const }));

    expect(turnSpan(makeTurn({ body }))).toBe(k + 1);
  });

  it("refuses a body entry that states its own seq", () => {
    expect(() => makeTurn({ body: [{ kind: "text" }, { seq: 3 }] })).toThrow(/body\[1\]/);
  });

  it("refuses a body entry that states its own turn", () => {
    expect(() => makeTurn({ body: [{ turn: "t-other" }] })).toThrow(/body\[0\]/);
  });
});

describe("makeSession keys its turns the way the store does", () => {
  it("orders turn_order by turn_open.n rather than by insertion or by id", () => {
    const s = makeSession({
      turns: [makeTurn({ id: "t-a", n: 2 }), makeTurn({ id: "t-b", n: 1 })],
    });

    expect(s.turn_order).toEqual(["t-b", "t-a"]);
    expect([...s.turns.keys()].sort()).toEqual(["t-a", "t-b"]);
  });

  it("puts each turn's turn_open at seq 0 of its own entries", () => {
    const s = makeSession({ turns: [makeTurn({ id: "t-1", n: 1, body: [{ kind: "text" }] })] });
    const held = s.turns.get("t-1");

    expect(held?.entries[0]?.kind).toBe("turn_open");
    expect(held?.entries.map((e) => e.seq)).toEqual([0, 1]);
  });

  it("records where a turn_close landed, which is what turnLive reads", () => {
    const closed = makeSession({ turns: [makeTurn({ body: [{ kind: "turn_close" }] })] });
    const open = makeSession({ turns: [makeTurn({ body: [{ kind: "text" }] })] });

    expect(closed.turns.get("t-1")?.closeAt).toBe(1);
    expect(open.turns.get("t-1")?.closeAt).toBeUndefined();
  });
});

describe("each factory applies its override over its own defaults", () => {
  it("makeEntry", () => {
    expect(makeEntry({ id: "e-9" })).toMatchObject({
      id: "e-9",
      turn: "t-1",
      seq: 0,
      kind: "text",
    });
  });

  it("makeOpenEntry", () => {
    expect(makeOpenEntry({ text: "half" })).toMatchObject({ text: "half", kind: "text", n: 1 });
  });

  it("makeToolCall", () => {
    expect(makeToolCall({ status: "failed" })).toMatchObject({
      status: "failed",
      kind: "execute",
      id: "tc-1",
    });
  });

  it("makeRunState", () => {
    expect(makeRunState({ status: "completed" })).toMatchObject({
      status: "completed",
      workflowId: "wf-1",
    });
  });

  it("makeServerEvent", () => {
    expect(makeServerEvent({ chat_id: "c-2" })).toMatchObject({
      type: "chat_updated",
      chat_id: "c-2",
    });
  });
});
