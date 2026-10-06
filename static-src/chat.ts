// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns the
// chat-tab registration (openChatTab) and load-more pagination.

import type { ResumableSession } from "./types.js";
import {
  getActiveId,
  get,
  watchSession,
  setActive,
  setModel,
  upsertHeader,
  activeSession,
  removeChat,
  tabStatusFor,
  isEmptyChat,
  transcriptStale,
} from "./store.js";
import { loadList, loadMessages, confirmChatExists } from "./store-load.js";
import { effect, el } from "@cplieger/reactive";
import type { ChatHeader } from "./types.js";
import { ensureBound } from "./banner-stack.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  tabIdFor,
  getActiveTabId,
  renameTab,
  setTabStatus,
  openChatRefs,
  type OpenTabOutcome,
  type TabDotStatus,
} from "./tabs.js";
import { beginAdopt, adoptCommitted, opFailed } from "./tabs-sync.js";
import { hasPendingDecision, dropDecisions } from "./decision-dock.js";
import { forgetDeferredCue } from "./agent-finished-cue.js";
import { submitPrompt } from "./submit.js";
import { chatSkeleton, paintPlaceholder } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import {
  mountChatView,
  setLoadMore,
  loadTurnRail,
  pointTurnRail,
  activeTranscriptView,
  transcriptViewFor,
  disposeChatView,
} from "./messages.js";
import { addAttachment, unownedAttachmentPaths } from "./attachments.js";
import {
  saveComposerState,
  restoreComposerState,
  retargetComposer,
  seedComposerState,
  flushComposerDraft,
  dropComposerState,
} from "./composer-state.js";
import { setCurrentModel, getLastModel } from "./session-context.js";
import { refreshContextUI } from "./context-ui.js";
import { $ } from "./dom.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { info } from "./toast.js";
import { named, noticeSubject } from "./notice-subject.js";
import { createChat, forkChat, setMode, type CreatedChat } from "./actions/chat.js";
import { newOpID } from "./transport.js";

// --- Bus: activate chat from other modules without importing chat.ts ---

onBus(BUS_ACTIVATE_CHAT, (p) => {
  activateChatView(p.chatID);
  refreshChatView(p.chatID);
  if (p.then !== undefined) {
    p.then();
  }
});

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
  // A withheld agent-finished cue is keyed by chat id too.
  forgetDeferredCue(id);
  // Before removeChat: the store reassigning the active chat repaints synchronously,
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
 * Bumped by every activateChatView call, so a superseded activation of the SAME chat cannot
 * paint its retry box over a loaded transcript (`getActiveId() !== id` catches other chats).
 */
let activationGen = 0;

/**
 * Remove a previous activation's failure box; every activation clears it, since the
 * transcript repaint does not own it.
 */
function clearChatLoadError(): void {
  for (const box of (activeTranscriptView() ?? $.messages).querySelectorAll(".load-error")) {
    box.remove();
  }
}

/**
 * The transcript's failure affordance, one Retry button for both failures (messages did not
 * load, or the record is not in the store). Retry re-activates and refetches.
 */
function paintChatLoadError(message: string, id: string): void {
  const box = el("div", { className: "load-error" }, el("span", {}, message));
  const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
  btn.addEventListener("click", () => {
    activateChatView(id);
    refreshChatView(id); // the fetch the dispatcher would have supplied
  });
  box.appendChild(btn);
  (activeTranscriptView() ?? $.messages).appendChild(box);
}

/**
 * Re-read the chat list for a tab whose chat the store lacks and re-activate if it appears.
 * One attempt per activation; the generation check makes fire-and-forget safe.
 */
async function healMissingChat(id: string, gen: number): Promise<void> {
  if (!(await loadList())) {
    return;
  }
  if (getActiveId() !== id || activationGen !== gen || get(id) === undefined) {
    return;
  }
  activateChatView(id);
  refreshChatView(id);
}

/**
 * Point every per-chat view at `id` (a chat tab's `onShow`). Fetches NOTHING: `refreshRow`
 * calls `refreshChatView` right after.
 */
export function activateChatView(id: string): void {
  // The save MUST precede setActive: it reads the outgoing chat's id, which nothing
  // can recover afterwards.
  saveComposerState();
  const gen = ++activationGen;
  // Re-keyed before any branch can skip it, so no path inherits the previous chat's markers;
  // the refresh fetches the session-wide rail itself.
  pointTurnRail(id);
  setActive(id);
  restoreComposerState(id);
  ensureBound();
  clearChatLoadError();
  const session = get(id);
  if (session === undefined) {
    // A tab naming a chat the store lacks means the store is stale: one re-read. The affordance
    // paints FIRST so a failed re-read leaves something to act on.
    paintChatLoadError("This conversation is not loaded yet.", id);
    void healMissingChat(id, gen);
    return;
  }

  if (session.usage.context_size === 0 && session.model !== "") {
    // setModel recomputes the derived usage.context_size; mutating `session.usage`
    // directly writes to a reference subscribers never see.
    setModel(id, session.model);
  }

  // The view furniture stays ABOVE the arms: hoisting would add an uncoalesced
  // `/api/chats/{id}/turns` to every stale activation.
  if (!isEmptyChat(session) && !transcriptStale(session)) {
    setupLoadMore(id);
    void loadTurnRail(id); // UNFORCED; refreshChatView owns the forced one
  }
}

/**
 * Bring the chat's DATA up to date: `refreshRow` calls this after `activateChatView`, and
 * that hook's four direct callers call it themselves.
 */
export function refreshChatView(id: string): void {
  const session = get(id);
  if (session === undefined) {
    return; // healMissingChat is activation's, not refresh's
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
    return;
  }
  if (!transcriptStale(session)) {
    // The window is the server's answer and nothing has undermined it since, so
    // switching back costs ZERO fetches.
    return;
  }
  // The failure box is not content, so it would sit over the placeholder; idempotent.
  clearChatLoadError();
  // `showSubagent` does not `setActive`, so `getActiveId() !== id` keeps a delegated refresh's
  // shimmer out of another chat. Deferred 150ms on a cold transcript so a cached open never
  // flashes.
  const skeleton =
    session.turn_order.length > 0 || getActiveId() !== id
      ? null
      : skeletonTiming(() =>
          // Into the ACTIVE VIEW: the view's own column geometry positions the
          // placeholder. The multiplexer fallback covers a fixture with no view.
          paintPlaceholder(
            activeTranscriptView() ?? $.messages,
            chatSkeleton,
            // The transcript container is shared with the load-more furniture and with
            // messages.ts's drop-by-id half, so a placeholder here must not take it over.
            { mount: "append" },
          ),
        );
  void loadMessages(id).then((ok) => {
    skeleton?.cancel();
    if (getActiveId() !== id || activationGen !== gen) {
      return;
    }
    if (!ok) {
      paintChatLoadError("Failed to load messages.", id);
      return;
    }
    // The chat's record is in now, so its stored draft can be adopted. Deliberately
    // loses to a draft the user has started typing since the activation.
    seedComposerState(id);
    const fresh = get(id);
    if (fresh !== undefined) {
      setupLoadMore(id);
    }
    // The session-wide rail is its own fetch, FORCED: the load just re-stamped the session fresh.
    void loadTurnRail(id, { force: true });
  });
}

/**
 * Fetch a message-less chat's record to adopt its stored draft (the branch above skips that
 * GET). Re-checked after the await: the seed writes the shared composer.
 */
function seedEmptyChatDraft(id: string): void {
  void loadMessages(id).then((ok) => {
    if (ok && getActiveId() === id) {
      seedComposerState(id);
    }
  });
}

// --- Pagination ---

function setupLoadMore(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) {
    return;
  }
  setLoadMore(
    session.has_more
      ? (): void => {
          // `turn_order` is FILE order with an older page PREPENDED, so its first
          // id is the oldest resident turn — the cursor the window pages before.
          const oldest = session.turn_order[0];
          if (oldest === undefined) {
            return;
          }
          void loadMessages(chatID, oldest).then(() => {
            // scroll.ts watches for this removal as the "load complete" signal.
            // Scoped to this chat's view: parked views keep the id resident too.
            transcriptViewFor(chatID)?.querySelector(`[id="load-more-skeleton"]`)?.remove();
            if (getActiveId() !== chatID) {
              return;
            }
            const s = get(chatID);
            if (s === undefined) {
              return;
            }
            // Store mutations bump version; the chat-view effect re-renders.
            setupLoadMore(chatID);
          });
        }
      : null,
    session.has_more,
  );
}

// --- Sending prompts ---

/**
 * Send from the composer. `submitPrompt` sets `thinking`, which closes the picker's overlay
 * for every sender.
 */
export function sendPrompt(text: string): void {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  void submitPrompt(chatID, text);
}

const NEW_CHAT_NAME = "New conversation";

/**
 * Seed the store row for a chat the SERVER just created, from the returned header.
 * `usage.context_size` is derived: the client owns the model catalog.
 */
function seedChat(header: ChatHeader): void {
  upsertHeader(header);
  const model = header.model ?? "";
  if (model !== "") {
    setModel(header.id, model);
  }
}

/**
 * Adopt the tab a creating command opened server-side: paint it, hand the pending-op
 * machine what committed, activate. The reply carries the committed subject; with none,
 * the op is retired.
 */
function adoptCreatedTab(opID: string, created: CreatedChat): void {
  const subject = created.subject;
  if (subject === undefined) {
    opFailed(opID);
    return;
  }
  adoptSubject(subject, created.chat.name === "" ? NEW_CHAT_NAME : created.chat.name);
  adoptCommitted(opID, subject, created.version, true);
  activateTab(subject.id);
}

/**
 * Create a new chat and open its tab, resolving to the id or "" when refused. Files staged
 * with no chat move to it; a non-empty initialPrompt or files send at once. Every caller
 * awaits or detaches explicitly: a bare `void` then `getActiveId()` reads the PREVIOUS chat.
 */
export async function createSession(initialPrompt?: string): Promise<string> {
  const model = getLastModel();
  // Minted here, never inside run(), which retries would re-run (a second chat per gesture).
  // Registered BEFORE the dispatch so the tabs frame correlates.
  const opID = newOpID();
  beginAdopt(opID);
  // Read before the await: files staged with no chat open belong to no chat, so the
  // retarget below would discard them instead of handing them to the chat it makes.
  const carried = unownedAttachmentPaths();
  const created = await createChat.dispatch({ opID, model });
  if (created === null) {
    // The action framework has already raised its toast; there is no chat.
    opFailed(opID);
    return "";
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // The composer is THIS chat's from here, so text typed during the round trip is not filed
  // under the chat just left.
  retargetComposer(id);
  for (const path of carried) {
    addAttachment(path);
  }
  // The reply carries the tab the create opened server-side, so the row is painted and
  // ACTIVATED from the response, with no second round trip.
  adoptCreatedTab(opID, created);

  if (initialPrompt !== undefined && (initialPrompt !== "" || carried.length > 0)) {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  }
  return id;
}

/**
 * Open a TANGENT off `parentChatID` as a SUB-TAB: `chat.fork` binds it to KAS's
 * `session/fork`, so nothing is copied or synced. `TabSubject.Parent` is set once, making a
 * cycle unrepresentable.
 */
export async function openTangentChat(parentChatID: string): Promise<void> {
  if (parentChatID === "" || get(parentChatID) === undefined) {
    return;
  }
  const model = get(parentChatID)?.model ?? getLastModel();
  // The fork creates the chat and MINTS its id, so the sub-tab can only open once the
  // reply lands.
  const opID = newOpID();
  beginAdopt(opID);
  const created = await forkChat.dispatch({ opID, parentChatID });
  if (created === null) {
    opFailed(opID);
    return;
  }
  seedChat(created.chat);
  const id = created.chat.id;
  setActive(id);
  // The tangent is the active chat now, so the composer must be its own first.
  retargetComposer(id);
  // The reply's subject already carries the PARENT the coordinator nested the tangent
  // under, so adopting it paints the sub-tab and activates it with no second POST.
  adoptCreatedTab(opID, created);
  setCurrentModel(model);
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
    activateTab(tabID);
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
  }
  const tabID = tabIdFor("chat", id);
  if (tabID !== "" && getActiveTabId() !== tabID) {
    activateTab(tabID);
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
  // AWAITED: opening the tab is a round trip. `openChatTab` already activates, so the
  // explicit call below is the belt for a tab already open and active, where it refetches.
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
  activateChatView(chatID);
  refreshChatView(chatID);
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
