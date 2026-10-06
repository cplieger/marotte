// Shared modal system on @cplieger/ui-primitives' createModal (native <dialog>).
// Each `[data-modal]` content element is pre-authored in index.html; createModal
// wraps it in a <dialog> and owns focus, Escape, backdrop dismiss and scroll-lock.

import { createModal, type ModalController } from "@cplieger/ui-primitives/modal";
import { el } from "@cplieger/reactive";
import { pollUntil, registerCleanup } from "./actions/index.js";
import { $, byId } from "./dom.js";
import { apiGetTyped, apiPost } from "./api-client.js";
import { decodeLoginOptions, decodeWhoamiResponse } from "./wire/decoders.gen.js";
import type { LoginOptions } from "./wire/types.gen.js";
import { isSafeUrl } from "./utils-url.js";

const controllers = new Map<HTMLElement, ModalController>();
/** Open order; closeTopModal closes the last. */
const openStack: HTMLElement[] = [];
const closeCallbacks = new Map<HTMLElement, Set<() => void>>();

/** Run `fn` after `modal` finishes closing via ANY path (button, backdrop,
 *  Escape, programmatic). Safe to call before the modal is initialised. */
export function onModalClose(modal: HTMLDivElement, fn: () => void): void {
  let set = closeCallbacks.get(modal);
  if (set === undefined) {
    set = new Set();
    closeCallbacks.set(modal, set);
  }
  set.add(fn);
}

function handleModalClosed(content: HTMLElement): void {
  const i = openStack.lastIndexOf(content);
  if (i !== -1) {
    openStack.splice(i, 1);
  }
  const cbs = closeCallbacks.get(content);
  if (cbs !== undefined) {
    for (const fn of cbs) {
      fn();
    }
  }
}

/** Idempotent, so an openModal racing ahead of initAllModals still works. */
function ensureModal(content: HTMLElement): ModalController {
  const existing = controllers.get(content);
  if (existing !== undefined) {
    return existing;
  }
  const ctrl = createModal(content, {
    onClose: () => {
      handleModalClosed(content);
    },
  });
  // Authored `.hidden` so it cannot flash in <body> before createModal wraps it;
  // the <dialog> owns visibility from here.
  content.classList.remove("hidden");
  controllers.set(content, ctrl);
  wireCloseButtons(content, ctrl);
  return ctrl;
}

function wireCloseButtons(content: HTMLElement, ctrl: ModalController): void {
  for (const btn of content.querySelectorAll('.modal-header-row .icon-btn[aria-label="Close"]')) {
    btn.addEventListener("click", () => {
      ctrl.close();
    });
  }
}

/** Auto-wire every `[data-modal]` element. Optional: openModal initialises lazily. */
export function initAllModals(): void {
  for (const content of document.querySelectorAll<HTMLElement>("[data-modal]")) {
    ensureModal(content);
  }
}

const EXPAND_HINT =
  '<svg class="output-expand-hint" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>';

// Parsed once; each append imports a copy instead of re-running DOMParser.
// eslint-disable-next-line @typescript-eslint/no-non-null-assertion
const EXPAND_HINT_NODE = new DOMParser().parseFromString(EXPAND_HINT, "text/html").body.firstChild!;

/** Manages a rolling output bar that shows the last 4 lines and expands to a modal on click. */
export class RollingOutput {
  private full = "";
  private readonly bar: HTMLDivElement;
  private readonly modalId: string;

  constructor(barEl: HTMLDivElement, modalId: string) {
    this.bar = barEl;
    this.modalId = modalId;
    barEl.addEventListener("click", () => {
      this.openModal();
    });
  }

  clear(): void {
    this.full = "";
    this.bar.classList.add("hidden");
  }

  /** Keeps the last 4 SOURCE lines in a `.rolling-output-text` child: the child
   *  carries the line clamp because a clip and a padded box cannot be one element.
   *  The expand hint stays a direct child, positioned against the bar. */
  append(text: string): void {
    this.full += (this.full !== "" ? "\n" : "") + text;
    const lines = this.full.split("\n").filter((l) => l.trim() !== "");
    const view = el("div", { className: "rolling-output-text" }, lines.slice(-4).join("\n"));
    this.bar.replaceChildren(view, document.importNode(EXPAND_HINT_NODE, true));
    this.bar.classList.remove("hidden");
  }

  getText(): string {
    return this.full;
  }

  private openModal(): void {
    const modal = byId<HTMLDivElement>(this.modalId);
    const body = modal.querySelector(".subagent-modal-body, pre")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
    if (body !== null) {
      body.textContent = this.full;
      body.scrollTop = body.scrollHeight;
    }
    openModal(modal);
  }
}

/** Open a modal by its content element. */
export function openModal(modal: HTMLDivElement): void {
  const ctrl = ensureModal(modal);
  if (!openStack.includes(modal)) {
    openStack.push(modal);
  }
  ctrl.open();
}

/** Close a modal by its content element. No-op if it was never opened. */
export function closeModal(modal: HTMLDivElement): void {
  controllers.get(modal)?.close();
}

/** Close the topmost open modal; true if one closed. Idempotent, so it coexists
 *  with the platform's own Escape handling. */
export function closeTopModal(): boolean {
  for (let i = openStack.length - 1; i >= 0; i--) {
    const content = openStack[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const ctrl = controllers.get(content);
    if (ctrl?.isOpen === true) {
      ctrl.close();
      return true;
    }
  }
  return false;
}

let loginPollAbort: AbortController | null = null;
let loginPollUnregister: (() => void) | null = null;

function abortLoginPoll(): void {
  loginPollAbort?.abort();
  loginPollAbort = null;
  loginPollUnregister?.();
  loginPollUnregister = null;
}

export function showLoginModal(): void {
  openModal($.loginModal);
  void apiGetTyped<LoginOptions>("/api/login/options", decodeLoginOptions).then((opts) => {
    if (opts !== null) {
      applyLoginOptions(opts);
    }
  });
}

/** Offer only the sign-in doors the administrator permits and pre-fill the start
 *  URL. An unreadable answer offers both, as kiro-cli does for a missing file. */
export function applyLoginOptions(opts: LoginOptions): void {
  byId<HTMLButtonElement>("modal-login-free").classList.toggle("hidden", !opts.builder_id);
  byId<HTMLButtonElement>("modal-login-sso").classList.toggle("hidden", !opts.idc);
  const provider = byId<HTMLInputElement>("modal-provider");
  const region = byId<HTMLInputElement>("modal-region");
  if (provider.value === "" && opts.idc_start_url !== undefined) {
    provider.value = opts.idc_start_url;
  }
  if (region.value === "" && opts.idc_region !== undefined) {
    region.value = opts.idc_region;
  }
  if (opts.idc && !opts.builder_id) {
    byId<HTMLDivElement>("modal-sso-form").classList.remove("hidden");
  }
}

export function hideLoginModal(): void {
  closeModal($.loginModal);
}

export function initLoginModal(onLoggedIn: () => void): void {
  // Every login-modal close path must stop the poll; with no Close button, all
  // dismissals funnel through the controller's onClose.
  onModalClose($.loginModal, abortLoginPoll);

  const freeBtn = byId<HTMLButtonElement>("modal-login-free");
  const ssoBtn = byId<HTMLButtonElement>("modal-login-sso");
  const ssoForm = byId<HTMLDivElement>("modal-sso-form");
  const ssoSubmit = byId<HTMLButtonElement>("modal-sso-submit");
  const providerInput = byId<HTMLInputElement>("modal-provider");
  const regionInput = byId<HTMLInputElement>("modal-region");
  const status = byId<HTMLDivElement>("modal-status");

  freeBtn.addEventListener("click", () => {
    ssoForm.classList.add("hidden");
    status.textContent = "Connecting...";
    doLogin({}, status, onLoggedIn);
  });
  ssoBtn.addEventListener("click", () => {
    ssoForm.classList.toggle("hidden");
    status.textContent = "";
  });

  const submit = (): void => {
    // The server's validateProvider requires https; users paste bare hosts.
    let provider = providerInput.value.trim();
    if (provider !== "" && !/^https?:\/\//i.test(provider)) {
      provider = "https://" + provider;
      providerInput.value = provider;
    }
    if (provider === "") {
      status.textContent = "Start URL is required";
      return;
    }
    status.textContent = "Connecting...";
    doLogin({ provider, region: regionInput.value.trim() || undefined }, status, onLoggedIn);
  };

  ssoSubmit.addEventListener("click", submit);

  // No <form> wraps these, so Enter is wired by hand.
  const onEnter = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      submit();
    }
  };
  providerInput.addEventListener("keydown", onEnter);
  regionInput.addEventListener("keydown", onEnter);
}

function doLogin(
  body: Record<string, string | undefined>,
  status: HTMLDivElement,
  onLoggedIn: () => void,
): void {
  loginPollAbort?.abort();
  loginPollAbort = null;

  const btns = document.querySelectorAll("#login-modal .modal-btn");
  for (const b of btns) {
    (b as HTMLButtonElement).disabled = true;
  }

  interface LoginResp {
    url?: string;
    code?: string;
    error?: string;
    raw?: string;
  }
  const enable = (): void => {
    for (const b of btns) {
      (b as HTMLButtonElement).disabled = false;
    }
  };

  void apiPost<LoginResp>("/api/login", body)
    .then((d) => {
      if (d === null) {
        status.textContent = "Server error";
        return;
      }
      if (d.error !== undefined) {
        // kiro-cli refuses a fresh login while a session exists (usually a whoami parse
        // miss); reload so checkAuthAndStart re-runs with the tolerant parser.
        if (d.error === "already_logged_in") {
          status.textContent = "";
          status.append("You're already signed in. ");
          const reloadBtn = el("button", { type: "button", className: "btn-small" }, "Reload");
          reloadBtn.style.marginInlineStart = "var(--sp-2)";
          reloadBtn.addEventListener("click", () => {
            location.reload();
          });
          status.append(reloadBtn);
          return;
        }
        const detail = d.raw !== undefined ? `\n\nCLI output:\n${d.raw}` : "";
        status.textContent = d.error + detail;
        return;
      }
      if (d.url !== undefined) {
        const codeText = d.code !== undefined ? `Code: ${d.code}` : "";
        status.textContent = "";
        if (codeText) {
          status.append(codeText);
          status.append(el("br"));
        }
        if (isSafeUrl(d.url)) {
          const link = el(
            "a",
            { href: d.url, target: "_blank", rel: "noopener" },
            "Open login page",
          );
          link.style.color = "var(--c-accent)";
          status.append(link);
        } else {
          const span = el("span", null, d.url);
          span.style.color = "var(--c-text-tertiary)";
          status.append(span);
        }
        status.append(el("br"));
        const hint = el("span", null, "Complete login in the browser, then come back.");
        hint.style.color = "var(--c-text-tertiary)";
        status.append(hint);
        const MAX_POLL_ATTEMPTS = 200; // ~10 minutes at 3s intervals
        const ctrl = new AbortController();
        loginPollAbort = ctrl;
        loginPollUnregister?.();
        loginPollUnregister = registerCleanup(() => loginPollAbort?.abort());
        const signal = AbortSignal.any([
          ctrl.signal,
          AbortSignal.timeout(MAX_POLL_ATTEMPTS * 3000),
        ]);
        void (async () => {
          // Poll until signed_in, a dismiss, or the deadline. `signed_out` and
          // `unavailable` keep polling: both are expected mid-window.
          const outcome = await pollUntil(
            (s) => apiGetTyped("/api/whoami", decodeWhoamiResponse, s),
            {
              intervalMs: 3000,
              until: (wd) => wd.state === "signed_in",
              signal,
            },
          );
          if (outcome.status === "done") {
            loginPollAbort = null;
            loginPollUnregister?.(); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
            loginPollUnregister = null;
            onLoggedIn();
            return;
          }
          if (ctrl.signal.aborted) {
            return;
          }
          loginPollUnregister?.(); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
          loginPollUnregister = null;
          status.textContent = "Login timed out. Please reload and try again.";
        })();
      }
    })
    .finally(enable);
}
