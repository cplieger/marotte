// ---------------------------------------------------------------------------
// Git Changes tab: one collapsible section per repo in #git-changes-mount,
// painted from one /api/git/status-all read: branch and ahead/behind, the
// sync actions, the staged and unstaged file groups, and the commit box.
// Repainted on filter input, after every press, and on tab activate.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { onSSE } from "./bus.js";
import {
  ICON_REFRESH,
  ICON_REPO_EMPTY,
  ICON_FILTER,
  ICON_GIT_DOWN_ARROW,
  ICON_WARN,
} from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { preserveGitScroll } from "./git-scroll.js";
import {
  stage,
  discard,
  pull,
  pullAll,
  push,
  stash,
  stashPop,
  unstage,
} from "./actions/git-changes.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { gitRepoSkeleton, paintPlaceholder } from "./skeleton.js";
import { reconcile } from "./reconcile.js";
import { openChange } from "./navigate.js";
import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { escAttr as escapeHTML } from "./strings.js";
import {
  renderRecentCommits,
  renderCommitArea,
  refusalOf,
  type CommitDeps,
  type PressOpts,
  type PressRun,
  type Refusal,
} from "./git-changes-commit.js";

// --- Wire types ---

import type {
  GitFileEntry as FileEntry,
  GitPullResult,
  GitRepoStatus as RepoStatus,
} from "./git-types.js";
import {
  statusLetter,
  describeStatus,
  stashableCount,
  changedPathCount,
  distinctPaths,
  partiallyStagedPaths,
  isPullHeld,
  pullHeldWord,
} from "./git-types.js";

interface StatusAllResponse {
  repos: RepoStatus[];
}

// --- State ---

let inited = false;
let lastStatusAll: RepoStatus[] = [];
/** Whether `status-all` has ANSWERED. `lastStatusAll` initialises to `[]` and the
 *  empty-state row it paints is unkeyed, so nothing else distinguishes a clean
 *  worktree from one this client has never read. */
let statusAnswered = false;
let filterText = "";
let refreshGeneration = 0;
let refreshAbort: AbortController | null = null;
/** Long-lived abort controller for the recent-commits log fetch — only
 *  aborted on tab teardown, NOT on every repaint. */
const diffAbortCtrl = new AbortController();
registerCleanup(() => {
  refreshAbort?.abort();
});
registerCleanup(() => {
  diffAbortCtrl.abort();
});

/** Repos that recently received a successful push. Used to surface a
 *  contextual "Open PR" hint in their section header for a few
 *  re-renders after the push. */
const recentlyPushed = new Set<string>();
const RECENTLY_PUSHED_TTL_MS = 60_000;

// Bug 1: Preserve commit message textarea values across re-renders.
const commitMessages = new Map<string, string>();

/** The press each repository is running. Its git actions share one queue (the
 *  `git:<repo>` scope), so the section's other controls wait while it runs, and
 *  a repaint shows the press on the control that started it. */
const repoPresses = new Map<string, { key: string; done: Promise<void> }>();

/** Repositories with a press waiting on its confirm: a second press meanwhile
 *  would confirm and send twice. */
const confirmingRepos = new Set<string>();

/** Why a repository's last press did not land, said at the top of its section
 *  until its next press. */
const pressNotes = new Map<string, Refusal>();

// Bug 3: Track user-toggled collapse state (repos the user manually collapsed).
const userCollapsedRepos = new Set<string>();
const userExpandedRepos = new Set<string>();

/** What the last Pull all could NOT do, per repo — the input to the mark on a
 *  repo's block. Only the two verdicts a reader has to act on are kept
 *  (`isPullHeld`); a pulled or skipped repo needs no mark.
 *
 *  An entry survives a refresh, because the statement it makes is about a pass
 *  that happened and a reader who walked away should still find it. It is
 *  dropped the moment the repo stops being behind: there is nothing left to pull
 *  then, so the flag has stopped describing anything. Replaced wholesale by the
 *  next pass. */
const pullFlags = new Map<string, GitPullResult>();

// Deferred paint: set when paint bails due to focused textarea.
let paintDeferred = false;

/** Build the deps object for commit rendering functions. */
function commitDeps(): CommitDeps {
  return { commitMessages, diffAbort: diffAbortCtrl, press: pressControl };
}

// --- Public API ---

/** The tab's filter box.
 *
 *  A POPUP since the search-box audit. It was an `<input type="search">`
 *  hand-authored into index.html with its own magnifier SVG beside it — the one
 *  page-level search in the app that did not go through the shared shell, and the
 *  reason the toolbar's magnifier was a DEAD DOOR on the git view: `find-dispatch`
 *  had no branch for this tab, so Ctrl-F fell through to the transcript's handler,
 *  which declined because the chat view was hidden.
 *
 *  Its close CLEARS, which matters more here than on a permanent box: this filter
 *  also drives each repo section's expansion default, so a query left applied
 *  behind a closed box would leave the page both narrowed and expanded with
 *  nothing on screen explaining either. */
export const changesFind: SearchPopup = createSearchPopup<null>({
  id: "git-changes-filter",
  kind: "filter",
  label: "Filter changes by path",
  placeholder: "Filter by path\u2026",
  host: () => document.getElementById("git-view"),
  query: (q) => {
    filterText = q.toLowerCase();
    return null;
  },
  render: () => {
    paint();
  },
});

/** Initialise the Changes tab. Wires the global refresh button and the SSE
 *  forge-changed event. Idempotent. */
export function initChangesTab(): void {
  if (inited) {
    return;
  }
  inited = true;
  // Fire deferred paint when commit textarea loses focus.
  // Scoped to the mount container so the listener is tied to the tab's
  // DOM lifetime. focusout bubbles, so it reaches the container.
  const changesMount = document.getElementById("git-changes-mount");
  changesMount?.addEventListener(
    "focusout",
    (e) => {
      if (
        paintDeferred &&
        e.target instanceof HTMLTextAreaElement &&
        e.target.classList.contains("git-commit-input")
      ) {
        paint();
      }
    },
    { passive: true },
  );

  const refreshBtn = document.getElementById("git-refresh-all-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      // Default keepLabel=false: the icon is replaced by the
      // spinner while the refresh is in flight (then ✓/✗). The
      // button has no text label to keep, so this reads cleaner
      // than the icon + spinner side-by-side variant.
      // Explicit user refresh → opt into the server-side git fetch
      // so ahead/behind reflects the real remote state (18-F3).
      void withAsyncFeedback(refreshBtn, async () => {
        if (!(await refreshChanges(true))) {
          throw new Error("the status could not be read");
        }
      });
    });
  }

  const pullAllBtn = document.getElementById("git-pull-all-btn") as HTMLButtonElement | null;
  if (pullAllBtn !== null) {
    pullAllBtn.innerHTML = ICON_GIT_DOWN_ARROW;
    const status = el("span", { className: "git-tab-toolbar-error", role: "status" });
    pullAllBtn.before(status);
    // The pass's refresh runs once the button settles, so the loading binding
    // below and the button's own feedback end together.
    pullAllBtn.addEventListener("click", () => {
      status.textContent = "";
      let moved: string[] = [];
      void withAsyncFeedback(pullAllBtn, async () => {
        moved = await runPullAll(status);
      }).then(() => refreshChanges(false, moved));
    });
    // Bound once: the toolbar sits outside the repaint cycle. `git.pull` is in
    // the set so a single repo's Pull disables this button too: they are the
    // same operation at two scopes, and running both at once serializes on the
    // server anyway.
    bindLoadingState(["git.pull_all", "git.pull"], pullAllBtn);
  }

  // Refetch when the agent emits anything that touches files (it
  // emits this after every turn that wrote something), and when forge
  // accounts change (clones / removes ripple into the repo list).
  // Debounced so SSE bursts coalesce into a single refresh.
  let sseRefreshTimer: ReturnType<typeof setTimeout> | undefined;
  const debouncedRefresh = (): void => {
    clearTimeout(sseRefreshTimer);
    sseRefreshTimer = setTimeout(() => {
      void refreshChanges();
    }, 300);
  };
  onSSE("turn_closed", debouncedRefresh);
  onSSE("forges_changed", debouncedRefresh);
}

/** Read /api/git/status-all and repaint; a newer call supersedes an older
 *  one. Answers false when the status could not be read. `doFetch` adds a
 *  per-repo `git fetch` (?fetch=1), reserved for explicit navigation (the
 *  Refresh-all button, git-tab activation): SSE and post-press refreshes
 *  stay fetch-free so agent turns never fan out N network fetches (18-F3).
 *  `moved` names repositories a press just changed: the server answers that
 *  read after rescanning them, where an unnamed read answers from its last
 *  scan, which predates the press. */
export async function refreshChanges(
  doFetch = false,
  moved: readonly string[] = [],
): Promise<boolean> {
  refreshAbort?.abort();
  const ctrl = new AbortController();
  refreshAbort = ctrl;
  const gen = ++refreshGeneration;
  const signal = AbortSignal.any([ctrl.signal, AbortSignal.timeout(15_000)]);
  const url = doFetch
    ? "/api/git/status-all?fetch=1"
    : moved.length > 0
      ? `/api/git/status-all?paths=${encodeURIComponent(moved.join(","))}`
      : "/api/git/status-all";

  // Gated on NOT-YET-ANSWERED, not on the container being empty: `status-all` is
  // polled, so an ungated skeleton would paint over real content several times a
  // minute, and a CLEAN worktree is an answer rather than an absence.
  const skeleton = statusAnswered ? null : skeletonTiming(() => gitChangesSkeleton(), { signal });

  try {
    const data = await apiGet<StatusAllResponse>(url, signal);
    if (gen < refreshGeneration) {
      return true;
    } // stale — a newer call supersedes
    if (data === null) {
      if (ctrl.signal.aborted) {
        return true;
      }
      paintError("Failed to load git status.");
      return false;
    }
    lastStatusAll = data.repos;
    statusAnswered = true;
    paint();
    return true;
  } finally {
    skeleton?.cancel();
  }
}

/** Placeholder sections while `status-all` is in flight. Returns the teardown
 *  `skeletonTiming` calls. */
function gitChangesSkeleton(): () => void {
  // No `label`: this tab issues ONE request, so a static line would be chrome
  // that says nothing.
  return paintPlaceholder(document.getElementById("git-changes-mount"), () =>
    gitRepoSkeleton({ widths: ["38%", "52%", "30%"] }),
  );
}

/** Pull every repo a fast-forward is safe for, recording the two verdicts a
 *  reader has to act on as marks on their repo blocks (the action's toast
 *  carries the summary; the pass judged what was safe). A refused pass rejects
 *  after `status` says why. The caller's refresh is fetch-free: the pass has
 *  just fetched every remote, so the refs on disk are already fresh. Answers
 *  the repositories the pass pulled or held, the ones whose status it moved. */
async function runPullAll(status: HTMLElement): Promise<string[]> {
  const o = await pullAll.dispatch().outcome;
  if (o.status !== "success") {
    const why = o.status === "error" ? o.error.message : "it was cancelled";
    status.textContent = `Could not pull every repository. ${why}`;
    throw new Error(why);
  }
  pullFlags.clear();
  for (const r of o.value) {
    if (isPullHeld(r)) {
      pullFlags.set(r.repo, r);
    }
  }
  return o.value.filter((r) => r.verdict !== "skipped").map((r) => r.repo);
}

/** `n` files, as a sentence counts them. */
function files(n: number): string {
  return `${String(n)} file${n === 1 ? "" : "s"}`;
}

function pressedSection(repo: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(
    `#git-changes-mount section[data-repo="${CSS.escape(repo)}"]`,
  );
}

/** Hold `btn` while another press on its repository runs: disabled, and marked
 *  so the release gives it back. */
function hold(btn: HTMLButtonElement): void {
  if (!btn.disabled) {
    btn.disabled = true;
    btn.dataset["pressHeld"] = "";
  }
}

/** End `repo`'s press: forget it and give its section's held controls back. */
function release(repo: string): void {
  repoPresses.delete(repo);
  const held = pressedSection(repo)?.querySelectorAll<HTMLButtonElement>("[data-press-held]");
  for (const b of held ?? []) {
    delete b.dataset["pressHeld"];
    b.disabled = false;
  }
}

/** Wire `btn` as the control that runs `run` on `repo` under `key`. A control
 *  built while that press runs shows it; one built while another press on the
 *  repository runs waits for it. */
function pressControl(
  btn: HTMLButtonElement,
  repo: string,
  key: string,
  run: PressRun,
  opts: PressOpts = {},
): HTMLButtonElement {
  btn.dataset["press"] = key;
  const held = repoPresses.get(repo);
  if (held?.key === key) {
    void withAsyncFeedback(btn, () => held.done, { keepLabel: true });
  } else if (held !== undefined) {
    hold(btn);
  }
  btn.addEventListener("click", (ev) => {
    ev.stopPropagation();
    void press(btn, repo, key, run, opts);
  });
  return btn;
}

/** One press: its confirm, then its request with the button busy and the
 *  section's other controls held, then the section read again, with a refusal
 *  said at its top. */
async function press(
  btn: HTMLButtonElement,
  repo: string,
  key: string,
  run: PressRun,
  opts: PressOpts,
): Promise<void> {
  if (repoPresses.has(repo) || confirmingRepos.has(repo)) {
    return;
  }
  if (opts.confirm !== undefined) {
    confirmingRepos.add(repo);
    let ok: boolean;
    try {
      ok = await opts.confirm();
    } finally {
      confirmingRepos.delete(repo);
    }
    if (!ok || repoPresses.has(repo)) {
      return;
    }
  }
  pressNotes.delete(repo);
  pressedSection(repo)?.querySelector("[data-press-note]")?.remove();
  const record = { key, done: Promise.resolve() };
  repoPresses.set(repo, record);
  const others = pressedSection(repo)?.querySelectorAll<HTMLButtonElement>("[data-press]");
  for (const b of others ?? []) {
    if (b !== btn) {
      hold(b);
    }
  }
  // Registered and holding before the request starts, so a repaint the request
  // causes shows it.
  record.done = settlePress(repo, run, opts);
  await withAsyncFeedback(btn, () => record.done, { keepLabel: true });
}

async function settlePress(repo: string, run: PressRun, opts: PressOpts): Promise<void> {
  let refusal: Refusal | null;
  try {
    refusal = await run();
  } finally {
    release(repo);
  }
  if (refusal !== null) {
    pressNotes.set(repo, refusal);
  }
  if (opts.refresh !== false) {
    await refreshChanges(false, [repo]);
  } else if (refusal !== null) {
    paint();
  }
  if (refusal !== null) {
    throw new Error(refusal.detail);
  }
}

// --- Render ---

function paint(): void {
  preserveGitScroll(paintInner);
}

function paintInner(): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) {
    return;
  }

  // Bug 1: Skip re-render entirely if a commit textarea is focused
  // to avoid destroying user input mid-typing.
  const focused = document.activeElement;
  if (focused instanceof HTMLTextAreaElement && focused.classList.contains("git-commit-input")) {
    paintDeferred = true;
    return;
  }
  paintDeferred = false;

  // Smell fix: prune module-level Sets/Maps to keys present in lastStatusAll.
  const activeRepos = new Set(lastStatusAll.map((r) => r.repo));
  for (const k of userCollapsedRepos) {
    if (!activeRepos.has(k)) {
      userCollapsedRepos.delete(k);
    }
  }
  for (const k of userExpandedRepos) {
    if (!activeRepos.has(k)) {
      userExpandedRepos.delete(k);
    }
  }
  for (const k of commitMessages.keys()) {
    if (!activeRepos.has(k)) {
      commitMessages.delete(k);
    }
  }
  for (const k of pressNotes.keys()) {
    if (!activeRepos.has(k)) {
      pressNotes.delete(k);
    }
  }
  // A pull flag reports what the last pass could not do, so it expires when the
  // repo stops being behind — nothing is left to pull, so the mark describes
  // nothing — and when the repo goes away. Keyed on `behind` rather than on the
  // hazard itself: resolving a conflict does not pull the repo, and the mark
  // should stand until something does.
  for (const k of pullFlags.keys()) {
    const repo = lastStatusAll.find((r) => r.repo === k);
    if (repo === undefined || repo.behind === 0) {
      pullFlags.delete(k);
    }
  }

  // Bug 1: Capture current commit messages before destroying DOM.
  for (const ta of root.querySelectorAll<HTMLTextAreaElement>(".git-commit-input[data-repo]")) {
    const repo = ta.dataset["repo"];
    if (repo) {
      commitMessages.set(repo, ta.value);
    }
  }

  if (lastStatusAll.length === 0) {
    root.innerHTML = renderEmptyState({
      icon: ICON_REPO_EMPTY,
      title: "No repositories cloned",
      hint: "Open the <strong>Sources</strong> tab to clone one.",
    });
    return;
  }

  // Which repos survive the filter, and which of their files. ONE predicate,
  // read here to build the reconcile list and again in renderRepoSection to
  // build the rows — it used to be written twice, and the two copies disagreed:
  // this one kept a repo whose NAME matched, that one then applied the path
  // filter regardless and emptied it.
  const visibleRepos = lastStatusAll.filter((r) => filteredFilesFor(r) !== null);

  // Drop any prior non-keyed empty-state placeholder before reconciling.
  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }

  // Every repo dropped, which can ONLY be the filter: filteredFilesFor returns
  // the file array rather than null whenever filterText is empty, so an
  // unfiltered paint always has one section per repo.
  //
  // There is deliberately no aggregate "everything is quiet" state beside this
  // one. It would replace the repo list on a workspace whose repos are merely
  // behind origin, ahead of it, or holding a stash, hiding both their sync
  // buttons and their branch chip — the only door to the branch switcher. That
  // a workspace has nothing pending anywhere is the sidebar git badge's fact.
  if (visibleRepos.length === 0) {
    reconcile(root, [] as RepoStatus[], {
      key: (r) => r.repo,
      mount: () => el("div"),
    });
    root.innerHTML = renderEmptyState({
      icon: ICON_FILTER,
      title: "No matching changes",
      hint: "Adjust your filter to see more.",
    });
    return;
  }

  // Outer reconcile: keep section identity (and inline textareas /
  // commit-message drafts inside) across paints. Body content is
  // rebuilt fresh on update via renderRepoSection.
  reconcile(root, visibleRepos, {
    key: (r: RepoStatus) => r.repo,
    mount: (r: RepoStatus) => {
      const section = renderRepoSection(r);
      return section ?? el("section");
    },
    update: (section: HTMLElement, r: RepoStatus) => {
      const fresh = renderRepoSection(r);
      if (fresh === null) {
        return;
      }
      section.className = fresh.className;
      section.replaceChildren(...Array.from(fresh.childNodes));
    },
  });
}

// --- Empty-state markup helpers ---

function renderEmptyState(opts: { icon: string; title: string; hint: string }): string {
  return `
    <div class="git-multirepo-empty">
      <div class="git-multirepo-empty-icon">${opts.icon}</div>
      <div class="git-multirepo-empty-title">${opts.title}</div>
      <div class="git-multirepo-empty-hint">${opts.hint}</div>
    </div>
  `;
}

function paintError(msg: string): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) {
    return;
  }
  root.replaceChildren(el("div", { className: "git-multirepo-error" }, msg));
}

/** The files of `r` the current filter admits, or null when the filter
 *  excludes the repo entirely.
 *
 *  The whole filter rule, in one place, because it has two readers: paint
 *  builds the reconcile list from it and renderRepoSection builds the rows.
 *
 *  A REPO-NAME match admits every file in it. Naming a repo is a request to
 *  see that repo, and applying the path filter underneath it emptied the
 *  section instead: a repo whose changed paths did not happen to repeat the
 *  repo name rendered "No paths match the filter." under its own heading.
 *
 *  An admitted repo always yields a non-empty file list OR has nothing
 *  uncommitted, which is what makes "no paths match" unreachable and lets
 *  renderRepoSection's empty case be the honest one. */
function filteredFilesFor(r: RepoStatus): FileEntry[] | null {
  if (filterText === "" || r.repo.toLowerCase().includes(filterText)) {
    return r.files;
  }
  const hits = r.files.filter((f) => f.path.toLowerCase().includes(filterText));
  return hits.length > 0 ? hits : null;
}

function renderRepoSection(r: RepoStatus): HTMLElement | null {
  const filteredFiles = filteredFilesFor(r);
  if (filteredFiles === null) {
    return null;
  }
  // Hide clean repos by default.
  //
  // A repo the last Pull all marked needs no clause here: the pass only judges a
  // repo that is BEHIND, and the mark is pruned the moment it stops being
  // (paintInner), so `r.behind > 0` already opens every marked section and the
  // reason in its body is read without a click. One was written, and the red
  // check showed nothing could make it matter.
  const dataDefault = r.has_dirty || r.ahead > 0 || r.behind > 0;
  // A filter OUTRANKS the reader's latch: every section it admits holds a row it
  // selected, and a selected row inside a collapsed region is one the reader
  // cannot see and nothing on screen says exists. The latch is the resting
  // arrangement and is read again once the box is empty.
  let expandedDefault: boolean;
  if (filterText !== "") {
    expandedDefault = true;
  } else if (userCollapsedRepos.has(r.repo)) {
    expandedDefault = false;
  } else if (userExpandedRepos.has(r.repo)) {
    expandedDefault = true;
  } else {
    expandedDefault = dataDefault;
  }

  const section = el("section", { className: "git-repo-section", "data-repo": r.repo });

  // Header — the disclosure trigger. A native <button>, so createDisclosure
  // handles Enter/Space through the native click; no extra keydown wiring.
  const header = el("button", {
    type: "button",
    className: "git-repo-section-header",
  });
  header.innerHTML = renderHeaderHTML(r);
  // The chevron is prepended as an ELEMENT rather than written into the template
  // above, so `chevronEl()` stays the app's one construction of it. Safe against
  // being wiped: `renderHeaderHTML` has exactly this one call site.
  const chevron = chevronEl();
  chevron.classList.add("git-repo-section-chevron");
  header.prepend(chevron);

  // Preserve the branch-chip interception: a click on the branch chip must open
  // the branch switcher, NOT toggle the section. createDisclosure adds its own
  // (bubble-phase) click listener to the trigger, so a dedicated listener on the
  // chip that stops propagation makes the chip win — the disclosure's click
  // never fires for chip clicks.
  const branchChip = header.querySelector<HTMLElement>("[data-branch-trigger]");
  if (branchChip !== null) {
    branchChip.addEventListener("click", (ev) => {
      ev.preventDefault();
      ev.stopPropagation();
      void import("./git-branch-switcher.js")
        .then(({ openBranchSwitcher }) => {
          openBranchSwitcher(r.repo, branchChip);
        })
        .catch(() => {
          /* noop */
        });
    });
  }
  section.appendChild(header);

  // Body — the collapsing disclosure region. createDisclosure owns the collapse
  // (inline height 0<->auto, the .uip-disclosure-region clip, aria-hidden/inert);
  // the visual padding + flex layout live on the inner wrapper so they collapse
  // with the height (no residual sliver when closed).
  const body = el("div", { className: "git-repo-section-body" });
  const inner = el("div", { className: "git-repo-section-body-inner" });
  body.appendChild(inner);

  // onToggle persists the user's explicit toggle so re-renders respect it —
  // exactly the bookkeeping the old click handler did.
  createDisclosure(header, body, {
    open: expandedDefault,
    onToggle: (open) => {
      if (open) {
        userExpandedRepos.add(r.repo);
        userCollapsedRepos.delete(r.repo);
      } else {
        userCollapsedRepos.add(r.repo);
        userExpandedRepos.delete(r.repo);
      }
    },
  });

  if (!r.is_repo) {
    inner.append(el("div", { className: "git-repo-row-error" }, "Not a git repository."));
    section.appendChild(body);
    return section;
  }

  // Action bar — omitted entirely when the repo state services no action.
  // Every button in it is state-gated (see renderActionBar), so a clean,
  // in-sync, stash-free repo produced an EMPTY bar: a zero-height flex child
  // that still consumes the column's `gap: var(--sp-3)`, which read as 12px of
  // dead space above "Clean." with nothing to account for it.
  const actionBar = renderActionBar(r);
  if (actionBar.childElementCount > 0) {
    inner.appendChild(actionBar);
  }

  // Why the last Pull all left this repo alone, ABOVE the action bar: the bar
  // holds the Pull button this note explains the absence of, so the reason
  // wants to be read first.
  const flag = pullFlags.get(r.repo);
  if (flag !== undefined) {
    const lead = flag.verdict === "failed" ? "Pull failed." : "Not pulled.";
    inner.insertBefore(renderRepoNote(flag.verdict, lead, flag.detail ?? ""), inner.firstChild);
  }
  // Why the last press did not land, above everything: it is the newest thing
  // that happened to this repository.
  const refusal = pressNotes.get(r.repo);
  if (refusal !== undefined) {
    const note = renderRepoNote("failed", refusal.lead, refusal.detail);
    note.dataset["pressNote"] = "";
    inner.insertBefore(note, inner.firstChild);
  }

  // Open-PR hint after a successful push (transient).
  if (recentlyPushed.has(r.repo) && isFeatureBranch(r.branch)) {
    inner.appendChild(renderOpenPRHint(r));
  }

  // The file list, in one group per side of the index.
  //
  // An empty list here means nothing is uncommitted, never "the filter hid
  // everything": filteredFilesFor returns null rather than an empty array in
  // that case, and paint drops the section before it gets here.
  //
  // The sentence names the WORKING TREE and not the repo, because this row
  // renders directly under the sync actions: a repo behind origin, ahead of it,
  // or holding a stash reaches it with Pull, Push or Pop right above, and
  // "Clean." read as a verdict on those too. Scoped, it also earns its place
  // beside Pull, which cannot conflict with local edits there are none of.
  if (filteredFiles.length === 0) {
    inner.appendChild(el("div", { className: "git-repo-row-clean" }, "No uncommitted changes."));
  } else {
    inner.appendChild(renderFileList(r, filteredFiles));
  }

  // Commit area, only when something is staged — the index IS the
  // selection, so with nothing staged there is nothing to compose a
  // message for. It gets the staged FILE count (not the entry count) so
  // its button can name what it will commit.
  const stagedCount = changedPathCount(r.files.filter((f) => f.staged));
  if (stagedCount > 0) {
    inner.appendChild(renderCommitArea(r, commitDeps(), stagedCount));
  }

  // Recent commits sub-section (collapsed by default; expand → fetch).
  inner.appendChild(renderRecentCommits(r, commitDeps()));

  section.appendChild(body);
  return section;
}

function renderHeaderHTML(r: RepoStatus): string {
  const ahead = r.ahead > 0 ? ` <span class="git-repo-ahead">↑${r.ahead}</span>` : "";
  const behind = r.behind > 0 ? ` <span class="git-repo-behind">↓${r.behind}</span>` : "";
  const dirty = r.has_dirty
    ? ` <span class="git-repo-dirty-dot" title="Has uncommitted changes" aria-label="dirty"></span>`
    : "";
  const stashes =
    r.stashes > 0
      ? ` <span class="git-repo-stashes" title="${r.stashes} stash${r.stashes === 1 ? "" : "es"}">📦${r.stashes}</span>`
      : "";
  // The Pull-all mark. In the HEADER because a collapsed section is all a reader
  // scanning fifty repos sees, and carrying the warn glyph as well as a hue so
  // the state never rests on colour alone. The tooltip is omitted rather than
  // emptied when there is no detail: an empty one is a tooltip that opens onto
  // nothing.
  const flag = pullFlags.get(r.repo);
  let held = "";
  if (flag !== undefined) {
    const tip = flag.detail ?? "";
    const tipAttr = tip === "" ? "" : ` data-tooltip="${escapeHTML(tip)}"`;
    held =
      ` <span class="git-repo-pull-flag" data-verdict="${escapeHTML(flag.verdict)}"${tipAttr}>` +
      `${ICON_WARN}${escapeHTML(pullHeldWord(flag))}</span>`;
  }
  const branch = escapeHTML(r.branch || "(detached)");
  // The branch chip is a span (not a nested button — buttons can't
  // be inside a button per HTML spec); a dedicated click listener on
  // the chip (wired in renderRepoSection) stops propagation and routes
  // to openBranchSwitcher, so the disclosure trigger's click never fires
  // for chip clicks.
  return `
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">
      <span class="git-repo-branch-chip" data-branch-trigger="${escapeHTML(r.repo)}" data-tooltip="Switch branch">${branch}</span>${ahead}${behind}${stashes}${held}
    </span>
  `;
}

/** A note inside a repo section saying what did not happen to it: why the last
 *  Pull all left it alone (the header has room for a word, the reason needs a
 *  sentence), or why its last press was refused. The detail is the server's,
 *  the side that knows what it found. */
function renderRepoNote(verdict: string, lead: string, detail: string): HTMLElement {
  const box = el("div", { className: "git-repo-note", "data-verdict": verdict });
  const icon = el("span", { className: "git-repo-note-icon", "aria-hidden": "true" });
  icon.innerHTML = ICON_WARN;
  box.append(
    icon,
    el(
      "span",
      { className: "git-repo-note-msg" },
      el("strong", null, lead),
      detail === "" ? "" : ` ${detail}`,
    ),
  );
  return box;
}

/** The repo's SYNC actions: pull, push, stash, pop.
 *
 *  Stage all and Discard all used to lead this bar and have moved onto
 *  the file groups they act on (renderFileGroup), which is what lets
 *  their counts be checked against a heading the reader can see. That
 *  also retired the separator this function inserted between the two
 *  clusters: with one cluster left there is nothing to separate, so the
 *  insert, the trailing-separator cleanup and the `.action-bar-sep` rule
 *  in 14-tools.css are all gone.
 *
 *  Every button stays state-gated (18-F2): an action the repo state
 *  cannot service does not render, so there are no confirm-then-noop
 *  paths and no "Already up to date" pulls. */
function renderActionBar(r: RepoStatus): HTMLElement {
  const bar = el("div", { className: "git-repo-action-bar" });

  const btn = (label: string, title: string): HTMLButtonElement => {
    return el(
      "button",
      {
        type: "button",
        className: "btn-small",
        "data-tooltip": title,
      },
      label,
    ) as HTMLButtonElement;
  };

  if (r.behind > 0) {
    const pullBtn = btn(`Pull ↓${r.behind}`, "git pull --ff-only");
    bar.appendChild(
      pressControl(pullBtn, r.repo, "pull", async () =>
        refusalOf(await pull.dispatch({ repo: r.repo }).outcome, "pull"),
      ),
    );
  }

  if (r.ahead > 0) {
    const pushBtn = btn("Push", `Push ${r.ahead} commit${r.ahead === 1 ? "" : "s"} to origin`);
    pushBtn.classList.add("btn-primary");
    bar.appendChild(
      pressControl(pushBtn, r.repo, "push", async () => {
        const refusal = refusalOf(await push.dispatch({ repo: r.repo }).outcome, "push");
        if (refusal === null) {
          // Mark for "Open PR" hint surfacing on next renders.
          recentlyPushed.add(r.repo);
          setTimeout(() => {
            recentlyPushed.delete(r.repo);
            paint();
          }, RECENTLY_PUSHED_TTL_MS);
        }
        return refusal;
      }),
    );
  }

  // `git stash push` runs WITHOUT `-u`, so an untracked file is not stashable
  // even though the status parse reports it — `stashableCount` (git-types.ts)
  // carries that rule and why the two counts differ. Gating on `dirtyCount`
  // would still offer Stash on a tree whose only changes are new files, and git
  // would answer "No local changes to save": exactly the confirm-then-noop this
  // bar's rule exists to prevent.
  const stashable = stashableCount(r.files);

  if (stashable > 0) {
    const stashBtn = btn("Stash", "Stash uncommitted changes to tracked files");
    bar.appendChild(
      pressControl(stashBtn, r.repo, "stash", async () =>
        refusalOf(await stash.dispatch({ repo: r.repo }).outcome, "stash the changes"),
      ),
    );
  }

  if (r.stashes > 0) {
    const pop = btn("Pop", "Pop the most recent stash");
    bar.appendChild(
      pressControl(pop, r.repo, "pop", async () =>
        refusalOf(await stashPop.dispatch({ repo: r.repo }).outcome, "pop the stash"),
      ),
    );
  }

  return bar;
}

/** The file list, split into a Staged group and a Changes group.
 *
 *  It used to be ONE flat list, sorted staged-first, whose only marker of
 *  staged-ness was a 6% teal wash on the row: 1.09:1 against its own
 *  background in dark and 1.03:1 in light, under the 1.25:1 floor
 *  01-tokens.css declares for a step on its own ramp and well under
 *  WCAG 1.4.11's 3:1 for a state boundary. With the status cell reading
 *  "Modified" either way and the Unstage button hidden until hover, a
 *  staged row and an unstaged one were indistinguishable at rest, and the
 *  only dependable signal that anything was staged at all was the commit
 *  box appearing further down the section.
 *
 *  So staged-ness moves to a HEADER, where it is a count and a word. Each
 *  group also owns the bulk action that acts on exactly what its header
 *  counts, which is what makes those counts checkable: "Discard all" on
 *  the Changes header discards what "Changes (7)" names, and nothing
 *  else. The repo action bar keeps the sync operations.
 *
 *  A group renders only when it has members, so an all-staged tree shows
 *  one group rather than an empty second heading. */
function renderFileList(r: RepoStatus, files: FileEntry[]): HTMLElement {
  const wrap = el("div", { className: "git-file-groups" });
  const partial = partiallyStagedPaths(files);
  const staged = files.filter((f) => f.staged);
  const unstaged = files.filter((f) => !f.staged);

  if (staged.length > 0) {
    wrap.appendChild(renderFileGroup(r, "staged", staged, partial));
  }
  if (unstaged.length > 0) {
    wrap.appendChild(renderFileGroup(r, "unstaged", unstaged, partial));
  }
  return wrap;
}

/** One group: a header stating what it holds and how many, its own bulk
 *  actions, then its rows alphabetically. */
function renderFileGroup(
  r: RepoStatus,
  kind: "staged" | "unstaged",
  files: FileEntry[],
  partial: ReadonlySet<string>,
): HTMLElement {
  const group = el("div", { className: `git-file-group git-file-group-${kind}` });
  const count = changedPathCount(files);
  const label = kind === "staged" ? "Staged" : "Changes";

  const head = el("div", { className: "git-file-group-head" });
  head.append(
    el("span", { className: "git-file-group-label" }, label),
    el(
      "span",
      { className: "git-file-group-count" },
      `${String(count)} file${count === 1 ? "" : "s"}`,
    ),
  );

  const actions = el("span", { className: "git-file-group-actions" });
  for (const b of kind === "staged"
    ? stagedGroupActions(r, files)
    : unstagedGroupActions(r, files)) {
    actions.appendChild(b);
  }
  head.appendChild(actions);
  group.appendChild(head);

  // The heading is a sibling <div>, so nothing connects it to the list for a
  // screen reader: a row's own status cell reads "Status: Modified" on both
  // sides of the index, so without this label the group is a purely visual
  // distinction and staged-ness stays unannounced — which is the defect this
  // whole rework is about, just for a different reader. The count rides the
  // label so entering the list says how big it is.
  const heading = `${label}, ${String(count)} file${count === 1 ? "" : "s"}`;
  const list = el("ul", { className: "git-file-list", "aria-label": heading });
  const sorted = [...files].sort((a, b) => a.path.localeCompare(b.path));
  for (const f of sorted) {
    list.appendChild(renderFileRow(r, f, partial.has(f.path)));
  }
  group.appendChild(list);
  return group;
}

/** Build one group-header button. */
function groupBtn(label: string, title: string, danger = false): HTMLButtonElement {
  return el(
    "button",
    {
      type: "button",
      className: `btn-small${danger ? " btn-danger" : ""}`,
      "data-tooltip": title,
    },
    label,
  ) as HTMLButtonElement;
}

/** The Staged group's bulk action: Unstage all.
 *
 *  New capability. Staging every file was one click ("Stage all") and
 *  reversing it was N, one per row, with each row's Unstage button
 *  hidden until hovered. */
function stagedGroupActions(r: RepoStatus, entries: FileEntry[]): HTMLButtonElement[] {
  const paths = distinctPaths(entries);
  const b = groupBtn("Unstage all", "Move every staged change out of the index");
  return [
    pressControl(b, r.repo, "unstage-all", async () =>
      refusalOf(
        await unstage.dispatch({ repo: r.repo, files: paths }).outcome,
        `unstage ${files(paths.length)}`,
      ),
    ),
  ];
}

/** The Changes group's bulk actions: Stage all, then Discard all.
 *
 *  Both are scoped to THIS group, and that scope is the fix. Discard all
 *  used to sit in the repo action bar and take every entry including the
 *  staged ones, counted as `files.length` — so one file edited on both
 *  sides of the index made a destructive confirm offer to discard "2
 *  uncommitted changes", and that path went out twice in the payload.
 *  A bulk action whose scope is invisible is one nobody can check before
 *  pressing it. Discarding staged work too is Unstage all followed by
 *  this, which is two clicks and shows its work. */
function unstagedGroupActions(r: RepoStatus, entries: FileEntry[]): HTMLButtonElement[] {
  const paths = distinctPaths(entries);
  const count = paths.length;

  const stageBtn = groupBtn("Stage all", "Add every change below to the index");
  pressControl(stageBtn, r.repo, "stage-all", async () =>
    refusalOf(
      await stage.dispatch({ repo: r.repo, files: paths }).outcome,
      `stage ${files(count)}`,
    ),
  );

  const discardBtn = groupBtn(
    "Discard all",
    "Throw away every change below. This cannot be undone",
    true,
  );
  pressControl(
    discardBtn,
    r.repo,
    "discard-all",
    async () =>
      refusalOf(
        await discard.dispatch({ repo: r.repo, files: paths }).outcome,
        `discard ${files(count)}`,
      ),
    {
      // The scope is stated rather than implied. A reader who expects a clean
      // tree afterwards has to be told the index is untouched, and this is the
      // last moment to tell them.
      confirm: () => {
        const stagedCount = changedPathCount(r.files.filter((f) => f.staged));
        const keeps =
          stagedCount > 0
            ? ` Your ${String(stagedCount)} staged file${stagedCount === 1 ? "" : "s"} stay${stagedCount === 1 ? "s" : ""} untouched.`
            : "";
        return confirmDialog(
          `Discard ${String(count)} unstaged change${count === 1 ? "" : "s"} in ${r.repo}? This cannot be undone.${keeps}`,
          "Discard",
          "destructive",
        );
      },
    },
  );

  return [stageBtn, discardBtn];
}

function renderFileRow(r: RepoStatus, f: FileEntry, partiallyStaged: boolean): HTMLElement {
  const li = el("li", { className: "git-file-row" });

  // Top row: status + path + actions. Clicking it opens the file's diff in its
  // own editor tab.
  const top = el("div", { className: "git-file-row-top" });

  // A fixed-width COLOURED LETTER, not the status word this cell used to
  // print. Two reasons, and the first is measurable: "Untracked" is nine
  // characters against "Modified"'s eight and "M"'s one, and the cell
  // sized to its content, so every row's filename started at a different
  // x and a twenty-file list had a ragged left edge where the reader
  // scans. The second is that the app already HAS a per-letter palette
  // (`git-st-*`, 14-tools.css) which the file browser emits and this
  // panel did not, so the same change on the same file was a coloured
  // letter in one view and a grey word in the other. The word is not
  // lost: it is the tooltip and the accessible name, and `display` is
  // preferred over the local table because the server owns the mapping.
  const letter = statusLetter(f.status);
  const word = f.display || describeStatus(f.status);
  const status = el(
    "span",
    {
      className: `git-file-status git-st-${letter.toLowerCase()}`,
      "data-tooltip": word,
      "aria-label": `Status: ${word}`,
    },
    letter,
  );
  top.appendChild(status);

  // The filename IS the link to its own diff, which is the convention every
  // other changed-file affordance in the app already follows (navigate.ts
  // openChange, reached here and from a turn's ledger row, a tool card's
  // filename and the file browser's status letter).
  //
  // A real <button> rather than a role on the row, which is what the inline
  // drawer's disclosure trigger used to be: `role="button"` is
  // Children-Presentational, so it flattened the Stage and Discard buttons
  // beside it out of the accessibility tree. The row keeps a mouse handler
  // below, so the wide click target survives without that cost.
  // The label is a SPAN so the tooltip has ink to point at: the button is `flex: 1`
  // (it is what pushes the row's actions to the trailing edge), so its box is the
  // row's slack and a tooltip anchored at that box's centre landed 243px right of
  // the name — measured over 320 rows. No class, because it needs no rule: an
  // inline span inherits the button's mono type, and the ellipsis is the button's
  // own (`overflow: hidden` on the block box clips whatever is inside it).
  const label = el("span", { "data-tooltip-anchor": "" }, f.path);
  const path = el(
    "button",
    {
      type: "button",
      className: "git-file-path",
      "data-tooltip": f.path,
      "aria-label": `Open diff for ${f.path}`,
    },
    label,
  ) as HTMLButtonElement;
  path.addEventListener("click", (ev) => {
    ev.stopPropagation();
    openFileDiff(r, f);
  });
  top.appendChild(path);

  // Where a rename or copy came FROM. The server parsed this field and
  // threw it away, so a moved file rendered as "Renamed  path/to/new.ts"
  // with no way to tell what had moved — the one status whose whole
  // meaning is the pair of paths.
  const orig = f.orig_path ?? "";
  if (orig !== "") {
    top.appendChild(
      el(
        "span",
        {
          className: "git-file-orig",
          "data-tooltip": `${letter === "C" ? "Copied" : "Renamed"} from ${orig}`,
        },
        `\u2190 ${orig}`,
      ),
    );
  }

  // Why this path appears twice. Both of its rows carry the mark, so it
  // reads as one file in two states rather than as a duplicate.
  if (partiallyStaged) {
    top.appendChild(
      el(
        "span",
        {
          className: "git-file-partial",
          "data-tooltip":
            "This file is staged AND changed again since. Each side is listed in its own group.",
        },
        "partially staged",
      ),
    );
  }

  const actions = el("span", { className: "git-file-actions" });

  const action = (
    label: string,
    title: string,
    run: PressRun,
    opts: PressOpts = {},
    danger = false,
  ): HTMLButtonElement => {
    const b = el(
      "button",
      {
        type: "button",
        className: `btn-small${danger ? " btn-danger" : ""}`,
        "data-tooltip": title,
      },
      label,
    ) as HTMLButtonElement;
    return pressControl(b, r.repo, `${label.toLowerCase()}:${f.path}`, run, opts);
  };

  if (f.staged) {
    actions.appendChild(
      action("Unstage", "Move out of staged area", async () =>
        refusalOf(
          await unstage.dispatch({ repo: r.repo, files: [f.path] }).outcome,
          `unstage ${f.path}`,
        ),
      ),
    );
  } else {
    actions.appendChild(
      action("Stage", "Add to staged area", async () =>
        refusalOf(
          await stage.dispatch({ repo: r.repo, files: [f.path] }).outcome,
          `stage ${f.path}`,
        ),
      ),
    );
    actions.appendChild(
      action(
        "Discard",
        "Throw away this change",
        async () =>
          refusalOf(
            await discard.dispatch({ repo: r.repo, files: [f.path] }).outcome,
            `discard ${f.path}`,
          ),
        {
          confirm: () =>
            confirmDialog(
              `Discard changes to ${f.path}? This cannot be undone.`,
              "Discard",
              "destructive",
            ),
        },
        true,
      ),
    );
  }
  top.appendChild(actions);
  li.appendChild(top);

  // The whole row stays a mouse target, exactly as the drawer's trigger was.
  // Keyboard users reach the same action through the filename button above, and
  // the Stage/Discard buttons already stopPropagation.
  top.addEventListener("click", () => {
    openFileDiff(r, f);
  });

  return li;
}

/** Open one changed file's diff against HEAD, in its own editor tab.
 *
 *  The inline drawer this replaced rendered the raw unified-diff TEXT in a
 *  `<pre>` inside the file list, so a line wider than that column was simply
 *  clipped, in a surface with no room to scroll it: no line numbers, no
 *  highlighting, and a hard 26rem ceiling. The editor's diff tab is the app's
 *  existing answer for a changed file, and it was already the surface every
 *  other changed-file click reached; the git panel was the one outlier.
 *
 *  Two conversions happen here and only here. The Changes tab holds REPO-relative
 *  paths with one repo per section, while `openChange` takes the
 *  workspace-relative form every other caller has, so the repo name is joined
 *  back on — the workspace-root repo is named "." and owns paths with no prefix.
 *  The repo is then deliberately NOT passed onward: the diff loader resolves the
 *  owning repository from a workspace-relative path (internal/git `ownerOf`), so
 *  one spelling serves both sides of its fetch. */
function openFileDiff(r: RepoStatus, f: FileEntry): void {
  openChange(r.repo === "." ? f.path : `${r.repo}/${f.path}`);
}

// --- Helpers ---

const DEFAULT_BRANCHES = new Set(["main", "master", "develop", "trunk"]);

/** Heuristic: branches named "main", "master", "develop", "trunk"
 *  aren't feature branches and shouldn't trigger the post-push
 *  "Open PR" hint. Anything else is treated as a feature branch
 *  candidate. */
function isFeatureBranch(branch: string): boolean {
  return branch !== "" && !DEFAULT_BRANCHES.has(branch.toLowerCase());
}

/** Banner shown briefly after a successful push: invites the user to
 *  open a PR for the just-pushed branch. Click switches to the PRs
 *  tab and opens the new-PR dialog with source_branch pre-filled. */
function renderOpenPRHint(r: RepoStatus): HTMLElement {
  const hint = el("div", { className: "git-open-pr-hint" });
  hint.append(
    el("span", { className: "git-open-pr-hint-icon", "aria-hidden": "true" }, "💡"),
    el(
      "span",
      { className: "git-open-pr-hint-msg" },
      "Pushed ",
      el("strong", null, r.branch),
      " to origin. Open a pull request?",
    ),
  );
  const btn = el("button", { type: "button", className: "btn-small btn-primary" }, "Open PR");
  btn.addEventListener("click", () => {
    void (async () => {
      const { setGitTab } = await import("./git-tabs.js");
      const { openNewPRForRepo } = await import("./git-prs-tab.js");
      setGitTab("prs");
      await openNewPRForRepo(r.repo, r.branch);
    })().catch(() => {
      /* noop */
    });
  });
  hint.appendChild(btn);
  return hint;
}
