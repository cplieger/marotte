// DOM element registry, queried lazily and failing fast on a missing id. Modal-local ids stay in their own modules.

/** Look up an element by id; throws if missing. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- caller uses T for inference: el<HTMLInputElement>("id")
export function byId<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (e === null) {
    throw new Error(`Missing element: #${id}`);
  }
  return e as T;
}

/** Like byId, but null when absent. T is the call-site contract for the element subclass. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- T is the call-site contract for the returned element subclass
export function maybeEl<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

/**
 * Force a synchronous style and layout flush for `el`, so removing and re-adding a class in one task restarts an
 * animation. Returns the value read.
 */
export function forceReflow(el: Element): number {
  return el.getBoundingClientRect().height;
}

/**
 * Mark an element busy or clear it: the only place the value is spelled. `aria-busy` must be the literal `"true"`;
 * `toggleAttribute` writes "", which reads as false.
 */
export function setBusy(el: Element, busy: boolean): void {
  if (busy) {
    el.setAttribute("aria-busy", "true");
  } else {
    el.removeAttribute("aria-busy");
  }
}

/**
 * Mark a control busy: `disabled` and `aria-busy` together, so assistive tech and the busy face (`40-a11y.css`)
 * cannot disagree.
 */
export function setControlBusy(el: HTMLButtonElement | HTMLInputElement, busy: boolean): void {
  el.disabled = busy;
  setBusy(el, busy);
}

// Lazy getters, so the module may be imported before DOMContentLoaded.
class Elements {
  get sidebar(): HTMLElement {
    return byId("sidebar");
  }
  get tabList(): HTMLDivElement {
    return byId("tab-list");
  }
  get newChatBtn(): HTMLButtonElement {
    return byId("new-chat");
  }
  get menuToggle(): HTMLButtonElement {
    return byId("menu-toggle");
  }
  get sidebarClose(): HTMLButtonElement {
    return byId("sidebar-close");
  }
  get sidebarResize(): HTMLDivElement {
    return byId("sidebar-resize");
  }
  get settingsBtn(): HTMLButtonElement {
    return byId("settings-btn");
  }
  /**
   * The footer's identity control and the status popup's trigger. `status.ts` writes its `data-tooltip`; `app.ts`
   * makes it expandable.
   */
  get accountBtn(): HTMLButtonElement {
    return byId("account-btn");
  }
  /** The decorative connection mark inside `#account-btn`; `setStatus` toggles its classes. */
  get statusDot(): HTMLElement {
    return byId("status-dot");
  }
  get userEmail(): HTMLElement {
    return byId("user-email");
  }
  get logoutBtn(): HTMLButtonElement {
    return byId("logout-btn");
  }

  get messages(): HTMLDivElement {
    return byId("messages");
  }
  get messagesWrap(): HTMLDivElement {
    return byId("messages-wrap");
  }
  // The positioned wrapper around the scroller, so the turn map stays put while the transcript scrolls.
  get messagesWrapOuter(): HTMLDivElement {
    return byId("messages-wrap-outer");
  }
  get bannerStack(): HTMLDivElement {
    return byId("banner-stack");
  }
  get scrollBottom(): HTMLButtonElement {
    return byId("scroll-bottom");
  }
  get modelPicker(): HTMLDivElement {
    return byId("model-picker");
  }
  get promptForm(): HTMLFormElement {
    return byId("prompt-form");
  }
  get promptInput(): HTMLTextAreaElement {
    return byId("prompt-input");
  }
  get slashMenu(): HTMLUListElement {
    return byId("slash-menu");
  }
  get attachmentRow(): HTMLUListElement {
    return byId("attachment-row");
  }
  /** The bottom-bar region between the dock and the steer stack: one line per live
   *  workflow run this chat launched. */
  get runBar(): HTMLUListElement {
    return byId("run-bar");
  }
  get steerStack(): HTMLUListElement {
    return byId("steer-stack");
  }
  get sendBtn(): HTMLButtonElement {
    return byId("send-btn");
  }
  get switchModelBtn(): HTMLButtonElement {
    return byId("switch-model-btn");
  }
  get modelSwitchList(): HTMLDivElement {
    return byId("model-switch-list");
  }
  get rolePill(): HTMLButtonElement {
    return byId("role-pill");
  }
  get roleList(): HTMLDivElement {
    return byId("role-list");
  }
  /** The interaction dock's host. */
  get decisionDock(): HTMLDivElement {
    return byId("decision-dock");
  }
  get contextIndicator(): HTMLButtonElement {
    return byId("context-indicator");
  }
  get contextRingFill(): HTMLElement {
    return byId("context-ring-fill");
  }
  get contextRingWedge(): HTMLElement {
    return byId("context-ring-wedge");
  }
  get contextLabel(): HTMLElement {
    return byId("context-label");
  }

  get ctxModelPill(): HTMLElement {
    return byId("ctx-model-pill");
  }
  /** The reasoning tier on the model pill, its own element so the model name keeps the ellipsis. `.hidden` when no tier. */
  get ctxEffortPill(): HTMLElement {
    return byId("ctx-effort-pill");
  }
  get ctxTokens(): HTMLElement {
    return byId("ctx-tokens");
  }
  get ctxCredits(): HTMLElement {
    return byId("ctx-credits");
  }
  get ctxTurns(): HTMLElement {
    return byId("ctx-turns");
  }
  get ctxLastTurn(): HTMLElement {
    return byId("ctx-last-turn");
  }
  get ctxEntries(): HTMLElement {
    return byId("ctx-entries");
  }
  get ctxTools(): HTMLElement {
    return byId("ctx-tools");
  }
  get ctxMetering(): HTMLElement {
    return byId("ctx-metering");
  }

  // The card is the dot's sibling (15-input.css .pill-slot), so status.ts writes --status-color onto the card.
  get statusCard(): HTMLElement {
    return byId("status-card");
  }
  get stWs(): HTMLElement {
    return byId("st-ws");
  }
  get stKiro(): HTMLElement {
    return byId("st-kiro");
  }
  get stAuth(): HTMLElement {
    return byId("st-auth");
  }
  /** Hides with the auth row; `settings.ts`'s `setAuthLine` is the one writer of both. */
  get stAuthSep(): HTMLElement {
    return byId("st-auth-sep");
  }
  get stAccount(): HTMLElement {
    return byId("st-account");
  }
  get acctPlan(): HTMLElement {
    return byId("acct-plan");
  }
  get acctMeter(): HTMLElement {
    return byId("acct-meter");
  }
  get acctOverage(): HTMLElement {
    return byId("acct-overage");
  }

  get steeringInput(): HTMLTextAreaElement {
    return byId("steering-input");
  }
  get toolUpdateBtn(): HTMLButtonElement {
    return byId("tool-update-btn");
  }
  get toolUpdateOutput(): HTMLDivElement {
    return byId("tool-update-output");
  }
  get toolAddBtn(): HTMLButtonElement {
    return byId("tool-add-btn");
  }
  get toolsList(): HTMLDivElement {
    return byId("tools-list");
  }

  get shellPanel(): HTMLDivElement {
    return byId("shell-panel");
  }
  get shellBtn(): HTMLButtonElement {
    return byId("shell-btn");
  }
  get shellToggleBtn(): HTMLButtonElement {
    return byId("shell-toggle-btn");
  }
  get shellRestartBtn(): HTMLButtonElement {
    return byId("shell-restart-btn");
  }
  get shellKeysBtn(): HTMLButtonElement {
    return byId("shell-keys-btn");
  }
  get shellFullscreenBtn(): HTMLButtonElement {
    return byId("shell-fullscreen-btn");
  }
  get shellTerminal(): HTMLDivElement {
    return byId("shell-terminal");
  }
  get shellResize(): HTMLDivElement {
    return byId("shell-resize");
  }

  get gitBtn(): HTMLButtonElement {
    return byId("git-btn");
  }
  get gitBadge(): HTMLElement {
    return byId("git-badge");
  }

  get filesBtn(): HTMLButtonElement {
    return byId("files-btn");
  }
  get fbList(): HTMLDivElement {
    return byId("fb-list");
  }
  get fbBack(): HTMLButtonElement {
    return byId("fb-back");
  }
  get fbForward(): HTMLButtonElement {
    return byId("fb-forward");
  }
  get fbPath(): HTMLInputElement {
    return byId("fb-path");
  }
  get fbUpload(): HTMLButtonElement {
    return byId("fb-upload");
  }
  get fbDownload(): HTMLButtonElement {
    return byId("fb-download");
  }
  get fbNewFile(): HTMLButtonElement {
    return byId("fb-new-file");
  }
  get fbNewFolder(): HTMLButtonElement {
    return byId("fb-new-folder");
  }
  get fbAddToChat(): HTMLButtonElement {
    return byId("fb-add-to-chat");
  }
  get chatOptionsBtn(): HTMLButtonElement {
    return byId("chat-options-btn");
  }
  get chatOptionsCard(): HTMLElement {
    return byId("chat-options-card");
  }
  get fbRename(): HTMLButtonElement {
    return byId("fb-rename");
  }
  get fbDelete(): HTMLButtonElement {
    return byId("fb-delete");
  }
  get fbDropOverlay(): HTMLDivElement {
    return byId("fb-drop-overlay");
  }

  get historyBtn(): HTMLButtonElement {
    return byId("history-btn");
  }
  get historyTabBar(): HTMLElement {
    return byId("history-tab-bar");
  }

  get findBtn(): HTMLButtonElement {
    return byId("find-btn");
  }

  get docsBtn(): HTMLButtonElement {
    return byId("docs-btn");
  }
  get docsView(): HTMLDivElement {
    return byId("docs-view");
  }
  get docsTabBar(): HTMLElement {
    return byId("docs-tab-bar");
  }

  get editorContent(): HTMLTextAreaElement {
    return byId("editor-content");
  }
  get editorHighlight(): HTMLPreElement {
    return byId("editor-highlight");
  }
  get editorCode(): HTMLElement {
    return byId("editor-code");
  }
  get editorGutter(): HTMLPreElement {
    return byId("editor-gutter");
  }
  get editorFilename(): HTMLElement {
    return byId("editor-filename");
  }
  get editorError(): HTMLElement {
    return byId("editor-error");
  }
  get editorEditBtn(): HTMLButtonElement {
    return byId("editor-edit-btn");
  }
  get editorSaveBtn(): HTMLButtonElement {
    return byId("editor-save-btn");
  }
  get editorCancelBtn(): HTMLButtonElement {
    return byId("editor-cancel-btn");
  }
  get editorDiffBtn(): HTMLButtonElement {
    return byId("editor-diff-btn");
  }
  /** Its visibility has one writer, an effect in editor-core.ts. */
  get editorGitDiffBtn(): HTMLButtonElement {
    return byId("editor-git-diff-btn");
  }
  get editorPreviewBtn(): HTMLButtonElement {
    return byId("editor-preview-btn");
  }
  get webView(): HTMLDivElement {
    return byId("web-view");
  }
  get webStage(): HTMLDivElement {
    return byId("web-stage");
  }
  get webPath(): HTMLSpanElement {
    return byId("web-path");
  }
  get webViewport(): HTMLDivElement {
    return byId("web-viewport");
  }
  get webScaleBtn(): HTMLButtonElement {
    return byId("web-scale-btn");
  }
  get webReloadBtn(): HTMLButtonElement {
    return byId("web-reload-btn");
  }
  get webEditBtn(): HTMLButtonElement {
    return byId("web-edit-btn");
  }
  get editorMarkdown(): HTMLDivElement {
    return byId("editor-markdown");
  }
  get editorImage(): HTMLDivElement {
    return byId("editor-image");
  }
  get editorDiffPane(): HTMLDivElement {
    return byId("editor-diff-pane");
  }
  get editorConflictOverlay(): HTMLDivElement {
    return byId("editor-conflict-overlay");
  }
  get loginModal(): HTMLDivElement {
    return byId("login-modal");
  }
  get toolModal(): HTMLDivElement {
    return byId("tool-modal");
  }

  get notifyToggle(): HTMLInputElement {
    return byId("notify-toggle");
  }
  get notifyHint(): HTMLParagraphElement {
    return byId("notify-hint");
  }
  get notifySubOptions(): HTMLDivElement {
    return byId("notify-sub-options");
  }

  get settingsTabBar(): HTMLDivElement {
    return byId("settings-tab-bar");
  }

  get mcpModal(): HTMLDivElement {
    return byId("mcp-modal");
  }

  get uploadProgress(): HTMLDivElement {
    return byId("upload-progress");
  }
  get uploadProgressBar(): HTMLProgressElement {
    return byId("upload-progress-bar");
  }
  get uploadProgressLabel(): HTMLElement {
    return byId("upload-progress-label");
  }
  get uploadProgressCancel(): HTMLButtonElement {
    return byId("upload-progress-cancel");
  }

  get themeBtn(): HTMLButtonElement {
    return byId("theme-btn");
  }
  get pointerModeBtn(): HTMLButtonElement {
    return byId("pointer-mode-btn");
  }

  get appRoot(): HTMLElement {
    return byId("app");
  }
  get chatArea(): HTMLElement {
    return byId("chat-area");
  }
}

export const $ = new Elements();
