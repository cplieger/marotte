// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns the
// chat-tab registration (openChatTab) and load-more pagination.

import type { ResumableSession } from "./types.js";
import {
  getActiveId,
  getPaintedId,
  get,
  watchSession,
  setActive,
  setPainted,
  setModel,
  upsertHeader,
  activeSession,
  removeChat,
  tabStatusFor,
  isEmptyChat,
  messagesVersionOf,
  transcriptStale,
  markWindowStale,
} from "./store.js";
import {
  loadList,
  loadMessages,
  loadOlderTurns,
  confirmChatExists,
  type PageLoad,
} from "./store-load.js";
import { effect, el, touch } from "@cplieger/reactive";
import type { ChatHeader } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  claimNavigation,
  navigationHeld,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  openChatRefs,
  refreshActiveView,
  type OpenTabOutcome,
  type TabDotStatus,
} from "./tabs.js";
import { beginAdopt, adoptCommitted, opFailed } from "./tabs-sync.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { submitLabelled, submitPrompt, type SubmitResult } from "./submit.js";
import {
  mountChatView,
  setLoadMore,
  loadTurnRail,
  pointTurnRail,
  activeTranscriptView,
  transcriptViewFor,
  disposeChatView,
  parkPaintedView,
} from "./messages.js";
import { dropCover, holdCover, moveFocusOffTranscript } from "./transcript-cover.js";
import {
  addAttachment,
  addAttachmentTo,
  toAttached,
  unownedAttachmentPaths,
} from "./attachments.js";
import {
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  seedComposerState,
  flushComposerDraft,
  dropComposerState,
} from "./composer-state.js";
import { getLastModel } from "./session-context.js";
import { refreshContextUI } from "./context-ui.js";
import { $ } from "./dom.js";
import { info } from "./toast.js";
import { chatNotice, named, noticeSubject } from "./notice-subject.js";
import { cancelTurn, createChat, forkChat, setMode, type CreatedChat } from "./actions/chat.js";
import { ActionError } from "./actions/index.js";
import { SWITCH_LOAD_MS } from "./tab-view.js";
import { newOpID } from "./transport.js";

// --- Chat tab registration ---

/**
 * Open (or activate) a chat tab; `{ activate: false }` for a bulk open, since activation
 * fetches messages and conflicts per chat. A ROUND TRIP: `open_tab` creates the
 * server-owned tab and `tabs_changed` paints it.
 */
export async function openChatTab(
  id: string,
  name: string,
  opts?: { activate?: boolean; parentTabID?: string },
): Promise<OpenTabOutcome> {
  return openTab({
    kind: "chat",
    ref: id,
    name,
    // `parentTabID`, not a chat id: an unresolvable parent silently promotes the tangent to top
    // level. A side chat owns its own bridge.
    ...(opts?.parentTabID === undefined ? {} : { parent: opts.parentTabID }),
    ...(opts?.activate === undefined ? {} : { activate: opts.activate }),
  });
}

/**
 * A chat tab's dot state right now, a pending ask outranking the live state; shared with
 * tab-materialize.ts, which cannot read the dock.
 */
export function chatTabDot(id: string): TabDotStatus | "" {
  return tabStatusFor(get(id), hasPendingDecision(id));
}

/**
 * Everything closing a chat TAB does on this device, client-local only: deferred for this
 * device's own close (once per tab), immediate for a remote close. Teardown and the record
 * delete are the server's `close_tab`.
 */
export function closeChatTab(id: string): void {
  // The draft belongs to the CHAT, not the tab, so a pending save goes out before the
  // row drops.
  flushComposerDraft();
  // The dock queue is keyed by chat id, so a leftover queue resurrects on reopening the id.
  dropDecisions(id);
  // Before removeChat: the store reassigning the painted chat repaints synchronously,
  // and a dead view still in the registry would be parked by that paint.
  disposeChatView(id);
  // The store row, whatever retention says. Under retention the record survives
  // server-side; with retention off the server deleted it inside the close.
  removeChat(id);
  // Local only: reopening the chat seeds the text back from the server.
  dropComposerState(id);
  // `removeChat` reassigns the active chat with no activation, so retarget the composer;
  // idempotent.
  retargetComposer(getActiveId());
}

// --- Activation generation ---

/**
 * Bumped by every showChatView call, so a superseded activation of the SAME chat cannot
 * paint its retry box over a loaded transcript (the painted-chat check catches other chats).
 */
let activationGen = 0;

/** Remove every failure box under the transcript root: a box painted beside no view sits at the
 *  root, and the transcript repaint owns neither. */
function clearChatLoadError(): void {
  for (const box of $.messages.querySelectorAll(".load-error")) {
    box.remove();
  }
}

/** The transcript's failure affordance: the message and one Retry button running `retry`. */
function paintChatLoadError(message: string, retry: () => void): void {
  const box = el("div", { className: "load-error" }, el("span", {}, message));
  const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
  btn.addEventListener("click", retry);
  box.appendChild(btn);
  (activeTranscriptView() ?? $.messages).appendChild(box);
}

/** The one painter for a chat whose first page failed with nothing resident: the cover comes
 *  down and, while the chat is the painted one, the Retry box goes up (a chat tab's
 *  `onLoadFailed`). */
export function showBlankLoadFailure(chatID: string): void {
  dropCover();
  if (getPaintedId() !== chatID) {
    return;
  }
  clearChatLoadError();
  paintChatLoadError("Failed to load messages.", () => {
    void retryChatLoad(chatID);
  });
}

/** The reader is still on `chatID` and it is still the mounted transcript. */
function readerOn(chatID: string): boolean {
  return getActiveId() === chatID && getPaintedId() === chatID;
}

/** The failure box's Retry: load the chat again in place, under the cover when nothing of it is
 *  resident. A click inside the painted view, so no switch is involved. */
async function retryChatLoad(chatID: string): Promise<void> {
  // First: the click left focus on the Retry button the cover is about to hide.
  moveFocusOffTranscript();
  clearChatLoadError();
  markWindowStale(chatID);
  if ((get(chatID)?.turn_order.length ?? 0) === 0) {
    holdCover(chatID);
  }
  try {
    await refreshChatView(chatID, AbortSignal.timeout(SWITCH_LOAD_MS));
  } catch {
    if (readerOn(chatID)) {
      showBlankLoadFailure(chatID);
    }
  }
}

/** One store re-read per activation of a chat the store lacks; the generation check makes
 *  fire-and-forget safe. */
async function healMissingChat(id: string, gen: number): Promise<void> {
  if (!(await loadList())) {
    return;
  }
  if (!readerOn(id) || activationGen !== gen || get(id) === undefined) {
    return;
  }
  selectChat(id);
  showChatView(id);
  try {
    await refreshChatView(id, AbortSignal.timeout(SWITCH_LOAD_MS));
  } catch {
    if (readerOn(id)) {
      showBlankLoadFailure(id);
    }
  }
}

/**
 * Put the reader on chat `id`: the store's active chat and the composer move to it. Reads no
 * layout and paints nothing; `showChatView` mounts its transcript.
 */
export function selectChat(id: string): void {
  // The save MUST precede setActive: it reads the outgoing chat's id, which nothing
  // can recover afterwards.
  saveComposerState();
  setActive(id);
  restoreComposerState(id);
  const session = get(id);
  if (session?.usage.context_size === 0 && session.model !== "") {
    // setModel recomputes the derived usage.context_size; mutating `session.usage`
    // directly writes to a reference subscribers never see.
    setModel(id, session.model);
  }
}

/**
 * Mount chat `id`'s transcript and point every per-chat view at it (a chat tab's `onShow`).
 * Fetches NOTHING but the missing-session re-read: the refresh owns the page.
 */
export function showChatView(id: string): void {
  const gen = ++activationGen;
  // Re-keyed before any branch can skip it, so no path inherits the previous chat's markers;
  // the refresh fetches the session-wide rail itself.
  pointTurnRail(id);
  setPainted(id);
  ensureBound();
  clearChatLoadError();
  const session = get(id);
  if (session === undefined) {
    // A tab naming a chat the store lacks means the store is stale: one re-read. The box paints
    // FIRST, alone, so a failed re-read leaves something to act on and no other chat's turns
    // sit beside it; it is what the reader acts on, so the cover comes down.
    parkPaintedView();
    paintChatLoadError("This conversation is not loaded yet.", () => {
      void healMissingChat(id, activationGen);
    });
    dropCover();
    void healMissingChat(id, gen);
    return;
  }

  // The view furniture stays ABOVE the arms: hoisting would add an uncoalesced
  // `/api/chats/{id}/turns` to every stale activation.
  if (!isEmptyChat(session) && !transcriptStale(session)) {
    setupLoadMore(id);
    void loadTurnRail(id); // UNFORCED; refreshChatView owns the forced one
  }
}

/** The newest page `startChatPage` sent, until a `refreshChatView` takes or passes it. */
let startedPage: {
  readonly id: string;
  readonly signal: AbortSignal;
  readonly load: Promise<PageLoad>;
} | null = null;

/** Send the newest page `refreshChatView(id, signal)` would load, before `showChatView` mounts
 *  the chat (a chat tab's `prefetch`); that refresh joins it. Sends nothing for a chat the store
 *  lacks, an empty chat or a fresh window, whose refresh loads no page. */
export function startChatPage(id: string, signal: AbortSignal): void {
  const session = get(id);
  if (session === undefined || isEmptyChat(session) || !transcriptStale(session)) {
    return;
  }
  startedPage = { id, signal, load: loadMessages(id, undefined, signal) };
}

/**
 * Bring chat `id`'s DATA up to date (a chat tab's `refresh`). Resolves once the view has
 * something real to show; rejects with an `ActionError` only when nothing of the chat is
 * resident and its first page did not land, painting nothing (the caller owns that failure).
 * A failed refresh behind resident turns paints its own Retry box and resolves.
 */
export function refreshChatView(id: string, signal: AbortSignal): Promise<void> {
  const early = startedPage;
  startedPage = null;
  const session = get(id);
  if (session === undefined) {
    return Promise.resolve(); // showChatView painted the box and owns the re-read
  }
  // A refresh is not an activation and must not invalidate the one in flight, so it
  // reads the current generation rather than minting one.
  const gen = activationGen;
  if (isEmptyChat(session)) {
    // The record GET that adopts the server-held draft. Gated, because it IS a fetch:
    // an empty chat's draft is the only thing this arm is after.
    if (transcriptStale(session)) {
      seedEmptyChatDraft(id);
    }
    return Promise.resolve();
  }
  if (!transcriptStale(session)) {
    // The window is the server's answer and nothing has undermined it since, so
    // switching back costs ZERO fetches.
    return Promise.resolve();
  }
  if (getPaintedId() === id) {
    clearChatLoadError();
  }
  const load =
    early?.id === id && early.signal === signal ? early.load : loadMessages(id, undefined, signal);
  if (session.turn_order.length > 0) {
    void load.then((r) => {
      if (r.kind === "loaded") {
        afterPageLoad(id, gen);
      } else if (r.kind === "failed" && getPaintedId() === id && activationGen === gen) {
        paintChatLoadError("Failed to load messages.", () => {
          void retryChatLoad(id);
        });
      }
    });
    return Promise.resolve();
  }
  return load.then((r) => {
    if (r.kind !== "loaded") {
      throw pageRefusal(r, signal);
    }
    afterPageLoad(id, gen);
  });
}

/** The verdict of a first page that did not land, as the failure class its owner words. */
function pageRefusal(r: Exclude<PageLoad, { kind: "loaded" }>, signal: AbortSignal): ActionError {
  if (r.kind === "aborted") {
    const reason: unknown = signal.reason;
    return reason instanceof DOMException && reason.name === "TimeoutError"
      ? new ActionError("timeout", { code: "timeout" })
      : new ActionError("aborted", { code: "aborted" });
  }
  if (r.status === 404) {
    return new ActionError("gone", { status: 404 });
  }
  if (r.status === 0) {
    return new ActionError("network", { status: 0, code: "network" });
  }
  return new ActionError("load failed", { status: r.status });
}

/** What a landed newest page enables, for the chat still mounted by the same activation. */
function afterPageLoad(id: string, gen: number): void {
  if (activationGen !== gen) {
    return;
  }
  // The chat's record is in now, so its stored draft can be adopted. Deliberately
  // loses to a draft the user has started typing since the activation.
  if (getActiveId() === id) {
    seedComposerState(id);
  }
  if (getPaintedId() !== id || get(id) === undefined) {
    return;
  }
  setupLoadMore(id);
  // The session-wide rail is its own fetch, FORCED: the load just re-stamped the session fresh.
  void loadTurnRail(id, { force: true });
}

/**
 * Fetch a message-less chat's record to adopt its stored draft (the branch above skips that
 * GET). Re-checked after the await: the seed writes the shared composer.
 */
function seedEmptyChatDraft(id: string): void {
  void loadMessages(id).then((r) => {
    if (r.kind === "loaded" && getActiveId() === id) {
      seedComposerState(id);
    }
  });
}

// --- Pagination ---

/** The scroller's history edge follows the session's `has_more`, which any pager (the edge, the turn map, the in-chat
 *  search) moves, so it is re-read when the window changes rather than captured once. */
let stopLoadMore: (() => void) | null = null;

function setupLoadMore(chatID: string): void {
  stopLoadMore?.();
  stopLoadMore = effect(() => {
    touch(messagesVersionOf(chatID));
    if (getPaintedId() !== chatID) {
      return;
    }
    const hasMore = get(chatID)?.has_more === true;
    setLoadMore(
      hasMore
        ? (): void => {
            pageOlder(chatID);
          }
        : null,
      hasMore,
    );
  });
}

function pageOlder(chatID: string): void {
  void loadOlderTurns(chatID).then(() => {
    // scroll.ts watches for this removal as the "load complete" signal.
    // Scoped to this chat's view: parked views keep the id resident too.
    transcriptViewFor(chatID)?.querySelector(`[id="load-more-skeleton"]`)?.remove();
  });
}

// --- Sending prompts ---

/** Send from the composer; the composer holds the text until the answer says where it went. */
export function sendPrompt(text: string): Promise<SubmitResult> | undefined {
  const chatID = getActiveId();
  if (chatID === "") {
    return undefined;
  }
  return submitPrompt(chatID, text);
}

/** The composer's Esc: cancel what the active chat's run names (`run.cancel`), by id, so a stale press stops nothing else. */
export function cancelActiveTurn(): void {
  const chatID = getActiveId();
  const target = chatID === "" ? undefined : get(chatID)?.run?.cancel;
  if (target === undefined || target === "") {
    return;
  }
  void cancelTurn.dispatch({ chatID, target });
}

const NEW_CHAT_NAME = "New conversation";

/**
 * Seed the store row for a chat the SERVER just created, from the returned header.
 * `usage.context_size` is derived: the client owns the model catalog.
 */
function seedChat(header: ChatHeader): void {
  // The reply's header was built before the chat's log existed, so its read position is 0; a frame
  // that already landed for this chat carries the log's, which the stale 0 must not erase.
  const held = get(header.id)?.read_position;
  upsertHeader(held === undefined ? header : { ...header, read_position: held });
  const model = header.model ?? "";
  if (model !== "") {
    setModel(header.id, model);
  }
}

/**
 * Adopt the tab a creating command opened server-side: paint it and hand the pending-op machine
 * what committed. Answers the tab id, or "" when the reply carried no subject and the op was retired.
 */
function adoptCreatedTab(opID: string, created: CreatedChat): string {
  const subject = created.subject;
  if (subject === undefined) {
    opFailed(opID);
    return "";
  }
  adoptSubject(subject, created.chat.name === "" ? NEW_CHAT_NAME : created.chat.name);
  adoptCommitted(opID, subject, created.version, true);
  return subject.id;
}

/**
 * Create a new chat and open its tab, resolving to the id or "" when refused. Files staged
 * with no chat move to it; a non-empty initialPrompt or files send at once. The tab takes the
 * view only if the user made no navigation since the call; the store's active chat equals the
 * id after the await iff it did. Every caller awaits or detaches explicitly.
 */
export async function createSession(initialPrompt?: string): Promise<string> {
  const ticket = claimNavigation();
  const model = getLastModel();
  // Minted here, never inside run(), which retries would re-run (a second chat per gesture).
  // Registered BEFORE the dispatch so the tabs frame correlates.
  const opID = newOpID();
  beginAdopt(opID);
  // Read before the await: a chat the reader moves to meanwhile discards files no chat owns.
  const carried = unownedAttachmentPaths();
  const created = await createChat.dispatch({ opID, model });
  if (created === null) {
    // The action framework has already raised its toast; there is no chat.
    opFailed(opID);
    return "";
  }
  seedChat(created.chat);
  const id = created.chat.id;
  const tabID = adoptCreatedTab(opID, created);
  const sends = initialPrompt !== undefined && (initialPrompt !== "" || carried.length > 0);
  if (!navigationHeld(ticket)) {
    // The user moved on, so the composer on screen is another chat's: the carried files and the
    // prompt go to the new chat by id.
    if (sends) {
      void submitPrompt(id, initialPrompt, { attachments: carried.map(toAttached) });
    } else {
      for (const path of carried) {
        addAttachmentTo(id, path);
      }
    }
    return id;
  }
  setActive(id);
  // Ahead of the activation, whose save would drop what the empty composer held: the retarget
  // hands its text and files to this chat.
  retargetComposer(id);
  if (tabID !== "") {
    void activateTab(tabID);
  }
  if (sends) {
    void submitPrompt(id, initialPrompt);
  }
  return id;
}

const FRESH_TANGENT_NOTICE =
  "This tangent started without the parent conversation, so the agent has none of its context.";

/**
 * Open a TANGENT off `parentChatID` as a SUB-TAB: `chat.fork` binds it to KAS's
 * `session/fork`, so nothing is copied or synced. It forks at the head, or after the turn
 * `atMessageID` opened. `TabSubject.Parent` is set once, making a cycle unrepresentable. The
 * sub-tab takes the view only if the user made no navigation since the call. Answers the
 * tangent's id once it holds the view and the composer, or "" when nothing opened or the user
 * moved elsewhere first.
 */
export async function openTangentChat(parentChatID: string, atMessageID?: string): Promise<string> {
  if (parentChatID === "" || get(parentChatID) === undefined) {
    return "";
  }
  const ticket = claimNavigation();
  // The fork creates the chat and MINTS its id, so the sub-tab can only open once the
  // reply lands.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await forkChat.dispatch({
    opID,
    parentChatID,
    ...(atMessageID === undefined ? {} : { messageID: atMessageID }),
  });
  if (created === null) {
    opFailed(opID);
    return "";
  }
  seedChat(created.chat);
  const id = created.chat.id;
  // The reply's subject already carries the PARENT the coordinator nested the tangent
  // under, so adopting it paints the sub-tab with no second POST.
  const tabID = adoptCreatedTab(opID, created);
  const shown = navigationHeld(ticket);
  if (shown) {
    if (tabID !== "") {
      void activateTab(tabID);
    } else {
      // No tab to activate, so the store pointer and the composer move by hand.
      setActive(id);
      retargetComposer(id);
    }
  }
  // After the activation, so the notice offers Open only when the tangent is off screen.
  if (created.fresh) {
    chatNotice(id, FRESH_TANGENT_NOTICE, "warning");
  }
  return shown ? id : "";
}

/**
 * Put the reader on this chat, OPENING its tab when it has none. A refusal is not reported
 * here: `openTabCommand` already toasted, and the outcome lets the caller canonicalize the
 * URL.
 */
export async function switchSession(id: string): Promise<OpenTabOutcome | "activated"> {
  // A chat id and its TAB id are different values — the tab's is opaque and
  // server-minted — so reaching a chat's tab goes through the lookup.
  const tabID = tabIdFor("chat", id);
  if (tabID !== "") {
    if (id === getActiveId() && getActiveTabId() === tabID) {
      return "activated";
    }
    void activateTab(tabID);
    return "activated";
  }
  // No tab. The caller has already established the chat EXISTS, and only the router
  // can rewrite a URL naming no record, so this is the open.
  return openChatTab(id, get(id)?.name ?? "Chat");
}

/**
 * Settle a deep-linked chat id the store has NO row for by ASKING the server (the store goes
 * stale after `loadList`): `opened`, `gone`, or `unresolved` (say nothing terminal). The
 * store is re-read after the await.
 */
export async function resolveUnknownChat(id: string): Promise<"opened" | "gone" | "unresolved"> {
  const verdict = await confirmChatExists(id);
  if (verdict === "exists" || get(id) !== undefined) {
    await switchSession(id);
    return "opened";
  }
  return verdict;
}

/**
 * Attach workspace files to the active chat's next prompt as pills, switching to the chat
 * tab if needed. PLURAL: per-file calls would each create a chat on an empty workspace.
 */
export async function attachPathsToActiveChat(paths: readonly string[]): Promise<void> {
  if (paths.length === 0) {
    return;
  }
  let id = getActiveId();
  if (id === "") {
    id = await createSession();
    if (id === "") {
      return;
    }
    if (getActiveId() !== id) {
      // The user moved on while the chat was created, so the files wait on it unseen.
      for (const p of paths) {
        addAttachmentTo(id, p);
      }
      return;
    }
  }
  const tabID = tabIdFor("chat", id);
  if (tabID !== "" && getActiveTabId() !== tabID) {
    void activateTab(tabID);
  }
  for (const p of paths) {
    addAttachment(p);
  }
  $.promptInput.focus();
}

/**
 * Open a session the previous-session picker listed; every listed row is claimed by a
 * marotte chat (`toResumable`), so `chat_id` is always present.
 */
export async function openPreviousSession(
  row: ResumableSession,
): Promise<"opened" | "gone" | "failed"> {
  const chatID = row.chat_id ?? "";
  if (chatID === "") {
    return "failed";
  }
  const existing = get(chatID);
  if (existing === undefined) {
    // Required: a chat closed in this page is gone from the store while its file survives.
    await loadList();
  }
  // A tab already open and active is not activated again, so only a refresh can bring it current.
  const tabID = tabIdFor("chat", chatID);
  const wasActive = tabID !== "" && getActiveTabId() === tabID;
  // AWAITED: opening the tab is a round trip, and its activation is the one activation.
  const outcome = await openChatTab(chatID, get(chatID)?.name ?? row.title);
  if (outcome === "not-found") {
    // Retention is off and a close DELETED this conversation: said with activation skipped,
    // and no Open offered, since it would 404.
    info(
      named(
        noticeSubject(chatID, row.title),
        "That conversation is gone. It was ephemeral because retention is off.",
      ),
    );
    return "gone";
  }
  if (outcome !== "opened") {
    // The framework's error surface has already spoken; the row stays.
    return "failed";
  }
  // The user may have moved to another tab while the open was in flight; the view is then theirs.
  if (wasActive && tabIdFor("chat", chatID) === getActiveTabId()) {
    refreshActiveView();
  }
  return "opened";
}

/**
 * Create a chat pre-set to the "plan" mode (`?agent=planner`), persisted on the empty chat
 * and applied at session/new. AWAITS the create: `set_mode` addresses its chat.
 */
export async function createPlannerSession(): Promise<void> {
  const id = await createSession();
  if (id === "") {
    return;
  }
  void setMode.dispatch({ chatID: id, modeID: "plan" });
}

/**
 * A new chat in the "spec" mode, then `prompt`, shown in the transcript as `displayText`. A
 * refused mode switch sends nothing; its action has already raised the error.
 */
export async function createSpecSession(prompt: string, displayText: string): Promise<void> {
  const id = await createSession();
  if (id === "") {
    return;
  }
  const switched = await setMode.dispatch({ chatID: id, modeID: "spec" }).outcome;
  if (switched.status !== "success") {
    return;
  }
  void submitLabelled(id, prompt, displayText);
}

/**
 * One open chat tab's row effect: tracks this chat's signal (and the set's structure, so a
 * late row still paints) plus the dock, and writes only its own row.
 */
function chatRowEffect(chatID: string): () => void {
  return effect(() => {
    const s = watchSession(chatID);
    const pendingAsk = hasPendingDecision(chatID);
    if (s === undefined) {
      return;
    }
    // ONE lookup, reused by both writers below; a chat id is not the row's id.
    const tabID = tabIdFor("chat", chatID);
    if (tabID === "") {
      return;
    }
    // Reconcile tab name with server auto-rename / agent focus title.
    renameTab(tabID, s.name);
    // `updated_at` is LAST ACTIVITY, which the outcome phrase's age renders; the only caller
    // supplying one.
    setTabStatus(tabID, tabStatusFor(s, pendingAsk), s.updated_at);
  });
}

/** The previous install's teardown: a re-install (tests) tears the old wiring down
 *  first so row effects cannot stack and double-write. */
let disposeInstall: (() => void) | undefined;

/** Wire the tab strip's per-open-row effects and the context bar's active-session
 *  effect. Also mounts the chat view (idempotent). */
export function installStoreSubscribers(): void {
  mountChatView();
  disposeInstall?.();

  // The context bar tracks the ACTIVE session only: a background session's churn never
  // reaches it. Covers activation and every store write to the active chat.
  const disposeContext = effect(() => {
    const active = activeSession.value;
    if (active !== undefined) {
      refreshContextUI(active);
    }
  });

  // Row effects appear and disappear with open tabs, so a session with no open tab is
  // subscribed to by nothing and triggers nothing.
  const rowEffects = new Map<string, () => void>();
  const disposeSync = effect(() => {
    const open = openChatRefs();
    const openSet = new Set(open);
    for (const [chatID, dispose] of rowEffects) {
      if (!openSet.has(chatID)) {
        dispose();
        rowEffects.delete(chatID);
      }
    }
    for (const chatID of open) {
      if (!rowEffects.has(chatID)) {
        rowEffects.set(chatID, chatRowEffect(chatID));
      }
    }
  });

  disposeInstall = () => {
    disposeSync();
    for (const dispose of rowEffects.values()) {
      dispose();
    }
    rowEffects.clear();
    disposeContext();
  };
}
