// One section per forge kind, then one for a server whose kind the detect route names. The server keys a
// connection on kind and host, so a second account on the same host replaces the first.

import { apiGetTyped, CancellableSlot } from "./api-client.js";
import type { Decoder } from "./validators.js";
import { asObject, decodeArray } from "./validators.js";
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
import { probeForge, signOut } from "./actions/forge.js";
import {
  ensureListing,
  forgeRows,
  forgeRowsFailed,
  readForges,
  refreshListing,
  readMoreListing,
} from "./forge-store.js";
import { rowStatus, usableStatus } from "./forge-auth-status.js";
import { registerCleanup } from "./actions/index.js";
import { withAsyncFeedback } from "./async-button.js";
import { signal, effect, el, touch } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { abortPoll, endGrantsIn, isDeviceKind, renderDeviceSignIn } from "./forge-auth-oauth.js";
import { renderConnectionTarget } from "./forge-auth-connection.js";
import { renderDetectForm, renderPATForm, type PATFormDeps } from "./forge-auth-pat.js";
import { buildOwnerScopes, updateOwnerScopes } from "./forge-auth-owners.js";
import {
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

/**
 * A row action's outcome the row does not carry (a sign-out, or a check never answered), per forge ID. Cleared
 * when the next action on that row starts.
 */
const rowNotes = new Map<string, string>();

/**
 * A check_busy note's row as it was answered: the note retires when the row moves (the running check's verdict) or
 * after BUSY_NOTE_MS, whichever comes first.
 */
const busyNotes = new Map<string, { row: string }>();

/**
 * A UI fallback set to the server's checkBudget (internal/forges/check.go): a busy note does not outlast an ordinary
 * check, though a slow sign-out can hold the account longer.
 */
const BUSY_NOTE_MS = 20_000;

/** Row requests in flight, by forge ID and action, so a control rebuilt while its request runs shows that request. */
const rowRequests = new Map<string, Promise<void>>();

/** Every mutation of the panel-local state above bumps this; the paint effect subscribes to it and to the store. */
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

/** Each renderForgesPanel aborts the previous in-flight workspace read. */
const panelSlot = new CancellableSlot();

registerCleanup(() => {
  abortPoll();
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
 * Start the panel's paint and read its data again: the rows, the workspace's clone names and, with `fresh`, every
 * usable row's repository list from its forge. Resolves false when a read failed, true otherwise, a render a newer
 * one superseded included.
 */
export async function renderForgesPanel({
  fresh = false,
}: { fresh?: boolean } = {}): Promise<boolean> {
  if (document.getElementById("forges-panel") === null) {
    return true;
  }
  // Issued before anything reads the store, so a first open sends one rows read, not two.
  const rowsRead = readForges();
  const ids = fresh
    ? (forgeRows()?.forges ?? []).filter((f) => usableStatus(f.status)).map((f) => f.id)
    : [];
  ensurePanelEffect();
  const signal = panelSlot.start();
  const [rowsAnswer, localNames, listed] = await Promise.all([
    rowsRead,
    refreshLocalNames(signal),
    Promise.all(ids.map((id) => refreshListing(id))),
  ]);
  if (signal.aborted) {
    return true;
  }
  if (localNames !== null) {
    lastLocalNames = localNames;
    bumpState();
  }
  return rowsAnswer !== null && localNames !== null && listed.every(Boolean);
}

/** The workspace's clone names; null when the read failed. */
async function refreshLocalNames(signal?: AbortSignal): Promise<Set<string> | null> {
  const r = await apiGetTyped("/api/git/repos", decodeLocalReposResponse, signal);
  return r === null ? null : new Set(r.repos.filter((n) => n !== "."));
}

/** Read the page after an account's held one; rejects after the list notes why. */
async function loadMoreRepos(forgeId: string): Promise<void> {
  listNotes.delete(forgeId);
  const failure = await readMoreListing(forgeId);
  if (failure !== undefined) {
    listNotes.set(forgeId, `Could not load more repositories. ${failure}`);
    throw new Error(failure);
  }
}

function paintIntoRoot(root: HTMLElement): void {
  const data = forgeRows();
  if (data === null) {
    if (forgeRowsFailed()) {
      paintErrorState(root);
    }
    return;
  }
  root.querySelector(":scope > .forge-error")?.remove();
  const supportedKinds = ALL_KINDS.filter((k) => data.kinds.includes(k));
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
  return (forgeRows()?.forges ?? []).filter((f) => f.kind === kind);
}

/** Both the mount body (fresh empty li) and the update (li with stale children). */
function paintAccountRow(li: HTMLElement, a: ConfiguredForge): void {
  const usable = usableStatus(a.status);
  const control = rowStatus(a).control;
  li.classList.toggle("forge-account-row-error", !usable);

  // The identity and actions repaint apart: a check rewrites the identity's error line, and its button must survive
  // that to show the outcome.
  let top = li.querySelector<HTMLElement>(":scope > .forge-account-row-top");
  if (top === null) {
    top = el("div", { className: "forge-account-row-top" });
    li.prepend(top);
  }
  retireBusyNote(a);
  swapIfChanged(
    top,
    ":scope > .forge-account-identity",
    [wireSignature(a), rowNotes.get(a.id) ?? ""],
    () => renderAccountIdentity(a),
  );
  // Sign out is bound to the account it was built for, so a new sign-in rebuilds it.
  swapIfChanged(top, ":scope > .forge-account-actions", [control, a.kind, a.host, a.account], () =>
    renderAccountActions(a, li),
  );

  const oldDetails = li.querySelector<HTMLElement>(":scope > .forge-account-repos");
  const listing = usable ? ensureListing(a.id) : undefined;
  if (!usable) {
    oldDetails?.remove();
  } else if (listing === undefined) {
    // A listing held for an earlier sign-in is gone from the store; its rows must not stay on screen.
    if (oldDetails === null) {
      top.after(buildReposPending());
    } else if (!oldDetails.hasAttribute("data-pending")) {
      oldDetails.replaceWith(buildReposPending());
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
  if (control === "check") {
    if (oldOwners === null) {
      li.appendChild(buildOwnerScopes(a));
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

function renderAccountIdentity(a: ConfiguredForge): HTMLElement {
  const primary = el("span", { className: "forge-account-primary" });
  const hasEmail = a.email !== undefined && a.email !== "";
  const hasUsername = a.username !== undefined && a.username !== "";
  if (hasEmail || hasUsername) {
    primary.textContent = hasEmail ? a.email! : a.username!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  } else {
    // Only a row whose credential is missing or whose store cannot be read names no one, and nothing will until a
    // new sign-in or a repaired store replaces the row: no loading placeholder. The meta line shows the host.
    primary.textContent = "Unknown account";
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
  const st = rowStatus(a);
  if (st.line !== "") {
    id.appendChild(
      el(
        "span",
        { className: st.tone === "muted" ? "forge-account-note" : "forge-account-error" },
        st.line,
      ),
    );
  }
  const note = rowNotes.get(a.id) ?? "";
  if (note !== "") {
    id.appendChild(el("span", { className: "forge-account-error" }, note));
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

  const control = rowStatus(a).control;
  if (control === "none") {
    return actions;
  }
  if (control === "reconnect") {
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
    const key = join(a.id, "probe");
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
    void onSignOut(a.id, a.account, out);
  });
  adoptRowRequest(out, join(a.id, "sign out"));
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

function setRowNote(id: string, note: string): void {
  busyNotes.delete(id);
  if (note === "") {
    rowNotes.delete(id);
  } else {
    rowNotes.set(id, note);
  }
  bumpState();
}

/**
 * Rejects when the check failed, after the row says why. The two 409s are answers, not failures: `check_busy` names
 * the check already running, and `reconnect_required` means the page had not yet applied the row's new status.
 */
async function onProbe(id: string): Promise<void> {
  setRowNote(id, "");
  const o = await probeForge.dispatch({ forgeId: id }).outcome;
  if (o.status === "cancelled") {
    return;
  }
  if (o.status === "error") {
    if (o.error.code === "check_busy") {
      noteBusy(id, o.error.message);
      return;
    }
    if (o.error.code === "reconnect_required") {
      setRowNote(id, o.error.message);
      await readForges();
      return;
    }
    setRowNote(id, `Could not check the connection. ${o.error.message}`);
    throw new Error(o.error.message);
  }
  await readForges();
  const forge = o.value.forge;
  if (forge !== undefined && forge.status !== "connected") {
    throw new Error(forge.error?.message ?? "the check failed");
  }
}

function noteBusy(id: string, note: string): void {
  setRowNote(id, note);
  const row = rowById(id);
  const held = { row: row === undefined ? "" : wireSignature(row) };
  busyNotes.set(id, held);
  setTimeout(() => {
    if (busyNotes.get(id) === held) {
      setRowNote(id, "");
    }
  }, BUSY_NOTE_MS);
}

/** Drops a check_busy note once the row it was answered on has moved. */
function retireBusyNote(a: ConfiguredForge): void {
  const held = busyNotes.get(a.id);
  if (held !== undefined && held.row !== wireSignature(a)) {
    busyNotes.delete(a.id);
    rowNotes.delete(a.id);
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
  const deviceKind = isDeviceKind(kind) && forgeRows()?.oauth?.[kind] === true ? kind : null;
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

/** Signs out `account` from connection `id`; a confirm the connection's next sign-in overtook sends nothing. */
async function onSignOut(id: string, account: string, btn: HTMLButtonElement): Promise<void> {
  const pressed = rowById(id);
  const label =
    [pressed?.email, pressed?.username].find((s) => s !== undefined && s !== "") ??
    pressed?.host ??
    id;
  const ok = await confirmDialog(
    `Sign out of ${label}? Its stored token will be removed.`,
    "Sign out",
    "destructive",
  );
  if (!ok) {
    return;
  }
  // The DELETE removes whichever account the connection holds when it lands.
  const now = rowById(id);
  if (now === undefined) {
    return;
  }
  if (now.account !== account) {
    setRowNote(
      id,
      "This connection was signed in to another account while you confirmed, so nothing was signed out.",
    );
    return;
  }
  runRowRequest(btn, join(id, "sign out"), async () => {
    setRowNote(id, "");
    const o = await signOut.dispatch({ forgeId: id }).outcome;
    if (o.status === "cancelled") {
      return;
    }
    // A 5xx can follow a committed removal, so the rows are read whatever the answer.
    void readForges();
    if (o.status === "error") {
      setRowNote(id, `Could not sign out. ${o.error.message}`);
      throw new Error(o.error.message);
    }
  });
}

function rowById(id: string): ConfiguredForge | undefined {
  return forgeRows()?.forges.find((f) => f.id === id);
}
