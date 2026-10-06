// The transcript shell and multiplexer: `#messages` holds one `.transcript-view` per resident chat, the active one
// live and the parked ones frozen. Assistant bodies are composed by messages-blocks.ts.

import type { EntryPrompt, Session } from "./types.js";
import {
  get,
  getActive,
  getActiveId,
  watchActiveId,
  messagesVersionOf,
  renderCauseOf,
  bumpMessages,
  registerEvictionExemption,
  codeReferencesFor,
  liveRefusalFor,
} from "./store.js";
import { clearTurnSigs } from "./store-signals.js";
import { buildEvent } from "./messages-events.js";
import { releaseClampsIn } from "./clamp-text.js";
import { effect, el, touch } from "@cplieger/reactive";
import { reconcile, KEY_ATTR, type ReconcileSpec } from "./reconcile.js";
import { CHAT_SKELETON_ID } from "./skeleton.js";
import { $ } from "./dom.js";
import {
  getScrollEl,
  scrollToBottom,
  resetScrollState,
  setLoadMore,
  deferWhileReading,
  preserveReadingPosition,
  fillViewport,
  onReadingStateChange,
  onReaderGesture,
  onViewportChange,
  setAnchorProvider,
  setResumeLabel,
  readingState,
  jumpTo,
  attach as attachScroll,
  detach as detachScroll,
  type ReadingState,
} from "./scroll.js";
import {
  buildTurnHeader,
  updateTurnHeader,
  type TurnHeaderData,
} from "./fundamentals/turn-header.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  earnsTurnFooter,
  type TurnSummaryData,
} from "./fundamentals/turn-footer.js";
import {
  closeOfBody,
  payloadOf,
  projectTurn,
  projectTurns,
  turnCloseOf,
  turnLedger,
  turnAnchorID,
  turnFaceProse,
  turnFailureText,
  turnFoldHides,
  type Turn,
} from "./turns.js";
import { severityOf } from "./turn-severity.js";
import { ICON_REWIND } from "./icons.js";
import { buildAssistantBubble } from "./fundamentals/text-bubble.js";
import { isTurnOpen, isTurnRevealed, setTurnOpen } from "./fold-state.js";
import {
  entryRenders,
  firstPlanSeq,
  planResidency,
  rendersNothing,
  runCardOwners,
  sliceTurn,
  turnOrdinalOf,
  turnSpan,
  OVERSCAN_ENTRIES,
  RESIDENT_ENTRIES,
  type EntryRange,
  type ResidencyAnchor,
} from "./block-window.js";
import { forgetHeights, spacerHeight } from "./block-heights.js";
import { wireRowToggle } from "./disclosure-row.js";
import { initSearchRevealBuilder, searchHitCount } from "./chat-search.js";
import {
  mountTurnRail,
  setResidentTurns,
  resetTurnRail,
  loadTurnRail,
  pointTurnRail,
  initTurnRailCallbacks,
} from "./turn-rail.js";
import {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  disposeAssistantBody,
  pauseAssistantBody,
  resumeAssistantBody,
  resetBlockRenders,
  refreshGroupHeader,
  refreshMessageCard,
  liveRenderIDs,
  initBlockRenderer,
  getLiveAnchor,
  mountedWindow,
  blockElement,
  mountedToolCalls,
  mountHeadRange,
  dropHead,
  dropTail,
  geometrySkipped,
  setRunCardOwners,
} from "./messages-blocks.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { rewindChat } from "./actions/rewind.js";
import { compactChat } from "./actions/chat.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { confirm as confirmDialog } from "./confirm.js";
import { registerCleanup } from "./actions/index.js";
import {
  disposeAllToolEffects,
  disposeToolEffectsForChat,
  detachToolEffectsForRebuild,
  suspendToolEffectsFor,
  resumeToolEffectsFor,
  drainParkedTerminals,
  initToolCallbacks,
  initToolViewCallbacks,
} from "./messages-tools.js";
import {
  mountTurnFooterActions,
  turnMarkdown,
  initTurnActionCallbacks,
  initTurnActionsBodyProbe,
} from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";

// --- Public re-exports ---

export { getScrollEl, setLoadMore };
// This module owns the rail, so chat.ts reaches it through here.
export { loadTurnRail, pointTurnRail };

// --- Module state ---

const messagesEl = $.messages;

// Exactly one resident view carries `.is-active` and the scroller's observers. A parked view keeps its DOM and
// renders with every writer paused; the store keeps ingesting.

/** How many parked views stay resident, the active one not counted; past it the LRU parked view is disposed. */
export const PARKED_VIEWS = 3;

/**
 * One resident chat view; the saved-at-park fields mean something only while `parked`. The card walk answers
 * view-to-turn (`mountedBodies`).
 */
interface ChatView {
  chatID: string;
  el: HTMLElement;
  parked: boolean;
  scrollTop: number;
  readingState: ReadingState;
  followBaseline: number;
  reachableEntries: number;
  lastNewestTurnID: string | undefined;
  resumeLabel: string;
  /**
   * Turn ids still being written into at park. Resume rebuilds them even if closed since: their binding effects were
   * disposed, so they missed every update while parked.
   */
  pausedStreaming: Set<string>;
}

/** Resident views by chat id, iterated in LRU order: activation re-inserts, so the first parked one is evicted. */
const views = new Map<string, ChatView>();
let activeView: ChatView | null = null;

/** The active view's element, or null when none is active (boot, the last-tab window). */
export function activeTranscriptView(): HTMLElement | null {
  return activeView?.el ?? null;
}

/** A resident view's element for `chatID` — active or parked — or null. */
export function transcriptViewFor(chatID: string): HTMLElement | null {
  return views.get(chatID)?.el ?? null;
}

/**
 * Reveal a run's card in `chatID`'s transcript; returns whether it landed. Best-effort and never unfolds. Scoped to
 * that chat's view, since `.run-card[data-run]` repeats once per resident view.
 */
export function revealRunCard(chatID: string, workflowID: string): boolean {
  if (workflowID === "") {
    return false;
  }
  const view = transcriptViewFor(chatID);
  if (view === null) {
    return false;
  }
  const card = view.querySelector<HTMLElement>(`.run-card[data-run="${CSS.escape(workflowID)}"]`);
  if (card === null) {
    return false;
  }
  // `jumpTo`, not a raw `scrollIntoView`: it owns whether the jump parks the reader.
  jumpTo(card, { block: "start", behavior: "smooth" });
  return true;
}

/** Where paint reconciles: the active view, or the bare multiplexer before any view exists. */
function paintRoot(): HTMLElement {
  return activeView?.el ?? messagesEl;
}

/** The turn bodies this view has mounted, as `(turn id, body)`, found by walking cards. */
function mountedBodies(view: ChatView): [string, HTMLElement][] {
  const out: [string, HTMLElement][] = [];
  for (const card of view.el.querySelectorAll<HTMLElement>(".turn")) {
    const turnID = card.getAttribute(KEY_ATTR);
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    if (turnID !== null && body !== null) {
      out.push([turnID, body]);
    }
  }
  return out;
}

/** Park the active view: pause every writer, save the handle, hide. */
function parkView(view: ChatView): void {
  // Focus first: an inert subtree drops focus to <body>, and the composer is the app's focus home.
  const focused = document.activeElement;
  if (focused !== null && view.el.contains(focused)) {
    $.promptInput.focus();
  }
  const scroll = detachScroll();
  view.scrollTop = scroll.scrollTop;
  view.readingState = scroll.readingState;
  view.followBaseline = followBaseline;
  view.reachableEntries = reachableEntries;
  view.lastNewestTurnID = lastNewestTurnID;
  view.resumeLabel = $.scrollBottom.querySelector("span")?.textContent ?? "";
  view.pausedStreaming = new Set();
  const session = get(view.chatID);
  for (const [turnID] of mountedBodies(view)) {
    // The store decides off `turn_close` absence: an open turn will be written into again, so resume rebuilds it.
    const held = session?.turns.get(turnID);
    if (held !== undefined && turnCloseOf(held) === undefined) {
      view.pausedStreaming.add(turnID);
    }
    pauseTurnBody(view, turnID);
  }
  view.el.classList.remove("is-active");
  view.el.inert = true;
  view.parked = true;
  if (activeView === view) {
    activeView = null;
  }
}

/**
 * Make `chatID`'s view active, creating it if not resident. True when the view was unparked (a catch-up paint and
 * resume must follow).
 */
function activateView(chatID: string): boolean {
  let view = views.get(chatID);
  const unparking = view?.parked === true;
  const created = view === undefined;
  if (view === undefined) {
    view = {
      chatID,
      el: el("div", { className: "transcript-view" }),
      parked: false,
      scrollTop: 0,
      readingState: "following",
      followBaseline: 0,
      reachableEntries: 0,
      lastNewestTurnID: undefined,
      resumeLabel: "",
      pausedStreaming: new Set(),
    };
    messagesEl.appendChild(view.el);
  } else {
    // LRU refresh: re-insertion moves this chat to the back.
    views.delete(chatID);
  }
  views.set(chatID, view);
  view.parked = false;
  view.el.inert = false;
  view.el.classList.add("is-active");
  activeView = view;
  attachScroll({ el: view.el, scrollTop: view.scrollTop, readingState: view.readingState });
  if (created) {
    // After the attach, and only for a view this call created: `resetScrollState` ends in `setLoadMore(null, false)`,
    // scoped to the attached view, so earlier it strips the outgoing view's pagination, and on an existing view it
    // removes pagination the previous activation wired.
    resetScrollState();
  }
  // After attach: entering Reading recomputes the baseline from the active session, and these are the parked chat's.
  followBaseline = view.followBaseline;
  reachableEntries = view.reachableEntries;
  lastNewestTurnID = view.lastNewestTurnID;
  if (unparking) {
    setResumeLabel(view.resumeLabel);
  }
  evictParkedViews();
  return unparking;
}

/** Dispose least-recently-used parked views past the budget. */
function evictParkedViews(): void {
  const parked = [...views.values()].filter((v) => v.parked);
  for (let i = 0; i <= parked.length - 1 - PARKED_VIEWS; i++) {
    const victim = parked[i];
    if (victim !== undefined) {
      disposeChatView(victim.chatID);
    }
  }
}

/** Dispose views whose chat left the store (close, delete, mass removal). */
function pruneDeadViews(): void {
  for (const chatID of [...views.keys()]) {
    if (get(chatID) === undefined) {
      disposeChatView(chatID);
    }
  }
}

/**
 * The real per-view dispose: `disposeTurnBody` per card, the chat's tool effects, the container's removal. Every
 * exit path runs this, never a bare empty reconcile that would leave parked DOM behind.
 */
export function disposeChatView(chatID: string): void {
  const view = views.get(chatID);
  if (view === undefined) {
    return;
  }
  for (const card of view.el.querySelectorAll<HTMLElement>(".turn")) {
    const turnID = card.getAttribute(KEY_ATTR);
    if (turnID !== null) {
      disposeTurnBody(turnID);
    }
  }
  // Every clamp in the view in one sweep; `parkView` deliberately keeps its clamps and re-measures on unpark.
  releaseClampsIn(view.el);
  // After the row disposal, which records one last height per entry. Heights are keyed `(turnID, seq)`, and the cache
  // survives unmounts, so the view's dispose is where they stop meaning anything.
  forgetHeights(get(chatID)?.turn_order ?? []);
  disposeToolEffectsForChat(chatID, view.el);
  view.el.remove();
  views.delete(chatID);
  if (activeView === view) {
    activeView = null;
    lastActiveId = undefined;
  }
}

/**
 * Pause one turn's body: dispose its live bindings, finish its reveals, and suspend its tool-card effects. The
 * render, DOM and card stay.
 */
function pauseTurnBody(view: ChatView, turnID: string): void {
  disposeStreamingEffect(turnID);
  pauseAssistantBody(turnID);
  suspendToolEffectsFor(
    view.chatID,
    mountedToolCalls(turnID).map((tc) => tc.id),
    view.el,
  );
}

/**
 * Resume one turn's body after the catch-up paint. A closed turn only re-arms; one open at park rebuilds, since its
 * bindings were disposed and only a fresh render guarantees what landed while parked.
 */
function resumeTurnBody(
  view: ChatView,
  session: Session,
  turnID: string,
  body: HTMLElement,
  t: Turn | undefined,
): void {
  if (t !== undefined && view.pausedStreaming.has(turnID)) {
    rebuildTurnBody(session, t, body);
    return;
  }
  resumeAssistantBody(turnID);
  resumeToolEffectsFor(session.id, mountedToolCalls(turnID), view.el);
}

/** Rebuild a turn's body in place, keeping the card and its window; old render state goes through unmount disposal. */
function rebuildTurnBody(session: Session, t: Turn, body: HTMLElement): void {
  const want = mountedWindow(t.id) ?? bodyRange(t, wantedWindow.get(t.id) ?? EMPTY_RANGE);
  // Before the render's own disposers, which would drop the terminal links.
  const reattach = detachToolEffectsForRebuild(session.id, body);
  disposeStreamingEffect(t.id);
  finalizeAssistantBody(t.id);
  disposeAssistantBody(t.id);
  clearTurnSigs(t.id);
  releaseClampsIn(body);
  body.replaceChildren();
  buildAssistantBody(body, t, session.id, turnIsLive(t), want);
  reattach();
}

/**
 * Resume every paused body of the unparked view, then drain buffered terminal output once. One projection for the
 * pass, rather than one per body.
 */
function resumeView(view: ChatView, session: Session): void {
  const byID = new Map(projectTurns(session).map((t) => [t.id, t]));
  for (const [turnID, body] of mountedBodies(view)) {
    resumeTurnBody(view, session, turnID, body, byID.get(turnID));
  }
  view.pausedStreaming.clear();
  drainParkedTerminals(session.id, view.el);
}

/** bindLoadingState unsubs keyed by tool id, released at page teardown; a card's loading binding outlives its card. */
const bindUnbinds = new Map<string, (() => void)[]>();
function pushBind(key: string, unbind: () => void): void {
  let arr = bindUnbinds.get(key);
  if (arr === undefined) {
    arr = [];
    bindUnbinds.set(key, arr);
  }
  arr.push(unbind);
}

/**
 * Per-turn streaming cleanups, disposed on turn end and on unmount; separate so a tool card's loading binding
 * survives turn end.
 */
const streamingEffects = new Map<string, (() => void)[]>();
function pushStreamingEffect(turnID: string, fn: () => void): void {
  const arr = streamingEffects.get(turnID);
  if (arr === undefined) {
    streamingEffects.set(turnID, [fn]);
  } else {
    arr.push(fn);
  }
}
function disposeStreamingEffect(turnID: string): void {
  const arr = streamingEffects.get(turnID);
  if (arr !== undefined) {
    for (const fn of arr) {
      fn();
    }
    streamingEffects.delete(turnID);
  }
  const per = entryEffects.get(turnID);
  if (per !== undefined) {
    disposeEntryEffects(turnID, [...per.keys()]);
  }
}

/**
 * Per-entry cleanups (turn id to entry `seq`): an entry leaving the window takes its own subscriptions. Turn end
 * and card removal release everything.
 */
const entryEffects = new Map<string, Map<number, (() => void)[]>>();

function pushEntryEffect(turnID: string, seq: number, fn: () => void): void {
  let per = entryEffects.get(turnID);
  if (per === undefined) {
    per = new Map();
    entryEffects.set(turnID, per);
  }
  const arr = per.get(seq);
  if (arr === undefined) {
    per.set(seq, [fn]);
  } else {
    arr.push(fn);
  }
}

/** Run and clear the cleanups for `seqs`: the window drop's half of the contract. */
function disposeEntryEffects(turnID: string, seqs: Iterable<number>): void {
  const per = entryEffects.get(turnID);
  if (per === undefined) {
    return;
  }
  for (const seq of seqs) {
    const arr = per.get(seq);
    per.delete(seq);
    for (const fn of arr ?? []) {
      fn();
    }
  }
  if (per.size === 0) {
    entryEffects.delete(turnID);
  }
}

/**
 * Turn ids newly appended since the last paint. The entry animation and the sent-turn pin read it, and both stay
 * silent for a replay or prepend the reader did not cause.
 */
const appendNewIds = new Set<string>();
let lastNewestTurnID: string | undefined;
let lastActiveId: string | undefined;

/** The newest resident turn id, where the next arrival walk starts; `turn_order` is file order. */
function newestTurnID(s: Session): string | undefined {
  return s.turn_order[s.turn_order.length - 1];
}

function svgTemplate(markup: string): () => Node {
  const tpl = document.createElement("template");
  tpl.innerHTML = markup;
  const content = tpl.content;
  return () => content.cloneNode(true);
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

let mounted = false;

initToolCallbacks({
  pushBind,
  refreshGroupHeader,
  explainError,
});
initTurnActionCallbacks({ svgTemplate });
initBlockRenderer({
  pushStreamingEffect,
  pushEntryEffect,
  disposeEntryEffects,
  makeRow,
  makeEvent: buildEvent,
});
// The refusal callout's Rewind reuses this module's rewind flow; injected because refusal.ts cannot import this.
setRefusalRewindHandler((target) => {
  void handleRewindClick(target).catch((e: unknown) => {
    console.warn("refusal rewind failed", e);
  });
});

/**
 * Mount the chat view, once, from app.ts boot: reconciles on every store bump. Streaming chunks flow through
 * per-block signals, not this effect.
 */
export function mountChatView(): void {
  if (mounted) {
    return;
  }
  mounted = true;
  initFollowModel();
  // In the positioned outer wrapper, not the scroller, so it does not scroll away with the content.
  mountTurnRail($.messagesWrapOuter);
  // Both stub-landing surfaces call this module's on-demand build; injected because a static import back would cycle.
  initTurnRailCallbacks({ mountTurnBody, activeView: activeTranscriptView });
  // The ordinal is resolved in this projection, the one the grant is clamped against.
  initSearchRevealBuilder(
    (chatID, turnID, entryID) => {
      const t = turnByID.get(turnID);
      return mountTurnBody(chatID, turnID, t === undefined ? undefined : turnOrdinalOf(t, entryID));
    },
    mountTurnBodyForWalk,
    endWalkReveal,
  );
  // Scrolling re-windows, not repaints. Two hooks: the gesture says the reader moved, and the viewport change is the
  // frame to measure in. `onViewportChange` alone fires for this module's own compensation and would feed itself.
  onReaderGesture(noteReaderMoved);
  onViewportChange(windowPass);
  // The tool layer sits below this module, so the multiplexer injects whether a view is parked and that a resident
  // view's chat must not have its messages evicted.
  initToolViewCallbacks({
    isCardParked: (card) => {
      for (const v of views.values()) {
        if (v.parked && v.el.contains(card)) {
          return true;
        }
      }
      return false;
    },
  });
  registerEvictionExemption((chatID) => views.has(chatID));
  // Page unload is the one moment every view goes away; close/delete/LRU dispose per view.
  registerCleanup(teardownAll);
  // Inputs: which chat is active, and that chat's transcript version. Header-only updates bump no version.
  effect(() => {
    const id = watchActiveId();
    touch(messagesVersionOf(id));
    paint();
  });
}

// ---------------------------------------------------------------------------
// The follow model's two client-side obligations.
// ---------------------------------------------------------------------------

/**
 * Entries the reader can reach, which the resume chip counts; `entryRenders` is the test, so laned entries and fold
 * kinds, which add no height, are not counted.
 */
function entryCount(turns: readonly Turn[]): number {
  let n = 0;
  for (const t of turns) {
    const firstPlan = firstPlanSeq(t, "");
    for (const e of t.body) {
      if (entryRenders(e, "", firstPlan)) {
        n++;
      }
    }
  }
  return n;
}

/** Reachable entries present when the reader last entered Reading. */
let followBaseline = 0;

/**
 * The last full pass's reachable-entry count. Chunk and tool paints cannot add a reachable entry, so the walk runs
 * once per full pass.
 */
let reachableEntries = 0;

function initFollowModel(): void {
  // Following pins to the active text block, not the document bottom, or a large card below it scrolls the sentence
  // away. A registry read, since the follow path runs per frame.
  setAnchorProvider(getLiveAnchor);
  onReadingStateChange((next) => {
    if (next === "reading") {
      // A fresh count at the park, over the last full pass's projection: what is on screen.
      reachableEntries = entryCount(lastTurns);
      followBaseline = reachableEntries;
    }
    refreshResumeLabel();
  });
}

/** The resume control is the one element that knows the reader is behind, so it says how far. */
function refreshResumeLabel(): void {
  if (readingState() === "following") {
    return;
  }
  const session = getActive();
  const behind = reachableEntries - followBaseline;
  if (behind > 0) {
    setResumeLabel(`${String(behind)} new block${behind === 1 ? "" : "s"}`);
    return;
  }
  // Nothing new since they parked: say what the turn is doing rather than a count of zero.
  setResumeLabel(session?.thinking === true ? session.working_label || "Working" : "Latest");
}

/**
 * What this pass wants per turn on two axes: fold open/closed and residency. Computed before the reconcile so a new
 * card is born in its final state; existing cards go through `applyFoldPass`.
 */
interface FoldPlan {
  open: boolean;
  mounted: boolean;
  /**
   * Whether the header offers the fold. False for the newest turn, a running turn and one whose fold hides nothing;
   * true for every stub, where the toggle is the only way to a body.
   */
  canFold: boolean;
}
const foldPlan = new Map<string, FoldPlan>();

/**
 * Turn id to the ordinals its body may hold now: the plan's window united with the reader's demand. `has()` is
 * `mounted`, so it is cleared per pass.
 */
const wantedWindow = new Map<string, EntryRange>();

/** A range covering nothing, for a turn that renders nothing, so `.is-bodyless` has a body to mark. */
const EMPTY_RANGE: EntryRange = { from: 0, to: 0 };

/** Every ordinal a turn could have, for a caller with no range to name. */
const WHOLE_TURN: EntryRange = { from: 0, to: Number.MAX_SAFE_INTEGER };

/** Whether `outer` holds every ordinal of `inner`. */
function covers(outer: EntryRange, inner: EntryRange): boolean {
  return outer.from <= inner.from && outer.to >= inner.to;
}

function computeFoldPlan(
  chatID: string,
  turns: readonly Turn[],
  openable: readonly Turn[],
  anchor: ResidencyAnchor | undefined,
): void {
  foldPlan.clear();
  turnByID.clear();
  wantedWindow.clear();
  // The dispatcher's one turn-scope input: a render holds one turn and cannot see its neighbours' runs.
  setRunCardOwners(runCardOwners(turns));
  // The transcript's root lane; `planResidency` takes no default.
  const window = planResidency(openable, anchor, "");
  for (const [i, t] of turns.entries()) {
    turnByID.set(t.id, t);
    const hides = turnFoldHides(t);
    const policyOpen = isTurnOpen(chatID, t, i, turns.length);
    // The reader's request outranks the plan until it expires; the covering range wins, never the hull, which can
    // span a whole huge turn.
    const asked = demandRange(chatID, t);
    const grown = window.get(t.id);
    // A replace retracts nothing here: the next pass's drop and `syncSpacers` do, so the body is briefly over-height,
    // never under.
    const merged =
      grown === undefined ? asked : asked === undefined || covers(grown, asked) ? grown : asked;
    // A turn that renders nothing is bodied whatever the budget, for `.is-bodyless`; one with rows is a stub, since
    // an empty range emits the whole-turn tail spacer.
    const range = merged ?? (rendersNothing(t) ? EMPTY_RANGE : undefined);
    const mounted = range !== undefined;
    if (range !== undefined) {
      wantedWindow.set(t.id, range);
    }
    // A hides-nothing turn stays open while resident: its face would equal its body.
    const wantOpen = policyOpen || (!hides && mounted);
    foldPlan.set(t.id, {
      // A stub has no body, so it cannot render open; the override survives for the next pass.
      open: wantOpen && mounted,
      mounted,
      canFold: !mounted || (hides && i < turns.length - 1 && t.outcome !== "running"),
    });
  }
}

/** How long a `demandPin` lives if nothing clears it: clears a smooth `jumpTo` flight and `scroll.ts`'s 700ms pin. */
const PIN_GOAL_MS = 2000;

/**
 * Where the reader last asked to be, and until when. `mountTurnBody` is the only writer. One slot: a jump leaves
 * the turn it jumped from.
 */
let demandPin: { chatID: string; turnID: string; at: number; until: number } | undefined;

/** The turns a search-wide reveal built for the DOM walker, held until `chat-search.ts` ends the reveal. */
let demandWalk: { chatID: string; turnIDs: Set<string> } | undefined;

/**
 * The ordinals explicitly asked for in `t`: the live pin, else the walk, one overscan each side. The pin outranks
 * the walk.
 */
function demandRange(chatID: string, t: Turn): EntryRange | undefined {
  // `turnSpan`, not a rendered count: a grant is `seq` space.
  const span = turnSpan(t);
  const pin = demandPin;
  // A revealed turn is a standing request the clock may not take back; the single pin slot bounds it to one turn.
  if (
    pin?.chatID === chatID &&
    pin.turnID === t.id &&
    (Date.now() <= pin.until || isTurnRevealed(chatID, t.id))
  ) {
    // Clamped into the span: a recorded ordinal outlives its block, and an `at` past the span grants `from > to`.
    const at = Math.min(Math.max(pin.at, 0), Math.max(0, span - 1));
    return {
      from: Math.max(0, at - OVERSCAN_ENTRIES),
      to: Math.min(span, at + OVERSCAN_ENTRIES),
    };
  }
  if (demandWalk?.chatID === chatID && demandWalk.turnIDs.has(t.id)) {
    return { from: 0, to: Math.min(span, 2 * OVERSCAN_ENTRIES) };
  }
  return undefined;
}

/** The latest projection per turn id; the fold toggle reads it, since its closure holds the build-time turn. */
const turnByID = new Map<string, Turn>();

/** The last full pass's projection, which `windowPass` re-filters per scroll frame. */
let lastTurns: readonly Turn[] = [];

// `el.offsetTop` is comparable with `scrollTop` only because these rows are the `.msg-row`s, whose offset parent is
// `#messages-wrap` (`content-visibility: auto` makes each a containing block). Measuring a bubble, or adding
// `position: relative` to a card type, breaks this. Pick the last entry at or above the viewport top, or the first.

/**
 * Where the reader is, or `undefined` for the live edge. Membership is the store predicate, never `data-folded`,
 * which a deferral can leave behind the plan.
 */
function residencyAnchor(openable: readonly Turn[]): ResidencyAnchor | undefined {
  // Following outranks every DOM read: it means pinned to the live edge, and measuring a filling body lands behind it.
  if (readingState() === "following") {
    return undefined;
  }
  const scrollEl = getScrollEl();
  if (scrollEl.scrollHeight <= scrollEl.clientHeight) {
    return undefined;
  }
  const top = scrollEl.scrollTop;
  const cards = turnCards(paintRoot());
  const at = pickIndex(cards, top);
  const grown = new Set(openable.map((t) => t.id));
  for (let i = at; i < cards.length; i++) {
    const card = cards[i];
    const id = card?.getAttribute(KEY_ATTR);
    if (card === undefined || id === null || id === undefined || !grown.has(id)) {
      continue;
    }
    // A card the walk stepped forward to starts at the reader, so its first ordinal is the seed.
    return i === at ? { turnID: id, at: cardOrdinal(id, card, top) } : { turnID: id, at: 0 };
  }
  return undefined;
}

/** The index of the last entry at or above `top`, or 0. */
function pickIndex(entries: readonly HTMLElement[], top: number): number {
  let at = 0;
  for (const [i, e] of entries.entries()) {
    if (e.offsetTop <= top) {
      at = i;
    }
  }
  return at;
}

/**
 * The ordinal of `turnID` at the viewport top inside its card; a card rendered folded answers its first ordinal.
 * One level, since the renderer files one element per mounted `seq`; the range is the render's own (`mountedWindow`).
 */
function cardOrdinal(turnID: string, card: HTMLElement, top: number): number {
  const range = mountedWindow(turnID);
  if (range === undefined || card.hasAttribute("data-folded")) {
    return 0;
  }
  // A prose run files every seq against its one row, so the element's first seq is the ordinal.
  const seqs: number[] = [];
  const rows: HTMLElement[] = [];
  for (let seq = range.from; seq < range.to; seq++) {
    const row = blockElement(turnID, seq);
    if (row !== undefined && row !== rows[rows.length - 1]) {
      seqs.push(seq);
      rows.push(row);
    }
  }
  return seqs[pickIndex(rows, top)] ?? range.from;
}

/** Drop the pin once the reader has arrived within one overscan of it; a pin outside `openable` expires at `until`. */
function clearArrivedPin(chatID: string, anchor: ResidencyAnchor | undefined): void {
  const pin = demandPin;
  if (pin === undefined || anchor === undefined) {
    return;
  }
  if (pin.chatID === chatID && pin.turnID === anchor.turnID) {
    if (Math.abs(anchor.at - pin.at) < OVERSCAN_ENTRIES) {
      demandPin = undefined;
    }
  }
}

/** Whether the current full pass mounted a new card: born-folded cards queue nothing, so `fillViewport` needs this. */
let paintMountedCards = false;

function paint(): void {
  const session = getActive();
  if (session === undefined) {
    // Touch the views only when there is genuinely no active chat: an empty reconcile during a chat switch, then a
    // re-populate, flashes.
    if (getActiveId() === "") {
      // Hide without disposing: the last-tab window can reopen this chat. Disposal belongs to close/delete.
      pruneDeadViews();
      if (activeView !== null) {
        parkView(activeView);
      }
      lastActiveId = undefined;
    }
    return;
  }
  const isChatSwitch = lastActiveId !== session.id;
  let unparked = false;
  if (isChatSwitch) {
    pruneDeadViews();
    if (activeView !== null && activeView.chatID !== session.id) {
      parkView(activeView);
    }
    unparked = activateView(session.id);
  }
  // What the flushed version was for, untracked by design. A chat switch is always the full pass.
  const flushed = isChatSwitch ? undefined : renderCauseOf(session.id);
  if (flushed?.cause === "chunk") {
    // Text growth of mounted entries: their signal effects painted it, so only tail bookkeeping remains.
    refreshResumeLabel();
    lastNewestTurnID = newestTurnID(session);
    return;
  }
  if (flushed?.cause === "tool" && refreshToolTurn(session, flushed.turnID)) {
    // The keyed update refreshed that one turn; an absent render falls through to the full pass, which alone mounts.
    refreshResumeLabel();
    lastNewestTurnID = newestTurnID(session);
    return;
  }
  // Only genuinely new appended turns get the entry animation.
  appendNewIds.clear();
  // A fetched window is a replay, and only the paint's cause says so: a cold open paints before `loadMessages`
  // resolves, so its post-fetch paint looks like a first prompt.
  const replayed = isChatSwitch || flushed?.cause === "load";
  if (!replayed) {
    // An unset tail means the previous paint held no turns, so every turn here arrived (a fresh chat's first prompt).
    // A tail the order no longer carries names no arrivals.
    let from = 0;
    if (lastNewestTurnID !== undefined) {
      // Reverse scan: the tail is near the end, so this is O(1) amortized.
      from = -1;
      for (let i = session.turn_order.length - 1; i >= 0; i--) {
        if (session.turn_order[i] === lastNewestTurnID) {
          from = i + 1;
          break;
        }
      }
    }
    if (from >= 0) {
      for (let i = from; i < session.turn_order.length; i++) {
        const id = session.turn_order[i];
        if (id !== undefined) {
          appendNewIds.add(id);
        }
      }
    }
  }
  const turns = projectTurns(session);
  // The turns the window grows over: a folded body holds zero height. A store predicate, never `data-folded`.
  const openable = turns.filter(
    (t, i) => isTurnOpen(session.id, t, i, turns.length) || !turnFoldHides(t),
  );
  lastTurns = turns;
  const anchor = residencyAnchor(openable);
  clearArrivedPin(session.id, anchor);
  computeFoldPlan(session.id, turns, openable, anchor);
  paintMountedCards = false;
  paintSyncEntries = PAINT_SYNC_ENTRIES;
  const root = paintRoot();
  // The placeholder never coexists with content, so it is dropped where content lands, scoped to this view. An empty
  // turn list is a chat still loading; the load-more furniture mounts beside real turns.
  if (turns.length > 0) {
    const skel = document.getElementById(CHAT_SKELETON_ID);
    if (skel !== null && root.contains(skel)) {
      skel.remove();
    }
  }
  reconcile(root, turns, turnSpec);
  // One walk builds the card list the rail and the fold pass share.
  const cards = turnCards(root);
  // Re-run per full pass: the set changes as pages load and turns arrive.
  setResidentTurns(cards);
  applyFoldPass(session.id, turns, cards, false);
  // After the fold pass: an unmounted card is owed no build, and a folded one builds invisibly.
  drainColdBuilds();
  finalizeStreamingIfNeeded(turns);
  reachableEntries = entryCount(turns);
  refreshResumeLabel();
  lastNewestTurnID = newestTurnID(session);
  lastActiveId = session.id;
  if (unparked && activeView !== null) {
    // The DOM is at the store's state; now the paused effects come back.
    resumeView(activeView, session);
  }
}

/** The turn cards of `root` in document order, filtering out unkeyed furniture. */
function turnCards(root: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (const child of root.children) {
    if (child.classList.contains("turn")) {
      out.push(child as HTMLElement);
    }
  }
  return out;
}

/** Not re-entrant: the head compensation writes `scrollTop`. Across frames the plan-equality exit terminates. */
let inWindowPass = false;

/**
 * The plan the DOM was last brought to, which the window pass compares against. Not the current plan:
 * `startDemandBuild` writes its grant first, so comparing with itself would refuse the pass that applies it.
 */
let appliedPlan = "";

/**
 * Whether the reader stated a position since the last scroll pass. This module's own compensation publishes no
 * gesture, so the pass cannot schedule itself.
 */
let readerMoved = false;

function noteReaderMoved(): void {
  readerMoved = true;
}

/**
 * Re-window on a settled scroll frame the reader caused, over the last full pass's projection; `openable` is
 * recomputed since a fold toggle changes it. `fromScroll` false is a build settling.
 */
function windowPass(fromScroll = true): void {
  if (inWindowPass) {
    return;
  }
  if (fromScroll) {
    if (!readerMoved) {
      return;
    }
    readerMoved = false;
  }
  const session = getActive();
  const turns = lastTurns;
  if (session === undefined || session.id !== lastActiveId || turns.length === 0) {
    return;
  }
  // A filling body reports coordinates for a partial range; the builder's settle runs the skipped pass.
  if (coldBuilds.size > 0 || turnBodyBuilds.size > 0) {
    return;
  }
  const openable = turns.filter(
    (t, i) => isTurnOpen(session.id, t, i, turns.length) || !turnFoldHides(t),
  );
  const anchor = residencyAnchor(openable);
  clearArrivedPin(session.id, anchor);
  computeFoldPlan(session.id, turns, openable, anchor);
  if (planSignature() === appliedPlan) {
    return; // the common case for a scroll that stays inside the overscan
  }
  inWindowPass = true;
  try {
    applyFoldPass(session.id, turns, turnCards(paintRoot()), true);
  } finally {
    inWindowPass = false;
  }
  drainColdBuilds();
  refreshResumeLabel();
}

/** What the current plan asks of every turn, as one comparable string: the window pass's no-op exit. */
function planSignature(): string {
  const parts: string[] = [];
  for (const [id, plan] of foldPlan) {
    const range = wantedWindow.get(id);
    const window = range === undefined ? "-" : `${String(range.from)}:${String(range.to)}`;
    parts.push(
      `${id}|${String(plan.open)}${String(plan.mounted)}${String(plan.canFold)}|${window}`,
    );
  }
  return parts.join(",");
}

/**
 * The `tool`-cause fast path: refresh the owning turn's mounted body; false sends the caller to the full pass.
 * `projectTurn` slices one turn, since a tool update per frame must not walk the window.
 */
function refreshToolTurn(session: Session, turnID: string | undefined): boolean {
  if (turnID === undefined) {
    return false;
  }
  const t = projectTurn(session, turnID);
  if (t === undefined) {
    return false;
  }
  return refreshMessageCard(t, session.id, turnIsLive(t));
}

/**
 * The rewind confirmation, the only guard, so it states the losses: the turn and everything after leave, files
 * roll back to KAS's snapshots, and there is no undo.
 */
function rewindConfirmText(target: EntryPrompt, discarded: readonly Turn[]): string {
  const promptRaw = target.text.trim().replace(/\s+/g, " ");
  const prompt = promptRaw.length > 100 ? promptRaw.slice(0, 100) + "\u2026" : promptRaw;
  const lines = ["Rewind to this turn?", ""];
  if (prompt.length > 0) {
    lines.push(`Prompt: "${prompt}"`);
  }

  // Count across every dropped turn: a revert discards all of them.
  const calls = discarded.flatMap((t) =>
    t.body.flatMap((e) => {
      const call = payloadOf(e, "tool_call");
      return call === undefined ? [] : [call];
    }),
  );
  const files = [
    ...new Set(
      calls.flatMap((c) => (c.locations ?? []).map((l) => l.path.split("/").pop() ?? l.path)),
    ),
  ];
  // `discarded` LEADS with the addressed turn, so the later ones are what remains.
  const later = Math.max(discarded.length - 1, 0);
  const turnWord = later === 1 ? "turn" : "turns";
  lines.push(`Discards this prompt and ${String(later)} later ${turnWord}.`);
  if (calls.length > 0) {
    const toolPart = `${String(calls.length)} tool call${calls.length === 1 ? "" : "s"}`;
    const filePart =
      files.length > 0
        ? `, ${String(files.length)} file${files.length === 1 ? "" : "s"} touched (${files.slice(0, 4).join(", ")}${files.length > 4 ? ", \u2026" : ""})`
        : "";
    lines.push(`Work being undone: ${toolPart}${filePart}.`);
  }

  lines.push("");
  lines.push(
    "Files are rolled back on disk to their state before this turn. " +
      "The prompt itself is discarded too, so you will need to retype it. " +
      "This cannot be undone.",
  );
  return lines.join("\n");
}

/**
 * Confirm and dispatch the rewind. Refused mid-turn: KAS throws on a session with a live abortController.
 * `mountRewind` disables the button; this is the second gate.
 */
async function handleRewindClick(target: EntryPrompt): Promise<void> {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  if (session.thinking) {
    return;
  }
  // Through the projection: an undrawn turn is not one the reader can be told they are discarding.
  const turns = projectTurns(session);
  const at = turns.findIndex((t) => t.trigger?.id === target.id);
  if (at < 0) {
    return;
  }
  const proceed = await confirmDialog(
    rewindConfirmText(target, turns.slice(at)),
    "Rewind",
    "destructive",
  );
  if (!proceed) {
    return;
  }
  await rewindChat.dispatch({ chatID: session.id, messageID: target.id });
}

/**
 * `/rewind`: the newest drawn prompt turn, through the footer's confirm. False when `chatID` is not active or has
 * no such turn.
 */
export async function rewindLatestTurn(chatID: string): Promise<boolean> {
  const session = getActive();
  if (session?.id !== chatID) {
    return false;
  }
  const turns = projectTurns(session);
  for (let i = turns.length - 1; i >= 0; i--) {
    const trigger = turns[i]?.trigger;
    if (trigger !== undefined) {
      await handleRewindClick(trigger);
      return true;
    }
  }
  return false;
}

/**
 * The multiplexer-wide teardown on page unload: the per-view dispose for every resident view, then the shared
 * surfaces and module-global state. Exported for tests.
 */
export function teardownAll(): void {
  for (const chatID of [...views.keys()]) {
    disposeChatView(chatID);
  }
  // For what no view reaches: a detached render's effects and rows a view walk could not see. Idempotent.
  for (const arr of bindUnbinds.values()) {
    for (const fn of arr) {
      fn();
    }
  }
  bindUnbinds.clear();
  for (const id of [...streamingEffects.keys()]) {
    disposeStreamingEffect(id);
  }
  disposeAllToolEffects();
  resetBlockRenders();
  resetScrollState();
  resetTurnRail();
  lastActiveId = undefined;
  lastNewestTurnID = undefined;
}

// Two keyed levels: turns by id, then each card's `.turn-body` over its entries. Safe because reconcile considers
// only keyed children, so a card's header and footer are invisible to the inner pass.

const turnSpec: ReconcileSpec<Turn> = {
  key: (t) => t.id,
  mount: (t) => {
    const card = buildTurn(t);
    // Only a genuinely new turn animates; replays and prepends mount silently.
    if (appendNewIds.has(t.id)) {
      card.setAttribute("data-chat-entry", "");
    }
    return card;
  },
  update: updateTurn,
  onRemove: (card, key) => {
    // The face holds only prose and leaves with the card. The body is never asked for again, so dispose it here.
    disposeTurnBody(key);
    // A rewind truncation drops a card while its view lives, so `disposeChatView` never runs for it.
    releaseClampsIn(card);
  },
};

/** The caret's input: a turn with no `turn_close` is still being written, which `outcome: "running"` means. */
function turnIsLive(t: Turn): boolean {
  return t.outcome === "running";
}

/**
 * The residency range snapped to whole prose runs, since an edge inside a run would mount two rows with two
 * parsers. Every mount, price and completion test reads through here, so they agree where the body ends.
 */
function bodyRange(t: Turn, range: EntryRange): EntryRange {
  return sliceTurn(t, range);
}

/** Which end of a body's window a spacer stands at. */
type SpacerSide = "head" | "tail";

/**
 * Price both spacers against the range the body now holds. An element, not `padding-block`, which transitions;
 * a sibling of the body, since the renderer appends streamed entries to it.
 */
function syncSpacers(body: HTMLElement, t: Turn, range: EntryRange): void {
  // `seq` space: a rendered count reads short and withdraws a tail spacer the window still owes.
  const span = turnSpan(t);
  placeSpacer(body, "head", range.from > 0 ? spacerPx(t, range, "head") : 0);
  placeSpacer(body, "tail", range.to < span ? spacerPx(t, range, "tail") : 0);
}

/** Seat, re-price or remove one spacer, anchored on the body so the pair cannot share a side. */
function placeSpacer(body: HTMLElement, side: SpacerSide, px: number): void {
  const card = body.parentElement;
  if (card === null) {
    return;
  }
  const held = card.querySelector<HTMLElement>(`:scope > .turn-space[data-space="${side}"]`);
  if (px <= 0) {
    held?.remove();
    return;
  }
  const space = held ?? el("div", { className: "turn-space" });
  if (held === null) {
    space.dataset["space"] = side;
    if (side === "head") {
      body.before(space);
    } else {
      body.after(space);
    }
  }
  space.style.blockSize = `${String(px)}px`;
}

/**
 * One spacer's worth: 0 when it stands for no box (`placeSpacer` removes at 0), else floored at 1px. Priced at
 * collection on purpose: pricing a batch's drops from that batch's measurements measured 19x more drift. The estimate
 * reads high, the safe direction.
 */
function spacerPx(t: Turn, range: EntryRange, side: SpacerSide): number {
  // The root lane stated, not defaulted: the price and the residency budget must answer `entryRenders` alike.
  const px = spacerHeight(t, range, side, "");
  return px > 0 ? Math.max(1, px) : 0;
}

/**
 * Drop everything `turnID`'s mounted body owns. From the reconcile's onRemove and the eviction. Per-entry release
 * and height recording happen at the window drop, so this reads no layout.
 */
function disposeTurnBody(turnID: string): void {
  // Flush any live markdown stream, then drop the render state.
  finalizeAssistantBody(turnID);
  disposeAssistantBody(turnID);
  // Nothing else clears a mounted turn's signals before page teardown.
  clearTurnSigs(turnID);
}

// ---------------------------------------------------------------------------
// Per-role builders + updaters
// ---------------------------------------------------------------------------

/**
 * One collected transition and the reader edge it lands at, measured while collecting: reading between mutations
 * later forces a layout per change.
 */
interface FoldChange {
  readonly side: "head" | "tail";
  readonly fn: () => void;
}

/**
 * Apply the plan to every card. Deferred while reading and compensated, since content vanishing above the reader is
 * the failure. `immediate` (the window pass) skips only the deferral. Head changes run before tail ones under one
 * compensation; the tail runs bare.
 */
function applyFoldPass(
  chatID: string,
  turns: readonly Turn[],
  cards: readonly HTMLElement[],
  immediate: boolean,
): void {
  const hits = new Map<string, number>();
  const byID = new Map<string, Turn>();
  for (const t of turns) {
    // `countsByTurn` is keyed by the server's session-absolute turn number, which `t.n` now is.
    hits.set(t.id, searchHitCount(t.n));
    byID.set(t.id, t);
  }
  const changes: FoldChange[] = [];
  const top = getScrollEl().scrollTop;
  const sideOf = (el: HTMLElement): "head" | "tail" => (el.offsetTop < top ? "head" : "tail");
  // A pass that refused a turn records nothing: recording an unapplied plan drops the delta until the next gesture.
  let refused = false;
  const record = (): void => {
    if (!refused) {
      appliedPlan = planSignature();
    }
  };
  for (const card of cards) {
    const id = card.getAttribute(KEY_ATTR);
    if (id === null) {
      continue;
    }
    setHitCount(card, hits.get(id) ?? 0);
    const plan = foldPlan.get(id);
    const open = plan?.open ?? true;
    const wantMounted = plan?.mounted ?? true;
    // The affordance tracks the plan as turns arrive and stop running.
    card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
    const t = byID.get(id);
    const folded = card.hasAttribute("data-folded");
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    const side = sideOf(card);
    if (wantMounted && body === null && t !== undefined) {
      // No whole-turn fallback: `wantMounted` is `wantedWindow.has(id)`.
      const range = wantedWindow.get(id);
      if (range !== undefined) {
        if (folded) {
          // Hidden build: the card is folded, so the body lands at zero height.
          startTurnBody(card, t, range);
        } else {
          // A card mid-deferral is visible, so its build joins the compensated batch.
          changes.push({
            side,
            fn: () => {
              if (card.isConnected) {
                startTurnBody(card, t, range);
              }
            },
          });
        }
      }
    } else if (!wantMounted && body !== null) {
      changes.push({
        side,
        fn: () => {
          // A deferred transition is a request, re-checked when it runs: a newer plan may want this body.
          if (!card.isConnected || wantedWindow.has(id)) {
            return;
          }
          unmountTurnBody(card);
        },
      });
    } else if (body !== null && t !== undefined) {
      if (!collectWindowMove(chatID, changes, card, body, t, side, sideOf)) {
        refused = true;
      }
    }
    if (open === !folded) {
      if (!open && t !== undefined) {
        // Keep the face current; a keyed no-op when nothing changed.
        syncTurnFace(card, t);
      }
      continue;
    }
    changes.push({
      side,
      fn: () => {
        setCardFolded(card, !open);
        if (t !== undefined) {
          syncTurnFace(card, t);
        }
      },
    });
  }
  if (changes.length === 0) {
    record();
    // Born-folded cards queue nothing, so the pagination trigger still needs restoring.
    if (paintMountedCards) {
      fillViewport();
    }
    return;
  }
  const apply = (): void => {
    const head = changes.filter((c) => c.side === "head");
    preserveReadingPosition(() => {
      for (const c of head) {
        c.fn();
      }
    }, "content-growth");
    for (const c of changes) {
      if (c.side === "tail") {
        c.fn();
      }
    }
    // After the batch: a change the plan moved under has skipped itself.
    record();
    // A build in that batch got its first slice only, and the batch can run long after the paint.
    drainColdBuilds();
    // Folding can shrink the page below the viewport, leaving no scroll event to paginate.
    fillViewport();
  };
  if (immediate) {
    apply();
    return;
  }
  deferWhileReading(apply);
}

/** What moving `t`'s window costs an existing body. False when the turn was refused, so the plan is not recorded. */
function collectWindowMove(
  chatID: string,
  changes: FoldChange[],
  card: HTMLElement,
  body: HTMLElement,
  t: Turn,
  side: "head" | "tail",
  sideOf: (el: HTMLElement) => "head" | "tail",
): boolean {
  if (hasPendingBuild(t.id)) {
    // A still-owed cold build would mount whole on this frame; it converges on the moved range itself.
    return false;
  }
  const plan = wantedWindow.get(t.id);
  if (plan === undefined) {
    return true; // no plan entry, so nothing of this turn is in the signature either
  }
  const want = bodyRange(t, plan);
  const have = mountedWindow(t.id);
  if (have === undefined) {
    return true; // nothing mounted for this turn, so the cold build owns its whole range
  }
  // Re-checked when it runs: a newer plan's pass has already applied itself.
  const stale = (): boolean => {
    const now = wantedWindow.get(t.id);
    return now?.from !== plan.from || now.to !== plan.to;
  };
  const live = turnIsLive(t);
  // A folded body is `block-size: 0`, so its `offsetTop` reads 0 and `sideOf` would always answer "head"; use the card.
  const bodySide = (): "head" | "tail" => (geometrySkipped(body) ? side : sideOf(body));
  if (want.from < have.from) {
    changes.push({
      side: bodySide(),
      fn: () => {
        if (!stale()) {
          mountHeadRange(t, want, live);
          syncSpacers(body, t, want);
        }
      },
    });
  } else if (want.from > have.from) {
    changes.push({
      side: bodySide(),
      fn: () => {
        if (!stale()) {
          dropHead(t, want);
          syncSpacers(body, t, want);
        }
      },
    });
  }
  // Two calls: one compensated call would include the below-the-reader removal.
  if (want.to < have.to) {
    changes.push({
      side: "tail",
      fn: () => {
        if (!stale()) {
          dropTail(t, want);
          syncSpacers(body, t, want);
        }
      },
    });
  } else if (want.to > have.to) {
    // The tail extension lands below the reader and runs bare. The pass's own chat, never `getActiveId()`, since the
    // closure runs at a later frame.
    changes.push({
      side: "tail",
      fn: () => {
        if (card.isConnected && !hasPendingBuild(t.id) && !stale()) {
          updateAssistantBody(body, t, chatID, live, want);
          syncSpacers(body, t, want);
        }
      },
    });
  }
  return true;
}

/**
 * Whether `card`'s body mounts every ordinal of `t`, so "copy as text" is complete. Asked of the render, never
 * `wantedWindow`, which leads the body.
 */
function bodyHoldsWholeTurn(card: HTMLElement, t: Turn): boolean {
  if (card.querySelector(":scope > .turn-body") === null) {
    return false;
  }
  const have = mountedWindow(t.id);
  return have !== undefined && covers(have, bodyRange(t, WHOLE_TURN));
}
initTurnActionsBodyProbe(bodyHoldsWholeTurn);

/**
 * Wire the header's fold toggle. The click records the reader's choice, which outranks the two-newest rule and
 * persists per chat.
 */
function mountFoldToggle(header: HTMLElement, card: HTMLElement, t: Turn): void {
  const btn = header.querySelector<HTMLButtonElement>(
    ":scope > .turn-head-row > .turn-fold-toggle",
  );
  if (btn === null || btn.dataset["bound"] === "") {
    return;
  }
  btn.dataset["bound"] = "";
  btn.addEventListener("click", () => {
    // A no-fold turn's header is not a control; `isTurnOpen` ignores overrides for the newest and running turns, so
    // recording one would spring a surprise fold later.
    if (card.hasAttribute("data-no-fold")) {
      return;
    }
    const open = card.hasAttribute("data-folded");
    const chatID = getActiveId();
    if (chatID !== "") {
      setTurnOpen(chatID, t.id, open);
    }
    const fresh = turnByID.get(t.id) ?? t;
    if (open && chatID !== "" && card.querySelector(":scope > .turn-body") === null) {
      // Opening a stub: build it hidden, then unfold through the compensated write, all within this interaction.
      mountTurnBody(chatID, t.id)
        .then(() => {
          preserveReadingPosition(() => {
            setCardFolded(card, false);
            syncTurnFace(card, fresh);
          }, "content-growth");
          bumpMessages(chatID, "shape");
        })
        .catch((e: unknown) => {
          console.warn("[messages] stub body build failed", e);
        });
      return;
    }
    // Immediate and compensated: the reader's own action is not deferred but must not move what they read.
    preserveReadingPosition(() => {
      setCardFolded(card, !open);
      syncTurnFace(card, fresh);
    }, "content-growth");
  });
  // The whole header band folds, like the tool and delegate cards; `wireRowToggle` keeps nested clicks and selection.
  wireRowToggle(header, btn);
}

/** Show a turn's search-hit count, so the folded list says which turns are worth opening. */
function setHitCount(card: HTMLElement, n: number): void {
  const badge = card.querySelector<HTMLElement>(
    ":scope > .turn-header > .turn-head-row > .turn-hit-count",
  );
  if (badge === null) {
    return;
  }
  badge.textContent = n > 0 ? String(n) : "";
}

function setCardFolded(card: HTMLElement, folded: boolean): void {
  if (folded) {
    card.setAttribute("data-folded", "");
  } else {
    card.removeAttribute("data-folded");
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  // On the toggle, not the band: a roleless div takes no `aria-expanded` (axe `aria-allowed-attr`).
  header
    ?.querySelector<HTMLButtonElement>(":scope > .turn-head-row > .turn-fold-toggle")
    ?.setAttribute("aria-expanded", folded ? "false" : "true");
}

// A collapsed turn keeps the open layout: header, a face with the final answer prose where the body was, and the
// ledger. The face carries no run card; the composer's run bar is a live run's persistent surface.

/** Per-card content key that detects when the face must be rebuilt; the prose-only face needs no dispose. */
const turnFaces = new WeakMap<HTMLElement, string>();

function faceKey(t: Turn): string {
  // An outcome flip is when the final prose can be superseded by text of the same length.
  return `${t.outcome}|${String(turnFaceProse(t).length)}`;
}

function disposeTurnFace(card: HTMLElement): void {
  if (!turnFaces.has(card)) {
    return;
  }
  turnFaces.delete(card);
  card.querySelector(":scope > .turn-face")?.remove();
}

/** Build or refresh the face to match the fold state; idempotent per content key, so cheap every pass. */
function syncTurnFace(card: HTMLElement, t: Turn): void {
  if (!card.hasAttribute("data-folded")) {
    disposeTurnFace(card);
    return;
  }
  const key = faceKey(t);
  if (turnFaces.get(card) === key) {
    return;
  }
  disposeTurnFace(card);
  const face = el("div", { className: "turn-face" });
  const prose = turnFaceProse(t);
  if (prose !== "") {
    const bubble = buildAssistantBubble(prose, false);
    bubble.root.classList.add("turn-face-prose");
    face.appendChild(bubble.root);
  }
  // No failure text here: `syncTurnNotice` carries it at card level.
  if (face.childElementCount === 0) {
    // Nothing to show: the ledger row alone carries the fold.
    turnFaces.set(card, key);
    return;
  }
  // A card-level child in the body's slot, so the footer's grid is untouched. Anchored on the notice when one exists,
  // so a folded card's reason sits below its answer whatever the call order.
  const anchor =
    card.querySelector<HTMLElement>(":scope > .turn-notice") ??
    card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (anchor !== null) {
    anchor.before(face);
  } else {
    card.appendChild(face);
  }
  turnFaces.set(card, key);
}

/**
 * Mount or refresh the failure notice: one card-level row saying why the turn ended badly, tinted by severity.
 * Card-level so it survives a stub with no body; `syncTurnFace`'s anchor keeps it last before the ledger.
 */
function syncTurnNotice(card: HTMLElement, t: Turn): void {
  syncNoticeText(card, t);
  syncCompactAction(card, t);
}

/**
 * Whether the notice offers to compact: the turn overflowed the window, is still the newest, and no compaction
 * has landed since.
 */
function offersCompact(t: Turn): boolean {
  if (closeOfBody(t.body)?.failure_kind !== "context_limit") {
    return false;
  }
  const s = get(getActiveId());
  if (s?.turn_order[s.turn_order.length - 1] !== t.id) {
    return false;
  }
  const closeAt = t.body.findIndex((e) => e.kind === "turn_close");
  return !t.body.some((e, i) => i > closeAt && e.kind === "compaction");
}

/** The notice's one action, as a sibling, so the notice's text node stays what find-in-chat searches. */
function syncCompactAction(card: HTMLElement, t: Turn): void {
  const existing = card.querySelector<HTMLElement>(":scope > .turn-notice-actions");
  const notice = card.querySelector<HTMLElement>(":scope > .turn-notice");
  if (notice === null || !offersCompact(t)) {
    existing?.remove();
    return;
  }
  if (existing !== null) {
    return;
  }
  const chatID = getActiveId();
  const btn = el(
    "button",
    { type: "button", className: "btn-small", [CHROME_ATTR]: "" },
    "Compact the context",
  );
  btn.addEventListener("click", () => {
    void compactChat.dispatch({ chatID });
  });
  notice.after(el("div", { className: "turn-notice-actions" }, btn));
}

function syncNoticeText(card: HTMLElement, t: Turn): void {
  const text = turnFailureText(t);
  const existing = card.querySelector<HTMLElement>(":scope > .turn-notice");
  if (text === "") {
    existing?.remove();
    return;
  }
  const severity = severityOf(t.outcome);
  if (existing !== null) {
    if (existing.textContent !== text) {
      existing.textContent = text;
    }
    existing.dataset["severity"] = severity;
    existing.dataset["outcome"] = t.outcome;
    return;
  }
  const notice = el("div", { className: "turn-notice" }, text);
  notice.dataset["severity"] = severity;
  // The outcome beside the severity for the stated hue exception: `unknown` keeps a neutral ink, and severity alone
  // cannot tell it from `cancelled`.
  notice.dataset["outcome"] = t.outcome;
  // `status`, not `alert`: the turn has ended, and a repaint would re-announce an alert every pass.
  notice.setAttribute("role", "status");
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer !== null) {
    footer.before(notice);
  } else {
    card.appendChild(notice);
  }
}

/**
 * Mount or refresh the licensed-code footnote and refusal callout. The precedence is resolved here, where both
 * halves meet: `turn_close` is durable and the live value answers until it exists. Card-level, for stubs.
 */
function syncTurnCloseNotes(card: HTMLElement, t: Turn, chatID: string): void {
  const close = closeOfBody(t.body);
  syncRefusal(card, close?.refusal ?? liveRefusalFor(chatID, t.id), t.trigger);
  syncCodeReferences(card, close?.code_references ?? codeReferencesFor(chatID, t.id));
  placeAboveFooter(card, ".refusal-callout");
  placeAboveFooter(card, ".code-refs");
}

/**
 * Keep an appended region above the ledger, moving only one below the footer: re-seating an attached node restarts
 * its animations and drops focus and `:hover` inside it.
 */
function placeAboveFooter(card: HTMLElement, selector: string): void {
  const region = card.querySelector<HTMLElement>(`:scope > ${selector}`);
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (region === null || footer === null) {
    return;
  }
  const kids = [...card.children];
  if (kids.indexOf(region) > kids.indexOf(footer)) {
    footer.before(region);
  }
}

// --- The turn card ---

/**
 * Build one turn: tinted header (trigger), plain body (work), tinted footer (ledger), one card type for all. A
 * non-resident turn mounts as a stub (no `.turn-body`) folded at birth; a resident body uses the batched builder.
 */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  // The session-absolute anchor. Links land through the rail's jump: parked views keep their cards, so this id
  // exists once per resident view.
  card.id = turnAnchorID(t.n);

  const header = buildTurnHeader(headerData(t));
  mountFoldToggle(header, card, t);
  card.appendChild(header);
  card.toggleAttribute("data-running", t.outcome === "running");

  const plan = foldPlan.get(t.id);
  // Born with the fold affordance decided, so the toggle never flashes.
  card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
  // The window entry is `plan.mounted`, so no whole-turn fallback.
  const range = wantedWindow.get(t.id);
  if (range !== undefined) {
    const body = el("div", { className: "turn-body" });
    card.appendChild(body);
    startFirstSlice(body, t, bodyRange(t, range));
  } else {
    setCardFolded(card, true);
  }

  mountTurnFooter(card, t);
  // After the footer, which holds Rewind; the face's order is independent since it anchors on the notice.
  mountRewind(card, t);
  syncTurnNotice(card, t);
  syncTurnCloseNotes(card, t, getActiveId());
  syncTurnFace(card, t);
  syncTurnBodyless(card);
  paintMountedCards = true;

  // A turn the reader just sent overrides Reading. Both conditions: this mount also runs for replays, refetches and
  // prepends, and the pin publishes a gesture that revokes the rail's pick.
  if (t.trigger !== undefined && appendNewIds.has(t.id)) {
    scrollToBottom();
  }
  return card;
}

function updateTurn(card: HTMLElement, t: Turn): void {
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header !== null) {
    updateTurnHeader(header, headerData(t));
  }
  card.toggleAttribute("data-running", t.outcome === "running");
  // Hands off three bodies: a stub has none, a body still owed a build is the builder's, and a dropped range is the
  // fold pass's.
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  const plan = wantedWindow.get(t.id);
  if (body !== null && plan !== undefined && !hasPendingBuild(t.id)) {
    // Only the tail extension is reachable here: the renderer renders past `st.window.to` only; head extension is
    // `mountHeadRange`'s, under the fold pass's compensation.
    const range = bodyRange(t, plan);
    updateAssistantBody(body, t, getActiveId(), turnIsLive(t), range);
    syncSpacers(body, t, range);
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnNotice(card, t);
  syncTurnCloseNotes(card, t, getActiveId());
  syncTurnBodyless(card);
  syncTurnFace(card, t);
}

/** Start a stub's body in place from the current projection; `startFirstSlice` decides how much lands this frame. */
function startTurnBody(card: HTMLElement, t: Turn, range: EntryRange): void {
  if (card.querySelector(":scope > .turn-body") !== null) {
    return;
  }
  const body = existingOrNewBody(card);
  if (body === null) {
    return;
  }
  startFirstSlice(body, t, bodyRange(t, range));
  syncTurnBodyless(card);
}

/**
 * Drop a card's body DOM and every per-turn resource behind it, owed slices included, since `drainColdBuilds`
 * runs next and would rebuild it.
 */
function unmountTurnBody(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null) {
    return;
  }
  const owed = card.getAttribute(KEY_ATTR);
  if (owed !== null) {
    coldBuilds.delete(owed);
  }
  const turnID = card.getAttribute(KEY_ATTR);
  if (turnID !== null) {
    disposeTurnBody(turnID);
  }
  // The header's clamp stays with the card.
  releaseClampsIn(body);
  // The spacers are the body's siblings, so they leave with it.
  for (const space of card.querySelectorAll<HTMLElement>(":scope > .turn-space")) {
    space.remove();
  }
  body.remove();
  syncTurnBodyless(card);
}

// One entry point for every interaction needing a stub's body now, built folded; opening stays the caller's. A
// cold build yields between batches and re-reads the store and DOM per batch, so a reconcile mid-build ends it.

/** Entries per synchronous slice. A slice can overshoot by one prose run, bounded by `bodyRange`'s snap. */
const BUILD_BATCH_ENTRIES = 32;

/**
 * Entries one full pass may mount synchronously, refilled by `paint`: a work cap. Only a demand grant can make it
 * bind, and that body is then born empty for `drainColdBuilds`.
 */
const PAINT_SYNC_ENTRIES = RESIDENT_ENTRIES;
let paintSyncEntries = PAINT_SYNC_ENTRIES;

/** In-flight builds by turn id with their range, so a covered request joins instead of double-appending. */
const turnBodyBuilds = new Map<
  string,
  { readonly range: EntryRange; readonly done: Promise<void> }
>();

/**
 * Turns whose cold build owes the rest after its first slice, drained at the pass's end, so the frame that creates
 * the cards never finishes them.
 */
const coldBuilds = new Set<string>();

/** Whether `turnID`'s body is mid-build; the full pass keeps its hands off a partial body. */
function hasPendingBuild(turnID: string): boolean {
  return coldBuilds.has(turnID) || turnBodyBuilds.has(turnID);
}

/**
 * Take a cold build's first slice and queue the rest. Once the pass's allowance is spent, build over an empty
 * range so `drainColdBuilds` mounts it off the frame.
 */
function startFirstSlice(body: HTMLElement, t: Turn, range: EntryRange): void {
  const take = Math.min(BUILD_BATCH_ENTRIES, Math.max(0, paintSyncEntries));
  const to = Math.min(range.to, range.from + take);
  buildAssistantBody(body, t, getActiveId(), turnIsLive(t), { from: range.from, to });
  paintSyncEntries -= to - range.from;
  syncSpacers(body, t, { from: range.from, to });
  if (to < range.to) {
    coldBuilds.add(t.id);
  }
}

/**
 * Where the next slice ends, or `done`, derived from the mounted window: a full pass may finish the range while
 * this yields. Tail only; `mountHeadRange` owns the head.
 */
function nextBuildTo(turnID: string, want: EntryRange): number | "done" {
  const have = mountedWindow(turnID);
  const from = have === undefined ? want.from : have.to;
  if (have !== undefined && have.to >= want.to) {
    return "done";
  }
  return Math.min(want.to, from + BUILD_BATCH_ENTRIES);
}

/** `card`'s body, created empty after the header when absent; null only for a headerless card. */
function existingOrNewBody(card: HTMLElement): HTMLElement | null {
  const existing = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (existing !== null) {
    return existing;
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header === null) {
    return null;
  }
  const body = el("div", { className: "turn-body" });
  header.after(body);
  return body;
}

/**
 * Finish the bodies this pass could only start, one yielded build per turn; each slice re-reads the store and DOM.
 * The chat is read here, since another chat's build has no card to find.
 */
function drainColdBuilds(): void {
  if (coldBuilds.size === 0) {
    return;
  }
  const chatID = getActiveId();
  for (const id of [...coldBuilds]) {
    // The entry stays until the build settles, keeping `hasPendingBuild` true across the yield.
    void yieldToBrowser()
      .then(() => buildOrJoin(chatID, id, wantedWindow.get(id) ?? EMPTY_RANGE))
      .catch((e: unknown) => {
        console.warn("[messages] cold body build failed", e);
      })
      .finally(() => {
        coldBuilds.delete(id);
        // `buildOrJoin`'s own pass refused while this entry stood.
        windowPass(false);
      });
  }
}

function yieldToBrowser(): Promise<void> {
  const sched = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  if (sched?.yield !== undefined) {
    return sched.yield.call(sched);
  }
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

/**
 * Build the ordinals around `at` (default: the head) in yielded batches; resolves when they mount or the build
 * stops applying. Records the navigation pin.
 */
export function mountTurnBody(chatID: string, turnID: string, at?: number): Promise<void> {
  // Ordinal 0 is the head, so the pin always carries a number.
  demandPin = { chatID, turnID, at: at ?? 0, until: Date.now() + PIN_GOAL_MS };
  return startDemandBuild(chatID, turnID);
}

/**
 * Build `turnID`'s body for the search walker under the walk's demand set, not the single navigation pin, which
 * a loop over many hit turns would keep overwriting.
 */
export function mountTurnBodyForWalk(chatID: string, turnID: string): Promise<void> {
  if (demandWalk?.chatID !== chatID) {
    demandWalk = { chatID, turnIDs: new Set() };
  }
  demandWalk.turnIDs.add(turnID);
  return startDemandBuild(chatID, turnID);
}

/** Release the walk's grants: the reveal has ended. */
export function endWalkReveal(chatID: string): void {
  if (demandWalk?.chatID === chatID) {
    demandWalk = undefined;
  }
}

function startDemandBuild(chatID: string, turnID: string): Promise<void> {
  const t = turnByID.get(turnID);
  // Bounded: the builder re-projects per batch, so a whole-turn grant could mount a huge turn.
  const want = (t === undefined ? undefined : demandRange(chatID, t)) ?? {
    from: 0,
    to: 2 * OVERSCAN_ENTRIES,
  };
  // Written early: the builder slices against `wantedWindow`, so a build started here would miss the requested row.
  wantedWindow.set(turnID, want);
  return buildOrJoin(chatID, turnID, want);
}

/** `mountTurnBody` without the pin, joining on coverage, and chaining rather than cancelling another caller's build. */
function buildOrJoin(chatID: string, turnID: string, want: EntryRange): Promise<void> {
  const inflight = turnBodyBuilds.get(turnID);
  if (inflight !== undefined) {
    return covers(inflight.range, want)
      ? inflight.done
      : inflight.done.then(() => buildOrJoin(chatID, turnID, want));
  }
  const done = buildTurnBodyBatches(chatID, turnID).finally(() => {
    turnBodyBuilds.delete(turnID);
    // The only pass a head-ward grant gets: the build inserts nothing above its window and a rail jump scrolls first.
    windowPass(false);
  });
  turnBodyBuilds.set(turnID, { range: want, done });
  return done;
}

async function buildTurnBodyBatches(chatID: string, turnID: string): Promise<void> {
  for (;;) {
    const session = getActive();
    if (session?.id !== chatID) {
      return;
    }
    const root = paintRoot();
    let card: HTMLElement | null = null;
    for (const child of root.children) {
      if (child.getAttribute(KEY_ATTR) === turnID) {
        card = child as HTMLElement;
        break;
      }
    }
    if (card === null) {
      return;
    }
    // Re-projected per batch: a page load can reshape the window while this yields.
    const t = projectTurn(session, turnID);
    if (t === undefined) {
      return;
    }
    // The range was revoked while this yielded, so the rest is being dropped.
    const want = wantedWindow.get(turnID);
    if (want === undefined) {
      return;
    }
    const body = existingOrNewBody(card);
    if (body === null) {
      return;
    }
    // Recomputed per slice: the window can move while this yields.
    const range = bodyRange(t, want);
    const to = nextBuildTo(turnID, range);
    if (to === "done") {
      syncSpacers(body, t, range);
      syncTurnBodyless(card);
      return;
    }
    const slice = { from: range.from, to };
    const grow = (): void => {
      updateAssistantBody(body, t, chatID, turnIsLive(t), slice);
      syncSpacers(body, t, slice);
    };
    // Compensated only where the slice lands above the reader. A body the page skips moves nothing visible, and
    // measuring it forces a layout of the skipped subtree.
    if (!geometrySkipped(body) && body.offsetTop < getScrollEl().scrollTop) {
      preserveReadingPosition(grow, "content-growth");
    } else {
      grow();
    }
    const after = nextBuildTo(turnID, range);
    if (after === "done") {
      syncTurnBodyless(card);
      return;
    }
    // A slice that moved no edge mounted nothing; spinning would hold `hasPendingBuild` true.
    if (after === to) {
      return;
    }
    await yieldToBrowser();
  }
}

/**
 * `.is-bodyless` mirrors "the card ends with an empty body" for the header's CSS. Called after every build and
 * update pass, when the body's children and the footer have settled.
 */
function syncTurnBodyless(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  card.classList.toggle(
    "is-bodyless",
    body !== null && body.firstChild === null && body.nextElementSibling === null,
  );
}

function headerData(t: Turn): TurnHeaderData {
  const request = t.trigger?.text;
  return {
    n: t.n,
    outcome: t.outcome,
    ts: t.ts,
    // An empty prompt is not a request; fall through to the system-trigger rendering.
    request: request !== undefined && request.trim() !== "" ? request : undefined,
    // From the trigger entry: attachments never appear in the prompt text.
    attachments: t.trigger?.attachments ?? [],
  };
}

/**
 * Mount Rewind into the footer, once. KAS discards the given message and everything after, so the target is the
 * next turn's trigger (`t.rewindTo`): none on the last turn or when the next has no user message.
 */
function mountRewind(card: HTMLElement, t: Turn): void {
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  const target = t.rewindTo;
  if (footer === null) {
    return;
  }
  let btn = footer.querySelector<HTMLButtonElement>(":scope > .turn-rewind");
  if (target === undefined) {
    btn?.remove();
    return;
  }
  if (btn === null) {
    btn = el(
      "button",
      {
        className: "turn-rewind",
        type: "button",
        "aria-label": "Rewind to this point",
      },
      svgTemplate(ICON_REWIND)(),
      // Desktop only; the `aria-label` names the button once the word is dropped.
      el("span", { className: "turn-rewind-label" }, "Rewind"),
    ) as HTMLButtonElement;
    footer.appendChild(btn);
  }
  // Rebound per paint: the target moves as turns are added or removed.
  btn.onclick = (): void => {
    void handleRewindClick(target).catch((e: unknown) => {
      console.warn("[messages] rewind failed", e);
    });
  };
  // Disabled mid-turn: KAS refuses a revert on a session with a live abortController.
  const busy = getActive()?.thinking ?? false;
  btn.disabled = busy;
  // The consequence, in a channel separate from the name. `data-tooltip`, not `title`, reaches a disabled button.
  btn.setAttribute(
    "data-tooltip",
    busy
      ? "Cannot rewind while the agent is running. Cancel the turn first"
      : "Rewind to right after this turn, discarding everything that follows",
  );
}

/** Mount or refresh the outcome ledger as the card's last child, turn-scoped: it sums every assistant message. */
function mountTurnFooter(card: HTMLElement, t: Turn): void {
  const led = turnLedger(t);
  const data: TurnSummaryData = {
    credits: led.credits,
    elapsedMs: led.elapsedMs,
    changedFiles: led.changedFiles,
    models: led.models,
    outcome: t.outcome,
    toolMs: led.toolMs,
    kindCounts: led.kindCounts,
    delegateCount: led.delegateCount,
    delegateMs: led.delegateMs,
    startedAt: led.startedAt,
    endedAt: led.endedAt,
    stopReasonRaw: led.stopReasonRaw,
    truncated: led.truncated,
    asks: led.asks,
    requestIds: led.requestIds,
    throughput: led.throughput,
    recoveries: led.recoveries,
    steering: led.steering,
    engineErrorClass: led.engineErrorClass,
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  // One predicate, `earnsTurnFooter`; the markdown join is ordered last, so it runs only for a ledger-less turn.
  const keep = earnsTurnFooter(data, {
    rewindable: t.rewindTo !== undefined,
    settledProse: t.outcome !== "running" && turnMarkdown(t).trim() !== "",
  });
  if (!keep) {
    existing?.remove();
    return;
  }
  let footer = existing;
  if (footer === null) {
    footer = buildTurnFooter(data);
    card.appendChild(footer);
  } else {
    updateTurnFooter(footer, data);
  }
  mountTurnFooterActions(footer, card, t);
}

// --- Assistant ---

/**
 * Finalize a streamed turn: flush every markdown stream and settle every reasoning trace, folding nothing. Only a
 * successor folds a trace.
 */
function finalizeTurn(turnID: string): void {
  finalizeAssistantBody(turnID);
}

/**
 * Finalize every mounted body no longer written into: a turn with no `turn_close` keeps its caret. The population
 * is `liveRenderIDs`; the turn's own `turn_close` decides. Idempotent.
 */
function finalizeStreamingIfNeeded(turns: readonly Turn[]): void {
  const live = liveRenderIDs();
  if (live.length === 0) {
    return;
  }
  // This session's turns only: finalizing a parked chat's open tail would write into a frozen view.
  const own = new Map<string, Turn>();
  for (const t of turns) {
    own.set(t.id, t);
  }
  for (const id of live) {
    const t = own.get(id);
    if (t === undefined || turnIsLive(t)) {
      continue;
    }
    finalizeTurn(id);
    disposeStreamingEffect(id);
  }
}

// --- Helpers ---

/**
 * The row wrapper for a top-level assistant bubble. No avatar, since the card establishes identity; the row stays
 * because the dispatcher mounts into it.
 */
function makeRow(): HTMLDivElement {
  return el("div", { className: "msg-row" }) as HTMLDivElement;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
