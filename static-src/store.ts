// Client-side session store. Sessions are an ordered keyed collection; the TURNS a session
// holds are not, because a turn's entries carry per-entry and per-lane streaming signals
// (store-signals.ts) that are finer-grained than a per-chat version signal.

import type {
  Session,
  ChatHeader,
  Entry,
  EntryToolCall,
  EntryToolResult,
  OpenEntry,
  ToolProgressPayload,
  TurnState,
  Usage,
  ToolCall,
  CodeReference,
  PendingSteer,
  SteerOrigin,
  ToolStatus,
} from "./types.js";
// From the generated wire rather than turns.ts, for one spelling of the enum.
import type { RefusalInfo, SteerRowState, TurnOutcome } from "./wire/types.gen.js";
import type { ClassifiedRunStatus } from "./run-status.js";
// turns.ts is a pure leaf that reaches nothing here, so this edge is one-way.
import { payloadOf } from "./turns.js";
import { callIDOfToolResult } from "./entry-ids.js";
import { severityOf } from "./turn-severity.js";
import {
  signal,
  computed,
  createCollection,
  batch,
  touch,
  SignalMap,
  type Signal,
} from "@cplieger/reactive";
import { forgetView, viewStale } from "./view-freshness.js";
import {
  bumpLane,
  clearEntryTextSig,
  clearTurnSigs,
  ensureEntryTextSig,
  peekToolCallSig,
  toolCallSigs,
  toolCallSigKey,
  writeEntryText,
} from "./store-signals.js";
import { noteToolActivity } from "./tool-silence.js";

// --- Messages reactivity: PER-CHAT transcript versions ---
// One version signal per chat, so a background chat's stream cannot repaint the visible
// transcript. Every bump carries a RenderCause, declared at the branch that knows what
// changed; causes merge upward per chat until the flush.
const messagesVersionSigs = new SignalMap<number>();

/** What a version bump was FOR — what the renderer may skip.
 *
 *   - `chunk`: text growth of a MOUNTED block; paint refreshes tail bookkeeping only.
 *   - `tool`: an existing tool call's update; keyed update of the owning turn.
 *   - `fact`: a transcript fact flipped; full projection + reconcile.
 *   - `shape`: the keyed list of turns and entries changed; the full pass.
 *   - `load`: the window was REPLACED by a fetched page; the full pass, plus the one fact
 *     the array cannot state — those rows are a REPLAY, so none of them is an arrival. */
export type RenderCause = "chunk" | "tool" | "fact" | "shape" | "load";

/** `load` outranks `shape`: shape's work is contained in load's, while load's REPLAY
 *  statement is not recoverable from the array, so dropping it would animate every row
 *  of a reopened conversation. */
const CAUSE_RANK: Record<RenderCause, number> = {
  chunk: 0,
  tool: 1,
  fact: 2,
  shape: 3,
  load: 4,
};

/** The per-chat cause accumulator. `turnID` survives only while every merged cause is
 *  `tool` for ONE turn — the keyed-update address; two turns escalate to shape. */
const pendingCause = new Map<string, { cause: RenderCause; turnID?: string }>();

/** The cause the chat's CURRENT version was flushed with. Renderer-only read. */
const flushedCause = new Map<string, { cause: RenderCause; turnID?: string }>();

function mergeCause(chatID: string, cause: RenderCause, turnID?: string): void {
  const cur = pendingCause.get(chatID);
  if (cur === undefined) {
    pendingCause.set(chatID, turnID !== undefined ? { cause, turnID } : { cause });
    return;
  }
  if (cause === "tool" && cur.cause === "tool") {
    // Different turns escalate: one keyed update cannot refresh two of them.
    if (cur.turnID !== turnID) {
      pendingCause.set(chatID, { cause: "shape" });
    }
    return;
  }
  if (CAUSE_RANK[cause] > CAUSE_RANK[cur.cause]) {
    pendingCause.set(chatID, turnID !== undefined ? { cause, turnID } : { cause });
  }
}

/** THIS chat's transcript version. A tracked read subscribes to this chat's transcript
 *  changes and nothing else's. */
export function messagesVersionOf(chatID: string): Signal<number> {
  return messagesVersionSigs.ensure(chatID, 0);
}

/** The cause the current version was bumped for; `paint()` reads it untracked right after
 *  the version. A chat switch or an absent record reads as `shape`, the full pass. */
export function renderCauseOf(chatID: string): { cause: RenderCause; turnID?: string } {
  return flushedCause.get(chatID) ?? { cause: "shape" };
}

/** Flush the accumulator into a version bump. The SYNC path paints the MERGED cause and
 *  clears it, so a pending chunk flush never inherits a later shape. */
function flushCause(chatID: string): void {
  const merged = pendingCause.get(chatID);
  if (merged === undefined) {
    return;
  }
  pendingCause.delete(chatID);
  messagesScheduled.delete(chatID);
  flushedCause.set(chatID, merged);
  const sig = messagesVersionSigs.ensure(chatID, 0);
  sig.value = sig.peek() + 1;
}

/** Bump `chatID`'s transcript version synchronously. List-shape writers use this, in and
 *  out of module; per-delta paths go through `scheduleMessages`. The split is real: a
 *  message arriving must be in the DOM before the frame it was announced in, while a
 *  tick's worth of deltas should collapse into one repaint. */
export function bumpMessages(chatID: string, cause: RenderCause = "shape"): void {
  mergeCause(chatID, cause);
  flushCause(chatID);
}

/** Chats with a bump parked on the next microtask. `removeChat` deletes its id here, so a
 *  pending flush cannot re-mint the version signal it just cleared. */
const messagesScheduled = new Set<string>();

/** Coalesce one chat's per-delta bumps into a single repaint per microtask. */
function scheduleMessages(chatID: string, cause: RenderCause, turnID?: string): void {
  mergeCause(chatID, cause, turnID);
  if (messagesScheduled.has(chatID)) {
    return;
  }
  messagesScheduled.add(chatID);
  queueMicrotask(() => {
    if (messagesScheduled.delete(chatID)) {
      flushCause(chatID);
    }
  });
}

/** The cause an entry's arrival earns, from its LANE alone.
 *
 *  A laned entry other than the invocation renders at no position of its own, and the
 *  invocation's `tool_call` is in the ISSUER's lane, so `""` is exactly the set that needs
 *  a structural pass. */
function entryCause(lane: string | undefined): RenderCause {
  return (lane ?? "") === "" ? "shape" : "chunk";
}

// --- Model context sizes ---
export const MODEL_CONTEXT_SIZES: Record<string, number> = {};

export function parseContextSize(description: string): number | undefined {
  const m = /(\d+)\s*[Kk]\s*context/i.exec(description);
  if (m?.[1] !== undefined) {
    return parseInt(m[1], 10) * 1_000;
  }
  const m2 = /(\d+)\s*[Mm]\s*context/i.exec(description);
  if (m2?.[1] !== undefined) {
    return parseInt(m2[1], 10) * 1_000_000;
  }
  if (/\b1M\b/.test(description)) {
    return 1_000_000;
  }
  return undefined;
}

// --- Session state: the collection is the single source of truth ---
/** Ordered keyed collection of sessions. Structure ops fire `sessions.ids`; per-session
 *  field writes fire `signalFor(id)`. Module-private: consumers go through the typed
 *  accessors below and the `activeSession` computed. */
const sessions = createCollection<Session>((s) => s.id);
/** The active chat id. */
const activeId = signal("");
/** Active session, tracking the active id AND the active session's signal, so subscribers
 *  re-render only when that session or which session is active changes. */
export const activeSession = computed<Session | undefined>(() => {
  touch(sessions.ids); // also re-derive on structural changes (add/remove/setAll)
  const id = activeId.value;
  return id === "" ? undefined : sessions.signalFor(id)?.value;
});

// --- Accessors ---
export function getSessions(): Session[] {
  return sessions.items();
}
export function getActiveId(): string {
  return activeId.peek();
}
/** The active chat id as a TRACKED read; `getActiveId` is the untracked peek. */
export function watchActiveId(): string {
  return activeId.value;
}
export function getActive(): Session | undefined {
  const id = activeId.peek();
  return id === "" ? undefined : sessions.get(id);
}
export function get(id: string): Session | undefined {
  return sessions.get(id);
}

/** One chat's session as a TRACKED read: re-runs on THIS session's field changes and on
 *  the session set's structure changing, never on another session's field churn. */
export function watchSession(id: string): Session | undefined {
  touch(sessions.ids);
  return sessions.signalFor(id)?.value;
}

/** Whether `chatID`'s log already holds a turn this client's prompt opened.
 *
 *  The ACCEPTANCE test for a prompt this client sent: `CmdPrompt` persists and broadcasts
 *  the turn's `turn_open` BEFORE the ACP call and nothing rolls that back, so an echo of
 *  our own message id is proof the server took the prompt whatever the POST went on to
 *  answer.
 *
 *  A lookup for a `turn_open.prompt.id`, which is the ONLY place the client addresses a
 *  prompt by its id — the client-minted message id survives on the log as that field and
 *  nowhere else. */
export function hasMessage(chatID: string, messageID: string): boolean {
  const s = sessions.get(chatID);
  if (s === undefined) {
    return false;
  }
  for (const state of s.turns.values()) {
    const first = state.entries[0];
    if (first !== undefined && payloadOf(first, "turn_open")?.prompt?.id === messageID) {
      return true;
    }
  }
  return false;
}

export function setSessions(v: Session[]): void {
  sessions.setAll(v);
}

export function setActive(id: string): void {
  if (activeId.peek() === id) {
    return;
  }
  // Re-derives `activeSession`, so the messages renderer repaints the new chat's
  // #messages; without it the renderer keeps the previous chat's DOM.
  activeId.value = id;
  stampActivity(id);
}

// --- Eviction: reclaim idle background transcripts ---
// The sweep evicts the message window of a chat nobody can be reading. The session ROW
// survives with its header data, and `residency` is what makes the next activation
// refetch instead of trusting the hole.

/** How often the sweep looks for idle chats. */
export const EVICT_SWEEP_MS = 5 * 60 * 1000;
/** How long a chat must sit without activity before its window is evictable. */
export const EVICT_IDLE_MS = 30 * 60 * 1000;

/** When each chat last did anything a reader could be following. A side table rather than
 *  a Session field so a per-chunk stamp never churns the session signal; a chat with NO
 *  entry is treated as active. */
const lastActivity = new Map<string, number>();

function stampActivity(chatID: string): void {
  if (chatID !== "") {
    lastActivity.set(chatID, Date.now());
  }
}

/** Externally-owned reasons a chat must not be evicted, registered by the composition
 *  root so this module stays a leaf (importing tabs.ts or run-store.ts here would invert
 *  the dependency direction). Nothing registered means no external exemption. */
const evictionExemptions: ((chatID: string) => boolean)[] = [];

/** Register one exemption predicate. Returns its unregister. */
export function registerEvictionExemption(fn: (chatID: string) => boolean): () => void {
  evictionExemptions.push(fn);
  return () => {
    const i = evictionExemptions.indexOf(fn);
    if (i >= 0) {
      evictionExemptions.splice(i, 1);
    }
  };
}

/** Whether the sweep may evict this chat's window. Five exemptions, each alone decisive:
 *  the active chat, a busy chat, a chat with an EXECUTING run, a parked view, and an open
 *  subagent tab projecting the chat. */
function evictable(s: Session, now: number): boolean {
  if (s.turn_order.length === 0) {
    return false; // nothing resident to reclaim
  }
  if (s.id === activeId.peek()) {
    return false; // the active chat is being read
  }
  if (s.thinking) {
    return false; // busy: a live turn is streaming into this window
  }
  const at = lastActivity.get(s.id);
  if (at === undefined || now - at < EVICT_IDLE_MS) {
    return false; // recently active, or never observed — err toward keeping
  }
  return !evictionExemptions.some((fn) => fn(s.id));
}

function sweepEvictions(): void {
  // A hidden tab's clock keeps running but nothing is reclaimed until the reader returns:
  // eviction is for THEIR memory, and a wake-up burst of refetches is the cost avoided.
  // The capability read only skips on a positive "hidden"; a document-less runtime sweeps.
  const hidden = (globalThis as { readonly document?: { readonly hidden?: boolean } }).document
    ?.hidden;
  if (hidden === true) {
    return;
  }
  const now = Date.now();
  for (const s of getSessions()) {
    if (evictable(s, now)) {
      evictChatMessages(s.id);
    }
  }
}

let evictTimer: ReturnType<typeof setInterval> | undefined;

/** Start the sweep. Idempotent; the composition root calls it at boot. */
export function startEvictionSweep(): void {
  evictTimer ??= setInterval(sweepEvictions, EVICT_SWEEP_MS);
}

/** Stop the sweep (tests and symmetric teardown). */
export function stopEvictionSweep(): void {
  if (evictTimer !== undefined) {
    clearInterval(evictTimer);
    evictTimer = undefined;
  }
}

/** Whether older turns exist, for a window whose LEFT EDGE the server has not spoken
 *  about: the record's turn count against what is resident.
 *
 *  Wrong only toward WITHHOLDING a button: a live turn the header has not counted yet
 *  reads equal, and a count ratcheted past a rewind's shrink heals on that rewind's
 *  refetch. */
export function derivedHasMore(turnCount: number, residentCount: number): boolean {
  return turnCount > residentCount;
}

/** Drop every signal a chat's resident turns minted. Covers a chat leaving WHOLE (removal,
 *  eviction), where no reconcile ever runs for background rows; the renderer's own dispose
 *  covers rows that unmount. */
function clearEntrySignals(chatID: string, turns: ReadonlyMap<string, TurnState>): void {
  for (const [turnID, state] of turns) {
    clearTurnSigs(turnID);
    for (const e of state.entries) {
      const call = payloadOf(e, "tool_call");
      if (call !== undefined) {
        toolCallSigs.clear(toolCallSigKey(chatID, call.id));
      }
    }
  }
}

/** Evict a chat's transcript window, keeping the session ROW so header data stays.
 *  Everything keyed by the window goes with it: the turn map and its order, the per-entry
 *  signals, and the chat's version signal. `residency: "evicted"` is what the next
 *  activation keys its refetch on. */
export function evictChatMessages(chatID: string): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  clearEntrySignals(chatID, s.turns);
  clearLiveTurnFacts(chatID);
  s.turns = new Map();
  s.turn_order = [];
  s.has_more = derivedHasMore(s.turn_count, 0);
  s.residency = "evicted";
  messagesScheduled.delete(chatID);
  pendingCause.delete(chatID);
  flushedCause.delete(chatID);
  messagesVersionSigs.clear(chatID);
  // The held version describes the WINDOW, and the window just went. This is what
  // makes the dispatcher's `viewStale`-only gate equivalent to `transcriptStale`, and
  // what keeps the digest from asking about a transcript nobody holds.
  forgetView("chat", chatID);
}

/** Background ingest on an evicted chat leaves it PARTIAL, so only a successful
 *  newest-page load may claim `loaded` again. Called at every site that pushes a NEW
 *  message row into a session. */
function noteResidentMutation(s: Session): void {
  if (s.residency === "evicted") {
    s.residency = "partial";
  }
}

/** The chat kind's refetch gate: a window is trustworthy only if a newest-page load
 *  succeeded and the version map still holds the `chat` subject its answer stamped.
 *
 *  The residency term is NOT redundant: a frame stamped for a chat with no resident
 *  window is applied to nothing, so the transport observes no version for it and the
 *  map stays honest — but a window can also be `partial` (background ingest after an
 *  eviction) while an older claim is still held, and `residency` is the fact about the
 *  WINDOW that `store-load.ts` reads beside the load that may claim `loaded`. */
export function transcriptStale(s: Session): boolean {
  return s.residency !== "loaded" || viewStale("chat", s.id);
}

export function isThinking(id: string): boolean {
  return get(id)?.thinking ?? false;
}

/** Whether a chat holds no conversation at all: nothing on the record and nothing resident.
 *
 *  Both halves are load-bearing, which is why this is one predicate. `turn_count` is the
 *  server's count and is 0 on a chat it has never heard of, while `turn_order` is the
 *  paginated window a `session/load` replay can fill before a header refresh restates the
 *  count. An absent chat is empty, so callers need no second null check. */
export function isEmptyChat(s: Session | undefined): boolean {
  return s === undefined || (s.turn_count === 0 && s.turn_order.length === 0);
}

export function setThinking(id: string, v: boolean): void {
  if (get(id) === undefined) {
    return;
  }
  stampActivity(id);
  sessions.update(id, (s) => {
    const next: Session = {
      ...s,
      thinking: v,
      working_label: v ? s.working_label : "Thinking",
    };
    // A new turn invalidates what the previous one left behind: the agent's declared status,
    // and the server's last liveness statement, which described the PREVIOUS turn — left
    // standing, a `turn_open: false` from before this turn started would make `turnLive` fall
    // back to `thinking` alone.
    if (v) {
      delete next.agent_status;
      delete next.turn_open;
    }
    return next;
  });
  // Transcript fact: `thinking` feeds the live-turn derivation the renderer paints from.
  scheduleMessages(id, "fact");
}

/** Record the server's statement about whether this chat has a turn of its OWN open.
 *  Written from the single-chat GET's `live` field (newest page only) and by the handler
 *  that settles a chat's turn; see `types.ts` `Session.turn_open`. */
export function setTurnOpen(id: string, open: boolean): void {
  const s = get(id);
  if (s === undefined || s.turn_open === open) {
    return; // no-op: do not churn the session signal on a repeated statement
  }
  sessions.update(id, (prev) => ({ ...prev, turn_open: open }));
  // Transcript fact: it feeds `turnLive`, which is the projection's liveness input.
  scheduleMessages(id, "fact");
}

/** Does this row's own state say NOTHING about liveness? A boot-snapshot hint carries
 *  neither input, so its `turn_open: false` is a guess rather than a statement. `loadList`'s
 *  row rebuild is what drops the term (types.ts `Session.provisional`), so a boot whose
 *  chat-list GET fails keeps it and that turn reads `running` until the next list. */
function statesNoLiveness(s: Session): boolean {
  return s.provisional === true;
}

/** Does this chat hold a resident turn with no `turn_close`?
 *
 *  LIVENESS IS THE LOG, so this reads the append the appender made and nothing this client
 *  remembers. `closeAt` is written by `appendEntry` at the index the close landed on, which
 *  is why the question costs one field read per resident turn rather than a scan. */
function hasOpenTurn(s: Session): boolean {
  for (const state of s.turns.values()) {
    if (state.closeAt === undefined) {
      return true;
    }
  }
  return false;
}

/** Is a turn RUNNING on this chat?
 *
 *  DERIVED, NEVER LATCHED. A delegate's entries are entries of the parent's own turn, so
 *  that turn carries no `turn_close` until the whole thing ends and this cannot read
 *  anything but live for the duration.
 *
 *  `thinking` IS NOT AN INPUT: it may never outrank an open turn, so folding it in here
 *  would put a latch back under the derivation. The two remaining terms are the cases the
 *  log cannot answer: `turn_open` is the server's own `live` for a chat whose window is not
 *  resident, and a row that states NEITHER (a boot-snapshot hint) is read as live, because
 *  guessing the other way derives a TERMINAL verdict over a turn the server is still
 *  streaming. */
export function turnLive(s: Session): boolean {
  return hasOpenTurn(s) || s.turn_open === true || statesNoLiveness(s);
}

/** Which chat's log holds this turn, or "" when no resident window does. The digest's
 *  `live_turn` subject is keyed by TURN id (one stamp per open turn), so
 *  the chat it belongs to is this side's to resolve. */
export function chatHoldingTurn(turnID: string): string {
  for (const s of sessions.items()) {
    if (s.turns.has(turnID)) {
      return s.id;
    }
  }
  return "";
}

/** Grade a persisted turn outcome for the dot: `done`, `failed`, or "" for one that grades
 *  neither. `tabStatusFor` is the reader, and the header's `last_turn_outcome` is the one
 *  field it reads, so the verdict has one source.
 *
 *  THE HOLLOW RING MEANS THE CHAT HAS NOT INITIATED, which decides the floor: a chat that
 *  has run a turn may never paint `idle`, so only a chat with no turn and a record with no
 *  outcome reach it. `stopped` therefore latches DONE, because `done` is the transport's
 *  verdict that a turn FINISHED, never the agent's claim that it succeeded. */
export function outcomeLatch(outcome: TurnOutcome | undefined): "done" | "failed" | "" {
  // ABSENCE is answered here and never by `severityOf`, which grades OUTCOMES: its default
  // arm reads an unrecognised value as `stopped` so a value the wire adds later cannot read
  // as a turn that worked. "The wire said something I cannot grade" is a turn that ran; "the
  // record carries no outcome" is the one case the hollow ring is still correct for.
  if (outcome === undefined) {
    return "";
  }
  switch (severityOf(outcome)) {
    case "clean":
    case "stopped":
      return "done";
    case "broken":
      return "failed";
    case "running":
      return "";
  }
}

/** Derive the chat tab's activity-dot state. ONE rule, shared by the store effect and the
 *  turn-lifecycle handlers. Order is precedence, and `pendingAsk` is a parameter because
 *  `decision-dock.ts` imports this module.
 *
 *  THE VERDICT HAS ONE SOURCE AND LIVENESS IS THE LOG. `failed` and `done` read the same
 *  field — `last_turn_outcome`, the header's statement about the newest FINISHED turn — and
 *  BOTH are gated on liveness, because that field is rewritten only at a `turn_close`: it
 *  still describes the previous turn for the whole of the next one, so an ungated `failed`
 *  paints a chat red for the duration of a turn that is running fine. Ahead of the gate the
 *  order is precedence as stated: `input` (a question outranks activity, and the two COEXIST
 *  since an ask arrives mid-turn) → `failed` → `working` → `waiting` (a `waiting_on_user`
 *  status declared during a running turn is the same false verdict in yellow) → `done` →
 *  `idle`, which MEANS THE CHAT HAS NOT INITIATED, so a chat tab always shows a dot. */
export type TabDotState = "" | "input" | "failed" | "working" | "waiting" | "done" | "idle";

export function tabStatusFor(s: Session | undefined, pendingAsk = false): TabDotState {
  if (s === undefined) {
    return "";
  }
  if (pendingAsk) {
    return "input";
  }
  const live = turnLive(s);
  const latch = outcomeLatch(s.last_turn_outcome);
  if (!live && latch === "failed") {
    return "failed";
  }
  if (live) {
    return "working";
  }
  if (s.agent_status === "waiting_on_user") {
    return "waiting";
  }
  return latch === "done" ? "done" : "idle";
}

/** The pause classes the dot vocabulary distinguishes. A classified value rather than the
 *  raw `pauseReason`, so this module never learns KAS's pause sentences or its node
 *  signals: `run-store.ts` owns that rule and hands the answer over already decided. */
export type RunPauseClass = "" | "need_input";

/** The same dot vocabulary for a workflow RUN, which has no `Session` behind it. Its own
 *  function rather than a branch in `tabStatusFor` because the two inputs share not one
 *  field; what they share is the OUTPUT vocabulary and precedence.
 *
 *  `paused` is `waiting` — stopped, not finished — EXCEPT for a step parked on a person,
 *  which is `input`, the park being the half that survives a client which never received
 *  the card. `cancelled` is `done`: the reader asked for the stop. `pause` is read only in
 *  the `paused` arm, so a stale reason on a finished run cannot paint it yellow. */
export function runStatusFor(
  status: ClassifiedRunStatus | undefined,
  pendingAsk = false,
  pause: RunPauseClass = "",
): TabDotState {
  if (pendingAsk) {
    return "input";
  }
  switch (status) {
    case undefined:
      return "";
    case "running":
      return "working";
    case "paused":
      return pause === "need_input" ? "input" : "waiting";
    case "failed":
    case "aborted":
      return "failed";
    case "completed":
    case "cancelled":
      return "done";
    case "unknown":
      return "";
  }
}

/** The status a delegate's surfaces paint, folding in its chat's OWN turn liveness: a
 *  delegate's whole state is its invocation call's `ToolStatus`, so a call whose
 *  `tool_result` never arrived reads `in_progress` forever. A turn that ends settles every
 *  unsettled call as `aborted` (`Turn.Close`, `EntryLog.synthesizeCloseLocked`), so once
 *  the chat holds no live turn an `in_progress` call folds onto `aborted` — the server's
 *  own word, already rendered on all three surfaces (`delegate_dot.json`'s `stale` row).
 *  Only `in_progress` folds, and a chat this client holds no row for answers live:
 *  liveness is not knowledge. */
export function delegateStatusFor(status: ToolStatus, turnLive: boolean): ToolStatus {
  return status === "in_progress" && !turnLive ? "aborted" : status;
}

/** Does `chatID`'s own turn read live to this client? The TRACKED read of the second input
 *  above, so an effect that folds it repaints when a `busy_chats` reconcile moves it. */
export function chatTurnLive(chatID: string): boolean {
  const s = watchSession(chatID);
  return s === undefined || turnLive(s);
}

/** The same dot vocabulary for a SUBAGENT, whose tab is a sub-tab under the chat that
 *  dispatched it. A delegate's whole state is its INVOCATION TOOL CALL's `ToolStatus`, a
 *  generated closed union, so the arms below are exhaustive and need no `default`.
 *
 *  `undefined` means THIS CLIENT HOLDS NO INVOCATION, which is a resident-window fact
 *  rather than anything about the delegate, and is answered FIRST so no arm below has to
 *  consider absence. Nothing maps to `idle`: a delegate someone opened a tab for has run.
 *
 *  `turnLive` is the chat's own liveness, defaulting to the answer that claims nothing: the
 *  fold above is what a stale spinner needs, and a caller with no session to read must not
 *  invent one. */
export function subagentStatusFor(status: ToolStatus | undefined, turnLive = true): TabDotState {
  if (status === undefined) {
    return "";
  }
  switch (delegateStatusFor(status, turnLive)) {
    case "pending":
    case "in_progress":
      return "working";
    // `aborted` joins `completed`: a delegate the reader stopped is over, not broken —
    // the same fold `runStatusFor` already makes for a cancelled run two arms above.
    case "completed":
    case "aborted":
      return "done";
    case "failed":
      return "failed";
  }
}

/** Record the agent-declared activity status for a chat (chat_status SSE, from the KAS
 *  focus_update channel). An empty string clears the field. */
export function setAgentStatus(id: string, status: string): void {
  const s = get(id);
  if (s === undefined) {
    return;
  }
  if ((s.agent_status ?? "") === status) {
    return; // no-op: don't churn the session signal
  }
  sessions.update(id, (prev) => {
    const next: Session = { ...prev };
    if (status === "") {
      delete next.agent_status;
    } else {
      next.agent_status = status;
    }
    return next;
  });
}

export function setWorkingLabel(id: string, label: string): void {
  if (get(id)?.working_label === label) {
    return;
  }
  sessions.update(id, (s) => ({ ...s, working_label: label }));
  scheduleMessages(id, "fact"); // the resume control's fallback label
}

// --- Mid-turn steers (the dock's waiting rows) ---
// `session.steers` is what the agent has NOT read: the bottom dock's rows. A row that
// has left the dock is a `steer` ENTRY of the turn it landed in,
// so this field is the dock and nothing else. The client writes INTENT (`recordSteerSent`,
// un-written by `forgetSteer`) and every other mutator here adopts a server FACT.

/** The steer id KAS will return for a message id. `internal/marotte/commands.go` documents
 *  the convention (`"steer-" + messageID`) and `internal/command/steer_test.go` pins it.
 *  Deriving it is what lets the optimistic row be reconciled by a plain id match: the
 *  POST's reply carries the authoritative id, but `transportAction` discards the body. */
export function steerIDFor(messageID: string): string {
  return `steer-${messageID}`;
}

/** Number of steers waiting for the agent, confirmed or still in flight. */
export function steerCount(id: string): number {
  return get(id)?.steers?.length ?? 0;
}

/** Record a steer this client has just POSTed, before any server frame. `pending` says the
 *  id is DERIVED rather than confirmed, which is what the dock reads to withhold Edit and
 *  Discard — there is no server-side id to clear yet. Rolled back by `forgetSteer`. */
export function recordSteerSent(id: string, messageID: string, text: string): void {
  const s = get(id);
  if (s === undefined || messageID === "") {
    return;
  }
  // `user` is a FACT here, not a guess: this row is this device's own POST.
  const entry: PendingSteer = {
    id: steerIDFor(messageID),
    text,
    origin: "user",
    pending: true,
  };
  const existing = s.steers ?? [];
  // Idempotent by id: submit.ts reuses one message id when retrying a failed attempt, and
  // that retry must refresh the row rather than add a second.
  const at = existing.findIndex((e) => e.id === entry.id);
  const next = at >= 0 ? existing.map((e, i) => (i === at ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact"); // the dock row is transcript-adjacent state
}

/** Un-draw one steer row; the rollback half of `recordSteerSent`. Removes by id and leaves
 *  everything else alone, so a 409 cannot take a sibling still waiting with it. */
export function forgetSteer(id: string, steerID: string): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  const rest = s.steers.filter((e) => e.id !== steerID);
  if (rest.length === s.steers.length) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, rest));
  scheduleMessages(id, "fact");
}

/** Adopt one `steer_queued` frame. A ROW frame upserts the row its id names, exactly
 *  one row per message: the id matches (adopt the text and state, clear `pending`); no
 *  id match but the OLDEST pending row carries the same text (adopt the server's id, the
 *  fallback if the prefix convention drifts); neither (append it — another device, or
 *  this one before a reload). A BATCH frame (`replaces`) adds no row: it records the id
 *  KAS now holds the named rows under, so a resubmit repaints nothing. The log is
 *  checked FIRST because a reconnect replays the frame for a steer the agent has since
 *  read, which would otherwise put a delivered message back in the dock. */
export function recordSteerQueued(
  id: string,
  steer: {
    id: string;
    text: string;
    origin: SteerOrigin;
    replaces?: readonly string[] | undefined;
    /** Absent reads as `queued`, the plain row a direct POST reply describes. */
    state?: SteerRowState | undefined;
  },
): void {
  const s = get(id);
  if (s === undefined || steer.id === "") {
    return;
  }
  if (steer.replaces !== undefined && steer.replaces.length > 0) {
    adoptSteerBatch(id, s, steer.id, steer.replaces);
    return;
  }
  if (steer.state === "removed" || holdsSteerEntry(s, steer.id)) {
    forgetSteer(id, steer.id);
    return;
  }
  const existing = s.steers ?? [];
  const at = existing.findIndex((e) => e.id === steer.id);
  const adoptAt =
    at >= 0 ? at : existing.findIndex((e) => e.pending === true && e.text === steer.text);
  const prev = adoptAt >= 0 ? existing[adoptAt] : undefined;
  // The frame's origin wins in every branch: the server resolved it against the ledger of
  // what it sent, where the optimistic row's `user` was this device's claim.
  const entry: PendingSteer = {
    id: steer.id,
    text: steer.text,
    origin: steer.origin,
    ...(prev?.kas !== undefined && { kas: prev.kas }),
    ...(prev?.compacted === true && { compacted: true as const }),
    ...(steer.state === "unsent" && { unsent: true as const }),
  };
  const next =
    adoptAt >= 0 ? existing.map((e, i) => (i === adoptAt ? entry : e)) : [...existing, entry];
  sessions.update(id, (cur) => ({ ...cur, steers: next }));
  scheduleMessages(id, "fact");
}

/** Record that KAS holds `keys` under `batchID`. The rows keep their element and their
 *  words; only the id their read will arrive under moves. A batch the log already shows
 *  read retires its members instead. */
function adoptSteerBatch(
  chatID: string,
  s: Session,
  batchID: string,
  keys: readonly string[],
): void {
  if (s.steers === undefined) {
    return;
  }
  const members = new Set(keys);
  if (holdsSteerEntry(s, batchID)) {
    const rest = s.steers.filter((e) => !members.has(e.id));
    if (rest.length !== s.steers.length) {
      sessions.update(chatID, (cur) => withSteers(cur, rest));
      scheduleMessages(chatID, "fact");
    }
    return;
  }
  if (!s.steers.some((e) => members.has(e.id) && e.kas !== batchID)) {
    return;
  }
  const next = s.steers.map((e) => (members.has(e.id) ? { ...e, kas: batchID } : e));
  sessions.update(chatID, (cur) => withSteers(cur, next));
}

/** Remove every CONFIRMED waiting steer, returning a snapshot to restore from. The
 *  optimistic half of `chat.clear_steers`; the transcript's record is the `steer` entry
 *  the server appends for each cleared id, so nothing here writes one. A `pending` entry
 *  STAYS — it is not in KAS's buffer yet, so removing it locally would hide a message
 *  still on its way.
 *  Returns the array as it was, so the rollback restores the exact order. */
export function dropConfirmedSteers(id: string): readonly PendingSteer[] {
  const s = get(id);
  if (s?.steers === undefined) {
    return [];
  }
  const prev = s.steers;
  const rest = prev.filter((e) => e.pending === true);
  if (rest.length === prev.length) {
    return [];
  }
  sessions.update(id, (cur) => withSteers(cur, rest));
  scheduleMessages(id, "fact");
  return prev;
}

/** Mark every row the dock holds NOW as sent across a compaction (types.ts
 *  `PendingSteer.compacted`). Arrival order is the whole rule: the `compaction` entry
 *  landing is what says these rows were queued before it, so a row that arrives afterwards
 *  is never marked and no timestamp is compared. Idempotent — a second compaction over the
 *  same rows changes nothing. */
export function markSteersCompacted(id: string): void {
  const s = get(id);
  if (s?.steers === undefined || s.steers.every((e) => e.compacted === true)) {
    return;
  }
  const marked = s.steers.map((e) => ({ ...e, compacted: true as const }));
  sessions.update(id, (cur) => withSteers(cur, marked));
  scheduleMessages(id, "fact");
}

/** Put a `dropConfirmedSteers` snapshot back. The rollback half. */
export function restoreSteers(id: string, prev: readonly PendingSteer[]): void {
  if (prev.length === 0 || get(id) === undefined) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, prev));
  scheduleMessages(id, "fact");
}

/** Forget the dock's contents WITHOUT promoting them. The `BUS_RECONCILE` path: a gap
 *  means the frames that resolved these steers may be among the ones lost, so promoting
 *  them would assert "the agent never read this" on no evidence. Existing marks stay. */
export function forgetSteers(id: string): void {
  const s = get(id);
  if (s?.steers === undefined) {
    return;
  }
  sessions.update(id, (cur) => withSteers(cur, []));
  scheduleMessages(id, "fact");
}

/** Write `steers` onto a session, DELETING the field when the list is empty, so a session
 *  compares equal to one that never had steers: pending-steers.ts's `computed` dedups by
 *  value and an empty array would repaint on every clear. */
function withSteers(s: Session, steers: readonly PendingSteer[]): Session {
  const copy = { ...s };
  if (steers.length === 0) {
    delete copy.steers;
  } else {
    copy.steers = [...steers];
  }
  return copy;
}

// --- SSE-driven mutations ---
export function upsertHeader(h: ChatHeader): void {
  const existing = get(h.id);
  if (existing !== undefined) {
    // Server-authoritative re-sync of the header fields; messages, thinking and
    // working_label are client/stream-owned.
    sessions.update(h.id, (s) => {
      const next: Session = {
        ...s,
        name: h.name,
        // `model` is the ONE header field the CLIENT can legitimately be ahead of the server on:
        // a pick before the first prompt applies locally and rides that prompt, so until then the
        // record genuinely has no model. `Model` is `omitempty` on the wire, which makes "not set"
        // and "cleared" the same frame, and taking it as a clear is what reset the pill to "auto".
        // Absent means no news.
        model: h.model !== undefined && h.model !== "" ? h.model : s.model,
        acp_session_id: h.acp_session_id ?? "",
        current_mode_id: h.current_mode_id ?? "",
        supervised_mode: h.supervised_mode ?? false,
        effort: h.effort ?? "",
        // Absent means the server has no session catalog to report (a chat with no bridge, or a
        // header built before session/new answered), NOT that the tiers went away.
        effort_levels: h.effort_levels ?? s.effort_levels ?? [],
        effort_active: h.effort_active ?? s.effort_active ?? "",
        usage: h.usage,
        // The header owns the count and a header read never shrinks it: a live turn the
        // appender has not closed yet is not counted, and a rewind's shrink heals on that
        // rewind's own refetch.
        turn_count: Math.max(s.turn_count, h.turn_count),
        // The model badge's ONE input, and a header read is its authority in both
        // directions: the apply at a turn's close arrives with the field empty, which is
        // what clears the pending pick on every device.
        pending_model: h.pending_model ?? "",
      };
      if (h.compaction_watermark !== undefined) {
        next.compaction_watermark = h.compaction_watermark;
      } else {
        delete next.compaction_watermark;
      }
      // A header read is the AUTHORITY for this field, so an absent outcome is a CLEAR and
      // not "no news" — the OPPOSITE of `model` and `effort_levels` in the same literal,
      // which deliberately fall back to `s`. Hence the explicit delete (`compaction_watermark`'s
      // own shape above): an `exactOptionalPropertyTypes` spread of `undefined` is a type
      // error, and a conditional spread would carry the stale value forward.
      if (h.last_turn_outcome !== undefined) {
        next.last_turn_outcome = h.last_turn_outcome;
      } else {
        delete next.last_turn_outcome;
      }
      next.updated_at = h.updated_at;
      return next;
    });
    return;
  }
  const s: Session = {
    id: h.id,
    name: h.name,
    model: h.model ?? "",
    acp_session_id: h.acp_session_id ?? "",
    current_mode_id: h.current_mode_id ?? "",
    supervised_mode: h.supervised_mode ?? false,
    effort: h.effort ?? "",
    effort_levels: h.effort_levels ?? [],
    effort_active: h.effort_active ?? "",
    usage: h.usage,
    turn_count: h.turn_count,
    pending_model: h.pending_model ?? "",
    turns: new Map(),
    turn_order: [],
    // A header carries no window, so this is the DERIVATION and not an answer.
    has_more: derivedHasMore(h.turn_count, 0),
    thinking: false,
    working_label: "Thinking",
    // The row is REBUILT from the header rather than spread from `s`, so a conditional spread
    // IS a replace here: nothing carries over because nothing is there.
    ...(h.last_turn_outcome !== undefined && { last_turn_outcome: h.last_turn_outcome }),
    updated_at: h.updated_at,
  };
  if (h.compaction_watermark !== undefined) {
    s.compaction_watermark = h.compaction_watermark;
  }
  sessions.prepend([s]);
}

export function setCurrentMode(id: string, modeID: string): void {
  const s = get(id);
  if (s === undefined || s.current_mode_id === modeID) {
    return;
  }
  sessions.update(id, (cur) => ({ ...cur, current_mode_id: modeID }));
}

export function removeChat(id: string): void {
  const doomed = get(id);
  if (doomed === undefined) {
    return;
  }
  const wasActive = activeId.peek() === id;
  const order = sessions.ids.peek();
  // Both reactive writes below feed the `activeSession` computed. Batch them so subscribers
  // re-derive ONCE; otherwise removing the active chat double-fires it and the messages
  // renderer flashes a transient teardown of the new chat's DOM.
  batch(() => {
    sessions.remove(id);
    clearLiveTurnFacts(id);
    // Every signal the chat's window minted: the renderer's own dispose only reaches rows a
    // reconcile removes, and a background chat's never see one.
    clearEntrySignals(id, doomed.turns);
    lastActivity.delete(id);
    // A flush parked on the next microtask must not re-mint the signal cleared here.
    messagesScheduled.delete(id);
    pendingCause.delete(id);
    flushedCause.delete(id);
    messagesVersionSigs.clear(id);
    forgetView("chat", id);
    if (wasActive) {
      const remaining = order.filter((x) => x !== id);
      activeId.value = remaining[0] ?? "";
    }
  });
}

/** Re-insert a previously-removed session at `atIndex`, or at the head. For optimistic
 *  action rollbacks. Idempotent. */
export function reinsertSession(session: Session, atIndex?: number): void {
  if (sessions.has(session.id)) {
    return;
  }
  const order = [...sessions.items()];
  const target = atIndex !== undefined ? Math.max(0, Math.min(atIndex, order.length)) : 0;
  order.splice(target, 0, session);
  sessions.setAll(order);
}

// --- The entry log: the five operations over `session.turns` ---
// Position is `seq` and nothing re-anchors, re-indexes or reorders: a value that does not
// fit is a HOLE, and the repair is the one range read of section 6.4 rather than a local
// fix-up. COUNTS, never lengths — the server's text is UTF-8 bytes and this side's is a
// UTF-16 string, so no length is compared across the wire.

/** The range read a hole needs, injected because this module never fetches: `store-load.ts`
 *  owns the read and this module owns the detection. */
let repairTurn: ((chatID: string, turnID: string, afterSeq?: number) => void) | undefined;

export function registerTurnRepair(
  fn: (chatID: string, turnID: string, afterSeq?: number) => void,
): void {
  repairTurn = fn;
}

/** The other half of that split, for the reads already OUT: a revert drops turns, and a
 *  read issued before the frame landed would seat one of them again. The reads in flight
 *  are `store-load.ts`'s, so the abort is injected exactly as the read is. */
let abortRevertReads: ((chatID: string, turnIDs: readonly string[]) => void) | undefined;

export function registerRevertReadAbort(
  fn: (chatID: string, turnIDs: readonly string[]) => void,
): void {
  abortRevertReads = fn;
}

/** Record that this chat's window has a gap in it. `residency` is what makes
 *  `transcriptStale` true, so the next activation refetches instead of trusting a window
 *  with a known gap, and it is what `applyTurnRange` restores once every repair has landed.
 *
 *  Exported because a gap has two detectors: an entry event this module folds, and a PAGE
 *  whose own walk found a `seq` that is not next (`store-load.ts` `buildPageTurns`). The
 *  rule about what a gap does to `residency` stays here, at ONE owner. */
export function markWindowStale(chatID: string): void {
  const s = get(chatID);
  if (s?.residency === "loaded") {
    s.residency = "partial";
  }
}

/** Mark the gap and ask for the range read that closes it. `afterSeq` is omitted for a turn
 *  the store does not hold at all, which asks for the whole turn. */
function markHole(chatID: string, turnID: string, afterSeq?: number): void {
  markWindowStale(chatID);
  repairTurn?.(chatID, turnID, afterSeq);
}

/** Bump the narrowest pass that can show an entry event, and choose SYNCHRONOUS versus
 *  microtask off the same lane test: a shape change must be in the DOM in the frame it was
 *  announced in, while a laned entry's growth may coalesce. */
function publishEntry(chatID: string, cause: RenderCause): void {
  if (cause === "shape") {
    bumpMessages(chatID, cause);
  } else {
    scheduleMessages(chatID, cause);
  }
}

/** Create the turn a `turn_opened` announced, with its `turn_open` as `entries[0]`, and
 *  append its id to the chat's turn order. Idempotent by turn id: a replayed frame names a
 *  turn already held and changes nothing. */
export function openTurn(chatID: string, entry: Entry): void {
  const s = get(chatID);
  if (s === undefined || s.turns.has(entry.turn)) {
    return;
  }
  stampActivity(chatID);
  noteResidentMutation(s);
  s.turns.set(entry.turn, { entries: [entry], openEntries: new Map() });
  s.turn_order.push(entry.turn);
  // `turn_open.n` IS the session-absolute count of turns, so the newest one restates the
  // header's `turn_count`; `max` because an older page's turns arrive with lower ordinals.
  s.turn_count = Math.max(s.turn_count, payloadOf(entry, "turn_open")?.n ?? 0);
  s.has_more = derivedHasMore(s.turn_count, s.turn_order.length);
  bumpMessages(chatID, "shape");
}

/** A held turn's session-absolute ordinal, or `undefined` for a turn whose `turn_open`
 *  the window does not hold — which the drop below reads as "cannot be placed against the
 *  cut" and therefore leaves alone. */
function heldOrdinal(state: TurnState | undefined): number | undefined {
  const open = state?.entries[0];
  if (open?.kind !== "turn_open") {
    return undefined;
  }
  return payloadOf(open, "turn_open")?.n;
}

/** A revert's two writes, both keyed on `from_n`: drop every held turn the window took,
 *  and write the surviving high-water as the count.
 *
 *  THE DROP IS BY ORDINAL, NEVER BY POSITION IN `turn_order`. The store holds the N newest
 *  turns, so a client can legitimately hold turns 6 and 7 and not the 5 a revert names:
 *  a position rule finds nothing at or after `from` and drops nothing, leaving two reverted
 *  turns rendered under a count that agrees with neither. The carrier is kept — it is the
 *  turn the record LIVES in and is below the cut by construction.
 *
 *  THE COUNT IS WRITTEN, not merged. `upsertHeader` merges a header's count as
 *  `Math.max`, so a SHRINK is discarded there — correct for a header, which can read low
 *  while a replay is still filling the window, and wrong for a revert, which STATES what
 *  left. `from_n - 1` is the floor (every reverted turn's `n` is at or above `from_n`,
 *  every survivor's below it) and the `max` covers the no-survivor case, where the floor
 *  is 0 while the minted carrier is a real surviving turn numbered 1 that DRAWS. */
function applyRevert(chatID: string, s: Session, entry: Entry): void {
  const fromN = payloadOf(entry, "turn_revert")?.from_n;
  if (fromN === undefined) {
    return;
  }
  const dropped = new Map<string, TurnState>();
  for (const id of s.turn_order) {
    if (id === entry.turn) {
      continue;
    }
    const state = s.turns.get(id);
    const n = heldOrdinal(state);
    if (state !== undefined && n !== undefined && n >= fromN) {
      dropped.set(id, state);
    }
  }
  if (dropped.size > 0) {
    clearEntrySignals(chatID, dropped);
    for (const id of dropped.keys()) {
      s.turns.delete(id);
    }
    s.turn_order = s.turn_order.filter((id) => !dropped.has(id));
    // The window's own record of what the revert took, read by the range read's seat as
    // the abort's belt. Extended rather than replaced: a second rewind's window does not
    // un-revert the first one's.
    s.reverted = new Set([...(s.reverted ?? []), ...dropped.keys()]);
  }
  // Outside the drop: a page read in flight can carry reverted turns this window never held.
  abortRevertReads?.(chatID, [...dropped.keys()]);
  const carrier = heldOrdinal(s.turns.get(entry.turn)) ?? 0;
  s.turn_count = Math.max(fromN - 1, carrier);
  s.has_more = derivedHasMore(s.turn_count, s.turn_order.length);
}

/** Append one sealed entry at the position its `seq` claims.
 *
 *  THE SEQ IS THE CHECK: an entry whose `seq` is exactly `entries.length` is appended, an
 *  entry naming a turn the store does not hold is a hole, and any other `seq` is a hole
 *  too — except a REDELIVERY, which is provable rather than guessed: a sealed entry is
 *  immutable, so a `seq` the store already holds under the SAME id is the frame arriving
 *  twice and is dropped. Without that arm a reconnect's replay would ask for a range read
 *  per frame. */
export function appendEntry(chatID: string, entry: Entry): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const state = s.turns.get(entry.turn);
  if (state === undefined) {
    if (entry.kind === "turn_revert") {
      // A `turn_revert` whose CARRIER the store does not hold is NOT a hole. Both of the
      // handler's writes read `from_n` alone, so neither needs the carrier, and the
      // carrier arrives with the next window read — where a hole here would fire a range
      // read for a turn the client is about to page in anyway.
      applyRevert(chatID, s, entry);
      publishEntry(chatID, "shape");
      return;
    }
    markHole(chatID, entry.turn);
    return;
  }
  if (entry.seq !== state.entries.length) {
    if (state.entries[entry.seq]?.id === entry.id) {
      return;
    }
    markHole(chatID, entry.turn, state.entries.length - 1);
    return;
  }
  stampActivity(chatID);
  noteResidentMutation(s);
  state.entries.push(entry);
  if (entry.kind === "turn_close") {
    // The one cached lookup: `closeOf` and `hasOpenTurn` read this rather than scanning.
    state.closeAt = entry.seq;
  }
  if (entry.kind === "steer") {
    // The dock rows leave HERE, in the same store update that seats the note, so no render
    // frame exists in which the steer is in neither place.
    retireSteerRows(chatID, entry);
  }
  if (entry.kind === "tool_result") {
    publishSettledCall(chatID, state, entry);
  }
  if (entry.kind === "turn_revert") {
    // AFTER the push: the record is the carrier's own entry at its own `seq`, and the
    // drop is what the record MEANS rather than part of seating it.
    applyRevert(chatID, s, entry);
  }
  bumpLane(entry.turn, entry.lane ?? "");
  publishEntry(chatID, entryCause(entry.lane));
}

/** Publish a `tool_result`'s settled value at the card its `tool_call` mounted. A mounted
 *  card's ONE refresh channel is its own signal, so without this it keeps painting the
 *  state the create was built with — the spinner — for the life of the document. The
 *  pairing is the entry id (`entry-ids.ts`), so there is no join table to consult. */
function publishSettledCall(chatID: string, state: TurnState, entry: Entry): void {
  const callID = callIDOfToolResult(entry.id);
  const result = payloadOf(entry, "tool_result");
  if (callID === null || result === undefined) {
    return;
  }
  const call = heldToolCall(state, callID);
  if (call === undefined) {
    return;
  }
  republishToolCall(chatID, entry.turn, settledToolCall(call, result));
}

/** Remove the dock rows a `steer` entry settles, without a bump of its own: the caller
 *  publishes. The entry's id is a row's own, or the id KAS held a batch of rows under;
 *  `resends` names the rows its words carried. */
function retireSteerRows(chatID: string, entry: Entry): void {
  const s = get(chatID);
  if (s?.steers === undefined) {
    return;
  }
  const rest = s.steers.filter((e) => !steerEntrySettles(entry, e.id, e.kas));
  if (rest.length !== s.steers.length) {
    sessions.update(chatID, (cur) => withSteers(cur, rest));
  }
}

function steerEntrySettles(entry: Entry, rowID: string, kas: string | undefined): boolean {
  if (entry.id === rowID || (kas !== undefined && entry.id === kas)) {
    return true;
  }
  return payloadOf(entry, "steer")?.resends?.includes(rowID) === true;
}

/** Whether a resident turn already holds a `steer` entry settling this id. What a reconnect
 *  needs: the replay carries the frame for a steer the agent has since READ, and without
 *  this the dock would take a delivered message back. */
function holdsSteerEntry(s: Session, steerID: string): boolean {
  for (const state of s.turns.values()) {
    for (const e of state.entries) {
      if (e.kind === "steer" && steerEntrySettles(e, steerID, undefined)) {
        return true;
      }
    }
  }
  return false;
}

/** Store the open entry a lane is coalescing into, with the `n` it arrived with — the
 *  count of deltas already folded into `open.text`. One open entry per lane, so a second
 *  open in the same lane replaces the first. Mints the streaming signal, which is what a
 *  surface that is not the entry's own bubble follows. */
export function openEntry(chatID: string, open: OpenEntry): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const state = s.turns.get(open.turn);
  if (state === undefined) {
    markHole(chatID, open.turn);
    return;
  }
  const lane = open.lane ?? "";
  stampActivity(chatID);
  state.openEntries.set(lane, { ...open, lane });
  ensureEntryTextSig(open.turn, open.id, open.text);
  bumpLane(open.turn, lane);
  publishEntry(chatID, entryCause(lane));
}

/** Extend a lane's open entry by one delta. `n` is the running count AFTER the delta, so
 *  the only admissible value is `open.n + 1`; anything else, and any frame naming an entry
 *  this lane is not coalescing, is the same hole. */
export function applyDelta(
  chatID: string,
  turn: string,
  entryID: string,
  lane: string,
  n: number,
  delta: string,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const state = s.turns.get(turn);
  if (state === undefined) {
    markHole(chatID, turn);
    return;
  }
  const open = state.openEntries.get(lane);
  if (open?.id !== entryID || n !== open.n + 1) {
    markHole(chatID, turn, state.entries.length - 1);
    return;
  }
  stampActivity(chatID);
  const full = open.text + delta;
  state.openEntries.set(lane, { ...open, text: full, n });
  // `painted` reports that a signal CELL exists, not that a surface subscribed: `openEntry`
  // mints one for every live stream. It answers false only for an open tail seated from a
  // page GET that nothing has mounted, where the full pass is what puts the text on screen.
  const painted = writeEntryText(turn, entryID, full, delta);
  bumpLane(turn, lane);
  publishEntry(chatID, painted ? "chunk" : entryCause(lane));
}

/** Seal a lane's open entry: build the `Entry` from the open state and hand it to
 *  `appendEntry`, so the hole check applies to the seal's own `seq`. `n` must equal the
 *  count this side holds, or the two disagree about how much text the entry carries. */
export function sealEntry(
  chatID: string,
  turn: string,
  entryID: string,
  lane: string,
  seq: number,
  ts: number,
  n: number,
): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  const state = s.turns.get(turn);
  if (state === undefined) {
    markHole(chatID, turn);
    return;
  }
  const open = state.openEntries.get(lane);
  if (open?.id !== entryID || n !== open.n) {
    markHole(chatID, turn, state.entries.length - 1);
    return;
  }
  state.openEntries.delete(lane);
  clearEntryTextSig(turn, entryID);
  const entry: Entry = {
    id: open.id,
    turn,
    lane,
    kind: open.kind,
    // `text` and `thinking` carry the same one-field payload, so the kind on the envelope
    // is what tells them apart.
    payload: { text: open.text },
    seq,
    ts,
  };
  appendEntry(chatID, entry);
}

export function setSupervisedMode(chatID: string, enabled: boolean): void {
  const s = get(chatID);
  if (s === undefined || s.supervised_mode === enabled) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, supervised_mode: enabled }));
}

/** Set the chat's reasoning-effort level. Per-chat like model, mode and supervised, so a
 *  tab switch reads the new chat's level instead of carrying the previous one over. */
export function setEffort(chatID: string, effort: string): void {
  const s = get(chatID);
  if (s === undefined || (s.effort ?? "") === effort) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, effort }));
}

/** Set session model and notify subscribers. `usage.context_size` derives from the model,
 *  so it is refreshed in the same update — callers must never mutate `session.usage` on a
 *  stale reference, since sessions.update replaces the object. */
export function setModel(chatID: string, model: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({
    ...cur,
    model,
    usage: { ...cur.usage, context_size: contextSizeFor(model) },
  }));
}

/** Set session name and notify subscribers. */
export function setName(chatID: string, name: string): void {
  if (!sessions.has(chatID)) {
    return;
  }
  sessions.update(chatID, (cur) => ({ ...cur, name }));
}

/** Return the current index of a session in the list, or -1. */
export function indexOfSession(id: string): number {
  return sessions.ids.peek().indexOf(id);
}

/** The live `code_references` replace, per (chat, turn). LIVE-ONLY: the durable value is
 *  `turn_close.code_references`, so this holds the footnote's answer for a turn that has
 *  not closed yet and is dropped with the window. The server sends the full deduped list
 *  each time, so it replaces rather than appends. */
const liveCodeRefs = new Map<string, Map<string, readonly CodeReference[]>>();

export function setCodeReferences(chatID: string, turnID: string, refs: CodeReference[]): void {
  const s = get(chatID);
  if (!s?.turns.has(turnID)) {
    return;
  }
  const byTurn = liveCodeRefs.get(chatID) ?? new Map<string, readonly CodeReference[]>();
  byTurn.set(turnID, refs);
  liveCodeRefs.set(chatID, byTurn);
  scheduleMessages(chatID, "fact");
}

/** The live attributions for a turn, or undefined when none arrived. The renderer prefers
 *  the turn's own `turn_close.code_references` once it exists. */
export function codeReferencesFor(
  chatID: string,
  turnID: string,
): readonly CodeReference[] | undefined {
  return liveCodeRefs.get(chatID)?.get(turnID);
}

/** The live refusal per (chat, turn), stamped from the ONE tagged `entry_sealed` of a turn —
 *  the frame the refusal branch publishes, since a tagged chunk opens no entry and SEALS the
 *  lane's open one. LIVE-ONLY for `liveCodeRefs`' reason: the durable value is
 *  `turn_close.refusal`, so this carries the callout for a turn that has not closed yet and
 *  the close's own aggregate takes over. Stamped ONCE per turn — the wire sends it at most
 *  once, and a second stamp would repaint the turn for a fact already on screen. */
const liveRefusals = new Map<string, Map<string, RefusalInfo>>();

export function setLiveRefusal(chatID: string, turnID: string, refusal: RefusalInfo): void {
  const s = get(chatID);
  if (!s?.turns.has(turnID)) {
    return;
  }
  const byTurn = liveRefusals.get(chatID) ?? new Map<string, RefusalInfo>();
  if (byTurn.has(turnID)) {
    return;
  }
  byTurn.set(turnID, refusal);
  liveRefusals.set(chatID, byTurn);
  // `shape`, not `fact`: the callout is an element that has to MOUNT, and the turn's keyed
  // state is what the reconcile reads to mount it.
  bumpMessages(chatID, "shape");
}

/** The live refusal for a turn, or undefined when none arrived. The renderer prefers the
 *  turn's own `turn_close.refusal` once it exists. */
export function liveRefusalFor(chatID: string, turnID: string): RefusalInfo | undefined {
  return liveRefusals.get(chatID)?.get(turnID);
}

/** Drop a chat's LIVE turn facts — the attributions and the refusal, both of which the
 *  turn's own `turn_close` carries durably (window evicted, chat removed). */
export function clearLiveTurnFacts(chatID: string): void {
  liveCodeRefs.delete(chatID);
  liveRefusals.delete(chatID);
}

/** Fold one `tool_progress` frame onto the call a card is showing.
 *
 *  The frame is LIVE-ONLY and its durable twin is the `tool_result` entry, so this writes
 *  no entry: it folds onto the SIGNAL cell the card subscribes to, seeded from the
 *  `tool_call` entry's own payload. A frame for a call this window does not hold reports
 *  `undefined`, which is the handler's own signal to ask for the turn's range. */
export function applyToolProgress(
  chatID: string,
  turnID: string,
  p: ToolProgressPayload,
): ToolCall | undefined {
  const s = get(chatID);
  if (s === undefined) {
    return undefined;
  }
  const state = s.turns.get(turnID);
  if (state === undefined) {
    return undefined;
  }
  const prev = peekToolCallSig(chatID, p.tool_call_id) ?? heldToolCall(state, p.tool_call_id);
  if (prev === undefined) {
    return undefined;
  }
  const next = foldToolCallDelta(prev, p);
  republishToolCall(chatID, turnID, next);
  // The SILENCE marker's one input on this path: the wall clock at which this client
  // applied a frame for that call. A display value in `elapsed_ms`'s class — no entry
  // `ts` is read here and nothing about the fold's ORDER depends on it.
  noteToolActivity(chatID, p.tool_call_id);
  return next;
}

/** The `tool_call` entry's own payload as a `ToolCall`, for a card whose signal has not
 *  been minted yet. */
function heldToolCall(state: TurnState, toolCallID: string): ToolCall | undefined {
  for (const e of state.entries) {
    const call = payloadOf(e, "tool_call");
    if (call?.id === toolCallID) {
      return call;
    }
  }
  return undefined;
}

/** Fold one delta onto a held tool call, returning the new value. A fresh object
 *  rather than a mutation, because the card's signal dedups by identity.
 *
 *  EXPORTED only for the cross-language contract test, which drives this against the
 *  same fixture the Go builder is driven against so the two folds cannot drift.
 *  Every production caller reaches it through `applyToolProgress`. */
export function foldToolCallDelta(prev: ToolCall, d: ToolProgressPayload): ToolCall {
  // `output_replace` is the only case where accumulated output legitimately shrinks
  // or is rewritten. The flag is read FIRST and is authoritative on its own, because
  // the delta is `omitempty` on the Go side: a replace-to-empty travels as
  // `{output_replace: true}` with no delta at all, which read the other way round
  // means "unchanged" here and `""` to the server.
  const output =
    d.output_replace === true
      ? (d.output_delta ?? "")
      : d.output_delta === undefined
        ? prev.output
        : (prev.output ?? "") + d.output_delta;
  const diffs =
    d.diffs_appended === undefined ? prev.diffs : [...(prev.diffs ?? []), ...d.diffs_appended];
  return {
    ...prev,
    ...(d.title !== undefined && { title: d.title }),
    ...(d.kind !== undefined && { kind: d.kind }),
    ...(d.status !== undefined && { status: d.status }),
    ...(output !== undefined && { output }),
    ...(d.output_spans !== undefined && { output_spans: d.output_spans }),
    ...(diffs !== undefined && { diffs }),
    ...(d.locations !== undefined && { locations: d.locations }),
    ...(d.duration_ms !== undefined && { duration_ms: d.duration_ms }),
    ...(d.terminal_id !== undefined && { terminal_id: d.terminal_id }),
    ...(d.agent_subtask_id !== undefined && { agent_subtask_id: d.agent_subtask_id }),
    ...(d.workflow_id !== undefined && { workflow_id: d.workflow_id }),
    ...(d.checkpoint !== undefined && { checkpoint: d.checkpoint }),
    ...(d.disclosed !== undefined && { disclosed: d.disclosed }),
    ...(d.denial !== undefined && { denial: d.denial }),
    // ONE-WAY: the wire only ever sends `true`, so an absent field means unchanged
    // rather than false and the mark is never cleared by a later frame.
    ...(d.declined === true && { declined: true }),
  };
}

/** Publish a FETCHED window's tool calls at the cards already mounted for them.
 *
 *  A mounted tool card has exactly one refresh channel — the per-call signal effect
 *  `messages-tools.ts` installs at mount — so a wholesale window replacement
 *  (`store-load.ts`) leaves every mounted card showing whatever it was built from unless
 *  the fetched value is pushed at it. That is what made the boot snapshot's deliberately
 *  truncated output permanent: the record is a paint-time hint the server's answer is
 *  meant to supersede.
 *
 *  Through `republishToolCall` rather than `ensureToolCallSig` on purpose: that is already
 *  the one place a call is published to a card, it picks the repaint cause, and its
 *  `get`-not-`ensure` shape means it mints no signal for a card nobody mounted —
 *  `ensureToolCallSig` would also IGNORE its `initial` argument for an existing signal,
 *  which is the trap that makes creation the wrong verb here.
 *
 *  A call the card is ALREADY showing is skipped, and that guard is load-bearing rather
 *  than a saving: the card's own effect guards on OBJECT IDENTITY
 *  (`messages-tools.ts` `mountToolCallCard`), and this publishes the freshly decoded
 *  object, which is never the one the card mounted with. So without it every mounted card
 *  repaints on every load — and `applyOutputUpdate` re-windows the output and removes and
 *  re-creates `.tool-output-reveal`, so a reader who expanded a long output with
 *  "Show N more lines" would lose that expansion, and any selection inside the `<pre>`
 *  with it. On the boot path, where the card really is showing the snapshot's truncated
 *  copy, the compare misses and nothing changes.
 *
 *  The DURABLE value of a tool call is its `tool_result` entry, so a settled call is
 *  published from that rather than from the `tool_call` the turn opened with. */
export function republishWindowToolCalls(chatID: string, turnIDs: readonly string[]): void {
  const s = get(chatID);
  if (s === undefined) {
    return;
  }
  for (const turnID of turnIDs) {
    const state = s.turns.get(turnID);
    if (state === undefined) {
      continue;
    }
    const settled = new Map<string, EntryToolResult>();
    for (const e of state.entries) {
      const result = payloadOf(e, "tool_result");
      const callID = result === undefined ? null : callIDOfToolResult(e.id);
      if (result !== undefined && callID !== null) {
        settled.set(callID, result);
      }
    }
    for (const e of state.entries) {
      const call = payloadOf(e, "tool_call");
      if (call === undefined) {
        continue;
      }
      const next = settledToolCall(call, settled.get(e.id));
      const shown = peekToolCallSig(chatID, next.id);
      if (shown !== undefined && paintsTheSame(shown, next)) {
        continue;
      }
      republishToolCall(chatID, turnID, next);
    }
  }
}

/** A tool call as the card should paint it: the call as created, with its `tool_result`'s
 *  settled value over the top. The result carries no id of its own — the pairing is the
 *  entry id (`entry-ids.ts`) — so this is the one place the two halves are joined. */
export function settledToolCall(
  call: EntryToolCall,
  result: EntryToolResult | undefined,
): ToolCall {
  if (result === undefined) {
    return call;
  }
  return {
    ...call,
    // `status` is required on the result, so it always wins: the initial value on the call
    // is a starting state rather than a verdict.
    status: result.status,
    ...(result.title !== undefined && { title: result.title }),
    ...(result.kind !== undefined && { kind: result.kind }),
    ...(result.output !== undefined && { output: result.output }),
    ...(result.output_spans !== undefined && { output_spans: result.output_spans }),
    ...(result.diffs !== undefined && { diffs: result.diffs }),
    ...(result.locations !== undefined && { locations: result.locations }),
    ...(result.duration_ms !== undefined && { duration_ms: result.duration_ms }),
    ...(result.terminal_id !== undefined && { terminal_id: result.terminal_id }),
    ...(result.workflow_id !== undefined && { workflow_id: result.workflow_id }),
    ...(result.checkpoint !== undefined && { checkpoint: result.checkpoint }),
    ...(result.disclosed !== undefined && { disclosed: result.disclosed }),
    ...(result.denial !== undefined && { denial: result.denial }),
    ...(result.declined === true && { declined: true }),
  };
}

/** Whether a mounted card built from `shown` would paint `next` identically: the fields
 *  `applyToolCallUpdate` READS, which is a narrower set than a `ToolCall`'s own — the
 *  title, the output and the spans that style it, the diffs, the status with its duration,
 *  and the terminal id `linkTerminal` claims. A field it never reads (`kind`, `locations`,
 *  `checkpoint`, the subtask and workflow ids) cannot move the card, so a difference there
 *  is not a reason to repaint one. */
function paintsTheSame(shown: ToolCall, next: ToolCall): boolean {
  return (
    shown.title === next.title &&
    shown.status === next.status &&
    shown.duration_ms === next.duration_ms &&
    shown.output === next.output &&
    shown.terminal_id === next.terminal_id &&
    sameSpans(shown.output_spans, next.output_spans) &&
    sameDiffs(shown.diffs, next.diffs)
  );
}

/** Element-wise equality for two style-span lists. Both sides are freshly decoded
 *  objects on the fetch path, so identity answers nothing and the fields are the
 *  comparison. */
function sameSpans(a: ToolCall["output_spans"], b: ToolCall["output_spans"]): boolean {
  const x = a ?? [];
  const y = b ?? [];
  return (
    x.length === y.length &&
    x.every((s, i) => {
      const t = y[i];
      if (t === undefined) {
        return false;
      }
      return (
        s.start === t.start &&
        s.end === t.end &&
        s.fg === t.fg &&
        s.bg === t.bg &&
        s.attrs === t.attrs
      );
    })
  );
}

/** Element-wise equality for two diff lists. */
function sameDiffs(a: ToolCall["diffs"], b: ToolCall["diffs"]): boolean {
  const x = a ?? [];
  const y = b ?? [];
  return (
    x.length === y.length &&
    x.every((d, i) => {
      const e = y[i];
      if (e === undefined) {
        return false;
      }
      return d.path === e.path && d.old_text === e.old_text && d.new_text === e.new_text;
    })
  );
}

/** Push a tool call's new value at whatever is rendering it, and schedule the
 *  narrowest pass that can show it. Shared by the live progress path and the fetched
 *  window so the two cannot disagree about which pass a tool update needs. */
function republishToolCall(chatID: string, turnID: string, call: ToolCall): void {
  const sig = toolCallSigs.get(toolCallSigKey(chatID, call.id));
  if (sig !== undefined) {
    sig.value = call;
    // The card's own effect repaints it; the tool paint refreshes the owning turn's keyed
    // state only — never a projection, never a mount.
    scheduleMessages(chatID, "tool", turnID);
  } else {
    // Signal-absent fallback: nothing is mounted, so the full pass puts the update on
    // screen — unless nothing is MEANT to be, which is a laned call.
    scheduleMessages(chatID, entryCause(call.agent_subtask_id));
  }
}

// --- Utilities ---
export function contextSizeFor(modelID: string): number {
  return MODEL_CONTEXT_SIZES[modelID] ?? 0;
}

export function defaultUsage(): Usage {
  return {
    context_pct: 0,
    context_size: 0,
    credits: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
}
