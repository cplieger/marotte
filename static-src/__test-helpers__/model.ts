// The typed fixture module: one factory per model type, each taking a partial and
// returning a FULL value with NO CAST anywhere in this file.
//
// That ban is the whole mechanism. A `Session` built with `as unknown as Session` is
// invisible to the type checker, so a field added to the type reddens nothing and
// every hand-built fixture silently carries `undefined` where production reads a
// value — the compile-green run-red class. A factory that RETURNS the type makes the
// same addition one compile error in one file, which is why the drift guard for the
// TYPE half is `npm run typecheck:tests` rather than a test.
import type { RunState } from "../run-store.js";
import type { ServerEvent, Session, TurnState } from "../types.js";
import type { Turn } from "../turns.js";
import type { Entry, EntryToolCall, EntryTurnOpen, OpenEntry, Usage } from "../wire/types.gen.js";

/** What a caller may state about one of a turn's body entries. `turn` and `seq` are
 *  REFUSED rather than merged: they are the turn's own coordinates, so a fixture that
 *  states them can violate the store invariant and assert nothing about production. */
type BodyEntry = Partial<Entry>;

interface TurnOverrides extends Omit<Partial<Turn>, "body"> {
  readonly body?: readonly BodyEntry[];
}

export interface SessionOverrides extends Omit<Partial<Session>, "turns" | "turn_order"> {
  /** The resident turns in any order. The factory keys the map by each turn's own id
   *  and orders `turn_order` by `turn_open.n`, because a map and an array a caller
   *  states separately can disagree about one window. */
  readonly turns?: readonly Turn[];
}

export function makeEntry(over: Partial<Entry> = {}): Entry {
  return { id: "e-1", turn: "t-1", kind: "text", payload: { text: "" }, seq: 0, ts: 0, ...over };
}

export function makeOpenEntry(over: Partial<OpenEntry> = {}): OpenEntry {
  return { turn: "t-1", id: "e-open", kind: "text", text: "", n: 1, ...over };
}

export function makeToolCall(over: Partial<EntryToolCall> = {}): EntryToolCall {
  return { id: "tc-1", title: "Run Command", kind: "execute", status: "completed", ts: 0, ...over };
}

export function makeRunState(over: Partial<RunState> = {}): RunState {
  return { workflowId: "wf-1", status: "running", ...over };
}

export function makeServerEvent(over: Partial<ServerEvent> = {}): ServerEvent {
  return { type: "chat_updated", ...over };
}

/** One turn as the transcript projects it. Each body entry's `turn` and `seq` are
 *  DERIVED from its position, so `entries[i].seq === i` holds by construction over the
 *  `turn_open` this turn's own state prepends (sse-adapter.ts states that invariant:
 *  "the store's invariant, so the length answers it"). */
export function makeTurn(over: TurnOverrides = {}): Turn {
  const { body = [], ...rest } = over;
  const id = rest.id ?? "t-1";
  const entries = body.map((stated, i) => {
    if (stated.seq !== undefined || stated.turn !== undefined) {
      throw new Error(
        `makeTurn: body[${i}] may not state seq or turn; the factory derives both from position`,
      );
    }
    return makeEntry({ ...stated, turn: id, seq: i + 1 });
  });
  return {
    id,
    n: 1,
    trigger: undefined,
    ts: 0,
    outcome: "completed",
    rewindTo: undefined,
    openEntries: new Map(),
    ...rest,
    body: entries,
  };
}

export function makeSession(over: SessionOverrides = {}): Session {
  const { turns = [], ...rest } = over;
  const states = new Map<string, TurnState>();
  for (const t of turns) {
    states.set(t.id, turnStateOf(t));
  }
  const usage: Usage = {
    context_pct: 0,
    context_size: 0,
    credits: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
  return {
    id: "c-1",
    name: "Chat",
    model: "claude-sonnet-5",
    acp_session_id: "s-1",
    current_mode_id: "vibe",
    usage,
    turn_count: turns.length,
    has_more: false,
    thinking: false,
    working_label: "",
    ...rest,
    turns: states,
    turn_order: [...turns].sort((a, b) => a.n - b.n).map((t) => t.id),
  };
}

/** The store's own shape for one projected turn: the `turn_open` at seq 0 and the
 *  body after it, with `closeAt` where a `turn_close` landed. */
function turnStateOf(t: Turn): TurnState {
  const open: EntryTurnOpen = {
    source: t.trigger === undefined ? "event" : "prompt",
    n: t.n,
    ...(t.trigger === undefined ? {} : { prompt: t.trigger }),
  };
  const entries = [
    makeEntry({ id: t.id, turn: t.id, kind: "turn_open", payload: open, seq: 0, ts: t.ts }),
    ...t.body,
  ];
  const state: TurnState = { entries, openEntries: new Map(t.openEntries) };
  const closeAt = entries.findIndex((e) => e.kind === "turn_close");
  if (closeAt >= 0) {
    state.closeAt = closeAt;
  }
  return state;
}
