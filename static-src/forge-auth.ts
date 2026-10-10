// One section per forge kind, then one for a server whose kind the detect route names. The server keys a
// connection on kind and host, so a second account on the same host replaces the first.

// signal.aborted and generation guards: the value can flip between awaited microtasks though the type says it cannot.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import { apiGetTyped, apiPost, CancellableSlot } from "./api-client.js";
import type { Decoder } from "./validators.js";
import { asObject, decodeArray } from "./validators.js";
import { decodeRepoList } from "./wire/decoders.gen.js";
import { confirm as confirmDialog } from "./confirm.js";
import {
  FORGE_ICONS,
  ICON_CLOSE_UI,
  ICON_EXTERNAL,
  ICON_GLOBE,
  ICON_PLUS_UI,
  ICON_SPINNER,
} from "./icons.js";
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
  updateRepoRow,
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

/** Each connected account's repository listing, by forge ID: what its
 *  collapsible footer counts and lists. */
let lastReposByForge: Record<string, RepoListing> = {};

/** Why an account's last list-wide press (Load more, Clone all, Delete all) fell
 *  short, by forge ID, until the next one. */
const listNotes = new Map<string, string>();

/** Locally-cloned repo names: the cloned count per account and each row's green dot and Trash button. */
let lastLocalNames = new Set<string>();

/**
 * Forge IDs whose repo footer renders expanded on the next paint, so a newly added account lands with its repos
 * visible. Cleared after one paint.
 */
const expandOnNextPaint = new Set<string>();

let oauthByKind: Partial<Record<ForgeKind, boolean>> = {};

/**
 * A row action's failure the row does not carry (a sign-out, or a check never answered), per forge ID. Cleared
 * when the next action on that row starts.
 */
const rowNotes = new Map<string, string>();

/** Row requests in flight, by forge ID and action, so a control rebuilt while its request runs shows that request. */
const rowRequests = new Map<string, Promise<void>>();

/** True when the last /api/forges fetch failed: the effect renders the error UI with Retry instead of the sections. */
let lastForgesError = false;

let lastForgesData: ForgesListResponse | null = null;

/** Stops a stale concurrent renderForgesPanel from overwriting a newer render. */
let renderGen = 0;

/** Every mutation of the panel state above bumps this; the paint effect subscribes to it. */
const stateVersion = signal(0);

function bumpState(): void {
  stateVersion.value = stateVersion.peek() + 1;
}

/** The effect re-acquires #forges-panel on every run, so a tab close and reopen paints into the new root. */
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

let revalidateController: AbortController | null = null;

/** Each renderForgesPanel aborts the previous in-flight fetch. */
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

function manageAccountURL(kind: ForgeKind, host: string): string {
  return FORGE_URLS[kind](host);
}

/**
 * Render the full forges panel. Idempotent; call after every list mutation. `revalidate` (default true) re-probes
 * connected accounts after the first paint, since tokens can be revoked or expire silently. `skipRepos` skips the
 * local-repos fetch so an optimistic in-memory mutation is not overwritten by a stale answer. `fresh` reads every
 * repository list from its forge rather than the server's cache. Resolves false when a read failed, true otherwise,
 * a render a newer one superseded included: the panel names a failed forge listing, and a failed workspace read keeps
 * the clone state the panel last read.
 */
export async function renderForgesPanel(
  opts: { revalidate?: boolean; skipRepos?: boolean; fresh?: boolean } = {},
): Promise<boolean> {
  const root = document.getElementById("forges-panel");
  if (root === null) {
    return true;
  }

  ensurePanelEffect();

  const myGen = ++renderGen;
  const signal = panelSlot.start();

  // Through the shared store: a sign-in or sign-out must reach the sidebar badge and the PRs tab too. It carries no
  // signal on purpose (one consumer navigating away must not abort a fetch others await); the guards below make a
  // stale answer harmless.
  const data = await refreshForges();
  if (signal.aborted || myGen !== renderGen) {
    return true;
  }
  if (data === null) {
    lastForgesError = true;
    bumpState();
    return false;
  }

  // The accounts paint before their repositories are read, so an account with no listing yet shows that read.
  lastForgesError = false;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();

  let localRead = true;
  if (opts.skipRepos !== true) {
    const [localNames, reposByForge] = await Promise.all([
      refreshLocalNames(signal),
      refreshReposByForge(data.forges, signal, opts.fresh === true),
    ]);
    if (signal.aborted || myGen !== renderGen) {
      return true;
    }
    localRead = localNames !== null;
    lastLocalNames = localNames ?? lastLocalNames;
    lastReposByForge = reposByForge;
  } else {
    const reposByForge = await refreshReposByForge(data.forges, signal, opts.fresh === true);
    if (signal.aborted || myGen !== renderGen) {
      return true;
    }
    lastReposByForge = reposByForge;
  }
  bumpState();

  if (opts.revalidate !== false) {
    const ids = data.forges.filter((f) => f.connected).map((f) => f.id);
    if (ids.length > 0) {
      void revalidateInBackground(ids);
    }
  }
  // Every listing was just read, so a stale or unread one is this read failing.
  return localRead && Object.values(lastReposByForge).every((l) => !l.stale && !l.unread);
}

/** The workspace's clone names; null when the read failed. */
async function refreshLocalNames(signal?: AbortSignal): Promise<Set<string> | null> {
  const r = await apiGetTyped("/api/git/repos", decodeLocalReposResponse, signal);
  return r === null ? null : new Set(r.repos.filter((n) => n !== "."));
}

async function refreshReposByForge(
  forges: ConfiguredForge[],
  signal?: AbortSignal,
  fresh = false,
): Promise<Record<string, RepoListing>> {
  const map: Record<string, RepoListing> = {};
  await Promise.all(
    forges
      .filter((f) => f.connected)
      .map(async (f) => {
        const page = await apiGetTyped(
          `/api/forges/${encodeURIComponent(f.id)}/repos${fresh ? "?refresh=1" : ""}`,
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

/** An open add-account slot is a non-keyed sibling, so the reconcile after the probes leaves the user's pane alone. */
async function revalidateInBackground(ids: string[]): Promise<void> {
  // The most recent paint's state wins.
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
  lastLocalNames = localNames ?? lastLocalNames;
  lastReposByForge = reposByForge;
  lastForgesData = data;
  oauthByKind = data.oauth ?? {};
  bumpState();
}

function paintIntoRoot(root: HTMLElement): void {
  if (lastForgesError) {
    paintErrorState(root);
    return;
  }
  if (lastForgesData === null) {
    return;
  }

  const errEl = root.querySelector(":scope > .forge-error");
  errEl?.remove();

  const supportedKinds = ALL_KINDS.filter((k) => lastForgesData!.kinds.includes(k)); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  reconcile(root, [...supportedKinds, OTHER_SERVER], sectionSpec);
}

function paintErrorState(root: HTMLElement): void {
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") !== null) {
      child.remove();
    }
  }
  if (root.querySelector(":scope > .forge-error") !== null) {
    return;
  }
  const retryBtn = el("button", { type: "button", className: "btn-small" }, "Retry");
  retryBtn.addEventListener("click", () => {
    void renderForgesPanel();
  });
  const errDiv = el("div", { className: "forge-error" }, "Failed to load forges.", retryBtn);
  root.appendChild(errDiv);
}

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
    updateRepoRow(li, r, repoDeps);
  },
};

/** The header, list container and slot keep their identity across repaints; the account list is reconciled. */
function buildKindSection(kind: ForgeKind): HTMLElement {
  const badge = el(
    "span",
    { className: `forge-kind-badge forge-kind-${kind}` },
    iconEl(FORGE_ICONS[kind] ?? ""),
  );
  const section = buildSectionShell(kind, badge, kindTitle(kind), (s) => {
    showAddPane(s, kind);
  });

  // Always present, so reconcile has a deterministic mount point.
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
      "aria-expanded": "false",
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

  // The add pane's mount point, a non-keyed sibling that survives reconcile. It sits directly under the header,
  // beside the "+" that opens it: below the list, on a populated section the button read as doing nothing.
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

/** Both the mount body (fresh empty li) and the update (li with stale children). */
function paintAccountRow(li: HTMLElement, a: ConfiguredForge): void {
  li.classList.toggle("forge-account-row-error", !a.connected);

  // The identity and actions repaint apart: a check rewrites the identity's error line, and its button must survive
  // that to show the outcome.
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

  const oldDetails = li.querySelector<HTMLElement>(":scope > .forge-account-repos");
  const listing = lastReposByForge[a.id];
  if (!a.connected) {
    oldDetails?.remove();
  } else if (listing === undefined) {
    if (oldDetails === null) {
      top.after(buildReposPending());
    }
  } else if (oldDetails === null || oldDetails.hasAttribute("data-pending")) {
    const details = buildAccountReposDetails(a, listing);
    if (oldDetails === null) {
      top.after(details);
    } else {
      oldDetails.replaceWith(details);
    }
  } else {
    updateAccountReposDetails(oldDetails, a, listing);
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

/**
 * Which controls a row offers: none needing a request when none can succeed; a new sign-in when only that revives
 * the credential.
 */
type RowState = "unusable" | "reconnect" | "ok";

const CONNECTION_UNUSABLE = "connection_unusable";

function rowState(a: ConfiguredForge): RowState {
  if (a.error_code === CONNECTION_UNUSABLE) {
    return "unusable";
  }
  return a.reconnect_required ? "reconnect" : "ok";
}

function rowErrorText(a: ConfiguredForge): string {
  const reason = a.last_error ?? "";
  if (a.reconnect_required) {
    return "This account needs a new sign-in: the stored credential can no longer be used or renewed. Reconnect to sign in again.";
  }
  if (reason === "") {
    return "";
  }
  if (a.error_kind === "rate_limited") {
    return `The forge is rate limiting this account. ${retryWords(a)}`;
  }
  // A connected row's error is a temporary one: ConfiguredForge.connected owns that.
  if (a.connected) {
    return `The last check hit a temporary problem and runs again automatically. ${reason}`;
  }
  if (a.error_code === "scope_insufficient") {
    return `The token is missing a permission this needs. Add it to the token on the forge, then check again. ${reason}`;
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
    // No identity until the background probe lands (first paint after a sign-in): a skeleton, not the host, which the
    // meta line already shows.
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

/** Registered before `fn` runs, so a repaint `fn` causes builds its control showing the request. */
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

/** A press while the same request runs does nothing. */
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

/** Rejects when the check failed, after the row says why. */
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
  const failure = o.value.error ?? "";
  if (!o.value.connected || failure !== "") {
    throw new Error(failure === "" ? "the check failed" : failure);
  }
}

/** A new sign-in is the one remedy for a credential the server can neither use nor renew. */
function openReconnect(li: HTMLElement, a: ConfiguredForge): void {
  const section = li.closest<HTMLElement>(".forge-kind-section");
  if (section === null) {
    return;
  }
  setPaneOpen(slotOf(section), true);
  showAddPane(section, a.kind, a.host);
}

function buildReposPending(): HTMLElement {
  return el(
    "div",
    { className: "forge-account-repos", "data-pending": "" },
    el(
      "div",
      { className: "forge-account-repos-pending", role: "status" },
      el(
        "span",
        { className: "forge-account-repos-icon", "aria-hidden": "true" },
        iconEl(ICON_SPINNER),
      ),
      el("span", { className: "forge-account-repos-label" }, "Loading repositories…"),
    ),
  );
}

function buildAccountReposDetails(a: ConfiguredForge, l: RepoListing): HTMLElement {
  return buildAccountReposDetailsImpl(a, l, reposRenderDeps);
}

function updateAccountReposDetails(details: HTMLElement, a: ConfiguredForge, l: RepoListing): void {
  updateAccountReposDetailsImpl(details, a, l, reposRenderDeps);
}

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
  connected: onConnected,
};

/**
 * Every supported method is offered stacked: the device sign-in where the kind has one, and the PAT paste for all.
 * A second press on the same `+` closes the pane.
 */
function onAddAccount(section: HTMLElement, showPane: (section: HTMLElement) => void): void {
  const slot = slotOf(section);
  if (slot.dataset["mode"] === "add") {
    closeSlot(slot);
    return;
  }
  setPaneOpen(slot, true);
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
        connected: (id) => {
          onConnected(slot, id);
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

function onConnected(slot: HTMLElement, id: string): void {
  closeSlot(slot);
  expandOnNextPaint.add(id);
  void renderForgesPanel();
}

function closeSlot(slot: HTMLElement): void {
  emptySlot(slot);
  setPaneOpen(slot, false);
}

function setPaneOpen(slot: HTMLElement, open: boolean): void {
  if (open) {
    slot.dataset["mode"] = "add";
  } else {
    delete slot.dataset["mode"];
  }
  const btn = slot.closest(".forge-kind-section")?.querySelector<HTMLElement>("[data-forge-add]");
  if (btn === null || btn === undefined) {
    return;
  }
  btn.setAttribute("aria-expanded", String(open));
  // The tooltip is the visible label, so the name carries the same word (WCAG 2.5.3).
  const label = open ? "Close" : "Add an account";
  btn.setAttribute("aria-label", label);
  btn.setAttribute("data-tooltip", label);
  btn.replaceChildren(iconEl(open ? ICON_CLOSE_UI : ICON_PLUS_UI));
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
