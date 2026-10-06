// The transcript SHELL, and the multiplexer: `#messages` holds one `.transcript-view`
// per resident chat, the active one live and the parked ones frozen. One effect watches
// the active chat id plus that chat's messages version and reconciles into the active
// view by message id. Assistant BODIES are composed entirely by messages-blocks.ts.

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
  initTurnHeaderCallbacks,
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
import { confirm as confirmDialog } from "./confirm.js";
import { registerCleanup } from "./actions/index.js";
import {
  disposeAllToolEffects,
  disposeToolEffectsForChat,
  suspendToolEffectsFor,
  resumeToolEffectsFor,
  drainParkedTerminals,
  initToolCallbacks,
  initToolViewCallbacks,
} from "./messages-tools.js";
import {
  mountTurnFooterActions,
  resetTurnSourceView,
  turnMarkdown,
  initTurnActionCallbacks,
  initTurnActionsBodyProbe,
  syncSourceView,
  copyWithFeedback,
} from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";

// --- Public re-exports ---

export { getScrollEl, setLoadMore };
// This module owns the rail, so chat.ts reaches it through here rather than
// driving the same surface itself.
export { loadTurnRail, pointTurnRail };

// --- Module state ---

const messagesEl = $.messages;

// --- The transcript multiplexer ---
//
// Exactly one resident view carries `.is-active` and the scroller's observers. A parked
// view keeps its DOM and its renders with every writer that could reach that DOM
// paused: the store keeps ingesting, only rendering is frozen.

/** How many PARKED views stay resident (the active view is not counted).
 *  Past this, the least-recently-used parked view runs the real dispose. */
export const PARKED_VIEWS = 3;

/** One resident chat view. The saved-at-park fields are meaningful only while
 *  `parked` is true. There is no view→turn table: the card walk answers that
 *  (`mountedBodies`). */
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
  /** Turn ids still being written into when the view parked. Resume rebuilds these
   *  bodies fresh even if they have since closed: their binding effects were
   *  disposed, so they missed every update that landed while parked. */
  pausedStreaming: Set<string>;
}

/** Resident views by chat id. Iteration order is the LRU order: activation
 *  re-inserts, so the first parked entry is the eviction candidate. */
const views = new Map<string, ChatView>();
let activeView: ChatView | null = null;

/** The active view's element, for consumers mounting transcript furniture.
 *  Null when no chat view is active (boot, the last-tab window). */
export function activeTranscriptView(): HTMLElement | null {
  return activeView?.el ?? null;
}

/** A resident view's element for `chatID` — active or parked — or null. */
export function transcriptViewFor(chatID: string): HTMLElement | null {
  return views.get(chatID)?.el ?? null;
}

/** Reveal a run's card in `chatID`'s transcript. Returns whether it landed.
 *
 *  BEST-EFFORT: mountedness is the whole condition and this never unfolds, so a card
 *  outside the paginated window or inside a stub answers false and the caller keeps
 *  only the tab. SCOPED to that chat's view, because `.run-card[data-run]` repeats
 *  once per resident view and `document.querySelector` can answer with a parked one. */
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

/** Where paint reconciles: the active view, or the bare multiplexer before any
 *  view exists (the boot instant; nothing renders turns there). */
function paintRoot(): HTMLElement {
  return activeView?.el ?? messagesEl;
}

/** The turn bodies this view has mounted, as `(turn id, body element)`. The CARD WALK
 *  is the index: a card carries its turn id as its reconcile key and its body is one
 *  child query, so there is no view→turn table to keep in step with the DOM. */
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
  // Focus relocation first: an inert subtree drops focus to <body> on its own,
  // and the composer is the app's focus home.
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
    // The STORE decides, off the turn's own `turn_close` absence — the same fact
    // `turnIsLive` states, read here without a projection: an open turn is one whose
    // body will be written into again, and it is exactly the one a resume rebuilds.
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

/** Make `chatID`'s view the active one, creating it when it is not resident.
 *  Returns true when the view was UNPARKED (a catch-up paint must follow and
 *  the pass's end must resume the view's messages). */
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
    // LRU refresh: re-insertion moves this chat to the back of the order.
    views.delete(chatID);
  }
  views.set(chatID, view);
  view.parked = false;
  view.el.inert = false;
  view.el.classList.add("is-active");
  activeView = view;
  attachScroll({ el: view.el, scrollTop: view.scrollTop, readingState: view.readingState });
  if (created) {
    // AFTER the attach: `resetScrollState` ends in `setLoadMore(null, false)` and every
    // indicator lookup is scoped to the ATTACHED `viewEl`, so run before it, it stripped
    // the OUTGOING view's pagination button — a wrong-view mutation a parked view then
    // carried back with a control missing.
    //
    // Only for a view this call CREATED: an existing view's furniture is its own,
    // restored by the attach above, and resetting it takes away the pagination the
    // previous activation wired up.
    resetScrollState();
  }
  // After attach: entering Reading recomputes the baseline from the ACTIVE
  // session, and these are the parked chat's own numbers.
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

/** The REAL per-view dispose: `disposeTurnBody` per card the view holds, the chat's
 *  tool effects through their composite keys, and the container's removal. LRU eviction,
 *  chat close/delete and `teardownAll` all run this — never a bare empty reconcile,
 *  which would strip render state while leaving parked DOM behind. */
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
  // Every clamp in the view, in one sweep: the single per-view dispose close, delete,
  // eviction and `teardownAll` all run, so it covers a whole chat's headers without
  // counting them. `parkView` deliberately does NOT release — a parked view keeps its
  // clamps and re-measures on unpark.
  releaseClampsIn(view.el);
  // AFTER the row disposal, which records one last height per entry. Heights are
  // keyed `(turnID, seq)`, so the chat's TURN ids are the whole key space; the
  // measurement cache is the one per-turn store an unmount deliberately KEEPS, so a
  // view going away is where its numbers stop standing for anything on screen.
  forgetHeights(get(chatID)?.turn_order ?? []);
  disposeToolEffectsForChat(chatID, view.el);
  view.el.remove();
  views.delete(chatID);
  if (activeView === view) {
    activeView = null;
    lastActiveId = undefined;
  }
}

/** Pause one turn's body: dispose its live-binding effects, finish its reveals, and
 *  suspend the tool-card effects of the calls its render mounted, through the owning
 *  view's composite keys. The render, the DOM and the card stay. */
function pauseTurnBody(view: ChatView, turnID: string): void {
  disposeStreamingEffect(turnID);
  pauseAssistantBody(turnID);
  suspendToolEffectsFor(
    view.chatID,
    mountedToolCalls(turnID).map((tc) => tc.id),
    view.el,
  );
}

/** Resume one turn's body after the catch-up paint. A CLOSED turn needs only what
 *  pause suspended re-armed, the paint's own update having already trued grown text.
 *  One that was still open at park rebuilds instead: its binding effects were
 *  disposed, so an update that landed while parked is only guaranteed to appear
 *  through a fresh render of the current store. */
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

/** Rebuild a turn's body in place, keeping the card (its reconcile key and DOM
 *  position) and the window it holds. The old render state goes through the same
 *  disposal an unmount runs, minus the card itself. */
function rebuildTurnBody(session: Session, t: Turn, body: HTMLElement): void {
  const want = mountedWindow(t.id) ?? bodyRange(t, wantedWindow.get(t.id) ?? EMPTY_RANGE);
  disposeStreamingEffect(t.id);
  finalizeAssistantBody(t.id);
  disposeAssistantBody(t.id);
  clearTurnSigs(t.id);
  // Drop the turn's suspended tool entries so the fresh mount below cannot
  // clobber-leak them; the rebuild re-creates cards, effects and signals.
  disposeToolEffectsForChat(session.id, body);
  releaseClampsIn(body);
  body.replaceChildren();
  buildAssistantBody(body, t, session.id, turnIsLive(t), want);
}

/** Resume every paused body of the freshly unparked view, then drain the
 *  terminal output that buffered while it was parked — once.
 *
 *  ONE projection for the pass: a rebuild needs the `Turn` and this is the only place
 *  that knows the whole set, so projecting per body would re-walk the session's turns
 *  once per card. */
function resumeView(view: ChatView, session: Session): void {
  const byID = new Map(projectTurns(session).map((t) => [t.id, t]));
  for (const [turnID, body] of mountedBodies(view)) {
    resumeTurnBody(view, session, turnID, body, byID.get(turnID));
  }
  view.pausedStreaming.clear();
  drainParkedTerminals(session.id, view.el);
}

/** bindLoadingState unsubs, keyed by the TOOL id the binding belongs to (that is what
 *  `messages-tools.ts` passes). Released wholesale at page teardown; a turn's own
 *  disposal does not reach them, so a card's loading-state binding outlives its card. */
const bindUnbinds = new Map<string, (() => void)[]>();
function pushBind(key: string, unbind: () => void): void {
  let arr = bindUnbinds.get(key);
  if (arr === undefined) {
    arr = [];
    bindUnbinds.set(key, arr);
  }
  arr.push(unbind);
}

/** Per-TURN streaming effect cleanups, disposed on turn end as well as on unmount.
 *  Separate from `bindUnbinds` so a tool card's loading-state binding survives a turn
 *  end, which is not the end of that card. */
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

/** Per-ENTRY cleanups: turn id → entry `seq` → cleanups. Beside `streamingEffects`
 *  because the lifetime differs by the axis the window needs — an entry that leaves takes
 *  its own subscriptions and its siblings keep theirs. Turn end and card removal still
 *  release everything, through the sibling below. */
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

/** Turn ids newly appended at the end since the last paint. Two mounts read it for the
 *  same reason — the entry animation and a sent turn's live-edge pin — and both must stay
 *  silent for a replay or a prepend, which the reader did not cause. */
const appendNewIds = new Set<string>();
let lastNewestTurnID: string | undefined;
let lastActiveId: string | undefined;

/** The newest RESIDENT turn id: the tail the next paint's arrival walk starts from.
 *  `turn_order` is file order, so its last member is the newest whatever order the
 *  entries of two interleaved turns arrived in. */
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

// Initialize callbacks for extracted modules.
initToolCallbacks({
  pushBind,
  refreshGroupHeader,
  explainError,
});
initTurnActionCallbacks({ svgTemplate });
// The header's Copy reuses the assistant side's copy behaviour verbatim.
// Injected because turn-header.ts is a pure fundamental: it renders the band,
// it does not know about the actions framework.
initTurnHeaderCallbacks({ copy: copyWithFeedback });
initBlockRenderer({
  pushStreamingEffect,
  pushEntryEffect,
  disposeEntryEffects,
  makeRow,
  makeEvent: buildEvent,
});
// The refusal callout's Rewind CTA reuses this module's rewind flow (confirm → revert
// → refetch). Injected because refusal.ts cannot import messages.ts.
setRefusalRewindHandler((target) => {
  void handleRewindClick(target).catch((e: unknown) => {
    console.warn("refusal rewind failed", e);
  });
});

/** Mount the chat view. Idempotent. Called once at app boot from app.ts.
 *  Subscribes to store.version and reconciles the message list on every
 *  bump. Streaming markdown chunks flow through per-block signals bound
 *  at mount, not through this effect. */
export function mountChatView(): void {
  if (mounted) {
    return;
  }
  mounted = true;
  initFollowModel();
  // The rail lives in the transcript's positioned outer wrapper rather than in
  // the scroller, so it stays put instead of scrolling away with the content.
  mountTurnRail($.messagesWrapOuter);
  // The two navigation surfaces that can land on a stub call the same
  // on-demand build this module's own fold toggle uses. Injected — both
  // modules are imported BY this one, so a static import back would cycle.
  initTurnRailCallbacks({ mountTurnBody, activeView: activeTranscriptView });
  // The hit's ordinal is resolved HERE, in the projection the plan is grown over:
  // a second projection could price the same entry at a different ordinal, and the
  // grant is clamped against this one.
  initSearchRevealBuilder(
    (chatID, turnID, entryID) => {
      const t = turnByID.get(turnID);
      return mountTurnBody(chatID, turnID, t === undefined ? undefined : turnOrdinalOf(t, entryID));
    },
    mountTurnBodyForWalk,
    endWalkReveal,
  );
  // Scrolling does not repaint; it re-windows. TWO hooks: the gesture says the READER
  // moved, the viewport change is the settled frame to measure in. `onViewportChange`
  // fires for every scroll whoever wrote it, this module's own compensation included,
  // and the anchor comes off scroll position — so alone it lets the pass feed itself.
  onReaderGesture(noteReaderMoved);
  onViewportChange(windowPass);
  // The tool layer sits below this module, so the two facts only the multiplexer
  // knows arrive injected: whether a chat's view is parked, and that a RESIDENT
  // view's chat must not have its messages evicted out from under the DOM.
  // Registered here rather than in app.ts because the view registry lives in this
  // module and this module already imports store.js, so routing through app.ts
  // would add an indirection with no cycle to break.
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
  // Page unload is the one production moment every view goes away at once;
  // the close/delete/LRU paths dispose per view.
  registerCleanup(teardownAll);
  // The transcript's two inputs: WHICH chat is active, and THAT chat's own
  // transcript version. Header-only updates (usage ticks, titles, modes) write
  // the session signal but bump no version, so they never reach paint();
  // removing the active chat repaints via the activeId write in removeChat.
  effect(() => {
    const id = watchActiveId();
    touch(messagesVersionOf(id));
    paint();
  });
}

// ---------------------------------------------------------------------------
// The follow model's two client-side obligations (§3.4).
// ---------------------------------------------------------------------------

/** Entries the reader can REACH, which is what the resume chip counts. `entryRenders`
 *  is the whole test, so it is the renderer's own answer rather than a second reading
 *  of it: an entry in another LANE is dropped by `placeEntry`, and the four fold kinds
 *  render at no position of their own, so neither adds document height in any fold
 *  state and counting one would promise a distance that does not exist. `firstPlanSeq`
 *  is hoisted per turn for the reason `planResidency` hoists it. */
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

/** The last FULL pass's reachable-entry count. Chunk- and tool-cause paints cannot add
 *  a REACHABLE entry (an entry this transcript draws arrives as `shape`), so the walk
 *  runs once per full pass rather than once per streamed delta. */
let reachableEntries = 0;

function initFollowModel(): void {
  // Following pins to the ACTIVE TEXT BLOCK, not the document bottom: otherwise a
  // 400-line diff card rendering below the streamed sentence scrolls it off the top.
  // WHICH bubble is the anchor registry's call (messages-blocks.ts owns `.streaming`
  // and the delegate boxes); a registry read, not a selector walk, because the
  // follow path runs per frame.
  setAnchorProvider(getLiveAnchor);
  onReadingStateChange((next) => {
    if (next === "reading") {
      // A fresh count at the park, so the baseline and the cached count agree
      // on what is reachable right now. Over the last full pass's projection
      // rather than a fresh one: that is what is on screen, and a park is a
      // reading-state change rather than a store change.
      reachableEntries = entryCount(lastTurns);
      followBaseline = reachableEntries;
    }
    refreshResumeLabel();
  });
}

/** The resume control is the only element on screen that knows the reader is
 *  behind, so it is the only one that can say how far. */
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
  // Nothing new since they parked: say what the turn is doing instead of
  // claiming a count of zero.
  setResumeLabel(session?.thinking === true ? session.working_label || "Working" : "Latest");
}

/** What this pass wants per turn, on the two independent axes: the fold policy's
 *  open/closed (`fold-state.ts`) and residency's mountedness (`block-window.ts`).
 *  Computed BEFORE the reconcile so a new card is born in its final state; existing
 *  cards go through `applyFoldPass`, never through the reconcile. */
interface FoldPlan {
  open: boolean;
  mounted: boolean;
  /** Whether the header offers the fold at all. False for the newest turn
   *  (nothing after it to get back to), a running turn, and a turn whose fold
   *  would hide nothing — the card carries `data-no-fold` and the toggle
   *  disappears. True for every stub, whatever those rules say: there the
   *  toggle is the only way to reach a body that does not exist yet. */
  canFold: boolean;
}
const foldPlan = new Map<string, FoldPlan>();

/** turn id → the ordinals that turn's body may hold NOW: the union of the plan's WINDOW
 *  and the reader's own DEMAND. TWO WRITERS, the second writing only what the first
 *  re-derives. `has()` IS `mounted`, hence the per-pass CLEAR beside `foldPlan`. */
const wantedWindow = new Map<string, EntryRange>();

/** A range covering nothing, at a turn's first ordinal: what a turn that renders
 *  nothing gets, so `.is-bodyless` has a body to mark. Never a turn with rows to
 *  draw — see the range decision in `computeFoldPlan`. */
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
  // The dispatcher's one turn-scope input, installed with the rest of this pass's
  // projection: a render holds one turn and cannot see the run its neighbours own.
  setRunCardOwners(runCardOwners(turns));
  // The transcript's root lane, stated because `planResidency` takes no default.
  const window = planResidency(openable, anchor, "");
  for (const [i, t] of turns.entries()) {
    turnByID.set(t.id, t);
    const hides = turnFoldHides(t);
    const policyOpen = isTurnOpen(chatID, t, i, turns.length);
    // The reader's own REQUEST outranks the plan's silence until it expires: a
    // jump does not open the turn it lands on. Whichever range COVERS the request
    // wins; the hull is refused, since a hit and the reader at opposite ends of one
    // 700-block turn hull into all of it.
    const asked = demandRange(chatID, t);
    const grown = window.get(t.id);
    // A REPLACE retracts nothing here: `applyFoldPass`'s drop plus `syncSpacers`'s
    // update own that on the FOLLOWING pass, so until then the body is
    // OVER-height — never under, which is what keeps `scrollHeight` past the viewport.
    const merged =
      grown === undefined ? asked : asked === undefined || covers(grown, asked) ? grown : asked;
    // A turn that RENDERS NOTHING here is bodied whatever the budget says: `.is-bodyless`
    // needs a body element to mark. One with rows to draw is a STUB instead, because an
    // empty range emits the whole-turn tail spacer and no row — 23,988px of nothing,
    // measured on a 400-block turn.
    const range = merged ?? (rendersNothing(t) ? EMPTY_RANGE : undefined);
    const mounted = range !== undefined;
    if (range !== undefined) {
      wantedWindow.set(t.id, range);
    }
    // A hides-nothing turn stays OPEN while it is resident: its face would be
    // identical to its body, so an auto-fold buys nothing and its animation
    // reads as "something happened, nothing changed".
    const wantOpen = policyOpen || (!hides && mounted);
    foldPlan.set(t.id, {
      // A stub has no body, so it cannot render open however the disclosure
      // rules read. The override survives — revealing it gives it presence on the
      // next pass, and then this reads true.
      open: wantOpen && mounted,
      mounted,
      canFold: !mounted || (hides && i < turns.length - 1 && t.outcome !== "running"),
    });
  }
}

/** How long a `demandPin`'s grant lives when nothing clears it earlier. A smooth
 *  `jumpTo` flight is ~50 events over a few hundred milliseconds and `scroll.ts`
 *  gives its own pin pass 700ms to settle, so 2000 clears both with room. */
const PIN_GOAL_MS = 2000;

/** Where the reader last asked to be, and how long the grant holding it stands.
 *  `mountTurnBody` is the only writer, so no caller can forget to record it. ONE
 *  SLOT, chat included: a jump moves the reader to one turn, and the turn they
 *  jumped away from is no longer where they are. */
let demandPin: { chatID: string; turnID: string; at: number; until: number } | undefined;

/** The turns a search-wide reveal built for the DOM walker, held until the reveal
 *  ends. ONE SCOPE, NOT N DEADLINES: a reader's arrival cannot be awaited so a pin
 *  needs a clock, while the reveal's end is an event `chat-search.ts` fires. */
let demandWalk: { chatID: string; turnIDs: Set<string> } | undefined;

/** The ordinals somebody explicitly ASKED for inside `t`: the pin while its chat matches and
 *  `Date.now() <= until`, else the walk while its set holds `t.id`. One overscan each side of the
 *  position asked for — the floor the window guarantees around its own anchor, so arrival is a
 *  handover. The PIN outranks the WALK, which asked for no ordinal. */
function demandRange(chatID: string, t: Turn): EntryRange | undefined {
  // `turnSpan`, never a rendered-entry COUNT: a grant is `seq` space, and a count falls
  // short of the last ordinal by every entry that renders elsewhere.
  const span = turnSpan(t);
  const pin = demandPin;
  // A REVEALED turn is a standing request the budget may not take back: letting the
  // clock expire under one dropped the body the reader had just opened. The deadline
  // still governs a grant they can ARRIVE at (`clearArrivedPin` is its ordinary
  // release), and the single pin slot bounds the standing case to one turn.
  if (
    pin?.chatID === chatID &&
    pin.turnID === t.id &&
    (Date.now() <= pin.until || isTurnRevealed(chatID, t.id))
  ) {
    // Clamped into the span, for `turnOrdinalOf`'s reason: a recorded ordinal
    // outlives the block it named, and an `at` past the span grants `from > to`.
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

/** The latest projection per turn id, refreshed each fold-plan pass. The fold
 *  toggle reads it, because its bound closure holds the BUILD-time turn and a
 *  face built from that would show a stale body. */
const turnByID = new Map<string, Turn>();

/** The last FULL pass's projection, in order: what `windowPass` re-filters rather
 *  than re-projecting per scroll frame. A store change schedules a paint, so a turn
 *  that vanished is caught by the builder's own "card gone" guards. */
let lastTurns: readonly Turn[] = [];

// The anchor ladder. COORDINATES: `el.offsetTop` is comparable with `scrollTop` only
// because the rows this reads ARE the `.msg-row`s (`entryEls` files `view.row`) and a
// `.msg-row` carries `content-visibility: auto`, so it is a containing block for the
// bubbles inside it while its OWN offset parent is the absolutely positioned
// `#messages-wrap` — measuring a bubble, or adding `position: relative` to a card type,
// breaks this silently. PICK: the last entry at or above the viewport top, or the first.

/** Where the reader is, or `undefined` for the live edge. Membership is the STORE predicate,
 *  never the card's `data-folded`: that is the last APPLIED plan, so through a deferral a turn
 *  that left `openable` is still bodied on screen, and seeding there hands back an ordinal
 *  `planResidency` cannot place — the absent-turn fallback then unmounts it under the reader. */
function residencyAnchor(openable: readonly Turn[]): ResidencyAnchor | undefined {
  // PRECEDENCE 1, outranking every DOM read: Following MEANS pinned to the live
  // edge, which is what `undefined` says. Measuring instead reads a body that is
  // still filling, so the answer lands behind the edge and the window follows it.
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
    // A card the walk STEPPED FORWARD to starts at the reader, so its first
    // ordinal is the seed; only the card they are actually in gets descended.
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

/** The ordinal of `turnID` the viewport top sits at, inside its own card. A card
 *  rendered FOLDED answers its first ordinal, and that test is a DOM read on purpose:
 *  a folded body's rows are not laid out whatever the plan says.
 *
 *  ONE LEVEL, because an entry IS the ordinal: the renderer files one element per
 *  mounted `seq`, so a position inside a tool group or a delegate card has no ordinal
 *  to answer with. The range is the RENDER's own (`mountedWindow`) rather than this
 *  pass's plan, since the entries measured are the ones the last pass mounted. */
function cardOrdinal(turnID: string, card: HTMLElement, top: number): number {
  const range = mountedWindow(turnID);
  if (range === undefined || card.hasAttribute("data-folded")) {
    return 0;
  }
  // A prose RUN files every one of its seqs against the one row it mounted, so the
  // element's FIRST seq is the ordinal and the repeats are skipped.
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

/** Drop the pin once the reader has ARRIVED: the ladder's own answer within one
 *  overscan of the ordinal asked for. A pin on a turn outside `openable` has no
 *  ordinal the ladder can name, so that one expires at `until` instead. */
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

/** Whether the current full pass mounted at least one new card. A pass whose cards were
 *  born folded queues no changes, so `fillViewport` needs this door: without it a page
 *  of born-folded stubs leaves the viewport unfilled and no scroll event follows. */
let paintMountedCards = false;

function paint(): void {
  const session = getActive();
  if (session === undefined) {
    // No session for the active id. Only touch the views when there is
    // genuinely NO active chat (all closed, or the last-tab window). A
    // transient undefined during a chat switch or a not-yet-loaded session
    // must NOT wipe the DOM — that empty reconcile pass, immediately followed
    // by a re-populate, was the flashing bug.
    if (getActiveId() === "") {
      // HIDE without disposing: the last-tab window can reopen this chat, and
      // its view unparks then. Disposal belongs to the close/delete paths
      // (disposeChatView) — a view whose CHAT left the store runs it here.
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
  // What the flushed version was FOR — what this pass may skip. Read after the
  // effect's version read; untracked by design. A chat switch is always the
  // full pass: the flushed cause describes the previous chat's delta, not the
  // transcript this pass must now show whole.
  const flushed = isChatSwitch ? undefined : renderCauseOf(session.id);
  if (flushed?.cause === "chunk") {
    // Pure text growth of MOUNTED entries: their signal effects painted the
    // text, so nothing mounts and nothing folds. Tail bookkeeping only.
    refreshResumeLabel();
    lastNewestTurnID = newestTurnID(session);
    return;
  }
  if (flushed?.cause === "tool" && refreshToolTurn(session, flushed.turnID)) {
    // An existing call's update, and its card was mounted: the keyed update
    // refreshed that one turn. An absent render falls through to the full
    // pass instead — only the full pass mounts.
    refreshResumeLabel();
    lastNewestTurnID = newestTurnID(session);
    return;
  }
  // Mark genuinely-new appended TURNS (streaming arrival) so only those get the
  // entry animation. Chat-switches, paginated prepends and refetched windows are
  // silent (no animation).
  appendNewIds.clear();
  // A FETCHED window is a replay whatever its rows look like, and only the paint's CAUSE
  // can say so: a cold open paints on `setActive` before `loadMessages` resolves, so its
  // post-fetch paint is not a chat switch and recorded no tail — indistinguishable from
  // a first prompt by the array alone.
  const replayed = isChatSwitch || flushed?.cause === "load";
  if (!replayed) {
    // Where the arrivals start. An UNSET tail means the previous paint of this
    // same chat held no turns at all, so every turn here arrived since. With a
    // fetched window excluded above, that is the reader's first prompt in a fresh
    // chat — the one appended-tail paint with no tail behind it. A tail the order
    // no longer carries (a rewind truncated past it) names no arrivals.
    let from = 0;
    if (lastNewestTurnID !== undefined) {
      // Reverse scan: the tail is always near the end (set at the end of the
      // previous paint), so scanning backward is O(1) amortized.
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
  // The turns the window is grown OVER: a folded body is `block-size: 0` +
  // `content-visibility: hidden`, so its ordinals hold zero height and would spend
  // the budget on content nobody can see. A STORE predicate, never a card's
  // `data-folded`, which is the last applied plan and can be a deferral behind.
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
  // The placeholder never coexists with content, dropped HERE because this is the
  // line where content lands; the activation's continuation is a frame too late, and
  // a skeleton may only paint over an EMPTY container. Only with something to
  // replace it: an empty turn list is a chat still loading. Scoped to THIS view; the
  // load-more furniture is deliberately untouched, as it mounts BESIDE real turns.
  if (turns.length > 0) {
    const skel = document.getElementById(CHAT_SKELETON_ID);
    if (skel !== null && root.contains(skel)) {
      skel.remove();
    }
  }
  reconcile(root, turns, turnSpec);
  // ONE walk over the container's children builds the card list every full-pass
  // consumer shares — the rail's observer and the fold pass.
  const cards = turnCards(root);
  // Tell the rail which cards exist so it can track the turn in view. Re-run per
  // full pass because the set changes as pages load and turns arrive.
  setResidentTurns(cards);
  applyFoldPass(session.id, turns, cards, false);
  // After the fold pass: a card that unmounted here is not owed a build, and a
  // card the pass folded is one the remaining slices land under invisibly.
  drainColdBuilds();
  finalizeStreamingIfNeeded(turns);
  reachableEntries = entryCount(turns);
  refreshResumeLabel();
  lastNewestTurnID = newestTurnID(session);
  lastActiveId = session.id;
  if (unparked && activeView !== null) {
    // The catch-up pass above brought the DOM to the store's current state;
    // now the paused effects come back: settled messages re-arm, the paused
    // tail rebuilds, parked terminal output drains once.
    resumeView(activeView, session);
  }
}

/** The turn cards of `root`, in document order. Unkeyed furniture lives beside
 *  them, hence the filter. */
function turnCards(root: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (const child of root.children) {
    if (child.classList.contains("turn")) {
      out.push(child as HTMLElement);
    }
  }
  return out;
}

/** Not re-entrant: the head-side compensation writes `scrollTop`, which emits a
 *  scroll. Across FRAMES the plan-equality exit is what terminates — the next pass
 *  measures after the compensation, and an unchanged plan emits nothing. */
let inWindowPass = false;

/** The plan the DOM was last brought to. What the window pass compares against, and
 *  it cannot be the CURRENT plan: `startDemandBuild` writes its grant into
 *  `wantedWindow` before building, so a pass comparing the plan with itself refuses
 *  the one pass that would apply the grant. */
let appliedPlan = "";

/** Whether the READER has stated a position since the last scroll-driven pass. Only the
 *  reader may move the window: this module's own compensation goes through the
 *  controller's marked write and publishes no gesture, so the pass cannot schedule
 *  itself. */
let readerMoved = false;

function noteReaderMoved(): void {
  readerMoved = true;
}

/** Re-window on a settled scroll frame the READER caused. Not a paint: the projection
 *  is the last full pass's, and only residency can change. The `openable` filter is
 *  RECOMPUTED rather than carried, because a fold toggle between two frames changes it.
 *  `fromScroll` false is a build settling: its own reason to re-window, no gesture. */
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
  // A body still FILLING reports coordinates for a partial range — the same premise
  // the rows reconcile is guarded on. The builder's settle runs the pass this skips.
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

/** What the current plan asks of every projected turn, as one comparable string:
 *  the window pass's no-op exit. */
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

/** The `tool`-cause fast path: refresh the owning TURN's mounted body through the
 *  renderer's existing update, touching no sibling. False when the turn is gone or
 *  nothing is mounted for it — the caller runs the full pass then.
 *
 *  The projection is what the update needs (`Turn`, not `TurnState`), and it is one
 *  turn's worth: `projectTurn` slices the session's own turn rather than walking every
 *  turn of the window, which is what a tool update per frame must not do. */
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
 * The confirmation shown before a rewind.
 *
 * THE CONFIRM IS THE ONLY GUARD, so it states the LOSSES rather than describing an
 * operation: the addressed turn and everything after it leave the transcript, the
 * files roll back to KAS's snapshots, and there is no undo. It surfaces what is
 * being rewound FROM so that cost is legible first; field reads stay defensive.
 */
function rewindConfirmText(target: EntryPrompt, discarded: readonly Turn[]): string {
  const promptRaw = target.text.trim().replace(/\s+/g, " ");
  const prompt = promptRaw.length > 100 ? promptRaw.slice(0, 100) + "\u2026" : promptRaw;
  const lines = ["Rewind to this turn?", ""];
  if (prompt.length > 0) {
    lines.push(`Prompt: "${prompt}"`);
  }

  // Count across EVERY turn being dropped, not just the addressed one. The old text
  // described one turn's work because a fork left the rest alive somewhere else;
  // a revert discards all of it, so summarising only the first would understate
  // the cost by however many turns follow.
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
 * Confirm the rewind and dispatch it.
 *
 * REFUSED MID-TURN, not queued: KAS throws on a session with a live
 * abortController, so a button offered during a turn could only produce an error
 * the user cannot act on. `mountRewind` disables it; this is the second gate.
 */
async function handleRewindClick(target: EntryPrompt): Promise<void> {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  if (session.thinking) {
    return;
  }
  // Through the projection rather than the store's own order: an UNDRAWN turn is not
  // one the reader can be told they are discarding.
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

/** The multiplexer-wide teardown: the REAL per-view dispose applied to every resident
 *  view, then the shared surfaces (scroll, rail) and the module-global belts for state
 *  no view owns. Runs on page unload; the close, delete and eviction paths dispose per
 *  view instead. Exported for the op-set tests. */
export function teardownAll(): void {
  for (const chatID of [...views.keys()]) {
    disposeChatView(chatID);
  }
  // Belts for what no view reaches: a detached render's effects (the subagent
  // page shares these registries) and per-message state for rows a view walk
  // could not see. Each is idempotent over what the view disposes already ran.
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

// --- Reconcile specs ---
//
// Two levels, both keyed: TURNS by the turn id every one of their entries names, then
// each card's `.turn-body` over that turn's entries. The nesting is safe because
// reconcile only considers children carrying its key attribute, so a card's unkeyed
// header and footer are invisible to the inner pass.

const turnSpec: ReconcileSpec<Turn> = {
  key: (t) => t.id,
  mount: (t) => {
    const card = buildTurn(t);
    // Only animate a genuinely-new turn; chat-switch replay and pagination
    // prepends mount silently. The turn ids that arrived at the tail since the
    // last paint are what paint() records in appendNewIds.
    if (appendNewIds.has(t.id)) {
      card.setAttribute("data-chat-entry", "");
    }
    return card;
  },
  update: updateTurn,
  onRemove: (card, key) => {
    // No face teardown: the face holds only prose, so it has no effect to stop,
    // its element leaves with the card, and the WeakMap entry dies with it.
    //
    // Dispose the body: the renderer is never asked for this turn again, so nothing
    // else releases its state.
    disposeTurnBody(key);
    // The header's request-text clamp goes with the card, and so do the clamps inside
    // the entries: a rewind truncation drops a card while its view lives, so
    // `disposeChatView` never runs for it.
    releaseClampsIn(card);
  },
};

/** The caret's input, and the one owner of the spelling: a turn with no `turn_close` is
 *  still being written into, which is what `outcome: "running"` means (turns.ts owns why
 *  it is the one member no `turn_close` carries). */
function turnIsLive(t: Turn): boolean {
  return t.outcome === "running";
}

/** The residency plan's range SNAPPED to whole prose runs (a run is one
 *  bubble with one markdown stream, so an edge inside one would mount it as two rows with
 *  two parsers and a visible seam).
 *
 *  Every mount, price and completion test reads the wanted window through here, so the
 *  body, its spacers and the yielded builder cannot disagree about where it ends. */
function bodyRange(t: Turn, range: EntryRange): EntryRange {
  return sliceTurn(t, range);
}

/** Which end of a body's window a spacer stands at. */
type SpacerSide = "head" | "tail";

/** Price both of a body's spacers against the range it now holds, and create or remove
 *  each as the window reaches that end of the turn.
 *
 *  An ELEMENT rather than `padding-block` on `.turn-body`, which transitions and would
 *  animate every window move past `preserveReadingPosition`'s measurement — and a SIBLING
 *  of the body rather than a child, because the renderer owns that element's children and
 *  seats a streamed entry with `appendChild`, so a spacer inside would take every later
 *  entry below it. */
function syncSpacers(body: HTMLElement, t: Turn, range: EntryRange): void {
  // `seq` space, like `range.to` and like `spacerHeight`'s own bound: a rendered-entry
  // count reads short of it and withdraws a tail spacer the window still owes.
  const span = turnSpan(t);
  placeSpacer(body, "head", range.from > 0 ? spacerPx(t, range, "head") : 0);
  placeSpacer(body, "tail", range.to < span ? spacerPx(t, range, "tail") : 0);
}

/** Seat, re-price or remove one spacer. Anchored on the BODY rather than on the header,
 *  so the pair cannot end up on the same side of it whatever else the card holds. */
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

/** What one spacer is worth, 0 when it stands for no box and floored at 1px when it stands for
 *  one. `placeSpacer` removes at 0, so the price is the seat gate: a remainder whose entries all
 *  render elsewhere reserves no height and leaves `.is-bodyless` reachable. Read when a change is
 *  COLLECTED, deliberately: pricing the same batch's own drops from the measurements that batch
 *  takes moved the reader 19x further on the 700-block fixture (−47px of drift became −909px),
 *  because the compensation's error over the tail extension no longer cancels. The estimate reads
 *  HIGH, the safe direction, and the next window move re-prices it. */
function spacerPx(t: Turn, range: EntryRange, side: SpacerSide): number {
  // The transcript's root lane, stated rather than defaulted: `spacerHeight` carries no
  // default because the price and the residency budget have to answer `entryRenders`
  // identically, and a silent `""` in one of them is that divergence.
  const px = spacerHeight(t, range, side, "");
  return px > 0 ? Math.max(1, px) : 0;
}

/** Drop everything `turnID`'s mounted body owns: the render's own state, its
 *  turn-lifetime effects and its per-turn signals. Called from the turn reconcile's
 *  onRemove — a removed card's body is never rendered again, so nothing else would
 *  release it — and from the 2→3/1→3 eviction.
 *
 *  Per-ENTRY release is the renderer's own, at the window drop that removes the entry
 *  (`dropHead`/`dropTail`), which is also where a departing row's measured height is
 *  recorded. So this reads no layout and needs no measured-heights pass. */
function disposeTurnBody(turnID: string): void {
  // Flush any live markdown stream, then drop the render state.
  finalizeAssistantBody(turnID);
  disposeAssistantBody(turnID);
  // The turn's per-entry streaming signals go with it: nothing else clears a mounted
  // turn's signals before page teardown, so signals left here would outlive the card
  // for the rest of the page.
  clearTurnSigs(turnID);
}

// ---------------------------------------------------------------------------
// Per-role builders + updaters
// ---------------------------------------------------------------------------

/** One collected transition and which EDGE of the reader it lands at. The side is
 *  measured in the COLLECT loop: at application time "later" is whenever
 *  `deferWhileReading` releases, and reading between mutations forces one layout
 *  per change on a batch that can be one per mounted card. */
interface FoldChange {
  readonly side: "head" | "tail";
  readonly fn: () => void;
}

/** Apply the plan to every card: fold, unfold, and the ordinals each body holds. DEFERRED
 *  WHILE READING and COMPENSATED, both mandatory — content vanishing from above the reader is
 *  the failure this guards. `immediate` is the WINDOW pass, which skips the deferral only: a
 *  scrolling reader is Reading by definition. Every HEAD change runs before every TAIL one, so
 *  ONE compensation wraps them; the tail runs BARE, or it drags the reader's view. */
function applyFoldPass(
  chatID: string,
  turns: readonly Turn[],
  cards: readonly HTMLElement[],
  immediate: boolean,
): void {
  const hits = new Map<string, number>();
  const byID = new Map<string, Turn>();
  for (const t of turns) {
    // `countsByTurn` is keyed by `Hit.turn`, which the server computes over the
    // WHOLE message array — so this join is only honest now that `t.n` is
    // session-absolute. Window-local, it looked up an absolute key and a folded row's
    // match count read 0 (or another turn's) on any chat long enough to page.
    hits.set(t.id, searchHitCount(t.n));
    byID.set(t.id, t);
  }
  const changes: FoldChange[] = [];
  const top = getScrollEl().scrollTop;
  const sideOf = (el: HTMLElement): "head" | "tail" => (el.offsetTop < top ? "head" : "tail");
  // A pass that refused a turn brought the DOM to LESS than the plan, so it records
  // nothing: `appliedPlan` is what the window pass exits on, and recording a plan
  // this batch did not apply drops the delta until the reader's next gesture.
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
    // The affordance tracks the plan: the previously-newest turn gains its
    // toggle when the next turn arrives, and a turn that stops running gains
    // or loses it by what its fold would hide.
    card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
    const t = byID.get(id);
    const folded = card.hasAttribute("data-folded");
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    const side = sideOf(card);
    if (wantMounted && body === null && t !== undefined) {
      // No whole-turn fallback: `wantMounted` IS `wantedWindow.has(id)`, so an
      // absent range is the presence rule breaking, not a body to guess at.
      const range = wantedWindow.get(id);
      if (range !== undefined) {
        if (folded) {
          // Hidden build: the card is folded, so the body lands at zero height.
          startTurnBody(card, t, range);
        } else {
          // A card mid-deferral (its fold is still queued) is visible, so its
          // build moves content; it joins the compensated batch instead.
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
          // A deferred transition is a REQUEST, re-checked when it runs: the window
          // pass applies its own transitions from a newer plan while this closure
          // is still queued, and unmounting a body that plan wants would take its
          // `coldBuilds` entry with it.
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
        // Already folded: keep the face current (a run card or the persisted
        // outcome can arrive after the fold). Cheap — keyed no-op when nothing
        // changed.
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
    // Born-folded cards queue nothing, so the pagination chain below still
    // needs its trigger restored when this pass mounted cards.
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
    // Read AFTER the batch: a change the plan moved under has skipped itself.
    record();
    // A build in that batch got its first slice only, and this batch can run long
    // after the paint that queued it — so it drains its own.
    drainColdBuilds();
    // Folding can starve its own pagination trigger — once the resident turns
    // fold, the page can be shorter than the viewport, and then there is no
    // overflow, no scroll event and no fetch. Restore the trigger.
    fillViewport();
  };
  if (immediate) {
    apply();
    return;
  }
  deferWhileReading(apply);
}

/** What moving `t`'s window costs a body that already exists: the rows the range
 *  gains and loses, then each boundary row's own edges. False when the turn was
 *  REFUSED rather than collected, which is what stops the pass recording a plan it
 *  did not bring the DOM to. */
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
    // A cold build still owed would be mounted whole on this frame, which is the cost
    // the yielded builder exists to refuse. It converges on the moved range itself.
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
  // A deferred transition is a REQUEST, re-checked when it runs: a newer plan's own
  // pass has already applied itself, and this batch's range is that plan's range.
  const stale = (): boolean => {
    const now = wantedWindow.get(t.id);
    return now?.from !== plan.from || now.to !== plan.to;
  };
  const live = turnIsLive(t);
  // A folded body is `block-size: 0`, so anything inside one reports offsetTop 0 and
  // `sideOf` answers "head" whatever the card's real position. The card's own side is
  // measured on a `.turn` in the view this pass paints, which is the active one.
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
  // Two calls, never one: a relocation retracts both edges, and one compensated
  // call would correct by a delta that includes the below-the-reader removal.
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
    // The tail EXTENSION, which is the streaming path: it lands below the reader, so it
    // runs bare like the drop beside it. The PASS's own chat, threaded down from its
    // caller and never `getActiveId()`, because this closure runs at a later frame
    // boundary and an ambient read hands a chat switch inside the deferral the wrong one.
    changes.push({
      side: "tail",
      fn: () => {
        if (card.isConnected && !hasPendingBuild(t.id) && !stale()) {
          updateAssistantBody(body, t, chatID, live, want);
          syncSpacers(body, t, want);
          syncSourceView(body);
        }
      },
    });
  }
  return true;
}

/** Whether `card`'s body holds every ordinal `t`'s body has, MOUNTED: what makes
 *  "copy as text" a complete answer rather than a hole. Asked of the RENDER, never of
 *  `wantedWindow`, which leads the body by a build that has not landed AND by a window
 *  move `deferWhileReading` holds until the reader returns. */
function bodyHoldsWholeTurn(card: HTMLElement, t: Turn): boolean {
  if (card.querySelector(":scope > .turn-body") === null) {
    return false;
  }
  const have = mountedWindow(t.id);
  return have !== undefined && covers(have, bodyRange(t, WHOLE_TURN));
}
initTurnActionsBodyProbe(bodyHoldsWholeTurn);

/** Wire the header's fold toggle. The click RECORDS the reader's choice, which outranks
 *  the two-newest rule and persists per chat, so the next paint cannot undo it. */
function mountFoldToggle(header: HTMLElement, card: HTMLElement, t: Turn): void {
  const btn = header.querySelector<HTMLButtonElement>(
    ":scope > .turn-head-row > .turn-fold-toggle",
  );
  if (btn === null || btn.dataset["bound"] === "") {
    return;
  }
  btn.dataset["bound"] = "";
  btn.addEventListener("click", () => {
    // A no-fold turn's header is not a control: the newest turn is the one
    // being read, a running one is the one being watched, and a hides-nothing
    // turn has nothing to hide — isTurnOpen ignores overrides for the first
    // two anyway, so recording one here would only spring a surprise fold
    // later.
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
      // Opening a STUB: its body does not exist yet, so build it hidden, then unfold
      // through the same compensated write a resident toggle uses and declare the
      // shape change. All in this interaction, so nothing waits on a later paint.
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
    // Applied immediately and compensated: this is the reader's own action, so
    // it is not deferred, but it still must not move what they are looking at.
    preserveReadingPosition(() => {
      setCardFolded(card, !open);
      syncTurnFace(card, fresh);
    }, "content-growth");
  });
  // The band activates that button, so folding a turn is the WHOLE header rather than
  // a 16x16 target, matching the tool and delegate cards. `wireRowToggle` is what
  // keeps a nested control's own click, and a drag that selects the prompt.
  wireRowToggle(header, btn);
}

/** Show how many search hits a turn holds, so scanning the folded list tells the
 *  reader which turns are worth opening before they open any. */
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
  if (card.hasAttribute("data-folded") !== folded) {
    // The raw-source view belongs to the surface it was opened on (body or
    // face); crossing the fold renders the other surface fresh, so the toggle
    // resets rather than latching against a view that no longer shows raw.
    resetTurnSourceView(card);
  }
  if (folded) {
    card.setAttribute("data-folded", "");
  } else {
    card.removeAttribute("data-folded");
  }
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  // On the TOGGLE, never on the header: the band is a plain div, and a div with
  // no role takes no `aria-expanded` (axe `aria-allowed-attr`, critical). The
  // button is the disclosure control the keyboard reaches anyway — the band
  // only forwards its click.
  header
    ?.querySelector<HTMLButtonElement>(":scope > .turn-head-row > .turn-fold-toggle")
    ?.setAttribute("aria-expanded", folded ? "false" : "true");
}

// --- The collapsed turn's FACE ---
//
// A collapsed turn is input + output in the OPEN layout: the header carries the
// request as when open, a face slots in where the body was with the turn's final
// answer prose in full, and the ledger footer stays below it.
//
// The face carries NO run card: a live run's persistent surface is the composer
// band's run bar (`run-bar.ts`), which survives both the fold and a reload.

/** Face bookkeeping per CARD element: the content key, which detects a change the
 *  face has to be rebuilt for. No dispose half — with only prose in it the face
 *  holds no effect, and its element leaves with the card. */
const turnFaces = new WeakMap<HTMLElement, string>();

function faceKey(t: Turn): string {
  // `t.outcome` stays in the key: an outcome flip is exactly when the turn's final
  // prose block can be superseded by text of the same length.
  return `${t.outcome}|${String(turnFaceProse(t).length)}`;
}

function disposeTurnFace(card: HTMLElement): void {
  if (!turnFaces.has(card)) {
    return;
  }
  turnFaces.delete(card);
  card.querySelector(":scope > .turn-face")?.remove();
}

/** Build or refresh the face to match the card's fold state. Idempotent per
 *  content key, so the fold pass can call it every pass for cheap. */
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
  // No failure text here: `syncTurnNotice` carries it at card level, for both folds.
  if (face.childElementCount === 0) {
    // Nothing to show: the ledger row alone carries the fold, as before.
    turnFaces.set(card, key);
    return;
  }
  // A card-level child in the body's slot rather than inside the footer, so the
  // footer's own grid is untouched and a turn with no footer still gets its face.
  //
  // ANCHORED ON THE NOTICE WHEN THERE IS ONE: both regions sit between the body and
  // the ledger, and this has FIVE call sites against the notice's two, so an
  // insertion order would put a folded card's reason above its answer. The anchor
  // makes the order this function's property rather than the call order's.
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

/** Mount or refresh the turn's failure notice: one card-level row saying why a turn that
 *  did not end cleanly ended that way. Idempotent, tinted by SEVERITY so a cancel reads
 *  as `stopped` rather than a failure.
 *  A card-level child before the footer, NOT an entry in `.turn-body`: a tier-3 stub has
 *  no body element at all and the notice must survive residency. ONE mount point for both
 *  fold states, `syncTurnFace` early-returning for an unfolded card — and THE LAST REGION
 *  BEFORE THE LEDGER, which that function's anchor rather than this insertion point
 *  enforces: why the turn stopped reads after what it produced. */
function syncTurnNotice(card: HTMLElement, t: Turn): void {
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
  // The outcome beside the severity, for the ONE stated hue exception the four
  // other outcome surfaces in 29-turns.css already carry: `unknown` keeps a
  // neutral ink because an unreadable end has no honest hue, and severity alone
  // cannot express it (`unknown` and `cancelled` are one severity, two inks).
  notice.dataset["outcome"] = t.outcome;
  // `role="status"` rather than `alert`: the turn has already ended by the time
  // this mounts, so it is a result to be read in course, not an interruption. A
  // repaint would re-announce an `alert` on every pass.
  notice.setAttribute("role", "status");
  const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
  if (footer !== null) {
    footer.before(notice);
  } else {
    card.appendChild(notice);
  }
}

/** Mount / refresh the licensed-code footnote and the model-refusal callout.
 *  THE PRECEDENCE IS RESOLVED HERE, the one place holding both halves of
 *  each fact: `turn_close` is durable and the store's live value answers until that
 *  entry exists. Neither renderer reads the store, so they cannot disagree.
 *  Card-level for `syncTurnNotice`'s reason: a stub has no body to mount into. The
 *  Rewind target is this turn's OWN prompt, which `refusal.ts` states at its
 *  handler type. */
function syncTurnCloseNotes(card: HTMLElement, t: Turn, chatID: string): void {
  const close = closeOfBody(t.body);
  syncRefusal(card, close?.refusal ?? liveRefusalFor(chatID, t.id), t.trigger);
  syncCodeReferences(card, close?.code_references ?? codeReferencesFor(chatID, t.id));
  placeAboveFooter(card, ".refusal-callout");
  placeAboveFooter(card, ".code-refs");
}

/** Keep an appended card-level region above the ledger. Moves ONLY a region below the
 *  footer: re-seating an attached node restarts its animations and drops focus and
 *  `:hover` inside it, and `.code-refs` is a disclosure the reader opens. */
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

/** Build one turn: tinted header (the trigger), plain body (the work), tinted
 *  footer (the outcome ledger). One card type for every turn, so a one-word answer
 *  and a forty-tool-call refactor differ only in how much body they have.
 *
 *  Born in the residency this pass planned: a non-resident turn mounts as a STUB (no
 *  `.turn-body`, no inner reconcile, no per-block effects) and folds at birth. A
 *  resident body goes through the same batched builder the reveal uses. */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  // The anchor, session-absolute now that the paint pass supplies the window's base
  // (`turnAnchorID` in turns.ts owns why it is still not a working permalink). The
  // rail keeps joining on the reconcile key — parked views keep their cards, so this
  // id exists once per resident view.
  card.id = turnAnchorID(t.n);

  const header = buildTurnHeader(headerData(t));
  mountFoldToggle(header, card, t);
  card.appendChild(header);
  card.toggleAttribute("data-running", t.outcome === "running");

  const plan = foldPlan.get(t.id);
  // Born with its fold affordance decided, so the toggle never flashes on a
  // card that does not offer one. The fold pass keeps it current afterwards.
  card.toggleAttribute("data-no-fold", !(plan?.canFold ?? true));
  // The window entry IS `plan.mounted`, so it decides here rather than a
  // whole-turn fallback for a range the presence rule says exists.
  const range = wantedWindow.get(t.id);
  if (range !== undefined) {
    const body = el("div", { className: "turn-body" });
    card.appendChild(body);
    startFirstSlice(body, t, bodyRange(t, range));
  } else {
    setCardFolded(card, true);
  }

  mountTurnFooter(card, t);
  // After the footer: Rewind lives inside it, so it must exist first — and the
  // face goes into the slot above it, so a card born folded builds it here. The
  // two calls are order-INDEPENDENT: `syncTurnFace` anchors on the notice.
  mountRewind(card, t);
  syncTurnNotice(card, t);
  syncTurnCloseNotes(card, t, getActiveId());
  syncTurnFace(card, t);
  syncTurnBodyless(card);
  paintMountedCards = true;

  // A turn the reader just sent overrides Reading. BOTH conditions, because a user
  // trigger alone does not mean they asked for anything NOW: this mount also runs
  // for a chat-switch replay, a refetched window and a prepend, and the pin
  // publishes a reader gesture that would revoke the timeline rail's own pick.
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
  // Three bodies this pass keeps its hands off: a stub has none; one with a build
  // still owed is the builder's, because an update over a partial body would mount
  // every remaining entry on the frame the yielded builder exists to protect; and a
  // DROPPED range is the fold pass's, about to remove it.
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  const plan = wantedWindow.get(t.id);
  if (body !== null && plan !== undefined && !hasPendingBuild(t.id)) {
    // The TAIL extension is all this path can reach: the renderer renders past its own
    // `st.window.to` and nothing else, so a HEAD extension is `mountHeadRange`'s and
    // stays the fold pass's, which owns the compensation.
    const range = bodyRange(t, plan);
    updateAssistantBody(body, t, getActiveId(), turnIsLive(t), range);
    syncSpacers(body, t, range);
    syncSourceView(body);
  }
  mountTurnFooter(card, t);
  mountRewind(card, t);
  syncTurnNotice(card, t);
  syncTurnCloseNotes(card, t, getActiveId());
  syncTurnBodyless(card);
  syncTurnFace(card, t);
}

/** Start a stub's body in place, from the turn's current projection: the
 *  stub→resident transition (a search reveal, a failure flip, a live-run attach,
 *  a rewind shrinking the window).
 *
 *  `startFirstSlice` owns how much lands on the frame — the same policy `buildTurn`
 *  uses, this being the same cold build reached from the fold pass. */
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

/** The 2→3/1→3 transition: drop a card's body DOM and every per-turn resource behind
 *  it — the same disposal the card's own removal runs.
 *
 *  The turn's OWED SLICES go with it: `applyFoldPass` calls `drainColdBuilds` on the
 *  next line, so an entry left standing would send the builder straight back to
 *  rebuild the body this call just removed. The builder guards the same transition
 *  from its own side, for the eviction that lands mid-build. */
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
  // The clamps inside the entries go with the body; the header's stays with the card.
  releaseClampsIn(body);
  // The spacers are the body's siblings, so they leave with it rather than inside it:
  // a stub prices nothing.
  for (const space of card.querySelectorAll<HTMLElement>(":scope > .turn-space")) {
    space.remove();
  }
  body.remove();
  syncTurnBodyless(card);
}

// --- The on-demand body build ---
//
// ONE entry point for every interaction needing a stub's body NOW; built while the
// card is FOLDED, so nothing visible moves, and OPENING it stays the caller's
// business, which keeps the three callers' fold semantics apart. A cold build YIELDS
// between batches (`scheduler.yield()`, else a macrotask) so a 300-block turn cannot
// freeze the main thread on one click; each batch re-reads the store and the DOM, so
// a pass that reconciled the body mid-build ends the loop instead of double-mounting.

/** Entries per synchronous slice of a cold build. The renderer mounts a whole prose RUN
 *  as one bubble, so a slice can overshoot by the entries of the run its edge lands in;
 *  `bodyRange`'s snap is what makes that overshoot bounded and the same one the residency
 *  budget already tolerates. */
const BUILD_BATCH_ENTRIES = 32;

/** Entries one full pass may mount SYNCHRONOUSLY across every body it starts, refilled by
 *  `paint`. A WORK CAP; nothing here measures a frame. No path makes it bind for the
 *  WINDOW's own bodies, whose first slices cannot sum past one window; a DEMAND grant is
 *  outside the budget and can, and that body is then born EMPTY for `drainColdBuilds` —
 *  the behaviour this is kept for. */
const PAINT_SYNC_ENTRIES = RESIDENT_ENTRIES;
let paintSyncEntries = PAINT_SYNC_ENTRIES;

/** In-flight builds by turn id with the range each is building, so a second caller
 *  joins one that COVERS its request instead of double-appending rows. */
const turnBodyBuilds = new Map<
  string,
  { readonly range: EntryRange; readonly done: Promise<void> }
>();

/** Turns whose cold build got its first slice and owes the rest: added by the
 *  two cold builders, dispatched by `drainColdBuilds` at the end of the pass and
 *  removed when that build settles. So the frame that creates the cards is never
 *  the frame that finishes them. */
const coldBuilds = new Set<string>();

/** Whether `turnID`'s body is mid-build — owed a drain, or in flight in the
 *  yielded loop. The full pass reads it to keep its hands off a partial body
 *  (see `updateTurn`). */
function hasPendingBuild(turnID: string): boolean {
  return coldBuilds.has(turnID) || turnBodyBuilds.has(turnID);
}

/** Take a cold build's FIRST slice, and queue whatever is left.
 *
 *  The one place either cold builder decides what a paint pays on the frame. A pass that
 *  has spent the allowance builds the body over an EMPTY range: the render exists, so
 *  `mountedWindow` answers and `drainColdBuilds` mounts all of it off the frame. */
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

/** Where the next slice ENDS, or `done`. Derived from the MOUNTED window rather than
 *  carried in the build entry, because a full pass can render the whole range while this
 *  build yields — and then the build must see it as done instead of mounting it twice.
 *
 *  The TAIL shortfall only: the renderer's update path renders past its own window's `to`
 *  and nothing else, so a head reported here would spend a slice mounting nothing.
 *  `mountHeadRange` owns that edge. */
function nextBuildTo(turnID: string, want: EntryRange): number | "done" {
  const have = mountedWindow(turnID);
  const from = have === undefined ? want.from : have.to;
  if (have !== undefined && have.to >= want.to) {
    return "done";
  }
  return Math.min(want.to, from + BUILD_BATCH_ENTRIES);
}

/** `card`'s body element, created empty after the header when it has none. Null
 *  only for a card with no header, which is not a card this renderer builds. */
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

/** Finish the bodies this pass could only start. One build per turn, each behind a
 *  yield so the task that created the cards ends first; the builder re-reads the store
 *  and the DOM per slice, so a chat switch or full pass landing in between ends the
 *  build instead of double-mounting. The chat is read HERE rather than passed: a build
 *  for any other chat has no card to find. */
function drainColdBuilds(): void {
  if (coldBuilds.size === 0) {
    return;
  }
  const chatID = getActiveId();
  for (const id of [...coldBuilds]) {
    // The entry stays until the build SETTLES, which is what keeps
    // `hasPendingBuild` true across the yield — the window a full pass would
    // otherwise walk into and finish the body synchronously. A second drain over
    // the same id joins the in-flight build rather than starting one.
    void yieldToBrowser()
      .then(() => buildOrJoin(chatID, id, wantedWindow.get(id) ?? EMPTY_RANGE))
      .catch((e: unknown) => {
        console.warn("[messages] cold body build failed", e);
      })
      .finally(() => {
        coldBuilds.delete(id);
        // `buildOrJoin`'s own pass ran with this entry still standing, so it refused.
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

/** Build the ordinals around `at` in `turnID`'s body, in yielded block batches.
 *  Resolves when the range covering `at` is mounted, or the moment the build stops
 *  being applicable. `at` absent is the turn's HEAD, which a stub shows.
 *
 *  Records the navigation pin, every caller being a reader interaction; the drain
 *  uses `buildOrJoin` direct. */
export function mountTurnBody(chatID: string, turnID: string, at?: number): Promise<void> {
  // Ordinal 0 IS the turn's head, so the pin always carries a number and the
  // arrival test needs no second rule for a caller that named no ordinal.
  demandPin = { chatID, turnID, at: at ?? 0, until: Date.now() + PIN_GOAL_MS };
  return startDemandBuild(chatID, turnID);
}

/** Build `turnID`'s body for the search walker without claiming the NAVIGATION
 *  pin: it joins the walk's own demand set instead, which is scoped to the reveal
 *  rather than to a deadline. One pin slot cannot serve a loop over N hit turns —
 *  each would overwrite the last, so the loop would do N builds to keep one. */
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
  // BOUNDED even where the last pass has no projection for this turn: `buildTurnBodyBatches`
  // re-projects per batch, so a whole-turn grant written here can find the turn by its first
  // slice and mount all 700 blocks of it. The head range is the walk's own grant, and the
  // asker's range replaces it on the next pass.
  const want = (t === undefined ? undefined : demandRange(chatID, t)) ?? {
    from: 0,
    to: 2 * OVERSCAN_ENTRIES,
  };
  // Written EARLY, not owned: the builder slices against `wantedWindow` per slice,
  // so a build starting inside this call would resolve with the requested row
  // unmounted. The next pass recomputes it from the asker this caller recorded.
  wantedWindow.set(turnID, want);
  return buildOrJoin(chatID, turnID, want);
}

/** `mountTurnBody` without the pin, joining on COVERAGE rather than identity, or a
 *  second caller resolves with its own row absent. Chaining rather than cancelling,
 *  because that build's range may be the one another caller is waiting on. */
function buildOrJoin(chatID: string, turnID: string, want: EntryRange): Promise<void> {
  const inflight = turnBodyBuilds.get(turnID);
  if (inflight !== undefined) {
    return covers(inflight.range, want)
      ? inflight.done
      : inflight.done.then(() => buildOrJoin(chatID, turnID, want));
  }
  const done = buildTurnBodyBatches(chatID, turnID).finally(() => {
    turnBodyBuilds.delete(turnID);
    // The ONLY pass a HEAD-ward grant gets: the build inserts nothing above its own
    // window, and a rail jump scrolls BEFORE building, so no later event carries it.
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
    // Re-projected per batch rather than captured: a page load can reshape the
    // window while a build yields, and the projection is the only truth about
    // what this turn's body holds now.
    const t = projectTurn(session, turnID);
    if (t === undefined) {
      return;
    }
    // The terminal condition beside "no card" and "turn left the projection": the
    // RANGE was revoked while this build yielded, so every row left is one the pass
    // has decided to drop. Both residency sources reach this through `wantedWindow`.
    const want = wantedWindow.get(turnID);
    if (want === undefined) {
      return;
    }
    const body = existingOrNewBody(card);
    if (body === null) {
      return;
    }
    // Recomputed per slice, like the projection above it: the window can move while
    // this yields, and the build must converge on where it went.
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
    // COMPENSATED only where the slice lands ABOVE the reader, read once per slice:
    // under a window a slice BELOW them is the common case, and compensating that
    // drags their view. A body the page is not rendering moves no line they can see, and
    // measuring one makes the browser lay out the subtree it skipped, once per slice.
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
    // A slice that left the mounted edge where it found it mounted nothing, and another
    // over the same range would too. Spinning holds `hasPendingBuild` true.
    if (after === to) {
      return;
    }
    await yieldToBrowser();
  }
}

/** `.is-bodyless` mirrors "the card ends with an empty body" for CSS: the
 *  header's bottom-edge treatment keys on it (29-turns.css). Called after every
 *  build/update pass, where both facts it encodes settle — reconcile owns the
 *  body's children and mountTurnFooter owns whether a footer follows. */
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
    // An empty prompt is not a request; fall through to the system-trigger
    // rendering rather than showing a blank header band.
    request: request !== undefined && request.trim() !== "" ? request : undefined,
    // Read off the trigger ENTRY, where the appender stamped them. Not derived
    // from the request text: an image or a document attachment never appears in
    // the prompt's own text at all, so there is nothing there to parse back out.
    attachments: t.trigger?.attachments ?? [],
  };
}

/** Mount the Rewind action into the turn's FOOTER, once.
 *
 *  The footer rather than the header because that is where its meaning is legible:
 *  KAS discards the message it is given plus everything after, so the addressed
 *  message is the NEXT turn's trigger (`t.rewindTo`) and the button means "go back
 *  to the state after this turn". Hence none on the last turn, and none when the
 *  next turn has no user message to address. */
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
      // Desktop only: 29-turns.css drops the word at the width the footer's own
      // actions collapse, and the `aria-label` above is the name once it is gone.
      el("span", { className: "turn-rewind-label" }, "Rewind"),
    ) as HTMLButtonElement;
    footer.appendChild(btn);
  }
  // Rebound on every paint: the target moves when a turn is added or removed,
  // and a stale closure would address a message that is no longer next.
  btn.onclick = (): void => {
    void handleRewindClick(target).catch((e: unknown) => {
      console.warn("[messages] rewind failed", e);
    });
  };
  // DISABLED mid-turn, not queued. KAS refuses a revert on a session with a live
  // abortController, so an enabled button during a turn could only produce an
  // error the user cannot act on. Refreshed on every paint because `thinking` is
  // exactly what a paint is reacting to.
  const busy = getActive()?.thinking ?? false;
  btn.disabled = busy;
  // The CONSEQUENCE, in the channel `aria-label` is not: the name carries the
  // action. `data-tooltip` rather than `title` reaches this button even while it
  // is disabled, because a disabled control still fires hover events.
  btn.setAttribute(
    "data-tooltip",
    busy
      ? "Cannot rewind while the agent is running. Cancel the turn first"
      : "Rewind to right after this turn, discarding everything that follows",
  );
}

/** Mount / refresh the turn's outcome ledger as the card's last child.
 *
 *  Turn-scoped rather than message-scoped: a turn can hold more than one
 *  assistant message (a mid-turn model switch splits it), and the ledger
 *  describes the TURN, so it sums across them and renders once. */
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
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  // ONE predicate, in the footer's own module: the two extra reasons a turn card's
  // footer survives an unstamped ledger used to sit here as a second expression
  // beside `hasTurnSummary`, so the same question had two answers. The markdown join
  // is still ordered last, so it runs only for a ledger-less turn.
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

/** Finalize a streamed assistant turn: flush every markdown stream + SETTLE every
 *  reasoning trace (via the block dispatcher) — the label and the pulse, folding
 *  nothing. A fold is POSITIONAL: a successor being posted is what seals a trace, so
 *  the trace nothing followed stays expanded past turn end. The copy/export actions
 *  live in the turn footer and mount on the paint that follows turn end. */
function finalizeTurn(turnID: string): void {
  finalizeAssistantBody(turnID);
}

/** Finalize every mounted body that is no longer being written into: a turn with no
 *  `turn_close` keeps its caret, everything else flushes its markdown and SETTLES its
 *  reasoning traces, which flips the label and drops the pulse without collapsing
 *  anything — only a successor arriving after a trace folds it.
 *  The population is `liveRenderIDs` — the renders still carrying live text, an unsealed
 *  bubble or a caret the reveal's residue keeps past an earlier `end()`. No second live
 *  set unions in: the turn's own `turn_close` decides, so neither the last message nor
 *  the chat's `thinking` latch is read. Idempotent. */
function finalizeStreamingIfNeeded(turns: readonly Turn[]): void {
  const live = liveRenderIDs();
  if (live.length === 0) {
    return;
  }
  // THIS session's turns only: a parked chat's still-open tail carries a live render
  // too, and finalizing it from another chat's paint would write into the parked view
  // (the freeze) and seal a turn that is not over — its own unpark decides, off the
  // store's state then. A detached render's id is not in this map either.
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

/** The row wrapper for a top-level assistant bubble.
 *
 *  No avatar: the card already establishes identity. The row element stays because
 *  the block dispatcher mounts into it. */
function makeRow(): HTMLDivElement {
  return el("div", { className: "msg-row" }) as HTMLDivElement;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
