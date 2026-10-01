// ---------------------------------------------------------------------------
// subagent-slice: what a delegate is — and what the pipeline around it is —
// projected out of a chat's entry log.
//
// A subagent execution has no record of its own anywhere. There is no
// `/api/subagents/{id}`, no `subagent_*` SSE event and no store keyed by
// subtask: what exists is the delegate's LANE. Every entry a delegate produced
// carries its uuid as `Entry.lane`, and its dispatching `tool_call` carries the
// same uuid in `payload.agent_subtask_id` while sitting in the ISSUER's lane
// (section 3.4). So "the introspect subagent's output" is a QUERY over a
// conversation's turns, and this module is that query.
//
// Pure and DOM-free, over a `TurnSource` plus a subtask id. Three consumers,
// which is the whole reason it is not a private helper in any of them: the tab
// factory (tab-materialize.ts) and the tab dot (subagent-dots.ts) need the
// delegate's INVOCATION for a label and a status, and the subagent page
// (subagent-view.ts) needs the turn its lane lives in. None can import another,
// and none can import the transcript's dispatcher, which reaches the whole
// rendering stack.
//
// NOTHING IS COPIED AND NO LANE IS CLEARED. A view renders the REAL turn with
// the delegate's uuid as its render root, and every lane predicate in
// `messages-blocks.ts` compares against that root — so the delegate's entries
// render in the flow and a nested invocation renders as a card for the
// grandchild. The `settledToolCall` join is the store's own pure function; the
// store is never read here, because the two callers subscribe to it differently
// (the factory peeks untracked, the page reads inside an effect) and a module
// that fetched for them would make one of those wrong.
// ---------------------------------------------------------------------------

import type { Entry, OpenEntry, ToolCall, TurnState } from "./types.js";
import { payloadOf, projectTurn, type Turn, type TurnSource } from "./turns.js";
import { callIDOfToolResult } from "./entry-ids.js";
import { settledToolCall } from "./store.js";
import { isSubagentInvocation, isToolActive } from "./tool-schema.js";

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id. The full shape is
 *  `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`, so a stage names its
 *  driver in its own id — the same way a workflow run's step names its run. No wire
 *  field carries this and none is needed; measured across 36 live chat files, 59 of 60
 *  stage calls have that shape.
 *
 *  Duplicated from `messages-blocks.ts` deliberately rather than imported: that
 *  module reaches the whole transcript stack, and this one is a leaf three other leaves
 *  read. Both copies are pinned against the same literals, each from its own side:
 *  `subagent-exec-source.test.ts` reads `pipelineOf`/`stageName` directly, and
 *  `messages-blocks.test.ts` drives its copy through the transcript's nesting. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not
 *  stage-shaped (a plain `invoke_sub_agent` call has no pipeline).
 *
 *  `indexOf` for the separator, because only the RIGHT half can contain one. A driver
 *  id is machine-minted by KAS and a stage name is author-supplied, so the first
 *  occurrence is the seam and a stage called `run_stage_two` still resolves to its own
 *  driver. Measured over the 65 distinct stage ids on the live volume: every driver
 *  half is a `toolu_bdrk_*` tool-use id, and stage names carry underscores freely
 *  (`review_fable-sub-agent-start`, `perf_hotfix-sub-agent-start`).
 *
 *  `messages-blocks.ts` `stagePipelineID` used `lastIndexOf` and its comment named
 *  exactly the case that breaks it; both were corrected together. Nothing on the
 *  volume trips it today (zero ids carry two separators), so the defect was latent:
 *  such a stage would have resolved to a driver that does not exist and rendered as a
 *  flat sibling of its own pipeline. */
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
  /** The TURN whose log holds this delegate's lane, or undefined when neither the
   *  lane nor its invocation is resident.
   *
   *  A delegate never spans two turns: a mid-turn model switch appends a
   *  `model_switched` entry rather than closing the turn (section 4.2), so one lane
   *  is one turn's and the page needs no second projection to stitch. */
  readonly turn: Turn | undefined;
  /** The lane's SEALED entries, in `seq` order.
   *
   *  Carried for the two questions a caller asks WITHOUT rendering: whether the
   *  delegate has produced anything (the page's empty note), and what its trailing
   *  output is (`subagent-tail.ts`). A renderer takes `turn` instead and addresses
   *  the lane itself, so nothing here is a copy the DOM depends on. */
  readonly entries: readonly Entry[];
  /** Whether the lane holds an OPEN entry — text arriving now, with no `seq` and no
   *  position (section 3.4), so it is not in `entries`. */
  readonly open: boolean;
  /** Whether the delegate is still working.
   *
   *  From the INVOCATION's own status when it is resident, because that is the
   *  fact — a delegate can finish while its conversation carries on for another
   *  ten minutes, and reading the chat's `thinking` flag would leave a settled
   *  delegate under a streaming caret. The chat's flag is only the fallback for a
   *  delegate whose invocation has been paged out. */
  readonly live: boolean;
}

/** One `tool_call` entry located in the log: the call as a card paints it, plus the
 *  turn it was issued in, which is the turn its delegate's lane lives in. */
interface LocatedCall {
  turnID: string;
  call: ToolCall;
}

/** Every `tool_call` entry of one turn as a card paints it, joined with its result.
 *
 *  Two passes over the turn rather than one, because a result is appended after its
 *  call and the join has to be complete before any call is answered. */
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

/** The tool call that dispatched `subtaskID`, with the turn it was issued in.
 *
 *  Walks the resident turns NEWEST first: a delegate a reader has a tab open for is
 *  almost always in the window's tail, and a hit ends the walk. */
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

/** The tool call that dispatched `subtaskID`, or undefined.
 *
 *  Exported on its own because the tab factory and the tab dot want only this: a
 *  label for a row and a status for its mark, with no reason to reach the lane. */
export function findSubagentInvocation(src: TurnSource, subtaskID: string): ToolCall | undefined {
  return locateInvocation(src, subtaskID)?.call;
}

/** A shape signature over a lane's entries, for deciding whether a mounted render can
 *  be UPDATED or has to be rebuilt.
 *
 *  The dispatcher's incremental update appends past a watermark, so it is only correct
 *  while the prefix it mounted is unchanged: tail growth keeps it, a range read that
 *  filled a hole does not. Each component is the entry's KIND plus its `seq` — design
 *  8.8 states the kind alone, and the `seq` is what separates a same-kind entry
 *  inserted mid-lane from growth at the tail. */
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

/** What a delegate BELONGS to, which decides whether its page is a tree or a leaf.
 *
 *  `pipeline` is the driver's tool-call id when the delegate is a stage of one, and
 *  `members` are every stage of that pipeline in conversation order, the requested
 *  one included. Both empty for a plain `invoke_sub_agent`, which is the single-leaf
 *  case and the one the exec page renders with no tree pane at all. */
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

/** Resolve the pipeline a delegate belongs to, and its siblings.
 *
 *  Driven off the turn's tool calls rather than off lanes, for the reason
 *  `indexPipelines` is: a stage's entries carry only a bare subtask uuid, which names
 *  nothing, while the stage's INVOCATION carries both that uuid and its driver's id.
 *  Scanning the whole window also makes the two arrival orders equivalent — the driver
 *  and its first stage race on the wire, and after a reload the driver is persisted
 *  while live entries are not. */
export function groupOf(src: TurnSource, subtaskID: string): SubagentGroup {
  return resolveGroup(src, subtaskID).group;
}

/** `groupOf` plus the located call it resolved the pipeline from: that call names its
 *  TURN as well as its driver, so the projection takes the answer rather than asking
 *  the same question again. */
function resolveGroup(
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
  /** One slice per subtask id the group names, the requested delegate included.
   *
   *  Every named id HAS an entry, empty or not, because "not projected" and
   *  "projected and empty" are different answers and the page says different things
   *  for each: a delegate dispatched a moment ago has a real empty slice, while an id
   *  this projection does not name is not a delegate of this group at all. */
  readonly slices: Map<string, SubagentSlice>;
}

/** Every entry of `lane` in the turn `state` holds, in `seq` order, plus whether that
 *  lane has an open entry. */
function laneOf(state: TurnState, lane: string): { entries: Entry[]; open: boolean } {
  const out: Entry[] = [];
  for (const e of state.entries) {
    if ((e.lane ?? "") === lane) {
      out.push(e);
    }
  }
  return { entries: out, open: state.openEntries.has(lane) };
}

/** Project a delegate AND its siblings out of a conversation.
 *
 *  A pipeline's stages interleave with each other and with the chat's own lane, and the
 *  page needs all of them — a stage's page shows the WHOLE pipeline — so every member
 *  comes out of the ONE window scan `resolveGroup` makes, carrying the turn that scan
 *  found it in. The only lookup left is the FALLBACK for a lane whose invocation is not
 *  resident. It still costs a pass over every resident turn for a pipeline, plus one
 *  `projectTurn` per member: a caller wanting ONE lane's entries takes `readLane`.
 *
 *  `chatLive` is the fallback for `live` only, per member — see that field's own
 *  note. A stage can finish while its siblings and the conversation carry on.
 *
 *  `subtaskID === ""` names no delegate, so the projection is empty and the walk is
 *  skipped: the page early-returns on that before it ever gets here. */
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
  // The requested delegate is already a member whenever it is a STAGE — `resolveGroup`
  // resolves the pipeline off its own invocation and then finds itself — so this
  // branch is the plain `invoke_sub_agent` case, answered by the call that resolution
  // already located.
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

/** The id of the turn whose log holds `lane`, or undefined. Newest first, for
 *  `locateInvocation`'s reason.
 *
 *  Exported because a caller holding a lane and nothing else has to name the turn
 *  before it can subscribe: the lane signal is keyed `(turn, lane)`. A caller that also
 *  wants the lane's entries takes `readLane`, which answers both from one walk. */
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
