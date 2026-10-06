// Per-repo collapsible sections in #git-prs-mount, each listing its open PRs and their actions. Rows are the server's
// inventory (git-prs-state.ts): one GET, then one forge_inventory frame per connection per cycle.

import { join } from "@cplieger/keyenc";

import { apiGetTyped, apiPost } from "./api-client.js";
import { BUS_RECONCILE, onBus, onSSE } from "./bus.js";
import { setBusy } from "./dom.js";
import { isSafeURL } from "./url-safety.js";
import { presentedTag } from "./sse-adapter.js";
import { observeStamp } from "./subject-versions.js";
import { observePRView, VIEW_PAGE } from "./git-prs-watch.js";
import { decodeInventoryList } from "./wire/decoders.gen.js";
import { relativeTime } from "./relative-time.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { openMergeMethodDialog } from "./merge-dialog.js";
import { FORGE_ICONS, ICON_REFRESH, ICON_PR_EMPTY, ICON_FILTER } from "./icons.js";
import { preserveGitScroll } from "./git-scroll.js";
import { flashTarget } from "./flash-target.js";
import { prIdentity } from "./push-subject.js";
import type {
  Affordance,
  ConfiguredForge,
  InventoryEntry,
  InventoryList,
  InventoryScope,
  RepoSuccessor,
} from "./wire/types.gen.js";
import {
  mergePR,
  closePR,
  createPR,
  armAutoMerge,
  reopenPR,
  rerunChecks,
  readCapabilities,
  readMergeStatus,
  refreshPRs as refreshPRsAction,
  requestPRCycle,
  sendCloseOnUnload,
  watchPRView,
} from "./actions/git-prs.js";
import type { PRArgs } from "./actions/git-prs.js";
import {
  checkChip,
  mergeVerdict,
  movedRepository,
  queueText,
  rerunControl,
  rerunRefusal,
  canArmAutoMerge,
  capitalise,
} from "./git-pr-status.js";
import type { MergeVerdict, Moved, RerunControl } from "./git-pr-status.js";
import { registerCleanup } from "./actions/index.js";
import type { ActionOutcome } from "./actions/index.js";
import {
  applyInventoryEntry,
  applyInventoryList,
  bindPRPaint,
  cloneDirOf,
  cycleAfter,
  cloneInDir,
  connectedForges,
  lapsedForges,
  getElsewherePRs,
  getPRGroups,
  heldEntry,
  inventoryHeld,
  reinsertPRInGroups,
  removePRFromGroups,
  settleRemoval,
  setPRForges,
} from "./git-prs-state.js";
import type { ElsewherePR } from "./git-prs-state.js";
import { ensureForges } from "./forge-store.js";
import { partialWhy } from "./forge-types.js";
import { reconcile } from "./reconcile.js";
import { forgetSig, sigChanged, wireSignature } from "./paint-sig.js";
import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import { createDialog, type DialogController } from "@cplieger/ui-primitives/dialog";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { gitRepoSkeleton, paintPlaceholder } from "./skeleton.js";
import { iconEl } from "./icon-el.js";

import type { GitPR as PR, GitRepoGroup as RepoGroup } from "./git-types.js";

let filterText = "";
/** Each section's disclosure, so every paint re-decides its open state: reconcile keeps the section element. */
const disclosures = new WeakMap<HTMLElement, DisclosureController>();
/**
 * The reader's own toggles by `groupKey`, which a repaint respects and a filter outranks. Only user toggles are
 * recorded, or this module's own opens would read back as the reader's wish.
 */
const readerToggled = new Map<string, boolean>();
/**
 * A one-shot slot (as `prIdentity` spells it), consumed on success: recording it would re-scroll the reader on every
 * later paint.
 */
let pendingFocus = "";
/** Whether this request has already spent its one healing refresh. */
let focusRefreshed = false;
let refreshGen = 0;
let refreshController: AbortController | null = null;
registerCleanup(() => refreshController?.abort());

/** A repository section's identity: one path can name a repository on two hosts. */
function groupKey(g: RepoGroup): string {
  return join(g.forge_id, g.repo_id);
}

/** The Contributions elsewhere key, which no two-part `groupKey` can spell. */
const ELSEWHERE_KEY = join("elsewhere");

let prsInited = false;

export const prsFind: SearchPopup = createSearchPopup<null>({
  id: "git-prs-filter",
  kind: "filter",
  label: "Filter pull requests",
  placeholder: "Filter pull requests\u2026",
  note: true,
  host: () => document.getElementById("git-view"),
  query: (q) => {
    filterText = q.toLowerCase();
    return null;
  },
  render: () => {
    paint();
  },
});

/** The unit is the pull request on both axes: a match is a row, and the scan reads every row in memory. */
const NOUNS: Nouns = {
  match: { one: "pull request", many: "pull requests" },
  scanned: { one: "pull request", many: "pull requests" },
};

/** Silent with no filter: a count restating the list is noise. */
function noteFor(total: number, shown: number): string {
  if (filterText === "") {
    return "";
  }
  if (shown > 0) {
    return scanNote({ scanned: total, matched: shown, truncated: false }, shown, NOUNS);
  }
  return emptyNote(classify({ matched: 0, shown: 0, scanned: total, truncated: false }), NOUNS);
}

// Spelled once, so the rendered labels and the filter's cannot drift.
const DRAFT_LABEL = "draft";
const AUTO_MERGE_LABEL = "auto-merge";
const MERGE_LABEL = "Merge";
const ARM_LABEL = "Merge when green";
const RERUN_LABEL = "Re-run";
const REOPEN_LABEL = "Reopen";
const CLOSE_LABEL = "Close";
const UNDO_LABEL = "Undo";

function closingText(pr: PR): string {
  return `PR #${String(pr.number)} closes in a few seconds.`;
}

function numberLabel(pr: PR): string {
  return `#${String(pr.number)}`;
}

/** One string, as the row renders the parts joined, so what a reader sees is what they type back. */
function subText(pr: PR): string {
  const parts: string[] = [];
  if (pr.author !== undefined && pr.author !== "") {
    parts.push(`by @${pr.author}`);
  }
  if (pr.updated_at !== undefined && pr.updated_at > 0) {
    parts.push(relativeTime(pr.updated_at));
  }
  if (pr.source_branch !== "" && pr.target_branch !== "") {
    parts.push(`${pr.source_branch} → ${pr.target_branch}`);
  }
  return parts.join(" · ");
}

/** `appendSummary` renders the same pieces. */
function summaryText(pr: PR, number: string): string[] {
  return [
    number,
    pr.title,
    pr.draft === true ? DRAFT_LABEL : "",
    checkChip(pr)?.text ?? "",
    pr.action.auto_merge_armed === "yes" ? AUTO_MERGE_LABEL : "",
    queueText(pr),
    subText(pr),
    pr.state,
  ];
}

/**
 * Every string a PR row shows, in one list, so the filter reaches anything on the row (census in
 * git-prs-tab-filter.test.ts).
 */
function rowText(g: RepoGroup, pr: PR): string[] {
  if (closing.has(rowId(g, pr))) {
    return [closingText(pr), UNDO_LABEL];
  }
  const v = rowView(g, pr);
  return [
    ...summaryText(pr, numberLabel(pr)),
    mergeNote(v.merge),
    rerunNote(v.rerun),
    v.outcome,
    MERGE_LABEL,
    canArmAutoMerge(pr) ? ARM_LABEL : "",
    v.rerun.offer ? RERUN_LABEL : "",
    pr.state !== "open" ? REOPEN_LABEL : "",
    CLOSE_LABEL,
  ];
}

/** The group spans repositories, so the label names the repository. */
function elsewhereLabel(pr: PR): string {
  return `${pr.repo}${numberLabel(pr)}`;
}

function filteredElsewhere(rows: readonly ElsewherePR[]): ElsewherePR[] {
  if (filterText === "") {
    return [...rows];
  }
  return rows.filter((r) =>
    summaryText(r.pr, elsewhereLabel(r.pr)).join("\n").toLowerCase().includes(filterText),
  );
}

function prMatches(g: RepoGroup, pr: PR): boolean {
  return rowText(g, pr).join("\n").toLowerCase().includes(filterText);
}

/** A repo name match admits every PR in it, as on the Changes tab. */
function groupMatches(g: RepoGroup): boolean {
  return filterText !== "" && g.full_name.toLowerCase().includes(filterText);
}

function filteredPRs(g: RepoGroup): PR[] {
  if (filterText === "" || groupMatches(g)) {
    return g.prs;
  }
  return g.prs.filter((pr) => prMatches(g, pr));
}

/**
 * A filter outranks everything, or a selected row sits invisible in a collapsed region. Otherwise the reader's toggle
 * stands, and an untouched repo opens.
 */
function wantOpen(g: RepoGroup): boolean {
  if (filterText !== "") {
    return true;
  }
  // The focus request force-opens its group without a `readerToggled` entry, so the reader's collapse survives.
  if (holdsPendingFocus(g)) {
    return true;
  }
  return readerToggled.get(groupKey(g)) ?? true;
}

/** Closed until the reader opens it; otherwise as `wantOpen`. */
function wantElsewhereOpen(rows: readonly ElsewherePR[]): boolean {
  if (filterText !== "") {
    return true;
  }
  if (
    pendingFocus !== "" &&
    rows.some((r) => prIdentity(r.forge_id, r.pr.repo_id, r.pr.number) === pendingFocus)
  ) {
    return true;
  }
  return readerToggled.get(ELSEWHERE_KEY) ?? false;
}

/**
 * Compared exactly and never parsed: the server mints the subject from the same `repo_id`, and a forge id
 * (`<kind>:<host>`) is not self-delimiting.
 */
function holdsPendingFocus(g: RepoGroup): boolean {
  if (pendingFocus === "") {
    return false;
  }
  return g.prs.some((pr) => prIdentity(g.forge_id, g.repo_id, pr.number) === pendingFocus);
}

/** Focus the pull request `identity` names, once. */
export function requestPRFocus(identity: string): void {
  pendingFocus = identity;
  focusRefreshed = false;
  // A paint, not a frame: the force-open reaches the disclosure only through `paintGroupBody`. Safe before the tab
  // exists: `paintInner` returns early and the activation's paint consumes the request.
  paint();
}

/** The slot clears before the scroll, so a request repainted directly and by a later paint cannot double-scroll. */
function attemptFocus(): void {
  if (pendingFocus === "") {
    return;
  }
  const root = document.getElementById("git-prs-mount");
  if (root === null) {
    return;
  }
  // Compared exactly, as `holdsPendingFocus` does.
  const row = root.querySelector<HTMLElement>(`[data-pr="${CSS.escape(pendingFocus)}"]`);
  if (row === null) {
    // One healing refresh: a stale list heals, a wrong identity costs one fetch and leaves no selection. The slot stands
    // so the refresh's paint retries.
    if (focusRefreshed) {
      pendingFocus = "";
      return;
    }
    focusRefreshed = true;
    void refreshPRs().catch(() => {
      /* the error state is already painted by refreshPRs */
    });
    return;
  }
  pendingFocus = "";
  focusRefreshed = false;
  flashTarget(() => row);
}

export function initPRsTab(): void {
  if (prsInited) {
    return;
  }
  prsInited = true;
  bindPRPaint(paint);

  const refreshBtn = document.getElementById("git-refresh-prs-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    const status = el("span", { className: "git-tab-toolbar-error", role: "status" });
    refreshBtn.before(status);
    refreshBtn.addEventListener("click", () => {
      void withAsyncFeedback(refreshBtn, () => pressRefresh(status));
    });
  }

  // The list depends on connected forges, and a reconnect can change a connection's capabilities and refusals.
  onSSE("forges_changed", () => {
    rerunCaps.clear();
    rerunRefused.clear();
    void refreshPRsAction.dispatch();
  });
  onSSE("forge_inventory", (_chatID, p) => {
    if (applyInventoryEntry(p.entry) && inventoryHeld()) {
      paint();
      settlePress();
    }
  });
  // The stream lost frames it cannot replay, so no held entry can be trusted.
  onBus(BUS_RECONCILE, ({ signal }) => {
    if (inventoryHeld()) {
      void refreshPRs(signal, { reset: true }).catch(() => {
        /* the error state is already painted by refreshPRs */
      });
    }
  });
  // A close waiting out its window is one the reader asked for: leaving the list sends it now, a hidden page keeps its
  // window, and an unload sends it by the request that outlives the page.
  observePRView((watching) => {
    postWatch(watching);
    if (!watching && document.visibilityState === "visible") {
      flushCloses();
    }
  });
  window.addEventListener("pagehide", sendClosesOnUnload);
}

/** Watch posts run one at a time, in the order the view changed. */
let watchChain: Promise<unknown> = Promise.resolve();

function postWatch(watching: boolean): void {
  const tag = presentedTag();
  if (tag === "") {
    return;
  }
  watchChain = watchChain.then(
    () => watchPRView.dispatch({ watching, tag, page: VIEW_PAGE }),
    () => undefined,
  );
}

/** A cycle normally ends inside the poller's 60 s interval, plus a 30 s margin. */
const REFRESH_BOUND_MS = 90_000;

/** The press the refresh button waits on: the cycle the refresh route named for
 *  it, and how it ends. */
let press: { cycle: string; finish: (sentence: string | null) => void } | null = null;

/**
 * Resolves once every connected connection holds an entry from that cycle or later. A refusal, an unread
 * connection, or the bound running out rejects with its sentence in `status`.
 */
function pressRefresh(status: HTMLElement): Promise<void> {
  status.textContent = "";
  return new Promise<void>((resolve, reject) => {
    let open = true;
    const finish = (sentence: string | null): void => {
      if (!open) {
        return;
      }
      open = false;
      clearTimeout(timer);
      press = null;
      if (sentence === null) {
        resolve();
        return;
      }
      status.textContent = sentence;
      reject(new Error(sentence));
    };
    const timer = setTimeout(() => {
      finish(
        `The refresh did not finish within ${String(REFRESH_BOUND_MS / 1000)} seconds. The list updates when it does.`,
      );
    }, REFRESH_BOUND_MS);
    void requestPRCycle.dispatch().outcome.then((o) => {
      if (o.status !== "success") {
        finish(
          o.status === "error"
            ? `Could not refresh pull requests. ${o.error.message}`
            : "The refresh was cancelled.",
        );
        return;
      }
      if (!open) {
        return;
      }
      // The re-read settles it if the cycle's frames landed before this answer.
      press = { cycle: o.value.cycle_id, finish };
      void refreshPRs().catch(() => {
        /* the error state is already painted by refreshPRs */
      });
    });
  });
}

/** Completion is read off each entry's cycle id, never off a frame's arrival. */
function settlePress(): void {
  if (press === null) {
    return;
  }
  const asked = press.cycle;
  const forges = connectedForges();
  const held = forges.map((f) => heldEntry(f.id));
  if (held.some((e) => e === undefined || cycleAfter(asked, e.cycle_id))) {
    return;
  }
  const failed = forges.filter((_f, i) => held[i]?.state === "failed").map((f) => f.host);
  press.finish(
    failed.length === 0 ? null : `Could not read ${failed.join(", ")}. The list says why.`,
  );
}

interface RefreshOptions {
  /** Replace every held entry, whatever its cycle: a reconcile. */
  reset?: boolean;
}

/** The newest `refreshPRs` call, which a superseded one waits out. */
let newestRefresh: Promise<void> = Promise.resolve();

/**
 * Read the inventory and repaint. Only the latest result wins; a superseded call settles when the newer one has, so a
 * caller awaiting it can read what was painted.
 */
export function refreshPRs(externalSignal?: AbortSignal, opts: RefreshOptions = {}): Promise<void> {
  const run = readAndPaintPRs(externalSignal, opts);
  newestRefresh = run;
  return run;
}

async function readAndPaintPRs(
  externalSignal: AbortSignal | undefined,
  opts: RefreshOptions,
): Promise<void> {
  const myGen = ++refreshGen;
  refreshController?.abort();
  refreshController = new AbortController();
  const signal = AbortSignal.any([refreshController.signal, AbortSignal.timeout(20_000)]);
  // A local ref, so the closure does not see a later refreshController.
  const myController = refreshController;
  if (externalSignal) {
    externalSignal.addEventListener(
      "abort",
      () => {
        myController.abort();
      },
      { once: true },
    );
  }

  // The 150ms show delay keeps a warm read from flashing placeholders; the signal suppresses it for a superseded refresh.
  const root = document.getElementById("git-prs-mount");
  const skeleton = inventoryHeld() ? null : skeletonTiming(() => showPRSkeleton(root), { signal });

  try {
    const read = await readInventory(signal);
    if (myGen !== refreshGen) {
      await newerSettled();
      return;
    }
    if (read === null) {
      return;
    }
    skeleton?.cancel();
    setPRForges(read.forges);
    const adopted = applyInventoryList(read.list, { reset: opts.reset === true });
    for (const i of adopted) {
      observeStamp(read.list.subject[i]);
    }
    paint();
    askForMissingEntries();
    settlePress();
  } catch (err) {
    if (myGen !== refreshGen) {
      await newerSettled();
      return;
    }
    // The toast is transient and every empty state below means success, so a failure must say so in the pane.
    if (root !== null) {
      paintLoadError(root, err);
    }
    throw err;
  } finally {
    skeleton?.cancel();
  }
}

/** Its failure is that call's to report. */
async function newerSettled(): Promise<void> {
  await newestRefresh.catch(() => undefined);
}

/** Null when the read was aborted; throws when either could not be read. */
async function readInventory(
  signal: AbortSignal,
): Promise<{ forges: ConfiguredForge[]; list: InventoryList } | null> {
  // From the shared store, which supplies each connection's kind and host.
  const [forgesRes, list] = await Promise.all([
    ensureForges(),
    apiGetTyped("/api/forges/inventory", decodeInventoryList, signal),
  ]);
  if (signal.aborted) {
    return null;
  }
  if (forgesRes === null) {
    throw new Error("Failed to load forges");
  }
  if (list === null) {
    throw new Error("The pull-request inventory could not be read");
  }
  return { forges: forgesRes.forges, list };
}

/** A connected forge the inventory has not answered for is one the poller has not read; ask it to. */
function askForMissingEntries(): void {
  if (connectedForges().some((f) => heldEntry(f.id) === undefined)) {
    void requestPRCycle.dispatch();
  }
}

function showPRSkeleton(root: HTMLElement | null): () => void {
  // Shared with the Changes tab (skeleton.ts): both stand in for `.git-repo-section`, so its geometry has one definition.
  return paintPlaceholder(root, () =>
    gitRepoSkeleton({
      label: "Loading pull requests\u2026",
      widths: ["45%", "32%", "58%"],
    }),
  );
}

function paintLoadError(root: HTMLElement, err: unknown): void {
  const msg = err instanceof Error ? err.message : String(err);
  root.replaceChildren(
    el(
      "div",
      { className: "git-multirepo-error" },
      `Could not load pull requests. ${msg}`,
      el("br"),
      "Use the refresh button above to try again.",
    ),
  );
}

/** One connection painted as a state: loading, failed, or listed with notes on
 *  what its lists have not read yet. */
interface ConnectionState {
  forge: ConfiguredForge;
  entry: InventoryEntry | undefined;
  /** The connection needs a new sign-in, so no entry is coming. */
  lapsed: boolean;
}

function connectionStates(): ConnectionState[] {
  const out: ConnectionState[] = lapsedForges().map((forge) => ({
    forge,
    entry: undefined,
    lapsed: true,
  }));
  for (const forge of connectedForges()) {
    const entry = heldEntry(forge.id);
    if (
      entry === undefined ||
      entry.state === "loading" ||
      entry.state === "failed" ||
      entryNotes(forge, entry).length > 0
    ) {
      out.push({ forge, entry, lapsed: false });
    }
  }
  return out.sort((a, b) => a.forge.id.localeCompare(b.forge.id));
}

function loading(s: ConnectionState): boolean {
  return !s.lapsed && (s.entry === undefined || s.entry.state === "loading");
}

/** A scope whose walk goes on, a scope cut short, and a list that failed beside the read ones. */
function entryNotes(forge: ConfiguredForge, e: InventoryEntry): string[] {
  if (e.state !== "ready" && e.state !== "partial") {
    return [];
  }
  const notes: string[] = [];
  for (const s of e.scopes) {
    const where = scopeWhere(s);
    if ((s.next ?? "") !== "") {
      notes.push(`Still reading the pull requests ${where} on ${forge.host}, a page per cycle.`);
    }
    const why = partialWhy(s.partial, s.next);
    if (why !== "") {
      notes.push(`Not every pull request ${where} on ${forge.host} was read: ${why}.`);
    }
  }
  if (e.state === "partial" && e.error !== undefined) {
    notes.push(
      `Could not read one of the lists on ${forge.host}. ${capitalise(inventoryErrorText(e))}`,
    );
  }
  return notes;
}

function scopeWhere(s: InventoryScope): string {
  return s.scope === "authored" ? "you opened" : `in ${s.owner ?? ""}`;
}

function renderConnectionState(s: ConnectionState): HTMLElement {
  const box = el("div", { className: "git-prs-connection" });
  if (s.lapsed) {
    box.appendChild(
      el(
        "div",
        { className: "git-multirepo-error" },
        `Could not list pull requests on ${s.forge.host}. ${capitalise(RECONNECT_WORDS)}`,
      ),
    );
  } else if (s.entry?.state === "failed") {
    box.appendChild(
      el(
        "div",
        { className: "git-multirepo-error" },
        `Could not list pull requests on ${s.forge.host}. ${capitalise(inventoryErrorText(s.entry))}`,
      ),
    );
  } else if (s.entry !== undefined && !loading(s)) {
    for (const note of entryNotes(s.forge, s.entry)) {
      box.appendChild(el("p", { className: "section-hint" }, note));
    }
  } else {
    box.appendChild(
      gitRepoSkeleton({
        label: `Loading pull requests from ${s.forge.host}\u2026`,
        widths: ["45%", "32%"],
      }),
    );
  }
  return box;
}

const RECONNECT_WORDS = "this account needs a new sign-in. Reconnect it in Sources.";

/** From its code and kind: the inventory carries no upstream message. */
function inventoryErrorText(e: InventoryEntry): string {
  const err = e.error;
  if (e.credential === "reconnect_required" || err?.code === "reconnect_required") {
    return RECONNECT_WORDS;
  }
  if (err?.code === "scope_insufficient") {
    return "the token is missing a permission this list needs. Add it to the token on the forge.";
  }
  switch (err?.kind) {
    case "rate_limited":
      return `the forge is rate limiting this account. ${waitWords(e)}`;
    case "unauthorized":
      return "the forge refused the credential. Check the account in Sources.";
    case "forbidden":
    case "not_found":
      return "the forge refused the list for this account.";
    case "transient":
    case "upstream":
      return "the forge did not answer as expected. The next cycle tries again.";
    default:
      return err?.code !== undefined && err.code !== ""
        ? `the forge refused the list (${err.code}).`
        : "the list could not be read.";
  }
}

/** The wait a rate limit asked for, counted from the cycle that met it. */
function waitWords(e: InventoryEntry): string {
  const wait = e.error?.retry_after_s ?? 0;
  if (wait <= 0) {
    return "The next cycle tries again.";
  }
  const left = Math.ceil((wait * 1000 - (Date.now() - e.fetched_at)) / 1000);
  if (left <= 0) {
    return "The next cycle tries again.";
  }
  return `It can be read again in ${String(left)} second${left === 1 ? "" : "s"}.`;
}

function paint(): void {
  preserveGitScroll(paintInner);
  // Registered after preserveGitScroll returns, so it runs after that function's own rAF scroll restore and the scroll
  // into view wins. Guarded, so a settled tab schedules nothing.
  if (pendingFocus !== "") {
    requestAnimationFrame(attemptFocus);
  }
}

function paintInner(): void {
  const root = document.getElementById("git-prs-mount");
  // Before the first read there is nothing to paint over the placeholder.
  if (root === null || !inventoryHeld()) {
    return;
  }

  const groups = getPRGroups();
  const elsewhere = getElsewherePRs();
  const states = connectionStates();
  setBusy(root, states.some(loading));

  // A toggle for a repo no longer listed describes nothing.
  const active = new Set(groups.map(groupKey));
  if (elsewhere.length > 0) {
    active.add(ELSEWHERE_KEY);
  }
  for (const k of readerToggled.keys()) {
    if (!active.has(k)) {
      readerToggled.delete(k);
    }
  }

  if (connectedForges().length === 0 && lapsedForges().length === 0) {
    prsFind.shell?.setNote("");
    root.innerHTML = renderEmptyState({
      icon: ICON_PR_EMPTY,
      title: "No connected forges",
      hint: "Open the <strong>Sources</strong> tab to add a forge account.",
    });
    return;
  }

  // One predicate, read here for the note and the section list and in paintGroupBody for the rows.
  const visible: RepoGroup[] = [];
  let total = 0;
  let shown = 0;
  for (const g of groups) {
    total += g.prs.length;
    const rows = filteredPRs(g);
    shown += rows.length;
    if (filterText === "" || rows.length > 0 || groupMatches(g)) {
      visible.push(g);
    }
  }
  const shownElsewhere = filteredElsewhere(elsewhere);
  total += elsewhere.length;
  shown += shownElsewhere.length;
  prsFind.shell?.setNote(noteFor(total, shown));
  askCapabilities(groups);
  pruneRowState(groups);

  // A connection still loading or failed is not "caught up", so its state is the pane's content.
  if (visible.length === 0 && states.length === 0 && shownElsewhere.length === 0) {
    // No hint under a filter: the note above already says what it found.
    root.innerHTML =
      filterText === ""
        ? renderEmptyState({
            icon: ICON_PR_EMPTY,
            title: "All caught up",
            hint: "No open pull requests across your connected forges.",
          })
        : renderEmptyState({ icon: ICON_FILTER, title: "No matching pull requests" });
    return;
  }

  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  const items: PaneItem[] = [
    ...states.map((connection) => ({ connection })),
    ...visible.map((group) => ({ group })),
  ];
  if (shownElsewhere.length > 0) {
    items.push({ elsewhere: { all: elsewhere, shown: shownElsewhere } });
  }
  reconcile(root, items, {
    key: paneKey,
    // The mount builds the chrome only and fills the body through the update path, so every row comes from a keyed
    // reconcile. An unkeyed row cannot be matched or removed, and the next paint would append a second copy.
    mount: (it) => {
      if ("connection" in it) {
        const box = renderConnectionState(it.connection);
        sigChanged(box, connectionSig(it.connection));
        return box;
      }
      if ("elsewhere" in it) {
        const section = renderElsewhere();
        paintElsewhereBody(section, it.elsewhere);
        return section;
      }
      const section = renderGroup(it.group);
      paintGroupBody(section, it.group);
      return section;
    },
    update: (node, it) => {
      if ("connection" in it) {
        if (sigChanged(node, connectionSig(it.connection))) {
          node.replaceChildren(...Array.from(renderConnectionState(it.connection).childNodes));
        }
        return;
      }
      if ("elsewhere" in it) {
        paintElsewhereBody(node, it.elsewhere);
        return;
      }
      paintGroupBody(node, it.group);
    },
  });
}

/** The contributions elsewhere: every one, for the count, and those the filter
 *  admits, for the rows. */
interface ElsewhereView {
  all: readonly ElsewherePR[];
  shown: readonly ElsewherePR[];
}

/** One child of the pane: a connection's state, a repository's section, or the
 *  Contributions elsewhere group at the end. */
type PaneItem =
  | { readonly group: RepoGroup }
  | { readonly connection: ConnectionState }
  | { readonly elsewhere: ElsewhereView };

function paneKey(it: PaneItem): string {
  if ("connection" in it) {
    return join("connection", it.connection.forge.id);
  }
  return "elsewhere" in it ? ELSEWHERE_KEY : join("repo", groupKey(it.group));
}

function connectionSig(s: ConnectionState): string[] {
  return [
    s.forge.host,
    s.entry === undefined ? "" : wireSignature(s.entry.error ?? {}),
    s.entry?.state ?? "",
    // A later cycle's entry restates the rate limit's wait.
    String(s.entry?.fetched_at ?? 0),
    ...(s.entry === undefined ? [] : entryNotes(s.forge, s.entry)),
  ];
}

/**
 * Header identity is kept across paints; the open state is decided again, or a kept element only ever gets the
 * mount's decision.
 */
function paintGroupBody(section: HTMLElement, g: RepoGroup): void {
  const meta = section.querySelector(".git-repo-section-meta");
  if (meta !== null) {
    meta.textContent = `${String(g.prs.length)} open`;
  }

  const ctl = disclosures.get(section);
  if (ctl !== undefined) {
    if (wantOpen(g)) {
      ctl.open();
    } else {
      ctl.close();
    }
  }

  const body = section.querySelector<HTMLElement>(":scope > .git-repo-section-body");
  if (body === null) {
    return;
  }
  // createDisclosure owns the region; replaceChildren() on it would drop the wrapper and break the collapse.
  const inner = body.querySelector<HTMLElement>(":scope > .git-repo-section-body-inner");
  if (inner === null) {
    return;
  }

  // The pane lists a group only when the filter admits a row, so the list is never empty.
  const filtered = filteredPRs(g);
  let list = inner.querySelector<HTMLElement>(":scope > .git-pr-list");
  if (list === null) {
    list = el("ul", { className: "git-pr-list" });
    inner.replaceChildren(list);
  }
  reconcile(list, filtered, {
    key: (pr: PR) => `${g.forge_id}:${pr.number}`,
    mount: (pr: PR) => {
      const row = renderPRRow(g, pr);
      sigChanged(row, rowSig(g, pr));
      return row;
    },
    // A surviving row is repainted: merge_blocked is per fetch, so a kept row would hold a disabled Merge after its
    // checks went green. `data-pr` comes from fields a repaint cannot move. Guarded, since it is polled.
    update: (row: HTMLElement, pr: PR) => {
      if (!sigChanged(row, rowSig(g, pr))) {
        return;
      }
      row.classList.toggle("git-pr-row-closing", closing.has(rowId(g, pr)));
      row.replaceChildren(...Array.from(renderPRRow(g, pr).childNodes));
    },
  });
}

function renderEmptyState(opts: { icon: string; title: string; hint?: string }): string {
  const hint =
    opts.hint === undefined ? "" : `<div class="git-multirepo-empty-hint">${opts.hint}</div>`;
  return `
    <div class="git-multirepo-empty">
      <div class="git-multirepo-empty-icon">${opts.icon}</div>
      <div class="git-multirepo-empty-title">${opts.title}</div>
      ${hint}
    </div>
  `;
}

function renderGroup(g: RepoGroup): HTMLElement {
  const section = el("section", { className: "git-repo-section", "data-repo": g.full_name });

  // The New PR button's stopPropagation keeps the toggle from firing.
  const header = el("div", { className: "git-repo-section-header git-repo-section-header-row" });

  const toggle = el("button", {
    type: "button",
    className: "git-repo-section-header-toggle",
  });
  const chevron = chevronEl();
  chevron.classList.add("git-repo-section-chevron");
  toggle.append(
    chevron,
    el(
      "span",
      {
        className: `git-repo-section-forge-icon git-repo-section-forge-${g.forge_kind}`,
        "aria-hidden": "true",
      },
      iconEl(FORGE_ICONS[g.forge_kind] ?? ""),
    ),
    el("span", { className: "git-repo-section-name" }, g.full_name),
    el("span", { className: "git-repo-section-meta" }, `${String(g.prs.length)} open`),
  );
  header.appendChild(toggle);

  const newBtn = el("button", { type: "button", className: "btn-small btn-primary" }, "+ New PR");
  newBtn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openNewPRDialog(g);
  });
  header.appendChild(newBtn);

  section.appendChild(header);

  // Left empty here: paintGroupBody fills it on mount and every paint. An open disclosure settles to height:auto, so
  // later content grows the region.
  const body = el("div", { className: "git-repo-section-body" });
  const inner = el("div", { className: "git-repo-section-body-inner" });
  body.appendChild(inner);
  disclosures.set(
    section,
    createDisclosure(toggle, body, {
      open: wantOpen(g),
      onToggle: (open, source) => {
        if (source === "user") {
          readerToggled.set(groupKey(g), open);
        }
      },
    }),
  );

  section.appendChild(body);
  return section;
}

/** Closed by default and filled by `paintElsewhereBody`; no New PR, since none of its repositories is the account's. */
function renderElsewhere(): HTMLElement {
  const section = el("section", { className: "git-repo-section", "data-group": "elsewhere" });
  const header = el("div", { className: "git-repo-section-header git-repo-section-header-row" });
  const toggle = el("button", { type: "button", className: "git-repo-section-header-toggle" });
  const chevron = chevronEl();
  chevron.classList.add("git-repo-section-chevron");
  toggle.append(
    chevron,
    el("span", { className: "git-repo-section-name" }, "Contributions elsewhere"),
    el("span", { className: "git-repo-section-meta" }),
  );
  header.appendChild(toggle);
  section.appendChild(header);

  const body = el("div", { className: "git-repo-section-body" });
  body.appendChild(
    el(
      "div",
      { className: "git-repo-section-body-inner" },
      el(
        "p",
        { className: "section-hint" },
        "Pull requests you opened in repositories outside your owners. They open on their forge. Add an owner in Sources to act on its pull requests here.",
      ),
      el("ul", { className: "git-pr-list" }),
    ),
  );
  disclosures.set(
    section,
    createDisclosure(toggle, body, {
      open: false,
      onToggle: (open, source) => {
        if (source === "user") {
          readerToggled.set(ELSEWHERE_KEY, open);
        }
      },
    }),
  );
  section.appendChild(body);
  return section;
}

function paintElsewhereBody(section: HTMLElement, view: ElsewhereView): void {
  const meta = section.querySelector(".git-repo-section-meta");
  if (meta !== null) {
    meta.textContent = `${String(view.all.length)} open`;
  }
  const ctl = disclosures.get(section);
  if (ctl !== undefined) {
    if (wantElsewhereOpen(view.all)) {
      ctl.open();
    } else {
      ctl.close();
    }
  }
  const list = section.querySelector<HTMLElement>(".git-pr-list");
  if (list === null) {
    return;
  }
  reconcile(list, [...view.shown], {
    key: (r: ElsewherePR) => join(r.forge_id, r.pr.repo_id, String(r.pr.number)),
    mount: renderElsewhereRow,
    update: (row: HTMLElement, r: ElsewherePR) => {
      if (sigChanged(row, [wireSignature(r.pr), r.forge_id])) {
        row.replaceChildren(...Array.from(renderElsewhereRow(r).childNodes));
      }
    },
  });
}

/** No action, since the account may hold no right there; its link is the way to act. */
function renderElsewhereRow(r: ElsewherePR): HTMLElement {
  const li = el("li", {
    className: "git-pr-row git-pr-row-readonly",
    "data-pr": prIdentity(r.forge_id, r.pr.repo_id, r.pr.number),
  });
  appendSummary(li, r.pr, elsewhereLabel(r.pr));
  return li;
}

/** `summaryText` lists the same strings for the filter. */
function appendSummary(li: HTMLElement, pr: PR, number: string): void {
  // One identity element for the title line: the number and title share an href, so two anchors would be two tab stops
  // for one destination. The number rides inside as a span.
  const hasURL = pr.url !== undefined && pr.url !== "";
  const num = el("span", { className: "git-pr-row-number" }, number);
  // The ellipsis clip cannot sit on the link: it would cut away the expander carrying the hit region.
  const text = el("span", { className: "git-pr-row-text" }, pr.title);
  const title = hasURL
    ? el("a", { className: "git-pr-row-title", target: "_blank", rel: "noreferrer" }, num, text)
    : el("span", { className: "git-pr-row-title" }, num, text);
  if (hasURL) {
    title.setAttribute("href", pr.url!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  title.setAttribute("data-tooltip", pr.title);
  li.appendChild(title);

  // Chips lead the sub line so a long title does not ellipsise into them on the title line.
  const sub = el("div", { className: "git-pr-row-sub" });

  if (pr.draft === true) {
    sub.appendChild(el("span", { className: "git-pr-row-tag" }, DRAFT_LABEL));
  }

  // Check status comes in the list call already made. A forge with no CI state gets no chip.
  const chip = checkChip(pr);
  if (chip !== null) {
    const chipEl = el("span", { className: `git-pr-row-tag ${chip.className}` }, chip.text);
    chipEl.setAttribute("data-tooltip", chip.tooltip);
    sub.appendChild(chipEl);
  }

  if (pr.action.auto_merge_armed === "yes") {
    const armed = el(
      "span",
      { className: "git-pr-row-tag git-pr-check-pending" },
      AUTO_MERGE_LABEL,
    );
    armed.setAttribute("data-tooltip", "The forge will merge this once its requirements are met.");
    sub.appendChild(armed);
  }

  const queue = queueText(pr);
  if (queue !== "") {
    sub.appendChild(el("span", { className: "git-pr-row-tag git-pr-check-pending" }, queue));
  }

  const subLine = subText(pr);
  if (subLine !== "") {
    sub.appendChild(el("span", { className: "git-pr-row-sub-text" }, subLine));
  }
  li.appendChild(sub);
}

/** Null when its read failed; absent until it answers. Kept until the connections change. */
const rerunCaps = new Map<string, Affordance | null>();
const capsAsked = new Set<string>();
/** By `join(forge_id, repo_id)`, for the life of the list. */
const rerunRefused = new Map<string, string>();
/** In-flight row actions by row identity and label, so a repaint while one runs renders its button busy. */
const pressing = new Map<string, Set<string>>();
/**
 * The last press's sentence per row. An error stands until the row's next press; a success until its connection's
 * next entry, the forge's own word.
 */
const outcomes = new Map<string, { text: string; error: boolean; cycle: string }>();
const NOTHING_PRESSED: ReadonlySet<string> = new Set();
let noteSeq = 0;
/** Repositories a refusal said moved, by groupKey: where to, and the row and the
 *  press it refused, which the re-point offer and its focus follow. */
const moved = new Map<string, { to: RepoSuccessor; row: string; label: string }>();
/** Repositories the reader re-pointed, by groupKey: their rows' routes take the
 *  successor's id while the list still names them by the old one. */
const repointed = new Map<string, RepoSuccessor>();

/** The id a group's routes address: its own, or the one it was re-pointed to. */
function addressOf(g: RepoGroup): string {
  return repointed.get(groupKey(g))?.repo_id ?? g.repo_id;
}

/** Once until the connections change. */
function askCapabilities(groups: readonly RepoGroup[]): void {
  for (const g of groups) {
    const id = g.forge_id;
    if (
      rerunCaps.has(id) ||
      capsAsked.has(id) ||
      !g.prs.some((pr) => pr.action.checks === "failing")
    ) {
      continue;
    }
    capsAsked.add(id);
    void readCapabilities.dispatch({ forge_id: id }).then((caps) => {
      capsAsked.delete(id);
      // The route carries every capability forgeapi names, so an absent key is an answer that did not arrive.
      rerunCaps.set(id, caps?.connection["rerun_checks"] ?? null);
      paint();
    });
  }
}

/** A repository created later at the old path is another one. */
function pruneRowState(groups: readonly RepoGroup[]): void {
  const listed = new Set(groups.flatMap((g) => g.prs.map((pr) => rowId(g, pr))));
  for (const id of outcomes.keys()) {
    if (!listed.has(id)) {
      outcomes.delete(id);
    }
  }
  const repos = new Set(groups.map(groupKey));
  for (const m of [moved, repointed]) {
    for (const key of m.keys()) {
      if (!repos.has(key)) {
        m.delete(key);
      }
    }
  }
}

function rowId(g: RepoGroup, pr: PR): string {
  return prIdentity(g.forge_id, g.repo_id, pr.number);
}

interface RowView {
  merge: MergeVerdict;
  rerun: RerunControl;
  /** The last press's sentence while it stands, or "". */
  outcome: string;
  failed: boolean;
  busy: ReadonlySet<string>;
  /** Where this row's refused press found its repository moved, while offered. */
  movedTo: RepoSuccessor | undefined;
}

function rowView(g: RepoGroup, pr: PR): RowView {
  const id = rowId(g, pr);
  const o = outcomes.get(id);
  const stands = o !== undefined && (o.error || heldEntry(g.forge_id)?.cycle_id === o.cycle);
  const follow = merging.get(id);
  const m = moved.get(groupKey(g));
  return {
    merge: mergeVerdict(pr),
    rerun:
      pr.action.checks === "failing"
        ? rerunControl(
            g.forge_kind,
            rerunCaps.get(g.forge_id),
            rerunRefused.get(join(g.forge_id, g.repo_id)),
          )
        : { offer: false },
    outcome: follow ?? (stands ? o.text : ""),
    failed: follow === undefined && stands && o.error,
    busy: pressing.get(id) ?? NOTHING_PRESSED,
    movedTo: m?.row === id ? m.to : undefined,
  };
}

/** Everything a row renders from, for its repaint guard. */
function rowSig(g: RepoGroup, pr: PR): string[] {
  const v = rowView(g, pr);
  return [
    wireSignature(pr),
    g.forge_kind,
    g.forge_id,
    String(closing.has(rowId(g, pr))),
    v.rerun.offer ? `offer:${v.rerun.reason}` : "",
    v.outcome,
    String(v.failed),
    [...v.busy].sort().join(","),
    v.movedTo?.repo_id ?? "",
    addressOf(g),
  ];
}

function mergeNote(v: MergeVerdict): string {
  switch (v.state) {
    case "ready":
      return "";
    case "unknown":
      return `Cannot merge yet: ${v.reason}`;
    case "blocked":
      return `Cannot merge: ${v.reason}`;
  }
}

function rerunNote(c: RerunControl): string {
  return c.offer && c.reason !== "" ? `Cannot re-run checks: ${c.reason}` : "";
}

interface PressOutcome {
  ok: boolean;
  text: string;
}

/** A cancelled dispatch says nothing. */
function refused(o: ActionOutcome<unknown>, lead: string): PressOutcome {
  return o.status === "error"
    ? { ok: false, text: `${lead}. ${o.error.message}` }
    : { ok: true, text: "" };
}

/** Offers the re-point when the refusal names where the repository (`key`, a groupKey) moved. */
function refusedOn(
  key: string,
  row: string,
  o: ActionOutcome<unknown>,
  lead: string,
  label: string,
): PressOutcome {
  const m: Moved = o.status === "error" ? movedRepository(o.error) : { moved: false };
  if (!m.moved) {
    return refused(o, lead);
  }
  if (m.to === null) {
    return {
      ok: false,
      text: `${lead}. This repository moved, and the forge did not say where. Refresh the list to read it again.`,
    };
  }
  moved.set(key, { to: m.to, row, label });
  return { ok: false, text: `${lead}. This repository moved to ${m.to.display_path}.` };
}

function useButton(g: RepoGroup, id: string, to: RepoSuccessor): HTMLButtonElement {
  const use = rowButton(`Use ${to.display_path}`, NOTHING_PRESSED);
  use.setAttribute("data-tooltip", `Act on ${to.display_path}, where this repository moved`);
  use.addEventListener("click", () => {
    repoint(g, id, document.activeElement === use);
  });
  return use;
}

/** Asks for a cycle, which lists the rows under the successor. */
function repoint(g: RepoGroup, id: string, focused: boolean): void {
  const key = groupKey(g);
  const m = moved.get(key);
  if (m === undefined) {
    return;
  }
  moved.delete(key);
  repointed.set(key, m.to);
  outcomes.set(id, {
    text: `Now acting on ${m.to.display_path}.`,
    error: false,
    cycle: heldEntry(g.forge_id)?.cycle_id ?? "",
  });
  void requestPRCycle.dispatch();
  paint();
  if (focused) {
    focusRowButton(id, m.label);
  }
}

/** Busy and disabled in the press's frame and through any repaint while it runs; its sentence in the row once it ends. */
async function pressRow(
  g: RepoGroup,
  id: string,
  btn: HTMLButtonElement,
  label: string,
  run: () => Promise<PressOutcome>,
): Promise<void> {
  outcomes.delete(id);
  setRowStatus(btn, "");
  let busy = pressing.get(id);
  if (busy === undefined) {
    busy = new Set();
    pressing.set(id, busy);
  }
  busy.add(label);
  let out: PressOutcome = { ok: true, text: "" };
  try {
    // The status line announces the sentence, so the button announces nothing.
    await withAsyncFeedback(
      btn,
      async () => {
        out = await run();
        if (!out.ok) {
          throw new Error(out.text);
        }
      },
      { keepLabel: true, announce: false },
    );
  } finally {
    busy.delete(label);
    if (busy.size === 0) {
      pressing.delete(id);
    }
  }
  if (out.text !== "") {
    outcomes.set(id, {
      text: out.text,
      error: !out.ok,
      cycle: heldEntry(g.forge_id)?.cycle_id ?? "",
    });
  }
  // A repaint while the request ran replaced this button with a busy copy.
  if (btn.isConnected) {
    setRowStatus(btn, out.text, !out.ok);
    const m = moved.get(groupKey(g));
    if (m?.row === id) {
      btn
        .closest(".git-pr-row")
        ?.querySelector(".git-pr-row-actions")
        ?.lastElementChild?.before(useButton(g, id, m.to));
    }
  } else {
    paint();
  }
}

/** Leaves the button's outcome glyph standing; the next paint repaints the row from its state. */
function setRowStatus(btn: HTMLElement, text: string, failed = false): void {
  const row = btn.closest(".git-pr-row");
  const status = row?.querySelector(".git-pr-row-status");
  if (row === null || status === null || status === undefined) {
    return;
  }
  status.textContent = text;
  status.classList.toggle("err", failed);
  forgetSig(row);
}

function rowButton(
  label: string,
  busy: ReadonlySet<string>,
  className = "btn-small",
): HTMLButtonElement {
  const btn = el("button", { type: "button", className }, label) as HTMLButtonElement;
  if (busy.has(label)) {
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
    btn.prepend(
      el("span", { className: "spinner-sm btn-async-spinner", "aria-hidden": "true" }),
      " ",
    );
  }
  return btn;
}

/** The control is described by this sentence. */
function appendNote(
  li: HTMLElement,
  control: HTMLElement,
  kind: string,
  text: string,
  state = "",
): void {
  noteSeq += 1;
  const id = `git-pr-note-${String(noteSeq)}`;
  const note = el("p", { className: "git-pr-row-note", id, "data-note": kind }, text);
  if (state !== "") {
    note.setAttribute("data-state", state);
  }
  control.setAttribute("aria-describedby", id);
  li.appendChild(note);
}

/** One poll interval, past which the poller's own cycle carries the row. */
const MERGE_FOLLOW_INTERVAL_MS = 3000;
const MERGE_FOLLOW_BOUND_MS = 60_000;

const merging = new Map<string, string>();

/**
 * The row says merging and leaves once the forge reads it merged, or stays with a sentence past the bound. A row the
 * list stops holding ends the follow-up.
 */
async function followMerge(
  g: RepoGroup,
  pr: PR,
  prRef: PRArgs,
  state: string,
): Promise<PressOutcome> {
  const id = rowId(g, pr);
  merging.set(id, state === "enqueued" ? "Waiting in the merge queue…" : "Merging…");
  paint();
  // The bound ends the waits and aborts a read still in flight.
  const deadline = new AbortController();
  const bound = setTimeout(() => {
    deadline.abort();
  }, MERGE_FOLLOW_BOUND_MS);
  try {
    while (!(await pauseUnless(MERGE_FOLLOW_INTERVAL_MS, deadline.signal))) {
      if (!stillListed(g, pr)) {
        return { ok: true, text: "" };
      }
      const read = readMergeStatus.dispatch(prRef);
      const stop = (): void => {
        read.abort();
      };
      deadline.signal.addEventListener("abort", stop, { once: true });
      const o = await read.outcome;
      deadline.signal.removeEventListener("abort", stop);
      if (o.status === "success" && o.value.merged === "yes") {
        removePRFromGroups(g.forge_id, g.repo_id, pr.number);
        return { ok: true, text: "" };
      }
    }
  } finally {
    clearTimeout(bound);
    merging.delete(id);
  }
  void requestPRCycle.dispatch();
  return {
    ok: true,
    text: "The forge has not finished the merge yet. The list shows it once it does.",
  };
}

/** Wait `ms`, or less when `signal` aborts first; answers whether it aborted. */
function pauseUnless(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(true);
      return;
    }
    const onAbort = (): void => {
      clearTimeout(timer);
      resolve(true);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve(false);
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function stillListed(g: RepoGroup, pr: PR): boolean {
  return getPRGroups().some(
    (h) => groupKey(h) === groupKey(g) && h.prs.some((p) => p.number === pr.number),
  );
}

/** Its Undo takes it back without touching the forge. */
const CLOSE_UNDO_MS = 8000;

const closing = new Map<
  string,
  { args: PRArgs; key: string; listedID: string; timer: ReturnType<typeof setTimeout> }
>();

/** The row turns into its Undo bar now; the close is sent when the window lapses. */
function startClose(id: string, g: RepoGroup, args: PRArgs, focused: boolean): void {
  outcomes.delete(id);
  closing.set(id, {
    args,
    key: groupKey(g),
    listedID: g.repo_id,
    timer: setTimeout(() => {
      sendClose(id);
    }, CLOSE_UNDO_MS),
  });
  paint();
  if (focused) {
    focusRowButton(id, UNDO_LABEL);
  }
}

function undoClose(id: string, focused: boolean): void {
  const held = closing.get(id);
  if (held === undefined) {
    return;
  }
  clearTimeout(held.timer);
  closing.delete(id);
  paint();
  if (focused) {
    focusRowButton(id, CLOSE_LABEL);
  }
}

/** The row comes back with the forge's reason when the close is refused. */
function sendClose(id: string): void {
  const held = closing.get(id);
  if (held === undefined) {
    return;
  }
  clearTimeout(held.timer);
  closing.delete(id);
  const { args } = held;
  const removed = removePRFromGroups(args.forge_id, held.listedID, args.pr_number);
  void closePR.dispatch(args).outcome.then((o) => {
    if (o.status === "success") {
      if (removed !== undefined) {
        settleRemoval(removed, o.value.cycle_id);
      }
      return;
    }
    const out = refusedOn(held.key, id, o, "Could not close", CLOSE_LABEL);
    if (out.text !== "") {
      outcomes.set(id, {
        text: out.text,
        error: true,
        cycle: heldEntry(args.forge_id)?.cycle_id ?? "",
      });
    }
    if (removed !== undefined) {
      reinsertPRInGroups(removed);
    }
    paint();
  });
}

function flushCloses(): void {
  for (const id of [...closing.keys()]) {
    sendClose(id);
  }
}

function sendClosesOnUnload(): void {
  for (const [id, held] of closing) {
    clearTimeout(held.timer);
    closing.delete(id);
    removePRFromGroups(held.args.forge_id, held.listedID, held.args.pr_number);
    sendCloseOnUnload(held.args);
  }
}

function focusRowButton(id: string, label: string): void {
  const row = document
    .getElementById("git-prs-mount")
    ?.querySelector(`[data-pr="${CSS.escape(id)}"]`);
  [...(row?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
    .find((b) => b.textContent.trim() === label)
    ?.focus();
}

function renderClosingRow(pr: PR, id: string): HTMLElement {
  const li = el("li", { className: "git-pr-row git-pr-row-closing", "data-pr": id });
  noteSeq += 1;
  const textId = `git-pr-note-${String(noteSeq)}`;
  const undo = rowButton(UNDO_LABEL, NOTHING_PRESSED);
  undo.setAttribute("aria-describedby", textId);
  undo.addEventListener("click", () => {
    undoClose(id, document.activeElement === undo);
  });
  li.append(
    el("p", { className: "git-pr-row-status", role: "status", id: textId }, closingText(pr)),
    el("div", { className: "git-pr-row-actions" }, undo),
  );
  return li;
}

function renderPRRow(g: RepoGroup, pr: PR): HTMLElement {
  // `data-pr` is the tab's one DOM row identity, the only thing a focus request finds a row by. Built with
  // push-subject.ts's `prIdentity`, which the Go twin is pinned against, so the tab compares and never parses.
  const id = rowId(g, pr);
  if (closing.has(id)) {
    return renderClosingRow(pr, id);
  }
  const li = el("li", { className: "git-pr-row", "data-pr": id });
  appendSummary(li, pr, numberLabel(pr));
  const v = rowView(g, pr);

  const actions = el("div", { className: "git-pr-row-actions" });

  const prRef = {
    forge_id: g.forge_id,
    repo_id: addressOf(g),
    owner: g.owner,
    name: g.name,
    pr_number: pr.number,
  };

  // No accent on a per-row action: `btn-primary` marks the one thing to do on a surface, and a per-row count scales
  // with the open PRs. Accents stay section-level (`+ New PR`, Push, Commit).
  const merge = rowButton(MERGE_LABEL, v.busy);
  const mergeText = mergeNote(v.merge);
  if (mergeText === "") {
    merge.setAttribute("data-tooltip", "Merge this pull request");
  } else {
    merge.disabled = true;
    appendNote(li, merge, "merge", mergeText, v.merge.state);
  }
  merge.addEventListener("click", () => {
    void (async () => {
      const strategy = await openMergeMethodDialog({
        title: "Merge pull request",
        message: `PR #${String(pr.number)} "${pr.title}"`,
        confirmLabel: "Merge",
        forge_id: g.forge_id,
        repo_id: addressOf(g),
      });
      if (strategy === null) {
        return;
      }
      await pressRow(g, id, merge, MERGE_LABEL, async () => {
        // head_sha pins the merge to the rendered commit: if something pushed since, the forge refuses instead of landing an
        // unreviewed commit.
        const o = await mergePR.dispatch({
          ...prRef,
          forge_kind: g.forge_kind,
          head_sha: pr.head_sha ?? "",
          strategy,
        }).outcome;
        if (o.status !== "success") {
          return refusedOn(groupKey(g), id, o, "Could not merge", MERGE_LABEL);
        }
        // Only a merged outcome removes the row at once: an accepted or queued merge leaves the PR open.
        if (o.value.outcome.state === "merged") {
          const removed = removePRFromGroups(g.forge_id, g.repo_id, pr.number);
          if (removed !== undefined) {
            settleRemoval(removed, o.value.cycle_id);
          }
          return { ok: true, text: "" };
        }
        return await followMerge(g, pr, prRef, o.value.outcome.state);
      });
    })();
  });
  actions.appendChild(merge);

  // Checks unsettled: offer to hand the merge to the forge rather than a disabled button.
  if (canArmAutoMerge(pr)) {
    const arm = rowButton(ARM_LABEL, v.busy);
    arm.setAttribute("data-tooltip", "Let the forge merge this once its checks pass");
    arm.addEventListener("click", () => {
      void (async () => {
        const strategy = await openMergeMethodDialog({
          title: "Merge when green",
          message: `PR #${String(pr.number)} "${pr.title}" merges once its checks pass.`,
          confirmLabel: "Arm auto-merge",
          forge_id: g.forge_id,
          repo_id: addressOf(g),
        });
        if (strategy === null) {
          return;
        }
        await pressRow(g, id, arm, ARM_LABEL, async () => {
          const o = await armAutoMerge.dispatch({
            ...prRef,
            forge_kind: g.forge_kind,
            head_sha: pr.head_sha ?? "",
            strategy,
          }).outcome;
          return o.status === "success"
            ? { ok: true, text: "Auto-merge armed: the forge merges this once its checks pass." }
            : refusedOn(groupKey(g), id, o, "Could not arm auto-merge", ARM_LABEL);
        });
      })();
    });
    actions.appendChild(arm);
  }

  // A failed check here is most often flaky, so a retry beats a context switch.
  if (v.rerun.offer) {
    const rerun = rowButton(RERUN_LABEL, v.busy);
    if (v.rerun.reason === "") {
      rerun.setAttribute("data-tooltip", "Re-run the failed CI jobs");
    } else {
      rerun.disabled = true;
      appendNote(li, rerun, "rerun", rerunNote(v.rerun));
    }
    rerun.addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog(
          `Re-run failed checks on PR #${pr.number}?`,
          "Re-run",
          "normal",
        );
        if (!ok) {
          return;
        }
        await pressRow(g, id, rerun, RERUN_LABEL, async () => {
          // The same pin as the merge: the chip is pr.head_sha's folded state, so the re-run must name that commit.
          const o = await rerunChecks.dispatch({ ...prRef, head_sha: pr.head_sha ?? "" }).outcome;
          if (o.status === "success") {
            return { ok: true, text: "Re-run started." };
          }
          const refusal =
            o.status === "error" ? rerunRefusal(o.error.code, o.error.message) : undefined;
          if (refusal === undefined) {
            return refusedOn(groupKey(g), id, o, "Could not re-run checks", RERUN_LABEL);
          }
          // Every later press on this repository is refused the same way.
          rerunRefused.set(join(g.forge_id, g.repo_id), refusal);
          paint();
          return { ok: false, text: "" };
        });
      })();
    });
    actions.appendChild(rerun);
  }

  // Reopen mirrors Close and renders on the state that earns it.
  if (pr.state !== "open") {
    const reopen = rowButton(REOPEN_LABEL, v.busy);
    reopen.setAttribute("data-tooltip", "Reopen this pull request");
    reopen.addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog(`Reopen PR #${pr.number}?`, "Reopen", "normal");
        if (!ok) {
          return;
        }
        await pressRow(g, id, reopen, REOPEN_LABEL, async () => {
          const o = await reopenPR.dispatch(prRef).outcome;
          return o.status === "success"
            ? { ok: true, text: "Reopened." }
            : refusedOn(groupKey(g), id, o, "Could not reopen", REOPEN_LABEL);
        });
      })();
    });
    actions.appendChild(reopen);
  }

  // No confirmation: the Undo bar the row turns into is the guard.
  const close = el(
    "button",
    { type: "button", className: "btn-small btn-danger" },
    CLOSE_LABEL,
  ) as HTMLButtonElement;
  close.addEventListener("click", () => {
    startClose(id, g, prRef, document.activeElement === close);
  });

  if (v.movedTo !== undefined) {
    actions.appendChild(useButton(g, id, v.movedTo));
  }
  actions.appendChild(close);

  li.appendChild(actions);
  li.appendChild(
    el(
      "p",
      { className: v.failed ? "git-pr-row-status err" : "git-pr-row-status", role: "status" },
      v.outcome,
    ),
  );
  return li;
}

/**
 * Open the New PR dialog for the workspace clone checked out in `repoName`, with the source branch prefilled (the
 * Changes tab's "Open PR" hint).
 */
export async function openNewPRForRepo(repoName: string, sourceBranch: string): Promise<void> {
  if (!inventoryHeld()) {
    try {
      await refreshPRs();
    } catch {
      /* ignore: the lookup below decides */
    }
  }
  // An ssh clone joins no connection, so a listed repository of the same name is the remaining way to find it.
  const group = cloneGroup(repoName) ?? getPRGroups().find((g) => g.name === repoName);
  if (group === undefined) {
    // Not in any forge group: an inline error in the mount.
    const root = document.getElementById("git-prs-mount");
    if (root !== null) {
      root.replaceChildren(
        el(
          "div",
          { className: "git-multirepo-error" },
          "No connected forge knows about ",
          el("strong", null, repoName),
          ". Connect one in Sources.",
        ),
      );
    }
    return;
  }
  openNewPRDialog(group, sourceBranch);
}

/** The repository the clone in `dir` joins, as a group: its section's when it has
 *  open pull requests, else one holding none. */
function cloneGroup(dir: string): RepoGroup | undefined {
  const c = cloneInDir(dir);
  if (c === undefined) {
    return undefined;
  }
  const listed = getPRGroups().find((g) => g.forge_id === c.forge_id && g.repo_id === c.repo_id);
  if (listed !== undefined) {
    return listed;
  }
  const f = connectedForges().find((x) => x.id === c.forge_id);
  if (f === undefined) {
    return undefined;
  }
  return {
    forge_id: f.id,
    forge_kind: f.kind,
    forge_host: f.host,
    repo_id: c.repo_id,
    owner: "",
    name: dir,
    full_name: dir,
    prs: [],
  };
}

// Created once and reused so backdrop and Escape listeners do not stack. Unlike the permission prompts, this is a
// re-openable form, so backdrop and Escape dismissal are on.
let prDialogCtl: DialogController | null = null;
function prDialogController(dlg: HTMLDialogElement): DialogController {
  prDialogCtl ??= createDialog(dlg, { closeOnBackdrop: true, closeOnEscape: true });
  return prDialogCtl;
}

/** Each open builds fresh buttons from these, dropping the last open's listeners and outcome state. */
let prDialogButtons: {
  submit: HTMLButtonElement;
  generate: HTMLButtonElement;
  close: HTMLButtonElement[];
} | null = null;

function freshButton(old: HTMLButtonElement, pristine: HTMLButtonElement): HTMLButtonElement {
  const btn = pristine.cloneNode(true) as HTMLButtonElement;
  old.replaceWith(btn);
  return btn;
}

function openNewPRDialog(g: RepoGroup, sourceBranch = ""): void {
  const dlg = document.getElementById("pr-create-dialog") as HTMLDialogElement | null;
  if (dlg === null) {
    return;
  }
  const dialogCtl = prDialogController(dlg);

  const baseInput = document.getElementById("pr-base") as HTMLInputElement | null;
  const headInput = document.getElementById("pr-head") as HTMLInputElement | null;
  const titleInput = document.getElementById("pr-title") as HTMLInputElement | null;
  const bodyInput = document.getElementById("pr-body") as HTMLTextAreaElement | null;
  const draftInput = document.getElementById("pr-draft") as HTMLInputElement | null;
  const status = document.getElementById("pr-dialog-status");
  const submitBtn = document.getElementById("pr-submit-btn") as HTMLButtonElement | null;
  const generateBtn = document.getElementById("pr-generate-btn") as HTMLButtonElement | null;

  if (
    !baseInput ||
    !headInput ||
    !titleInput ||
    !bodyInput ||
    !draftInput ||
    !submitBtn ||
    !generateBtn
  ) {
    console.error("PR dialog missing required elements");
    return;
  }
  const closeBtns = [...dlg.querySelectorAll<HTMLButtonElement>("[data-pr-close]")];
  prDialogButtons ??= {
    submit: submitBtn.cloneNode(true) as HTMLButtonElement,
    generate: generateBtn.cloneNode(true) as HTMLButtonElement,
    close: closeBtns.map((b) => b.cloneNode(true) as HTMLButtonElement),
  };
  const pristine = prDialogButtons;

  const setStatus = (text: string, tone: "" | "ok" | "err"): void => {
    if (status !== null) {
      status.textContent = text;
      status.className = tone === "" ? "forge-status" : `forge-status ${tone}`;
    }
  };

  // Pre-fill base and head; the AI drafts the body.
  baseInput.value = "main";
  headInput.value = sourceBranch;
  // Fixed only when the caller named the branch (the Changes tab's post-push hint).
  headInput.readOnly = sourceBranch !== "";
  titleInput.value = "";
  bodyInput.value = "";
  draftInput.checked = false;

  const newSubmit = freshButton(submitBtn, pristine.submit);
  const newGenerate = freshButton(generateBtn, pristine.generate);
  const freshClose = closeBtns.map((b, i) => {
    const fresh = freshButton(b, pristine.close[i] ?? b);
    fresh.addEventListener("click", () => {
      dialogCtl.close();
    });
    return fresh;
  });

  let generateAbort = new AbortController();
  dlg.addEventListener(
    "close",
    () => {
      generateAbort.abort();
    },
    { once: true },
  );

  /** Resolves early, saying nothing, once a newer draft or the dialog's close owns the button. */
  const generate = async (ctrl: AbortController): Promise<void> => {
    setStatus("Generating description…", "");
    const res = await apiPost<{ output?: string; error?: string }>(
      `/api/git/pr-description`,
      // The clone's own directory when the inventory joins one; it need not share the forge's name.
      {
        repo: cloneDirOf(g.forge_id, g.repo_id) ?? g.name,
        branch: baseInput.value.trim() || "main",
      },
      ctrl.signal,
    );
    if (ctrl.signal.aborted) {
      return;
    }
    if (res === null) {
      setStatus("Network error.", "err");
      throw new Error("network error");
    }
    if (res.error !== undefined && res.error !== "") {
      setStatus(res.error, "err");
      throw new Error(res.error);
    }
    // The server's single {output} description fills the body; the title stays the user's.
    if (res.output !== undefined && res.output !== "" && bodyInput.value === "") {
      bodyInput.value = res.output;
    }
    setStatus("Description generated. Edit and submit.", "ok");
  };

  const draft = (): void => {
    generateAbort.abort();
    generateAbort = new AbortController();
    const ctrl = generateAbort;
    void withAsyncFeedback(newGenerate, () => generate(ctrl), { keepLabel: true });
  };
  newGenerate.addEventListener("click", draft);

  /** It names and links the PR, and offers nothing that could open it again. */
  const opened = (pr: PR): void => {
    setStatus(`Opened pull request #${String(pr.number)}.`, "ok");
    const url = pr.url ?? "";
    if (status !== null && isSafeURL(url)) {
      status.append(
        " ",
        el(
          "a",
          { href: url, target: "_blank", rel: "noopener noreferrer" },
          `Open #${String(pr.number)} on ${new URL(url).host}`,
        ),
      );
    }
    // The utility class: `.btn-small` declares a display that beats `[hidden]`.
    newSubmit.classList.add("hidden");
    newGenerate.classList.add("hidden");
    for (const b of freshClose) {
      if (b.textContent.trim() !== "") {
        b.textContent = "Close";
      }
    }
  };

  newSubmit.addEventListener("click", () => {
    void withAsyncFeedback(
      newSubmit,
      async () => {
        setStatus("Opening the pull request…", "");
        const o = await createPR.dispatch({
          forge_id: g.forge_id,
          repo_id: addressOf(g),
          owner: g.owner,
          name: g.name,
          source_branch: headInput.value.trim(),
          target_branch: baseInput.value.trim(),
          title: titleInput.value.trim(),
          body: bodyInput.value,
          draft: draftInput.checked,
        }).outcome;
        if (o.status !== "success") {
          const why = o.status === "error" ? o.error.message : "it was cancelled";
          setStatus(`Could not open the pull request. ${why}`, "err");
          throw new Error(why);
        }
        opened(o.value);
        // The list reads it at its next cycle; ask for one so it is there when the reader looks.
        void requestPRCycle.dispatch();
      },
      { keepLabel: true },
    );
  });

  dialogCtl.open();
  // Started at once to overlap with the user reading the form; a failure leaves the fields blank.
  draft();
}
