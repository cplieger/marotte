// The SUBAGENT adapter: a delegate's slice of a chat, folded into the exec view's model (the
// workflow's is `run-exec-source.ts`) as ONE leaf: no tree pane, no timeline. Its blocks persist
// in the chat file with `agent_subtask_id`.

import type { ToolCall } from "./types.js";
import { delegateStatusFor } from "./store.js";
import { humanName } from "./strings.js";
import { modelCategoryOf, subagentLabel, subagentName } from "./roles.js";
import { inFlight, type ExecState } from "./exec-view/status.js";
import type { ExecFact, ExecNode, ExecRun } from "./exec-view/model.js";
import type { SubagentSlice } from "./subagent-slice.js";

/** What a delegate with no resident invocation reads as. */
const FALLBACK = "Subagent";

/** The node path a delegate's content is filed under: the subtask id, a fresh uuid per dispatch.
 *  Exported because `page.bodyFor(path)` and the focus must agree on it. */
export function subagentPath(subtaskID: string): string {
  return subtaskID;
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

/** The agent, the parent's `modelCategory` request, then the handles, last and monospace. No
 *  duration row: the detail header and tree row already state it. */
function factsOf(invocation: ToolCall | undefined): ExecFact[] {
  if (invocation === undefined) {
    return [];
  }
  const facts: ExecFact[] = [];
  const add = (label: string, value: string, mono = false): void => {
    if (value !== "") {
      facts.push(mono ? { label, value, mono } : { label, value });
    }
  };
  add("Agent", subagentName(invocation));
  add("Category", modelCategoryOf(invocation));
  add("Subtask", invocation.agent_subtask_id ?? "", true);
  add("Call", invocation.id, true);
  return facts;
}

/** The one-line summary under a row's label: the delegate's own agent id. Empty when the label
 *  already says it, since repeating it is noise on a row two words wide. */
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

function toLeaf(subtaskID: string, invocation: ToolCall | undefined, turnLive: boolean): ExecNode {
  const label = invocation === undefined ? FALLBACK : subagentLabel(invocation);
  // ONE owner for "what does an absent invocation mean": `toolState`'s own `undefined` arm.
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
  // The span: a call's `ts` (dispatch time) plus `duration_ms`. `end` stays absent while in flight,
  // and a settled call with no duration gets no end rather than a zero-width span.
  if (invocation !== undefined && invocation.ts > 0) {
    out.start = new Date(invocation.ts).toISOString();
    const ms = invocation.duration_ms ?? 0;
    if (ms > 0 && !inFlight(state)) {
      out.end = new Date(invocation.ts + ms).toISOString();
    }
  }
  const facts = factsOf(invocation);
  if (facts.length > 0) {
    out.facts = facts;
  }
  // NO `output`: a delegate's last text block IS its report, so filling it renders the report twice;
  // the transcript keeps it in context.
  return out;
}

/** Fold a delegate's slice into the exec view's model. `slice` is `sliceSubagent`'s projection,
 *  passed in because the view already holds it. `turnLive` defaults to the answer that claims
 *  nothing. */
export function subagentToExec(subtaskID: string, slice: SubagentSlice, turnLive = true): ExecRun {
  const leaf = toLeaf(subtaskID, slice.invocation, turnLive);
  return {
    label: leaf.label,
    state: leaf.state,
    nodes: [leaf],
    live: slice.live,
    focus: subagentPath(subtaskID),
  };
}
