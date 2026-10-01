// Turn-body composition: the one dispatcher turning a turn's `body` entries into
// DOM, composed from the fundamentals/ primitives.

import type {
  Entry,
  EntrySteer,
  EntryToolResult,
  OpenEntry,
  PlanEntry,
  ToolCall,
  ToolKind,
  ToolStatus,
  PlanStatus,
  FileChange,
} from "./types.js";
import type { Turn, TurnOutcome } from "./turns.js";
import { closeOfBody, entryUnion, payloadOf } from "./turns.js";
import type { EventEntry } from "./messages-events.js";
import {
  effectiveRunID,
  entryAt,
  entryRenders,
  firstPlanSeq,
  proseRunAt,
  runResults,
  turnOrdinalOf,
  turnSpan,
  type EntryRange,
  type RunResults,
} from "./block-window.js";
import { steerAckID, toolResultID } from "./entry-ids.js";
import { effect, el } from "@cplieger/reactive";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import {
  chatTurnLive,
  delegateStatusFor,
  get,
  getActiveId,
  settledToolCall,
  turnLive,
} from "./store.js";
import {
  clearEntryTextSig,
  ensureEntryTextSig,
  ensureToolCallSig,
  peekToolCallSig,
  toolCallSigKey,
  toolCallSigs,
} from "./store-signals.js";
import { recordEntryHeight, recordRowHeight } from "./block-heights.js";
import { lineDelta } from "./diff.js";
import { isInternalToolTitle, isSubagentInvocation, isToolActive } from "./tool-schema.js";
import type { TurnSummaryData } from "./fundamentals/turn-footer.js";
import {
  buildAssistantBubble,
  type AssistantBubble,
  type AssistantBubbleOpts,
} from "./fundamentals/text-bubble.js";
import { buildReasoning, type ReasoningView } from "./fundamentals/reasoning.js";
import { buildSteerNote, type SteerNoteData } from "./fundamentals/steer-note.js";
import {
  buildSubagentCard,
  buildSubagentContainer,
  type SubagentCard,
  type SubagentContainer,
  type SubagentOpener,
} from "./fundamentals/subagent-block.js";
import { bindSubagentTail } from "./subagent-tail.js";
import { buildTodoList, updateTodoList, type TodoItem } from "./fundamentals/todo.js";
import { mountToolCallCard, disposeToolSlot } from "./messages-tools.js";
import { planElement, updatePlanElement } from "./messages-plan.js";
import {
  buildToolGroupShell,
  groupBody,
  refreshGroupHeader,
  autoCollapseGroup,
  setGroupSuperseded,
} from "./tool-group.js";

// Re-exported for messages.ts to inject into messages-tools' status-flip path.
export { refreshGroupHeader };
import { iconForSubagent, subagentLabel, subagentName } from "./roles.js";
import { buildRunCard, type RunCardView, type RunDisclosure } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { buildPath } from "./route-path.js";

// Callbacks injected by messages.ts, which owns avatar markup and the
// streaming-effect registry.

interface BlockCbs {
  /** Register a cleanup disposed on turn finalize / turn unmount. */
  pushStreamingEffect(turnID: string, cleanup: () => void): void;
  /** Register a cleanup disposed when this ENTRY leaves the window. */
  pushEntryEffect(turnID: string, seq: number, cleanup: () => void): void;
  /** Run the cleanups for the entries a window drop removed: the drop's half of
   *  `pushEntryEffect`'s contract. */
  disposeEntryEffects(turnID: string, seqs: Iterable<number>): void;
  /** Build an avatar row for a top-level assistant bubble. */
  makeRow(): HTMLDivElement;
  /** The event row one of the four event kinds draws. `messages.ts` owns that markup, so
   *  the dispatcher asks for it rather than declaring a fifth vocabulary here. NON-NULLABLE,
   *  like every other mounter, because `indexGroups` posts a run break at every rendering
   *  entry's `seq` and a seam answering null would price a break against a row nothing
   *  draws. Its argument is the NARROWED union, so the four kinds are the seam's own type
   *  rather than a claim the dispatcher makes about which entries it passes. */
  makeEvent(entry: EventEntry): HTMLElement;
}

let cbs: BlockCbs = {
  pushStreamingEffect: () => {
    /* until init */
  },
  pushEntryEffect: () => {
    /* until init */
  },
  disposeEntryEffects: () => {
    /* until init */
  },
  makeRow: () => el("div") as HTMLDivElement,
  makeEvent: () => el("div"),
};

/** Install the callbacks. WHOLESALE, so a caller supplying four of the five members
 *  leaves the fifth at its default with no diagnostic anywhere — the type is what keeps
 *  the two in step, and an added member has to reach every caller in the same change. */
export function initBlockRenderer(c: BlockCbs): void {
  cbs = c;
}

// The live-anchor registry: which bubble Following pins to. Last-writer-wins, so
// the newest top-level bubble wins. Delegate-hosted bubbles never register — their
// box is collapsed with `height: 0` + `overflow: hidden`, so it reports offsets the
// reader cannot see.

let liveAnchor: { renderID: string; el: HTMLElement } | null = null;

/** The element Following pins to, or null for the document bottom.
 *
 *  Self-healing: a mid-turn rebuild replaces a turn's render, so the slot can point at
 *  a detached element. When the anchor is not its own render's current top-level live
 *  bubble, re-derive from the render map. */
export function getLiveAnchor(): HTMLElement | null {
  if (liveAnchor !== null && renders.get(liveAnchor.renderID)?.topLiveEl !== liveAnchor.el) {
    liveAnchor = null;
    rescanLiveAnchor();
  }
  return liveAnchor?.el ?? null;
}

/** Point the slot at the newest still-live top-level bubble of the active chat, or
 *  leave it null. Registration order is mount order, so the last match is newest. */
function rescanLiveAnchor(): void {
  const activeChat = getActiveId();
  for (const [id, st] of renders) {
    if (!st.detached && st.chatID === activeChat && st.topLiveEl !== null) {
      liveAnchor = { renderID: id, el: st.topLiveEl };
    }
  }
}

/** Identity-guarded clear: only the registered element's own seal clears the slot.
 *  Only the ACTIVE chat's renders are rescan candidates — a pin into a parked chat
 *  would strand Following on an element the reader cannot see. */
function clearLiveAnchor(el: HTMLElement): void {
  if (liveAnchor?.el !== el) {
    return;
  }
  liveAnchor = null;
  rescanLiveAnchor();
}

// run id → the `tool_call` ENTRY that hosts that run's card, derived per pass by
// `block-window.ts` `runCardOwners` over every resident turn and read by `ownsRunCard`.
// DERIVED rather than a live registry: a registry the paint writes answers "no host yet,
// I host" to two mentions in one turn, posting the box twice against one store cell.
// An ABSENT run falls toward the pre-owner behaviour — every mention builds the card — so
// a render the paint installed no map for still draws one.

let runCardOwnerEntries: ReadonlyMap<string, string> = new Map();

export function setRunCardOwners(owners: ReadonlyMap<string, string>): void {
  runCardOwnerEntries = owners;
}

/** Whether THIS `tool_call` entry is the one that hosts `runID`'s card. A DETACHED render
 *  is exempt: the subagent page is its own surface with its own lane, and the transcript's
 *  owner names an entry that page may not even hold. */
function ownsRunCard(st: TurnRender, runID: string, entryID: string): boolean {
  if (st.detached) {
    return true;
  }
  const owner = runCardOwnerEntries.get(runID);
  return owner === undefined || owner === entryID;
}

// Which collapsible containers are open, so a re-mount restores what the reader chose.
// A DELEGATE has no key, because its card is not a disclosure. Detached renders register
// nothing: a disclosure the reader set is a property of the transcript.

const openContainers = new Map<string, boolean>();

function setContainerOpen(key: string, open: boolean): void {
  openContainers.set(key, open);
}

/** What the READER last left `key` at, or undefined when they have not decided — which is
 *  NOT the same as closed. The creation site owns the default, and under the
 *  newest-element policy that default is `st.boxExpanded`'s verdict.
 *
 *  Only a reader's own toggle writes here: every view gates its registry write on the
 *  disclosure's `source === "user"`, so an auto collapse and a failure auto-open leave no
 *  entry. An entry IS the latch — a decided box keeps its auto path off for life. */
function containerOpen(key: string): boolean | undefined {
  return openContainers.get(key);
}

/** Carry a dropped tool card's own disclosure into the registry, so the re-mount
 *  restores what the reader chose. `aria-expanded` on `.tool-disclosure` is the only
 *  record a card's details were opened — the boxes above have keys, a card had
 *  nothing. */
function recordDisclosure(el: HTMLElement, toolID: string): void {
  const toggle = el.querySelector<HTMLElement>(".tool-disclosure");
  if (toolID !== "" && toggle !== null) {
    setContainerOpen(`tool:${toolID}`, toggle.getAttribute("aria-expanded") === "true");
  }
}

/** Drop a render's container keys. */
function pruneContainers(st: TurnRender): void {
  if (st.detached) {
    return; // never registered
  }
  for (const tc of st.calls) {
    openContainers.delete(`tool:${tc.call.id}`);
  }
  for (const pipelineID of st.pipelines.keys()) {
    openContainers.delete(`pipe:${pipelineID}`);
  }
  for (const runID of st.runs.keys()) {
    openContainers.delete(`run:${runID}`);
  }
}

/** One `tool_call` entry of the turn, joined with its result: the pair every gate here
 *  asks about. Hoisted once per pass, because five readers walk it. */
interface TurnToolCall {
  /** The `tool_call` ENTRY's id, which is what `runCardOwners` names an owner by. */
  entryID: string;
  seq: number;
  lane: string;
  /** The call as a card paints it, `store.ts`'s own join of the create and its result. */
  call: ToolCall;
  /** The call's EFFECTIVE workflow id: its own when the wire carries one, else its
   *  `tool_result`'s. What makes a call the run's card, resolved once per pass. */
  runID: string;
}

/** A mounted PROSE RUN: the consecutive `text` entries of one lane as ONE
 *  `.msg-row > .message.assistant` with ONE markdown stream, so an entry seal inside it is
 *  invisible and a list started in one entry continues in the next. `seqs` is
 *  the concatenation order, which is also the offset table: a member's source offset is the
 *  prefix sum over it, resolved at query time rather than stored per entry. */
interface ProseRun {
  /** The run's FIRST member `seq`, which is also the key `st.proseRuns` holds it under, or
   *  `-1` while the run holds only an open entry and so has no position yet. Carried on the
   *  run so `st.runHead` and `st.proseRuns` cannot key on two different notions of it. */
  head: number;
  row: HTMLElement;
  bubble: AssistantBubble;
  seqs: number[];
  /** The lane's OPEN entry id when this run holds it as its tail member, else null. Its
   *  text is the run's tail and is not in `seqs`, because an open entry has no `seq`. */
  openID: string | null;
}

/** The lane's OPEN entry as this render mounts it: the id its streaming signal is keyed
 *  by, the surface carrying its text, and that subscription's disposer. At most one per
 *  render — a render draws one lane, and a lane holds at most one open entry. */
type OpenTail =
  | { kind: "text"; id: string; run: ProseRun; stop: () => void }
  | { kind: "thinking"; id: string; view: ReasoningView; stop: () => void };

// Per-turn render state

interface TurnRender {
  /** The chat whose turns this render belongs to. Carried, not read from the store:
   *  the subagent page renders one delegate of whatever chat its tab names, so the
   *  active chat would key its cards under a chat that never writes them. */
  chatID: string;
  /** The `.turn-body` container holding this turn's rendered entries. Handed in rather
   *  than created: one turn is one card, so the card already owns its body. */
  bodyEl: HTMLElement;
  /** The entry `seq` range this render currently holds. Widened on both edges by
   *  `renderRange`; `dropEntryRange` is the only writer that narrows it. */
  window: EntryRange;
  /** entry `seq` → the element whose removal drops that entry, and the ONLY
   *  entry→element mapping: a `seq` is unique per TURN and not per DOM subtree,
   *  because `runCardFor` routes a later turn's mentions into the first's card. */
  entryEls: Map<number, HTMLElement>;
  /** entry `seq` → a call that brings that entry's DOM up to a full text. Wraps
   *  the handle's `setText` in an arrow rather than storing the method itself:
   *  both handles keep their state in a closure and never read `this`, but a
   *  detached method reference is a shape the linter rightly refuses to take on
   *  trust. Read by `syncMountedText`. */
  entryText: Map<number, (full: string) => void>;
  /** The run's FIRST `seq` → its one mounted bubble. */
  proseRuns: Map<number, ProseRun>;
  /** Member `seq` → its run's first `seq`. What tells a shared element from an entry's
   *  own: every member maps to ONE row in `entryEls`, so a drop, a text fold and a height
   *  key each have to know whether this `seq` speaks for the row. */
  runHead: Map<number, number>;
  /** The mounted OPEN entry of this render's lane, or null. It is what streams: the store
   *  retires a sealed entry's signal, so nothing else has a cell to subscribe to. */
  openTail: OpenTail | null;
  /** subtask id → its delegate card. Minted by the delegate's INVOCATION block,
   *  which is the only block of a delegate's this render draws. */
  subagents: Map<string, SubagentCard>;
  /** orchestrate tool-call id → the PIPELINE container that call opened. Keyed by the
   *  invocation because the orchestrate call has no subtask of its own. */
  pipelines: Map<string, SubagentContainer>;
  /** stage subtask id → the orchestrate tool-call id that owns it. Built by
   *  `indexPipelines`; a stage's TEXT block carries only the bare subtask uuid. */
  stagePipeline: Map<string, string>;
  /** orchestrate tool-call id → its stage subtask ids, in first-seen order. */
  pipelineStages: Map<string, string[]>;
  /** orchestrate tool-call id → its declared `stages` length, `0` when absent or
   *  malformed. A FLOOR rather than the answer — see `pipelineStageCount`. */
  pipelineDeclared: Map<string, number>;
  /** workflow id → the run card THIS render hosts. Keyed by run: the owner rule names
   *  ONE `tool_call` entry per run, so a render holds at most one card for it. */
  runs: Map<string, RunCardView>;
  /** workflow id → the ARMED render effect's disposer, absent while the card is
   *  suspended. Outside `disposers` because pause must stop the effect and release
   *  the clock WITHOUT running the card's final dispose. */
  runEffects: Map<string, () => void>;
  /** Cleanups that outlive the TURN, bucketed by entry `seq` with `-1` for the render's own:
   *  a window drop drains only the buckets it removed, `disposeAll` drains them all. Separate
   *  from `pushStreamingEffect`, disposed at turn end — right for a caret, wrong for a run card
   *  whose run carries on for minutes after `run_workflow` returns. */
  disposers: Map<number, (() => void)[]>;
  /** Whether this render lives OUTSIDE the transcript (the subagent page). Two
   *  consequences: turn-lifetime cleanups go into `disposers`, because messages.ts
   *  has never heard of this render; and a cleanup must not clear a shared per-tool
   *  signal, which the transcript may still be reading. */
  detached: boolean;
  /** Every mounted bubble handle (for finalize end()). */
  bubbles: AssistantBubble[];
  /** The one bubble currently carrying the streaming caret, or null. `bubbles` is
   *  append-only with no notion of its tail, so without this pointer nothing can
   *  seal the block that WAS the tail when a new one opens. */
  liveBubble: AssistantBubble | null;
  /** This turn's TOP-LEVEL streaming bubble root, or null: the live-anchor registry's
   *  per-render half. Delegate-hosted bubbles never set it. */
  topLiveEl: HTMLElement | null;
  /** Every mounted reasoning handle, for the turn-end seal. A different question
   *  from `openReasoning`, which answers which one is still open. */
  reasonings: ReasoningView[];
  /** A container's genuinely-live TRAILING reasoning trace. A trace the store
   *  already has a successor for is sealed at its own mount, so head insertion
   *  never reaches this map. */
  openReasoning: Map<HTMLElement, ReasoningView>;
  /** container key → its tool groups, keyed by the STORE run each one opened at, so
   *  which group a card joins is a function of the store instead of mount order.
   *  Outer key is the container's KEY, a bijection with its element inside one
   *  render. */
  toolGroups: Map<string, Map<number, HTMLDivElement>>;
  /** Container key (`sub:`/`pipe:`/`run:`) → the entry `seq` that ESTABLISHES it, which is
   *  where its box belongs however far down the range first reached it. `indexGroups`
   *  writes it, `placeContainer` reads it, and both mean the same `seq`. */
  containerAt: Map<string, number>;
  /** Container key (same keys as `containerAt`) → whether that box renders EXPANDED
   *  under the newest-element policy. Resolved once per pass by `indexGroups` through
   *  `rendersExpanded`, and overwritten wholesale there, so a box the walk no longer
   *  seats leaves no stale verdict behind. On `st` rather than on `GroupIndex` because
   *  the two LAZY creation sites (`pipelineBoxFor`, `runCardFor`) are reached without
   *  `idx` in hand, and one home beats two. */
  boxExpanded: Map<string, boolean>;
  /** Where an entry being INSERTED goes in its container: before this node. A Map
   *  for the duration of a HEAD extension and null otherwise, so the append path
   *  is byte-identical outside one. Null is also what tells `appendEntry` not to
   *  seal: an inserted entry is posted after nothing. */
  insertBefore: Map<HTMLElement, HTMLElement | null> | null;
  /** This render's own key in `renders`, which for a detached render is the derived id
   *  rather than the bare turn id. */
  renderID: string;
  /** The turn's own id: what the streaming signals and the height cache are keyed by,
   *  and what every one of its entries names. */
  turnID: string;
  /** The view's ROOT lane — `""` for a chat's transcript, the delegate's uuid for its
   *  detached page. Every lane predicate here is RELATIVE to it, so no view has to strip
   *  a lane off a copy of an entry to make it render. */
  lane: string;
  /** This turn and the three facts hoisted off it once per pass: the lane's first `plan`
   *  seq, the workflow-bearing results, and the tool calls joined with them. Held rather
   *  than passed because the three LAZY container creators are reached from a range that
   *  need not contain the invocation entry, and each has to bind itself from the call or
   *  the box renders with a generic header and no ledger. */
  turn: Turn;
  firstPlan: number;
  results: RunResults;
  calls: readonly TurnToolCall[];
  /** The same joins KEYED for the two questions asked once per tool call: by the
   *  `tool_call` ENTRY's id (`placeEntry`, `indexGroups`, `syncFolds`) and by the TOOL
   *  id (`callByToolID`). Maps beside the ordered array rather than a scan of it, for
   *  the reason `block-window.ts` states at `runResults`' own declaration — both walks
   *  ask per call, so a scan per call is quadratic in the turn's length. */
  callByEntry: ReadonlyMap<string, TurnToolCall>;
  callByTool: ReadonlyMap<string, TurnToolCall>;
  /** lane → the greatest `seq` that lane holds. `entryIsLive` is asked once per placed
   *  entry and answers from here in O(1), where a walk over the body made it the third
   *  instance of the same quadratic. */
  laneTail: ReadonlyMap<string, number>;
  /** The newest `plan` entry of this view's lane, with the `seq` it sits at. Hoisted into
   *  `refreshTurn`'s own walk, so the fold gate is a comparison rather than a scan. */
  newestPlan: { seq: number; entries: PlanEntry[] };
  /** The `seq` of the `plan` entry whose entries the mounted plan card SHOWS, or `-1`
   *  for no card. The plan fold rule read back: a later `plan` replaces the
   *  card's entries, so `newestPlan.seq` differing from this is the derived value having
   *  moved. */
  planFrom: number;
  /** Container key (`sub:`/`pipe:`, the `containerAt` spelling) → the disposer that
   *  releases that box's binding. Membership is also the already-bound guard.
   *
   *  Keyed by the BOX rather than by the invocation call, because a box outlives the
   *  invocation ENTRY that seated it (`isContainerRoot`) and that entry's own disposer
   *  bucket is therefore the wrong lifetime — so whoever releases the box releases the
   *  binding, and an entry can never name a box that is gone. */
  boxBindings: Map<string, () => void>;
}

const renders = new Map<string, TurnRender>();

/** chat id → workflow id → the render whose turn HOSTS that run's card.
 *
 *  The transcript-level half of `TurnRender.runs`: a run's frames span several
 *  turns, and this is what routes every later turn's mentions into the card the
 *  first one built. Claimed at build, released by the host's own disposer.
 *  Detached renders are never in it — the subagent page is its own surface, and
 *  adopting the transcript's card would move the DOM node out of it. */
const runCardHosts = new Map<string, Map<string, TurnRender>>();

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)

/** The whole of `t`'s `seq` space, for a caller that windows nothing. */
function wholeOf(t: Turn): EntryRange {
  return { from: 0, to: turnSpan(t) };
}

/** The render id a body is keyed under: the turn's own id in the transcript, and the
 *  derived one on a delegate's page, because `renders` is one map and the transcript
 *  already holds that turn's entry. */
function renderIDFor(turnID: string, lane: string): string {
  return lane === "" ? turnID : `${turnID}#${lane}`;
}

/** Build the turn body from scratch over `range`. `range` absent is the whole turn,
 *  which is what a detached render always wants. */
export function buildAssistantBody(
  bodyEl: HTMLElement,
  turn: Turn,
  chatID: string,
  live: boolean,
  range?: EntryRange,
): void {
  buildBody(bodyEl, turn, chatID, live, false, "", range);
}

function buildBody(
  bodyEl: HTMLElement,
  turn: Turn,
  chatID: string,
  live: boolean,
  detached: boolean,
  lane: string,
  range?: EntryRange,
): void {
  const want = range ?? wholeOf(turn);
  const st: TurnRender = {
    chatID,
    bodyEl,
    window: { from: want.from, to: want.from },
    entryEls: new Map(),
    entryText: new Map(),
    proseRuns: new Map(),
    runHead: new Map(),
    openTail: null,
    subagents: new Map(),
    pipelines: new Map(),
    stagePipeline: new Map(),
    pipelineStages: new Map(),
    pipelineDeclared: new Map(),
    runs: new Map(),
    runEffects: new Map(),
    disposers: new Map(),
    detached,
    bubbles: [],
    liveBubble: null,
    topLiveEl: null,
    reasonings: [],
    openReasoning: new Map(),
    toolGroups: new Map(),
    containerAt: new Map(),
    boxExpanded: new Map(),
    insertBefore: null,
    renderID: renderIDFor(turn.id, lane),
    turnID: turn.id,
    lane,
    turn,
    firstPlan: -1,
    results: new Map(),
    calls: [],
    callByEntry: new Map(),
    callByTool: new Map(),
    laneTail: new Map(),
    newestPlan: { seq: -1, entries: [] },
    planFrom: -1,
    boxBindings: new Map(),
  };
  renders.set(st.renderID, st);
  refreshTurn(st, turn);
  // ONE index per pass: the mount and the collapse sync ask it different
  // questions about the same run boundaries.
  const idx = indexGroups(st);
  renderRange(st, want.from, want.to, live, idx);
  syncOpenTail(st, live);
  syncContainerCollapse(st, idx);
}

/** Adopt this pass's turn and re-hoist the three facts every gate reads off it. ONE
 *  walk over the body per pass: `firstPlanSeq`, `runResults` and the call join each
 *  ask a per-entry question inside their own loop, so a scan per question is quadratic
 *  in the turn's length. */
function refreshTurn(st: TurnRender, turn: Turn): void {
  st.turn = turn;
  st.firstPlan = firstPlanSeq(turn, st.lane);
  st.results = runResults(turn);
  const settled = new Map<string, EntryToolResult>();
  for (const e of turn.body) {
    const r = payloadOf(e, "tool_result");
    if (r !== undefined) {
      settled.set(e.id, r);
    }
  }
  const calls: TurnToolCall[] = [];
  const byEntry = new Map<string, TurnToolCall>();
  const byTool = new Map<string, TurnToolCall>();
  const laneTail = new Map<string, number>();
  let newestPlan: { seq: number; entries: PlanEntry[] } = { seq: -1, entries: [] };
  for (const e of turn.body) {
    const lane = e.lane ?? "";
    if (e.seq > (laneTail.get(lane) ?? -1)) {
      laneTail.set(lane, e.seq);
    }
    if (e.kind === "plan" && lane === st.lane) {
      const entries = payloadOf(e, "plan")?.entries;
      if (entries !== undefined) {
        newestPlan = { seq: e.seq, entries };
      }
    }
    const call = payloadOf(e, "tool_call");
    if (call === undefined) {
      continue;
    }
    const held: TurnToolCall = {
      entryID: e.id,
      seq: e.seq,
      lane,
      call: settledToolCall(call, settled.get(toolResultID(e.id))),
      runID: effectiveRunID(e, st.results),
    };
    calls.push(held);
    byEntry.set(held.entryID, held);
    // FIRST wins, matching the ordered array's own `find`: two entries carrying one tool
    // id is not a shape the log produces, and a later one must not displace the card's.
    if (!byTool.has(held.call.id)) {
      byTool.set(held.call.id, held);
    }
  }
  st.calls = calls;
  st.callByEntry = byEntry;
  st.callByTool = byTool;
  st.laneTail = laneTail;
  st.newestPlan = newestPlan;
  indexPipelines(st);
}

/** The joined call for one TOOL id (KAS's, which the entry payload carries as `id`). */
function callByToolID(st: TurnRender, toolID: string): TurnToolCall | undefined {
  return st.callByTool.get(toolID);
}

/** Where a mount's turn-lifetime cleanup goes. A transcript render hands it to
 *  messages.ts, which disposes at TURN END as well as on unmount; a DETACHED render
 *  keeps its own, or a cleanup clearing a shared signal would reach into the
 *  transcript's live cards. */
function pushLifetimeEffect(st: TurnRender, seq: number, cleanup: () => void): void {
  if (st.detached) {
    pushDisposer(st, seq, cleanup);
    return;
  }
  cbs.pushEntryEffect(st.turnID, seq, cleanup);
}

/** Add a cleanup to `seq`'s bucket; `-1` is the render's own. */
function pushDisposer(st: TurnRender, seq: number, cleanup: () => void): void {
  const arr = st.disposers.get(seq);
  if (arr === undefined) {
    st.disposers.set(seq, [cleanup]);
  } else {
    arr.push(cleanup);
  }
}

/** Run and drop `seq`'s bucket. */
function runDisposers(st: TurnRender, seq: number): void {
  const arr = st.disposers.get(seq);
  if (arr === undefined) {
    return;
  }
  st.disposers.delete(seq);
  for (const fn of arr) {
    fn();
  }
}

/** Clear a per-tool signal, unless this render shares it with the transcript. The map's
 *  own delete: one caller does not earn a named helper over an exported map. */
function releaseToolSig(st: TurnRender, toolID: string): void {
  if (!st.detached) {
    toolCallSigs.clear(toolCallSigKey(st.chatID, toolID));
  }
}

/** Incrementally sync the turn body: mount newly-arrived entries and bring
 *  already-mounted ones up to the store's text. */
export function updateAssistantBody(
  bodyEl: HTMLElement,
  turn: Turn,
  chatID: string,
  streaming: boolean,
  range?: EntryRange,
): void {
  updateBody(bodyEl, turn, chatID, streaming, false, "", range);
}

/** The `tool`-cause fast path: refresh ONE mounted turn through the same update
 *  path a full pass would run for it, touching no other render.
 *
 *  Returns false when nothing is mounted for the turn — only the full pass mounts. */
export function refreshMessageCard(turn: Turn, chatID: string, live: boolean): boolean {
  const st = renders.get(turn.id);
  if (st === undefined) {
    return false;
  }
  updateAssistantBody(st.bodyEl, turn, chatID, live, st.window);
  return true;
}

/** The entry `seq` range `turnID`'s body currently holds, or undefined when
 *  nothing is mounted for it. The builder's completion test. */
export function mountedWindow(turnID: string): EntryRange | undefined {
  return renders.get(turnID)?.window;
}

/** The settled tool calls `turnID`'s mounted body knows about — the render's OWN join,
 *  which is the set whose cards a park suspends and an unpark re-arms. Empty for a turn
 *  nothing is mounted for, so a caller needs no separate mounted test. */
export function mountedToolCalls(turnID: string): ToolCall[] {
  return (renders.get(turnID)?.calls ?? []).map((c) => c.call);
}

/** The element whose removal drops `seq` of `turnID`, or undefined.
 *
 *  Resolves the RENDER first, so a card hosting another turn's mentions is in the
 *  wrong render's map and cannot answer — which a subtree query for the same `seq`
 *  cannot promise. */
export function blockElement(turnID: string, seq: number): HTMLElement | undefined {
  return renders.get(turnID)?.entryEls.get(seq);
}

/** Where `entryID`'s own words START inside the prose RUN they are rendered in, and that
 *  run's whole source length — the run's offset table, in RUNES, so a reader comparing
 *  against the server's own rune offset compares like with like.
 *
 *  Here because the run's source is this module's (`runText` over the members' payloads plus
 *  the open tail), and a caller re-deriving it would be a second segmentation. Undefined for
 *  an entry no run holds, which is every kind that renders its own element. */
export function runOffsetOf(
  turnID: string,
  entryID: string,
): { offset: number; total: number } | undefined {
  const st = renders.get(turnID);
  if (st === undefined) {
    return undefined;
  }
  const view = runHolding(st, entryID);
  if (view === undefined) {
    return undefined;
  }
  let offset = 0;
  for (const seq of view.seqs) {
    if (entryAt(st.turn, seq)?.id === entryID) {
      return { offset, total: runeLen(runText(st, view)) };
    }
    offset += runeLen(entryTextAt(st, seq));
  }
  // Past every SEALED member: the open tail is the one member with no `seq`, so its text
  // starts where the sealed ones end.
  return view.openID === entryID ? { offset, total: runeLen(runText(st, view)) } : undefined;
}

/** The run holding `entryID`, whether it holds it as a sealed member or as its open tail. */
function runHolding(st: TurnRender, entryID: string): ProseRun | undefined {
  const seq = turnOrdinalOf(st.turn, entryID);
  const head = seq === undefined ? undefined : st.runHead.get(seq);
  if (head !== undefined) {
    return st.proseRuns.get(head);
  }
  const tail = st.openTail;
  return tail?.kind === "text" && tail.id === entryID ? tail.run : undefined;
}

/** Runes, not UTF-16 units: the server counts what Go counts, and a string's own iterator
 *  yields exactly that — one element per code point. Deliberately not `Intl.Segmenter`,
 *  which counts GRAPHEME CLUSTERS: an emoji with a modifier is several runes to Go and one
 *  cluster to it, so a denominator built that way disagrees with the numerator it divides. */
function runeLen(s: string): number {
  return Array.from(s).length;
}

/** Ids of renders still carrying live text: an unsealed live bubble, or any
 *  bubble whose caret has not drained (`.streaming` is granted and revoked by
 *  the bubble itself, so the class read IS the caret test — no subtree scan).
 *  Detached renders report too; the transcript caller drops ids it never
 *  mounted. */
export function liveRenderIDs(): string[] {
  const out: string[] = [];
  for (const [id, st] of renders) {
    if (st.liveBubble !== null || st.bubbles.some((b) => b.root.classList.contains("streaming"))) {
      out.push(id);
    }
  }
  return out;
}

function updateBody(
  bodyEl: HTMLElement,
  turn: Turn,
  chatID: string,
  streaming: boolean,
  detached: boolean,
  lane: string,
  range?: EntryRange,
): void {
  const st = renders.get(renderIDFor(turn.id, lane));
  if (st === undefined) {
    // Should not happen (build runs first), but stay self-healing.
    buildBody(bodyEl, turn, chatID, streaming, detached, lane, range);
    return;
  }
  const want = range ?? wholeOf(turn);
  // Ahead of the render, and on EVERY pass rather than only when entries arrive: a
  // stage's entries can reach the dispatcher before its own invocation is in the
  // store (out-of-order SSE), and `indexPipelines` is the only thing that knows
  // which pipeline a stage belongs to.
  refreshTurn(st, turn);
  // The INDEX leads, because `rehomeStages` reaches `pipelineBoxFor` through
  // `stageHostFor`, so a box can be CREATED before this pass's index exists — and a
  // creation site reading a stale `st.boxExpanded` is what would force the collapse sync
  // to also open things. Value-neutral for the index: its `st.pipelines` reads are both
  // disjunctions with `pipelineHasContainer`, the gate `stageHostFor` checks first.
  const idx = indexGroups(st);
  // BEFORE the range, so an adopted box precedes whatever this pass mounts.
  rehomeStages(st, streaming);
  if (want.to > st.window.to) {
    renderRange(st, st.window.to, want.to, streaming, idx);
  }
  // UNCONDITIONAL, unlike the range: an open entry has no `seq`, so its arrival moves no
  // window edge and only this call puts it on screen.
  syncOpenTail(st, streaming);
  // BEFORE the collapse sync, so a card this pass folded into place takes its verdict in
  // the same pass rather than rendering expanded for one frame.
  syncFolds(st, streaming, idx);
  syncContainerCollapse(st, idx);
  syncMountedText(st);
}

/** Bring every mounted entry up to the store's current text for that entry.
 *
 *  The FALLBACK path: the per-entry signal effect is only created for an entry the
 *  renderer judged live, so a misjudged one would freeze at the text it mounted
 *  with. Safe over a subscribed entry — both writers own their own watermark. */
function syncMountedText(st: TurnRender): void {
  for (const [seq, setText] of st.entryText) {
    const e = entryAt(st.turn, seq);
    if (e === undefined) {
      continue;
    }
    // ONE fold per prose run, from its head, which writes every member in `seq` order:
    // a member's own sink would write the same concatenation N times.
    const head = st.runHead.get(seq);
    if (head !== undefined && head !== seq) {
      continue;
    }
    const text = payloadOf(e, "text")?.text ?? payloadOf(e, "thinking")?.text;
    if (text !== undefined) {
      setText(text);
    }
  }
  // The OPEN entry is mounted and is not in `entryText`, having no `seq` to be keyed by.
  const tail = st.openTail;
  const open = openEntryOf(st);
  if (tail === null || open?.id !== tail.id) {
    return;
  }
  if (tail.kind === "text") {
    tail.run.bubble.setText(runText(st, tail.run));
  } else {
    tail.view.setText(open.text);
  }
}

/** Bring a mounted entry up to date with a DERIVED value that moved after it mounted:
 *  the two folds a first dispatch cannot have got right, because the entry
 *  SUPPLYING the value arrives after the entry that renders it.
 *
 *  `renderRange` mounts only above `st.window.to`, so nothing else revisits a `seq` and
 *  both losses are silent — a plan card frozen at the turn's first plan state, a launch
 *  drawn as a tool row that never becomes the run card. The other two rules have their
 *  own channels: a `tool_result` folds through its card's signal, a `steer_ack` at its `seq`. */
function syncFolds(st: TurnRender, live: boolean, idx: GroupIndex): void {
  // Rule 3, a later `plan` REPLACING the card's entries. `entryRenders` admits only the
  // turn's first plan entry, so the newest state is read off the store and the card is
  // updated in place; `st.planFrom` is what the card currently shows.
  if (st.firstPlan >= 0 && st.entryEls.has(st.firstPlan) && st.newestPlan.seq !== st.planFrom) {
    const first = entryAt(st.turn, st.firstPlan);
    if (first !== undefined) {
      mountPlan(st, st.bodyEl, first);
    }
  }
  // The RUN CARD rule: the entry that FIRST supplies the effective workflow id
  // re-dispatches that one `tool_call`, so its tool row is replaced in place by the card.
  // The mounted ELEMENT records what the entry was drawn as, so no second copy of the
  // verdict can go stale; the index already prices the box at this `seq`.
  for (const held of st.calls) {
    if (held.seq < st.window.from || held.seq >= st.window.to) {
      continue;
    }
    if (held.runID === "" || (held.call.agent_subtask_id ?? "") !== "") {
      continue;
    }
    if (isInternalToolTitle(held.call.title) || !ownsRunCard(st, held.runID, held.entryID)) {
      continue;
    }
    const el = st.entryEls.get(held.seq);
    if (el === undefined || el === st.runs.get(held.runID)?.root) {
      continue;
    }
    const e = entryAt(st.turn, held.seq);
    if (e === undefined) {
      continue;
    }
    // The hosted card it answers with is DISCARDED here: `placeEntry` re-seats that same
    // card, where `resolveRunCardFate` would delete one with nothing mounted inside it.
    dropEntry(st, held.seq);
    cbs.disposeEntryEffects(st.turnID, [held.seq]);
    // The row was a group MEMBER, so its shell can be left with nothing in it. Only the
    // groups: a box this render holds is not this drop's to judge.
    pruneEmptyToolGroups(st);
    placeEntry(st, e, live, idx);
  }
}

/** Finalize: flush every markdown stream + settle every reasoning trace. */
export function finalizeAssistantBody(renderID: string): void {
  const st = renders.get(renderID);
  if (st === undefined) {
    return;
  }
  for (const b of st.bubbles) {
    // end(), not finishNow(): the turn is over, but the last entry's reveal is text
    // the model really did produce last, so let it land.
    b.end();
  }
  st.liveBubble = null;
  for (const r of st.reasonings) {
    // settle(), not seal(): the turn ending is not another element being posted, and
    // the trigger for a fold is positional. Every trace something DID follow was
    // already sealed at its own mount or by `sealReasoning`, so what this leaves
    // expanded is exactly the trace that is still the newest element in its lane.
    r.settle();
  }
}

/** Drop a turn's render state (reconcile.onRemove / chat switch). */
export function disposeAssistantBody(renderID: string): void {
  const st = renders.get(renderID);
  if (st !== undefined) {
    // A bubble mid-reveal holds a frame loop; finish it before its DOM goes.
    for (const b of st.bubbles) {
      b.finishNow();
    }
    disposeAll(st);
  }
  renders.delete(renderID);
}

/** This render's half of a PARK: release the open tail, finish any reveal, then suspend
 *  the run cards (effects stop, clock holds release) without the final dispose. The
 *  streaming and tool-card effects are the callers' registries; the tail's subscription is
 *  in NEITHER, so this is the only path a park can stop it by — and a turn holding one is
 *  open, which is exactly the turn an unpark rebuilds, so the tail is re-mounted there. */
export function pauseAssistantBody(renderID: string): void {
  const st = renders.get(renderID);
  if (st === undefined) {
    return;
  }
  releaseOpenTail(st);
  for (const b of st.bubbles) {
    b.finishNow();
  }
  st.liveBubble = null;
  for (const [workflowID, card] of st.runs) {
    disarmRunCard(st, workflowID, card);
  }
}

/** Re-arm an unparked render's run cards; the effect's first run re-reads the run's
 *  cell. Idempotent per card. */
export function resumeAssistantBody(renderID: string): void {
  const st = renders.get(renderID);
  if (st === undefined) {
    return;
  }
  for (const [workflowID, card] of st.runs) {
    armRunCard(st, workflowID, card);
  }
}

export function resetBlockRenders(): void {
  liveAnchor = null;
  for (const st of renders.values()) {
    for (const b of st.bubbles) {
      b.finishNow();
    }
    disposeAll(st);
  }
  renders.clear();
}

// The DETACHED render: one delegate's LANE, on its own page

/** Render one delegate's own transcript into `host`, as the main agent's.
 *
 *  The turn is the REAL one and nothing is copied: the page's root lane is the
 *  delegate's uuid, and every lane predicate here compares against that root — so the
 *  delegate's entries render in the flow and a nested invocation renders as a card for
 *  the grandchild, with no view stripping a lane off an entry to make it draw. */
export function buildDetachedBody(
  host: HTMLElement,
  turn: Turn,
  chatID: string,
  subtask: string,
  live: boolean,
): void {
  buildBody(host, turn, chatID, live, true, subtask);
}

/** Append newly-arrived entries and bring mounted ones up to the store's text. */
export function updateDetachedBody(
  host: HTMLElement,
  turn: Turn,
  chatID: string,
  subtask: string,
  live: boolean,
): void {
  updateBody(host, turn, chatID, live, true, subtask);
}

/** Flush every markdown stream and SETTLE every reasoning trace — `finalizeAssistantBody`
 *  for a detached render, so it folds nothing either: the trace nothing followed is still
 *  the newest element of its lane, and only a successor being posted seals one. */
export function finalizeDetachedBody(turnID: string, subtask: string): void {
  finalizeAssistantBody(renderIDFor(turnID, subtask));
}

/** Drop a detached render. The page's own unmount, and the only thing that fires
 *  its disposers — messages.ts never sees this id. */
export function disposeDetachedBody(turnID: string, subtask: string): void {
  disposeAssistantBody(renderIDFor(turnID, subtask));
}

/** Run and clear every cleanup this render owns. Idempotent: both dispose paths can
 *  reach one render, and a store subscription disposed twice must not throw. */
function disposeAll(st: TurnRender): void {
  releaseOpenTail(st);
  pruneContainers(st);
  for (const key of [...st.disposers.keys()]) {
    runDisposers(st, key);
  }
}

// Entry dispatch

/** One dispatchable unit of a range: a prose RUN's members, or a single entry. */
type Placement = { run: number; seqs: number[] } | { run: -1; seqs: [number] };

/** Group `[from, to)` before dispatching it, so a run's members reach ONE mounter. The run
 *  boundaries come from `block-window.ts` `proseRunAt`, the rule the window snap applies, and
 *  a run's `run` field is its FIRST `seq` — which can sit BELOW `from` on a live turn, where
 *  the members in range extend a bubble already mounted. */
function groupRange(st: TurnRender, from: number, to: number): Placement[] {
  const out: Placement[] = [];
  for (let seq = from; seq < to; seq++) {
    const e = entryAt(st.turn, seq);
    if (e === undefined || !entryRenders(e, st.lane, st.firstPlan)) {
      continue;
    }
    const run = proseRunAt(st.turn, seq, st.lane, st.firstPlan);
    if (run === null) {
      out.push({ run: -1, seqs: [seq] });
      continue;
    }
    const seqs: number[] = [];
    for (let i = seq; i < Math.min(run.to, to); i++) {
      const m = entryAt(st.turn, i);
      if (m?.kind === "text" && entryRenders(m, st.lane, st.firstPlan)) {
        seqs.push(i);
      }
    }
    out.push({ run: run.from, seqs });
    seq = Math.min(run.to, to) - 1;
  }
  return out;
}

function renderRange(
  st: TurnRender,
  from: number,
  to: number,
  live: boolean,
  idx: GroupIndex,
): void {
  const placements = groupRange(st, from, to);
  // Only a TAIL append moves the tail, and nothing re-establishes a caret sealed by
  // mistake. Nor does a range whose every entry renders NOTHING here: nothing is placed,
  // so ending the caret would stop the reader's streaming reply for an arrival this view
  // draws none of. Idempotent.
  const tailAppend = to > st.window.to && placements.length > 0;
  // A range LEADING with an extension of the mounted run keeps the caret: its members are
  // the same bubble still being written, so sealing here would drop `.streaming` at every
  // entry seal — the one thing a seal must not do. What ends that run is a
  // placement AFTER it, which the loop seals as it reaches it.
  const first = placements[0];
  const extendsRun = first !== undefined && first.run >= 0 && st.proseRuns.has(first.run);
  if (tailAppend && !extendsRun) {
    sealLiveBubble(st);
  }
  st.window = { from: Math.min(st.window.from, from), to: Math.max(st.window.to, to) };
  for (const [i, p] of placements.entries()) {
    if (p.run >= 0) {
      const tail = p.seqs[p.seqs.length - 1];
      const tailEntry = tail === undefined ? undefined : entryAt(st.turn, tail);
      mountProse(
        st,
        st.bodyEl,
        p.run,
        p.seqs,
        live && tailEntry !== undefined && entryIsLive(st, tailEntry),
      );
      // The run END, over the whole range rather than its first element: anything
      // following this run in the range renders below it, so the caret and the follow anchor
      // may not stay on it.
      if (tailAppend && i + 1 < placements.length) {
        sealLiveBubble(st);
      }
      continue;
    }
    const e = entryAt(st.turn, p.seqs[0]);
    if (e !== undefined) {
      placeEntry(st, e, live && entryIsLive(st, e), idx);
    }
  }
}

// The window's two moving edges: ONE call per edge, never one for both. A
// relocation retracts a row at both ends, and a single compensated call would
// correct by a delta that includes the below-the-reader removal.

/** Mount `keep`'s ordinals below the mounted head IN PLACE: the head extension.
 *
 *  Positional rather than a row rebuild, so nothing replays an animation, drops a
 *  selection or forgets a reader-set disclosure — safe because grouping and sealing
 *  are derived from the store. Bounded by `keep.to` too, or a move to a DISJOINT
 *  range mounts everything between the two and the tail drop takes it straight
 *  back. */
export function mountHeadRange(turn: Turn, keep: EntryRange, live: boolean): void {
  const st = renders.get(turn.id);
  const from = keep.from;
  if (st === undefined || from >= st.window.from) {
    return;
  }
  const to = Math.min(st.window.from, keep.to);
  if (from >= to) {
    return;
  }
  refreshTurn(st, turn);
  const idx = indexGroups(st);
  st.insertBefore = new Map();
  try {
    renderRange(st, from, to, live, idx);
  } finally {
    st.insertBefore = null;
  }
  syncContainerCollapse(st, idx);
}

/** Retract the turn's mounted window at the HEAD to `keep.from`. Collected as a
 *  head-side change: everything it removes is above the reader. */
export function dropHead(turn: Turn, keep: EntryRange): void {
  const st = renders.get(turn.id);
  if (st === undefined || keep.from <= st.window.from) {
    return;
  }
  dropEntryRange(st, { from: keep.from, to: st.window.to });
}

/** Retract the turn's mounted window at the TAIL to `keep.to`. Collected as a
 *  tail-side change: it runs BARE, because its delta is below the reader and
 *  compensating it would drag their view. */
export function dropTail(turn: Turn, keep: EntryRange): void {
  const st = renders.get(turn.id);
  if (st === undefined || keep.to >= st.window.to) {
    return;
  }
  dropEntryRange(st, { from: st.window.from, to: keep.to });
}

/** Release everything the mounted `seq`s OUTSIDE `keep` own, and leave no effect subscribed to
 *  a detached node. `openContainers` keys deliberately SURVIVE: a drop is a window move, not a
 *  render dispose, so a box the reader opened comes back open. */
function dropEntryRange(st: TurnRender, keep: EntryRange): void {
  const removed: number[] = [];
  for (let seq = st.window.from; seq < st.window.to; seq++) {
    if (seq < keep.from || seq >= keep.to) {
      removed.push(seq);
    }
  }
  if (removed.length === 0) {
    return;
  }
  const orphaned = new Map<string, RunCardView>();
  for (const seq of removed) {
    const hosted = dropEntry(st, seq);
    if (hosted !== undefined) {
      orphaned.set(hosted.runID, hosted.card);
    }
  }
  cbs.disposeEntryEffects(st.turnID, removed);
  pruneOrphanedCards(st, keep);
  pruneEmptyContainers(st);
  rebindSurvivingBoxes(st);
  st.window = keep;
  // After the LOOP: `st` is a candidate claimant, and mid-loop its `entryEls` still holds
  // ordinals this same drop is about to take.
  for (const [runID, card] of orphaned) {
    resolveRunCardFate(st, runID, card);
  }
}

/** Re-home or release `runID`'s card once the drop that took its launch block is
 *  complete: one card per run, hosted by the earliest render still holding mounted
 *  blocks inside it, which can be `st` itself. */
function resolveRunCardFate(st: TurnRender, runID: string, card: RunCardView): void {
  const claim = liveRunClaimant(st, card);
  if (claim === undefined) {
    // Nothing mounted inside it, so the run's own state goes back — or `runCardFor`
    // hands the next claimant a DETACHED node and re-homing never fires.
    st.runs.delete(runID);
    releaseRunCard(st, runID, card);
    card.root.remove();
    return;
  }
  // RE-HOMED: the card is a CONTAINER, and removing it takes a render's mounted entries
  // out of the document while that render still counts them.
  const seat = seatAbove(claim.host, claim.host.bodyEl, claim.at, card.root);
  if (claim.host !== st) {
    adoptRunCard(claim.host, st, runID, card, seat);
    return;
  }
  // The claim is already this render's, so only the SEAT can be wrong: the `seq` it was
  // placed at is gone. Guarded because ANY re-seat blurs whatever the card holds focus
  // on, and a drop runs while the reader scrolls.
  if (card.root.nextElementSibling !== seat) {
    st.bodyEl.insertBefore(card.root, seat);
  }
}

/** Whether `el` sits in a subtree the page is not rendering: a folded card's body, a
 *  parked view, or a collapsed pipeline container. A `closest()` test, never a geometry
 *  read — reading a descendant's box there forces the browser to render what it
 *  skipped, which is the cost being avoided. */
export function geometrySkipped(el: Element): boolean {
  return (
    el.closest(".turn[data-folded] > .turn-body") !== null ||
    el.closest(".transcript-view:not(.is-active)") !== null ||
    el.closest(".subagent-block.collapsed > .subagent-body") !== null
  );
}

/** Release one entry: its measured height into the cache, its disclosure state, its element,
 *  its text sink, its streaming signal and its entry-lifetime cleanups. Answers with the run
 *  card the entry hosted, whose fate its caller decides once the whole range is gone. */
function dropEntry(st: TurnRender, seq: number): { runID: string; card: RunCardView } | undefined {
  const entry = entryAt(st.turn, seq);
  const hosted = hostedRun(st, entry);
  const el = st.entryEls.get(seq);
  st.entryEls.delete(seq);
  // A prose run's members share ONE row, so only its HEAD releases the element; a later
  // member drops its own registrations and leaves the row to that drop. The window snap
  // keeps whole runs on one side of an edge, so the head is always in the same range.
  const head = st.runHead.get(seq);
  st.runHead.delete(seq);
  // The RANGE the row held, which is the key its height is worth remembering under: a row
  // dropped under a partial window measured that slice and answers for no other.
  let runRange: EntryRange | undefined;
  if (head !== undefined && head === seq) {
    const view = st.proseRuns.get(seq);
    st.proseRuns.delete(seq);
    if (view !== undefined) {
      runRange = { from: seq, to: (view.seqs[view.seqs.length - 1] ?? seq) + 1 };
    }
    // The row carrying the open tail is going, so its subscription would write into a
    // detached bubble for as long as the entry stays open.
    if (st.openTail?.kind === "text" && st.openTail.run === view) {
      releaseOpenTail(st);
    }
  }
  if (el !== undefined && head !== undefined && head !== seq) {
    st.entryText.delete(seq);
    if (entry !== undefined) {
      clearEntryTextSig(st.turnID, entry.id);
    }
    runDisposers(st, seq);
    return hosted;
  }
  if (el !== undefined && !isContainerRoot(st, el, hosted?.card)) {
    // MEASURED on the way out, so the spacer replacing it holds the height it held, and
    // only a REAL reading: a detached element answers 0 and a short spacer leaves the
    // document shorter than the content it stands in for. The estimate over-prices.
    // Skipped inside an unrendered subtree, where the read answers no height a spacer
    // could hold and forces the browser to render what it chose to skip.
    const px = geometrySkipped(el) ? 0 : el.offsetHeight;
    if (px > 0) {
      // A prose RUN's row is one box for every member it held, so its height is keyed by
      // that range rather than by the head alone, which would price the rest at zero.
      if (runRange === undefined) {
        recordEntryHeight(st.turnID, seq, px);
      } else {
        recordRowHeight(st.turnID, runRange, px);
      }
    }
    recordDisclosure(el, entry === undefined ? "" : (payloadOf(entry, "tool_call")?.id ?? ""));
    st.bubbles = st.bubbles.filter((b) => {
      if (b.root !== el && !el.contains(b.root)) {
        return true;
      }
      // A reveal in flight holds a frame loop, and its DOM is about to go.
      b.finishNow();
      if (st.liveBubble === b) {
        st.liveBubble = null;
      }
      return false;
    });
    st.reasonings = st.reasonings.filter((view) => {
      if (view.root !== el && !el.contains(view.root)) {
        return true;
      }
      for (const [container, open] of st.openReasoning) {
        if (open === view) {
          st.openReasoning.delete(container);
        }
      }
      return false;
    });
    if (st.topLiveEl !== null && (st.topLiveEl === el || el.contains(st.topLiveEl))) {
      clearLiveAnchor(st.topLiveEl);
      st.topLiveEl = null;
    }
    forgetSubagentCard(st, el);
    el.remove();
  }
  st.entryText.delete(seq);
  if (entry !== undefined) {
    clearEntryTextSig(st.turnID, entry.id);
  }
  runDisposers(st, seq);
  return hosted;
}

/** Drop the render state of the delegate card `el` IS, if it is one: an `st.subagents`
 *  entry pointing at a removed node would refuse to build the next card. */
function forgetSubagentCard(st: TurnRender, el: HTMLElement): void {
  const subtask = el.dataset["subtask"] ?? "";
  if (subtask !== "" && st.subagents.get(subtask)?.root === el) {
    st.subagents.delete(subtask);
  }
}

/** Whether `el` is a CONTAINER whose lifetime this render owns somewhere other than the
 *  entry that stamped it. Released like an ordinary entry it would be removed out from
 *  under entries `st.window` still counts as mounted. A DELEGATE's card counts, and
 *  `pruneOrphanedCards` ends that one instead. */
function isContainerRoot(st: TurnRender, el: HTMLElement, card: RunCardView | undefined): boolean {
  if (el === card?.root) {
    return true;
  }
  for (const box of st.pipelines.values()) {
    if (box.root === el) {
      return true;
    }
  }
  for (const sa of st.subagents.values()) {
    if (sa.root === el) {
      return true;
    }
  }
  return false;
}

/** Remove every delegate card the surviving range no longer holds an invocation for. Takes
 *  `keep` rather than `st.window` so it can run BEFORE `pruneEmptyContainers`, which a
 *  pipeline losing its last stage has to look empty to; a card holds no entries of its own,
 *  so DOM emptiness cannot answer this. */
function pruneOrphanedCards(st: TurnRender, keep: EntryRange): void {
  if (st.subagents.size === 0) {
    return;
  }
  const live = new Set<string>();
  for (const tc of st.calls) {
    const subtask = tc.call.agent_subtask_id ?? "";
    if (subtask !== "" && tc.seq >= keep.from && tc.seq < keep.to) {
      live.add(subtask);
    }
  }
  for (const [subtask, sa] of st.subagents) {
    if (!live.has(subtask)) {
      releaseBox(st, `sub:${subtask}`);
      sa.root.remove();
      st.subagents.delete(subtask);
    }
  }
}

/** Release the binding `key`'s box holds, if it still holds the one it registered.
 *
 *  Every path that RELEASES a box calls this, because the binding's own entry-lifetime
 *  disposer cannot: a box survives the drop of its invocation entry, so that bucket is
 *  drained after the box dies — or never, when the entry sits outside the window. Without
 *  it a dead card keeps its entry, its effect paints a detached node, and the next card
 *  for that subtask is refused the binding, so it renders once and never moves again. */
function releaseBox(st: TurnRender, key: string): void {
  st.boxBindings.get(key)?.();
}

/** The run card THIS render hosts for `entry`'s workflow, for any `tool_call` entry naming
 *  one. The pair `dropEntry` needs twice: to leave the card out of the generic release, and
 *  to decide its fate afterwards. */
function hostedRun(
  st: TurnRender,
  entry: Entry | undefined,
): { runID: string; card: RunCardView } | undefined {
  if (entry?.kind !== "tool_call") {
    return undefined;
  }
  const runID = effectiveRunID(entry, st.results);
  const card = runID === "" ? undefined : st.runs.get(runID);
  return card === undefined ? undefined : { runID, card };
}

/** The render holding mounted entries INSIDE `card`, and the lowest such `seq`. `st` is a
 *  candidate like any other: a later mention of the same run can be in the turn that launched
 *  it, which leaves ONE render holding both the launch and content inside the card. DOM order
 *  decides between several, because `renders` is keyed in BUILD order and a scroll up builds
 *  earlier turns last. */
function liveRunClaimant(
  st: TurnRender,
  card: RunCardView,
): { host: TurnRender; at: number } | undefined {
  let out: { host: TurnRender; at: number } | undefined;
  for (const other of renders.values()) {
    if (other.detached || other.chatID !== st.chatID) {
      continue;
    }
    for (let seq = other.window.from; seq < other.window.to; seq++) {
      const el = other.entryEls.get(seq);
      if (el === undefined || !card.root.contains(el)) {
        continue;
      }
      const held = out?.host.bodyEl;
      if (
        held === undefined ||
        (other.bodyEl.compareDocumentPosition(held) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
      ) {
        out = { host: other, at: seq };
      }
      break;
    }
  }
  return out;
}

/** Where something the store places at `at` belongs in `host`: before the first child of
 *  `host` holding a mounted ordinal ABOVE `at`, and the tail when there is none. `placed`
 *  is the node being seated, excluded because its own members walk up to it. */
function seatAbove(
  st: TurnRender,
  host: HTMLElement,
  at: number,
  placed: HTMLElement,
): HTMLElement | null {
  for (let i = at + 1; i < st.window.to; i++) {
    let node = st.entryEls.get(i) ?? null;
    while (node !== null && node.parentElement !== host) {
      node = node.parentElement;
    }
    if (node !== null && node !== placed) {
      return node;
    }
  }
  return null;
}

/** Re-subscribe every CONTAINER the drop left STANDING whose invocation entry it took:
 *  that entry's cleanup released the binding, no path re-binds an existing box, and the
 *  box would keep a frozen header and ledger until its invocation re-mounts.
 *
 *  Both kinds of survivor, and the delegate CARD is one — `isContainerRoot` keeps its
 *  element while the entry's cleanup releases its binding, so a card that outlived the
 *  drop is the shape that freezes. Its status follows its invocation CALL, which the
 *  store holds for the whole turn, not that call's ENTRY, whose residency is the window's. */
function rebindSurvivingBoxes(st: TurnRender): void {
  for (const pipelineID of st.pipelines.keys()) {
    const inv = callByToolID(st, pipelineID);
    if (
      inv !== undefined &&
      isPipelineInvocation(inv.call) &&
      !st.boxBindings.has(`pipe:${pipelineID}`)
    ) {
      bindPipeline(st, inv.call, false, inv.seq);
    }
  }
  for (const [subtask, sa] of st.subagents) {
    const inv = st.calls.find(
      (c) => (c.call.agent_subtask_id ?? "") === subtask && isSubagentInvocation(c.call),
    );
    if (inv !== undefined && !st.boxBindings.has(`sub:${subtask}`)) {
      bindSubagent(st, subtask, sa, inv.call, inv.seq);
    }
  }
}

/** Remove every tool GROUP shell left with no card in it. Its own function because two
 *  callers reach it and only one of them may touch the other containers: a drop sweeps
 *  everything, while `syncFolds` takes one card out of a group and must not also prune a
 *  pipeline box that is legitimately empty (a driver that dispatched no stage keeps its
 *  box, `pipelineBoxFor`). */
function pruneEmptyToolGroups(st: TurnRender): void {
  for (const [key, bucket] of st.toolGroups) {
    for (const [runStart, group] of bucket) {
      if (groupBody(group).firstElementChild === null) {
        group.remove();
        bucket.delete(runStart);
      }
    }
    if (bucket.size === 0) {
      st.toolGroups.delete(key);
    }
  }
}

/** Remove every container this drop left with nothing in it, and its render state
 *  with it. Its `openContainers` key stays, per the disclosure rule. */
function pruneEmptyContainers(st: TurnRender): void {
  pruneEmptyToolGroups(st);
  for (const [pipelineID, box] of st.pipelines) {
    if (box.body.firstElementChild === null) {
      releaseBox(st, `pipe:${pipelineID}`);
      st.openReasoning.delete(box.body);
      box.root.remove();
      st.pipelines.delete(pipelineID);
    }
  }
}

/** Whether `e` is the entry its own LANE is still writing: the tail `seq` of that lane in
 *  the turn's body.
 *
 *  ONE rule for every kind, because a lane coalesces one entry at a time
 *  (`TurnState.openEntries` is keyed by lane), so the lane's own tail IS the streaming
 *  position and a delegate interleaving cannot move it. Exactly one streaming caret per
 *  render still holds, because a render draws one lane. */
function entryIsLive(st: TurnRender, e: Entry): boolean {
  return (st.laneTail.get(e.lane ?? "") ?? -1) <= e.seq;
}

/** End the bubble currently carrying the caret, if any. */
function sealLiveBubble(st: TurnRender): void {
  // finishNow, not end: the lane's tail MOVED, so this entry's residual reveal is no
  // longer live text, and "exactly one streaming caret" is the invariant this function
  // keeps. The turn's LAST entry gets the graceful drain instead.
  st.liveBubble?.finishNow();
  st.liveBubble = null;
}

/** Get or build a delegate's CARD, whose one creator is its invocation entry: the delegate's
 *  own entries are in ITS lane, so this view draws none of them and there is nothing else to
 *  seat it from. A stage's host pipeline is `st.stagePipeline`'s. */
function subagentCardFor(st: TurnRender, subtask: string, live: boolean): SubagentCard {
  const existing = st.subagents.get(subtask);
  if (existing !== undefined) {
    return existing;
  }
  // The invocation is TURN-level here, so it is found whether or not this range holds its
  // `seq`. Read BEFORE the build because the card's tail is latched on its starting status
  // and never comes back: built settled, a delegate that is still working would lose its
  // tail for the rest of the render.
  const inv = st.calls.find(
    (c) => (c.call.agent_subtask_id ?? "") === subtask && isSubagentInvocation(c.call),
  )?.call;
  const sa = buildSubagentCard(
    "Subagent",
    inv?.status ?? (live ? "in_progress" : "completed"),
    subagentOpenerFor(st, subtask),
  );
  sa.root.dataset["subtask"] = subtask;
  st.subagents.set(subtask, sa);
  // And the BINDING, for the reason `pipelineBoxFor` binds its own box: a card THIS path
  // seated has no subscription and no ledger until its invocation entry mounts, which a
  // window need never reach — so its status would stay frozen at whatever it last
  // painted. `bindSubagent` paints first and returns early for a CARD already bound, so
  // `placeEntry`'s own bind is a no-op whichever of the two got here first.
  if (inv !== undefined) {
    bindSubagent(st, subtask, sa, inv, invocationIndex(st, inv.id));
  }
  // The card lands in its HOST (top level or a pipeline body), at the `seq` that
  // establishes it — the same `seq` `indexGroups` prices its run break at.
  const host = stageHostFor(st, subtask, live);
  placeContainer(st, host, sa.root, st.containerAt.get(`sub:${subtask}`));
  return sa;
}

/** The `seq` `toolID`'s own entry sits at, or `-1` — the render-lifetime bucket — when
 *  this turn holds no entry for it. */
function invocationIndex(st: TurnRender, toolID: string): number {
  return callByToolID(st, toolID)?.seq ?? -1;
}

/** What the delegate card's HEAD opens, or nothing.
 *
 *  Injected rather than imported so `fundamentals/` keeps pointing downward, and lazy
 *  because `subagent-view.ts` reaches the whole page. A DETACHED render gets none: it
 *  IS the page. The chat id is the RENDER's, the one the blocks came from. */
function subagentOpenerFor(st: TurnRender, subtask: string): { open?: SubagentOpener } {
  if (st.detached) {
    return {};
  }
  const chatID = st.chatID;
  if (chatID === "" || subtask === "") {
    return {};
  }
  return {
    open: {
      href: buildPath({ kind: "subagent", chat: chatID, id: subtask }),
      open: () => {
        void import("./subagent-view.js")
          .then(({ openSubagentView }) => {
            void openSubagentView(chatID, subtask);
          })
          .catch(() => {
            /* noop: the link degrades to its href on the next click */
          });
      },
    },
  };
}

/** Whether this pipeline's own box is in this render, or will be by the end of the
 *  pass. `stageHostFor`'s question, asked without building anything: an EXISTING
 *  box outranks the count there, so the count alone answers a different one. */
function hostsPipelineBox(st: TurnRender, pipelineID: string): boolean {
  return st.pipelines.has(pipelineID) || pipelineHasContainer(st, pipelineID);
}

/** Where a stage's own box goes: its pipeline's body when that pipeline has a
 *  container, otherwise the top level. Building the box here makes the two arrival
 *  orders equivalent. An EXISTING container wins over the count, so that check is
 *  first — the count can rise after a container exists. */
function stageHostFor(st: TurnRender, subtask: string, live: boolean): HTMLElement {
  const pipelineID = st.stagePipeline.get(subtask);
  if (pipelineID === undefined) {
    return st.bodyEl;
  }
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing.body;
  }
  if (!pipelineHasContainer(st, pipelineID)) {
    return st.bodyEl;
  }
  return pipelineBoxFor(st, pipelineID, live).body;
}

/** Re-derive every mounted delegate card's host and move the ones that no longer sit in it.
 *  A host is chosen when the card is BUILT, from a stage count that is only a lower bound
 *  then — a lone stage is PROMOTED to the top level and its pipeline grows a container
 *  later — so without this the stage stays a sibling of its own pipeline. Idempotent:
 *  re-appending a card in place would re-fire its mount animation. */
function rehomeStages(st: TurnRender, live: boolean): void {
  for (const [subtask, sa] of st.subagents) {
    const host = stageHostFor(st, subtask, live);
    if (sa.root.parentElement !== host) {
      host.appendChild(sa.root);
    }
  }
}

/** Write the driver's header onto its box: the label from the stage COUNT, the
 *  status and the footer ledger from the driver's own call. */
function paintPipeline(st: TurnRender, box: SubagentContainer, driver: ToolCall): void {
  box.setName(pipelineLabel(st, driver.id));
  box.setStatus(driver.status);
  box.setSummary(pipelineSummary(st, driver));
}

/** Get or build the PIPELINE box for one orchestrate tool call.
 *
 *  It also ADOPTS any stage of its own sitting at the top level, the upgrade path
 *  after a lone stage was promoted. A RE-PARENT, never a rebuild: the move carries
 *  the disclosure, the observers and every effect with the node. */
function pipelineBoxFor(st: TurnRender, pipelineID: string, live: boolean): SubagentContainer {
  const existing = st.pipelines.get(pipelineID);
  if (existing !== undefined) {
    return existing;
  }
  const key = `pipe:${pipelineID}`;
  const box = buildSubagentContainer(
    pipelineLabel(st, pipelineID),
    live ? "in_progress" : "completed",
    st.detached
      ? {}
      : {
          // The reader's own state first, then THIS pass's newest-element verdict. The
          // absent-entry fallback is a floor rather than a path: `indexGroups` runs
          // ahead of every creation site in the pass, so a box with no seat is one
          // being established by content that has just arrived.
          startOpen: containerOpen(key) ?? st.boxExpanded.get(key) ?? true,
          userDecided: containerOpen(key) !== undefined,
          onOpenChange: (open: boolean): void => {
            setContainerOpen(key, open);
          },
        },
  );
  box.root.dataset["pipeline"] = pipelineID;
  st.pipelines.set(pipelineID, box);
  // Same-pass adoption; `rehomeStages` is the general case.
  const promoted = (st.pipelineStages.get(pipelineID) ?? [])
    .map((subtask) => st.subagents.get(subtask))
    .filter((v): v is SubagentCard => v?.root.parentElement === st.bodyEl);
  const first = promoted[0];
  if (first === undefined) {
    placeContainer(st, st.bodyEl, box.root, st.containerAt.get(`pipe:${pipelineID}`));
  } else {
    // Lands where the first adopted stage sat, keeping transcript order, and not
    // through `appendBlock`: nothing is posted after an open trace by swapping a
    // node already in place.
    first.root.replaceWith(box.root);
  }
  for (const v of promoted) {
    box.body.appendChild(v.root);
  }
  // A box the STAGE path built paints itself, because nothing else will: the driver's
  // effect re-runs only when its tool call CHANGES, and a settled driver never does.
  const driver = peekToolCallSig(st.chatID, pipelineID);
  if (driver !== undefined) {
    paintPipeline(st, box, driver);
  }
  // And the BINDING, for the same reason the subagent box binds itself: a box the
  // stage path built has no subscription and no ledger until its own invocation
  // entry mounts, which a window need never reach.
  const inv = callByToolID(st, pipelineID);
  if (inv !== undefined && isPipelineInvocation(inv.call)) {
    bindPipeline(st, inv.call, live, inv.seq);
  }
  return box;
}

/** The disclosure a transcript run card reads and writes: the reader's own recorded
 *  state, this pass's newest-element verdict, and where a reader's flip goes.
 *
 *  `st` is a parameter because the verdict lives on the render: `indexGroups` resolves
 *  it once per pass, and this is read at the card's creation site. */
function runDisclosure(st: TurnRender, workflowID: string): RunDisclosure {
  const key = `run:${workflowID}`;
  return {
    wasOpen: () => containerOpen(key),
    defaultOpen: st.boxExpanded.get(key) ?? true,
    onOpenChange: (open) => {
      setContainerOpen(key, open);
    },
  };
}

/** Get or build the run card for one workflow id, and subscribe it to the store.
 *
 *  The subscription is the whole reason the card needs no event handling of its
 *  own: `run-store.ts` owns the fetch and holds a signal per run, so one effect
 *  per card re-renders it whenever that run changes and nothing else does. */
function runCardFor(st: TurnRender, workflowID: string, name: string, owner = false): RunCardView {
  const existing = st.runs.get(workflowID);
  if (existing !== undefined) {
    reseatInserted(st, st.bodyEl, existing.root);
    return existing;
  }
  // The launch call is the card's only witness for its label and for a launch that
  // FAILED. `st.calls` order is the turn's own `seq` order, so on the ordinary turn this
  // is the owner itself; it differs only where the owner is a LATER mention, whose
  // label is the placeholder either way.
  const launch = st.calls.find((c) => c.runID === workflowID)?.call;
  if (!st.detached) {
    // ONE box per run per TRANSCRIPT, not per render. Still live under the owner rule,
    // because ownership is derived over the RESIDENT window: loading an older page can
    // put an earlier mention of the same run ahead of the current owner, so the card
    // re-homes into that render rather than being rebuilt beside itself. Step rows are
    // keyed by node path, so the move lands in the right row.
    const host = runCardHosts.get(st.chatID)?.get(workflowID);
    const hosted = host?.runs.get(workflowID);
    if (hosted !== undefined && host !== undefined) {
      if (owner) {
        adoptRunCard(st, host, workflowID, hosted);
      }
      return hosted;
    }
    let hosts = runCardHosts.get(st.chatID);
    if (hosts === undefined) {
      hosts = new Map();
      runCardHosts.set(st.chatID, hosts);
    }
    hosts.set(workflowID, st);
  }
  // The footer link re-opens the run's tab. Injected here rather than imported by
  // the card, so `fundamentals/` keeps pointing only downward — and lazily, because
  // `run-view.ts` reaches the whole run page and the transcript must not carry it.
  // The parent is THIS render's chat; the run store's own record of it is fed by SSE
  // and answers nothing before the first frame.
  const chatID = st.chatID;
  const card = buildRunCard(
    workflowID,
    launch === undefined ? name : recipeNameOf(launch),
    (id, label, focusNode) => {
      void import("./run-view.js")
        .then(({ openRunView }) => {
          // The third argument is what makes a step row a DOOR: two args means "the
          // run", a row passes its own node path and means "the run, at this step".
          void openRunView(id, label, chatID, focusNode ?? "");
        })
        .catch(() => {
          /* noop: the link degrades to its href on the next click */
        });
    },
    st.detached ? undefined : runDisclosure(st, workflowID),
  );
  st.runs.set(workflowID, card);
  placeContainer(st, st.bodyEl, card.root, st.containerAt.get(`run:${workflowID}`));
  pushDisposer(st, -1, () => {
    releaseRunCard(st, workflowID, card);
  });
  // The first read the card ever gets; every later one arrives through the effect.
  invalidateRun(workflowID);
  armRunCard(st, workflowID, card);
  if (launch !== undefined) {
    card.setLaunch(launch.status, launch.output);
  }
  return card;
}

/** Give up this render's claim on `workflowID`'s card: its effect, its clock hold, the host
 *  slot, and the store's cell. Idempotent, which is what lets the render lifetime and the
 *  entry lifetime share one function. */
function releaseRunCard(st: TurnRender, workflowID: string, card: RunCardView): void {
  disarmRunCard(st, workflowID, card);
  // Slot and cell together, and only while this render holds the claim: a re-homed
  // card outlives its old host's dispose, and its effect still reads that cell.
  const hosts = runCardHosts.get(st.chatID);
  if (hosts?.get(workflowID) !== st) {
    return;
  }
  hosts.delete(workflowID);
  if (hosts.size === 0) {
    runCardHosts.delete(st.chatID);
  }
  // The store's cache bound, and UNCONDITIONAL: which surfaces still need this run's
  // cell is a registered demand's answer rather than a call site's (`run-store.ts`).
  forgetRun(workflowID);
}

/** Move `workflowID`'s card out of `host` and into `st`, claim and all; `seat` is the node to
 *  place it before, absent meaning the mount position. Moving the NODE keeps the element, so no
 *  effect churns and no entry animation replays. */
function adoptRunCard(
  st: TurnRender,
  host: TurnRender,
  workflowID: string,
  card: RunCardView,
  seat?: HTMLElement | null,
): void {
  host.runs.delete(workflowID);
  const stop = host.runEffects.get(workflowID);
  if (stop !== undefined) {
    host.runEffects.delete(workflowID);
    st.runEffects.set(workflowID, stop);
  }
  st.runs.set(workflowID, card);
  let hosts = runCardHosts.get(st.chatID);
  if (hosts === undefined) {
    hosts = new Map();
    runCardHosts.set(st.chatID, hosts);
  }
  hosts.set(workflowID, st);
  if (seat === undefined) {
    placeInContainer(st, st.bodyEl, card.root);
  } else {
    st.bodyEl.insertBefore(card.root, seat);
  }
  pushDisposer(st, -1, () => {
    releaseRunCard(st, workflowID, card);
  });
}

/** Adopt the launch tool call into its run's card, and answer with the card's root: the recipe
 *  name from the call's input as a placeholder label, and a failed launch reported rather than
 *  lost. A launch that FAILED never created a run, so `GET /api/runs/{id}` has nothing and the
 *  card would sit at "starting" forever with the tool call its only witness. */
function bindRunCard(st: TurnRender, workflowID: string, tc: ToolCall): HTMLElement {
  const card = runCardFor(st, workflowID, recipeNameOf(tc), true);
  card.setLaunch(tc.status, tc.output);
  return card.root;
}

/** The recipe a launch names, from the tool call's own input. A placeholder only:
 *  every render prefers the run's `runLabel`. */
function recipeNameOf(tc: ToolCall): string {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const rec = input as Record<string, unknown>;
    for (const key of ["workflowPath", "recipe", "name"]) {
      const v = rec[key];
      if (typeof v === "string" && v !== "") {
        // A path's last segment without its extension reads as the recipe name.
        const base = v.split("/").pop() ?? v;
        return base.replace(/\.(ya?ml|json)$/i, "");
      }
    }
  }
  return "Workflow run";
}

// The shared run clock: ONE interval for every card on screen, stopped when no card
// holds it. Holders are REFCOUNTED per workflow id — the same run can be on screen
// more than once (a parked chat's card, a subagent page's, the composer band's run
// bar), so a release names WHICH holder let go.

/** What the clock needs of a holder, which is one method. Narrower than `RunCardView`
 *  because the second consumer is `run-bar.ts`, not a card. */
export interface RunClockHolder {
  tick(): void;
}

const clockHolders = new Map<string, Set<RunClockHolder>>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

/** Tick this run's clock once a second while `holder` is showing it. Refcounted;
 *  `releaseRunClock` is the other half and both are idempotent. */
export function holdRunClock(workflowID: string, card: RunClockHolder): void {
  let holders = clockHolders.get(workflowID);
  if (holders === undefined) {
    holders = new Set();
    clockHolders.set(workflowID, holders);
  }
  holders.add(card);
  clockTimer ??= setInterval(() => {
    for (const set of clockHolders.values()) {
      for (const c of set) {
        c.tick();
      }
    }
  }, 1000);
}

/** Stop ticking for one holder. The interval dies with the last one. */
export function releaseRunClock(workflowID: string, card: RunClockHolder): void {
  const holders = clockHolders.get(workflowID);
  if (holders === undefined) {
    return;
  }
  holders.delete(card);
  if (holders.size === 0) {
    clockHolders.delete(workflowID);
  }
  if (clockHolders.size === 0 && clockTimer !== undefined) {
    clearInterval(clockTimer);
    clockTimer = undefined;
  }
}

/** Subscribe a run card to its store cell and hold the shared clock. `disarmRunCard`
 *  is the suspend half; both are idempotent. */
function armRunCard(st: TurnRender, workflowID: string, card: RunCardView): void {
  if (st.runEffects.has(workflowID)) {
    return;
  }
  const stop = effect(() => {
    // Two inputs on different clocks: `inspect` says what the steps are doing, the
    // dock says which is blocked on a person. The run's status cannot carry the second
    // — KAS blocks the asking step's turn and leaves the run `running`.
    card.render(runState(workflowID), runPendingAsks(workflowID));
  });
  st.runEffects.set(workflowID, stop);
  holdRunClock(workflowID, card);
}

function disarmRunCard(st: TurnRender, workflowID: string, card: RunCardView): void {
  const stop = st.runEffects.get(workflowID);
  if (stop === undefined) {
    return;
  }
  stop();
  st.runEffects.delete(workflowID);
  releaseRunClock(workflowID, card);
}

/** Whether this entry is a DELEGATE's invocation: a `tool_call` in the view's own root lane
 *  whose payload names a subtask, which is the one entry of a delegate's this view draws —
 *  as its card. The lane test is what makes the rule relative: on the delegate's own page
 *  the root IS that uuid, so a nested invocation there is a card for the grandchild. */
function isDelegateInvocation(st: TurnRender, e: Entry): boolean {
  const call = payloadOf(e, "tool_call");
  return (
    call !== undefined &&
    (e.lane ?? "") === st.lane &&
    (call.agent_subtask_id ?? "") !== "" &&
    isSubagentInvocation(call)
  );
}

function placeEntry(st: TurnRender, e: Entry, live: boolean, idx: GroupIndex): void {
  const container = st.bodyEl;
  const seq = e.seq;
  // Switched on the UNION rather than on `e.kind`, so an arm that hands the entry on
  // hands over a payload narrowed to its kind — `payloadOf` is for the arms that read
  // one field, and this is the one that passes the whole entry across a seam.
  const u = entryUnion(e);
  switch (u.kind) {
    case "turn_open":
    case "turn_close":
    case "turn_bind":
    case "tool_result":
    case "reconciled":
      // Rendered elsewhere, or nowhere: the header, the footer, the call's own card, and
      // for a `reconciled` nothing at all — it states that a merge had nothing to add.
      // `entryRenders` already refuses these, so this arm is the switch's totality
      // rather than a gate.
      return;
    case "text":
    case "steer_ack": {
      // A `steer_ack` is AGENT content at its own position: the words are the
      // model's, so they render as the model's rather than inside the reader's note — in a
      // bubble of its OWN, because it renders and therefore ends the run it interrupted,
      // which is the boundary `proseRunAt` already draws. A `text` entry reaching this arm
      // is a one-member run: `renderRange` groups before it dispatches.
      mountProse(st, container, seq, [seq], live);
      return;
    }
    case "thinking": {
      mountThinking(st, container, e, live, idx);
      return;
    }
    case "steer": {
      const steer = payloadOf(e, "steer");
      if (steer !== undefined) {
        mountSteerNote(st, container, e, steer);
      }
      return;
    }
    case "plan": {
      mountPlan(st, container, e);
      return;
    }
    case "compaction":
    case "compaction_failed":
    case "safety_blocked":
    case "model_switched":
    case "mode_switched":
    case "turn_revert": {
      const row = cbs.makeEvent(u);
      stampEntry(st, row, seq);
      appendEntry(st, container, row);
      return;
    }
    case "tool_call": {
      const held = st.callByEntry.get(e.id);
      if (held === undefined) {
        return;
      }
      const tc = held.call;
      // Only matches TRANSCRIPTS PERSISTED BEFORE 2026-08-31, when the engine stopped
      // emitting these: their card sits stuck at in_progress forever. Title-keyed
      // because the persisted call carries no tool id of its own.
      if (isInternalToolTitle(tc.title)) {
        return;
      }
      const subtask = tc.agent_subtask_id ?? "";
      // A WORKFLOW LAUNCH becomes the run's card, not a tool row. The call has no
      // subtask of its own, so this branch is ahead of the subtask checks. Only the
      // run's OWNER takes it: a later mention — an `inspect_workflow` on the same run —
      // renders as an ordinary tool card, because taking this branch would seat its
      // entry on the card's element and clobber the launch's state with the inspect's.
      if (subtask === "" && held.runID !== "" && ownsRunCard(st, held.runID, e.id)) {
        // Stamped like every other kind, so a search hit on the launch call
        // resolves to the card it opened rather than to the whole turn.
        stampEntry(st, bindRunCard(st, held.runID, tc), seq);
        return;
      }
      // A PIPELINE LAUNCH becomes the pipeline's box, not a tool row — same shape as
      // the workflow launch above, and ahead of the subtask checks for the same reason.
      if (subtask === "" && isPipelineInvocation(tc)) {
        bindPipeline(st, tc, live, seq);
        return;
      }
      // The subagent invocation BECOMES the delegate's card rather than a tool row, and
      // is its only creator. Stamped ahead of the bind, which returns early for a card
      // already bound, so a search hit on the invocation resolves to the card.
      if (isDelegateInvocation(st, e)) {
        const sa = subagentCardFor(st, subtask, live);
        stampEntry(st, sa.root, seq);
        bindSubagent(st, subtask, sa, tc, seq);
        return;
      }
      if (isTodoTool(tc)) {
        mountTodo(st, container, tc, seq);
        return;
      }
      // The run's key AND the verdict about it, resolved here because only this pass
      // holds the index: the group's own region is created in that state rather than
      // opened and folded by `syncContainerCollapse` a few statements later.
      const runStart = groupRunStart(idx, st.lane, seq);
      mountToolCard(st, container, st.lane, tc, seq, runStart, runFollowed(idx, st.lane, runStart));
      return;
    }
    default: {
      // Totality, held by the compiler rather than by a comment: a kind added to
      // AnyEntry with no arm here is a type error at `_never`, not a blank card body.
      // RETURNED rather than discarded, because `noUnusedLocals` reads a bare binding
      // as dead and the design's own spelling does not compile under this config.
      const _never: never = u;
      return _never;
    }
  }
}

/** A `steer_ack`'s own words, which the payload carries as `text`. */
function ackText(e: Entry): string {
  return payloadOf(e, "steer_ack")?.text ?? "";
}

// Entry mounters

/** Record `el` as `seq`'s element and stamp both coordinates on it. The map answers
 *  every lookup; the attributes serve the one consumer that starts from an ELEMENT —
 *  the anchor ladder — which is why the owning turn id is stamped beside the `seq`: a
 *  card in this body can carry another turn's numbering. */
function stampEntry(st: TurnRender, el: HTMLElement, seq: number): void {
  el.dataset["entrySeq"] = String(seq);
  el.dataset["entryTurn"] = st.turnID;
  const e = entryAt(st.turn, seq);
  if (e !== undefined) {
    el.dataset["entryId"] = e.id;
  }
  st.entryEls.set(seq, el);
}

/** One member's own source text, whichever kind supplies it. */
function entryTextAt(st: TurnRender, seq: number): string {
  const e = entryAt(st.turn, seq);
  if (e === undefined) {
    return "";
  }
  return payloadOf(e, "text")?.text ?? ackText(e);
}

/** The concatenation of `seqs`' texts, in order. No marker node: the token set has no
 *  raw-HTML construct, so a `<span>` in the input renders as text. */
function proseText(st: TurnRender, seqs: readonly number[]): string {
  let out = "";
  for (const seq of seqs) {
    out += entryTextAt(st, seq);
  }
  return out;
}

/** The lane's open entry, whatever its kind. */
function openEntryOf(st: TurnRender): OpenEntry | undefined {
  return st.turn.openEntries.get(st.lane);
}

/** `id`'s open text, or `""` once that entry has sealed and left `openEntries`. */
function openTextAt(st: TurnRender, id: string): string {
  const open = openEntryOf(st);
  return open?.id === id ? open.text : "";
}

/** The run's ONE source: its sealed members' texts, then its open tail's. */
function runText(st: TurnRender, view: ProseRun): string {
  const sealed = proseText(st, view.seqs);
  return view.openID === null ? sealed : sealed + openTextAt(st, view.openID);
}

/** Mount `seqs` (a prose run's members, in order) into the run headed by `head`, building
 *  the bubble when this render does not hold it yet. EXTENDS an existing run rather than
 *  mounting a second beside it, which is the common live shape: `updateBody` renders
 *  `[st.window.to, want.to)`, so a newest `text` entry continuing a mounted run arrives as
 *  a range starting mid-run, and a second bubble there IS the seam 8.5 removes. */
function mountProse(
  st: TurnRender,
  container: HTMLElement,
  head: number,
  seqs: readonly number[],
  live: boolean,
): void {
  // Built WITH the run's text rather than filled afterwards: the reveal buffer sees only
  // GROWTH, so text present at mount is history and paints in one pass (`reveal.ts`).
  const view =
    st.proseRuns.get(head) ??
    adoptOpenRun(st, head, seqs) ??
    buildProseRun(st, container, head, proseText(st, seqs), live);
  for (const seq of seqs) {
    joinProseRun(st, view, seq);
  }
}

/** The open-only run whose entry SEALED into one of `seqs`, re-keyed under the position the
 *  seal assigned it. Without it that seal would mount a second bubble beside the one already
 *  streaming its text, which is the seam a prose run exists to remove. */
function adoptOpenRun(st: TurnRender, head: number, seqs: readonly number[]): ProseRun | undefined {
  const tail = st.openTail;
  if (tail?.kind !== "text" || tail.run.head >= 0) {
    return undefined;
  }
  if (!seqs.some((seq) => entryAt(st.turn, seq)?.id === tail.id)) {
    return undefined;
  }
  const view = tail.run;
  view.head = head;
  st.proseRuns.set(head, view);
  stampEntry(st, view.row, head);
  return view;
}

function buildProseRun(
  st: TurnRender,
  container: HTMLElement,
  head: number,
  initial: string,
  live: boolean,
): ProseRun {
  // Only a LIVE transcript bubble joins the anchor registry; its seal callback clears
  // this render's slot. A seal INSIDE the run never fires it, because only a run END
  // calls the bubble's own `seal()`.
  const topLive = live && !st.detached;
  // Every bubble carries a row: the only container a bubble is mounted into is this
  // render's own turn body. Created BEFORE the bubble so the initial blank report
  // lands on it.
  const row = cbs.makeRow();
  const opts: AssistantBubbleOpts = {
    onBlankChange: (blank): void => {
      row.classList.toggle("is-empty", blank);
    },
  };
  if (topLive) {
    opts.onSeal = (root): void => {
      if (st.topLiveEl === root) {
        st.topLiveEl = null;
      }
      clearLiveAnchor(root);
    };
  }
  const bubble = buildAssistantBubble(initial, live, opts);
  st.bubbles.push(bubble);
  if (live) {
    st.liveBubble = bubble;
  }
  if (topLive) {
    st.topLiveEl = bubble.root;
    liveAnchor = { renderID: st.renderID, el: bubble.root };
  }
  const view: ProseRun = { head, row, bubble, seqs: [], openID: null };
  row.appendChild(bubble.root);
  // A run of an OPEN entry alone has no position to be keyed or stamped by: `adoptOpenRun`
  // seats it when the seal assigns one.
  if (head >= 0) {
    st.proseRuns.set(head, view);
    // The ROW is the element whose removal drops the run, and it is stamped for the run's
    // FIRST entry: `data-entries` is what carries the rest.
    stampEntry(st, row, head);
  }
  appendEntry(st, container, row);
  return view;
}

/** Seat one SEALED member on `view`: the shared element, the shared sink, and the run's own
 *  `data-entries`. No subscription — a sealed entry's text is frozen and the store has
 *  retired its signal, so only the open tail streams. */
function joinProseRun(st: TurnRender, view: ProseRun, seq: number): void {
  const e = entryAt(st.turn, seq);
  if (e === undefined || view.seqs.includes(seq)) {
    return;
  }
  // The open tail HAVING SEALED: the words are the same and now carry a position, so the
  // tail retires and its entry joins as any other member.
  if (st.openTail?.id === e.id) {
    releaseOpenTail(st);
  }
  view.seqs.push(seq);
  st.runHead.set(seq, view.head);
  st.entryEls.set(seq, view.row);
  writeRunEntries(st, view);
  // EVERY member's sink writes the WHOLE run, so a fold cannot leave a run showing one
  // entry's text; `syncMountedText` calls the head's alone.
  st.entryText.set(seq, () => {
    view.bubble.setText(runText(st, view));
  });
  view.bubble.setText(runText(st, view));
}

/** The run's member ids in stream order, the open tail's last: what the element carries in
 *  place of a marker node, and what an entry id is resolved through. */
function writeRunEntries(st: TurnRender, view: ProseRun): void {
  const ids = view.seqs.map((seq) => entryAt(st.turn, seq)?.id ?? "");
  if (view.openID !== null) {
    ids.push(view.openID);
  }
  view.row.dataset["entries"] = ids.join(" ");
}

// The OPEN entry: what streams

/** Mount, keep or retire the lane's OPEN entry. It renders at the TAIL of its
 *  lane's view — for lane `""` the streaming bubble at the bottom of the card — and it is
 *  the one entry whose text still grows, so it is also the one thing that subscribes.
 *
 *  Runs AFTER the range, so a seal that landed in this pass has already taken over the
 *  surface at the position it was assigned. */
function syncOpenTail(st: TurnRender, live: boolean): void {
  const open = openEntryOf(st);
  const tail = st.openTail;
  if (tail !== null && tail.id !== open?.id) {
    // The seal clears the tail as it takes the surface over, so an entry whose id has
    // changed with the tail still standing was NOT adopted: its surface has no position and
    // would be drawn a second time when the seal reaches its own.
    dropOpenSurface(st, tail);
    releaseOpenTail(st);
  }
  // Liveness is the STORE's, not the caller's flag: an open entry with no `turn_close`
  // beside it is a turn still being written, and it is the only shape that can stream. A
  // stranded open entry — a bridge death, whose close the appender synthesizes — mounts
  // nothing, and `live` is left to decide the caret alone.
  if (open === undefined || st.openTail !== null || closeOfBody(st.turn.body) !== undefined) {
    return;
  }
  if (open.kind === "thinking") {
    mountOpenThinking(st, open);
    return;
  }
  mountOpenProse(st, open, live);
}

/** The open text entry as the tail member of its run: the mounted run its lane's newest
 *  rendering entry sits in, or a run of its own when the entry below it is not prose. */
function mountOpenProse(st: TurnRender, open: OpenEntry, live: boolean): void {
  const tailSeq = lastRenderedSeq(st);
  if (tailSeq >= 0 && (tailSeq < st.window.from || tailSeq >= st.window.to)) {
    // The lane's tail is paged out, so there is nothing on screen for the entry to sit
    // after; it mounts when the window reaches its seal.
    return;
  }
  const run = tailSeq < 0 ? null : proseRunAt(st.turn, tailSeq, st.lane, st.firstPlan);
  const view =
    (run === null ? undefined : st.proseRuns.get(run.from)) ??
    buildProseRun(st, st.bodyEl, -1, open.text, live);
  view.openID = open.id;
  writeRunEntries(st, view);
  // The prefix is captured ONCE, which holds because the appender seals a lane's open entry
  // before it appends anything to that lane: no sealed member can join this run while the
  // tail is mounted, so `watchOpenText`'s full-write fallback cannot go stale.
  const sealed = proseText(st, view.seqs);
  // A no-op on the run just built for it, which carries the text already.
  view.bubble.setText(sealed + open.text);
  const stop = watchOpenText(st, open, sealed.length, (full, delta) => {
    if (delta === null) {
      view.bubble.setText(sealed + full);
    } else {
      view.bubble.append(delta);
    }
  });
  st.openTail = { kind: "text", id: open.id, run: view, stop };
}

/** The open thinking entry as its own live trace. EXPANDED and left as the container's open
 *  trace by construction: nothing can be posted after an entry that has not sealed. */
function mountOpenThinking(st: TurnRender, open: OpenEntry): void {
  const view = buildReasoning(open.text, true, true);
  st.reasonings.push(view);
  appendEntry(st, st.bodyEl, view.root);
  st.openReasoning.set(st.bodyEl, view);
  const stop = watchOpenText(st, open, 0, (full) => {
    // No watermark: the trace's own setText renders only the tail past what it holds.
    view.setText(full);
  });
  st.openTail = { kind: "thinking", id: open.id, view, stop };
}

/** Subscribe to `open`'s streaming signal, reporting each write as `(full, delta)` with a
 *  null delta when the write does not bridge what the surface has accepted — a missed or
 *  replayed write, or a cell that advanced before this subscription existed. `prefix` is the
 *  text ahead of this entry on the surface, which no write of its own can move. */
function watchOpenText(
  st: TurnRender,
  open: OpenEntry,
  prefix: number,
  write: (full: string, delta: string | null) => void,
): () => void {
  const sig = ensureEntryTextSig(st.turnID, open.id, open.text);
  let accepted = prefix + open.text.length;
  return effect(() => {
    const v = sig.value;
    write(v.full, accepted + v.delta.length === prefix + v.full.length ? v.delta : null);
    accepted = prefix + v.full.length;
  });
}

/** Remove the surface an open entry mounted for itself — an open-only run's row, a live
 *  trace. Neither carries a `seq`, so no window drop reaches either; a run that has since
 *  been seated belongs to its members and is left alone. */
function dropOpenSurface(st: TurnRender, tail: OpenTail): void {
  if (tail.kind === "thinking") {
    st.reasonings = st.reasonings.filter((view) => view !== tail.view);
    for (const [container, open] of st.openReasoning) {
      if (open === tail.view) {
        st.openReasoning.delete(container);
      }
    }
    tail.view.root.remove();
    return;
  }
  if (tail.run.head >= 0) {
    return;
  }
  const { bubble, row } = tail.run;
  bubble.finishNow();
  st.bubbles = st.bubbles.filter((b) => b !== bubble);
  if (st.liveBubble === bubble) {
    st.liveBubble = null;
  }
  if (st.topLiveEl === bubble.root) {
    clearLiveAnchor(bubble.root);
    st.topLiveEl = null;
  }
  row.remove();
}

/** Retire the mounted open tail: the subscription goes and a run holding it drops back to
 *  its sealed members. The ELEMENT stays — either the seal has just taken it over, or it is
 *  the reader's live edge and the run keeps whatever it painted. */
function releaseOpenTail(st: TurnRender): void {
  const tail = st.openTail;
  if (tail === null) {
    return;
  }
  st.openTail = null;
  tail.stop();
  if (tail.kind === "text") {
    tail.run.openID = null;
    writeRunEntries(st, tail.run);
  }
}

/** The greatest `seq` this view renders at, or -1: where the lane's tail is, which is what
 *  the open entry sits after. */
function lastRenderedSeq(st: TurnRender): number {
  for (let seq = turnSpan(st.turn) - 1; seq > 0; seq--) {
    const e = entryAt(st.turn, seq);
    if (e !== undefined && entryRenders(e, st.lane, st.firstPlan)) {
      return seq;
    }
  }
  return -1;
}

/** The steer note: the reader's own words at the `seq` the entry landed at, and never the
 *  agent's — the acknowledgement is its own entry a few rows down. `lane` is the marker for
 *  a steer a DELEGATE read: the note is in the parent's flow either way, so without it the
 *  reader cannot tell which agent their words reached. The ack fold reads every lane of the
 *  turn, because an ack keeps the lane of the text it was stripped from. */
function mountSteerNote(st: TurnRender, container: HTMLElement, e: Entry, steer: EntrySteer): void {
  const data: SteerNoteData = {
    text: steer.text,
    origin: steer.origin,
    dropped: steer.state === "dropped",
    acknowledged: st.turn.body.some((o) => o.kind === "steer_ack" && o.id === steerAckID(e.id)),
    lane: e.lane ?? "",
  };
  if (steer.reason !== undefined) {
    data.reason = steer.reason;
  }
  const note = buildSteerNote(data);
  stampEntry(st, note, e.seq);
  appendEntry(st, container, note);
}

function mountThinking(
  st: TurnRender,
  container: HTMLElement,
  e: Entry,
  live: boolean,
  idx: GroupIndex,
): void {
  const seq = e.seq;
  const initial = payloadOf(e, "thinking")?.text ?? "";
  // The OPEN trace having sealed: its element is already at the tail with its words in it,
  // so the seal only gives it a position, and re-mounting would draw the trace twice.
  const tail = st.openTail;
  const adopted = tail?.kind === "thinking" && tail.id === e.id ? tail.view : undefined;
  if (adopted === undefined && initial === "") {
    return; // an empty settled "Thinking completed" dropdown is worse than none
  }
  const host = st.lane;
  // Sealed from the STORE, not from what arrives next: a trace the store already has a
  // successor for is finished however this range reached it.
  const followed = containerFollowed(idx, host, seq);
  // The disclosure is POSITIONAL and the pulse is not: a settled trace that is still
  // the newest element in its lane renders EXPANDED, while a live trace something has
  // already been posted after renders collapsed.
  const view = adopted ?? buildReasoning(initial, live, rendersExpanded(idx, host, seq));
  if (adopted === undefined) {
    st.reasonings.push(view);
  } else {
    releaseOpenTail(st);
  }
  st.entryText.set(seq, (full) => {
    view.setText(full);
  });
  // Append (sealing any open predecessor) BEFORE registering the new view, or
  // appendEntry would seal the trace being mounted.
  stampEntry(st, view.root, seq);
  if (adopted === undefined) {
    appendEntry(st, container, view.root);
  }
  // `openReasoning` is a container's genuinely-live TRAILING trace, which is why head
  // insertion never reaches it.
  if (followed) {
    view.seal();
  } else {
    st.openReasoning.set(container, view);
  }
}

function mountToolCard(
  st: TurnRender,
  container: HTMLElement,
  key: string,
  tc: ToolCall,
  seq: number,
  runStart: number,
  superseded: boolean,
): void {
  const group = toolGroupFor(st, container, key, runStart);
  // BEFORE the refresh below, which is the group's one disclosure-creation site: a
  // verdict arriving after it would reach a region already open, and closing that is
  // the animation this ordering exists to avoid.
  setGroupSuperseded(group, superseded);
  // A card whose details the reader had open is built open rather than opened after
  // the mount, for the group's reason one level down: the region's open height would
  // be committed first and the reveal would animate on a repaint.
  const card = mountToolCallCard(st.chatID, tc, containerOpen(`tool:${tc.id}`) === true);
  card.setAttribute(RECONCILE_KEY, tc.id);
  stampEntry(st, card, seq);
  // Cards live in the group's body region (the disclosure-collapsible
  // container), not on the group root beside the header.
  placeInContainer(st, groupBody(group), card);
  refreshGroupHeader(group);
  // The slot is THIS render's, disposed with it: the transcript's card and the
  // subagent page's detached card for the same call come and go independently
  // (the slot registry is a multimap). st.disposers, not pushLifetimeEffect —
  // a transcript card outlives turn end, and park suspends it through the
  // registry rather than disposing it.
  pushDisposer(st, seq, () => {
    disposeToolSlot(st.chatID, tc.id, card);
  });
}

function mountTodo(st: TurnRender, container: HTMLElement, tc: ToolCall, seq: number): void {
  const list = buildTodoList(parseTodoItems(tc));
  list.dataset["toolId"] = tc.id;
  stampEntry(st, list, seq);
  appendEntry(st, container, list);
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  const cleanup = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    updateTodoList(list, parseTodoItems(next));
    last = next;
  });
  pushLifetimeEffect(st, seq, () => {
    cleanup();
    releaseToolSig(st, tc.id);
  });
}

/** Wire the PIPELINE invocation's SHAPE and header onto its box, and the box's
 *  footer ledger onto every stage's members.
 *
 *  A PROMOTED pipeline paints nothing, so `driverNeedsBox` gates the whole paint; the
 *  writing is `paintPipeline`, which `pipelineBoxFor` also runs. Not folded into
 *  `bindSubagent`: the label comes from the stage COUNT and the ledger sums across
 *  stages rather than one subtask's members. */
function bindPipeline(st: TurnRender, tc: ToolCall, live: boolean, seq: number): void {
  const key = `pipe:${tc.id}`;
  if (st.boxBindings.has(key)) {
    return;
  }
  // Reserved before the first paint, which reaches `pipelineBoxFor` — and that binds the
  // box it builds, so an unreserved key re-enters here and leaks the inner subscription.
  let dispose: (() => void) | null = null;
  const release = (): void => {
    if (st.boxBindings.get(key) !== release) {
      return;
    }
    st.boxBindings.delete(key);
    dispose?.();
    releaseToolSig(st, tc.id);
  };
  st.boxBindings.set(key, release);
  const paint = (next: ToolCall): void => {
    if (!driverNeedsBox(st, next)) {
      return;
    }
    const box = pipelineBoxFor(st, tc.id, live);
    // Stamped HERE rather than at the call site: the box does not exist for a
    // single-stage pipeline, and the upgrade that builds one replaces the node it lands
    // on, so the stamp has to follow whatever this paint's box currently is.
    stampEntry(st, box.root, seq);
    paintPipeline(st, box, next);
  };
  paint(tc);
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  dispose = effect(() => {
    const next = sig.value;
    if (next === last) {
      return;
    }
    paint(next);
    last = next;
  });
  pushLifetimeEffect(st, seq, release);
}

/** The footer outcome a settled invocation earns. `aborted` is its OWN outcome rather
 *  than folding onto `completed`: a delegate the reader stopped produced no result, and
 *  the footer's tint and lead word are what say so. */
function delegateOutcome(status: ToolStatus): TurnOutcome {
  switch (status) {
    case "failed":
      return "failed";
    case "aborted":
      return "cancelled";
    default:
      return "completed";
  }
}

/** The pipeline's ledger: every stage's members, summed. Changed files merge BY PATH
 *  rather than adding counts — two stages that touched one file each report that
 *  file's own totals, and adding them would double-count it. */
function pipelineSummary(st: TurnRender, invocation: ToolCall): TurnSummaryData {
  let toolMs = 0;
  let delegateCount = 0;
  let delegateMs = 0;
  const kindCounts: Partial<Record<ToolKind, number>> = {};
  const changed: Record<string, FileChange> = {};
  for (const subtask of st.pipelineStages.get(invocation.id) ?? []) {
    const stage = subagentSummary(st, subtask, invocation);
    toolMs += stage.toolMs ?? 0;
    for (const [kind, n] of Object.entries(stage.kindCounts ?? {}) as [ToolKind, number][]) {
      kindCounts[kind] = (kindCounts[kind] ?? 0) + n;
    }
    // The STAGES are this pipeline's delegates. Their own nested counts are
    // deliberately not folded in: a grandchild is the stage's delegate, and adding it
    // here would make "3 delegates" mean something different per pipeline.
    delegateCount++;
    delegateMs += stage.elapsedMs ?? 0;
    for (const [path, ch] of Object.entries(stage.changedFiles ?? {})) {
      const cur = changed[path] ?? { lines_added: 0, lines_removed: 0 };
      changed[path] = {
        lines_added: cur.lines_added + ch.lines_added,
        lines_removed: cur.lines_removed + ch.lines_removed,
      };
    }
  }
  const out: TurnSummaryData = {
    changedFiles: changed,
    toolMs,
    kindCounts,
    delegateCount,
    delegateMs,
    startedAt: invocation.ts,
  };
  if (!isToolActive(invocation.status)) {
    out.outcome = delegateOutcome(invocation.status);
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
      out.endedAt = invocation.ts + elapsed;
    }
  }
  return out;
}

/** Write the invocation call's identity, status and footer ledger onto a card. The whole of
 *  what a card shows, so a seat that has the call but not its block reads the same.
 *
 *  `live` is the CHAT's own turn liveness, the delegate's second input: a call still reading
 *  `in_progress` in a chat holding no live turn is a stale spinner and paints the stopped
 *  treatment instead (`store.ts` `delegateStatusFor`). */
function paintSubagent(
  st: TurnRender,
  subtask: string,
  sa: SubagentCard,
  tc: ToolCall,
  live: boolean,
): void {
  sa.setName(subagentLabel(tc));
  sa.setIcon(iconForSubagent(subagentName(tc)));
  sa.setStatus(delegateStatusFor(tc.status, live));
  sa.setSummary(subagentSummary(st, subtask, tc));
}

/** Wire the subagent invocation tool's status/name/icon onto its card, its footer ledger
 *  onto the members' current state, and — while it works — its rolling tail onto the
 *  delegate's own blocks in the store. */
function bindSubagent(
  st: TurnRender,
  subtask: string,
  sa: SubagentCard,
  tc: ToolCall,
  seq: number,
): void {
  const key = `sub:${subtask}`;
  if (st.boxBindings.has(key)) {
    return;
  }
  let stopTail: (() => void) | null = null;
  let dispose: (() => void) | null = null;
  const release = (): void => {
    if (st.boxBindings.get(key) !== release) {
      return;
    }
    st.boxBindings.delete(key);
    stopTail?.();
    stopTail = null;
    dispose?.();
    releaseToolSig(st, tc.id);
  };
  st.boxBindings.set(key, release);
  // UNTRACKED at the seat (`get` is a peek), TRACKED in the effect below: the first paint
  // happens inside a render pass, and subscribing the whole render to a chat's liveness
  // would repaint every turn on every `busy_chats` frame.
  const seated = get(st.chatID);
  const seatedLive = seated === undefined || turnLive(seated);
  let effective = delegateStatusFor(tc.status, seatedLive);
  paintSubagent(st, subtask, sa, tc, seatedLive);
  // The tail exists only while the delegate does, so its subscription does too: a settled
  // card has no tail element, and the footer is its last word. Keyed on the EFFECTIVE
  // status, so a stale spinner gets no tail either.
  stopTail =
    isToolActive(effective) && !st.detached
      ? bindSubagentTail(st.chatID, subtask, (lines) => {
          sa.setTail(lines);
        })
      : null;
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  dispose = effect(() => {
    // READ FIRST, before any bail: this is the subscription that settles a delegate card in
    // the SAME pass as the `busy_chats` reconcile that clears its chat's latch, which is the
    // only thing that will ever say the turn ended (`handlers/system.ts` reconcileThinking).
    const live = chatTurnLive(st.chatID);
    const next = sig.value;
    const nextEffective = delegateStatusFor(next.status, live);
    if (next === last && nextEffective === effective) {
      return;
    }
    if (nextEffective !== effective) {
      effective = nextEffective;
      sa.setStatus(nextEffective);
      if (!isToolActive(nextEffective)) {
        stopTail?.();
        stopTail = null;
      }
    }
    const label = subagentLabel(next);
    if (label !== subagentLabel(last)) {
      sa.setName(label);
      sa.setIcon(iconForSubagent(subagentName(next)));
    }
    // The members settle BEFORE the invocation does (the delegate finishes last), so
    // the settle tick sees their final diffs.
    sa.setSummary(subagentSummary(st, subtask, next));
    last = next;
  });
  pushLifetimeEffect(st, seq, release);
}

/** The facts a delegate's footer can state honestly; credits and the resolved model are
 *  absent, nothing on this wire carrying them per delegate. Members come out of the
 *  MESSAGE's tool calls rather than the per-tool signals, which a mounted card mints and
 *  which would report zeros; membership is EITHER side's stamp. The store mutates that
 *  array in place, so a member's late diff is here by the time the invocation settles. */
function subagentSummary(st: TurnRender, subtask: string, invocation: ToolCall): TurnSummaryData {
  let toolMs = 0;
  let delegateCount = 0;
  let delegateMs = 0;
  const kindCounts: Partial<Record<ToolKind, number>> = {};
  const changed: Record<string, FileChange> = {};
  for (const held of st.calls) {
    const tc = held.call;
    // Membership is the ENTRY's LANE or the call's own stamp, and it needs both arms: a
    // delegate's calls are appended in its lane, while the invocation carries the stamp
    // and sits in the ISSUER's lane.
    if (
      (held.lane !== subtask && (tc.agent_subtask_id ?? "") !== subtask) ||
      tc.id === invocation.id
    ) {
      continue;
    }
    kindCounts[tc.kind] = (kindCounts[tc.kind] ?? 0) + 1;
    const ms = tc.duration_ms ?? 0;
    toolMs += ms;
    // A delegate this delegate dispatched. Counted by the same predicate the turn
    // ledger uses, so nesting is one rule rather than two — the member loop already
    // skips `invocation` itself, so a card cannot count as its own delegate.
    if (isSubagentInvocation(tc)) {
      delegateCount++;
      delegateMs += ms;
    }
    for (const d of tc.diffs ?? []) {
      // lineDelta, not stats(lineDiff(...)): it strips the trailing newline first, so
      // these match the server's numbers (internal/buffer/linediff.go).
      const s = lineDelta(d.old_text ?? "", d.new_text);
      const cur = changed[d.path] ?? { lines_added: 0, lines_removed: 0 };
      changed[d.path] = {
        lines_added: cur.lines_added + s.added,
        lines_removed: cur.lines_removed + s.removed,
      };
    }
  }
  const settled = !isToolActive(invocation.status);
  const out: TurnSummaryData = {
    changedFiles: changed,
    toolMs,
    kindCounts,
    delegateCount,
    delegateMs,
    // The invocation's own stamp, which is when the delegate was dispatched.
    startedAt: invocation.ts,
  };
  if (settled) {
    out.outcome = delegateOutcome(invocation.status);
    const elapsed = invocation.duration_ms ?? 0;
    if (elapsed > 0) {
      out.elapsedMs = elapsed;
      // DERIVED here, unlike a turn's, and the difference is the source: a turn has
      // wall-clock stamps at both ends and an agent-measured `turn_elapsed_ms` that
      // is not the span between them, while a delegate has one stamp and this call's
      // own measured duration. Withheld while the delegate runs and withheld when
      // nothing measured it, rather than reported as equal to the start.
      out.endedAt = invocation.ts + elapsed;
    }
  }
  return out;
}

// Reasoning + tool-group per-container bookkeeping

function sealReasoning(st: TurnRender, container: HTMLElement): void {
  const view = st.openReasoning.get(container);
  if (view !== undefined) {
    view.seal();
    st.openReasoning.delete(container);
  }
}

/** Append into a container, sealing the trace open there first.
 *
 *  The ONE door for "anything posted after an open trace ends it": the wire carries no
 *  thinking-ended signal, so the next element's arrival IS the end signal, and sealing
 *  at the append keeps the rule total — a mounter added later cannot reach the DOM
 *  without it. That IS the one policy, at trace scope: a successor being posted is what
 *  folds an element, so turn end folds nothing (`finalizeAssistantBody` only settles) and
 *  every container's own collapse is `syncContainerCollapse`'s, read off the store. */
function appendEntry(st: TurnRender, container: HTMLElement, el: HTMLElement): void {
  if (st.insertBefore === null) {
    // Only a TAIL append supersedes an open trace: an INSERTED entry is posted
    // after nothing, and the trace it would seal is BELOW it.
    sealReasoning(st, container);
  }
  placeInContainer(st, container, el);
}

/** Place `el` in `container`: before the insertion reference while a head extension is in
 *  flight, at the end otherwise. The reference is the container's first child when the extension
 *  first touches it, captured HERE because every creation path reaches a container through this
 *  function, and it does not move as the extension proceeds — which keeps the inserted ordinals
 *  ascending. */
function placeInContainer(st: TurnRender, container: HTMLElement, el: HTMLElement): void {
  const refs = captureInsertRef(st, container);
  if (refs === null) {
    container.appendChild(el);
    return;
  }
  container.insertBefore(el, refs.get(container) ?? null);
}

/** Record `container`'s insertion boundary on the extension's FIRST touch even when it is
 *  null, and answer the reference map. `has` rather than `?? capture`: a container the
 *  extension created is empty then, so a re-capture takes its own first member. */
function captureInsertRef(
  st: TurnRender,
  container: HTMLElement,
): Map<HTMLElement, HTMLElement | null> | null {
  const refs = st.insertBefore;
  if (refs !== null && !refs.has(container)) {
    refs.set(container, container.firstElementChild as HTMLElement | null);
  }
  return refs;
}

/** Bring an ALREADY-MOUNTED node down to the ordinal being inserted, and step the reference
 *  past it. The reference is the boundary between inserted and pre-existing content, so only a
 *  node at or BELOW it moves: a card the insertion itself placed is above it, and moving that one
 *  carries it past every ordinal mounted since. A step card's launch is the reachable case. */
function reseatInserted(st: TurnRender, container: HTMLElement, el: HTMLElement): void {
  const refs = captureInsertRef(st, container);
  if (refs === null) {
    return;
  }
  const ref = refs.get(container) ?? null;
  if (ref !== el) {
    if (
      ref === null ||
      (el.compareDocumentPosition(ref) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
    ) {
      return;
    }
    container.insertBefore(el, ref);
  }
  refs.set(container, el.nextElementSibling as HTMLElement | null);
}

/** Place a lazily-created CONTAINER where the STORE puts it: above the first mounted ordinal
 *  after `at`, the index establishing it. Seating it by the RANGE instead would leave it under
 *  ordinals the store puts after it — one run split into two groups, and one window reached two
 *  ways two documents. `at` absent appends. This seats a box and does not own its position for
 *  life: `runCardFor` reseats a step-built card down to its launch ordinal, and
 *  `pipelineBoxFor`'s adoption arm replaces the element outright. */
function placeContainer(
  st: TurnRender,
  host: HTMLElement,
  el: HTMLElement,
  at: number | undefined,
): void {
  const seat = at === undefined ? null : seatAbove(st, host, at, el);
  if (seat === null) {
    appendEntry(st, host, el);
    return;
  }
  // Not `appendBlock`: a box above mounted content supersedes no trace, and the reference
  // must be captured before the insert moves the first child.
  captureInsertRef(st, host);
  host.insertBefore(el, seat);
}

/** The group a card at run `runStart` joins, built on first use. */
function toolGroupFor(
  st: TurnRender,
  container: HTMLElement,
  key: string,
  runStart: number,
): HTMLDivElement {
  let bucket = st.toolGroups.get(key);
  if (bucket === undefined) {
    bucket = new Map();
    st.toolGroups.set(key, bucket);
  }
  let group = bucket.get(runStart);
  if (group === undefined) {
    group = buildToolGroupShell();
    appendEntry(st, container, group);
    bucket.set(runStart, group);
  }
  return group;
}

// ---------------------------------------------------------------------------
// Grouping and sealing, derived from the store rather than accumulated
// ---------------------------------------------------------------------------

/** Where each of a message's containers BREAKS its run of tool cards, and how far
 *  its content reaches. Built once per pass rather than answered per card: the
 *  per-card question is "what came before me in my own container", and
 *  re-classifying every earlier block is quadratic on one long tool loop. */
interface GroupIndex {
  /** container key → ascending `seq`s at which a new run may START: one past an entry that
   *  closed the container, or one past the entry establishing a nested container's box. Every
   *  position is the STORE's own `seq`, so one entry answers one run start under any range —
   *  which a group's key needs, because the group outlives the pass that built it. */
  readonly starts: ReadonlyMap<string, number[]>;
  /** container key → the greatest `seq` that posts anything into it, in the STORE: a
   *  trace is finished by a successor the store holds, whether or not this range reached
   *  it. */
  readonly lastPost: ReadonlyMap<string, number>;
}

function indexGroups(st: TurnRender): GroupIndex {
  const starts = new Map<string, number[]>();
  const lastPost = new Map<string, number>();
  const built = new Set<string>();
  // Each box's SEAT: the host it is posted into and the store index it is
  // established at. The one fact only this walk knows, and the only input
  // `rendersExpanded` needs.
  const boxes = new Map<string, { host: string; at: number }>();
  const startAt = (key: string, at: number): void => {
    const list = starts.get(key);
    if (list === undefined) {
      starts.set(key, [at]);
    } else {
      list.push(at);
    }
  };
  const post = (key: string, at: number, closes: boolean): void => {
    lastPost.set(key, at);
    if (closes) {
      startAt(key, at + 1);
    }
  };
  // Priced at the STORE's index (for a stage the host is its pipeline's box, whose own
  // creation posts at the top level), and RECORDED there for `placeContainer`: the run
  // break and the box's own seat are one fact, so no floor can move either.
  const openBox = (id: string, host: string, at: number): void => {
    if (built.has(id)) {
      return;
    }
    built.add(id);
    st.containerAt.set(id, at);
    boxes.set(id, { host, at });
    lastPost.set(host, at);
    startAt(host, at + 1);
  };
  // THE RULE, stated over the RENDERING SET rather than over a kind list: every entry this
  // view draws at its own position POSTS into the root lane at its own `seq`, and it CLOSES
  // the run of tool cards unless it IS a tool card. So a steer note, an acknowledgement, a
  // plan card and an event row each break a run exactly as prose does, and no kind added
  // later can silently price a run as continuous across a row the reader sees between two
  // cards. A DELEGATE's own entries are in ITS lane, which this view draws none of, so the
  // only thing a delegate posts is its card at its invocation's `seq`.
  for (const e of st.turn.body) {
    if (!entryRenders(e, st.lane, st.firstPlan)) {
      continue;
    }
    if (e.kind !== "tool_call") {
      post(st.lane, e.seq, true);
      continue;
    }
    const held = st.callByEntry.get(e.id);
    if (held === undefined || isInternalToolTitle(held.call.title)) {
      continue; // mounts nothing, so it posts nothing
    }
    const tc = held.call;
    const subtask = tc.agent_subtask_id ?? "";
    if (subtask !== "") {
      // The delegate's card, priced at its invocation and hosted by its pipeline's box
      // when that pipeline has one.
      const pipelineID = st.stagePipeline.get(subtask);
      let host = "";
      if (pipelineID !== undefined && hostsPipelineBox(st, pipelineID)) {
        host = `pipe:${pipelineID}`;
        openBox(host, "", e.seq);
      }
      openBox(`sub:${subtask}`, host, e.seq);
      continue;
    }
    if (held.runID !== "" && ownsRunCard(st, held.runID, e.id)) {
      // Gated on the OWNER, exactly as `placeEntry` is: a later mention of the same run
      // mounts a tool ROW, so pricing a box for it would post a break at a `seq` that
      // holds no box and split the tool run around nothing.
      openBox(`run:${held.runID}`, "", e.seq);
    } else if (isPipelineInvocation(tc)) {
      // Priced at the DRIVER's entry, where the box stands: `driverNeedsBox` stops asking
      // for one at a count of 1, and the box outlives that.
      if (hostsPipelineBox(st, tc.id) || driverNeedsBox(st, tc)) {
        openBox(`pipe:${tc.id}`, "", e.seq);
      }
    } else {
      post(st.lane, e.seq, isTodoTool(tc));
    }
  }

  for (const list of starts.values()) {
    list.sort((a, b) => a - b);
  }
  const idx: GroupIndex = { starts, lastPost };
  // AFTER the sort, so `lastPost` is final.
  const expanded = new Map<string, boolean>();
  for (const [id, seat] of boxes) {
    expanded.set(id, rendersExpanded(idx, seat.host, seat.at));
  }
  st.boxExpanded = expanded;
  return idx;
}

/** Whether an element established at `seq` `at` in container `host` renders EXPANDED
 *  under the newest-element policy. THE one gate. Its first clause is the whole
 *  no-cascade rule: `""` is the view's ROOT lane, so anything whose host is a box is
 *  refused HERE rather than trusted to a flag at its creation site.
 *
 *  SCOPE: this gate's outermost is the TURN's root lane and no-cascade is WITHIN a lane.
 *  The turn card runs the same rule at turn scope (`fold-state.ts` `isTurnOpen`), so the
 *  newest box inside the newest turn is expanded — two scopes, not a cascade. */
function rendersExpanded(idx: GroupIndex, host: string, at: number): boolean {
  return host === "" && !containerFollowed(idx, host, at);
}

/** The `seq` the run of tool cards holding `seq` `i` started at, which is the key of the
 *  group that card joins. */
function groupRunStart(idx: GroupIndex, key: string, i: number): number {
  let start = 0;
  for (const at of idx.starts.get(key) ?? []) {
    if (at > i) {
      break;
    }
    start = at;
  }
  return start;
}

/** Whether anything is posted into `key` after block `i`: what seals a reasoning
 *  trace. */
function containerFollowed(idx: GroupIndex, key: string, i: number): boolean {
  return (idx.lastPost.get(key) ?? -1) > i;
}

/** Whether the run starting at `runStart` is FOLLOWED: a LATER run starts here,
 *  which happens only where something closed this one. The run's END, not its
 *  start — its own second and later cards post at their own `seq`s, so
 *  `containerFollowed(runStart)` reads every multi-card run as followed. */
function runFollowed(idx: GroupIndex, key: string, runStart: number): boolean {
  const starts = idx.starts.get(key) ?? [];
  return (starts[starts.length - 1] ?? -1) > runStart;
}

/** Apply the newest-element verdict to every collapsible container in this render.
 *
 *  COLLAPSE-ONLY, which is what makes it idempotent: the verdict is monotone, so applying
 *  it every pass and collapsing when superseded are one function, and a box mounted
 *  already-superseded folds at its first sync rather than awaiting a successor.
 *  Three arms for three collapsible kinds, differing in ARITY: a box occupies one `seq`,
 *  so `st.boxExpanded`'s per-seat verdict answers it, while a group spans a RUN whose end
 *  only `starts` knows. An ABSENT seat reads EXPANDED, as both creation sites read it. */
function syncContainerCollapse(st: TurnRender, idx: GroupIndex): void {
  for (const [key, bucket] of st.toolGroups) {
    for (const [runStart, group] of bucket) {
      if (runFollowed(idx, key, runStart)) {
        autoCollapseGroup(group);
      }
    }
  }
  for (const [pipelineID, box] of st.pipelines) {
    box.setSuperseded(st.boxExpanded.get(`pipe:${pipelineID}`) === false);
  }
  for (const [workflowID, card] of st.runs) {
    card.setSuperseded(st.boxExpanded.get(`run:${workflowID}`) === false);
  }
}

// Plan (an entry at its own position, with every later one folding into its card)

/** Mount the turn's FIRST `plan` entry as a card, or bring that card up to the newest
 *  plan state. `entryRenders` admits only the first, so the fold is read off the store
 *  rather than driven by a second dispatch: the card's entries are REPLACED by the
 *  newest `plan` entry of the lane, which is what makes a plan's history one element. */
function mountPlan(st: TurnRender, container: HTMLElement, e: Entry): void {
  const newest = st.newestPlan;
  if (newest.entries.length === 0) {
    return;
  }
  const held = st.entryEls.get(e.seq);
  if (held === undefined) {
    const built = planElement(newest.entries);
    stampEntry(st, built, e.seq);
    appendEntry(st, container, built);
  } else {
    updatePlanElement(held as HTMLDivElement, newest.entries);
  }
  // What the card SHOWS, which is what `syncFolds` compares the store against.
  st.planFrom = newest.seq;
}

// Subagent + todo classification / parsing

/** The prefix KAS puts on a PIPELINE STAGE's tool-call id, whose full shape is
 *  `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/** The orchestrate tool-call id a stage belongs to, or "" when the id is not
 *  stage-shaped. `indexOf` for the separator, not `lastIndexOf`: the driver half is
 *  machine-minted and a stage NAME is author-supplied, so the FIRST occurrence is the
 *  seam and a stage called `run_stage_two` still resolves to its own driver.
 *  `subagent-slice.ts` parses the same id shape against the same literals. */
function stagePipelineID(tc: ToolCall): string {
  const id = tc.id;
  if (!id.startsWith(STAGE_PREFIX)) {
    return "";
  }
  const rest = id.slice(STAGE_PREFIX.length);
  const sep = rest.indexOf(STAGE_SEP);
  if (sep <= 0 || sep + STAGE_SEP.length >= rest.length) {
    return "";
  }
  return rest.slice(0, sep);
}

/** The tool call that STARTS a subagent-orchestration pipeline. */
function isPipelineInvocation(tc: ToolCall): boolean {
  return tc.title === "Orchestrate Sub-agent";
}

/** Learn which pipeline each stage subtask belongs to, and how many stages each DRIVER
 *  declared, from the message's tool calls alone. Read from the tool-call ARRAY rather
 *  than the frames so it has no ordering dependency: a stage whose text arrived before
 *  its invocation is still placed on the next pass. A stage keeps its first pipeline. */
function indexPipelines(st: TurnRender): void {
  for (const held of st.calls) {
    const tc = held.call;
    if (isPipelineInvocation(tc)) {
      st.pipelineDeclared.set(tc.id, declaredStageCount(tc));
      continue;
    }
    const subtask = tc.agent_subtask_id ?? "";
    if (subtask === "") {
      continue;
    }
    const pipelineID = stagePipelineID(tc);
    if (pipelineID === "" || st.stagePipeline.has(subtask)) {
      continue;
    }
    st.stagePipeline.set(subtask, pipelineID);
    const stages = st.pipelineStages.get(pipelineID);
    if (stages === undefined) {
      st.pipelineStages.set(pipelineID, [subtask]);
    } else {
      stages.push(subtask);
    }
  }
}

/** The pipeline box's header label: its KIND plus its stage count. "Subagent pipeline",
 *  not "Pipeline" — the run card is this app's other container for delegated work.
 *  Byte-identical to `subagent-exec-source.ts`'s `ExecRun.label` for the same object. */
function pipelineLabel(st: TurnRender, pipelineID: string): string {
  const n = pipelineStageCount(st, pipelineID);
  return n > 1 ? `Subagent pipeline · ${String(n)} stages` : "Subagent pipeline";
}

function declaredStageCount(tc: ToolCall): number {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const stages = (input as Record<string, unknown>)["stages"];
    if (Array.isArray(stages)) {
      return stages.length;
    }
  }
  return 0;
}

/** How many stages this pipeline has: the GREATER of the driver's declared count and
 *  the stages seen so far. Both are lower bounds — declared is the only source that
 *  knows a stage still on its way, observed the only one that knows a driver dispatched
 *  more than it declared. */
function pipelineStageCount(st: TurnRender, pipelineID: string): number {
  const declared = st.pipelineDeclared.get(pipelineID) ?? 0;
  return Math.max(declared, (st.pipelineStages.get(pipelineID) ?? []).length);
}

/** Whether this pipeline renders a CONTAINER at all. ONE stage is PROMOTED instead: a
 *  container over a single card is two disclosures and two ledgers for one piece of
 *  work. Every other count keeps the container, ZERO included — nothing stands in for a
 *  driver with no stage, and a block that renders nothing is a lost block. */
function pipelineHasContainer(st: TurnRender, pipelineID: string): boolean {
  return pipelineStageCount(st, pipelineID) !== 1;
}

/** Whether the DRIVER's own block has a box to render. Its own function because of the
 *  one exception: a driver that SETTLED having dispatched no stage would otherwise be
 *  invisible, and deferring to the settle stops that fallback displacing a live stage. */
function driverNeedsBox(st: TurnRender, tc: ToolCall): boolean {
  if (pipelineHasContainer(st, tc.id)) {
    return true;
  }
  return !isToolActive(tc.status) && (st.pipelineStages.get(tc.id)?.length ?? 0) === 0;
}

/** kiro-cli's todo tracker surfaces as a `todo_list` tool call. Match the tool
 *  name loosely (todo_list / TodoList / "todo list" / todo-list). */
function isTodoTool(tc: ToolCall): boolean {
  return tc.title.toLowerCase().replace(/[\s_-]/g, "") === "todolist";
}

/** Tolerant parse of a todo_list tool's input into normalized items. Unknown shapes
 *  yield an empty list rather than throwing. */
function parseTodoItems(tc: ToolCall): TodoItem[] {
  const rows = todoRows(tc.input);
  const out: TodoItem[] = [];
  for (const row of rows) {
    if (typeof row === "string") {
      if (row.trim() !== "") {
        out.push({ content: row, status: "pending" });
      }
      continue;
    }
    if (row !== null && typeof row === "object") {
      const o = row as Record<string, unknown>;
      const content = firstString(o, ["content", "task", "title", "text", "name", "description"]);
      if (content !== "") {
        out.push({ content, status: normalizeTodoStatus(o["status"] ?? o["state"]) });
      }
    }
  }
  return out;
}

function todoRows(input: unknown): unknown[] {
  if (Array.isArray(input)) {
    return input;
  }
  if (input !== null && typeof input === "object") {
    const o = input as Record<string, unknown>;
    for (const key of ["todos", "items", "tasks", "list", "todo_list"]) {
      const v = o[key];
      if (Array.isArray(v)) {
        return v;
      }
    }
  }
  return [];
}

function firstString(o: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const v = o[k];
    if (typeof v === "string" && v.trim() !== "") {
      return v;
    }
  }
  return "";
}

function normalizeTodoStatus(v: unknown): PlanStatus {
  const s = typeof v === "string" ? v.toLowerCase().replace(/[\s-]/g, "_") : "";
  if (s === "in_progress" || s === "active" || s === "doing" || s === "started") {
    return "in_progress";
  }
  if (
    s === "completed" ||
    s === "complete" ||
    s === "done" ||
    s === "checked" ||
    s === "finished"
  ) {
    return "completed";
  }
  return "pending";
}
