// One collapsible section per repo in #git-changes-mount, painted from one /api/git/status-all read. Repainted on
// filter input, after every press, and on tab activate.

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

let inited = false;
let lastStatusAll: RepoStatus[] = [];
/**
 * Whether `status-all` has answered: the empty-state row is unkeyed, so nothing else tells a clean worktree from an
 * unread one.
 */
let statusAnswered = false;
let filterText = "";
let refreshGeneration = 0;
let refreshAbort: AbortController | null = null;
/** Aborted only on tab teardown, never on a repaint. */
const diffAbortCtrl = new AbortController();
registerCleanup(() => {
  refreshAbort?.abort();
});
registerCleanup(() => {
  diffAbortCtrl.abort();
});

/** Repos just pushed, for a transient "Open PR" hint in their section header. */
const recentlyPushed = new Set<string>();
const RECENTLY_PUSHED_TTL_MS = 60_000;

// Typed commit messages, kept across repaints.
const commitMessages = new Map<string, string>();

/**
 * The press each repository runs. Its actions share one `git:<repo>` queue, so the section's other controls wait,
 * and a repaint shows the press on the control that started it.
 */
const repoPresses = new Map<string, { key: string; done: Promise<void> }>();

/** A second press while a confirm is open would confirm and send twice. */
const confirmingRepos = new Set<string>();

/** Why a repository's last press did not land, said at the top of its section until its next press. */
const pressNotes = new Map<string, Refusal>();

// Repos the user collapsed by hand.
const userCollapsedRepos = new Set<string>();
const userExpandedRepos = new Set<string>();

/**
 * What the last Pull all could not do, per repo (`isPullHeld` verdicts only). Survives a refresh so a reader who
 * walked away still finds it; dropped once the repo stops being behind; replaced by the next pass.
 */
const pullFlags = new Map<string, GitPullResult>();

// Set when paint bails on a focused commit box.
let paintDeferred = false;

function commitDeps(): CommitDeps {
  return { commitMessages, diffAbort: diffAbortCtrl, press: pressControl };
}

/**
 * The tab's filter popup. Its close clears: the filter also drives each section's expansion default, so a query left
 * behind a closed box would leave the page narrowed and expanded with nothing explaining either.
 */
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

/** Initialise the Changes tab: the refresh button and the SSE refresh triggers. Idempotent. */
export function initChangesTab(): void {
  if (inited) {
    return;
  }
  inited = true;
  // Fires the deferred paint when the commit box loses focus; focusout bubbles to the mount.
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
      // An explicit refresh opts into the server-side git fetch, so ahead/behind reflects the remote.
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
    // The pass's refresh runs once the button settles, so the loading binding and the button's feedback end together.
    pullAllBtn.addEventListener("click", () => {
      status.textContent = "";
      let moved: string[] = [];
      void withAsyncFeedback(pullAllBtn, async () => {
        moved = await runPullAll(status);
      }).then(() => refreshChanges(false, moved));
    });
    // Bound once: the toolbar is outside the repaint cycle. `git.pull` is in the set because a single Pull and Pull all
    // are one operation at two scopes.
    bindLoadingState(["git.pull_all", "git.pull"], pullAllBtn);
  }

  // Refetch when the agent writes files or forge accounts change; debounced so bursts coalesce.
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

/**
 * Read /api/git/status-all and repaint; a newer call supersedes an older one. Answers false when the status could not
 * be read. `doFetch` adds a per-repo `git fetch`, reserved for explicit navigation so agent turns never fan out N
 * fetches. `moved` names repositories a press just changed, which the server rescans before answering.
 */
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

  // Gated on not-yet-answered, not on an empty container: `status-all` is polled, and a clean worktree is an answer.
  const skeleton = statusAnswered ? null : skeletonTiming(() => gitChangesSkeleton(), { signal });

  try {
    const data = await apiGet<StatusAllResponse>(url, signal);
    if (gen < refreshGeneration) {
      return true;
    } // A newer call superseded this one.
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

/** Returns the teardown `skeletonTiming` calls. */
function gitChangesSkeleton(): () => void {
  // No `label`: this tab issues one request.
  return paintPlaceholder(document.getElementById("git-changes-mount"), () =>
    gitRepoSkeleton({ widths: ["38%", "52%", "30%"] }),
  );
}

/**
 * Records the two verdicts a reader must act on as marks on their blocks; the toast carries the summary. A refused
 * pass rejects after `status` says why. The caller's refresh is fetch-free: the pass just fetched every remote.
 * Answers the repositories whose status it moved.
 */
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

function files(n: number): string {
  return `${String(n)} file${n === 1 ? "" : "s"}`;
}

function pressedSection(repo: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(
    `#git-changes-mount section[data-repo="${CSS.escape(repo)}"]`,
  );
}

/** Disabled, and marked so the release gives it back. */
function hold(btn: HTMLButtonElement): void {
  if (!btn.disabled) {
    btn.disabled = true;
    btn.dataset["pressHeld"] = "";
  }
}

function release(repo: string): void {
  repoPresses.delete(repo);
  const held = pressedSection(repo)?.querySelectorAll<HTMLButtonElement>("[data-press-held]");
  for (const b of held ?? []) {
    delete b.dataset["pressHeld"];
    b.disabled = false;
  }
}

/** A control built while that press runs shows it; one built while another press on the repository runs waits. */
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

/**
 * The confirm, then the request with the button busy and the section's other controls held, then a re-read, with a
 * refusal said at the section's top.
 */
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
  // Registered and holding before the request starts, so a repaint the request causes shows it.
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

function paint(): void {
  preserveGitScroll(paintInner);
}

function paintInner(): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) {
    return;
  }

  // A repaint while a commit box is focused would destroy the user's typing.
  const focused = document.activeElement;
  if (focused instanceof HTMLTextAreaElement && focused.classList.contains("git-commit-input")) {
    paintDeferred = true;
    return;
  }
  paintDeferred = false;

  // Prune module-level state to repos still present.
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
  // A pull flag expires when the repo stops being behind or goes away. Keyed on `behind`, not on the hazard: resolving a
  // conflict does not pull the repo.
  for (const k of pullFlags.keys()) {
    const repo = lastStatusAll.find((r) => r.repo === k);
    if (repo === undefined || repo.behind === 0) {
      pullFlags.delete(k);
    }
  }

  // Capture typed commit messages before the DOM is replaced.
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

  // One predicate, read here for the reconcile list and in renderRepoSection for the rows.
  const visibleRepos = lastStatusAll.filter((r) => filteredFilesFor(r) !== null);

  for (const child of [...root.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }

  // Every repo dropped can only be the filter: unfiltered, there is one section per repo. No aggregate "all quiet"
  // state: it would hide a behind repo's sync buttons and its branch chip; that fact is the sidebar badge's.
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

  // Keeps section identity (and commit drafts inside) across paints; the body is rebuilt on update.
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

/**
 * The files of `r` the filter admits, or null when it excludes the repo. A repo-name match admits every file. An
 * admitted repo always yields files or has nothing uncommitted, so "no paths match" is unreachable.
 */
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
  // Hide clean repos by default. A Pull all mark only lands on a behind repo, so `r.behind > 0` already opens it.
  const dataDefault = r.has_dirty || r.ahead > 0 || r.behind > 0;
  // A filter outranks the reader's latch: a selected row inside a collapsed region is invisible and unannounced. The
  // latch is read again once the box is empty.
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

  // A native <button>, so createDisclosure handles Enter/Space through the native click.
  const header = el("button", {
    type: "button",
    className: "git-repo-section-header",
  });
  header.innerHTML = renderHeaderHTML(r);
  // `chevronEl()` stays the app's one construction of the chevron; `renderHeaderHTML` has this one call site, so it is
  // not wiped.
  const chevron = chevronEl();
  chevron.classList.add("git-repo-section-chevron");
  header.prepend(chevron);

  // A chip click must open the branch switcher, not toggle the section: the chip's own listener stops propagation
  // before the disclosure's bubble-phase listener.
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

  // createDisclosure owns the collapse; padding and layout live on the inner wrapper so they collapse with the height.
  const body = el("div", { className: "git-repo-section-body" });
  const inner = el("div", { className: "git-repo-section-body-inner" });
  body.appendChild(inner);

  // onToggle persists the user's explicit toggle so repaints respect it.
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

  // Omitted when no action applies: an empty bar still takes the column's gap.
  const actionBar = renderActionBar(r);
  if (actionBar.childElementCount > 0) {
    inner.appendChild(actionBar);
  }

  // Above the action bar: it explains the absent Pull button there.
  const flag = pullFlags.get(r.repo);
  if (flag !== undefined) {
    const lead = flag.verdict === "failed" ? "Pull failed." : "Not pulled.";
    inner.insertBefore(renderRepoNote(flag.verdict, lead, flag.detail ?? ""), inner.firstChild);
  }
  // Above everything: the newest thing that happened to this repository.
  const refusal = pressNotes.get(r.repo);
  if (refusal !== undefined) {
    const note = renderRepoNote("failed", refusal.lead, refusal.detail);
    note.dataset["pressNote"] = "";
    inner.insertBefore(note, inner.firstChild);
  }

  if (recentlyPushed.has(r.repo) && isFeatureBranch(r.branch)) {
    inner.appendChild(renderOpenPRHint(r));
  }

  // Empty here means nothing is uncommitted, never filtered out (paint drops that section). The sentence names the
  // working tree, since Pull, Push or Pop may sit right above it.
  if (filteredFiles.length === 0) {
    inner.appendChild(el("div", { className: "git-repo-row-clean" }, "No uncommitted changes."));
  } else {
    inner.appendChild(renderFileList(r, filteredFiles));
  }

  // Only when something is staged: the index is the selection. Gets the staged file count so the button names it.
  const stagedCount = changedPathCount(r.files.filter((f) => f.staged));
  if (stagedCount > 0) {
    inner.appendChild(renderCommitArea(r, commitDeps(), stagedCount));
  }

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
  // In the header, since a collapsed section is all a scanning reader sees; glyph plus hue. No detail means no tooltip.
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
  // The chip is a span (no button inside a button); its own listener stops propagation so the disclosure never fires.
  return `
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">
      <span class="git-repo-branch-chip" data-branch-trigger="${escapeHTML(r.repo)}" data-tooltip="Switch branch">${branch}</span>${ahead}${behind}${stashes}${held}
    </span>
  `;
}

/**
 * Says what did not happen to the repo: why the last Pull all left it alone, or why its last press was refused. The
 * detail is the server's.
 */
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

/**
 * The repo's sync actions: pull, push, stash, pop. Every button is state-gated: one the repo state cannot service
 * does not render, so there are no confirm-then-noop paths.
 */
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

  // `git stash push` runs without `-u`, so untracked files are not stashable; `stashableCount` (git-types.ts) owns the
  // rule. Gating on `dirtyCount` would offer a Stash that answers "No local changes to save".
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

/**
 * Split into Staged and Changes groups, each owning the bulk action for exactly what its header counts. A group
 * renders only with members.
 */
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

  // The heading is a sibling <div>, so the label carries the group and its count for a screen reader.
  const heading = `${label}, ${String(count)} file${count === 1 ? "" : "s"}`;
  const list = el("ul", { className: "git-file-list", "aria-label": heading });
  const sorted = [...files].sort((a, b) => a.path.localeCompare(b.path));
  for (const f of sorted) {
    list.appendChild(renderFileRow(r, f, partial.has(f.path)));
  }
  group.appendChild(list);
  return group;
}

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

/** Both scoped to this group, so the confirm's count is the payload. Discarding staged work too is Unstage all first. */
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
      // The scope is stated: the index is untouched, and this is the last moment to say so.
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

  // Status, path and actions. A click opens the file's diff in its own editor tab.
  const top = el("div", { className: "git-file-row-top" });

  // A fixed-width coloured letter (`git-st-*`, as the file browser): a status word left filenames at ragged x. The word
  // is the tooltip and accessible name; `display` is preferred because the server owns the mapping.
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

  // The filename opens its own diff, as every changed-file affordance does. A real <button>, not a role on the row:
  // `role="button"` is Children-Presentational and would hide Stage and Discard. The label is a span so the tooltip
  // anchors on ink; the `flex: 1` button's centre is far from the name.
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

  // Where a rename or copy came from: its meaning is the pair of paths.
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

  // Both rows carry the mark, so it reads as one file in two states.
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

  // The whole row stays a mouse target; the filename button serves the keyboard, and Stage/Discard stopPropagation.
  top.addEventListener("click", () => {
    openFileDiff(r, f);
  });

  return li;
}

/**
 * Open one changed file's diff against HEAD in its own editor tab. Paths here are repo-relative, so the repo name is
 * joined back on (the root repo is "." and has no prefix). The repo is not passed on: the diff loader resolves the
 * owner from the workspace-relative path.
 */
function openFileDiff(r: RepoStatus, f: FileEntry): void {
  openChange(r.repo === "." ? f.path : `${r.repo}/${f.path}`);
}

const DEFAULT_BRANCHES = new Set(["main", "master", "develop", "trunk"]);

/** Default branches do not trigger the post-push "Open PR" hint. */
function isFeatureBranch(branch: string): boolean {
  return branch !== "" && !DEFAULT_BRANCHES.has(branch.toLowerCase());
}

/** Click opens the PRs tab and the new-PR dialog with source_branch prefilled. */
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
