// DOM element registry, queried lazily and failing fast on a missing id. Modal-local ids stay in their own modules.

// A hidden surface (a background tab's view, a closed modal) is PARKED in a windowless document: off the page, so a
// page-wide walk does not pay for it (a password manager's form scan reads every field's `labels`, and Gecko
// populates each by walking the whole document), yet still connected, so a binding that ends when its element
// leaves the document keeps running. Every lookup below searches the parked surfaces too.
let hold: Document | null = null;
const places = new WeakMap<Element, Comment>();
/** Elements that scrolled since their surface last parked; their offsets are read only when it parks. */
const scrolled = new Set<Element>();
const parkedScroll = new WeakMap<Element, readonly (readonly [Element, number, number])[]>();
let watchingScroll = false;

/** The parked surfaces still owed a place on the page; one whose placeholder left with its container is dropped. */
function parked(): Document | null {
  if (hold !== null) {
    for (const surface of [...hold.body.children]) {
      if (places.get(surface)?.isConnected !== true) {
        surface.remove();
      }
    }
  }
  return hold;
}

function noteScroll(e: Event): void {
  if (e.target instanceof Element) {
    scrolled.add(e.target);
  }
}

/** Look up an element by id; throws if missing. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- caller uses T for inference: el<HTMLInputElement>("id")
export function byId<T extends HTMLElement>(id: string): T {
  const e = maybeEl<T>(id);
  if (e === null) {
    throw new Error(`Missing element: #${id}`);
  }
  return e;
}

/** Like byId, but null when absent. T is the call-site contract for the element subclass. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- T is the call-site contract for the returned element subclass
export function maybeEl<T extends HTMLElement>(id: string): T | null {
  return (document.getElementById(id) ?? parked()?.getElementById(id) ?? null) as T | null;
}

/** The page's first element matching `selector`, or null. T is the call-site contract for the subclass. */
// eslint-disable-next-line @typescript-eslint/no-unnecessary-type-parameters -- T is the call-site contract for the returned element subclass
export function queryEl<T extends Element = Element>(selector: string): T | null {
  return document.querySelector<T>(selector) ?? parked()?.querySelector<T>(selector) ?? null;
}

/** Every element of the page matching `selector`: the shown page in document order, then the parked surfaces. */
export function queryAll<T extends Element = Element>(selector: string): T[] {
  const found = [...document.querySelectorAll<T>(selector)];
  const off = parked();
  if (off !== null) {
    found.push(...off.querySelectorAll<T>(selector));
  }
  return found;
}

/** The roots a page-wide walk covers: the document, then the parked surfaces. */
export function pageRoots(): ParentNode[] {
  const off = parked();
  return off === null ? [document] : [document, off];
}

/** Park a hidden surface, keeping its place for `unpark`. A no-op for one already parked or off the page, and for
 *  one holding playing media, which leaving the document would pause (HTML "media element removed"). Reads the
 *  offsets of what scrolled inside it, so call it before the writes that hide it. */
export function park(surface: HTMLElement): void {
  if (surface.ownerDocument !== document || !surface.isConnected || playsMedia(surface)) {
    return;
  }
  if (!watchingScroll) {
    watchingScroll = true;
    document.addEventListener("scroll", noteScroll, { capture: true, passive: true });
  }
  const offsets: (readonly [Element, number, number])[] = [];
  for (const t of scrolled) {
    if (!t.isConnected || t.ownerDocument !== document) {
      scrolled.delete(t);
    } else if (surface.contains(t)) {
      scrolled.delete(t);
      if (t.scrollTop !== 0 || t.scrollLeft !== 0) {
        offsets.push([t, t.scrollTop, t.scrollLeft]);
      }
    }
  }
  parkedScroll.set(surface, offsets);
  const place = document.createComment(surface.id);
  places.get(surface)?.remove();
  places.set(surface, place);
  surface.replaceWith(place);
  hold ??= document.implementation.createHTMLDocument("");
  hold.body.append(surface);
}

/** Put a parked surface back in its place, run `show`, then restore its scroll offsets, which a hidden box cannot
 *  take. Runs `show` alone for a surface that is not parked. */
export function unpark(surface: HTMLElement, show: () => void): void {
  const place = places.get(surface);
  if (place !== undefined) {
    places.delete(surface);
    if (surface.ownerDocument === document) {
      place.remove();
    } else {
      place.replaceWith(surface);
    }
  }
  show();
  for (const [t, top, left] of parkedScroll.get(surface) ?? []) {
    t.scrollTop = top;
    t.scrollLeft = left;
  }
  parkedScroll.delete(surface);
}

function playsMedia(surface: HTMLElement): boolean {
  return [...surface.querySelectorAll<HTMLMediaElement>("audio, video")].some((m) => !m.paused);
}

/** Set `name` to `value`, or remove it for null, writing nothing when it already holds that: a same-value write still
 *  queues mutation records and, for a selector-read attribute, a restyle. */
export function setAttr(node: Element, name: string, value: string | null): void {
  if (value === null) {
    if (node.hasAttribute(name)) {
      node.removeAttribute(name);
    }
  } else if (node.getAttribute(name) !== value) {
    node.setAttribute(name, value);
  }
}

/** Set the custom property `name` on `node`'s inline style, writing nothing when it already holds `value`. */
export function setStyleVar(node: HTMLElement, name: string, value: string): void {
  if (node.style.getPropertyValue(name) !== value) {
    node.style.setProperty(name, value);
  }
}

/** Set `node`'s text, writing nothing when it already reads that: assigning `textContent` replaces the text node. A null
 *  node writes nothing. */
export function setText(node: Node | null, text: string): void {
  if (node !== null && node.textContent !== text) {
    node.textContent = text;
  }
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
  /** `status.ts` writes its `data-tooltip`; `app.ts` makes it expandable. */
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
  /** The fold row's slot above the run list, outside the list's scroll clip. */
  get runBarFold(): HTMLElement {
    return byId("run-bar-fold");
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
  /** The sign-in rows and their separator; `settings.ts`'s `setSignInRows` is their one writer. */
  get stSignInSep(): HTMLElement {
    return byId("st-signin-sep");
  }
  get stSignInMethod(): HTMLElement {
    return byId("st-signin-method");
  }
  get stSignInURL(): HTMLElement {
    return byId("st-signin-url");
  }
  get stSignInRegion(): HTMLElement {
    return byId("st-signin-region");
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
  get editorViewer(): HTMLDivElement {
    return byId("editor-viewer");
  }
  get editorEditGutter(): HTMLDivElement {
    return byId("editor-edit-gutter");
  }
  get editorNotice(): HTMLDivElement {
    return byId("editor-notice");
  }
  get editorReadonlyLabel(): HTMLElement {
    return byId("editor-readonly-label");
  }
  get editorLiveBtn(): HTMLButtonElement {
    return byId("editor-live-btn");
  }
  get editorLiveReason(): HTMLElement {
    return byId("editor-live-reason");
  }
  get editorDownloadBtn(): HTMLButtonElement {
    return byId("editor-download-btn");
  }
  get editorGotoBtn(): HTMLButtonElement {
    return byId("editor-goto-btn");
  }
  get editorGoto(): HTMLFormElement {
    return byId("editor-goto");
  }
  get editorGotoInput(): HTMLInputElement {
    return byId("editor-goto-input");
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
  get webOpenBtn(): HTMLAnchorElement {
    return byId("web-open-btn");
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
  get clockSelect(): HTMLSelectElement {
    return byId("clock-choice");
  }

  get chatArea(): HTMLElement {
    return byId("chat-area");
  }
}

export const $ = new Elements();
