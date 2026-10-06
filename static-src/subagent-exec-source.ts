// The SUBAGENT adapter: a delegate's slice of a chat, folded into the exec view's model (the
// workflow's is `run-exec-source.ts`). Its blocks persist in the chat file with `agent_subtask_id`.
// TWO SHAPES, chosen by content:
//   a plain `invoke_sub_agent`  -> ONE leaf: no tree pane, no timeline.
//   an `orchestrate_subagent`   -> the driver with its stages (tree + timeline), except at one
//                                  stage, which promotes to the first shape.
// A stage's page shows the WHOLE pipeline with that stage selected, so the ref names a subtask.

import type { ToolCall } from "./types.js";
import { delegateStatusFor } from "./store.js";
import { humanName, truncate } from "./strings.js";
import { inlineAgentOf, subagentLabel, subagentName } from "./roles.js";
import { inFlight, type ExecState } from "./exec-view/status.js";
import type { ExecFact, ExecNode, ExecRun } from "./exec-view/model.js";
import type { SubagentProjection } from "./subagent-slice.js";

/** What a delegate with no resident invocation reads as. */
const FALLBACK = "Subagent";

/** The node path a delegate's content is filed under: the subtask id, a fresh uuid per dispatch (a
 *  stage NAME can repeat across pipelines). Exported because `page.bodyFor(path)` and the focus must
 *  agree on it. */
export function subagentPath(subtaskID: string): string {
  return subtaskID;
}

/** The driver's path. Prefixed, because a driver is addressed by its TOOL-CALL id
 *  while every stage is addressed by a subtask uuid, and an unprefixed tool-call id
 *  could in principle collide with one. */
function driverPath(pipelineID: string): string {
  return `pipeline:${pipelineID}`;
}

/** A delegate's state from its invocation's `ToolStatus` folded against the chat's turn liveness,
 *  so page, dot and card agree when the turn died before `tool_result`. */
function toolState(status: ToolCall["status"] | undefined, turnLive: boolean): ExecState {
  switch (status === undefined ? undefined : delegateStatusFor(status, turnLive)) {
    // No invocation resident: `unknown`, not `pending` ("not started" is wrong for a delegate whose turn
    // died after it finished). The run adapter folds an unrecognised status the same way.
    case undefined:
      return "unknown";
    case "pending":
      return "pending";
    case "in_progress":
      return "running";
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    // `exec-view/status.ts` already words `warn` as "stopped", which is what an
    // abort is: the work ended without a verdict of its own.
    case "aborted":
      return "warn";
  }
}

/** The identity facts for one delegate. Model and effort rows appear only for an inline helper
 *  (`inlineAgent`); a saved agent's input names neither. */
function factsOf(invocation: ToolCall | undefined, stage: string): ExecFact[] {
  const facts: ExecFact[] = [];
  const add = (label: string, value: string, mono = false): void => {
    if (value !== "") {
      facts.push(mono ? { label, value, mono } : { label, value });
    }
  };
  if (invocation !== undefined) {
    add("Agent", subagentName(invocation));
    const inline = inlineAgentOf(invocation);
    if (inline !== null) {
      add("Model", inline.model);
      add("Effort", inline.effort);
    }
  }
  add("Stage", stage);
  if (invocation !== undefined) {
    // No duration row: the detail header and tree row already state it. Handles last, monospace.
    add("Subtask", invocation.agent_subtask_id ?? "", true);
    add("Call", invocation.id, true);
  }
  return facts;
}

/** The one-line summary under a row's label: the delegate's own agent id, so a
 *  pipeline's tree reads as which agent ran which stage. Empty when the label already
 *  says it, since repeating it is noise on a row two words wide. */
function subtitleOf(invocation: ToolCall | undefined, label: string): string {
  if (invocation === undefined) {
    return "";
  }
  const id = subagentName(invocation);
  if (id === "" || humanName(id) === label) {
    return "";
  }
  return id;
}

/** The prose an `orchestrate_subagent` driver declares: its `task` (the stages are the tree),
 *  truncated, since the header renders one line per input. */
function driverInputs(driver: ToolCall | undefined): Record<string, string> | undefined {
  if (driver === undefined) {
    return undefined;
  }
  const input = driver.input;
  if (input === null || input === undefined || typeof input !== "object") {
    return undefined;
  }
  const task = (input as Record<string, unknown>)["task"];
  if (typeof task !== "string" || task.trim() === "") {
    return undefined;
  }
  return { Task: truncate(task.trim(), 400) };
}

/** Fold one delegate into a leaf. */
function toLeaf(
  subtaskID: string,
  stage: string,
  invocation: ToolCall | undefined,
  turnLive: boolean,
): ExecNode {
  const label =
    invocation === undefined
      ? stage === ""
        ? FALLBACK
        : humanName(stage)
      : subagentLabel(invocation);
  // ONE owner for "what does an absent invocation mean": `toolState`'s own
  // `undefined` arm. The ternary this replaces answered `pending` here while the
  // driver's answered `running` two folds down, so one absence had two readings.
  const state: ExecState = toolState(invocation?.status, turnLive);
  const out: ExecNode = {
    path: subagentPath(subtaskID),
    label,
    kind: "step",
    state,
    children: [],
    // Every delegate can host one: its blocks are in the chat file, so unlike a
    // workflow step there is no such thing as a leaf here that streams nothing.
    transcript: true,
  };
  const sub = subtitleOf(invocation, label);
  if (sub !== "") {
    out.subtitle = sub;
  }
  // The timeline's input: a call's `ts` (dispatch time) plus `duration_ms`. `end` stays absent while
  // in flight, and a settled call with no duration gets no end rather than a zero-width bar.
  if (invocation !== undefined && invocation.ts > 0) {
    out.start = new Date(invocation.ts).toISOString();
    const ms = invocation.duration_ms ?? 0;
    if (ms > 0 && !inFlight(state)) {
      out.end = new Date(invocation.ts + ms).toISOString();
    }
  }
  const facts = factsOf(invocation, stage);
  if (facts.length > 0) {
    out.facts = facts;
  }
  // NO `output`: a delegate's last text block IS its report, so filling it renders the report twice;
  // the transcript keeps it in context.
  return out;
}

/** A driver reads as the worst outcome beneath it; its own status counts only when terminal (an
 *  `in_progress` driver tells the reader nothing). */
function rollUp(own: ExecState, kids: readonly ExecNode[]): ExecState {
  if (kids.length === 0) {
    return own;
  }
  const states = new Set(kids.map((k) => k.state));
  for (const s of ["input", "fail", "warn", "running", "waiting"] as const) {
    if (states.has(s)) {
      return s;
    }
  }
  // No `unknown` clause, unreachable: a KID is always a stage with a status (`groupOf` admits only
  // `isSubagentInvocation` members). `unknown` arrives as `own` alone.
  if (states.has("ok")) {
    return states.has("pending") ? "running" : "ok";
  }
  return own;
}

/** Fold a delegate and its group into the exec view's model. `projection` is
 *  `sliceSubagentGroup`'s walk, passed in because the view already holds it. `turnLive` defaults
 *  to the answer that claims nothing. */
export function subagentToExec(
  subtaskID: string,
  projection: SubagentProjection,
  turnLive = true,
): ExecRun {
  const group = projection.group;
  // The delegate the TAB names. Present for every non-empty id the projection was
  // asked about, so the fallbacks below are for the empty id alone.
  const own = projection.slices.get(subtaskID);
  const focus = subagentPath(subtaskID);

  // --- the single-delegate shape --------------------------------------------
  if (group.pipeline === "") {
    const leaf = toLeaf(subtaskID, "", own?.invocation, turnLive);
    return {
      id: subtaskID,
      label: leaf.label,
      state: leaf.state,
      nodes: [leaf],
      live: own?.live ?? false,
      focus,
    };
  }

  // --- the pipeline shape ----------------------------------------------------
  const stages = group.members.map((m) => toLeaf(m.subtaskID, m.stage, m.invocation, turnLive));
  // A stage the transcript has not reached yet is a real node, and showing it is the
  // point of a plan: `orchestrate_subagent` declares its stage list up front, so a
  // pipeline can say "four stages, one running, two not started" from its first frame.
  for (const name of declaredStages(group.driver)) {
    if (!stages.some((s) => s.label === humanName(name) || factStage(s) === name)) {
      stages.push({
        path: `stage:${group.pipeline}:${name}`,
        label: humanName(name),
        kind: "step",
        state: "pending",
        children: [],
        facts: [{ label: "Stage", value: name }],
      });
    }
  }
  const driverState = rollUp(toolState(group.driver?.status, turnLive), stages);
  const root: ExecNode = {
    // No stage count (the header's `step N of M` states it). `Stages`: the label above already reads
    // `Subagent pipeline`, so this row says what it HOLDS.
    path: driverPath(group.pipeline),
    label: "Stages",
    kind: "parallel",
    state: driverState,
    children: stages,
    subtitle: "orchestrated subagents",
  };
  const out: ExecRun = {
    id: subtaskID,
    // The EXECUTION's name: a stage's name beside the header's progress would read as the stage's
    // progress. The tab and detail pane name the delegate.
    label: "Subagent pipeline",
    state: driverState,
    // ONE stage renders no group row, as the transcript promotes; the pipeline's identity lives in
    // `ExecRun` fields the header renders, and the window comes from the leaves.
    nodes: stages.length === 1 ? stages : [root],
    live: (own?.live ?? false) || inFlight(driverState),
    focus,
  };
  const inputs = driverInputs(group.driver);
  if (inputs !== undefined) {
    out.inputs = inputs;
  }
  return out;
}

/** A node's declared stage name, off its own facts. Reading it back rather than
 *  carrying a second field, since `ExecFact` is the model's channel for exactly this
 *  and a parallel array would be one more thing to keep aligned. */
function factStage(node: ExecNode): string {
  return node.facts?.find((f) => f.label === "Stage")?.value ?? "";
}

/** The stage names an `orchestrate_subagent` call declared, read through guards (`input` is
 *  model-produced), as `indexPlan` reads `nodePlan`. */
function declaredStages(driver: ToolCall | undefined): string[] {
  const input = driver?.input;
  if (input === null || input === undefined || typeof input !== "object") {
    return [];
  }
  const stages = (input as Record<string, unknown>)["stages"];
  if (!Array.isArray(stages)) {
    return [];
  }
  const out: string[] = [];
  for (const s of stages) {
    if (s === null || typeof s !== "object") {
      continue;
    }
    const name = (s as Record<string, unknown>)["name"];
    if (typeof name === "string" && name !== "") {
      out.push(name);
    }
  }
  return out;
}
