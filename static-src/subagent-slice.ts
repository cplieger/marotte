// What a delegate (and its pipeline) is, projected out of a chat's entry log. A delegate has no
// record of its own: its entries carry its uuid as `Entry.lane`, and its dispatching `tool_call`
// carries it in `payload.agent_subtask_id` from the ISSUER's lane. Pure and DOM-free, a leaf for
// three consumers (tab-materialize.ts, subagent-dots.ts, subagent-view.ts). Nothing is copied: a
// view renders the REAL turn with the uuid as render root, and the store is never read here.

import type { Entry, OpenEntry, ToolCall, TurnState } from "./types.js";
import { payloadOf, projectTurn, type Turn, type TurnSource } from "./turns.js";
import { callIDOfToolResult } from "./entry-ids.js";
import { settledToolCall } from "./store.js";
import { isSubagentInvocation, isToolActive } from "./tool-schema.js";

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id
 *  (`invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`). Duplicated from
 *  `messages-blocks.ts` to stay a leaf; both copies are pinned against the same literals. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not stage-shaped.
 *  `indexOf`: driver ids are machine-minted and stage names author-supplied, so the FIRST
 *  separator is the seam (`run_stage_two` still resolves). */
export function pipelineOf(toolCallID: string): string {
  if (!toolCallID.startsWith(STAGE_PREFIX)) {
    return "";
  }
  const rest = toolCallID.slice(STAGE_PREFIX.length);
  const sep = rest.indexOf(STAGE_SEP);
  if (sep <= 0 || sep + STAGE_SEP.length >= rest.length) {
    return "";
  }
  return rest.slice(0, sep);
}

/** The stage's own name, from the same id. "" when it is not stage-shaped. */
export function stageName(toolCallID: string): string {
  if (pipelineOf(toolCallID) === "") {
    return "";
  }
  const rest = toolCallID.slice(STAGE_PREFIX.length);
  return rest.slice(rest.indexOf(STAGE_SEP) + STAGE_SEP.length);
}

/** One delegate's lane, located in the conversation that ran it. */
export interface SubagentSlice {
  /** The tool call that DISPATCHED the delegate, or undefined when the chat's
   *  resident window does not hold it. It carries the name, the status and the
   *  wall clock, so the page's header is built from it and from nothing else. */
  readonly invocation: ToolCall | undefined;
  /** The TURN holding this delegate's lane, or undefined when neither lane nor invocation is
   *  resident. A delegate never spans turns (a model switch appends `model_switched`). */
  readonly turn: Turn | undefined;
  /** The lane's SEALED entries in `seq` order, for the page's empty note and `subagent-tail.ts`;
   *  a renderer takes `turn` and addresses the lane itself. */
  readonly entries: readonly Entry[];
  /** Whether the lane holds an OPEN entry — text arriving now, with no `seq` and no
   *  position, so it is not in `entries`. */
  readonly open: boolean;
  /** Whether the delegate is still working: from the INVOCATION's status when resident (a delegate
   *  can finish while its chat carries on), else the chat's flag. */
  readonly live: boolean;
}

/** One `tool_call` entry located in the log: the call as a card paints it, plus the
 *  turn it was issued in, which is the turn its delegate's lane lives in. */
interface LocatedCall {
  turnID: string;
  call: ToolCall;
}

/** Every `tool_call` entry of one turn joined with its result; two passes, since a result is
 *  appended after its call. */
function callsOfTurn(entries: readonly Entry[]): ToolCall[] {
  const settled = new Map<string, Entry>();
  for (const e of entries) {
    if (e.kind === "tool_result") {
      const callID = callIDOfToolResult(e.id);
      if (callID !== null) {
        settled.set(callID, e);
      }
    }
  }
  const out: ToolCall[] = [];
  for (const e of entries) {
    const call = payloadOf(e, "tool_call");
    if (call === undefined) {
      continue;
    }
    const resultEntry = settled.get(e.id);
    out.push(
      settledToolCall(
        call,
        resultEntry === undefined ? undefined : payloadOf(resultEntry, "tool_result"),
      ),
    );
  }
  return out;
}

/** The tool call that dispatched `subtaskID` and its turn, walking resident turns NEWEST first. */
function locateInvocation(src: TurnSource, subtaskID: string): LocatedCall | undefined {
  if (subtaskID === "") {
    return undefined;
  }
  for (let i = src.turn_order.length - 1; i >= 0; i--) {
    const turnID = src.turn_order[i];
    if (turnID === undefined) {
      continue;
    }
    const state = src.turns.get(turnID);
    if (state === undefined) {
      continue;
    }
    for (const call of callsOfTurn(state.entries)) {
      if ((call.agent_subtask_id ?? "") === subtaskID && isSubagentInvocation(call)) {
        return { turnID, call };
      }
    }
  }
  return undefined;
}

/** The tool call that dispatched `subtaskID`, or undefined (what the tab factory and dot need). */
export function findSubagentInvocation(src: TurnSource, subtaskID: string): ToolCall | undefined {
  return locateInvocation(src, subtaskID)?.call;
}

/** A shape signature over a lane's entries: whether a mounted render can be UPDATED (the
 *  dispatcher appends past a watermark) or must rebuild. Kind plus `seq`, so a same-kind entry
 *  inserted mid-lane differs from tail growth. */
export function blockShape(entries: readonly Entry[]): string[] {
  return entries.map((e) => `${e.kind}\u0000${String(e.seq)}`);
}

/** Whether `next` extends `prev` rather than replacing it. */
export function shapeExtends(prev: readonly string[], next: readonly string[]): boolean {
  if (next.length < prev.length) {
    return false;
  }
  for (let i = 0; i < prev.length; i++) {
    if (prev[i] !== next[i]) {
      return false;
    }
  }
  return true;
}

/** One member of a PIPELINE: a delegate plus the identity its stage id carries. */
interface SubagentMember {
  /** The delegate's uuid, which is its lane. */
  subtaskID: string;
  /** The stage's author-given name, or "" for a delegate that is not a stage. */
  stage: string;
  invocation: ToolCall;
  /** The turn the invocation was issued in, which is the turn this member's lane lives
   *  in. Carried out of the walk that found the call, so the projection resolves a
   *  member's turn without a second pass over the same window. */
  turnID: string;
}

/** What a delegate BELONGS to: `pipeline` is the driver's tool-call id and `members` every stage
 *  in conversation order; both empty for a plain `invoke_sub_agent` (the single-leaf case). */
export interface SubagentGroup {
  pipeline: string;
  /** The `orchestrate_subagent` call that opened the pipeline, when it is resident.
   *  It carries the declared stage list, so it is what names a stage the transcript
   *  has not reached yet. */
  driver: ToolCall | undefined;
  members: SubagentMember[];
}

/** The title the `orchestrate_subagent` driver call carries. */
const PIPELINE_TITLE = "Orchestrate Sub-agent";

/** Resolve a delegate's pipeline and siblings from the turn's tool calls (a stage's entries carry
 *  only a bare uuid), with the located call the pipeline came from: that call names its TURN as
 *  well as its driver, so the projection takes the answer rather than asking again. Scanning the
 *  whole window makes the driver/stage arrival orders equivalent. */
export function resolveGroup(
  src: TurnSource,
  subtaskID: string,
): { group: SubagentGroup; own: LocatedCall | undefined } {
  const own = locateInvocation(src, subtaskID);
  const pipeline = own === undefined ? "" : pipelineOf(own.call.id);
  if (pipeline === "") {
    return { group: { pipeline: "", driver: undefined, members: [] }, own };
  }
  const members: SubagentMember[] = [];
  const seen = new Set<string>();
  let driver: ToolCall | undefined;
  for (const turnID of src.turn_order) {
    const state = src.turns.get(turnID);
    if (state === undefined) {
      continue;
    }
    for (const call of callsOfTurn(state.entries)) {
      if (call.id === pipeline && call.title === PIPELINE_TITLE) {
        driver = call;
        continue;
      }
      const sub = call.agent_subtask_id ?? "";
      if (sub === "" || seen.has(sub) || pipelineOf(call.id) !== pipeline) {
        continue;
      }
      if (!isSubagentInvocation(call)) {
        continue;
      }
      seen.add(sub);
      members.push({ subtaskID: sub, stage: stageName(call.id), invocation: call, turnID });
    }
  }
  return { group: { pipeline, driver, members }, own };
}

/** A whole GROUP projected out of one conversation: what the requested delegate
 *  belongs to, plus a slice for every member of it. */
export interface SubagentProjection {
  /** The pipeline the requested delegate belongs to, and its siblings. */
  readonly group: SubagentGroup;
  /** One slice per subtask id the group names, empty or not: "projected and empty" differs from
   *  "not projected". */
  readonly slices: Map<string, SubagentSlice>;
}

function laneOf(state: TurnState, lane: string): { entries: Entry[]; open: boolean } {
  const out: Entry[] = [];
  for (const e of state.entries) {
    if ((e.lane ?? "") === lane) {
      out.push(e);
    }
  }
  return { entries: out, open: state.openEntries.has(lane) };
}

/** Project a delegate AND its siblings from ONE window scan (`resolveGroup`), plus a fallback for
 *  a lane whose invocation is not resident; for one lane's entries take `readLane`. `chatLive` is
 *  the per-member `live` fallback. `subtaskID === ""` skips the walk. */
export function sliceSubagentGroup(
  src: TurnSource,
  subtaskID: string,
  chatLive: boolean,
): SubagentProjection {
  const { group, own } = resolveGroup(src, subtaskID);
  const slices = new Map<string, SubagentSlice>();
  if (subtaskID === "" && group.members.length === 0) {
    return { group, slices };
  }
  const wanted = new Map<string, LocatedCall | undefined>();
  for (const m of group.members) {
    wanted.set(m.subtaskID, { turnID: m.turnID, call: m.invocation });
  }
  // A STAGE is already a member, so this is the plain `invoke_sub_agent` case.
  if (subtaskID !== "" && !wanted.has(subtaskID)) {
    wanted.set(subtaskID, own);
  }

  for (const [lane, located] of wanted) {
    const invocation = located?.call;
    // The turn holding the lane, from the invocation when it is resident and from a
    // scan for the lane's own entries otherwise: a window can hold a delegate's
    // output with its dispatching call paged out.
    const turnID = located?.turnID ?? turnHoldingLane(src, lane);
    const state = turnID === undefined ? undefined : src.turns.get(turnID);
    const held = state === undefined ? { entries: [], open: false } : laneOf(state, lane);
    slices.set(lane, {
      invocation,
      turn: turnID === undefined ? undefined : projectTurn(src, turnID),
      entries: held.entries,
      open: held.open,
      live: invocation === undefined ? chatLive : isToolActive(invocation.status),
    });
  }
  return { group, slices };
}

/** The id of the turn whose log holds `lane`, newest first. The lane signal is keyed
 *  `(turn, lane)`; for the entries too, take `readLane`. */
export function turnHoldingLane(src: TurnSource, lane: string): string | undefined {
  if (lane === "") {
    return undefined;
  }
  for (let i = src.turn_order.length - 1; i >= 0; i--) {
    const turnID = src.turn_order[i];
    if (turnID === undefined) {
      continue;
    }
    const state = src.turns.get(turnID);
    if (state === undefined) {
      continue;
    }
    if (state.openEntries.has(lane)) {
      return turnID;
    }
    for (const e of state.entries) {
      if ((e.lane ?? "") === lane) {
        return turnID;
      }
    }
  }
  return undefined;
}

/** ONE lane, read on its own terms: the turn holding it, its sealed entries in `seq`
 *  order, and its open entry when it has one. */
export interface LaneRead {
  /** The turn whose log holds the lane, or undefined when nothing of it is resident.
   *  A subscriber names it to reach `laneSig(turnID, lane)`. */
  readonly turnID: string | undefined;
  readonly entries: readonly Entry[];
  /** The lane's OPEN entry — text arriving now, with no `seq` and no position — or
   *  undefined. It is the lane's tail, so a reader takes it last. */
  readonly open: OpenEntry | undefined;
}

/** Read one lane without projecting anything around it: for a caller that wants that
 *  lane's output and neither the pipeline around it nor a projected turn. Newest-first
 *  with an early exit, so a delegate in the newest turn costs that turn. */
export function readLane(src: TurnSource, lane: string): LaneRead {
  if (lane !== "") {
    for (let i = src.turn_order.length - 1; i >= 0; i--) {
      const turnID = src.turn_order[i];
      if (turnID === undefined) {
        continue;
      }
      const state = src.turns.get(turnID);
      if (state === undefined) {
        continue;
      }
      const open = state.openEntries.get(lane);
      const held = laneOf(state, lane);
      if (open !== undefined || held.entries.length > 0) {
        return { turnID, entries: held.entries, open };
      }
    }
  }
  return { turnID: undefined, entries: [], open: undefined };
}
