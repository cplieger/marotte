// ---------------------------------------------------------------------------
// Forge authentication UI: one always-visible section per forge kind, then one
// for a server whose kind the detect route names. The add pane opens ABOVE a
// section's account list, next to the "+" that asked for it. The server keys a
// connection on kind and host, so a second account on the same host replaces
// the first; accounts on different hosts coexist.
// ---------------------------------------------------------------------------

// signal.aborted / generation-counter defensive guards: the value can flip
// between awaited microtasks even though the type system sees it as an
// always-defined boolean.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import { apiGetTyped, apiPost, CancellableSlot } from "./api-client.js";
import type { Decoder } from "./validators.js";
import { asObject, decodeArray } from "./validators.js";
import { decodeRepoList } from "./wire/decoders.gen.js";
import { confirm as confirmDialog } from "./confirm.js";
import { FORGE_ICONS, ICON_EXTERNAL, ICON_GLOBE, ICON_PLUS_UI } from "./icons.js";
import type { ConfiguredForge, ForgeKind, Repo } from "./wire/types.gen.js";
import { FORGE_META, FORGE_URLS, kindTitle } from "./forge-types.js";
import { listRepoPage, probeForge, signOut } from "./actions/forge.js";
import { refreshForges, type ForgesListResponse } from "./forge-store.js";
import { registerCleanup } from "./actions/index.js";
import { withAsyncFeedback } from "./async-button.js";
import { signal, effect, el, touch } from "@cplieger/reactive";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { abortPoll, endGrantsIn, isDeviceKind, renderDeviceSignIn } from "./forge-auth-oauth.js";
import { renderConnectionTarget } from "./forge-auth-connection.js";
import { renderDetectForm, renderPATForm, type PATFormDeps } from "./forge-auth-pat.js";
import { buildOwnerScopes, updateOwnerScopes, type OwnerScopesDeps } from "./forge-auth-owners.js";
import {
  readFirstPage,
  readNextPage,
  renderRepoRow,
  renderRepoState,
  renderRepoIdentity,
  renderRepoActions,
  type RepoDeps,
  type RepoListing,
} from "./forge-auth-repos.js";
import {
  buildAccountReposDetails as buildAccountReposDetailsImpl,
  updateAccountReposDetails as updateAccountReposDetailsImpl,
  type ReposRenderDeps,
} from "./forge-auth-repos-render.js";

interface LocalReposResponse {
  repos: string[];
}

// --- Response decoders ------------------------------------------------

const decodeLocalReposResponse: Decoder<LocalReposResponse> = (v) => {
  const o = asObject(v, "$.local_repos");
  return {
    repos: decodeArray(
      o["repos"],
      (el) => {
        if (typeof el !== "string") {
          throw new TypeError(`expected string, got ${typeof el}`);
        }
        return el;
      },
      "$.local_repos.repos",
    ),
  };
};

import { iconEl } from "./icon-el.js";

// --- Module state -----------------------------------------------------

/** Each connected account's repository listing, by forge ID: what its
 *  collapsible footer counts and lists. */
let lastReposByForge: Record<string, RepoListing> = {};

/** Why an account's last list-wide press (Load more, Clone all, Delete all) fell
 *  short, by forge ID, until the next one. */
const listNotes = new Map<string, string>();

/** Names of locally-cloned repos. Used to compute the cloned-count
 *  per-account and to drive the green dot / Trash button per row. */
let lastLocalNames = new Set<string>();

/** Forge IDs whose collapsible footer should render expanded on the
 *  next paint. Populated when the user successfully adds an account
 *  (PAT submit or OAuth complete) so the user lands on the freshly-
 *  added account with its repos visible. Cleared after one paint. */
const expandOnNextPaint = new Set<string>();

/** OAuth availability per kind, populated from the forges list response. */
let oauthByKind: Partial<Record<ForgeKind, boolean>> = {};

/** A row action's failure the row itself does not carry, per forge ID: a
 *  sign-out or a check the server never answered. Cleared when the next
 *  action on that row starts. */
const rowNotes = new Map<string, string>();

/** Row requests in flight, keyed by forge ID and action (a repository row's by
 *  its own key), so a control rebuilt while its request runs shows that request
 *  rather than a fresh button. */
const rowRequests = new Map<string, Promise<void>>();

/** True when the last /api/forges fetch failed; the effect renders an
 *  error UI with a Retry button instead of the kind sections. */
let lastForgesError = false;

/** Last-known forges payload. Effect paints from this when non-null
 *  and `lastForgesError` is false. */
let lastForgesData: ForgesListResponse | null = null;

/** Generation counter to prevent stale concurrent renderForgesPanel
 *  calls from overwriting a newer render. */
let renderGen = 0;

/** Monotonic state-version signal. Every mutation to lastForgesData /
 *  lastReposByForge / lastLocalNames / lastForgesError bumps this; the
 *  paint effect subscribes to it and reconciles the panel. */
const stateVersion = signal(0);

function bumpState(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** Lazy-initialized paint effect. First renderForgesPanel call sets it
 *  up; subsequent calls are no-ops. The effect runs on every bumpState
 *  and re-acquires #forges-panel each run, so tab close/reopen is
 *  handled transparently (the next bump after re-mounting paints into
 *  the new root). */
let panelEffectStarted = false;
function ensurePanelEffect(): void {
  if (panelEffectStarted) {
    return;
  }
  panelEffectStarted = true;
  effect(() => {
    touch(stateVersion);
    const root = document.getElementById("forges-panel");
    if (root === null) {
      return;
    }
    paintIntoRoot(root);
  });
}

// --- In-flight handles for cancel-on-navigate -------------------------

/** AbortController for the background revalidation probes. */
let revalidateController: AbortController | null = null;

/** CancellableSlot for the primary renderForgesPanel fetch path.
 *  Each call aborts the previous in-flight fetch. */
const panelSlot = new CancellableSlot();

registerCleanup(() => {
  abortPoll();
  revalidateController?.abort();
  panelSlot.abort();
});

const ALL_KINDS = Object.keys(FORGE_META) as ForgeKind[];

/** The section for a server whose forge kind nobody has named. */
const OTHER_SERVER = "other";
type SectionKey = ForgeKind | typeof OTHER_SERVER;

/** Manage-account URL on the forge itself, parameterized by host. */
function manageAccountURL(kind: ForgeKind, host: string): string {
  return FORGE_URLS[kind](host);
}

/** Render the full forges panel. Idempotent; call after every list
 *  mutation to refresh.
 *
 *  When `revalidate` is true (the default), connected accounts are
 *  re-probed in parallel after the initial paint. Tokens can be
 *  silently revoked or expire; this catches that on page open. The
 *  initial paint shows last-known state immediately; the panel
 *  re-renders once when all probes have settled.
 *
 *  When `skipRepos` is true, the local-repos fetch is skipped so that
 *  an optimistic in-memory mutation (e.g. removeLocalRepo) is not
 *  overwritten by a stale server response before the action completes. */
export async function renderForgesPanel(
  opts: { revalidate?: boolean; skipRepos?: boolean } = {},
): Promise<void> {
  const root = document.getElementById("forges-panel");
  if (root === null) {
    return;
  }

  ensurePanelEffect();

  const myGen = ++renderGen;
  const signal = panelSlot.start();

  // Through the shared store rather than this module's own fetch: a sign-in or a
  // sign-out has to reach the sidebar badge and the PRs tab too, which a private
  // copy could never do. It carries no signal, deliberately — see forge-store.ts:
  // one consumer navigating away must not abort a fetch two others await. The
  // guards below are what make a stale answer harmless here.
  const data = await refreshForges();
  if (signal.aborted || myGen !== renderGen) {
    return;
  }
  if (data === null) {
    lastForgesError = true;
    bumpState();
    return;
  }

  // Refresh repo + local-clone caches in parallel with the forges
  // list. The per-account collapsible footer renders from these.
  // When skipRepos is true (optimistic updates), skip the local-repos
  // fetch so the caller's in-memory mutation isn't overwritten.
  if (opts.skipRepos !== true) {
    const [localNames, reposByForge] = await Promise.all([
      refreshLocalNames(signal),
      refreshReposByForge(data.forges, signal),
    ]);
    if (signal.aborted || myGen !== renderGen) {
      return;
    }
    lastLocalNames = localNames;
    lastReposByForge = reposByForge;
  } else {
    const reposByForge = await refreshReposByForge(data.forges, signal);
    if (signal.aborted || myGen !== renderGen) {
      return;
    }
    lastReposByForge = reposByForge;
  }

  lastForgesError = false;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();

  if (opts.revalidate !== false) {
    const ids = data.forges.filter((f) => f.connected).map((f) => f.id);
    if (ids.length > 0) {
      void revalidateInBackground(ids);
    }
  }
}

async function refreshLocalNames(signal?: AbortSignal): Promise<Set<string>> {
  const r = await apiGetTyped("/api/git/repos", decodeLocalReposResponse, signal);
  return new Set((r?.repos ?? []).filter((n) => n !== "."));
}

async function refreshReposByForge(
  forges: ConfiguredForge[],
  signal?: AbortSignal,
): Promise<Record<string, RepoListing>> {
  const map: Record<string, RepoListing> = {};
  await Promise.all(
    forges
      .filter((f) => f.connected)
      .map(async (f) => {
        const page = await apiGetTyped(
          `/api/forges/${encodeURIComponent(f.id)}/repos`,
          decodeRepoList,
          signal,
        );
        map[f.id] = readFirstPage(lastReposByForge[f.id], page);
      }),
  );
  return map;
}

/** Read the page after an account's held one into its listing. A listing that
 *  moved on while the page was read keeps its own state. */
async function loadMoreRepos(forgeId: string): Promise<void> {
  const held = lastReposByForge[forgeId];
  if (held === undefined || held.next === "") {
    return;
  }
  listNotes.delete(forgeId);
  const o = await listRepoPage.dispatch({ forgeId, after: held.next }).outcome;
  if (o.status === "cancelled") {
    return;
  }
  if (o.status === "error") {
    listNotes.set(forgeId, `Could not load more repositories. ${o.error.message}`);
    throw new Error(o.error.message);
  }
  const now = lastReposByForge[forgeId];
  if (now?.next === held.next) {
    lastReposByForge[forgeId] = readNextPage(now, o.value);
  }
}

/** Re-probe every connected account in parallel; on completion, re-fetch
 *  /api/forges and bump state with the post-probe results. The paint
 *  effect surgically reconciles the panel; an open add-account slot is
 *  preserved as a non-keyed sibling so the user's mid-interaction is
 *  not disrupted. */
async function revalidateInBackground(ids: string[]): Promise<void> {
  // Cancel any prior in-flight revalidation; we always want the most
  // recent paint's state to win.
  revalidateController?.abort();
  revalidateController = new AbortController();
  const signal = revalidateController.signal;
  const myGen = renderGen;
  await Promise.allSettled(
    ids.map((id) => apiPost(`/api/forges/${encodeURIComponent(id)}/probe`, {}, signal)),
  );
  if (signal.aborted) {
    return;
  }
  const data = await refreshForges();
  if (signal.aborted) {
    return;
  }
  if (data === null || data === undefined) {
    return;
  }
  const [localNames, reposByForge] = await Promise.all([
    refreshLocalNames(signal),
    refreshReposByForge(data.forges, signal),
  ]);
  if (signal.aborted) {
    return;
  }
  if (myGen !== renderGen) {
    return;
  }
  lastLocalNames = localNames;
  lastReposByForge = reposByForge;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();
}

// --- Effect-driven paint ----------------------------------------------

/** Called by the paint effect on every state change. Reconciles the
 *  panel root to match the latest forges/repos/local-names state. */
function paintIntoRoot(root: HTMLElement): void {
  if (lastForgesError) {
    paintErrorState(root);
    return;
  }
  if (lastForgesData === null) {
    return;
  }

  // Clear any leftover error UI before reconciling kind sections.
  const errEl = root.querySelector(":scope > .forge-error");
  errEl?.remove();

  const supportedKinds = ALL_KINDS.filter((k) => lastForgesData!.kinds.includes(k)); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  reconcile(root, [...supportedKinds, OTHER_SERVER], sectionSpec);
}

function paintErrorState(root: HTMLElement): void {
  // Remove existing keyed kind sections first; the error UI stands alone.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") !== null) {
      child.remove();
    }
  }
  if (root.querySelector(":scope > .forge-error") !== null) {
    return;
  } // already shown
  const retryBtn = el("button", { type: "button", className: "btn-small" }, "Retry");
  retryBtn.addEventListener("click", () => {
    void renderForgesPanel();
  });
  const errDiv = el("div", { className: "forge-error" }, "Failed to load forges.", retryBtn);
  root.appendChild(errDiv);
}

// --- Reconcile specs --------------------------------------------------

const sectionSpec: ReconcileSpec<SectionKey> = {
  key: (k) => k,
  mount: (k) => (k === OTHER_SERVER ? buildOtherSection() : buildKindSection(k)),
  update: (el, k) => {
    if (k !== OTHER_SERVER) {
      updateKindSection(el, k);
    }
  },
};

const accountSpec: ReconcileSpec<ConfiguredForge> = {
  key: (a) => a.id,
  mount: (a) => {
    const li = el("li", { className: "forge-account-row", "data-id": a.id });
    paintAccountRow(li, a);
    return li;
  },
  update: (li, a) => {
    paintAccountRow(li, a);
  },
};

const repoSpec: ReconcileSpec<Repo> = {
  key: (r) => r.repo_id,
  mount: (r) => renderRepoRow(r, repoDeps),
  update: (li, r) => {
    const cloned = lastLocalNames.has(r.name);
    li.querySelector(":scope > .forge-account-repo-state")?.replaceWith(renderRepoState(cloned));
    li.querySelector(":scope > .forge-account-repo-identity")?.replaceWith(renderRepoIdentity(r));
    li.querySelector(":scope > .forge-account-repo-actions")?.replaceWith(
      renderRepoActions(r, cloned, repoDeps),
    );
  },
};

// --- Kind section -----------------------------------------------------

/** Build a fresh section element for one forge kind. The header,
 *  account list container, and slot are static (their identity is
 *  preserved across re-paints); the account list is reconciled on
 *  every update. */
function buildKindSection(kind: ForgeKind): HTMLElement {
  const badge = el(
    "span",
    { className: `forge-kind-badge forge-kind-${kind}` },
    iconEl(FORGE_ICONS[kind] ?? ""),
  );
  const section = buildSectionShell(kind, badge, kindTitle(kind), (s) => {
    showAddPane(s, kind);
  });

  // Always present an account list container so reconcile has a
  // deterministic mount point. Empty list renders nothing.
  const list = el("ul", { className: "forge-account-list" });
  section.appendChild(list);
  reconcile(list, accountsForKind(kind), accountSpec);

  return section;
}

/** The section for a server whose kind is not known. It holds no accounts: a
 *  connection made here lands in the section of the kind the server answered. */
function buildOtherSection(): HTMLElement {
  const badge = el("span", { className: "forge-kind-badge" }, iconEl(ICON_GLOBE));
  return buildSectionShell(OTHER_SERVER, badge, "Another server", showDetectPane);
}

/** A section's header (badge, title, the "+" that toggles the add pane) and
 *  its add-pane slot. */
function buildSectionShell(
  key: SectionKey,
  badge: HTMLElement,
  title: string,
  showPane: (section: HTMLElement) => void,
): HTMLElement {
  const section = el("section", { className: "forge-kind-section", "data-kind": key });

  const addBtn = el(
    "button",
    {
      type: "button",
      className: "btn-small forge-kind-add-btn",
      "data-forge-add": key,
      "aria-label": "Add an account",
      "data-tooltip": "Add an account",
    },
    iconEl(ICON_PLUS_UI),
  );
  addBtn.addEventListener("click", () => {
    onAddAccount(section, showPane);
  });

  section.appendChild(
    el(
      "header",
      { className: "forge-kind-header" },
      badge,
      el("h3", { className: "forge-kind-title" }, title),
      addBtn,
    ),
  );

  // Inline mount point for the add-account pane (OAuth + PAT) and
  // status messages. Non-keyed sibling — survives reconcile.
  //
  // It sits DIRECTLY under the header, above the account list, because
  // the "+" that opens it is in that header. Below the list, the pane
  // opened one account row plus one expanded repo list away from the
  // click that asked for it, so on a kind that already has an account
  // the button read as doing nothing. Same rule the knowledge-base add
  // form follows (`knowledge-list`.before(form)).
  section.appendChild(el("div", { className: "forge-kind-slot", "data-forge-slot": key }));
  return section;
}

function updateKindSection(section: HTMLElement, kind: ForgeKind): void {
  const list = section.querySelector<HTMLElement>(":scope > .forge-account-list");
  if (list === null) {
    return;
  }
  reconcile(list, accountsForKind(kind), accountSpec);
}

function accountsForKind(kind: ForgeKind): ConfiguredForge[] {
  if (lastForgesData === null) {
    return [];
  }
  return lastForgesData.forges.filter((f) => f.kind === kind);
}

// --- Account row ------------------------------------------------------

/** Paint or repaint the contents of one account <li>. Used both as
 *  the spec's mount body (li freshly created, empty) and as update
 *  (li already in DOM, may have stale children). */
function paintAccountRow(li: HTMLElement, a: ConfiguredForge): void {
  li.classList.toggle("forge-account-row-error", !a.connected);

  // The identity and the actions repaint apart: a check rewrites the identity's
  // error line, and its button has to survive that to show the outcome.
  let top = li.querySelector<HTMLElement>(":scope > .forge-account-row-top");
  if (top === null) {
    top = el("div", { className: "forge-account-row-top" });
    li.prepend(top);
  }
  swapIfChanged(
    top,
    ":scope > .forge-account-identity",
    [wireSignature(a), rowNotes.get(a.id) ?? ""],
    () => renderAccountIdentity(a),
  );
  swapIfChanged(top, ":scope > .forge-account-actions", [rowState(a), a.kind, a.host], () =>
    renderAccountActions(a, li),
  );

  // Repos details (only when connected and we have repo data).
  const oldDetails = li.querySelector<HTMLElement>(":scope > .forge-account-repos");
  const listing = lastReposByForge[a.id];
  if (a.connected && listing !== undefined) {
    if (oldDetails === null) {
      top.after(buildAccountReposDetails(a, listing));
    } else {
      updateAccountReposDetails(oldDetails, a, listing);
    }
  } else {
    oldDetails?.remove();
  }

  const oldOwners = li.querySelector<HTMLElement>(":scope > .forge-account-owners");
  if (rowState(a) === "ok") {
    if (oldOwners === null) {
      li.appendChild(buildOwnerScopes(a, ownerDeps));
    } else {
      updateOwnerScopes(oldOwners, a);
    }
  } else {
    oldOwners?.remove();
  }
}

/** Replace the child `selector` names, or add one, when `parts` moved. */
function swapIfChanged(
  host: HTMLElement,
  selector: string,
  parts: readonly string[],
  build: () => HTMLElement,
): void {
  const old = host.querySelector<HTMLElement>(selector);
  if (old !== null && !sigChanged(old, parts)) {
    return;
  }
  const fresh = build();
  sigChanged(fresh, parts);
  if (old === null) {
    host.appendChild(fresh);
  } else {
    old.replaceWith(fresh);
  }
}

/** Which controls a row can offer: none that need a request when no request
 *  can succeed, a new sign-in when only that revives the credential. */
type RowState = "unusable" | "reconnect" | "ok";

const CONNECTION_UNUSABLE = "connection_unusable";

function rowState(a: ConfiguredForge): RowState {
  if (a.error_code === CONNECTION_UNUSABLE) {
    return "unusable";
  }
  return a.reconnect_required ? "reconnect" : "ok";
}

/** The row's error in words, by the code the server gave it. */
function rowErrorText(a: ConfiguredForge): string {
  const reason = a.last_error ?? "";
  if (a.reconnect_required) {
    return "This account needs a new sign-in: the stored credential can no longer be used or renewed. Reconnect to sign in again.";
  }
  if (a.connected || reason === "") {
    return "";
  }
  if (a.error_code === "scope_insufficient") {
    return `The token is missing a permission this needs. Add it to the token on the forge, then check again. ${reason}`;
  }
  if (a.error_kind === "rate_limited") {
    return `The forge is rate limiting this account. ${retryWords(a)}`;
  }
  return reason;
}

/** The wait a rate limit asked for, counted from the probe that met it. */
function retryWords(a: ConfiguredForge): string {
  if (a.retry_after_s === undefined || a.retry_after_s <= 0) {
    return "Try again later.";
  }
  const since = a.last_probed === undefined ? 0 : Date.now() - a.last_probed;
  const left = Math.ceil((a.retry_after_s * 1000 - since) / 1000);
  if (left <= 0) {
    return "It can be checked again now.";
  }
  return `Try again in ${left} second${left === 1 ? "" : "s"}.`;
}

function renderAccountIdentity(a: ConfiguredForge): HTMLElement {
  const primary = el("span", { className: "forge-account-primary" });
  const hasEmail = a.email !== undefined && a.email !== "";
  const hasUsername = a.username !== undefined && a.username !== "";
  if (hasEmail || hasUsername) {
    primary.textContent = hasEmail ? a.email! : a.username!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  } else {
    // No identity data yet (this is the first paint right after a
    // PAT submit / OAuth complete; the background probe hasn't
    // populated email or username yet). Show a skeleton bar instead
    // of falling back to the host string — the host is already shown
    // on the meta line below.
    primary.classList.add("skeleton", "forge-account-primary-skeleton");
    primary.setAttribute("aria-label", "Loading account identity…");
  }

  const meta = el("span", { className: "forge-account-meta" });
  const parts: string[] = [];
  if (hasEmail && hasUsername) {
    parts.push("@" + a.username!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  if (a.host !== "") {
    parts.push(a.host);
  }
  meta.textContent = parts.join(" · ");

  const id = el("div", { className: "forge-account-identity" }, primary, meta);
  for (const text of [rowErrorText(a), rowNotes.get(a.id) ?? ""]) {
    if (text !== "") {
      id.appendChild(el("span", { className: "forge-account-error" }, text));
    }
  }
  return id;
}

function renderAccountActions(a: ConfiguredForge, li: HTMLElement): HTMLElement {
  const actions = el("div", { className: "forge-account-actions" });
  const manageURL = manageAccountURL(a.kind, a.host);
  if (manageURL !== "") {
    const manageLabel = el("span", null, "Manage");
    const manage = el(
      "a",
      {
        href: manageURL,
        target: "_blank",
        rel: "noreferrer",
        className: "btn-small forge-account-manage",
        "data-tooltip": "Manage account on forge",
        "aria-label": "Manage account on forge",
      },
      manageLabel,
      iconEl(ICON_EXTERNAL),
    );
    actions.appendChild(manage);
  }

  const state = rowState(a);
  if (state === "unusable") {
    return actions;
  }
  if (state === "reconnect") {
    const reconnect = el(
      "button",
      { type: "button", className: "btn-small btn-primary" },
      "Reconnect",
    ) as HTMLButtonElement;
    reconnect.addEventListener("click", () => {
      openReconnect(li, a);
    });
    actions.appendChild(reconnect);
  } else {
    const check = el(
      "button",
      {
        type: "button",
        className: "btn-small",
        "aria-label": "Check the connection",
        "data-tooltip": "Check the connection",
      },
      "Check",
    ) as HTMLButtonElement;
    const key = `${a.id} probe`;
    check.addEventListener("click", () => {
      runRowRequest(check, key, () => onProbe(a.id));
    });
    adoptRowRequest(check, key);
    actions.appendChild(check);
  }

  const out = el(
    "button",
    { type: "button", className: "btn-small btn-danger" },
    "Sign out",
  ) as HTMLButtonElement;
  out.addEventListener("click", () => {
    void onSignOut(a, out);
  });
  adoptRowRequest(out, `${a.id} sign out`);
  actions.appendChild(out);
  return actions;
}

/** Start the row request `key` unless it already runs, answering it. It is
 *  registered before `fn` runs, so a repaint `fn` causes builds its control
 *  showing the request. */
function startRowRequest(key: string, fn: () => Promise<void>): Promise<void> | undefined {
  if (rowRequests.has(key)) {
    return undefined;
  }
  let settle: (outcome: Promise<void>) => void = () => undefined;
  const p = new Promise<void>((resolve) => {
    settle = resolve;
  });
  rowRequests.set(key, p);
  const clear = (): void => {
    rowRequests.delete(key);
  };
  void p.then(clear, clear);
  settle(fn());
  return p;
}

/** Run one row request with its button's feedback; a press while the same
 *  request runs does nothing. */
function runRowRequest(btn: HTMLButtonElement, key: string, fn: () => Promise<void>): void {
  const p = startRowRequest(key, fn);
  if (p !== undefined) {
    void withAsyncFeedback(btn, () => p, { keepLabel: true });
  }
}

function adoptRowRequest(btn: HTMLButtonElement, key: string): void {
  const p = rowRequests.get(key);
  if (p !== undefined) {
    void withAsyncFeedback(btn, () => p, { keepLabel: true });
  }
}

/** Replace one row of the held list with the server's copy and repaint. */
function replaceRow(f: ConfiguredForge): void {
  if (lastForgesData === null) {
    return;
  }
  lastForgesData = {
    ...lastForgesData,
    forges: lastForgesData.forges.map((x) => (x.id === f.id ? f : x)),
  };
  bumpState();
}

function setRowNote(id: string, note: string): void {
  if (note === "") {
    rowNotes.delete(id);
  } else {
    rowNotes.set(id, note);
  }
  bumpState();
}

/** Check a connection; the answer is the row as the probe left it. Rejects
 *  when the check failed, after the row says why. */
async function onProbe(id: string): Promise<void> {
  setRowNote(id, "");
  const o = await probeForge.dispatch({ forgeId: id }).outcome;
  if (o.status === "cancelled") {
    return;
  }
  if (o.status === "error") {
    setRowNote(id, `Could not check the connection. ${o.error.message}`);
    throw new Error(o.error.message);
  }
  if (o.value.forge !== undefined) {
    replaceRow(o.value.forge);
  }
  if (!o.value.connected) {
    throw new Error(o.value.error ?? "the check failed");
  }
}

/** Open the add pane of `a`'s kind for its host: a new sign-in is the one
 *  remedy for a credential the server can neither use nor renew. */
function openReconnect(li: HTMLElement, a: ConfiguredForge): void {
  const section = li.closest<HTMLElement>(".forge-kind-section");
  if (section === null) {
    return;
  }
  slotOf(section).dataset["mode"] = "add";
  showAddPane(section, a.kind, a.host);
}

// --- Account repos (details) ------------------------------------------

function buildAccountReposDetails(a: ConfiguredForge, l: RepoListing): HTMLElement {
  return buildAccountReposDetailsImpl(a, l, reposRenderDeps);
}

function updateAccountReposDetails(details: HTMLElement, a: ConfiguredForge, l: RepoListing): void {
  updateAccountReposDetailsImpl(details, a, l, reposRenderDeps);
}

// --- Repo row ---------------------------------------------------------
// --- Deps wiring for extracted modules --------------------------------

const repoDeps: RepoDeps = {
  isCloned: (name) => lastLocalNames.has(name),
  addCloned: (name) => {
    lastLocalNames.add(name);
  },
  removeCloned: (name) => {
    lastLocalNames.delete(name);
  },
  bumpState,
  start: startRowRequest,
  running: (key) => rowRequests.get(key),
};

const reposRenderDeps: ReposRenderDeps = {
  get lastLocalNames() {
    return lastLocalNames;
  },
  expandOnNextPaint,
  bumpState,
  repoDeps,
  repoSpec,
  loadMore: loadMoreRepos,
  listNotes,
};

const ownerDeps: OwnerScopesDeps = {
  stored: (id, owners) => {
    const f = lastForgesData?.forges.find((x) => x.id === id);
    if (f !== undefined) {
      replaceRow({ ...f, owner_scopes: [...owners] });
    }
  },
};

const patDeps: PATFormDeps = {
  closeSlot,
  expandOnNextPaint: (id) => {
    expandOnNextPaint.add(id);
  },
  renderForgesPanel: () => {
    void renderForgesPanel();
  },
};

// --- Add-account flow dispatch ---

/** Toggle the add-account pane on a forge section. The pane offers
 *  every supported method stacked, so the user picks one without
 *  flipping between buttons: the device sign-in where the kind has one,
 *  and the PAT paste for every kind. Clicking the same `+` button twice
 *  closes the pane. */
function onAddAccount(section: HTMLElement, showPane: (section: HTMLElement) => void): void {
  const slot = slotOf(section);
  if (slot.dataset["mode"] === "add") {
    closeSlot(slot);
    return;
  }
  slot.dataset["mode"] = "add";
  showPane(section);
}

/** `host` prefills the server field, as a reconnect does. */
function showAddPane(section: HTMLElement, kind: ForgeKind, host?: string): void {
  const slot = slotOf(section);
  emptySlot(slot);

  const target = renderConnectionTarget(kind);
  if (host !== undefined) {
    target.hostInput.value = host;
  }
  const deviceKind = isDeviceKind(kind) && oauthByKind[kind] === true ? kind : null;
  const pane = el(
    "div",
    { className: "forge-add-pane" },
    el(
      "p",
      { className: "forge-add-pane-intro" },
      deviceKind !== null
        ? "Sign in through your browser, or paste a personal access token. Marotte keeps the credential in its own store and uses it for this forge's API and for git over HTTPS. SSH remotes are not covered."
        : "Paste a personal access token. Marotte keeps it in its own store and uses it for this forge's API and for git over HTTPS. SSH remotes are not covered.",
    ),
    target.el,
  );

  if (deviceKind !== null) {
    const oauthBody = el("div", { className: "forge-add-pane-body" });
    pane.append(
      el(
        "div",
        { className: "forge-add-pane-section" },
        el("h4", { className: "forge-add-pane-heading" }, "Browser-based sign-in"),
        oauthBody,
      ),
      el("hr", { className: "forge-add-pane-divider" }),
    );
    renderDeviceSignIn(
      oauthBody,
      { kind: deviceKind, hostInput: target.hostInput, options: target.options },
      {
        expandOnNextPaint: (id) => {
          expandOnNextPaint.add(id);
        },
        renderForgesPanel: () => {
          void renderForgesPanel();
        },
      },
    );
  }

  const patBody = el("div", { className: "forge-add-pane-body" });
  pane.appendChild(
    el(
      "div",
      { className: "forge-add-pane-section" },
      el("h4", { className: "forge-add-pane-heading" }, "Personal access token"),
      patBody,
    ),
  );

  slot.appendChild(pane);
  renderPATForm(patBody, kind, slot, target, patDeps);
}

function showDetectPane(section: HTMLElement): void {
  const slot = slotOf(section);
  emptySlot(slot);

  const target = renderConnectionTarget(null);
  const body = el("div", { className: "forge-add-pane-body" });
  slot.appendChild(
    el(
      "div",
      { className: "forge-add-pane" },
      el(
        "p",
        { className: "forge-add-pane-intro" },
        "For a server whose forge you do not know. Marotte asks the server whether it runs GitHub, GitLab, Gitea or Forgejo, then connects the token there, and the account appears under that forge. The token stays in Marotte's own store and serves the API and git over HTTPS. SSH remotes are not covered.",
      ),
      target.el,
      el(
        "div",
        { className: "forge-add-pane-section" },
        el("h4", { className: "forge-add-pane-heading" }, "Personal access token"),
        body,
      ),
    ),
  );
  renderDetectForm(body, slot, target, patDeps);
}

function closeSlot(slot: HTMLElement): void {
  emptySlot(slot);
  delete slot.dataset["mode"];
}

/** Empty a slot, ending any device sign-in its pane was waiting on. */
function emptySlot(slot: HTMLElement): void {
  endGrantsIn(slot);
  slot.replaceChildren();
}

function slotOf(section: HTMLElement): HTMLElement {
  const slot = section.querySelector<HTMLElement>("[data-forge-slot]");
  if (slot === null) {
    const div = el("div");
    section.appendChild(div);
    return div;
  }
  return slot;
}

// --- Sign out ---

async function onSignOut(f: ConfiguredForge, btn: HTMLButtonElement): Promise<void> {
  const label = f.email ?? f.username ?? f.host;
  const ok = await confirmDialog(
    `Sign out of ${label}? Its stored token will be removed.`,
    "Sign out",
    "destructive",
  );
  if (!ok) {
    return;
  }
  runRowRequest(btn, `${f.id} sign out`, async () => {
    setRowNote(f.id, "");
    const o = await signOut.dispatch({ forgeId: f.id }).outcome;
    if (o.status === "cancelled") {
      return;
    }
    if (o.status === "error") {
      setRowNote(f.id, `Could not sign out. ${o.error.message}`);
      throw new Error(o.error.message);
    }
    void renderForgesPanel();
  });
}
