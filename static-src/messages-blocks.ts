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
import { CHROME_ATTR } from "./chrome-attr.js";
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
import { iconForSubagent, inlineAgentDetail, subagentLabel, subagentName } from "./roles.js";
import { buildRunCard, type RunCardView, type RunDisclosure } from "./fundamentals/run-card.js";
import { invalidateRun, runState, forgetRun } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { buildPath } from "./route-path.js";

// Injected by messages.ts, which owns avatar markup and the streaming-effect registry.

interface BlockCbs {
  /** Register a cleanup disposed on turn finalize / turn unmount. */
  pushStreamingEffect(turnID: string, cleanup: () => void): void;
  /** Register a cleanup disposed when this ENTRY leaves the window. */
  pushEntryEffect(turnID: string, seq: number, cleanup: () => void): void;
  /** Run the cleanups for the entries a window drop removed. */
  disposeEntryEffects(turnID: string, seqs: Iterable<number>): void;
  /** Build an avatar row for a top-level assistant bubble. */
  makeRow(): HTMLDivElement;
  /**
   * The event row an event kind draws; `messages.ts` owns that markup. Non-nullable, since `indexGroups` posts a run
   * break at every rendering entry's `seq` and a null would price a break against nothing.
   */
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

/** Install the callbacks wholesale; the type keeps every caller supplying every member. */
export function initBlockRenderer(c: BlockCbs): void {
  cbs = c;
}

// Which bubble Following pins to, last writer wins. Delegate-hosted bubbles never register: their collapsed box
// reports offsets the reader cannot see.

let liveAnchor: { renderID: string; el: HTMLElement } | null = null;

/**
 * The element Following pins to, or null for the document bottom. Self-healing: a mid-turn rebuild can leave the
 * slot on a detached element, so it re-derives from the render map.
 */
export function getLiveAnchor(): HTMLElement | null {
  if (liveAnchor !== null && renders.get(liveAnchor.renderID)?.topLiveEl !== liveAnchor.el) {
    liveAnchor = null;
    rescanLiveAnchor();
  }
  return liveAnchor?.el ?? null;
}

/** Point the slot at the active chat's newest still-live top-level bubble, or null. */
function rescanLiveAnchor(): void {
  const activeChat = getActiveId();
  for (const [id, st] of renders) {
    if (!st.detached && st.chatID === activeChat && st.topLiveEl !== null) {
      liveAnchor = { renderID: id, el: st.topLiveEl };
    }
  }
}

/** Identity-guarded clear by the registered element's own seal. Only the active chat's renders are rescanned. */
function clearLiveAnchor(el: HTMLElement): void {
  if (liveAnchor?.el !== el) {
    return;
  }
  liveAnchor = null;
  rescanLiveAnchor();
}

// Run id to the `tool_call` entry hosting its card, derived per pass by `runCardOwners`. Derived, not a live
// registry, which would let two mentions in one turn both claim it. An absent run lets every mention build the card.

let runCardOwnerEntries: ReadonlyMap<string, string> = new Map();

export function setRunCardOwners(owners: ReadonlyMap<string, string>): void {
  runCardOwnerEntries = owners;
}

/** Whether this `tool_call` entry hosts `runID`'s card. A detached render is exempt, being its own surface. */
function ownsRunCard(st: TurnRender, runID: string, entryID: string): boolean {
  if (st.detached) {
    return true;
  }
  const owner = runCardOwnerEntries.get(runID);
  return owner === undefined || owner === entryID;
}

// Which collapsible containers are open, so a re-mount restores the reader's choice. Delegates have no key, and
// detached renders register nothing.

const openContainers = new Map<string, boolean>();

function setContainerOpen(key: string, open: boolean): void {
  openContainers.set(key, open);
}

/**
 * What the reader last left `key` at, or undefined if undecided (not closed); the creation site owns the default.
 * Only a user-sourced toggle writes here, so an entry is the latch that keeps the auto path off for life.
 */
function containerOpen(key: string): boolean | undefined {
  return openContainers.get(key);
}

/** Carry a dropped card's disclosure (its `.tool-disclosure` `aria-expanded`) into the registry for the re-mount. */
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

/** One `tool_call` entry joined with its result, hoisted once per pass for its five readers. */
interface TurnToolCall {
  /** The `tool_call` ENTRY's id, which is what `runCardOwners` names an owner by. */
  entryID: string;
  seq: number;
  lane: string;
  /** The call as a card paints it, `store.ts`'s own join of the create and its result. */
  call: ToolCall;
  /** The call's effective workflow id: its own, else its `tool_result`'s. */
  runID: string;
}

/**
 * A mounted prose run: one lane's consecutive `text` entries as one bubble with one markdown stream, so a seal
 * inside it is invisible. `seqs` is the concatenation order and, by prefix sum, the offset table.
 */
interface ProseRun {
  /** The run's first member `seq` and its `st.proseRuns` key, or `-1` while it holds only an open entry. */
  head: number;
  row: HTMLElement;
  bubble: AssistantBubble;
  seqs: number[];
  /** The lane's open entry id when this run holds it as its tail, else null; it has no `seq`. */
  openID: string | null;
}

/** The lane's open entry as mounted: its signal key, text surface and disposer. At most one per render. */
type OpenTail =
  | { kind: "text"; id: string; run: ProseRun; stop: () => void }
  | { kind: "thinking"; id: string; view: ReasoningView; stop: () => void };

// Per-turn render state

interface TurnRender {
  /** The chat this render belongs to, carried: the subagent page renders a chat other than the active one. */
  chatID: string;
  /** The `.turn-body` holding this turn's entries, owned by the card. */
  bodyEl: HTMLElement;
  /** The `seq` range held now; `renderRange` widens it and only `dropEntryRange` narrows it. */
  window: EntryRange;
  /**
   * Entry `seq` to the element whose removal drops it, the only such mapping: a `seq` is unique per turn, not per
   * subtree, since `runCardFor` routes later turns' mentions into the first's card.
   */
  entryEls: Map<number, HTMLElement>;
  /**
   * Entry `seq` to a call bringing its DOM up to a full text, read by `syncMountedText`. An arrow, not a detached
   * method reference.
   */
  entryText: Map<number, (full: string) => void>;
  /** The run's FIRST `seq` → its one mounted bubble. */
  proseRuns: Map<number, ProseRun>;
  /**
   * Member `seq` to its run's first `seq`: every member maps to one row, so drops, text folds and height keys must
   * know whether a `seq` speaks for the row.
   */
  runHead: Map<number, number>;
  /** The mounted open entry of this lane, or null: the only thing that streams. */
  openTail: OpenTail | null;
  /** Subtask id to its delegate card, minted by the delegate's invocation, the only block of it this render draws. */
  subagents: Map<string, SubagentCard>;
  /** Orchestrate tool-call id to the pipeline container it opened. */
  pipelines: Map<string, SubagentContainer>;
  /** Stage subtask id to its orchestrate tool-call id, built by `indexPipelines`. */
  stagePipeline: Map<string, string>;
  /** orchestrate tool-call id → its stage subtask ids, in first-seen order. */
  pipelineStages: Map<string, string[]>;
  /** Orchestrate tool-call id to its declared `stages` length, `0` when absent; a floor (`pipelineStageCount`). */
  pipelineDeclared: Map<string, number>;
  /** Workflow id to the run card this render hosts; the owner rule names one entry per run. */
  runs: Map<string, RunCardView>;
  /**
   * Workflow id to the armed effect's disposer, absent while suspended; outside `disposers` so pause skips the
   * final dispose.
   */
  runEffects: Map<string, () => void>;
  /**
   * Cleanups that outlive the turn, bucketed by entry `seq` (`-1` for the render's own). A run card's run outlives
   * its turn, which `pushStreamingEffect`'s turn-end disposal would cut short.
   */
  disposers: Map<number, (() => void)[]>;
  /**
   * Whether this render lives outside the transcript (the subagent page): its turn-lifetime cleanups go into
   * `disposers`, and a cleanup must not clear a shared per-tool signal.
   */
  detached: boolean;
  /** Every mounted bubble handle (for finalize end()). */
  bubbles: AssistantBubble[];
  /** The bubble carrying the streaming caret, or null; `bubbles` has no notion of its tail. */
  liveBubble: AssistantBubble | null;
  /** This turn's top-level streaming bubble, the live-anchor registry's per-render half. */
  topLiveEl: HTMLElement | null;
  /** Every mounted reasoning handle, for the turn-end seal. */
  reasonings: ReasoningView[];
  /** A container's genuinely live trailing reasoning trace; a trace with a successor is sealed at mount. */
  openReasoning: Map<HTMLElement, ReasoningView>;
  /** Container key to its tool groups, keyed by the store run each opened at, so grouping follows the store. */
  toolGroups: Map<string, Map<number, HTMLDivElement>>;
  /**
   * Container key to the entry `seq` establishing it, where its box belongs; `indexGroups` writes and
   * `placeContainer` reads the same `seq`.
   */
  containerAt: Map<string, number>;
  /**
   * Container key to whether its box renders expanded, overwritten wholesale per pass by `indexGroups`. On `st`
   * because the lazy creators run without `idx`.
   */
  boxExpanded: Map<string, boolean>;
  /**
   * Where an inserted entry goes in its container: a Map during a head extension, null otherwise. Null also tells
   * `appendEntry` not to seal.
   */
  insertBefore: Map<HTMLElement, HTMLElement | null> | null;
  /** This render's key in `renders`; a detached render's is derived. */
  renderID: string;
  /** The turn's id, which keys the streaming signals and the height cache. */
  turnID: string;
  /**
   * The view's root lane (`""` for the transcript, the delegate's uuid for its page); every lane predicate is
   * relative to it.
   */
  lane: string;
  /**
   * This turn plus three facts hoisted per pass: the lane's first `plan` seq, workflow-bearing results, and the
   * joined calls. Held because lazy container creators run from a range that may lack the invocation.
   */
  turn: Turn;
  firstPlan: number;
  results: RunResults;
  calls: readonly TurnToolCall[];
  /** The joins keyed by `tool_call` entry id and by tool id (`callByToolID`), so per-call lookups are not quadratic. */
  callByEntry: ReadonlyMap<string, TurnToolCall>;
  callByTool: ReadonlyMap<string, TurnToolCall>;
  /** Lane to the greatest `seq` it holds, so `entryIsLive` answers in O(1). */
  laneTail: ReadonlyMap<string, number>;
  /** The lane's newest `plan` entry and its `seq`, hoisted so the fold gate is a comparison. */
  newestPlan: { seq: number; entries: PlanEntry[] };
  /** The `seq` of the `plan` the mounted card shows, or `-1`; differing from `newestPlan.seq` means it moved. */
  planFrom: number;
  /**
   * Container key to its binding's disposer, also the already-bound guard. Keyed by box, since a box outlives the
   * invocation entry that seated it.
   */
  boxBindings: Map<string, () => void>;
  /** Whether the last pass threw outside every entry's boundary, so a repeating throw logs once. */
  failed: boolean;
}

const renders = new Map<string, TurnRender>();

/**
 * Chat id to workflow id to the render hosting that run's card: routes later turns' mentions into the first card.
 * Claimed at build, released by the host's disposer; detached renders are never in it.
 */
const runCardHosts = new Map<string, Map<string, TurnRender>>();

// ---------------------------------------------------------------------------
// Public API (called by messages.ts)

/** The whole of `t`'s `seq` space, for a caller that windows nothing. */
function wholeOf(t: Turn): EntryRange {
  return { from: 0, to: turnSpan(t) };
}

/** The render id for a body: the turn id in the transcript, a derived one on a delegate's page. */
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
    failed: false,
  };
  renders.set(st.renderID, st);
  guardPass(st, () => {
    refreshTurn(st, turn);
    // One index per pass, shared by the mount and the collapse sync.
    const idx = indexGroups(st);
    renderRange(st, want.from, want.to, live, idx);
    syncOpenTail(st, live);
    syncContainerCollapse(st, idx);
  });
}

/** Run one render pass so a throw stops at this turn rather than aborting the outer reconcile. */
function guardPass(st: TurnRender, pass: () => void): void {
  try {
    pass();
    st.failed = false;
  } catch (err) {
    if (!st.failed) {
      console.error(
        "[transcript] a turn render pass failed",
        { chat: st.chatID, turn: st.turnID },
        err,
      );
    }
    st.failed = true;
  }
}

/** Adopt this pass's turn and re-hoist its facts in one walk, rather than a quadratic scan per question. */
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
    // First wins, matching the ordered array's `find`.
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

/**
 * Where a mount's turn-lifetime cleanup goes: messages.ts for a transcript render, the render itself when detached,
 * so a cleanup clearing a shared signal cannot reach the transcript's cards.
 */
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

/** Clear a per-tool signal unless this render shares it with the transcript. */
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

/**
 * The `tool`-cause fast path: refresh one mounted turn through a full pass's update path. False when nothing is
 * mounted, since only the full pass mounts.
 */
export function refreshMessageCard(turn: Turn, chatID: string, live: boolean): boolean {
  const st = renders.get(turn.id);
  if (st === undefined) {
    return false;
  }
  updateAssistantBody(st.bodyEl, turn, chatID, live, st.window);
  return true;
}

/** The `seq` range `turnID`'s body holds, or undefined when nothing is mounted; the builder's completion test. */
export function mountedWindow(turnID: string): EntryRange | undefined {
  return renders.get(turnID)?.window;
}

/** The settled tool calls `turnID`'s mounted body knows (the cards a park suspends); empty when nothing is mounted. */
export function mountedToolCalls(turnID: string): ToolCall[] {
  return (renders.get(turnID)?.calls ?? []).map((c) => c.call);
}

/**
 * The element whose removal drops `seq` of `turnID`, or undefined. Resolves the render first, since a card can
 * host another turn's mentions.
 */
export function blockElement(turnID: string, seq: number): HTMLElement | undefined {
  return renders.get(turnID)?.entryEls.get(seq);
}

/**
 * Where `entryID`'s words start inside their prose run and the run's whole length, in runes to match the
 * server. Here because the run's source is this module's; undefined for an entry no run holds.
 */
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
  // The open tail has no `seq`, so its text starts where the sealed members end.
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

/** Runes, not UTF-16 units, matching Go's count; not `Intl.Segmenter`, whose grapheme clusters disagree with it. */
function runeLen(s: string): number {
  return Array.from(s).length;
}

/**
 * Ids of renders still carrying live text: an unsealed bubble or an undrained caret (`.streaming` is the caret
 * test). Detached renders report too.
 */
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
  guardPass(st, () => {
    updatePass(st, turn, want, streaming);
  });
}

function updatePass(st: TurnRender, turn: Turn, want: EntryRange, streaming: boolean): void {
  // Every pass: out-of-order SSE can deliver a stage's entries before its invocation, and only `indexPipelines` maps
  // stages to pipelines.
  refreshTurn(st, turn);
  // The index leads: `rehomeStages` can create a box via `stageHostFor`, and a creation site reading a stale
  // `st.boxExpanded` would force the collapse sync to open things.
  const idx = indexGroups(st);
  // Before the range, so an adopted box precedes what this pass mounts.
  rehomeStages(st, streaming);
  if (want.to > st.window.to) {
    renderRange(st, st.window.to, want.to, streaming, idx);
  }
  // Unconditional: an open entry has no `seq`, so only this call puts it on screen.
  syncOpenTail(st, streaming);
  // Before the collapse sync, so a card folded into place takes its verdict this pass.
  syncFolds(st, streaming, idx);
  syncContainerCollapse(st, idx);
  syncMountedText(st);
}

/**
 * Bring every mounted entry up to the store's text: the fallback for an entry misjudged as not live. Safe over a
 * subscribed entry, since both writers keep their own watermark.
 */
function syncMountedText(st: TurnRender): void {
  for (const [seq, setText] of st.entryText) {
    const e = entryAt(st.turn, seq);
    if (e === undefined) {
      continue;
    }
    // One fold per prose run, from its head, rather than the same concatenation N times.
    const head = st.runHead.get(seq);
    if (head !== undefined && head !== seq) {
      continue;
    }
    const text = payloadOf(e, "text")?.text ?? payloadOf(e, "thinking")?.text;
    if (text !== undefined) {
      setText(text);
    }
  }
  // The open entry has no `seq`, so it is not in `entryText`.
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

/**
 * Bring a mounted entry up to date with a derived value supplied by a later entry: a plan card's newest state, and
 * a launch tool row becoming the run card. `renderRange` never revisits a `seq`, so both would be lost silently.
 */
function syncFolds(st: TurnRender, live: boolean, idx: GroupIndex): void {
  // A later `plan` replaces the card's entries in place; `st.planFrom` is what the card shows.
  if (st.firstPlan >= 0 && st.entryEls.has(st.firstPlan) && st.newestPlan.seq !== st.planFrom) {
    const first = entryAt(st.turn, st.firstPlan);
    if (first !== undefined) {
      mountPlan(st, st.bodyEl, first);
    }
  }
  // The entry first supplying the effective workflow id re-dispatches its `tool_call`, so the row becomes the card.
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
    // Discarded: `placeEntry` re-seats that same card.
    dropEntry(st, held.seq);
    cbs.disposeEntryEffects(st.turnID, [held.seq]);
    // The row was a group member; only the groups, since a held box is not this drop's to judge.
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
    // end(), not finishNow(): the last entry's reveal is text the model really produced, so let it land.
    b.end();
  }
  st.liveBubble = null;
  for (const r of st.reasonings) {
    // settle(), not seal(): a fold is positional, so what stays expanded is the trace still newest in its lane.
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

/**
 * This render's half of a park: release the open tail, finish reveals, suspend run cards without the final
 * dispose. Only this path stops the tail's subscription; its open turn is rebuilt at unpark.
 */
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

/** Re-arm an unparked render's run cards; idempotent per card. */
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

/**
 * Render one delegate's transcript into `host`. The real turn, with the delegate's uuid as the root lane, so its
 * entries render in the flow and a nested invocation renders as a card.
 */
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

/** `finalizeAssistantBody` for a detached render: flush streams and settle traces, folding nothing. */
export function finalizeDetachedBody(turnID: string, subtask: string): void {
  finalizeAssistantBody(renderIDFor(turnID, subtask));
}

/** Drop a detached render: the page's own unmount, the only thing firing its disposers. */
export function disposeDetachedBody(turnID: string, subtask: string): void {
  disposeAssistantBody(renderIDFor(turnID, subtask));
}

/** Run and clear every cleanup this render owns; idempotent, since both dispose paths can reach it. */
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

/**
 * Group `[from, to)` so a run's members reach one mounter, using `proseRunAt`. A run's `run` is its first `seq`,
 * which can sit below `from` on a live turn.
 */
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
  // Only a tail append moves the caret, and not when nothing in the range renders here, which would stop the
  // streaming reply. Idempotent.
  const tailAppend = to > st.window.to && placements.length > 0;
  // A range leading with an extension of the mounted run keeps the caret: sealing would drop `.streaming` at every
  // entry seal.
  const first = placements[0];
  const extendsRun = first !== undefined && first.run >= 0 && st.proseRuns.has(first.run);
  if (tailAppend && !extendsRun) {
    sealLiveBubble(st);
  }
  st.window = { from: Math.min(st.window.from, from), to: Math.max(st.window.to, to) };
  for (const [i, p] of placements.entries()) {
    // Per placement: the window already claims the range, so one uncontained throw loses every later entry.
    try {
      place(st, p, live, idx);
    } catch (err) {
      failPlacement(st, p, err);
    }
    // Anything after this run in the range renders below it, so the caret and anchor leave it.
    if (p.run >= 0 && tailAppend && i + 1 < placements.length) {
      sealLiveBubble(st);
    }
  }
}

function place(st: TurnRender, p: Placement, live: boolean, idx: GroupIndex): void {
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
    return;
  }
  const e = entryAt(st.turn, p.seqs[0]);
  if (e !== undefined) {
    placeEntry(st, e, live && entryIsLive(st, e), idx);
  }
}

/**
 * Undo a failed placement and seat one fallback row, so every claimed `seq` still resolves; a run's earlier
 * members fail with it, sharing the bubble.
 */
function failPlacement(st: TurnRender, p: Placement, err: unknown): void {
  const view = p.run >= 0 ? st.proseRuns.get(p.run) : undefined;
  const seqs = [...new Set([...(view?.seqs ?? []), ...p.seqs])].sort((a, b) => a - b);
  if (view !== undefined) {
    if (st.openTail?.kind === "text" && st.openTail.run === view) {
      releaseOpenTail(st);
    }
    view.bubble.finishNow();
    st.bubbles = st.bubbles.filter((b) => b !== view.bubble);
    if (st.liveBubble === view.bubble) {
      st.liveBubble = null;
    }
    view.row.remove();
  }
  for (const seq of seqs) {
    dropEntry(st, seq);
  }
  cbs.disposeEntryEffects(st.turnID, seqs);
  if (p.run >= 0) {
    st.proseRuns.delete(p.run);
  }
  pruneEmptyToolGroups(st);
  const entries = seqs.map((seq) => entryAt(st.turn, seq));
  console.error(
    "[transcript] an entry failed to render",
    {
      chat: st.chatID,
      turn: st.turnID,
      entries: entries.map((e) => e?.id ?? ""),
      kind: entries[0]?.kind ?? "",
    },
    err,
  );
  const first = seqs[0];
  if (first === undefined) {
    return;
  }
  const row = buildEntryFallback(st, seqs);
  stampEntry(st, row, first);
  if (seqs.length > 1) {
    row.dataset["entries"] = entries.map((e) => e?.id ?? "").join(" ");
    for (const seq of seqs) {
      st.entryEls.set(seq, row);
      st.runHead.set(seq, first);
    }
  }
  appendEntry(st, st.bodyEl, row);
}

/** The row a failed entry renders as, text only: its words, a tool's title, else its kind. */
function buildEntryFallback(st: TurnRender, seqs: readonly number[]): HTMLElement {
  const first = seqs[0] === undefined ? undefined : entryAt(st.turn, seqs[0]);
  let text = proseText(st, seqs);
  if (text === "" && first !== undefined) {
    text = payloadOf(first, "tool_call")?.title ?? first.kind;
  }
  const row = document.createElement("div");
  row.className = "entry-fallback";
  const label = document.createElement("span");
  label.className = "entry-fallback-label";
  label.setAttribute(CHROME_ATTR, "");
  label.textContent = "This entry could not be displayed";
  row.append(label);
  if (text !== "") {
    const body = document.createElement("div");
    body.className = "entry-fallback-text";
    body.textContent = text;
    row.append(body);
  }
  return row;
}

/**
 * Mount `keep`'s ordinals below the mounted head in place: no replayed animations, lost selections or
 * disclosures. Bounded by `keep.to`, or a disjoint move mounts everything between.
 */
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

/** Retract the window's head to `keep.from`, a head-side change above the reader. */
export function dropHead(turn: Turn, keep: EntryRange): void {
  const st = renders.get(turn.id);
  if (st === undefined || keep.from <= st.window.from) {
    return;
  }
  // A move past the mounted tail keeps nothing, and an empty window at `keep.from` is where the tail mounts from.
  dropEntryRange(st, { from: keep.from, to: Math.max(keep.from, st.window.to) });
}

export function dropTail(turn: Turn, keep: EntryRange): void {
  const st = renders.get(turn.id);
  if (st === undefined || keep.to >= st.window.to) {
    return;
  }
  dropEntryRange(st, { from: st.window.from, to: keep.to });
}

/** Release everything the mounted `seq`s outside `keep` own. `openContainers` keys survive the window move. */
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
  // Every height before any row goes: a read between removals lays out a page short by the rows already gone, and
  // the browser clamps `scrollTop` to it before the spacer can take their place.
  for (const seq of removed) {
    recordOutgoingHeight(st, seq);
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
  // After the loop: `st` may claim, and mid-loop its `entryEls` holds ordinals being dropped.
  for (const [runID, card] of orphaned) {
    resolveRunCardFate(st, runID, card);
  }
}

/**
 * Re-home or release `runID`'s card after the drop: one card per run, hosted by the earliest render holding
 * mounted blocks inside it.
 */
function resolveRunCardFate(st: TurnRender, runID: string, card: RunCardView): void {
  const claim = liveRunClaimant(st, card);
  if (claim === undefined) {
    // Nothing mounted inside, so release the run's state, or `runCardFor` hands out a detached node.
    st.runs.delete(runID);
    releaseRunCard(st, runID, card);
    card.root.remove();
    return;
  }
  // Re-homed: the card is a container, and removing it would take mounted entries out of the document.
  const seat = seatAbove(claim.host, claim.host.bodyEl, claim.at, card.root);
  if (claim.host !== st) {
    adoptRunCard(claim.host, st, runID, card, seat);
    return;
  }
  // Only the seat can be wrong here; guarded because any re-seat blurs focus inside the card.
  if (card.root.nextElementSibling !== seat) {
    st.bodyEl.insertBefore(card.root, seat);
  }
}

/**
 * Whether `el` sits in a subtree the page is not rendering (folded body, parked view, collapsed pipeline). A
 * `closest()` test, since a geometry read would force that render.
 */
export function geometrySkipped(el: Element): boolean {
  return (
    el.closest(".turn[data-folded] > .turn-body") !== null ||
    el.closest(".transcript-view:not(.is-active)") !== null ||
    el.closest(".subagent-block.collapsed > .subagent-body") !== null
  );
}

/**
 * Record the height `seq`'s row holds, so the spacer replacing it holds it too. A run's row is one box, keyed by
 * the run's range; a member row, a container root and an unrendered subtree record nothing.
 */
function recordOutgoingHeight(st: TurnRender, seq: number): void {
  const el = st.entryEls.get(seq);
  const head = st.runHead.get(seq);
  if (el === undefined || (head !== undefined && head !== seq)) {
    return;
  }
  if (isContainerRoot(st, el, hostedRun(st, entryAt(st.turn, seq))?.card) || geometrySkipped(el)) {
    return;
  }
  const px = el.offsetHeight;
  if (px <= 0) {
    return;
  }
  const view = head === seq ? st.proseRuns.get(seq) : undefined;
  if (view === undefined) {
    recordEntryHeight(st.turnID, seq, px);
  } else {
    recordRowHeight(st.turnID, { from: seq, to: (view.seqs[view.seqs.length - 1] ?? seq) + 1 }, px);
  }
}

/**
 * Release one entry's disclosure, element, sink, signal and cleanups; returns any hosted run card for the caller to
 * decide on. `recordOutgoingHeight` keeps a height the spacer needs.
 */
function dropEntry(st: TurnRender, seq: number): { runID: string; card: RunCardView } | undefined {
  const entry = entryAt(st.turn, seq);
  const hosted = hostedRun(st, entry);
  const el = st.entryEls.get(seq);
  st.entryEls.delete(seq);
  // A run's members share one row, so only the head releases it; the window snap keeps whole runs on one side.
  const head = st.runHead.get(seq);
  st.runHead.delete(seq);
  if (head !== undefined && head === seq) {
    const view = st.proseRuns.get(seq);
    st.proseRuns.delete(seq);
    // The row carrying the open tail is going, so stop its subscription.
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

/** Drop the delegate-card state for `el`; a stale entry would refuse the next card. */
function forgetSubagentCard(st: TurnRender, el: HTMLElement): void {
  const subtask = el.dataset["subtask"] ?? "";
  if (subtask !== "" && st.subagents.get(subtask)?.root === el) {
    st.subagents.delete(subtask);
  }
}

/** Whether `el` is a container whose lifetime this render owns beyond its stamping entry; delegate cards count. */
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

/**
 * Remove every delegate card the surviving range has no invocation for. Takes `keep`, so it runs before
 * `pruneEmptyContainers`.
 */
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

/**
 * Release `key`'s binding if still its own. Every box release calls this: a box outlives its invocation entry, so
 * that entry's disposer cannot, and a stale binding freezes the next card.
 */
function releaseBox(st: TurnRender, key: string): void {
  st.boxBindings.get(key)?.();
}

/** The run card this render hosts for `entry`'s workflow; `dropEntry` needs it twice. */
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

/**
 * The render holding mounted entries inside `card` and the lowest such `seq`. DOM order decides, since `renders`
 * is keyed in build order.
 */
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

/**
 * Where something placed at `at` belongs in `host`: before the first child holding a later ordinal, else the
 * tail. `placed` is excluded.
 */
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

/**
 * Re-subscribe every surviving container whose invocation entry the drop took, delegate cards included, or its
 * header freezes. Status follows the invocation call, which the store holds all turn.
 */
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

/**
 * Remove every tool group shell left without a card. Separate because `syncFolds` must not prune a legitimately
 * empty pipeline box.
 */
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

/** Remove every container the drop left empty with its state; its `openContainers` key stays. */
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

/**
 * Whether `e` is its lane's tail `seq`: a lane coalesces one entry at a time, so its tail is the streaming
 * position, and one render draws one lane, so one caret.
 */
function entryIsLive(st: TurnRender, e: Entry): boolean {
  return (st.laneTail.get(e.lane ?? "") ?? -1) <= e.seq;
}

/** End the bubble currently carrying the caret, if any. */
function sealLiveBubble(st: TurnRender): void {
  // finishNow, not end: the lane's tail moved, and one streaming caret is the invariant. The last entry drains.
  st.liveBubble?.finishNow();
  st.liveBubble = null;
}

/**
 * Get or build a delegate's card, created only from its invocation entry, since the delegate's own entries are in
 * its lane. A stage's host pipeline is `st.stagePipeline`'s.
 */
function subagentCardFor(st: TurnRender, subtask: string, live: boolean): SubagentCard {
  const existing = st.subagents.get(subtask);
  if (existing !== undefined) {
    return existing;
  }
  // Turn-level, so found whether or not this range holds its `seq`; read before the build because the card's tail
  // latches on its starting status.
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
  // Bind too: a card seated here has no subscription until its invocation mounts, which a window may never reach.
  // `bindSubagent` is a no-op for an already-bound card.
  if (inv !== undefined) {
    bindSubagent(st, subtask, sa, inv, invocationIndex(st, inv.id));
  }
  // In its host, at the `seq` that establishes it, where `indexGroups` prices its run break.
  const host = stageHostFor(st, subtask, live);
  placeContainer(st, host, sa.root, st.containerAt.get(`sub:${subtask}`));
  return sa;
}

/** The `seq` of `toolID`'s entry, or `-1` (the render-lifetime bucket) when the turn holds none. */
function invocationIndex(st: TurnRender, toolID: string): number {
  return callByToolID(st, toolID)?.seq ?? -1;
}

/**
 * What the delegate card's head opens, or nothing for a detached render, which is the page. Injected so
 * `fundamentals/` points downward, and lazy because `subagent-view.ts` reaches the whole page.
 */
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

/** Whether this pipeline's box is, or will be by the pass's end, in this render; an existing box outranks the count. */
function hostsPipelineBox(st: TurnRender, pipelineID: string): boolean {
  return st.pipelines.has(pipelineID) || pipelineHasContainer(st, pipelineID);
}

/**
 * Where a stage's box goes: its pipeline's body when that pipeline has a container, else the top level, so both
 * arrival orders match. The existing-container check is first, since the count can rise later.
 */
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

/**
 * Re-derive every mounted delegate card's host and move those out of place: a lone stage was promoted on a stage
 * count that is only a lower bound. Idempotent, since an in-place re-append would replay the animation.
 */
function rehomeStages(st: TurnRender, live: boolean): void {
  for (const [subtask, sa] of st.subagents) {
    const host = stageHostFor(st, subtask, live);
    if (sa.root.parentElement !== host) {
      host.appendChild(sa.root);
    }
  }
}

/** Write the driver's header onto its box: label from the stage count, status and ledger from the driver's call. */
function paintPipeline(st: TurnRender, box: SubagentContainer, driver: ToolCall): void {
  box.setName(pipelineLabel(st, driver.id));
  box.setStatus(driver.status);
  box.setSummary(pipelineSummary(st, driver));
}

/** Get or build the pipeline box for an orchestrate call, adopting its top-level stages by re-parent, never rebuild. */
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
          // The reader's state first, then this pass's verdict; `indexGroups` runs ahead, so the fallback is a floor.
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
    // Where the first adopted stage sat, keeping order; a swap posts nothing after an open trace.
    first.root.replaceWith(box.root);
  }
  for (const v of promoted) {
    box.body.appendChild(v.root);
  }
  // A stage-built box paints itself: a settled driver's effect never re-runs.
  const driver = peekToolCallSig(st.chatID, pipelineID);
  if (driver !== undefined) {
    paintPipeline(st, box, driver);
  }
  // Bind too, as the subagent box does: its invocation entry may never mount.
  const inv = callByToolID(st, pipelineID);
  if (inv !== undefined && isPipelineInvocation(inv.call)) {
    bindPipeline(st, inv.call, live, inv.seq);
  }
  return box;
}

/**
 * The disclosure a transcript run card reads and writes: the reader's state, this pass's verdict, and where a flip
 * goes.
 */
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

/**
 * Get or build the run card for one workflow id and subscribe it: `run-store.ts` holds a signal per run, so one
 * effect per card is all its event handling.
 */
function runCardFor(st: TurnRender, workflowID: string, name: string, owner = false): RunCardView {
  const existing = st.runs.get(workflowID);
  if (existing !== undefined) {
    reseatInserted(st, st.bodyEl, existing.root);
    return existing;
  }
  // The launch call is the card's only witness for its label and a failed launch.
  const launch = st.calls.find((c) => c.runID === workflowID)?.call;
  if (!st.detached) {
    // One box per run per transcript: ownership is derived over the resident window, so an older page can make an
    // earlier mention the owner, and the card re-homes there. Step rows are keyed by node path.
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
  // The footer link re-opens the run's tab, injected so `fundamentals/` points downward and lazy since `run-view.ts`
  // reaches the whole page. The parent is this render's chat; the run store learns it only from SSE.
  const chatID = st.chatID;
  const card = buildRunCard(
    workflowID,
    launch === undefined ? name : recipeNameOf(launch),
    (id, label, focusNode) => {
      void import("./run-view.js")
        .then(({ openRunView }) => {
          // A third argument makes a step row open the run at that step.
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
  // The card's first read; later ones arrive through the effect.
  invalidateRun(workflowID);
  armRunCard(st, workflowID, card);
  if (launch !== undefined) {
    card.setLaunch(launch.status, launch.output);
  }
  return card;
}

/**
 * Give up this render's claim on the card: effect, clock hold, host slot, store cell. Idempotent, so the render
 * and entry lifetimes share it.
 */
function releaseRunCard(st: TurnRender, workflowID: string, card: RunCardView): void {
  disarmRunCard(st, workflowID, card);
  // Only while this render holds the claim: a re-homed card's effect still reads that cell.
  const hosts = runCardHosts.get(st.chatID);
  if (hosts?.get(workflowID) !== st) {
    return;
  }
  hosts.delete(workflowID);
  if (hosts.size === 0) {
    runCardHosts.delete(st.chatID);
  }
  // Unconditional: registered demand decides which surfaces still need the cell.
  forgetRun(workflowID);
}

/**
 * Move `workflowID`'s card from `host` into `st`, claim included; `seat` is the node to place it before. Moving the
 * node keeps effects and skips the entry animation.
 */
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

/**
 * Adopt the launch call into its run's card and return the root: a placeholder label from the input, and a failed
 * launch reported, since it created no run and the card would sit at "starting".
 */
function bindRunCard(st: TurnRender, workflowID: string, tc: ToolCall): HTMLElement {
  const card = runCardFor(st, workflowID, recipeNameOf(tc), true);
  card.setLaunch(tc.status, tc.output);
  return card.root;
}

/** The recipe a launch names, from its input; a placeholder, since renders prefer `runLabel`. */
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

// One interval for every card on screen, refcounted per workflow id: one run can show on several surfaces, so a
// release names its holder.

/** What the clock needs of a holder; narrower than `RunCardView` because `run-bar.ts` also holds it. */
export interface RunClockHolder {
  tick(): void;
}

const clockHolders = new Map<string, Set<RunClockHolder>>();
let clockTimer: ReturnType<typeof setInterval> | undefined;

/** Tick this run's clock once a second while `holder` shows it. Refcounted; idempotent with `releaseRunClock`. */
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

/** Subscribe a run card to its store cell and hold the clock; `disarmRunCard` suspends. Both idempotent. */
function armRunCard(st: TurnRender, workflowID: string, card: RunCardView): void {
  if (st.runEffects.has(workflowID)) {
    return;
  }
  const stop = effect(() => {
    // Two inputs on different clocks: `inspect` for step state, the dock for blocked asks. KAS leaves the run
    // `running` while a step waits on a person.
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

/**
 * Whether this is a delegate's invocation: a root-lane `tool_call` naming a subtask, drawn as its card. Relative to
 * the root, so on a delegate's page a nested invocation is the grandchild's card.
 */
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
  // On the union, so an arm handing the entry on hands a payload narrowed to its kind.
  const u = entryUnion(e);
  switch (u.kind) {
    case "turn_open":
    case "turn_close":
    case "turn_bind":
    case "tool_result":
    case "reconciled":
      // Rendered elsewhere or nowhere (`reconciled` says a merge added nothing); `entryRenders` refuses these, so this
      // arm is totality.
      return;
    case "text":
    case "steer_ack": {
      // A `steer_ack` is the model's words at its own position, in its own bubble, since rendering ends the run. A `text`
      // entry here is a one-member run.
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
      // Matches only transcripts persisted before 2026-08-31, when the engine stopped emitting these and their cards
      // stuck at in_progress. Title-keyed: the persisted call has no tool id.
      if (isInternalToolTitle(tc.title)) {
        return;
      }
      const subtask = tc.agent_subtask_id ?? "";
      // A workflow launch becomes the run's card, ahead of the subtask checks. Only the owner: a later mention renders as
      // a tool card, or it would clobber the launch's state.
      if (subtask === "" && held.runID !== "" && ownsRunCard(st, held.runID, e.id)) {
        // Stamped, so a search hit on the launch resolves to its card.
        stampEntry(st, bindRunCard(st, held.runID, tc), seq);
        return;
      }
      // A pipeline launch becomes its box, ahead of the subtask checks for the same reason.
      if (subtask === "" && isPipelineInvocation(tc)) {
        bindPipeline(st, tc, live, seq);
        return;
      }
      // The invocation becomes the delegate's card, stamped ahead of the bind so a hit resolves to it.
      if (isDelegateInvocation(st, e)) {
        const sa = subagentCardFor(st, subtask, live);
        stampEntry(st, sa.root, seq);
        bindSubagent(st, subtask, sa, tc, seq);
        return;
      }
      // Resolved here, where the index is, so the region is created in its final state.
      const runStart = groupRunStart(idx, st.lane, seq);
      mountToolCard(st, container, st.lane, tc, seq, runStart, runFollowed(idx, st.lane, runStart));
      return;
    }
    default: {
      // Totality by the compiler: a new AnyEntry kind without an arm fails at `_never`. Returned, since
      // `noUnusedLocals` rejects a bare binding.
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

/**
 * Record `el` as `seq`'s element and stamp both coordinates for the anchor ladder; the turn id too, since a card
 * here can carry another turn's numbering.
 */
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

/** The concatenation of `seqs`' texts. No marker node: the token set has no raw-HTML construct. */
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

/**
 * Mount `seqs` into the run headed by `head`, building the bubble if absent. Extends an existing run: a live update
 * range can start mid-run, and a second bubble there would be a visible seam.
 */
function mountProse(
  st: TurnRender,
  container: HTMLElement,
  head: number,
  seqs: readonly number[],
  live: boolean,
): void {
  // Built with the run's text: the reveal buffer sees only growth, so mount-time text paints at once.
  const view =
    st.proseRuns.get(head) ??
    adoptOpenRun(st, head, seqs) ??
    buildProseRun(st, container, head, proseText(st, seqs), live);
  for (const seq of seqs) {
    joinProseRun(st, view, seq);
  }
}

/**
 * The open-only run whose entry sealed into one of `seqs`, re-keyed under its new position, so the seal does not
 * mount a second bubble.
 */
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
  // Only a live transcript bubble joins the anchor registry; only a run end calls the bubble's `seal()`.
  const topLive = live && !st.detached;
  // Created before the bubble so the initial blank report lands on it.
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
  // An open-only run has no position yet; `adoptOpenRun` seats it at the seal.
  if (head >= 0) {
    st.proseRuns.set(head, view);
    // The row drops the run and is stamped for its first entry; `data-entries` carries the rest.
    stampEntry(st, row, head);
  }
  appendEntry(st, container, row);
  return view;
}

/**
 * Seat one sealed member on `view`: shared element, sink and `data-entries`. No subscription; only the open tail
 * streams.
 */
function joinProseRun(st: TurnRender, view: ProseRun, seq: number): void {
  const e = entryAt(st.turn, seq);
  if (e === undefined || view.seqs.includes(seq)) {
    return;
  }
  // The open tail has sealed: it retires and its entry joins as a member.
  if (st.openTail?.id === e.id) {
    releaseOpenTail(st);
  }
  view.seqs.push(seq);
  st.runHead.set(seq, view.head);
  st.entryEls.set(seq, view.row);
  writeRunEntries(st, view);
  // Every member's sink writes the whole run, so a fold cannot leave one entry's text; `syncMountedText` calls the
  // head's.
  st.entryText.set(seq, () => {
    view.bubble.setText(runText(st, view));
  });
  view.bubble.setText(runText(st, view));
}

/** The run's member ids in stream order, open tail last, standing in for marker nodes. */
function writeRunEntries(st: TurnRender, view: ProseRun): void {
  const ids = view.seqs.map((seq) => entryAt(st.turn, seq)?.id ?? "");
  if (view.openID !== null) {
    ids.push(view.openID);
  }
  view.row.dataset["entries"] = ids.join(" ");
}

// The OPEN entry: what streams

/**
 * Mount, keep or retire the lane's open entry, at the tail of its view: the one entry that grows and subscribes.
 * Runs after the range, so a seal this pass has already taken over the surface.
 */
function syncOpenTail(st: TurnRender, live: boolean): void {
  const open = openEntryOf(st);
  const tail = st.openTail;
  if (tail !== null && tail.id !== open?.id) {
    // An id that changed with the tail standing was not adopted, so its surface would draw twice.
    dropOpenSurface(st, tail);
    releaseOpenTail(st);
  }
  // Liveness is the store's: only an open entry with no `turn_close` can stream. A stranded one (a bridge death)
  // mounts nothing.
  if (open === undefined || st.openTail !== null || closeOfBody(st.turn.body) !== undefined) {
    return;
  }
  if (open.kind === "thinking") {
    mountOpenThinking(st, open);
    return;
  }
  mountOpenProse(st, open, live);
}

/** The open text entry as the tail member of its lane's newest run, or a run of its own after non-prose. */
function mountOpenProse(st: TurnRender, open: OpenEntry, live: boolean): void {
  const tailSeq = lastRenderedSeq(st);
  if (tailSeq >= 0 && (tailSeq < st.window.from || tailSeq >= st.window.to)) {
    // The lane's tail is paged out; it mounts when the window reaches its seal.
    return;
  }
  const run = tailSeq < 0 ? null : proseRunAt(st.turn, tailSeq, st.lane, st.firstPlan);
  const view =
    (run === null ? undefined : st.proseRuns.get(run.from)) ??
    buildProseRun(st, st.bodyEl, -1, open.text, live);
  view.openID = open.id;
  writeRunEntries(st, view);
  // Captured once: the appender seals a lane's open entry before appending to that lane, so the prefix cannot go
  // stale.
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

/** The open thinking entry as its own live trace, expanded: nothing follows an unsealed entry. */
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

/**
 * Subscribe to `open`'s signal, reporting `(full, delta)`, with a null delta for a write that does not bridge what
 * the surface holds. `prefix` is the text ahead of this entry on the surface.
 */
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

/** Remove the surface an open entry mounted for itself; no window drop reaches it, lacking a `seq`. */
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

/** Retire the mounted open tail: the subscription goes and its run drops back to sealed members. The element stays. */
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

/** The greatest `seq` this view renders at, or -1: where the open entry sits after. */
function lastRenderedSeq(st: TurnRender): number {
  for (let seq = turnSpan(st.turn) - 1; seq > 0; seq--) {
    const e = entryAt(st.turn, seq);
    if (e !== undefined && entryRenders(e, st.lane, st.firstPlan)) {
      return seq;
    }
  }
  return -1;
}

/**
 * The reader's own words at the steer's `seq`; the acknowledgement is its own entry. `lane` marks a steer a delegate
 * read. The ack fold reads every lane, since an ack keeps the lane of the text it came from.
 */
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
  // The open trace has sealed in place; re-mounting would draw it twice.
  const tail = st.openTail;
  const adopted = tail?.kind === "thinking" && tail.id === e.id ? tail.view : undefined;
  if (adopted === undefined && initial === "") {
    return; // an empty settled "Thinking completed" dropdown is worse than none
  }
  const host = st.lane;
  // Sealed from the store: a trace with a successor there is finished however the range reached it.
  const followed = containerFollowed(idx, host, seq);
  // The disclosure is positional, the pulse is not: a settled trace still newest in its lane is expanded, a live one
  // already followed is collapsed.
  const view = adopted ?? buildReasoning(initial, live, rendersExpanded(idx, host, seq));
  if (adopted === undefined) {
    st.reasonings.push(view);
  } else {
    releaseOpenTail(st);
  }
  st.entryText.set(seq, (full) => {
    view.setText(full);
  });
  // Append before registering the view, or appendEntry would seal the trace being mounted.
  stampEntry(st, view.root, seq);
  if (adopted === undefined) {
    appendEntry(st, container, view.root);
  }
  // Head insertion never reaches `openReasoning`, which holds only trailing traces.
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
  // Before the refresh, the group's disclosure-creation site, so it is not animated shut.
  setGroupSuperseded(group, superseded);
  // Built open when the reader had it open, rather than opened after mount and animated.
  const card = mountToolCallCard(st.chatID, tc, containerOpen(`tool:${tc.id}`) === true);
  card.setAttribute(RECONCILE_KEY, tc.id);
  stampEntry(st, card, seq);
  // Cards live in the group's collapsible body, not beside the header.
  placeInContainer(st, groupBody(group), card);
  refreshGroupHeader(group);
  // This render's slot, in `st.disposers` rather than `pushLifetimeEffect`: a transcript card outlives turn end, and
  // park suspends it through the registry.
  pushDisposer(st, seq, () => {
    disposeToolSlot(st.chatID, tc.id, card);
  });
}

/**
 * Wire the pipeline invocation's shape and header onto its box and its ledger onto the stages' members. A promoted
 * pipeline paints nothing (`driverNeedsBox`); separate from `bindSubagent` since it sums across stages.
 */
function bindPipeline(st: TurnRender, tc: ToolCall, live: boolean, seq: number): void {
  const key = `pipe:${tc.id}`;
  if (st.boxBindings.has(key)) {
    return;
  }
  // Reserved before the first paint, which binds the box it builds and would otherwise re-enter and leak.
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
    // Stamped here: the box an upgrade builds replaces the node, so the stamp follows the current box.
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

/** The footer outcome a settled invocation earns; `aborted` stays its own, since a stopped delegate produced nothing. */
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

/** The pipeline's ledger, summed over stages; files merge by path so one file is not double-counted. */
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
    // Stages are this pipeline's delegates; grandchildren are not folded in, so the count means one thing.
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

/**
 * Write the invocation's identity, status and ledger onto a card. `live` is the chat's turn liveness: an
 * `in_progress` call with no live turn is a stale spinner and paints stopped (`delegateStatusFor`).
 */
function paintSubagent(
  st: TurnRender,
  subtask: string,
  sa: SubagentCard,
  tc: ToolCall,
  live: boolean,
): void {
  sa.setName(subagentLabel(tc));
  sa.setDetail(inlineAgentDetail(tc));
  sa.setIcon(iconForSubagent(subagentName(tc)));
  sa.setStatus(delegateStatusFor(tc.status, live));
  sa.setSummary(subagentSummary(st, subtask, tc));
}

/**
 * Wire the invocation's status, name and icon onto its card, its ledger onto the members, and while it works its
 * rolling tail.
 */
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
  // Untracked at the seat, tracked in the effect: subscribing the render pass would repaint every turn per
  // `busy_chats` frame.
  const seated = get(st.chatID);
  const seatedLive = seated === undefined || turnLive(seated);
  let effective = delegateStatusFor(tc.status, seatedLive);
  paintSubagent(st, subtask, sa, tc, seatedLive);
  // The tail lives only while the delegate works, keyed on the effective status.
  stopTail =
    isToolActive(effective) && !st.detached
      ? bindSubagentTail(st.chatID, subtask, (lines) => {
          sa.setTail(lines);
        })
      : null;
  const sig = ensureToolCallSig(st.chatID, tc.id, tc);
  let last = tc;
  dispose = effect(() => {
    // Read first: this settles the card in the same pass as the `busy_chats` reconcile that clears the chat's latch.
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
    const detail = inlineAgentDetail(next);
    if (detail !== inlineAgentDetail(last)) {
      sa.setDetail(detail);
    }
    // The members settle before the invocation, so the settle tick sees their final diffs.
    sa.setSummary(subagentSummary(st, subtask, next));
    last = next;
  });
  pushLifetimeEffect(st, seq, release);
}

/**
 * What a delegate's footer can state honestly (no credits or model per delegate). Members come from the turn's
 * tool calls, not per-tool signals, which would report zeros.
 */
function subagentSummary(st: TurnRender, subtask: string, invocation: ToolCall): TurnSummaryData {
  let toolMs = 0;
  let delegateCount = 0;
  let delegateMs = 0;
  const kindCounts: Partial<Record<ToolKind, number>> = {};
  const changed: Record<string, FileChange> = {};
  for (const held of st.calls) {
    const tc = held.call;
    // Both arms: a delegate's calls sit in its lane, while the invocation carries the stamp in the issuer's lane.
    if (
      (held.lane !== subtask && (tc.agent_subtask_id ?? "") !== subtask) ||
      tc.id === invocation.id
    ) {
      continue;
    }
    kindCounts[tc.kind] = (kindCounts[tc.kind] ?? 0) + 1;
    const ms = tc.duration_ms ?? 0;
    toolMs += ms;
    // The turn ledger's predicate, so nesting is one rule; the loop skips `invocation` itself.
    if (isSubagentInvocation(tc)) {
      delegateCount++;
      delegateMs += ms;
    }
    for (const d of tc.diffs ?? []) {
      // lineDelta strips the trailing newline, matching the server (internal/buffer/linediff.go).
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
      // Derived from the dispatch stamp plus the measured duration, which is all a delegate has; withheld while running or
      // unmeasured.
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

/**
 * Append into a container, sealing its open trace first: the wire has no thinking-ended signal, so the next
 * element's arrival is the end. The one door, so no later mounter can skip it.
 */
function appendEntry(st: TurnRender, container: HTMLElement, el: HTMLElement): void {
  if (st.insertBefore === null) {
    // Only a tail append supersedes; an inserted entry is posted after nothing.
    sealReasoning(st, container);
  }
  placeInContainer(st, container, el);
}

/**
 * Place `el` before the insertion reference during a head extension, else at the end. The reference is captured
 * on the extension's first touch and does not move, keeping inserted ordinals ascending.
 */
function placeInContainer(st: TurnRender, container: HTMLElement, el: HTMLElement): void {
  const refs = captureInsertRef(st, container);
  if (refs === null) {
    container.appendChild(el);
    return;
  }
  container.insertBefore(el, refs.get(container) ?? null);
}

/**
 * Record `container`'s insertion boundary on the extension's first touch, null included; `has`, since a created
 * container is empty then. A turn body's head spacer leads it and holds no entry, so the boundary is past it.
 */
function captureInsertRef(
  st: TurnRender,
  container: HTMLElement,
): Map<HTMLElement, HTMLElement | null> | null {
  const refs = st.insertBefore;
  if (refs !== null && !refs.has(container)) {
    const first = container.firstElementChild as HTMLElement | null;
    const lead =
      first?.classList.contains("turn-space") === true ? first.nextElementSibling : first;
    refs.set(container, lead as HTMLElement | null);
  }
  return refs;
}

/**
 * Bring an already-mounted node down to the inserted ordinal and step the reference past it; only a node at or
 * below the reference moves.
 */
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

/**
 * Place a lazily created container where the store puts it: above the first mounted ordinal after `at`. By range
 * it would split one run into two groups. `at` absent appends; later reseats are `runCardFor`'s and `pipelineBoxFor`'s.
 */
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
  // Not `appendBlock`: the box supersedes no trace, and the reference must be captured before the insert.
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

/**
 * Where each container breaks its run of tool cards and how far its content reaches, built once per pass rather
 * than a quadratic per-card question.
 */
interface GroupIndex {
  /**
   * Container key to ascending `seq`s where a run may start: past a closing entry or a nested box's establishing
   * entry. Store `seq`s, so a run start is stable across ranges.
   */
  readonly starts: ReadonlyMap<string, number[]>;
  /** Container key to the greatest `seq` posting into it in the store, so a trace's successor counts unmounted. */
  readonly lastPost: ReadonlyMap<string, number>;
}

function indexGroups(st: TurnRender): GroupIndex {
  const starts = new Map<string, number[]>();
  const lastPost = new Map<string, number>();
  const built = new Set<string>();
  // Each box's seat (host and establishing index), the only input `rendersExpanded` needs.
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
  // Priced and recorded at the store's index for `placeContainer`: the run break and the box's seat are one fact.
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
  // Every entry this view draws at its own position posts into the root lane at its `seq` and closes the tool-card
  // run unless it is a tool card, so any rendering kind breaks a run. A delegate posts only its card.
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
      // The delegate's card, priced at its invocation, hosted by its pipeline's box if any.
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
      // Gated on the owner, as `placeEntry` is: a later mention mounts a tool row and must not post a break.
      openBox(`run:${held.runID}`, "", e.seq);
    } else if (isPipelineInvocation(tc)) {
      // At the driver's entry, where the box stands; the box outlives a count of 1.
      if (hostsPipelineBox(st, tc.id) || driverNeedsBox(st, tc)) {
        openBox(`pipe:${tc.id}`, "", e.seq);
      }
    } else {
      post(st.lane, e.seq, false);
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

/**
 * Whether an element at `at` in `host` renders expanded under the newest-element policy, the one gate. Only the root
 * lane `""` qualifies, which is the no-cascade rule; the turn card applies it separately at turn scope.
 */
function rendersExpanded(idx: GroupIndex, host: string, at: number): boolean {
  return host === "" && !containerFollowed(idx, host, at);
}

/** The `seq` the tool-card run holding `i` started at: the key of the group that card joins. */
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

/** Whether anything posts into `key` after `i`: what seals a reasoning trace. */
function containerFollowed(idx: GroupIndex, key: string, i: number): boolean {
  return (idx.lastPost.get(key) ?? -1) > i;
}

/**
 * Whether a later run starts where this one ends; `containerFollowed(runStart)` would read every multi-card run as
 * followed.
 */
function runFollowed(idx: GroupIndex, key: string, runStart: number): boolean {
  const starts = idx.starts.get(key) ?? [];
  return (starts[starts.length - 1] ?? -1) > runStart;
}

/**
 * Apply the newest-element verdict to every collapsible container. Collapse-only, so idempotent. A box's verdict
 * is per seat (`st.boxExpanded`), a group's needs `starts`; an absent seat reads expanded.
 */
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

/** Mount the turn's first `plan` entry as a card, or bring it to the newest plan state read off the store. */
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
  // What the card shows, which `syncFolds` compares against.
  st.planFrom = newest.seq;
}

// Subagent classification / parsing

/** KAS's prefix on a pipeline stage's tool-call id: `invoke_subagent_<orchestrateToolCallId>_stage_<stageName>`. */
const STAGE_PREFIX = "invoke_subagent_";
const STAGE_SEP = "_stage_";

/**
 * The orchestrate id a stage belongs to, or "". `indexOf`, since the stage name is author-supplied; parsed the same
 * way in `subagent-slice.ts`.
 */
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

/**
 * Learn each stage's pipeline and each driver's declared count from the tool-call array, so arrival order does not
 * matter. A stage keeps its first pipeline.
 */
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

/** The pipeline box's label, kind plus stage count; matches `subagent-exec-source.ts`'s `ExecRun.label`. */
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

/** The greater of the declared and observed stage counts, both lower bounds. */
function pipelineStageCount(st: TurnRender, pipelineID: string): number {
  const declared = st.pipelineDeclared.get(pipelineID) ?? 0;
  return Math.max(declared, (st.pipelineStages.get(pipelineID) ?? []).length);
}

/**
 * Whether this pipeline renders a container: one stage is promoted instead, every other count keeps it, zero
 * included.
 */
function pipelineHasContainer(st: TurnRender, pipelineID: string): boolean {
  return pipelineStageCount(st, pipelineID) !== 1;
}

/** Whether the driver's own block has a box: a driver that settled with no stage would otherwise be invisible. */
function driverNeedsBox(st: TurnRender, tc: ToolCall): boolean {
  if (pipelineHasContainer(st, tc.id)) {
    return true;
  }
  return !isToolActive(tc.status) && (st.pipelineStages.get(tc.id)?.length ?? 0) === 0;
}
