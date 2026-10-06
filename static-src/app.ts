// App wiring: construction, injection, `applyRoute` (the switch over every route kind,
// reaching most surfaces) and the pre-session catalog fetch, the one place that sees the
// endpoint, the phase sink and the picker. Boot is `boot.ts`, whoami is `identity.ts`.
// The server is the source of truth: a prompt posts a command and SSE drives rendering.

import type { ServerEvent } from "./types.js";
import { getActiveId, getSessions, isThinking } from "./store.js";
import { effect } from "@cplieger/reactive";
import { dispatch, onBus, onSSE, BUS_TAB_CHANGED, BUS_RECONCILE } from "./bus.js";
import { findGlyph } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { $, byId } from "./dom.js";
import { guardDuplicateActivation, initSidebarSwipe } from "./platform.js";
import { initShellViewport } from "./shell-viewport.js";
import { initPointerTier } from "./pointer-tier.js";
import { initPointerModeToggle, revealPointerModeToggle } from "./pointer-mode.js";
import { initPageTitleFit } from "./page-title.js";
import { initRolePicker } from "./role-picker.js";
import * as sse from "./sse-adapter.js";
import { initUI, renderIdentity } from "./settings.js";
import { initPostAuth, onTransportStatus, startBoot } from "./boot.js";
import { resolveIdentity } from "./identity.js";
import { fetchCatalog } from "./session-catalog.js";
import {
  setOnEmpty,
  activeChatRef,
  registerTabNotice,
  setChatSettledProbe,
  toggleDocsView,
  toggleHistoryView,
} from "./tabs.js";
import { chatNotice } from "./notice-subject.js";
import { applyRoute } from "./route-apply.js";
import { markBootDone } from "./view-swap.js";
import { ingestTabsChanged, listTabs } from "./tabs-sync.js";
import { onPopState } from "./router.js";
import type { Route } from "./route-path.js";
import { initModelPicker } from "./picker.js";
import { refreshRuntimeLine } from "./status.js";
import { initShellPanel } from "./shell.js";
import { initSidebarResize } from "./sidebar-resize.js";
import { hideLoginModal, initLoginModal } from "./modals.js";
import { initEditor } from "./editor-core.js";
import { activateFile, closeEditorFile, refreshFile } from "./editor-openers.js";
import { registerTabOpeners } from "./tab-materialize.js";
import { showRun, refreshRun } from "./run-view.js";
import { showSubagent, refreshSubagent } from "./subagent-view.js";
import { openAtLine } from "./navigate.js";
import { initAttachmentPillCallbacks } from "./attachment-pill.js";
import { initLinkifyCallbacks } from "./linkify.js";
import { setPreviewOpener } from "./preview-card.js";
import { openWebPreview } from "./web-open.js";
import { initFileBrowser } from "./files.js";
import { initFilePicker, openFilePicker } from "./files-picker.js";
import { initChatAttach } from "./files-drop.js";
import { initTaskListPill } from "./task-list.js";
import { initAwaySummary } from "./away-summary.js";
import { initAttention } from "./attention.js";
import { initTerminalStream } from "./terminal-stream.js";
import { initTooltips } from "./tooltip.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { initKeyboardShortcuts } from "./keys.js";
import { openShortcutsSheet } from "./shortcuts.js";
import {
  handleFindKey,
  toggleFindForActiveTab,
  findAffordanceForActiveTab,
} from "./find-dispatch.js";
import { handleFilesTypeAhead } from "./files-search.js";
import {
  createSession,
  sendPrompt,
  installStoreSubscribers,
  activateChatView,
  refreshChatView,
  closeChatTab,
  chatTabDot,
} from "./chat.js";
import { initModelSwitcher, pickModel } from "./model-switcher.js";
import { makeExpandable } from "./pill-expand.js";
import { loadAccountUsage } from "./account-usage.js";
import { initPromptInput, sendComposer } from "./prompt-input.js";
import { initComposerState } from "./composer-state.js";
import { initSlashMenu } from "./slash-menu.js";
import { hasAttachments } from "./attachments.js";
import { initPendingSteers } from "./pending-steers.js";
import { initRunBar } from "./run-bar.js";
import { initChatOptions } from "./chat-options.js";
import { mountDecisionDock } from "./decision-dock.js";
import { registerAllSSEDecoders } from "./wire/registry.gen.js";

// Explicit: the transport reports over the bus, so this import is the only thing loading
// the failure toast's subscription.
import "./failure-notice.js";
import "./handlers/chat.js";
import "./handlers/entries.js";
import "./handlers/turn.js";
import "./handlers/system.js";
import "./handlers/open-external-url.js";
import "./handlers/safety.js";
import "./handlers/knowledge-indexing.js";
import "./handlers/run.js";
import { installRunDotSubscriber } from "./run-dots.js";
import { installSubagentDotSubscriber } from "./subagent-dots.js";
import { installChatRunDotSubscriber } from "./chat-run-dots.js";
import { installDeferredCueSubscriber } from "./agent-finished-cue.js";
import { installNotifyAskGesture } from "./notify.js";
import { chatSettled } from "./chat-settled.js";
import "./handlers/steer.js";
import "./handlers/system-notice.js";
import { initPushMessages } from "./handlers/push-message.js";
import { registerNotificationOpener } from "./notification-open.js";
import { initLaunchQueue } from "./share-target.js";
import { cancelTurn } from "./actions/chat.js";
import { copyClipboard } from "./actions/messages.js";
import { setCopyCallback } from "./code-blocks.js";
import { installLinkGuard, setLinkCopyCallback } from "./link-guard.js";
import { registerCleanup, subscribeToActions } from "./actions/index.js";
import { initActions } from "./actions/boot.js";
import { initBeatPhase } from "./beat-phase.js";
import { initIconCrisp } from "./icon-crisp.js";

// Init

function init(): void {
  // FIRST: `data-pointer` on <html> decides every control height, hit target and icon size.
  // This re-applies what prepaint.js set (the only apply when it failed); the reveal is
  // registered here so a coarse pointer arriving during boot is not missed.
  initPointerTier({ onCoarseSeen: revealPointerModeToggle });
  initPointerModeToggle();

  // AFTER the tier, which sizes the bar's action buttons this fit measures against.
  initPageTitleFit();

  initActions();

  // Pixel-snap the icon boxes so a 1px stroke paints one pixel, on EVERY boot (login
  // included). `icon-crisp.ts` carries the measurement.
  initIconCrisp();

  // Before anything can paint a dot: the listener has to be up when the first
  // `vk-dot-beat` starts, or that dot keeps the 0ms fallback and beats out of step.
  initBeatPhase();

  registerTabNotice((subject, name, message, level) => {
    chatNotice(subject, message, level, name);
  });

  // The tab factory's injected half: these openers call `materializeTab` themselves, so
  // registering here keeps the factory out of a cycle.
  registerTabOpeners({
    chat: {
      show: activateChatView,
      refresh: refreshChatView,
      close: closeChatTab,
      dot: chatTabDot,
    },
    editor: { show: activateFile, refresh: refreshFile, close: closeEditorFile },
    run: {
      // `parentless` comes from the run store's record of the launching chat. No `cancel`: a
      // run tab is a VIEW, so its × stops nothing.
      show: (workflowID) => {
        showRun(workflowID);
      },
      refresh: refreshRun,
    },
    // No close half for the same reason: a subagent page is a projection of blocks the
    // chat store owns, so it starts nothing and can stop nothing.
    subagent: { show: showSubagent, refresh: refreshSubagent },
    // Lazily imported: the page module carries the markdown renderer and the task
    // tree, and a session that never opens a spec tab should not pay for either.
    spec: {
      show: (dir) => {
        void import("./spec-view.js").then((m) => {
          m.showSpec(dir);
        });
      },
      refresh: (dir) => {
        void import("./spec-view.js").then((m) => {
          m.refreshSpec(dir);
        });
      },
    },
  });

  setOnEmpty(() => {
    // DETACHED deliberately: this is a notification slot that must not mutate the
    // store it was called from, and nothing here reads the new chat's id.
    void createSession();
  });

  // The tab projection's SYNC half, fed here so its version rules are testable without a
  // transport: every `tabs_changed` frame in ARRIVAL order (the handler must not fan out),
  // and a whole RECONCILE, where the delta stream cannot be trusted.
  onSSE("tabs_changed", (_chatID, p) => {
    ingestTabsChanged(p);
  });
  onBus(BUS_RECONCILE, ({ signal }) => {
    void listTabs(signal);
  });

  // Before the stream opens: an event failing validation is dropped, never handed on
  // partial. The decoders are generated by cmd/wire-codegen.
  registerAllSSEDecoders();

  sse.init((evt: ServerEvent) => {
    dispatch(evt);
  }, onTransportStatus);

  installStoreSubscribers();

  // After installStoreSubscribers: both write tab state, and the effect's sweep paints a run
  // tab the boot restore opens later.
  installRunDotSubscriber();

  // The same dot for a SUBAGENT's row. Here for the reason above: an effect running at
  // import would paint against a strip that has not been restored yet.
  installSubagentDotSubscriber();

  // The WORKFLOW mark on a CHAT's row, for a run that outlives its turn. These three first
  // passes see an empty projection and repaint on the tab restore's bump.
  installChatRunDotSubscriber();

  // The one predicate both out-of-page cue paths ask, injected because `chat-settled.ts`'s
  // stores reach back into the tab projection. Before initAttention, for its first pass.
  setChatSettledProbe(chatSettled);
  // The other half: an agent-finished cue withheld while a run the turn launched is
  // still going is released here, when the last outstanding thing for that chat ends.
  installDeferredCueSubscriber();

  // A cue that could not notify arms this; the reader's next click raises the browser
  // prompt, which needs a gesture.
  registerCleanup(installNotifyAskGesture());

  // The out-of-page attention surfaces, folded from the chat tabs' dots. Before any tab
  // is opened, because it captures the served <title> as its base.
  initAttention();

  // No per-session model feed: /api/config-template is the one catalog feed, and the server
  // prefers a live session's report over the template.

  setupInput();
  initUI();
  initShellPanel();
  initSidebarResize();
  setCopyCallback((text) => void copyClipboard.dispatch(text, { silent: true }));
  // Not silent: the toast is the only confirmation a withheld link's click gets.
  setLinkCopyCallback((url) => void copyClipboard.dispatch(url));
  installLinkGuard();
  initEditor();
  initFileBrowser();
  initFilePicker();
  initChatAttach();
  // One opener for BOTH pill homes. Injected because attachment-pill.ts is a leaf and
  // one of its consumers is a pure `fundamentals/` view.
  initAttachmentPillCallbacks({ open: openAtLine });
  // Injected one rung out: markdown reaches linkify and `editor-markdown` reaches markdown,
  // so linkify importing the opener closed a ring.
  initLinkifyCallbacks({ open: openAtLine });
  setPreviewOpener(openWebPreview);
  initTaskListPill();
  // Through the same dispatcher as Ctrl-F, so the two cannot mean different things. A
  // direct find-in-chat call made this a dead control on /files and /file/{path}.
  $.findBtn.addEventListener("click", () => {
    toggleFindForActiveTab();
  });
  // …and it collapses where it has no destination (`is-collapsed`: `.hidden` cannot animate
  // out). Its glyph comes from the box's own producer, so the button cannot promise a
  // search and open a filter. Run inside an `effect`; the bus covers the tab switch.
  const syncFindAffordance = (): void => {
    const { available, kind } = findAffordanceForActiveTab();
    $.findBtn.classList.toggle("is-collapsed", !available);
    if (!available) {
      // Nothing to repaint: the control is on its way out, and swapping its glyph
      // mid-fade would be a second thing moving.
      return;
    }
    const verb = kind === "search" ? "Search" : "Filter";
    $.findBtn.replaceChildren(iconEl(findGlyph(kind)));
    $.findBtn.setAttribute("aria-label", verb);
    $.findBtn.setAttribute("data-tooltip", `${verb} (Ctrl+F)`);
  };
  effect(() => {
    syncFindAffordance();
  });
  onBus(BUS_TAB_CHANGED, syncFindAffordance);
  $.docsBtn.addEventListener("click", () => {
    void toggleDocsView();
  });
  $.historyBtn.addEventListener("click", () => {
    void toggleHistoryView();
  });
  // Retention = 0 is "no retention" (ephemeral chats, nothing survives a close) → hide
  // History; anything else keeps closed chats → show it.
  const syncHistoryBtn = (): void => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  };
  onRetentionChange(syncHistoryBtn);
  syncHistoryBtn();
  initAwaySummary();
  initTerminalStream();
  initTooltips();
  initLoginModal(onLoginSuccess);
  initSidebarSwipe($.chatArea, $.sidebar);
  initShellViewport();
  initKeyboardShortcuts({
    newChat: () => {
      // DETACHED: closing the sidebar is independent of whether the chat lands, and
      // awaiting would delay it behind a round trip for no gain.
      void createSession();
      $.sidebar.classList.remove("open");
    },
    toggleShell: () => {
      $.shellBtn.click();
    },
    toggleFiles: () => {
      $.filesBtn.click();
    },
    toggleGit: () => {
      $.gitBtn.click();
    },
    toggleSettings: () => {
      $.settingsBtn.click();
    },
    sendMessage: () => {
      sendComposer();
    },
    showShortcuts: openShortcutsSheet,
  });

  // Find (Ctrl-F / Cmd-F), scoped by the ACTIVE TAB. Capture phase to pre-empt the native
  // find; ONE listener, so the chord has one meaning.
  document.addEventListener("keydown", handleFindKey, true);
  document.addEventListener("keydown", focusComposerOnTyping);
  document.addEventListener("keydown", handleFilesTypeAhead);

  // Live-log every action error to the console regardless of toast policy, so a
  // suppressed-toast action is still visible in DevTools.
  subscribeToActions((inst) => {
    if (inst.status !== "error" || inst.error === undefined) {
      return;
    }
    const meta: string[] = [];
    if (inst.completedAt !== undefined) {
      meta.push(`${String(inst.completedAt - inst.startedAt)}ms`);
    }
    if (inst.attempts !== undefined && inst.attempts > 1) {
      meta.push(`${String(inst.attempts)} attempts`);
    }
    if (inst.error.status !== undefined) {
      meta.push(`HTTP ${String(inst.error.status)}`);
    }
    if (inst.error.code !== undefined) {
      meta.push(inst.error.code);
    }
    console.error(
      `[action] ${inst.name} failed (${meta.join(", ")}): ${inst.error.message}`,
      inst.error,
    );
  });

  // Unconditional: an active SW with a fetch handler is a PWA install-criteria requirement.
  // register() is idempotent.
  if ("serviceWorker" in navigator) {
    void navigator.serviceWorker.register("/sw.js").catch((err: unknown) => {
      console.warn("sw: registration failed", err);
    });
  }
  // A notification click is an OPEN INTENT (`deeplink`). Before initPushMessages, which
  // installs the message listener, or a click in between would throw.
  registerNotificationOpener((route) => {
    void applyRoute(route);
  });
  // The other half of the push channel: the worker posts here to route a notification
  // click and to toast a push that arrived while this page was focused.
  initPushMessages(adoptPushTag);
  // The presence tag derives from the push subscription's endpoint, ready only later, so
  // the stream reconnects once if the persisted tag differs.
  adoptPushTag();
  // A relaunch FOCUSES this window (manifest launch_handler), so a shortcut's or share's URL
  // arrives only in the launch queue, which delivers as soon as a consumer exists.
  initLaunchQueue();

  void startBoot({ applyRoute });
}

/**
 * Hand the profile's push subscription to the stream so its tag matches the server's
 * derivation. Best-effort: without one the tag stays.
 */
function adoptPushTag(): void {
  if (!("serviceWorker" in navigator)) {
    return;
  }
  navigator.serviceWorker.ready
    .then((reg) => reg.pushManager.getSubscription())
    .then((sub) => sse.adoptPushSubscription(sub))
    .catch((err: unknown) => {
      console.debug("sse: push subscription not adopted for the presence tag", err);
    });
}

function onLoginSuccess(): void {
  hideLoginModal();
  // The post-auth fan-out the signed-out boot held back; guarded, so a repeat is a no-op.
  initPostAuth();
  void resolveIdentity().then((v) => {
    // Only signed_in writes the row: a whoami timeout right after a login must not blank the
    // sidebar and read as a sign-out.
    if (v.state === "signed_in") {
      renderIdentity(v);
    }
  });
  // RESETS a live boot loop rather than being refused by it: a login is exactly the new
  // information that may have fixed the read.
  void fetchCatalog({ reset: true });
  if (getSessions().length === 0) {
    // DETACHED: nothing below reads it, and `markBootDone()` must not wait on a round
    // trip — it only flips the flag that lets view swaps animate.
    void createSession();
  }
  // The unauthenticated boot path returns before applyInitialRoute(), so flip the boot
  // flag here too.
  markBootDone();
}

// Input handling

function setupInput(): void {
  // Two peers meeting on one element: prompt-input owns behaviour, composer-state per-chat
  // state. Wired here because wiring from prompt-input closes an import cycle.
  initComposerState();
  // Before initPromptInput: its capture listener must see Enter first.
  initSlashMenu(() => {
    openFilePicker();
  });
  initPromptInput(
    (text: string) => {
      // The PROJECTION's active subject, not the store pointer: optimistic closes leave a closed
      // chat's row until confirmed, and the empty state must create rather than send into it.
      if (activeChatRef() === "") {
        // DETACHED, and the prompt rides INSIDE the create: `createSession(text)` sends
        // once the chat exists, so nothing here needs the id.
        void createSession(text);
      } else {
        sendPrompt(text);
      }
    },
    () => {
      // Cancel the active chat's in-flight turn. No-op if nothing running.
      if (getActiveId() === "") {
        return;
      }
      if (!isThinking(getActiveId())) {
        return;
      }
      void cancelTurn.dispatch({ chatID: getActiveId() });
    },
    hasAttachments,
  );

  const doCreate = guardDuplicateActivation(() => {
    // Detached: closing the sidebar does not depend on the chat. The guard absorbs a duplicate
    // dispatch; the create's op id covers a deliberate repeat.
    void createSession();
    $.sidebar.classList.remove("open");
  });
  $.newChatBtn.addEventListener("click", doCreate);
  $.menuToggle.addEventListener("click", () => $.sidebar.classList.toggle("open"));
  $.sidebarClose.addEventListener("click", () => {
    $.sidebar.classList.remove("open");
  });

  // The model switcher owns its button click, popover, queue and outside-click dismissal.
  initModelSwitcher();
  // The empty-chat model picker; only the selection callback is injected. pickModel, so a
  // hero pick PERSISTS like a pill pick. The Retry's promise is returned for picker.ts to
  // announce.
  initModelPicker(pickModel, () => fetchCatalog());
  // The role picker owns the prompt-bar role pill (expand, list, selection).
  initRolePicker();
  // Queued-prompt chips (pending sends buffered while a turn is in flight).
  initPendingSteers();
  // The composer band's live-run rows; a pure projection of the run store.
  initRunBar();
  initChatOptions();
  // The interaction dock takes its host as an argument so a future run tab's bottom bar
  // can host one too.
  mountDecisionDock($.decisionDock);

  // Each card is its trigger's SIBLING (see 15-input.css .pill-slot), so it is looked up
  // by id rather than queried inside the button.
  makeExpandable($.contextIndicator, byId("context-card"));
  // Lazily on open (usage changes slowly and may be rate-limited); the agent-runtime line
  // re-probes /api/health too. `haspopup: "dialog"`: a status panel with one link, not a menu.
  makeExpandable($.accountBtn, $.statusCard, {
    haspopup: "dialog",
    onExpand: () => {
      loadAccountUsage();
      void refreshRuntimeLine();
    },
  });
}

// URL routing

onPopState((route: Route) => {
  void applyRoute(route, "history");
});

/**
 * Redirect a bare printable keystroke with no modifier to the composer, unless focus
 * already sits somewhere that wants keys (a field, the terminal, an open dialog).
 */
function focusComposerOnTyping(e: KeyboardEvent): void {
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // `key.length === 1` excludes Enter, Escape, Tab, the arrows and the F-keys without
  // enumerating them.
  if (e.key.length !== 1) {
    return;
  }
  const active = document.activeElement;
  if (active instanceof HTMLElement) {
    if (
      active instanceof HTMLInputElement ||
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLSelectElement ||
      active.isContentEditable ||
      active.closest("#shell-panel, dialog[open], .wt-root") !== null
    ) {
      return;
    }
  }
  // Only when a transcript is on screen; typing on Settings or the file browser must not
  // yank focus into a composer the user cannot see.
  const chatView = document.getElementById("chat-view");
  if (chatView === null || chatView.classList.contains("hidden")) {
    return;
  }
  const input = $.promptInput;
  if (input.disabled) {
    return;
  }
  // Let the SAME keystroke land in the box: preventing the default and appending by hand
  // would drop dead keys and IME composition.
  input.focus();
}

document.addEventListener("DOMContentLoaded", init);
