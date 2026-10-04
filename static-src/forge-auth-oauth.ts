// ---------------------------------------------------------------------------
// OAuth device sign-in (GitHub, GitLab): the start state, the device prompt,
// polling, and Cancel. Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { pollUntil } from "./actions/index.js";
import { apiPostTyped } from "./api-client.js";
import { withAsyncFeedback } from "./async-button.js";
import type { ConnectionOptions, DeviceKind } from "./actions/forge.js";
import { cancelDeviceFlow, startDeviceFlow } from "./actions/forge.js";
import { DEFAULT_HOST, kindTitle } from "./forge-types.js";
import type { DeviceFlowResponse, ForgeKind } from "./wire/types.gen.js";
import { decodePollResult } from "./wire/decoders.gen.js";

export function isDeviceKind(kind: ForgeKind): kind is DeviceKind {
  return kind === "github" || kind === "gitlab";
}

export interface DeviceSignInTarget {
  readonly kind: DeviceKind;
  /** The pane's host field; another host than the public one asks for a client id. */
  readonly hostInput: HTMLInputElement;
  readonly options: () => ConnectionOptions;
}

export interface OAuthFlowDeps {
  /** Mark a forge ID for expansion on next paint. */
  expandOnNextPaint: (id: string) => void;
  /** Trigger a full panel re-render. */
  renderForgesPanel: () => void;
}

interface LiveGrant {
  poll: AbortController;
  kind: DeviceKind;
  grantId: string;
}

/** Every live grant by the element its prompt renders in. */
const grants = new Map<HTMLElement, LiveGrant>();

/** Abort every in-flight poll. Called from cleanup. */
export function abortPoll(): void {
  for (const g of grants.values()) {
    g.poll.abort();
  }
  grants.clear();
}

/** End every grant whose prompt sits inside `root`, before `root`'s subtree is
 *  removed: its poll stops and the server forgets the grant, once each. */
export function endGrantsIn(root: HTMLElement): void {
  for (const [body, g] of grants) {
    if (root.contains(body)) {
      grants.delete(body);
      g.poll.abort();
      void cancelDeviceFlow.dispatch({ kind: g.kind, grantId: g.grantId });
    }
  }
}

const POLL_MAX_ATTEMPTS = 60;
const POLL_BACKOFF_CAP_SEC = 60;
const POLL_MIN_INTERVAL_SEC = 5;

const NOTE: Record<DeviceKind, string> = {
  github:
    "An organization that restricts OAuth apps shows its repositories only once it approves Marotte. Until it does, connect with a token instead.",
  gitlab:
    "A GitLab sign-in lasts two hours unless the server also issues a refresh token, and then asks to be reconnected. For a longer-lived connection, connect with a token instead.",
};

/** Render the start state into `body`: the client id field another host
 *  needs, the Sign in button, and the family's note. */
export function renderDeviceSignIn(
  body: HTMLElement,
  target: DeviceSignInTarget,
  deps: OAuthFlowDeps,
): void {
  const clientIdInput = el("input", {
    type: "text",
    name: "client_id",
    className: "tool-form-input",
    autocomplete: "off",
  }) as HTMLInputElement;
  // A wrapper with no display rule of its own, so `hidden` hides it.
  const clientIdField = el(
    "div",
    { "data-forge-client-id": "" },
    el(
      "label",
      { className: "tool-form-label" },
      "OAuth client ID",
      clientIdInput,
      el(
        "span",
        { className: "tool-form-hint" },
        "This server signs in with its own OAuth application. Enter the client ID its administrator registered.",
      ),
    ),
  );
  const status = el("div", { className: "forge-card-status", "aria-live": "polite" });
  const start = el(
    "button",
    { type: "button", className: "btn-small btn-primary", "data-forge-device-start": "" },
    `Sign in with ${kindTitle(target.kind)}`,
  ) as HTMLButtonElement;

  // Ended when a grant replaces this state, so Cancel's re-render does not
  // stack a second listener on the same host field.
  const listening = new AbortController();
  const onPublicHost = (): boolean => target.hostInput.value.trim() === DEFAULT_HOST[target.kind];
  const syncClientId = (): void => {
    clientIdField.hidden = onPublicHost();
  };
  target.hostInput.addEventListener("input", syncClientId, { signal: listening.signal });
  syncClientId();

  start.addEventListener("click", () => {
    const host = target.hostInput.value.trim();
    const clientId = onPublicHost() ? "" : clientIdInput.value.trim();
    if (host === "") {
      setLine(status, "Enter the server's host.", "err");
      return;
    }
    if (!onPublicHost() && clientId === "") {
      setLine(status, "Enter the OAuth client ID for this server.", "err");
      return;
    }
    setLine(status, "");
    const req = { kind: target.kind, host, clientId, options: target.options() };
    void withAsyncFeedback(
      start,
      async () => {
        const o = await startDeviceFlow.dispatch(req).outcome;
        if (o.status === "cancelled") {
          return;
        }
        if (o.status === "error") {
          setLine(status, o.error.message, "err");
          throw new Error(o.error.message);
        }
        listening.abort();
        if (body.isConnected) {
          beginGrant(body, target, host, o.value, deps);
        }
      },
      { keepLabel: true },
    );
  });

  body.replaceChildren(
    el(
      "div",
      { className: "forge-device-start" },
      clientIdField,
      start,
      el("p", { className: "forge-help" }, NOTE[target.kind]),
      status,
    ),
  );
}

function beginGrant(
  body: HTMLElement,
  target: DeviceSignInTarget,
  host: string,
  start: DeviceFlowResponse,
  deps: OAuthFlowDeps,
): void {
  const poll = new AbortController();
  const grant: LiveGrant = { poll, kind: target.kind, grantId: start.grant_id };
  grants.set(body, grant);
  const forget = (): void => {
    if (grants.get(body) === grant) {
      grants.delete(body);
    }
  };
  const cancel = renderDevicePrompt(body, start);
  cancel.addEventListener("click", () => {
    void withAsyncFeedback(
      cancel,
      async () => {
        poll.abort();
        forget();
        const o = await cancelDeviceFlow.dispatch({ kind: target.kind, grantId: start.grant_id })
          .outcome;
        if (o.status === "success") {
          renderDeviceSignIn(body, target, deps);
          return;
        }
        if (o.status === "error") {
          const line = body.querySelector(".forge-device-status");
          if (line !== null) {
            line.textContent = `Could not cancel the sign-in. ${o.error.message}`;
          }
          throw new Error(o.error.message);
        }
      },
      { keepLabel: true },
    );
  });
  void pollDevice(body, target.kind, host, start, poll.signal, deps).finally(forget);
}

/** Render the device prompt (verification link, user code, copy button,
 *  status line, Cancel) into `host` and return the Cancel button. Built with
 *  the `el()` factory so no untrusted value is ever parsed as HTML. */
export function renderDevicePrompt(
  host: HTMLElement,
  start: DeviceFlowResponse,
): HTMLButtonElement {
  // Only render an anchor for http(s) URIs; any other scheme (or a
  // markup-injection payload) is shown as inert text. el() turns
  // strings into text nodes, never markup, so there is no XSS surface.
  const safeLink = /^https?:\/\//i.test(start.verification_uri);
  const uriNode: HTMLElement | string = safeLink
    ? el(
        "a",
        {
          className: "forge-device-link",
          target: "_blank",
          rel: "noreferrer",
          href: start.verification_uri,
        },
        start.verification_uri,
      )
    : start.verification_uri;
  const intro = el("p", null, "Open ", uriNode, " and enter:");

  const copyBtn = el(
    "button",
    { type: "button", className: "btn-small forge-copy-btn" },
    "Copy",
  ) as HTMLButtonElement;
  copyBtn.addEventListener("click", () => {
    void navigator.clipboard.writeText(start.user_code);
    copyBtn.textContent = "Copied";
    setTimeout(() => {
      copyBtn.textContent = "Copy";
    }, 2000);
  });
  const codeRow = el(
    "div",
    { className: "forge-device-code-row" },
    el("code", { className: "forge-device-code" }, start.user_code),
    copyBtn,
  );

  const status = el(
    "div",
    { className: "forge-device-status", "aria-live": "polite" },
    "Waiting for approval…",
  );
  const cancel = el(
    "button",
    { type: "button", className: "btn-small", "data-forge-device-cancel": "" },
    "Cancel",
  ) as HTMLButtonElement;

  host.replaceChildren(
    el(
      "div",
      { className: "forge-device-prompt" },
      intro,
      codeRow,
      status,
      el("div", { className: "forge-device-actions" }, cancel),
    ),
  );
  return cancel;
}

async function pollDevice(
  host: HTMLElement,
  kind: DeviceKind,
  forgeHost: string,
  start: DeviceFlowResponse,
  signal: AbortSignal,
  deps: OAuthFlowDeps,
): Promise<void> {
  const statusEl = host.querySelector<HTMLDivElement>(".forge-device-status");
  // pollUntil has no host concept; the caller aborts `signal` on host
  // teardown, and every status write is also guarded by host.isConnected
  // so a detached node is never touched.
  const setStatus = (text: string): void => {
    if (host.isConnected && statusEl !== null) {
      statusEl.textContent = text;
    }
  };

  const outcome = await pollUntil(
    (s) =>
      apiPostTyped(
        `/api/forges/oauth/${kind}/poll`,
        { grant_id: start.grant_id },
        decodePollResult,
        s,
      ),
    {
      intervalMs: Math.max(start.interval, POLL_MIN_INTERVAL_SEC) * 1000,
      // complete / expired / denied / error are terminal; "pending" keeps polling.
      until: (r) => r.status !== "pending",
      maxAttempts: POLL_MAX_ATTEMPTS,
      backoff: { factor: 2, maxMs: POLL_BACKOFF_CAP_SEC * 1000 },
      // A null poll result is a network error: surface it, then back off.
      onTransientError: () => {
        setStatus("Network error. Retrying…");
      },
      signal,
    },
  );

  if (outcome.status === "aborted") {
    return;
  }
  if (outcome.status === "timeout") {
    setStatus("Timed out waiting for approval. Try again.");
    return;
  }
  const res = outcome.result;
  if (res.status === "complete") {
    setStatus("Connected.");
    deps.expandOnNextPaint(`${kind}:${forgeHost}`);
    deps.renderForgesPanel();
    return;
  }
  if (res.status === "expired") {
    setStatus("Device code expired. Try again.");
    return;
  }
  if (res.status === "error" || res.status === "denied") {
    setStatus(`Error: ${res.error ?? "unknown"}`);
  }
}

function setLine(status: HTMLElement, text: string, kind: "ok" | "err" | "" = ""): void {
  status.textContent = text;
  status.className = kind === "" ? "forge-card-status" : `forge-card-status ${kind}`;
}
