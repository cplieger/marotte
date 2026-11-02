// The newest line a step printed, read for the run tab's folded Steps row.
import { describe, expect, it } from "vitest";
import { lastPrintedLine } from "./run-step-tail.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";
import type { TurnState } from "./types.js";

function entry(seq: number, kind: Entry["kind"], payload: unknown, lane = ""): Entry {
  return { id: `e${String(seq)}`, turn: "t1", kind, payload, seq, ts: seq, lane } as Entry;
}

function turn(entries: Entry[], open?: OpenEntry): TurnState {
  return {
    entries: [entry(0, "turn_open", {}), ...entries],
    openEntries: new Map(open === undefined ? [] : [["", open]]),
  };
}

/** The line for a step with one turn. */
function lineOf(t: TurnState): string {
  return lastPrintedLine([t]);
}

describe("lastPrintedLine", () => {
  it("is the streaming text's last non-blank line first", () => {
    const t = turn([entry(1, "text", { text: "older" })], {
      turn: "t1",
      id: "o1",
      kind: "text",
      text: "Reading the brief\nChecking the tree\n\n",
      n: 3,
    });
    expect(lastPrintedLine([t])).toBe("Checking the tree");
  });

  it("is the newest sealed text, thinking or tool call otherwise", () => {
    expect(lineOf(turn([entry(1, "text", { text: "a\nb" })]))).toBe("b");
    expect(lineOf(turn([entry(1, "thinking", { text: "weighing it" })]))).toBe("weighing it");
    expect(
      lineOf(
        turn([
          entry(1, "text", { text: "first" }),
          entry(2, "tool_call", { title: "Run the tests", id: "c1" }),
          entry(3, "tool_result", { id: "c1" }),
        ]),
      ),
    ).toBe("Run the tests");
  });

  it("skips a delegate's lane and an entry with nothing to say", () => {
    const t = turn([
      entry(1, "text", { text: "mine" }),
      entry(2, "text", { text: "" }),
      entry(3, "text", { text: "a delegate's" }, "uuid-1"),
      entry(4, "tool_call", { title: 7 }),
    ]);
    expect(lastPrintedLine([t])).toBe("mine");
  });

  it("is empty for a step that printed nothing", () => {
    expect(lastPrintedLine([turn([])])).toBe("");
  });

  it("caps a long line by code points, never splitting a pair", () => {
    expect(lineOf(turn([entry(1, "text", { text: "x".repeat(500) })]))).toHaveLength(200);
    const capped = lineOf(turn([entry(1, "text", { text: `${"x".repeat(199)}😀😀` })]));
    expect(capped).toBe(`${"x".repeat(199)}😀`);
  });

  // A retry or continuation opens a new turn that prints nothing for a while.
  it("keeps the previous turn's line while the newest turn is silent", () => {
    const printed = turn([entry(1, "text", { text: "Running the suite" })]);
    const silent = turn([]);
    expect(lastPrintedLine([printed, silent])).toBe("Running the suite");
    const spoke = turn([entry(1, "text", { text: "Retrying" })]);
    expect(lastPrintedLine([printed, spoke])).toBe("Retrying");
    expect(lastPrintedLine([])).toBe("");
  });

  it("skips markdown structure and drops block markers", () => {
    const cases: [string, string][] = [
      ["Here is the fix:\n```ts\nconst a = 1;\n```", "const a = 1;"],
      ["| a | b |\n|---|:--:|", "| a | b |"],
      ["Summary\n\n---", "Summary"],
      ["Done\n* * *", "Done"],
      ["## Findings", "Findings"],
      ["Steps:\n- read the brief", "read the brief"],
      ["> quoted reason", "quoted reason"],
      ["1. first item", "first item"],
      ["Plan\n-", "Plan"],
      ["inline ```code``` stays", "inline ```code``` stays"],
    ];
    for (const [text, line] of cases) {
      expect(lineOf(turn([entry(1, "text", { text })])), text).toBe(line);
    }
  });
});
