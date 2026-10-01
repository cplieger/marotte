// ---------------------------------------------------------------------------
// The subagent adapter: one delegate's slice of a chat, folded into the exec view's
// model.
//
// Pure, so these are plain value assertions with no DOM. What they pin is the half a
// reader cannot check by looking: which of the two SHAPES a delegate produces (a lone
// leaf against a pipeline's driver-and-stages), and the two facts the page's own
// layout rule then keys on — `nodes.length` and whether any node has children — since
// getting those wrong is what puts a tree pane of one row on screen or hides a
// pipeline's structure entirely.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { subagentToExec, subagentPath } from "./subagent-exec-source.js";
import { sliceSubagentGroup } from "./subagent-slice.js";
import type { TurnSource } from "./turns.js";
import type { TurnState } from "./types.js";
import type { Entry, EntryToolCall } from "./wire/types.gen.js";
import { makeToolCall } from "./__test-helpers__/model.js";

/** An invocation tool call. `id` carries the pipeline join when stage-shaped. */
function invocation(
  id: string,
  subtask: string,
  extra: Partial<EntryToolCall> = {},
): EntryToolCall {
  return makeToolCall({
    id,
    title: "Sub-agent: context-gatherer",
    kind: "other",
    agent_subtask_id: subtask,
    input: { name: "context-gatherer" },
    ...extra,
  });
}

function driver(id: string, extra: Partial<EntryToolCall> = {}): EntryToolCall {
  return makeToolCall({
    id,
    title: "Orchestrate Sub-agent",
    status: "in_progress",
    kind: "other",
    ...extra,
  });
}

/** ONE TURN's entries: its `turn_open`, then the calls as `tool_call` entries in the
 *  ISSUER's lane (which is where an invocation lives, design 3.4), then one `text` entry
 *  per `[lane, body]` pair so each delegate's lane has something to project.
 *
 *  Every case here is one turn, because a delegate never spans two: a mid-turn model
 *  switch appends a `model_switched` entry rather than closing the turn (design 4.2). */
function msg(calls: EntryToolCall[], texts: [string, string][] = [], turnID = "t1"): Entry[] {
  const entries: Entry[] = [
    {
      id: `${turnID}-open`,
      turn: turnID,
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source: "prompt", n: 1, prompt: { id: "m1", text: "delegate this" } },
    },
  ];
  for (const c of calls) {
    entries.push({
      id: c.id,
      turn: turnID,
      kind: "tool_call",
      seq: entries.length,
      ts: entries.length + 1,
      payload: c,
    });
  }
  for (const [lane, body] of texts) {
    entries.push({
      id: `${turnID}-e${String(entries.length)}`,
      turn: turnID,
      kind: "text",
      seq: entries.length,
      ts: entries.length + 1,
      lane,
      payload: { text: body },
    });
  }
  return entries;
}

/** The `TurnSource` the projection reads: the session satisfies it, so a fixture is one
 *  turn map plus its order. */
function source(turns: readonly Entry[][]): TurnSource {
  const map = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turns) {
    const turnID = entries[0]?.turn ?? "t1";
    map.set(turnID, { entries: [...entries], openEntries: new Map() });
    order.push(turnID);
  }
  return { turns: map, turn_order: order };
}

function exec(turns: readonly Entry[][], subtask: string, live = false) {
  return subagentToExec(subtask, sliceSubagentGroup(source(turns), subtask, live));
}

describe("the single-delegate shape", () => {
  // The page's layout rule is `nodes.length > 1 || any node has children`, so these two
  // numbers ARE whether a tree pane and a timeline appear. One leaf means the whole
  // width goes to the transcript, which is the point of the shape.
  it("produces exactly one childless leaf", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")], [["sub_1", "done"]])], "sub_1");
    expect(run.nodes).toHaveLength(1);
    expect(run.nodes[0]?.children).toHaveLength(0);
    expect(run.nodes[0]?.path).toBe(subagentPath("sub_1"));
    expect(run.nodes[0]?.transcript).toBe(true);
  });

  it("names the delegate from its invocation and focuses it", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")])], "sub_1");
    expect(run.label).toBe("context-gatherer");
    expect(run.focus).toBe(subagentPath("sub_1"));
  });

  // NO `output`, and it is the one field this adapter leaves empty on purpose. A
  // workflow step's `capturedOutput` is durable beside a transcript that is not, so
  // that pane's Output region is the only place its result exists. A delegate's blocks
  // ARE its transcript and its last text block IS its report, so filling this renders
  // the report twice on one screen — measured in the sidecar before it was removed.
  it("sets no output, because the transcript below already is it", () => {
    const run = exec(
      [
        msg(
          [invocation("tooluse_1", "sub_1")],
          [
            ["sub_1", "first"],
            ["sub_1", "the report"],
          ],
        ),
      ],
      "sub_1",
    );
    expect(run.nodes[0]?.output).toBeUndefined();
  });

  it("carries the delegate's handles as facts", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1")])], "sub_1");
    const facts = run.nodes[0]?.facts ?? [];
    expect(facts.find((f) => f.label === "Agent")?.value).toBe("context-gatherer");
    expect(facts.find((f) => f.label === "Subtask")?.value).toBe("sub_1");
    expect(facts.find((f) => f.label === "Call")?.value).toBe("tooluse_1");
  });

  // The timeline's only input. A tool call carries `ts` in millis plus a duration, so
  // the window is derivable — without it a pipeline's timeline draws nothing, and
  // overlap is the one fact a pipeline has that a column cannot show.
  it("derives the node's window from the call's ts and duration", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { ts: 1_700_000_000_000, duration_ms: 5000 })])],
      "sub_1",
    );
    expect(run.nodes[0]?.start).toBe(new Date(1_700_000_000_000).toISOString());
    expect(run.nodes[0]?.end).toBe(new Date(1_700_000_005_000).toISOString());
  });

  // A running node has a start and no end, which is what `ExecNode` means by "still
  // going" and what makes its bar draw to the live edge rather than to a stale point.
  it("leaves the end open while the delegate is running", () => {
    const run = exec(
      [
        msg([
          invocation("tooluse_1", "sub_1", {
            status: "in_progress",
            ts: 1_700_000_000_000,
            duration_ms: 5000,
          }),
        ]),
      ],
      "sub_1",
      true,
    );
    expect(run.nodes[0]?.start).toBe(new Date(1_700_000_000_000).toISOString());
    expect(run.nodes[0]?.end).toBeUndefined();
  });

  // `in_progress` is a ToolStatus and not a member of the run-node vocabulary,
  // so handing it to the node fold would make every running delegate read as
  // not started. Each adapter maps its own vocabulary.
  it("maps the tool vocabulary's in_progress onto running", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { status: "in_progress" })])],
      "sub_1",
      true,
    );
    expect(run.nodes[0]?.state).toBe("running");
    expect(run.state).toBe("running");
  });

  // `warn`, which `exec-view/status.ts` words as "stopped" and gives the yellow
  // road-sign mark. The arm compiles whatever it answers, so the VALUE needs the
  // assertion: `ok` would report the reader's own cancel as a clean finish and
  // `fail` as a malfunction.
  it("maps aborted onto warn", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1", { status: "aborted" })])], "sub_1");
    expect(run.nodes[0]?.state).toBe("warn");
    expect(run.state).toBe("warn");
  });

  // The duration is on screen twice already — the detail pane's header and the node's
  // tree row — so a third copy in the facts list would be one number three ways, and
  // the version this list produced disagreed with both (`180s` against `3m 0s`).
  it("states no duration fact, since two surfaces already show it", () => {
    const run = exec(
      [msg([invocation("tooluse_1", "sub_1", { status: "completed", duration_ms: 180_000 })])],
      "sub_1",
    );
    expect(run.nodes[0]?.facts?.some((f) => f.label === "Took")).toBe(false);
  });

  // A delegate whose turn has been paged out of the store's window: the page renders
  // its own not-resident note rather than reaching here, but the adapter must still
  // produce a legal run rather than throwing.
  it("survives a delegate with no resident invocation", () => {
    const run = exec([msg([])], "sub_gone");
    expect(run.nodes).toHaveLength(1);
    expect(run.label).toBe("Subagent");
  });

  // The VALUE, in its own case: `pending` is worded "not started" by
  // `exec-view/status.ts`, which is a positive claim about a delegate this adapter
  // has no status for — and the case that produces it most often is a delegate that
  // ran to completion in a turn the agent process died holding, so the page told the
  // reader the work never began. Reported from the live instance, on a page whose
  // header read "Subagent / not started" over a delegate that had finished.
  it("reads an absent invocation as unknown, never as not-started", () => {
    const run = exec([msg([], [["sub_gone", "it did run"]])], "sub_gone");
    expect(run.nodes[0]?.state).toBe("unknown");
    expect(run.state).toBe("unknown");
  });

  // The one status that still means not-started, so the arm above cannot be read as
  // "this adapter never says pending".
  it("keeps pending for an invocation that really has not started", () => {
    const run = exec([msg([invocation("tooluse_1", "sub_1", { status: "pending" })])], "sub_1");
    expect(run.nodes[0]?.state).toBe("pending");
  });
});

describe("the pipeline shape", () => {
  const messages = [
    msg(
      [
        driver("orc_1", {
          input: { task: "review the diff", stages: [{ name: "a" }, { name: "b" }] },
        }),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b", { status: "in_progress" }),
      ],
      [["sub_b", "working"]],
    ),
  ];

  // One root WITH children, which is what makes the page show its tree and its
  // timeline. A pipeline rendered as a flat leaf would hide the one thing it has that
  // a single delegate does not.
  it("nests every stage under the driver", () => {
    const run = exec(messages, "sub_b");
    expect(run.nodes).toHaveLength(1);
    const root = run.nodes[0];
    expect(root?.children.map((c) => c.path)).toEqual([
      subagentPath("sub_a"),
      subagentPath("sub_b"),
    ]);
    // The row names what it HOLDS, not the object: the header one row up already
    // reads `Subagent pipeline`, so repeating those words here stutters.
    expect(root?.label).toBe("Stages");
    // The EXECUTION's name, not the stage the tab names: the header states the whole
    // pipeline's progress and elapsed time beside it.
    expect(run.label).toBe("Subagent pipeline");
  });

  // Opening a stage's link means "open THIS stage", so the focus names it even though a
  // sibling might be the one wanting attention. That is the whole reason `ExecRun.focus`
  // exists.
  it("focuses the stage the tab names, not the interesting one", () => {
    expect(exec(messages, "sub_a").focus).toBe(subagentPath("sub_a"));
    expect(exec(messages, "sub_b").focus).toBe(subagentPath("sub_b"));
  });

  // The driver's own status says only that something under it is open. What a collapsed
  // group must still report is the worst outcome beneath it.
  it("rolls the driver's state up from its stages", () => {
    expect(exec(messages, "sub_b").nodes[0]?.state).toBe("running");
    const failed = [
      msg([
        driver("orc_1"),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a", { status: "completed" }),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b", { status: "failed" }),
      ]),
    ];
    expect(exec(failed, "sub_a").nodes[0]?.state).toBe("fail");
  });

  // A driver that is not resident is the OTHER absence, and it used to read `running`
  // — a claim of progress with nothing behind it, and the second of two different
  // answers this file gave for "no tool call". `toolState` owns both now, so it reads
  // `unknown`. Two stages, because one renders no root row at all.
  it("reads an absent driver as unknown rather than running", () => {
    const noDriver = [
      msg([
        invocation("invoke_subagent_orc_1_stage_a", "sub_a", { status: "pending" }),
        invocation("invoke_subagent_orc_1_stage_b", "sub_b", { status: "pending" }),
      ]),
    ];
    const run = exec(noDriver, "sub_a");
    expect(run.nodes[0]?.children).toHaveLength(2);
    expect(run.nodes[0]?.state).toBe("unknown");
  });

  // The driver declares its stage list up front, so a pipeline can say "two stages, one
  // running, one not started" from its first frame instead of growing rows as they
  // arrive.
  it("adds a declared stage the transcript has not reached", () => {
    const early = [
      msg([
        driver("orc_1", { input: { stages: [{ name: "a" }, { name: "b" }, { name: "c" }] } }),
        invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
      ]),
    ];
    const kids = exec(early, "sub_a").nodes[0]?.children ?? [];
    expect(kids).toHaveLength(3);
    expect(kids.slice(1).map((k) => k.state)).toEqual(["pending", "pending"]);
    // A stage with no invocation yet hosts nothing, which is what lets the detail pane
    // say so rather than waiting on content that cannot arrive.
    expect(kids[1]?.transcript).toBeUndefined();
  });

  // The task is what the pipeline was ASKED to do, the same slot `state.inputs` fills
  // for a workflow run.
  it("renders the driver's task as the run's input", () => {
    expect(exec(messages, "sub_b").inputs).toEqual({ Task: "review the diff" });
  });

  // `input` is whatever the model produced, so a shape this adapter does not recognise
  // must yield no rows rather than throw on a page whose other half renders fine.
  // TWO stages, so the malformed input is judged against the GROUP shape: at one the
  // pipeline promotes and the assertion below would describe the flat shape instead.
  it("survives a driver whose input is not the expected shape", () => {
    for (const bad of [undefined, null, "text", 42, { stages: "nope" }, { stages: [1, null] }]) {
      const odd = [
        msg([
          driver("orc_1", { input: bad } as Partial<EntryToolCall>),
          invocation("invoke_subagent_orc_1_stage_a", "sub_a"),
          invocation("invoke_subagent_orc_1_stage_b", "sub_b"),
        ]),
      ];
      const run = exec(odd, "sub_a");
      expect(run.nodes[0]?.children).toHaveLength(2);
      expect(run.inputs).toBeUndefined();
    }
  });

  // The page's own rule is `run.nodes.length > 1 || any node has children`, so a group
  // over its only child forces open a tree pane holding one navigable row and one row
  // that is not navigable at all. Promotion here is the twin of the transcript's.
  it("promotes a lone stage to the root, rendering no group row", () => {
    const solo = [
      msg([
        driver("orc_solo", { input: { task: "do it", stages: [{ name: "a" }] } }),
        invocation("invoke_subagent_orc_solo_stage_a", "sub_solo"),
      ]),
    ];
    const run = exec(solo, "sub_solo");
    expect(run.nodes).toHaveLength(1);
    expect(run.nodes[0]?.path).toBe(subagentPath("sub_solo"));
    expect(run.nodes[0]?.children).toHaveLength(0);
    expect(run.nodes[0]?.transcript).toBe(true);
    // The pipeline is still named and still states what it was asked to do: both are
    // ExecRun fields the header renders, not row fields.
    expect(run.label).toBe("Subagent pipeline");
    expect(run.inputs).toEqual({ Task: "do it" });
  });
});
