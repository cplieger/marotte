// What a delegate is, projected out of a chat's entry log. A delegate has no
// record of its own: its entries carry its uuid as `Entry.lane`, and its dispatching `tool_call`
// carries it in `payload.agent_subtask_id` from the ISSUER's lane. Pure and DOM-free, a leaf for
// three consumers (tab-materialize.ts, subagent-dots.ts, subagent-view.ts). Nothing is copied: a
// view renders the REAL turn with the uuid as render root, and the store is never read here.

import type { Entry, OpenEntry, ToolCall, TurnState } from "./types.js";
import { payloadOf, projectTurn, type Turn, type TurnSource } from "./turns.js";
import { callIDOfToolResult } from "./entry-ids.js";
import { settledToolCall } from "./store.js";
import { isSubagentInvocation, isToolActive } from "./tool-schema.js";

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

function laneOf(state: TurnState, lane: string): { entries: Entry[]; open: boolean } {
  const out: Entry[] = [];
  for (const e of state.entries) {
    if ((e.lane ?? "") === lane) {
      out.push(e);
    }
  }
  return { entries: out, open: state.openEntries.has(lane) };
}

/** Project one delegate: its invocation when resident, the turn holding its lane (from the
 *  invocation, else a scan for the lane's own entries, since a window can hold a delegate's output
 *  with its dispatching call paged out) and that lane. `chatLive` is `live`'s fallback with no
 *  resident invocation. For one lane's entries alone take `readLane`. */
export function sliceSubagent(
  src: TurnSource,
  subtaskID: string,
  chatLive: boolean,
): SubagentSlice {
  const located = locateInvocation(src, subtaskID);
  const invocation = located?.call;
  const turnID = located?.turnID ?? turnHoldingLane(src, subtaskID);
  const state = turnID === undefined ? undefined : src.turns.get(turnID);
  const held = state === undefined ? { entries: [], open: false } : laneOf(state, subtaskID);
  return {
    invocation,
    turn: turnID === undefined ? undefined : projectTurn(src, turnID),
    entries: held.entries,
    open: held.open,
    live: invocation === undefined ? chatLive : isToolActive(invocation.status),
  };
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
 *  lane's output and no projected turn. Newest-first
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
