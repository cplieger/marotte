// The turn projection: the store's `TurnState` per turn read into the `Turn`s the transcript
// renders. Nothing INFERS a boundary: the store already partitioned entries, an ordinal is
// `turn_open.n`, a verdict `turn_close.outcome`. Pure and DOM-free.

import { callIDOfToolResult } from "./entry-ids.js";
import { askBucket, type AskBucket } from "./interaction.js";
import { defaultFailureReason, severityOf } from "./turn-severity.js";
import type {
  AnyEntry,
  Entry,
  EntryKind,
  EntryPayload,
  EntryPrompt,
  EntryToolCall,
  EntryToolResult,
  EntryTurnClose,
  EntryTurnOpen,
  FileChange,
  KindedEntry,
  OpenEntry,
  ToolKind,
  TurnState,
  TurnThroughput,
} from "./types.js";
import type { TurnOutcome } from "./wire/types.gen.js";

/** A turn's result, as scannable colour down the transcript. Re-exported from the generated wire
 *  types (the rule exists in both languages). `running` is the member no `turn_close` carries: a
 *  turn with no close IS running, since crash-orphaned turns are closed before the log is served. */
export type { TurnOutcome };

// --- Reading an entry ---

/** Narrow an entry to its kind. The wire types `payload` as `unknown`, because Go's
 *  own field is an `any` chosen by `Kind`; every other module asks by kind and gets a
 *  typed payload. */
export function isEntryKind<K extends EntryKind>(e: Entry, kind: K): e is KindedEntry<K> {
  return e.kind === kind;
}

/** This entry's payload when it is of `kind`, else undefined. */
export function payloadOf<K extends EntryKind>(e: Entry, kind: K): EntryPayload[K] | undefined {
  return isEntryKind(e, kind) ? e.payload : undefined;
}

/** The entry as a discriminated union, so a dispatcher's switch is total by type.
 *  The one unchecked kind-to-payload cast in the client's entry path: `decodeEntry`
 *  validates the envelope and leaves `payload` as the server wrote it. */
export function entryUnion(e: Entry): AnyEntry {
  return e as AnyEntry;
}

/** A turn's opening entry. Absent only for a `TurnState` built by a caller that
 *  never saw its `turn_open` — a hole, which the store answers with a range read. */
export function turnOpenOf(t: TurnState): EntryTurnOpen | undefined {
  const first = t.entries[0];
  return first === undefined ? undefined : payloadOf(first, "turn_open");
}

/** A turn's close, from the cached index when the store recorded one and a backward
 *  scan otherwise — a `turn_close` is an ordinary entry and is almost always last. */
export function turnCloseOf(t: TurnState): EntryTurnClose | undefined {
  const at = t.closeAt;
  if (at !== undefined) {
    const e = t.entries[at];
    if (e !== undefined) {
      return payloadOf(e, "turn_close");
    }
  }
  return closeOfBody(t.entries);
}

/** The `turn_close` in a projected body, for a consumer holding a `Turn`. */
export function closeOfBody(body: readonly Entry[]): EntryTurnClose | undefined {
  for (let i = body.length - 1; i >= 0; i--) {
    const e = body[i];
    if (e !== undefined && isEntryKind(e, "turn_close")) {
      return e.payload;
    }
  }
  return undefined;
}

// --- The projection ---

/** The two store fields a projection reads, structurally, so this pure module needs
 *  no `Session`. */
export interface TurnSource {
  readonly turns: ReadonlyMap<string, TurnState>;
  readonly turn_order: readonly string[];
}

export interface Turn {
  /** Reconcile key: the turn's own id, which every one of its entries names. */
  id: string;
  /** The turn's 1-based SESSION-ABSOLUTE ordinal, read off `turn_open.n`. Assigned
   *  by the appender at open time and stored, so a paginated window and the rail's
   *  index agree by construction rather than by a join. */
  n: number;
  /** The reader's prompt. Absent when the turn was not reader-initiated (a
   *  run-completion wake, a wire bracket), in which case the header renders a typed
   *  trigger line instead of putting words in the reader's mouth. */
  trigger: EntryPrompt | undefined;
  /** Everything the trigger caused: the turn's entries after `turn_open` in `seq`
   *  order, with `turn_close` among them as an ordinary entry. */
  body: Entry[];
  /** Each lane's OPEN entry, verbatim from the store. It never reaches the log (no `seq`, no
   *  position), so it sits beside `body` and renders at its lane's tail. Empty on settled turns. */
  openEntries: ReadonlyMap<string, OpenEntry>;
  /** Turn start — the `turn_open`'s own append stamp. Metadata, never compared. */
  ts: number;
  outcome: TurnOutcome;
  /** The NEXT turn's prompt, which a rewind from this turn's footer addresses: KAS drops the given
   *  message and everything after, so keeping turn N means addressing N+1's prompt. Undefined on the
   *  last turn and when the next turn has no prompt. */
  rewindTo: EntryPrompt | undefined;
}

/** Whether a turn is DRAWN: the one predicate, twinned by the server's rail index. A reader-opened
 *  turn, a body entry rendering AT ITS OWN POSITION (`entryRenders`, block-window.ts; not an ACK),
 *  or the CLIENT-ONLY clause: an open lane-`""` entry, so a headerless turn's streaming first text
 *  has a card to mount into. */
export function turnIsDrawn(t: TurnState): boolean {
  const open = turnOpenOf(t);
  if (open === undefined) {
    return false;
  }
  if (open.source === "prompt" || open.source === "local_shell" || open.source === "empty_retry") {
    return true;
  }
  if (t.openEntries.has("")) {
    return true;
  }
  // The plan clause cannot flip the verdict, because the FIRST plan reached is the
  // turn's first and returns true. It is written literally so the predicate reads
  // as its own statement rather than as a simplification of it.
  let plans = 0;
  for (let i = 1; i < t.entries.length; i++) {
    const e = t.entries[i];
    if (e === undefined || (e.lane ?? "") !== "") {
      continue;
    }
    if (
      e.kind === "turn_bind" ||
      e.kind === "tool_result" ||
      e.kind === "turn_close" ||
      e.kind === "reconciled"
    ) {
      continue;
    }
    if (e.kind === "plan") {
      plans++;
      if (plans > 1) {
        continue;
      }
    }
    return true;
  }
  return false;
}

/** The `Turn` one source turn projects to. `rewindTo` is the CALLER's, because the two
 *  projections resolve it from different things: the whole pass reads the next drawn turn
 *  off the list it is building, a keyed read walks the source from the turn's own index. */
function turnOf(
  id: string,
  state: TurnState,
  open: EntryTurnOpen,
  rewindTo: EntryPrompt | undefined,
): Turn {
  const body = state.entries.slice(1);
  return {
    id,
    n: open.n,
    trigger: open.prompt,
    body,
    openEntries: state.openEntries,
    ts: state.entries[0]?.ts ?? 0,
    outcome: closeOfBody(body)?.outcome ?? "running",
    rewindTo,
  };
}

/** Read the resident entry log into the turns the transcript renders, in file order,
 *  dropping the turns `turnIsDrawn` refuses. */
export function projectTurns(src: TurnSource): Turn[] {
  const turns: Turn[] = [];
  for (const id of src.turn_order) {
    const state = src.turns.get(id);
    if (state === undefined) {
      continue;
    }
    const open = turnOpenOf(state);
    if (open === undefined || !turnIsDrawn(state)) {
      continue;
    }
    turns.push(turnOf(id, state, open, undefined));
  }
  for (let i = 0; i < turns.length; i++) {
    const t = turns[i];
    if (t !== undefined) {
      t.rewindTo = turns[i + 1]?.trigger;
    }
  }
  return turns;
}

/** ONE turn of the source, or undefined when absent or not drawn. For a keyed pass that knows which
 *  turn moved, so a per-frame tool update builds no whole projection; `rewindTo` is resolved too. */
export function projectTurn(src: TurnSource, turnID: string): Turn | undefined {
  const at = src.turn_order.indexOf(turnID);
  if (at < 0) {
    return undefined;
  }
  const state = src.turns.get(turnID);
  if (state === undefined) {
    return undefined;
  }
  const open = turnOpenOf(state);
  if (open === undefined || !turnIsDrawn(state)) {
    return undefined;
  }
  return turnOf(turnID, state, open, nextTrigger(src, at));
}

/** The trigger of the first DRAWN turn after `at` in file order — a rewind from turn N's
 *  footer addresses turn N+1's prompt, and an undrawn turn between them is not a turn
 *  the reader can see or revert to. */
function nextTrigger(src: TurnSource, at: number): EntryPrompt | undefined {
  for (let i = at + 1; i < src.turn_order.length; i++) {
    const id = src.turn_order[i];
    const state = id === undefined ? undefined : src.turns.get(id);
    if (state === undefined || !turnIsDrawn(state)) {
      continue;
    }
    return turnOpenOf(state)?.prompt;
  }
  return undefined;
}

// --- The ledger ---

/** Per-turn ledger inputs: what `turn_close` carries plus the turn's tool totals. `changedFiles` is
 *  the close's cumulative map verbatim; one turn has one close, so nothing merges. */
export interface TurnLedger {
  credits: number;
  elapsedMs: number;
  changedFiles: Record<string, FileChange>;
  /** The model(s) that answered, distinct and in emission order: the turn's
   *  `model_switched` entries closed with `turn_close.model`, which records only the
   *  model that finished. Empty renders nothing rather than "unknown". */
  models: string[];
  /** Time INSIDE tool calls: Σ settled `tool_result.duration_ms`. ZERO MEANS NOBODY STAMPED ONE. Calls
   *  overlap, so it is not bounded by `elapsedMs` and a derived model time can go negative. */
  toolMs: number;
  /** How many calls of each kind the turn made. PARTIAL over `ToolKind`: a kind with
   *  no calls has NO ENTRY, which is not an entry reading zero. */
  kindCounts: Partial<Record<ToolKind, number>>;
  /** Delegates the turn dispatched, and Σ their settled durations. Counted off the
   *  invocation's `agent_subtask_id`, so it counts the calls that OPEN a delegate
   *  and not the nested calls the delegate then made. */
  delegateCount: number;
  delegateMs: number;
  /** When the turn began and when its last entry landed, 0 for a turn that sealed
   *  nothing. NOT `startedAt + elapsedMs`: these are append stamps while `elapsed_ms`
   *  is the agent's own duration, and nothing on the wire carries a turn end. */
  startedAt: number;
  endedAt: number;
  /** The wire's stop reason verbatim, and whether the model stopped at a bound. "" and
   *  `false` mean NOTHING STAMPED THEM. A `string`, because the upstream vocabulary is
   *  OPEN: a reader renders it and `outcome` is what it decides on. */
  stopReasonRaw: string;
  truncated: boolean;
  /** How the turn's asks were answered, off each `tool_result.interaction`. Partial
   *  like `kindCounts`: no entry is not an entry reading zero. */
  asks: Partial<Record<AskBucket, number>>;
  /** The rest are `turn_close`'s verbatim; empty or undefined means KAS sent none. */
  requestIds: string[];
  throughput: TurnThroughput | undefined;
  recoveries: string[];
  steering: string[];
  engineErrorClass: string;
}

/** Tool kinds that mean "a command ran". `execute` and `shell` are the two KAS
 *  actually emits for a shell invocation; `command` is in the wire enum and is
 *  counted for completeness rather than because it has been observed. */
export const COMMAND_KINDS: ReadonlySet<ToolKind> = new Set<ToolKind>([
  "execute",
  "shell",
  "command",
]);

export function turnLedger(t: Turn): TurnLedger {
  const close = closeOfBody(t.body);
  const led: TurnLedger = {
    credits: close?.credits ?? 0,
    elapsedMs: close?.elapsed_ms ?? 0,
    changedFiles: close?.changed_files ?? {},
    models: [],
    toolMs: 0,
    kindCounts: {},
    delegateCount: 0,
    delegateMs: 0,
    startedAt: t.ts,
    endedAt: t.body[t.body.length - 1]?.ts ?? 0,
    stopReasonRaw: close?.stop_reason_raw ?? "",
    truncated: close?.truncated ?? false,
    asks: {},
    requestIds: close?.request_ids ?? [],
    throughput: close?.throughput,
    recoveries: close?.recoveries ?? [],
    steering: close?.steering ?? [],
    engineErrorClass: close?.engine_error_class ?? "",
  };
  const delegateCalls = new Set<string>();
  const noteModel = (m: string): void => {
    if (m !== "" && !led.models.includes(m)) {
      led.models.push(m);
    }
  };
  for (const e of t.body) {
    const call = payloadOf(e, "tool_call");
    if (call !== undefined) {
      countCall(led, call);
      if ((call.agent_subtask_id ?? "") !== "") {
        delegateCalls.add(call.id);
      }
      continue;
    }
    const result = payloadOf(e, "tool_result");
    if (result !== undefined) {
      countResult(led, e.id, result, delegateCalls);
      continue;
    }
    const switched = payloadOf(e, "model_switched");
    if (switched !== undefined) {
      noteModel(switched.from);
      noteModel(switched.to);
    }
  }
  noteModel(close?.model ?? "");
  return led;
}

function countCall(led: TurnLedger, call: EntryToolCall): void {
  led.kindCounts[call.kind] = (led.kindCounts[call.kind] ?? 0) + 1;
  if ((call.agent_subtask_id ?? "") !== "") {
    led.delegateCount++;
  }
}

function countResult(
  led: TurnLedger,
  entryID: string,
  result: EntryToolResult,
  delegateCalls: ReadonlySet<string>,
): void {
  const ms = result.duration_ms ?? 0;
  led.toolMs += ms;
  if (result.interaction !== undefined) {
    const bucket = askBucket(result.interaction);
    led.asks[bucket] = (led.asks[bucket] ?? 0) + 1;
  }
  const callID = callIDOfToolResult(entryID);
  if (callID !== null && delegateCalls.has(callID)) {
    led.delegateMs += ms;
  }
}

// --- Per-turn reads the renderer and the rail share ---

/** The stable DOM id a turn anchor targets, computed by more than one surface. A SESSION anchor,
 *  matching the rail's `TurnSummary.n` and the permalink fragment (`route-path.ts` parses
 *  `#turn-<n>`, `route-apply.ts` lands it through the rail's jump). */
export function turnAnchorID(n: number): string {
  return `turn-${String(n)}`;
}

/** Whether folding this turn would HIDE anything. The face shows run cards, the final top-level
 *  prose and a failed turn's error row; a turn with nothing else folds to an identical face, so no
 *  fold is offered. A revert's boundary row is not counted, so it stays on screen. */
export function turnFoldHides(t: Turn): boolean {
  let texts = 0;
  for (const e of t.body) {
    if ((e.lane ?? "") !== "") {
      return true;
    }
    switch (e.kind) {
      case "tool_call":
      case "thinking":
      case "plan":
      case "compaction":
      case "compaction_failed":
      case "safety_blocked":
      case "model_switched":
      case "mode_switched":
        return true;
      case "text":
        if ((payloadOf(e, "text")?.text ?? "").trim() !== "") {
          texts++;
        }
        break;
      default:
        break;
    }
  }
  return texts > 1;
}

/** The turn's final answer: the last non-empty TOP-LEVEL text entry. A collapsed
 *  turn's face renders it in full — the prompt in the header, this in the footer.
 *  Delegate prose (a non-empty lane) is a delegate's report rather than the turn's
 *  answer, so it never qualifies. */
export function turnFaceProse(t: Turn): string {
  for (let i = t.body.length - 1; i >= 0; i--) {
    const e = t.body[i];
    if (e === undefined || (e.lane ?? "") !== "") {
      continue;
    }
    const text = (payloadOf(e, "text")?.text ?? "").trim();
    if (text !== "") {
      return text;
    }
  }
  return "";
}

/** What a turn that did not end cleanly SAYS; "" for clean or running. A CANCELLED turn says
 *  nothing (its footer reads "Cancelled"); tested on the OUTCOME, since `unknown` shares its
 *  severity and must keep speaking. `failure_reason` first, then the outcome's default sentence.
 *  This notice owns the prose, present in both fold states. */
export function turnFailureText(t: Turn): string {
  const severity = severityOf(t.outcome);
  if (severity === "clean" || severity === "running") {
    return "";
  }
  if (t.outcome === "cancelled") {
    return "";
  }
  const reason = (closeOfBody(t.body)?.failure_reason ?? "").trim();
  return reason !== "" ? reason : defaultFailureReason(t.outcome);
}
