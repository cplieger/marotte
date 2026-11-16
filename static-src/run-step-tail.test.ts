// The newest line a step printed, read for the run tab's folded Steps row.
import { describe, expect, it } from "vitest";
import {
  forgetStepTails,
  lastPrintedLine,
  noteStepTurn,
  seedStepTails,
  stepTail,
  tailAppended,
  tailDelta,
} from "./run-step-tail.js";
import type { Entry, OpenEntry, RunStepStart } from "./wire/types.gen.js";
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

  it("is a tool call's card title over its wire title, as the dock line's server twin reads it", () => {
    const call = { title: "Run Command", card_title: "go test ./...", id: "c1" };
    expect(lineOf(turn([entry(1, "tool_call", call)]))).toBe("go test ./...");
    expect(lineOf(turn([entry(1, "tool_call", { title: "Run Command", id: "c1" })]))).toBe(
      "Run Command",
    );
  });

  // KAS can send a shell call's command only on its update, which the result stores.
  it("is a result's card title where it differs from its call's", () => {
    const call = { ...entry(1, "tool_call", shellCall), id: "c1" };
    const settled = { ...entry(2, "tool_result", { card_title: "go vet ./..." }), id: "c1:result" };
    expect(lineOf(turn([call, settled]))).toBe("go vet ./...");
    const same = { ...entry(3, "tool_result", { card_title: "Run Command" }), id: "c1:result" };
    expect(lineOf(turn([call, entry(2, "text", { text: "Reading the hits" }), same]))).toBe(
      "Reading the hits",
    );
    expect(lineOf(turn([entry(2, "text", { text: "mine" }), settled]))).toBe("mine");
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

const shellCall = { title: "Run Command", card_title: "Run Command", id: "c1" };

describe("a tail cell moved by appended entries", () => {
  it("takes a result's card title where it differs from its call's", () => {
    const wf = "wf_cell_result";
    noteStepTurn(wf, "t1", "s");
    tailAppended(wf, { ...entry(1, "tool_call", shellCall), id: "c1" });
    expect(stepTail(wf, "s")).toBe("Run Command");
    tailAppended(wf, {
      ...entry(2, "tool_result", { card_title: "go vet ./..." }),
      id: "c1:result",
    });
    expect(stepTail(wf, "s")).toBe("go vet ./...");
    tailAppended(wf, { ...entry(3, "tool_call", { ...shellCall, id: "c2" }), id: "c2" });
    tailAppended(wf, entry(4, "text", { text: "Reading the hits" }));
    tailAppended(wf, {
      ...entry(5, "tool_result", { card_title: "Run Command" }),
      id: "c2:result",
    });
    expect(stepTail(wf, "s")).toBe("Reading the hits");
    forgetStepTails(wf);
  });
});

/** A run read's row for step `path`'s turn `t-<path>`, its root lane streaming `e-<path>` at `n`. */
function row(path: string, n: number, text: string): readonly [string, RunStepStart] {
  const turn = `t-${path}`;
  return [
    path,
    {
      started_at: "2026-11-10T10:00:00Z",
      ended: false,
      turn,
      open: { turn, id: `e-${path}`, lane: "", kind: "text", text, n },
    },
  ];
}

function deltaOf(wf: string, path: string, n: number, text: string): void {
  tailDelta(wf, `t-${path}`, `e-${path}`, "", n, text);
}

describe("a tail cell whose seeding read predates its frames", () => {
  it("keeps every gapped step's parked deltas while another step streams in order", () => {
    const wf = "wf_cell_wide";
    const gapped = Array.from({ length: 10 }, (_, i) => `s${String(i)}`);
    seedStepTails(wf, [...gapped.map((p) => row(p, 3, "a")), row("s10", 0, "")]);
    for (const p of gapped) {
      deltaOf(wf, p, 5, "c");
    }
    deltaOf(wf, "s10", 1, "x");
    seedStepTails(
      wf,
      gapped.map((p) => row(p, 4, "ab")),
    );
    expect(gapped.map((p) => stepTail(wf, p))).toEqual(gapped.map(() => "abc"));
    expect(stepTail(wf, "s10")).toBe("x");
    forgetStepTails(wf);
  });

  it("follows the deltas past a later row when more arrived than a cell parks", () => {
    const wf = "wf_cell_long";
    seedStepTails(wf, [row("s", 1, "w1")]);
    for (let n = 100; n <= 400; n++) {
      deltaOf(wf, "s", n, `\nw${String(n)}`);
    }
    seedStepTails(wf, [row("s", 150, "w150")]);
    deltaOf(wf, "s", 401, "\nw401");
    expect(stepTail(wf, "s")).toBe("w401");
    forgetStepTails(wf);
  });
});
