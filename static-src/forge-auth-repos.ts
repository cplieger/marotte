import { el } from "@cplieger/reactive";
import { ICON_DOWNLOAD, ICON_EXTERNAL, ICON_GLOBE, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import type { PartialResult, Repo, RepoList } from "./wire/types.gen.js";
import { cloneRepo as cloneRepoAction, deleteLocal as deleteLocalAction } from "./actions/forge.js";

/** One account's repositories as the panel holds them: the first page, the pages
 *  loaded past it, and where the next page continues. */
export interface RepoListing {
  first: Repo[];
  /** The cursor the first page answered, which `more` continued from. */
  firstNext: string;
  /** The rows of the pages loaded past the first, in load order. */
  more: Repo[];
  /** The cursor the next page continues from, "" once the list is whole. */
  next: string;
  /** The last page read's partial, absent when it was whole. */
  partial?: PartialResult;
  /** No read of the list has succeeded. */
  unread: boolean;
  /** The last first-page read failed, so the rows are from an earlier one. */
  stale: boolean;
}

/**
 * The listing a first-page read leaves; null `page` (a failed read) keeps what was held and marks it stale. Later
 * pages stay while the first page still names their cursor, so re-reading an unchanged list keeps them.
 */
export function readFirstPage(prev: RepoListing | undefined, page: RepoList | null): RepoListing {
  if (page === null) {
    if (prev === undefined || prev.unread) {
      return { first: [], firstNext: "", more: [], next: "", unread: true, stale: false };
    }
    return { ...prev, stale: true };
  }
  const next = page.next ?? "";
  const partial = page.partial === undefined ? {} : { partial: page.partial };
  if (prev !== undefined && prev.more.length > 0 && next !== "" && next === prev.firstNext) {
    return { ...prev, first: page.repos, ...partial, unread: false, stale: false };
  }
  return {
    first: page.repos,
    firstNext: next,
    more: [],
    next,
    ...partial,
    unread: false,
    stale: false,
  };
}

/** The listing once the page `l.next` named has arrived. */
export function readNextPage(l: RepoListing, page: RepoList): RepoListing {
  const out: RepoListing = {
    first: l.first,
    firstNext: l.firstNext,
    more: [...l.more, ...page.repos],
    next: page.next ?? "",
    unread: false,
    stale: l.stale,
  };
  if (page.partial !== undefined) {
    out.partial = page.partial;
  }
  return out;
}

/** Every repository the listing holds, once each by id, in the order read. */
export function listedRepos(l: RepoListing): Repo[] {
  const seen = new Set<string>();
  return [...l.first, ...l.more].filter((r) => {
    if (seen.has(r.repo_id)) {
      return false;
    }
    seen.add(r.repo_id);
    return true;
  });
}

export interface RepoDeps {
  /** Check if a repo name is locally cloned. */
  isCloned: (name: string) => boolean;
  /** Mark a repo name as locally cloned. */
  addCloned: (name: string) => void;
  /** Remove a repo name from the cloned set. */
  removeCloned: (name: string) => void;
  /** Bump the state version to trigger a re-render. */
  bumpState: () => void;
  /** Start the row request `key` unless it already runs, answering it. */
  start: (key: string, fn: () => Promise<void>) => Promise<void> | undefined;
  /** The row request running under `key`, if any. */
  running: (key: string) => Promise<void> | undefined;
}

const rowNotes = new Map<string, string>();

/** A row's identity across accounts: its clone URL names the host. */
function rowKey(repo: Repo): string {
  return repo.clone_url ?? repo.url ?? repo.full_name;
}

function cloneKey(repo: Repo): string {
  return `clone ${rowKey(repo)}`;
}

/** A local copy is a directory named for the repository, so one removal serves every row of that name. */
function removeKey(repo: Repo): string {
  return `remove ${repo.name}`;
}

function setRowNote(repo: Repo, text: string, deps: RepoDeps): void {
  if (text === "") {
    rowNotes.delete(rowKey(repo));
  } else {
    rowNotes.set(rowKey(repo), text);
  }
  deps.bumpState();
}

export function renderRepoRow(repo: Repo, deps: RepoDeps): HTMLElement {
  const li = el("li", { className: "forge-account-repo-row" });
  const cloned = deps.isCloned(repo.name);
  li.append(
    renderRepoState(cloned),
    renderRepoIdentity(repo),
    renderRepoActions(repo, cloned, deps),
  );
  return li;
}

/** The row's name, its tags, and what it says about its last press. */
export function renderRepoIdentity(repo: Repo): HTMLElement {
  const idEl = el(
    "div",
    { className: "forge-account-repo-identity" },
    el("span", { className: "forge-account-repo-name" }, repo.full_name),
  );
  const tags: string[] = [];
  if (repo.private === true) {
    tags.push("private");
  }
  if (repo.archived === true) {
    tags.push("archived");
  }
  if (repo.fork === true) {
    tags.push("fork");
  }
  if (repo.default_branch !== undefined && repo.default_branch !== "") {
    tags.push(repo.default_branch);
  }
  if (tags.length > 0) {
    idEl.appendChild(el("span", { className: "forge-account-repo-tags" }, tags.join(" · ")));
  }
  const note = rowNotes.get(rowKey(repo)) ?? "";
  if (note !== "") {
    idEl.appendChild(el("span", { className: "forge-account-error" }, note));
  }
  return idEl;
}

export function renderRepoState(cloned: boolean): HTMLElement {
  const state = el("span", { className: "forge-account-repo-state" });
  if (cloned) {
    state.appendChild(
      el("span", {
        className: "git-sources-cloned-dot",
        role: "img",
        "aria-label": "Cloned",
        "data-tooltip": "Cloned and tracked",
      }),
    );
  } else {
    state.appendChild(iconEl(ICON_GLOBE));
    state.setAttribute("data-tooltip", "Remote, not cloned");
    state.setAttribute("role", "img");
    state.setAttribute("aria-label", "Remote, not cloned");
  }
  return state;
}

function pressRow(
  btn: HTMLButtonElement,
  key: string,
  fn: () => Promise<void>,
  deps: RepoDeps,
): void {
  const p = deps.start(key, fn);
  if (p !== undefined) {
    void withAsyncFeedback(btn, () => p);
  }
}

/** A control built while its request runs shows that request. */
function adoptRow(btn: HTMLButtonElement, key: string, deps: RepoDeps): void {
  const p = deps.running(key);
  if (p !== undefined) {
    void withAsyncFeedback(btn, () => p);
  }
}

export function renderRepoActions(repo: Repo, cloned: boolean, deps: RepoDeps): HTMLElement {
  const actions = el("span", { className: "forge-account-repo-actions" });

  if (repo.url !== undefined && repo.url !== "") {
    const open = el(
      "a",
      {
        href: repo.url,
        target: "_blank",
        rel: "noreferrer",
        className: "btn-small icon-only",
        "data-tooltip": "Open on forge",
        "aria-label": "Open on forge",
      },
      iconEl(ICON_EXTERNAL),
    );
    actions.appendChild(open);
  }

  if (cloned) {
    const trash = el(
      "button",
      {
        type: "button",
        className: "btn-small btn-danger icon-only",
        "data-tooltip": "Remove local copy",
        "aria-label": "Remove local copy",
      },
      iconEl(ICON_TRASH),
    ) as HTMLButtonElement;
    trash.addEventListener("click", () => {
      void confirmRemoval(repo, trash, deps);
    });
    adoptRow(trash, removeKey(repo), deps);
    actions.appendChild(trash);
  } else if (repo.clone_url !== undefined && repo.clone_url !== "") {
    const clone = el(
      "button",
      {
        type: "button",
        className: "btn-small icon-only",
        "data-tooltip": "Clone into workspace",
        "aria-label": "Clone into workspace",
      },
      iconEl(ICON_DOWNLOAD),
    ) as HTMLButtonElement;
    clone.addEventListener("click", () => {
      pressRow(clone, cloneKey(repo), () => cloneRepo(repo, deps), deps);
    });
    adoptRow(clone, cloneKey(repo), deps);
    actions.appendChild(clone);
  }

  return actions;
}

/** Clone one repository into the workspace. Rejects when it did not land, after
 *  its row says why. */
async function cloneRepo(
  repo: Repo,
  deps: RepoDeps,
  onProgress?: (line: string) => void,
): Promise<void> {
  setRowNote(repo, "", deps);
  const o = await cloneRepoAction.dispatch({
    url: repo.clone_url ?? "",
    ...(onProgress === undefined ? {} : { onProgress }),
  }).outcome;
  if (o.status === "cancelled") {
    throw new Error("the clone was cancelled");
  }
  const why = o.status === "error" ? o.error.message : (o.value.error ?? "");
  if (why !== "") {
    setRowNote(repo, `Could not clone. ${why}`, deps);
    throw new Error(why);
  }
  deps.addCloned(repo.name);
  deps.bumpState();
}

async function confirmRemoval(repo: Repo, btn: HTMLButtonElement, deps: RepoDeps): Promise<void> {
  const ok = await confirmDialog(
    `Delete the local copy of ${repo.name}? The remote stays intact. You can re-clone later.`,
    "Delete",
    "destructive",
  );
  if (ok) {
    pressRow(btn, removeKey(repo), () => removeLocalRepo(repo, deps), deps);
  }
}

/**
 * Delete the workspace copy of one repository. The row keeps its copy until the delete lands; a refusal rejects
 * after the row says why.
 */
async function removeLocalRepo(repo: Repo, deps: RepoDeps): Promise<void> {
  setRowNote(repo, "", deps);
  const o = await deleteLocalAction.dispatch({ repoName: repo.name }).outcome;
  if (o.status === "cancelled") {
    throw new Error("the removal was cancelled");
  }
  const why = o.status === "error" ? o.error.message : (o.value.error ?? "");
  if (why !== "") {
    setRowNote(repo, `Could not remove the local copy. ${why}`, deps);
    throw new Error(why);
  }
  deps.removeCloned(repo.name);
  deps.bumpState();
}

/**
 * Run `each` over `candidates` in turn as each row's own request, so a row press already running is awaited, not
 * doubled. Answers the full names of the ones that fell short.
 */
async function eachRow(
  candidates: readonly Repo[],
  key: (repo: Repo) => string,
  each: (repo: Repo, i: number) => Promise<void>,
  deps: RepoDeps,
): Promise<string[]> {
  const failed: string[] = [];
  for (const [i, repo] of candidates.entries()) {
    const k = key(repo);
    try {
      await (deps.start(k, () => each(repo, i)) ?? deps.running(k));
    } catch {
      failed.push(repo.full_name);
    }
  }
  return failed;
}

/** Clone every candidate, the button counting them off. Answers the list's
 *  sentence for the ones that fell short, "" when every clone landed. */
export async function cloneAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
  deps: RepoDeps,
): Promise<string> {
  const failed = await eachRow(
    candidates,
    cloneKey,
    (repo, i) => {
      const position = `Cloning ${String(i + 1)}/${String(candidates.length)}`;
      btn.textContent = `${position}…`;
      // git's progress stream, throttled server-side; the percent tells a reader a large repo is downloading, not hung.
      return cloneRepo(repo, deps, (line) => {
        const pct = /(\d{1,3})%/.exec(line)?.[1];
        btn.textContent = pct === undefined ? `${position}…` : `${position} (${pct}%)…`;
      });
    },
    deps,
  );
  return batchFailure("clone", failed, candidates.length);
}

/** Delete the local copy of every candidate. Answers the list's sentence for the
 *  ones that fell short, "" when every removal landed. */
export async function deleteAllForAccount(
  candidates: Repo[],
  btn: HTMLButtonElement,
  deps: RepoDeps,
): Promise<string> {
  const failed = await eachRow(
    candidates,
    removeKey,
    (repo, i) => {
      btn.textContent = `Deleting ${String(i + 1)}/${String(candidates.length)}…`;
      return removeLocalRepo(repo, deps);
    },
    deps,
  );
  return batchFailure("remove the local copy of", failed, candidates.length);
}

/**
 * The list's sentence for a batch that fell short, naming the repositories: a bare count leaves the reader diffing
 * directories to find which. Up to three names in full, the rest as a count; "" for none.
 */
export function batchFailure(verb: string, failedNames: readonly string[], total: number): string {
  if (failedNames.length === 0) {
    return "";
  }
  const shown = failedNames.slice(0, 3).join(", ");
  const more = failedNames.length - 3;
  const names = more > 0 ? `${shown} and ${String(more)} more` : shown;
  return `Could not ${verb} ${names} (${String(failedNames.length)} of ${String(total)} repos).`;
}
