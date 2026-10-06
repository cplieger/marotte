// One shared owner of /api/git/status-all, with per-path lookups. No timer: every automatic refresh is a fact
// arriving through `markGitDirty` (a tool call completing, an editor save, the shell closing, the tab becoming
// visible). The first two name their paths so only the owning repositories are rescanned. No file watcher: marotte
// is the agent's writer and can name the repos, and a recursive inotify watch over many worktrees exceeds the host
// limit. git-changes-tab.ts keeps its own `?fetch=1` read for user gestures.

import { apiAction, defineAction } from "./actions/index.js";
import { signal, subscribe } from "@cplieger/reactive";
import { absPath, onWorkspaceRoot, workspaceRoot } from "./workspace.js";
import type { GitRepoStatus } from "./git-types.js";
import { statusLetter } from "./git-types.js";

interface StatusAllResponse {
  repos?: GitRepoStatus[] | null;
}

/** The server's cap binds; this keeps a large turn from building a URL that is mostly dropped. */
const SCOPE_PATHS_MAX = 64;

/** Paths are workspace-relative, as `ownerOf` speaks; the path-to-repository split stays server-side. */
function scopeQuery(paths: readonly string[] | undefined): string {
  if (paths === undefined || paths.length === 0) {
    return "";
  }
  const wanted = [...new Set(paths.filter((p) => p !== ""))].slice(0, SCOPE_PATHS_MAX);
  if (wanted.length === 0) {
    return "";
  }
  return `?paths=${encodeURIComponent(wanted.join(","))}`;
}

interface ScopeArgs {
  paths?: readonly string[];
}

const fetchStatusAll = apiAction<ScopeArgs, StatusAllResponse>({
  name: "git-status.all",
  request: ({ paths }) => ({ method: "GET", path: `/api/git/status-all${scopeQuery(paths)}` }),
  error: false,
  success: false,
});

const refreshAction = defineAction<ScopeArgs, StatusAllResponse>({
  name: "git-status.refresh",
  // Keyed on the scope: two reads naming different repositories are two, or the second repository's rows go stale.
  dedupe: (args) => `git-status.refresh${scopeQuery(args.paths)}`,
  run: async (args) => (await fetchStatusAll.dispatch(args)) ?? { repos: [] },
  error: false,
  success: false,
});

/** A signal so consumers re-render on every read without holding a copy. */
const repos = signal<readonly GitRepoStatus[]>([]);

/** "<repo>\u0000<repo-relative path>" → letter, rebuilt every read; a map because the docs page asks ~200 times a paint. */
let index = new Map<string, string>();

/**
 * Absolute path → letter, for the file browser, which has no repo split of its own. Keys are joined through
 * workspace.ts's absPath, the one owner of the relative-to-absolute rule.
 */
let absIndex = new Map<string, string>();

/** Absolute directory → the worst letter beneath it, so a change deep down shows on its folders. */
let dirIndex = new Map<string, string>();

/**
 * Worst first: a conflict outranks everything, an untracked file nothing. Must list every letter git emits, or a
 * missed one falls to the unknown tail and a folder holding only it reports something else.
 */
const ROLLUP_ORDER: readonly string[] = ["U", "D", "T", "M", "R", "C", "A", "?"];

function worse(a: string, b: string): string {
  if (a === "") {
    return b;
  }
  if (b === "") {
    return a;
  }
  const ia = ROLLUP_ORDER.indexOf(a);
  const ib = ROLLUP_ORDER.indexOf(b);
  // An unknown letter sorts last rather than winning by accident.
  return (ia === -1 ? ROLLUP_ORDER.length : ia) <= (ib === -1 ? ROLLUP_ORDER.length : ib) ? a : b;
}

let started = false;

function rebuildIndex(list: readonly GitRepoStatus[]): void {
  const next = new Map<string, string>();
  const nextAbs = new Map<string, string>();
  const nextDir = new Map<string, string>();
  // The absolute indexes need the root; without it the lookups return "" rather than a letter from a guessed root.
  const rooted = workspaceRoot() !== "";
  for (const r of list) {
    if (!r.is_repo) {
      continue;
    }
    // `r.repo` is a directory name under the workspace, and "." means the root is the repo.
    const repoAbs = r.repo === "." ? workspaceRoot() : absPath(r.repo);
    for (const f of r.files) {
      const key = `${r.repo}\u0000${f.path}`;
      const letter = statusLetter(f.status);
      // First non-empty letter wins for a path listed twice (staged + unstaged), as in the git view.
      if (!next.has(key)) {
        next.set(key, letter);
      }
      if (letter === "" || !rooted) {
        continue;
      }
      const abs = `${repoAbs}/${f.path}`;
      if (!nextAbs.has(abs)) {
        nextAbs.set(abs, letter);
      }
      // Mark every ancestor up to and including the repo root.
      let cut = abs.lastIndexOf("/");
      while (cut > 0) {
        const dir = abs.slice(0, cut);
        nextDir.set(dir, worse(nextDir.get(dir) ?? "", letter));
        if (dir === repoAbs) {
          break;
        }
        cut = dir.lastIndexOf("/");
      }
    }
  }
  index = next;
  absIndex = nextAbs;
  dirIndex = nextDir;
}

// The root handshake and the first read race with no ordering; a read that wins builds no absolute keys, and no
// timer follows. Rebuilding when the root lands and republishing repaints rows painted letter-less meanwhile.
onWorkspaceRoot(() => {
  const list = repos.peek();
  rebuildIndex(list);
  // A new array identity: the index changed while the data did not, and `repos` is all consumers watch.
  repos.value = [...list];
});

// The catch-all for writers this client cannot name (the shell, another window, a command). Fires when a stale badge
// would be read, not on a schedule; unscoped. Guarded on the store having started, or a page with no subscriber
// would scan every worktree.
document.addEventListener("visibilitychange", () => {
  if (started && !document.hidden) {
    void refreshGitStatus();
  }
});

/** The module's only unprompted read, on the first subscriber; everything after it is `refreshGitStatus`. */
function startOnFirstSubscriber(): void {
  if (started) {
    return;
  }
  started = true;
  void refreshGitStatus();
}

/**
 * Re-read the tree, or only the repositories owning `paths`; deduped per scope with any read in flight. Called via
 * `git.ts`'s markGitDirty from the sites where the tree moves. The answer is always the whole repos array: the server
 * merges a scoped scan into its snapshot, so no partial list is published.
 */
export async function refreshGitStatus(paths?: readonly string[]): Promise<void> {
  const d = await refreshAction.dispatch(paths === undefined ? {} : { paths });
  const list = d?.repos ?? [];
  rebuildIndex(list);
  repos.value = list;
}

/**
 * The git status letter for one repo-relative path, or "" when clean, ignored, or the repo is unknown. Letters are
 * git-types.ts's vocabulary, the same the git view and file browser show.
 */
export function statusFor(repo: string, relPath: string): string {
  return index.get(`${repo}\u0000${relPath}`) ?? "";
}

/** The status letter for an ABSOLUTE path, or "" when clean/unknown. For
 *  consumers that hold real filesystem paths rather than a repo-relative pair. */
export function statusForPath(absPath: string): string {
  return absIndex.get(normalizeAbs(absPath)) ?? "";
}

/** The worst status letter anywhere BENEATH an absolute directory path, or "".
 *  What lets a collapsed folder admit that something inside it changed. */
export function statusUnder(absDirPath: string): string {
  return dirIndex.get(normalizeAbs(absDirPath)) ?? "";
}

/** Trailing slashes and a bare "/" would miss every key. */
function normalizeAbs(p: string): string {
  return p.length > 1 && p.endsWith("/") ? p.slice(0, -1) : p;
}

/**
 * Subscribe to the repos array. Fires immediately with the current value; the first subscriber starts the store's
 * one read.
 */
export function onGitStatusChange(fn: () => void): () => void {
  const off = subscribe(repos, fn);
  startOnFirstSubscriber();
  return off;
}

/** The current repos array, for consumers deriving aggregates (the badge). */
export function currentRepos(): readonly GitRepoStatus[] {
  return repos.value;
}

/** @internal Test seam: inject a repos array without a fetch. */
export function _setReposForTest(list: readonly GitRepoStatus[]): void {
  rebuildIndex(list);
  repos.value = list;
}
